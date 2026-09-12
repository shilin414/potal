package integration

import (
	"bufio"
	"context"
	"encoding/json"

	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
	"github.com/creation-agent-studio/backend-go/internal/platform/redisx"
	"github.com/creation-agent-studio/backend-go/internal/transport/sse"
)

type sseFrame struct {
	Sequence  uint64         `json:"sequence"`
	EventType string         `json:"event_type"`
	Payload   map[string]any `json:"payload"`
	CreatedAt string         `json:"created_at"`
}

func appendTestEvent(t *testing.T, svc *execution.Service, runID ids.ID, seq uint64, eventType string, text string) {
	t.Helper()
	if err := svc.AppendEvent(context.Background(), runID, eventType, map[string]any{"text": text}); err != nil {
		t.Fatalf("append event: %v", err)
	}
}

// TestSSETerminalRunReplayOnly: a run already terminal replays all
// persisted events and closes — no live subscription, no hang.
func TestSSETerminalRunReplayOnly(t *testing.T) {
	svc, _ := testEnv(t)
	runID := seedRun(t, svc, "feishu_aily")
	ctx := context.Background()
	if _, err := svc.Querier().CASFinishRun(ctx, dbFinishRunParams(runID.Bytes(), "succeeded", `{"text":"final"}`)); err != nil {
		t.Fatal(err)
	}
	appendTestEvent(t, svc, runID, 1, execution.EventContentDelta, "delta-1")
	appendTestEvent(t, svc, runID, 2, execution.EventRunCompleted, "final")

	frames := readSSE(t, svc, runID)
	if len(frames) != 2 {
		t.Fatalf("got %d frames, want 2 (replay only)", len(frames))
	}
	if frames[0].EventType != "content.delta" || frames[1].EventType != "run.completed" {
		t.Fatalf("order wrong: %s, %s", frames[0].EventType, frames[1].EventType)
	}
	if frames[1].Payload["text"] != "final" {
		t.Fatalf("terminal payload text missing: %v", frames[1].Payload)
	}
}

// TestSSEReplayThenLiveThenClose: replay from TiDB, live frames via Redis
// pub/sub, close on the first terminal event.
func TestSSEReplayThenLiveThenClose(t *testing.T) {
	svc, rdb := testEnv(t)
	if rdb == nil {
		t.Skip("needs STUDIO_TEST_REDIS=1")
	}
	ctx := context.Background()
	runID := seedRun(t, svc, "feishu_aily")
	// Queued (non-terminal) run: claim so the replay happens, stream stays open.
	if won, err := svc.CASClaim(ctx, runID); err != nil || !won {
		t.Fatalf("claim: %v %v", won, err)
	}
	appendTestEvent(t, svc, runID, 1, execution.EventContentDelta, "replayed-delta")

	frames := make(chan sseFrame, 16)
	ts := startSSEServer(t, svc, rdb, runID)
	defer ts.Close()

	// Establish the stream.
	reqCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(reqCtx, http.MethodGet, ts.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stream request: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}
	go func() {
		defer close(frames)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			var f sseFrame
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data:")), &f) == nil {
				frames <- f
			}
		}
	}()

	// Phase 1: the replayed frame must arrive (from TiDB, no created_at).
	var replay *sseFrame
	deadline := time.Now().Add(5 * time.Second)
	for replay == nil && time.Now().Before(deadline) {
		select {
		case f := <-frames:
			if f.Sequence == 1 && f.Payload["text"] == "replayed-delta" {
				cp := f
				replay = &cp
			}
		case <-time.After(200 * time.Millisecond):
		}
	}
	if replay == nil {
		t.Fatal("replay frame not received")
	}
	if replay.CreatedAt != "" {
		t.Fatalf("replay frame must omit created_at, got %q", replay.CreatedAt)
	}

	// Phase 2: publish live events; the gateway must forward them and
	// close after the terminal one.
	probe := rdb.Subscribe(ctx, rdb.RunEventsChannel(runID.String()))
	defer probe.Close()
	if _, err := probe.Receive(ctx); err != nil {
		t.Fatalf("probe subscribe: %v", err)
	}
	probeGot := make(chan string, 8)
	go func() {
		for msg := range probe.Channel() {
			probeGot <- msg.Payload
		}
	}()
	if err := rdb.Publish(ctx, rdb.RunEventsChannel(runID.String()),
		mustJSON(map[string]any{
			"run_id": runID.String(), "sequence": 2,
			"event_type": "content.delta", "payload": map[string]any{"text": "live-delta"},
			"created_at": time.Now().UTC().Format(time.RFC3339Nano),
		})).Err(); err != nil {
		t.Fatalf("publish live: %v", err)
	}
	if err := rdb.Publish(ctx, rdb.RunEventsChannel(runID.String()),
		mustJSON(map[string]any{
			"run_id": runID.String(), "sequence": 3,
			"event_type": "run.completed", "payload": map[string]any{"text": "done"},
			"created_at": time.Now().UTC().Format(time.RFC3339Nano),
		})).Err(); err != nil {
		t.Fatalf("publish terminal: %v", err)
	}

	var live, terminal *sseFrame
	deadline = time.Now().Add(10 * time.Second)
	for (live == nil || terminal == nil) && time.Now().Before(deadline) {
		select {
		case f, ok := <-frames:
			if !ok {
				goto drained
			}
			if f.Payload["text"] == "live-delta" {
				cp := f
				live = &cp
			}
			if f.EventType == "run.completed" {
				cp := f
				terminal = &cp
			}
		case <-time.After(200 * time.Millisecond):
		}
	}
drained:
	// Probe check: the publishes must have reached the channel itself.
	select {
	case p := <-probeGot:
		t.Logf("probe received: %.60s", p)
	default:
		t.Log("probe received nothing (publish lost at redis level?)")
	}
	if live == nil {
		t.Fatal("live delta missing")
	}
	if live.CreatedAt == "" {
		t.Fatal("live frame must carry created_at")
	}
	if terminal == nil {
		t.Fatal("terminal event missing")
	}
	// The connection must close after the terminal event (reader EOF).
	deadline = time.Now().Add(3 * time.Second)
	select {
	case _, ok := <-frames:
		if ok {
			// Extra frames tolerated, but the server should close soon.
			t.Log("extra frame after terminal")
		}
	case <-time.After(200 * time.Millisecond):
	}
	if _, ok := <-frames; ok {
		t.Fatal("stream did not close after terminal event")
	}
}

func startSSEServer(t *testing.T, svc *execution.Service, rdb *redisx.Client, runID ids.ID) *httptest.Server {
	t.Helper()
	gw := &sse.Gateway{Runs: svc, Redis: rdb, Keepalive: 500 * time.Millisecond}
	run, err := svc.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	// Redis is injected per test; wrap Stream via closure so the test can
	// pass its own client.
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gw.Stream(w, r, run)
	}))
}

func readSSE(t *testing.T, svc *execution.Service, runID ids.ID) []sseFrame {
	t.Helper()
	gw := &sse.Gateway{Runs: svc}
	run, err := svc.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gw.Stream(w, r, run)
	}))
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stream request: %v", err)
	}
	defer resp.Body.Close()
	var out []sseFrame
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var f sseFrame
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data:")), &f) == nil {
			out = append(out, f)
		}
	}
	return out
}

func mustJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

var _ = goredis.Nil
