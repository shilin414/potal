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

// TestSSEReplayThenLiveThenClose: replay from MySQL, live frames via Redis
// pub/sub, close on the first terminal event.
func TestSSEReplayThenLiveThenClose(t *testing.T) {
	svc, rdb := testEnv(t)
	if rdb == nil {
		t.Skip("needs STUDIO_TEST_REDIS=1")
	}
	ctx := context.Background()
	runID := seedRun(t, svc, "feishu_aily")
	// Queued (non-terminal) run: claim so the replay happens, stream stays open.
	if _, won, err := svc.ClaimRun(ctx, runID, "sse-test", time.Minute); err != nil || !won {
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

	// Phase 1: the replayed frame must arrive (from MySQL, no created_at).
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

// TestSSECancelledRunWithoutTerminalEventReplaysCancelled (第六轮 P1):
// a legacy settled run with NO persisted terminal event must be closed
// with a frame carrying its REAL outcome. The pre-fix fallback mapped
// everything that was not failed/interrupted to run.completed, so a
// user-cancelled run reconnected as a success.
func TestSSECancelledRunWithoutTerminalEventReplaysCancelled(t *testing.T) {
	svc, _ := testEnv(t)
	runID := seedRun(t, svc, "feishu_aily")
	ctx := context.Background()
	// The report's §25 invariant sweeps scan the WHOLE database: a leaked
	// terminal run with no terminal event keeps them red forever.
	deleteRunFixture(t, svc, runID)

	if _, err := svc.Querier().CASFinishRun(ctx, dbFinishRunParams(runID.Bytes(), "cancelled", `{}`)); err != nil {
		t.Fatal(err)
	}
	// Deliberately no run.cancelled event: this is the legacy shape the
	// fallback exists for.
	appendTestEvent(t, svc, runID, 1, execution.EventContentDelta, "partial")

	frames := readSSE(t, svc, runID)
	if len(frames) != 2 {
		t.Fatalf("got %d frames, want 2 (replayed delta + synthetic terminal)", len(frames))
	}
	synthetic := frames[len(frames)-1]
	if synthetic.EventType != execution.EventRunCancelled {
		t.Fatalf("synthetic frame event_type = %q, want %q: a cancelled run must "+
			"never be reported as a success", synthetic.EventType, execution.EventRunCancelled)
	}
	if synthetic.EventType == execution.EventRunCompleted {
		t.Fatal("cancelled run synthesized as run.completed")
	}
	if synthetic.Payload["status"] != execution.StatusCancelled {
		t.Fatalf("synthetic payload status = %v, want %q",
			synthetic.Payload["status"], execution.StatusCancelled)
	}
	if synthetic.CreatedAt == "" {
		t.Error("synthetic frame must carry created_at (it is not a replayed row)")
	}
}

// The other settled statuses keep their own frame (no single default).
func TestSSESyntheticTerminalFramesMatchRunStatus(t *testing.T) {
	cases := []struct {
		status    string
		wantEvent string
	}{
		{"succeeded", execution.EventRunCompleted},
		{"failed", execution.EventRunFailed},
		{"interrupted", execution.EventRunFailed},
		{"cancelled", execution.EventRunCancelled},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			svc, _ := testEnv(t)
			runID := seedRun(t, svc, "feishu_aily")
			deleteRunFixture(t, svc, runID)
			if _, err := svc.Querier().CASFinishRun(context.Background(),
				dbFinishRunParams(runID.Bytes(), tc.status, `{}`)); err != nil {
				t.Fatal(err)
			}
			frames := readSSE(t, svc, runID)
			if len(frames) != 1 {
				t.Fatalf("got %d frames, want 1 (synthetic terminal only)", len(frames))
			}
			if frames[0].EventType != tc.wantEvent {
				t.Fatalf("status %q → %q, want %q", tc.status, frames[0].EventType, tc.wantEvent)
			}
		})
	}
}

