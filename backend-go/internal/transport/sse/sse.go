// Package sse implements the run event stream gateway:
//
//  1. Replay every persisted RunEvent from MySQL (sequence 0 → head).
//  2. Subscribe the Redis pub/sub channel for live events.
//  3. Keepalive comment after 15s of silence.
//  4. Close after replay when the run is already terminal, or after the
//     first terminal live event.
//
// Frame format (validated frontend contract):
//
//	event: run.event\n
//	data: {"run_id","sequence","event_type","payload"[,"created_at"]}\n\n
package sse

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/platform/redisx"
)

const (
	defaultKeepalive = 15 * time.Second
)

// Gateway serves GET /api/v2/runs/{id}/stream for a single run.
type Gateway struct {
	Runs      *execution.Service
	Redis     *redisx.Client
	Keepalive time.Duration // defaults to 15s
}

// Stream writes the SSE response. The caller has already authenticated the
// user and verified run ownership.
func (g *Gateway) Stream(w http.ResponseWriter, r *http.Request, run *execution.Run) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
	h.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	ctx := r.Context()

	writeFrame := func(sequence uint64, eventType string, payload map[string]any, withCreated bool) bool {
		data := map[string]any{
			"run_id":     run.ID.String(),
			"sequence":   sequence,
			"event_type": eventType,
			"payload":    payload,
		}
		if withCreated {
			data["created_at"] = time.Now().UTC().Format(time.RFC3339Nano)
		}
		raw, err := json.Marshal(data)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "event: run.event\ndata: %s\n\n", raw); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	writeComment := func() bool {
		if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	var msgCh <-chan *goredis.Message

	// Phase order matters (§35):
	//   1. SUBSCRIBE first — live events buffer in the pub/sub channel
	//      from this point; nothing published later can be missed.
	//   2. Replay persisted events from MySQL (snapshot after subscription).
	//   3. Drain live frames, skipping sequences already replayed
	//      (overlap between snapshot and subscription is deduplicated).
	var lastReplayed uint64
	if g.Redis != nil && !execution.IsSettled(run.Status) {
		var pubsub *goredis.PubSub
		pubsub = g.Redis.Subscribe(ctx, g.Redis.RunEventsChannel(run.ID.String()))
		if _, err := pubsub.Receive(ctx); err != nil { // wait for confirmation
			_ = pubsub.Close()
			pubsub = nil
		}
		defer func() {
			if pubsub != nil {
				_ = pubsub.Close()
			}
		}()
		if pubsub != nil {
			msgCh = pubsub.Channel(
				goredis.WithChannelSize(4096),
				goredis.WithChannelSendTimeout(5*time.Second),
			)
		}
	}

	// Phase 2: MySQL replay.
	events, err := g.Runs.ListEventsAfter(ctx, run.ID, 0)
	if err != nil {
		return
	}
	replayedTerminal := false
	for _, ev := range events {
		if !writeFrame(ev.Sequence, ev.EventType, ev.Payload, false) {
			return
		}
		lastReplayed = ev.Sequence
		if execution.IsTerminalEventName(ev.EventType) {
			replayedTerminal = true
		}
	}
	if execution.IsSettled(run.Status) {
		// Belt-and-suspenders (修复计划 §19 fallback): the finalize
		// transaction guarantees terminal CAS + terminal event together;
		// if a LEGACY row predating that transaction is terminal without
		// a terminal event, synthesize the frame from the run status so
		// reconnecting clients still terminate their stream instead of
		// hammering a stream that closes immediately.
		if !replayedTerminal {
			synthetic := map[string]any{"status": run.Status}
			if t, ok := run.Output["text"].(string); ok && t != "" {
				synthetic["text"] = t
			}
			if run.ErrorCode != "" {
				synthetic["error_code"] = run.ErrorCode
			}
			if run.ErrorMessage != "" {
				synthetic["error_message"] = run.ErrorMessage
			}
			eventType := execution.EventRunCompleted
			if run.Status == execution.StatusFailed || run.Status == execution.StatusInterrupted {
				eventType = execution.EventRunFailed
			}
			writeFrame(0, eventType, synthetic, true)
		}
		return
	}
	if msgCh == nil {
		return
	}

	keepaliveAfter := g.Keepalive
	if keepaliveAfter <= 0 {
		keepaliveAfter = defaultKeepalive
	}
	keepalive := time.NewTicker(keepaliveAfter)
	defer keepalive.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-msgCh:
			if !ok {
				return
			}
			var frame struct {
				Sequence  uint64         `json:"sequence"`
				EventType string         `json:"event_type"`
				Payload   map[string]any `json:"payload"`
			}
			if err := json.Unmarshal([]byte(msg.Payload), &frame); err != nil {
				continue
			}
			// Skip events already covered by the replay snapshot.
			if frame.Sequence != 0 && frame.Sequence <= lastReplayed {
				continue
			}
			if !writeFrame(frame.Sequence, frame.EventType, frame.Payload, true) {
				return
			}
			if execution.IsTerminalEventName(frame.EventType) {
				return
			}
		case <-keepalive.C:
			if !writeComment() {
				return
			}
		}
	}
}
