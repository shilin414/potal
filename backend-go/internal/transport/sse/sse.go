// Package sse implements the run event stream gateway:
//
//  1. Subscribe the Redis pub/sub channel for live events.
//  2. Replay persisted RunEvents from the client's DURABLE CURSOR (not
//     necessarily from 0), page by page, stopping at the first terminal
//     event.
//  3. Keepalive comment after 15s of silence.
//  4. Close as soon as a terminal event is observed — in the replay, in
//     the live loop, or synthesized from an already-settled run status.
//
// A terminal event is a HARD boundary (第七轮 P1): replay stops at the
// first one and the handler returns regardless of what the caller's
// `run.Status` snapshot said. The snapshot was read before the stream
// opened, so it can still say `running` while the replay already carries
// the terminal event — trusting the snapshot kept the connection open
// (keepalive forever) after the client had been told the run ended.
//
// Resumability (第九轮 P1-1/P1-2): every DURABLE frame carries an SSE `id:`
// equal to its run sequence, so a reconnect can ask to resume from the last
// event it actually processed:
//
//	GET /v2/runs/{id}/stream?after=123
//	Last-Event-ID: 123            (set automatically by EventSource)
//
// Precedence is query > Last-Event-ID > 0: an explicit cursor is a deliberate
// client decision, while Last-Event-ID is whatever the browser last
// remembered.
//
// A TRANSIENT frame (sequence 0 — content.delta is fanned out live without
// being persisted) must NOT write an `id:`. Sequence 0 is a sentinel, not a
// position: writing it would make the browser's next Last-Event-ID "0" and
// silently restart every replay from the beginning, re-rendering the whole
// answer.
//
// Frame format (validated frontend contract):
//
//	event: run.event\n
//	id: 123\n                       (durable frames only)
//	data: {"run_id","sequence","event_type","payload"[,"created_at"]}\n\n
package sse

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/platform/redisx"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
)

const (
	defaultKeepalive = 15 * time.Second
)

// Gateway serves GET /api/v2/runs/{id}/stream for a single run.
type Gateway struct {
	Runs      *execution.Service
	Redis     *redisx.Client
	Keepalive time.Duration // defaults to 15s
	Metrics   *telemetry.Metrics
}

// ResumeCursor resolves the client's durable cursor. query `after` wins over
// the `Last-Event-ID` header (see the package comment); an unparsable value
// degrades to 0 rather than failing the stream — the worst case is a full
// replay, which is exactly the pre-第九轮 behaviour.
func ResumeCursor(r *http.Request) uint64 {
	if raw := r.URL.Query().Get("after"); raw != "" {
		if v, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64); err == nil {
			return v
		}
	}
	if raw := r.Header.Get("Last-Event-ID"); raw != "" {
		if v, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64); err == nil {
			return v
		}
	}
	return 0
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
	// Flush the response headers immediately. Go buffers them until the first
	// write or flush, so without this a stream that has nothing to send yet
	// (a fresh run with no persisted events) leaves the client waiting for the
	// response — the connection is only observably established when the first
	// frame happens to arrive.
	flusher.Flush()

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
		// The id line is what makes a reconnect able to resume instead of
		// replaying the whole run; it is written ONLY for durable frames.
		var frame bytes.Buffer
		frame.WriteString("event: run.event\n")
		if sequence > 0 {
			fmt.Fprintf(&frame, "id: %d\n", sequence)
		}
		frame.WriteString("data: ")
		frame.Write(raw)
		frame.WriteString("\n\n")
		if _, err := w.Write(frame.Bytes()); err != nil {
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

	after := ResumeCursor(r)

	var msgCh <-chan *goredis.Message

	// Phase order matters (§35):
	//   1. SUBSCRIBE first — live events buffer in the pub/sub channel
	//      from this point; nothing published later can be missed.
	//   2. Replay persisted events from MySQL (snapshot after subscription).
	//   3. Drain live frames, skipping sequences already replayed
	//      (overlap between snapshot and subscription is deduplicated).
	// lastReplayed starts at the client's cursor, not at 0: a live frame at
	// or below the cursor was already delivered to this client before the
	// reconnect, so re-sending it would duplicate content the client has
	// rendered. Replay above only ever raises it.
	lastReplayed := after
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

	// Phase 2: MySQL replay, PAGE BY PAGE from the durable cursor.
	//
	// A single unbounded read used to load a run's entire history into
	// memory before writing the first frame (第九轮 P1-3): a run with 100k
	// events paid for all of them up front, and the client waited. Paging
	// bounds both memory and time-to-first-frame, and the terminal-event
	// hard boundary stops the walk as soon as the run's log ends.
	replayedTerminal := false
	cursor := after
	for {
		page, err := g.Runs.ListEventPage(ctx, run.ID, cursor, execution.DefaultEventPageSize)
		if err != nil {
			return
		}
		for _, ev := range page.Items {
			if !writeFrame(ev.Sequence, ev.EventType, ev.Payload, false) {
				return
			}
			cursor = ev.Sequence
			lastReplayed = ev.Sequence
			if g.Metrics != nil {
				g.Metrics.SSEReplayEventsTotal.Inc()
			}
			if execution.IsTerminalEventName(ev.EventType) {
				// A terminal event ends the run's lifecycle, so it is the
				// LAST frame this stream may carry — even if the table
				// holds rows after it (dirty history: a pre-0020
				// failed-then-completed shape). Stopping here also makes
				// the terminal fact the replay itself observed, instead
				// of the stale run.Status snapshot below.
				replayedTerminal = true
				break
			}
		}
		if replayedTerminal || !page.HasMore {
			break
		}
	}
	if replayedTerminal {
		// The stream has already delivered the end of the run. Returning
		// here is what makes the contract "server closes after the
		// terminal event" hold: the live loop would otherwise wait on
		// Redis forever, and the terminal message it is waiting for has
		// already been deduplicated by the `sequence <= lastReplayed`
		// guard (第七轮 P1 — normal outcome race, no bad data required).
		return
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
			eventType, ok := syntheticTerminalEventName(run.Status)
			if !ok {
				// A settled status without a terminal event name is a
				// status nobody defined: never fabricate a frame (a
				// default of run.completed would report an unknown
				// outcome as a success).
				return
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

// syntheticTerminalEventName maps a SETTLED run status to the terminal
// frame the gateway synthesizes when the run has no persisted terminal
// event (修复计划 §19 fallback; legacy rows predating the atomic
// finalize transaction).
//
// cancelled must map to run.cancelled — mapping it to run.completed, as
// the previous inline `if failed||interrupted` did, resurrects exactly
// the "cancelled ≠ success" bug the execution closure removed: the
// frontend renders a user-cancelled run as a successful one.
//
// `interrupted` is the pre-closure terminal ALIAS of `failed` (第五轮
// P2-1) and therefore still maps to run.failed for rows migration 0018
// has not normalized yet.
//
// The bool is false for any non-settled or unknown status: the caller
// must NOT fabricate a terminal frame then, because a zero-value event
// name would either be ignored by the client (stream never closes) or,
// with a naive default, reported as a success nobody observed.
func syntheticTerminalEventName(status string) (string, bool) {
	switch status {
	case execution.StatusSucceeded:
		return execution.EventRunCompleted, true
	case execution.StatusCancelled:
		return execution.EventRunCancelled, true
	case execution.StatusFailed, execution.StatusInterrupted:
		return execution.EventRunFailed, true
	}
	return "", false
}