// TestSSEStaleRunningSnapshotClosesAfterTerminalReplay (第七轮 P1).
//
// The snapshot the HTTP handler hands to the gateway is read BEFORE the
// run finishes, so it can still say `running` while the replay already
// carries the terminal event. The gateway must close on what the REPLAY
// observed, never on that stale snapshot: otherwise the terminal Redis
// message — which the live loop deduplicates against lastReplayed — never
// reaches a return, and the server holds the connection open with
// keepalives forever. No bad data is required; this is the normal
// terminal outcome race.
func TestSSEStaleRunningSnapshotClosesAfterTerminalReplay(t *testing.T) {
	svc, rdb := testEnv(t)
	if rdb == nil {
		t.Skip("needs STUDIO_TEST_REDIS=1")
	}
	ctx := context.Background()
	convID := seedConversation(t, svc)
	runID := seedRunWithConversation(t, svc, "itest_sse_stale", convID)
	// Cleanup order: LIFO, so the run (FK child) goes before the conversation.
	deleteConversationFixture(t, svc, convID)
	deleteRunFixture(t, svc, runID)

	claimed, _, err := svc.ClaimRun(ctx, runID, "sse-worker", time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	// T1/T2: the stale snapshot — status=running, taken before the run ends.
	stale, err := svc.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if stale.Status != execution.StatusRunning {
		t.Fatalf("snapshot status = %q, want %q (the race needs a stale running snapshot)",
			stale.Status, execution.StatusRunning)
	}

	// T4: the run finishes while the stream is about to replay — terminal
	// CAS + terminal event in ONE transaction.
	if err := svc.FinalizeOwnedRun(ctx, claimed.Run, claimed.Ownership, &execution.FinishInput{
		Status:         execution.StatusSucceeded,
		Output:         map[string]any{"text": "done"},
		ProviderStatus: "Completed",
	}); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	ts := startSSEServerWithRun(t, svc, rdb, stale)
	defer ts.Close()

	reqCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(reqCtx, http.MethodGet, ts.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stream request: %v", err)
	}
	defer resp.Body.Close()

	frames := make(chan sseFrame, 32)
	eof := make(chan struct{})
	go func() {
		defer close(eof)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if !strings.HasPrefix(line, "data:") {
				continue // keepalive comments
			}
			var f sseFrame
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data:")), &f) == nil {
				frames <- f
			}
		}
	}()

	var terminal *sseFrame
	deadline := time.Now().Add(5 * time.Second)
	for terminal == nil && time.Now().Before(deadline) {
		select {
		case f, ok := <-frames:
			if !ok {
				t.Fatal("stream closed before replaying the terminal event")
			}
			if f.EventType == execution.EventRunCompleted {
				cp := f
				terminal = &cp
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	if terminal == nil {
		t.Fatal("terminal replay frame never arrived")
	}

	// The server must close the stream itself, right after the terminal
	// frame. Pre-fix the handler fell through to the live Redis loop, where
	// the 500ms keepalive kept the body open with no further data frames.
	select {
	case <-eof:
	case <-time.After(2 * time.Second):
		t.Fatal("server kept the stream open after replaying a terminal event " +
			"for a run whose status snapshot was stale (第七轮 P1)")
	}
}

// TestSSEReplayStopsAtFirstTerminalEvent (第七轮 P1): a terminal event is a
// HARD boundary. Dirty history can hold rows after it (the pre-0020
// failed-then-completed shape), and replaying those would let a later frame
// rewrite the outcome a client has already rendered — the same class of
// damage 0020 repairs for stored history.
func TestSSEReplayStopsAtFirstTerminalEvent(t *testing.T) {
	svc, _ := testEnv(t)
	runID := seedRun(t, svc, "itest_sse_boundary")
	deleteRunFixture(t, svc, runID)

	appendTestEvent(t, svc, runID, 1, execution.EventContentDelta, "delta-1")
	appendTestEvent(t, svc, runID, 2, execution.EventRunFailed, "boom")
	appendTestEvent(t, svc, runID, 3, execution.EventContentDelta, "after-terminal")

	frames := readSSE(t, svc, runID)
	if len(frames) != 2 {
		t.Fatalf("got %d frames, want 2 (replay must stop at the terminal event): %v",
			len(frames), frameTypes(frames))
	}
	if frames[1].EventType != execution.EventRunFailed {
		t.Fatalf("last replayed frame = %q, want %q (a later frame must never follow a terminal one)",
			frames[1].EventType, execution.EventRunFailed)
	}
	if frames[0].EventType != execution.EventContentDelta {
		t.Fatalf("first replayed frame = %q, want %q", frames[0].EventType, execution.EventContentDelta)
	}
}

func frameTypes(frames []sseFrame) []string {
	out := make([]string, 0, len(frames))
	for _, f := range frames {
		out = append(out, f.EventType)
	}
	return out
}

// deleteConversationFixture removes a seeded conversation and its messages
// at the end of the test. The report's §25 sweeps are DATABASE-WIDE, so a
// leaked conversation is a leak the next verification run has to explain.
func deleteConversationFixture(t *testing.T, svc *execution.Service, convID int64) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = svc.DB.ExecContext(ctx, `DELETE FROM messages WHERE conversation_id = ?`, convID)
		_, _ = svc.DB.ExecContext(ctx, `DELETE FROM conversations WHERE id = ?`, convID)
	})
}

// deleteRunFixture registers removal of a seeded run (and everything that
// points at it) at the end of the test.
//
// The report's §25 verification sweeps are DATABASE-WIDE, so a fixture
// run that is left in a terminal state without a canonical terminal event
// makes a correct migration look broken — and would just as surely hide a
// real regression when one appears.
func deleteRunFixture(t *testing.T, svc *execution.Service, runID ids.ID) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		// FK order: children first, then the run itself.
		for _, stmt := range []string{
			`DELETE FROM run_events WHERE run_id = ?`,
			`DELETE FROM run_leases WHERE run_id = ?`,
			`DELETE FROM provider_execution_slots WHERE run_id = ?`,
			`DELETE FROM runs WHERE id = ?`,
		} {
			_, _ = svc.DB.ExecContext(ctx, stmt, runID.Bytes())
		}
		_, _ = svc.DB.ExecContext(ctx,
			`DELETE FROM outbox_events WHERE aggregate = 'run' AND aggregate_id = ?`,
			runID.Bytes())
	})
}

func startSSEServer(t *testing.T, svc *execution.Service, rdb *redisx.Client, runID ids.ID) *httptest.Server {
	t.Helper()
	run, err := svc.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	return startSSEServerWithRun(t, svc, rdb, run)
}

// startSSEServerWithRun hands the gateway a CALLER-SUPPLIED run snapshot —
// exactly what the HTTP handler does (GetRun, then Stream). A stale
// snapshot is the whole point of 第七轮 P1.
//
// Batch 4: Redis is reached through the per-run hub, so this helper builds one
// hub manager per server and closes it with the test.
func startSSEServerWithRun(t *testing.T, svc *execution.Service, rdb *redisx.Client, run *execution.Run) *httptest.Server {
	t.Helper()
	hub := sse.NewHubManager(context.Background(), rdb, nil, sse.HubOptions{})
	t.Cleanup(hub.Close)
	gw := &sse.Gateway{Runs: svc, Hub: hub, Keepalive: 500 * time.Millisecond}
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
