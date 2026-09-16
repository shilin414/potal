// 第十轮 Batch 4 端到端验证：进程内 SSE Hub（单 Run 单 upstream + Subscriber fan-out）
// （执行报告 §53）。
//
// These tests exercise the properties that only a REAL Redis + MySQL can show,
// on top of the unit matrix in internal/transport/sse:
//
//	Case A  two HTTP streams of one run share ONE hub and one upstream
//	Case B  a v1 and a v2 subscriber on the same hub keep their own capability
//	Case C  a disconnect + reconnect with `after=<cursor>` loses and duplicates
//	        nothing
//
// The hub's own accounting (one upstream per run, bounded queues, protocol
// filtering, terminal boundary, idle lifecycle) is asserted in the package
// tests, where the Redis subscription can be COUNTED rather than inferred.
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

// startHubSSEServer builds a gateway with a hub of its own and hands back the
// manager, so a test can assert the FAN-OUT accounting of the shared hub (one
// hub, one upstream, N subscribers) rather than only the frames.
func startHubSSEServer(t *testing.T, svc *execution.Service, rdb *redisx.Client, run *execution.Run) (*httptest.Server, *sse.HubManager) {
	t.Helper()
	hub := sse.NewHubManager(context.Background(), rdb, nil, sse.HubOptions{})
	t.Cleanup(hub.Close)
	gw := &sse.Gateway{Runs: svc, Hub: hub, Keepalive: time.Hour}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gw.Stream(w, r, run)
	})), hub
}

// openStreamAt opens one SSE connection to an EXISTING server, so several
// streams can share the one hub behind it.
func openStreamAt(t *testing.T, url string) *sseStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		cancel()
		t.Fatalf("stream request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("stream request: %v", err)
	}
	s := &sseStream{frames: make(chan sseFrame, 64), closed: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(s.closed)
		defer close(s.frames)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			var f sseFrame
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data:")), &f) == nil {
				s.frames <- f
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		_ = resp.Body.Close()
	})
	return s
}

// runURL builds the stream URL of a hub-backed server.
func runURL(ts *httptest.Server, runID ids.ID, query string) string {
	url := ts.URL + "/v2/runs/" + runID.String() + "/stream"
	if query != "" {
		url += "?" + query
	}
	return url
}

// publishTerminal pushes the durable terminal frame of a run.
func publishTerminal(t *testing.T, rdb *redisx.Client, runID ids.ID, seq uint64) {
	t.Helper()
	publishLiveFrame(t, rdb, runID, seq, execution.EventRunCompleted, map[string]any{"status": execution.StatusSucceeded})
}

// contentCursor is the durable cursor a real client would resume from: the
// highest sequence of the run's own CONTENT frames that this connection has
// rendered.
//
// The subscription marker published by syncStream is deliberately excluded. It
// is a liveness PROBE, not run history: it hijacks one out-of-band durable
// sequence (9000) purely to prove the gateway is subscribed, which a real
// client would never receive because a real run's sequences are allocated
// monotonically and interleave with its content. Treating the probe as a
// position would make the reconnect below resume far past the frames under
// test.
//
// Transient frames carry sequence 0 and are excluded by construction, which is
// exactly what the SSE `id:` contract promises.
func (s *sseStream) contentCursor() uint64 {
	var max uint64
	for _, f := range s.all {
		if f.EventType == execution.EventContentChunk && f.Sequence > max {
			max = f.Sequence
		}
	}
	return max
}

// ── Case A ──────────────────────────────────────────────────────────────

