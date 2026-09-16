// 第九轮 SSE 可恢复性验证 (STUDIO_TEST_DB=1, live half needs STUDIO_TEST_REDIS=1).
//
// These tests read the RAW event-stream text rather than the parsed JSON,
// because the resume contract lives in the framing:
//
//	durable frame  →  id: <run sequence>      (the client's cursor)
//	transient frame→  NO id line              (sequence 0 is a sentinel)
//
// A transpient frame that wrote `id: 0` would make the browser's next
// Last-Event-ID "0" and silently restart every replay from the beginning,
// re-rendering the whole answer — so its absence is asserted, not assumed.
package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
	"github.com/creation-agent-studio/backend-go/internal/platform/redisx"
	"github.com/creation-agent-studio/backend-go/internal/transport/sse"
)

// rawSSEFrame is one parsed SSE frame, keeping the id line separate.
type rawSSEFrame struct {
	ID     string
	Data   string
	Fields []string
}

func (f rawSSEFrame) sequence(t *testing.T) uint64 {
	t.Helper()
	var body struct {
		Sequence uint64 `json:"sequence"`
	}
	if err := json.Unmarshal([]byte(f.Data), &body); err != nil {
		t.Fatalf("frame data is not JSON: %q", f.Data)
	}
	return body.Sequence
}

// parseSSEFrames groups raw lines into frames at each blank line.
func parseSSEFrames(lines []string) []rawSSEFrame {
	out := make([]rawSSEFrame, 0, len(lines))
	var cur *rawSSEFrame
	for _, line := range lines {
		trimmed := strings.TrimRight(line, "\r")
		if trimmed == "" {
			if cur != nil {
				out = append(out, *cur)
				cur = nil
			}
			continue
		}
		if strings.HasPrefix(trimmed, ":") {
			continue // keepalive comment
		}
		if cur == nil {
			cur = &rawSSEFrame{}
		}
		cur.Fields = append(cur.Fields, trimmed)
		switch {
		case strings.HasPrefix(trimmed, "id:"):
			cur.ID = strings.TrimSpace(strings.TrimPrefix(trimmed, "id:"))
		case strings.HasPrefix(trimmed, "data:"):
			cur.Data = strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out
}

// rawStream issues one streaming request and returns the raw lines. The
// `publish` hook (optional) runs concurrently shortly after the connection is
// established, so the live half of the protocol can be exercised; the read
// stops at EOF or after a short idle window.
func rawStream(t *testing.T, svc *execution.Service, rdb *redisx.Client, runID ids.ID,
	query string, headers map[string]string, publish func(ctx context.Context)) ([]rawSSEFrame, bool) {
	t.Helper()
	run, err := svc.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("load run: %v", err)
	}
	// A long keepalive keeps the transport quiet; the framing assertions are
	// about ids, not about liveness.
	//
	// Batch 4: the gateway no longer holds a Redis client — it reaches Redis
	// through the per-run hub. Each test server owns one hub manager and
	// closes it with the test (LIFO cleanup runs before the shared Redis/DB
	// teardown).
	hub := sse.NewHubManager(context.Background(), rdb, nil, sse.HubOptions{})
	t.Cleanup(hub.Close)
	gw := &sse.Gateway{Runs: svc, Hub: hub, Keepalive: time.Hour}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gw.Stream(w, r, run)
	}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	url := ts.URL
	if query != "" {
		url += "?" + query
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("stream request: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stream request: %v", err)
	}
	defer resp.Body.Close()

	if publish != nil {
		go func() {
			// The gateway subscribes to Redis before replaying, so a short
			// delay is enough for the subscription to be live.
			time.Sleep(300 * time.Millisecond)
			publish(context.Background())
		}()
	}

	lineCh := make(chan string, 256)
	go func() {
		defer close(lineCh)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			lineCh <- sc.Text()
		}
	}()

	lines := make([]string, 0, 32)
	closed := false
	for !closed {
		select {
		case l, ok := <-lineCh:
			if !ok {
				closed = true
				break
			}
			lines = append(lines, l)
		case <-time.After(2 * time.Second):
			return parseSSEFrames(lines), false
		}
	}
	return parseSSEFrames(lines), true
}

// seedSettledRunWithEvents creates a terminal run carrying three durable
// events (the last one terminal), so a replay always ends by itself.
func seedSettledRunWithEvents(t *testing.T, svc *execution.Service) ids.ID {
	t.Helper()
	ctx := context.Background()
	runID := seedRun(t, svc, "feishu_aily")
	deleteRunFixture(t, svc, runID)
	if _, err := svc.Querier().CASFinishRun(ctx, dbFinishRunParams(runID.Bytes(), "succeeded", `{"text":"final"}`)); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	for _, ev := range []struct{ typ, text string }{
		{execution.EventContentStarted, "started"},
		{execution.EventContentChunk, "chunk"},
		{execution.EventRunCompleted, "final"},
	} {
		if err := svc.AppendEvent(ctx, runID, ev.typ, map[string]any{"text": ev.text}); err != nil {
			t.Fatalf("append %s: %v", ev.typ, err)
		}
	}
	return runID
}

func frameSequences(frames []rawSSEFrame, t *testing.T) []uint64 {
	t.Helper()
	out := make([]uint64, 0, len(frames))
	for _, f := range frames {
		out = append(out, f.sequence(t))
	}
	return out
}