// TestSSEHubTwoStreamsShareOneUpstream is AC-1/AC-15 observed end to end: two
// HTTP connections watching the same run must be served by ONE hub with ONE
// Redis subscription, and both must see the same terminal frame.
func TestSSEHubTwoStreamsShareOneUpstream(t *testing.T) {
	svc, rdb, runID := streamRun(t, "feishu_aily")
	run, err := svc.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("load run: %v", err)
	}
	ts, hub := startHubSSEServer(t, svc, rdb, run)

	a := openStreamAt(t, runURL(ts, runID, "stream_protocol=2&after=0"))
	b := openStreamAt(t, runURL(ts, runID, "stream_protocol=2&after=0"))
	syncStream(t, rdb, runID, a)
	syncStream(t, rdb, runID, b)

	// The sharing is asserted while both connections are open.
	if got := hub.HubCount(); got != 1 {
		t.Fatalf("HubCount = %d for one run with two viewers, want 1", got)
	}
	if got := hub.UpstreamCount(); got != 1 {
		t.Fatalf("UpstreamCount = %d, want 1: two viewers must share the Redis subscription", got)
	}
	if got := hub.SubscriberCount(); got != 2 {
		t.Fatalf("SubscriberCount = %d, want 2", got)
	}

	publishTerminal(t, rdb, runID, 9100)

	if !a.await(func(f sseFrame) bool { return f.Sequence == 9100 }, 5*time.Second) {
		t.Fatalf("stream A never received the terminal frame; transcript=%v", frameSummary(a.all))
	}
	if !b.await(func(f sseFrame) bool { return f.Sequence == 9100 }, 5*time.Second) {
		t.Fatalf("stream B never received the terminal frame; transcript=%v", frameSummary(b.all))
	}
	for name, s := range map[string]*sseStream{"A": a, "B": b} {
		if f, ok := s.first(func(f sseFrame) bool { return f.Sequence == 9100 }); !ok ||
			f.EventType != execution.EventRunCompleted {
			t.Fatalf("stream %s terminal frame = %+v, want %s at sequence 9100",
				name, f, execution.EventRunCompleted)
		}
	}

	// A terminal event ends the run for EVERY viewer of the shared hub.
	for name, s := range map[string]*sseStream{"A": a, "B": b} {
		select {
		case <-s.closed:
		case <-time.After(5 * time.Second):
			t.Fatalf("stream %s stayed open after the terminal event", name)
		}
	}
}

// ── Case B ──────────────────────────────────────────────────────────────

// TestSSEHubMixedProtocolsOnOneRun is AC-2/AC-3 end to end: a legacy and a v2
// subscriber, one hub, one Redis subscription, and the capability stays with
// the connection.
func TestSSEHubMixedProtocolsOnOneRun(t *testing.T) {
	svc, rdb, runID := streamRun(t, "feishu_aily")
	run, err := svc.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("load run: %v", err)
	}
	ts, hub := startHubSSEServer(t, svc, rdb, run)

	legacy := openStreamAt(t, runURL(ts, runID, ""))
	v2 := openStreamAt(t, runURL(ts, runID, "stream_protocol=2"))
	syncStream(t, rdb, runID, legacy)
	syncStream(t, rdb, runID, v2)

	// The delta is published BEFORE the chunk, and Redis preserves order — so
	// by the time the chunk is in hand, a delta that was going to be emitted
	// already would be. The text is the shared fixture the delta matcher
	// (isLiveDelta) matches on.
	publishLiveFrame(t, rdb, runID, 0, execution.EventContentDelta,
		map[string]any{"text": protocolLiveDeltaText, "offset": 11})
	publishLiveFrame(t, rdb, runID, 9200, execution.EventContentChunk,
		map[string]any{"text": protocolLiveDeltaText, "offset": 11})

	const answer = protocolLiveDeltaText

	if !legacy.await(func(f sseFrame) bool { return f.Sequence == 9200 }, 5*time.Second) {
		t.Fatal("the legacy subscriber never received the durable chunk")
	}
	if !v2.await(func(f sseFrame) bool { return f.Sequence == 9200 }, 5*time.Second) {
		t.Fatal("the v2 subscriber never received the durable chunk")
	}

	// The DELTA is published BEFORE the chunk, and Redis preserves order — so
	// by the time the chunk is in hand, a delta that was going to be emitted
	// already would be.
	if legacy.saw(isLiveDelta) {
		t.Fatalf("the legacy subscriber received a transient delta from the SHARED hub: "+
			"the protocol must belong to the subscriber, never to the hub. transcript=%v",
			frameSummary(legacy.all))
	}
	if !v2.saw(isLiveDelta) {
		t.Fatalf("the v2 subscriber lost the transient delta it negotiated for. transcript=%v",
			frameSummary(v2.all))
	}

	// Both must end up with the same durable answer.
	for name, s := range map[string]*sseStream{"legacy": legacy, "v2": v2} {
		f, ok := s.first(func(f sseFrame) bool { return f.Sequence == 9200 })
		if !ok {
			t.Fatalf("%s: durable answer missing", name)
		}
		if got, _ := f.Payload["text"].(string); got != answer {
			t.Fatalf("%s: durable answer text = %q, want %q", name, got, answer)
		}
	}

	if got := hub.UpstreamCount(); got != 1 {
		t.Fatalf("UpstreamCount = %d for two viewers of opposite protocols, want 1", got)
	}
}

// ── Case C ──────────────────────────────────────────────────────────────

// TestSSEHubReconnectKeepsCursorSemantics is AC-4 at the HTTP edge: a wire
// drop followed by a reconnect with `after=<last durable sequence>` must
// deliver everything after the cursor exactly once.
//
// This is the scenario the hub's register-before-replay ordering and its
// cache/DB split are built for, and the one a fan-out refactor is most likely
// to break — as duplicates (a replayed event the client already rendered) or as
// a loss (an event published into the window between the two connections).
func TestSSEHubReconnectKeepsCursorSemantics(t *testing.T) {
	svc, rdb, runID := streamRun(t, "feishu_aily")
	run, err := svc.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("load run: %v", err)
	}
	ts, hub := startHubSSEServer(t, svc, rdb, run)

	first := openStreamAt(t, runURL(ts, runID, "after=0"))
	syncStream(t, rdb, runID, first)

	// Durable events (persisted, hence replayable) — the only kind a reconnect
	// may resume from.
	for _, text := range []string{"c1", "c2", "c3"} {
		if err := svc.AppendEvent(context.Background(), runID, execution.EventContentChunk,
			map[string]any{"text": text}); err != nil {
			t.Fatalf("append %s: %v", text, err)
		}
	}
	for _, want := range []string{"c1", "c2", "c3"} {
		text := want
		if !first.await(func(f sseFrame) bool {
			got, _ := f.Payload["text"].(string)
			return f.EventType == execution.EventContentChunk && got == text
		}, 5*time.Second) {
			t.Fatalf("%s never arrived; transcript=%v", text, frameSummary(first.all))
		}
	}
	cursor := first.contentCursor()

	// The browser goes away.
	first.cancel()
	<-first.closed

	// It comes back with the last durable frame it actually rendered.
	second := openStreamAt(t, runURL(ts, runID, "after="+strconv.FormatUint(cursor, 10)))
	if got := hub.HubCount(); got != 1 {
		t.Fatalf("HubCount = %d after a quick reconnect, want 1: the reconnect must reuse the hub", got)
	}

	for _, text := range []string{"c4", "c5"} {
		if err := svc.AppendEvent(context.Background(), runID, execution.EventContentChunk,
			map[string]any{"text": text}); err != nil {
			t.Fatalf("append %s: %v", text, err)
		}
	}
	for _, want := range []string{"c4", "c5"} {
		text := want
		if !second.await(func(f sseFrame) bool {
			got, _ := f.Payload["text"].(string)
			return f.EventType == execution.EventContentChunk && got == text
		}, 5*time.Second) {
			t.Fatalf("%s was lost across the reconnect; transcript=%v", text, frameSummary(second.all))
		}
	}

	// Nothing the client already had may be re-sent: those frames are at or
	// below the cursor.
	for _, text := range []string{"c1", "c2", "c3"} {
		want := text
		if f, ok := second.first(func(f sseFrame) bool {
			got, _ := f.Payload["text"].(string)
			return f.EventType == execution.EventContentChunk && got == want
		}); ok {
			t.Fatalf("the reconnect re-delivered %q at sequence %d, at or below the cursor %d "+
				"(duplicate render)", want, f.Sequence, cursor)
		}
	}

	// And across BOTH connections each chunk appears exactly once.
	seen := map[string]int{}
	for _, f := range append(append([]sseFrame{}, first.all...), second.all...) {
		if f.EventType != execution.EventContentChunk {
			continue
		}
		if text, ok := f.Payload["text"].(string); ok && strings.HasPrefix(text, "c") {
			seen[text]++
		}
	}
	for _, text := range []string{"c1", "c2", "c3", "c4", "c5"} {
		if seen[text] != 1 {
			t.Fatalf("chunk %q was delivered %d times across the reconnect, want exactly 1 (seen=%v)",
				text, seen[text], seen)
		}
	}
}