// TestSSEDurableFramesCarryResumeIds: every durable frame must publish its run
// sequence as the SSE id, and nothing may publish id 0.
func TestSSEDurableFramesCarryResumeIds(t *testing.T) {
	svc, _ := testEnv(t)
	runID := seedSettledRunWithEvents(t, svc)

	frames, closed := rawStream(t, svc, nil, runID, "", nil, nil)
	if !closed {
		t.Fatal("a settled run's stream must close after the replay")
	}
	if len(frames) != 3 {
		t.Fatalf("frames = %d, want 3 (%v)", len(frames), frameSequences(frames, t))
	}
	for i, f := range frames {
		want := i + 1
		if f.ID != strconv.Itoa(want) {
			t.Fatalf("frame %d: id = %q, want %q — a durable frame without its "+
				"sequence cannot be resumed from", i, f.ID, strconv.Itoa(want))
		}
		if !strings.HasPrefix(f.Fields[0], "event: run.event") {
			t.Fatalf("frame %d: first field = %q, want the event name", i, f.Fields[0])
		}
	}
}

// TestSSEResumesFromQueryCursor: `?after=N` must replay ONLY what follows N.
func TestSSEResumesFromQueryCursor(t *testing.T) {
	svc, _ := testEnv(t)
	runID := seedSettledRunWithEvents(t, svc)

	frames, closed := rawStream(t, svc, nil, runID, "after=2", nil, nil)
	if !closed {
		t.Fatal("stream did not close")
	}
	if got := frameSequences(frames, t); len(got) != 1 || got[0] != 3 {
		t.Fatalf("replay from cursor 2 returned %v, want [3] — a cursor exists so a "+
			"reconnect never re-renders what the client already has", got)
	}
}

// TestSSEResumesFromLastEventIDHeader: the same contract through the header
// EventSource replays automatically on reconnect.
func TestSSEResumesFromLastEventIDHeader(t *testing.T) {
	svc, _ := testEnv(t)
	runID := seedSettledRunWithEvents(t, svc)

	frames, closed := rawStream(t, svc, nil, runID, "", map[string]string{"Last-Event-ID": "2"}, nil)
	if !closed {
		t.Fatal("stream did not close")
	}
	if got := frameSequences(frames, t); len(got) != 1 || got[0] != 3 {
		t.Fatalf("replay from Last-Event-ID 2 returned %v, want [3]", got)
	}
}

// TestSSEQueryCursorOutranksLastEventID pins the documented precedence. The
// two sources disagree, so the result identifies the winner: query 2 → [3],
// header 1 → [2 3].
func TestSSEQueryCursorOutranksLastEventID(t *testing.T) {
	svc, _ := testEnv(t)
	runID := seedSettledRunWithEvents(t, svc)

	frames, closed := rawStream(t, svc, nil, runID, "after=2", map[string]string{"Last-Event-ID": "1"}, nil)
	if !closed {
		t.Fatal("stream did not close")
	}
	if got := frameSequences(frames, t); len(got) != 1 || got[0] != 3 {
		t.Fatalf("query=2 + Last-Event-ID=1 returned %v, want [3] (the explicit cursor wins)", got)
	}
}

// TestSSETransientFrameCarriesNoResumeId: a live transient (sequence 0) must be
// forwarded WITHOUT an id line. Writing `id: 0` would reset the client's
// cursor and turn the next reconnect into a full replay.
func TestSSETransientFrameCarriesNoResumeId(t *testing.T) {
	svc, rdb := testEnv(t)
	if rdb == nil {
		t.Skip("needs STUDIO_TEST_REDIS=1")
	}
	ctx := context.Background()
	runID := seedRun(t, svc, "feishu_aily")
	deleteRunFixture(t, svc, runID)
	if _, won, err := svc.ClaimRun(ctx, runID, "sse-cursor", time.Minute); err != nil || !won {
		t.Fatalf("claim: won=%v err=%v", won, err)
	}

	channel := rdb.RunEventsChannel(runID.String())
	publish := func(pctx context.Context) {
		if err := rdb.Publish(pctx, channel, mustJSON(map[string]any{
			"run_id": runID.String(), "sequence": 0,
			"event_type": execution.EventContentDelta,
			"payload":    map[string]any{"text": "live-delta"},
		})).Err(); err != nil {
			t.Logf("publish transient: %v", err)
			return
		}
		_ = rdb.Publish(pctx, channel, mustJSON(map[string]any{
			"run_id": runID.String(), "sequence": 1,
			"event_type": execution.EventRunCompleted,
			"payload":    map[string]any{"text": "done"},
			"created_at": time.Now().UTC().Format(time.RFC3339Nano),
		})).Err()
	}

	// 第九轮补丁 3.3-B: transient content.delta is a protocol-2 frame set, so
	// this test must DECLARE the capability — it asserts on the `id:` line of a
	// transient frame, and a legacy client no longer receives one at all
	// (TestSSELegacyClientReceivesNoTransientDelta pins that side).
	frames, closed := rawStream(t, svc, rdb, runID, "stream_protocol=2", nil, publish)
	if !closed {
		t.Fatal("the stream must close after the live terminal frame")
	}

	var transient *rawSSEFrame
	var terminal *rawSSEFrame
	for i := range frames {
		f := frames[i]
		if f.sequence(t) == 0 && strings.Contains(f.Data, "live-delta") {
			transient = &f
		}
		if strings.Contains(f.Data, `"run.completed"`) {
			terminal = &f
		}
	}
	if transient == nil {
		t.Fatalf("the transient frame was not forwarded: %v", frames)
	}
	if transient.ID != "" {
		t.Fatalf("transient frame carries id %q: sequence 0 is a SENTINEL, and writing it "+
			"makes the browser's next Last-Event-ID 0, restarting the replay from the beginning",
			transient.ID)
	}
	if terminal == nil {
		t.Fatal("the durable terminal frame was not forwarded")
	}
	if terminal.ID != "1" {
		t.Fatalf("durable terminal frame id = %q, want 1", terminal.ID)
	}
}
