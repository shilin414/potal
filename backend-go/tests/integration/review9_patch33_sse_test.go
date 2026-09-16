// 第九轮补丁 3.3-B 端到端验证：SSE stream protocol capability negotiation
// （复审报告 §三十）。
//
// The wire contract under test:
//
//	stream_protocol >= 2   transient content.delta + durable content.chunk
//	absent / < 2           durable content.chunk only
//
// A legacy client has no byte offset, so it can only append — and the gateway
// legitimately observes a durable chunk BEFORE the buffered transient delta for
// the same bytes. Suppressing the transient delta is what makes mixed-version
// deployment safe without teaching the gateway a third copy of the range
// reconciliation algorithm.
//
//	TestSSELegacyClientReceivesNoTransientDelta     (legacy)
//	TestSSEProtocol2ClientReceivesOffsets           (v2)
//	TestSSECursorSemanticsIgnoreStreamProtocol      (cursor)
//	TestSSELegacyClientClosesOnTerminalEvent        (terminal)
package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
	"github.com/creation-agent-studio/backend-go/internal/platform/redisx"
)

const protocolLiveDeltaText = "live-transient"

// sseStream is a live SSE connection plus everything it has delivered so far.
type sseStream struct {
	frames chan sseFrame
	closed chan struct{}
	all    []sseFrame
	cancel context.CancelFunc
}

// openSSEStream opens a running run's stream with the given query string.
//
// The reader half lives in openStreamAt (review10_sse_hub_test.go) so the
// single-hub tests can open several streams against ONE server without a second
// copy of the frame parser.
func openSSEStream(t *testing.T, svc *execution.Service, rdb *redisx.Client, runID ids.ID, query string) *sseStream {
	t.Helper()
	ts := startSSEServer(t, svc, rdb, runID)
	t.Cleanup(ts.Close)
	return openStreamAt(t, runURL(ts, runID, query))
}

// await reads until a frame matches, accumulating everything it passed. A false
// return means the deadline expired (the frame never arrived).
func (s *sseStream) await(match func(sseFrame) bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case f, ok := <-s.frames:
			if !ok {
				return false
			}
			s.all = append(s.all, f)
			if match(f) {
				return true
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	return false
}

// saw reports whether an already-delivered frame matches.
func (s *sseStream) saw(match func(sseFrame) bool) bool {
	for _, f := range s.all {
		if match(f) {
			return true
		}
	}
	return false
}

// first returns the first delivered frame matching, if any.
func (s *sseStream) first(match func(sseFrame) bool) (sseFrame, bool) {
	for _, f := range s.all {
		if match(f) {
			return f, true
		}
	}
	return sseFrame{}, false
}

// publishTransientFrame pushes one raw TRANSIENT live frame (sequence 0) onto a
// run's pub/sub channel — exactly what the executor's live fan-out publishes for
// `content.delta`.
//
// It refuses any other sequence on purpose (Batch 4.1). A durable frame is a
// claim about the run's persisted log, and the gateway now guarantees that the
// durable frames written to one connection are contiguous, repairing any hole
// from MySQL — so a hand-published durable sequence that MySQL does not know
// about is interpreted as a hole the log cannot fill and the connection is
// ended. Durable fixtures go through appendLiveEvent instead.
func publishTransientFrame(t *testing.T, rdb *redisx.Client, runID ids.ID, seq uint64, eventType string, payload map[string]any) {
	t.Helper()
	if seq != 0 {
		t.Fatalf("publishTransientFrame called with sequence %d: a durable frame must be persisted "+
			"through the service (appendLiveEvent) so the canonical log can back it", seq)
	}
	ctx := context.Background()
	if err := rdb.Publish(ctx, rdb.RunEventsChannel(runID.String()), mustJSON(map[string]any{
		"run_id":     runID.String(),
		"sequence":   seq,
		"event_type": eventType,
		"payload":    payload,
		"created_at": time.Now().UTC().Format(time.RFC3339Nano),
	})).Err(); err != nil {
		t.Fatalf("publish %s: %v", eventType, err)
	}
}

// streamRun seeds and claims a live run with its own Redis channel.
func streamRun(t *testing.T, provider string) (*execution.Service, *redisx.Client, ids.ID) {
	t.Helper()
	svc, rdb := testEnv(t)
	if rdb == nil {
		t.Skip("needs STUDIO_TEST_REDIS=1")
	}
	ctx := context.Background()
	runID := seedRun(t, svc, provider)
	deleteRunFixture(t, svc, runID)
	if _, won, err := svc.ClaimRun(ctx, runID, "sse-p33", time.Minute); err != nil || !won {
		t.Fatalf("claim: won=%v err=%v", won, err)
	}
	return svc, rdb, runID
}

// syncStream blocks until the gateway's subscription is provably live by
// round-tripping a marker frame through it. Without this the frames under test
// could be published before the subscription exists and the assertions would
// pass for the wrong reason.
//
// The marker is REPUBLISHED until one comes back: Redis pub/sub does not buffer
// for future subscribers, so a marker published in the window between the
// response headers being flushed and the gateway's SUBSCRIBE is dropped — and
// the read side of this test would then be asserting on a stream that never had
// a subscriber.
//
// The marker is TRANSIENT (sequence 0), and that is load-bearing after Batch
// 4.1. A durable frame is a claim about the run's persisted log: the gateway
// now guarantees that the durable frames it writes to one connection are
// CONTIGUOUS, repairing any hole from MySQL — so a probe that fabricates an
// out-of-band durable sequence (the old 9000 marker) would be read as a hole
// the log cannot fill, and the gateway would correctly end the connection.
// Sequence 0 carries no position, is never cached, never advances a cursor and
// is delivered to every subscriber, which is all a liveness probe needs.
//
// `run.started` rather than `content.delta`: only content.delta is capability
// filtered, and this probe must reach a protocol-1 connection too.
const protocolSyncEvent = "run.started"

func syncStream(t *testing.T, rdb *redisx.Client, runID ids.ID, s *sseStream) {
	t.Helper()
	const marker = "protocol-sync"
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		publishTransientFrame(t, rdb, runID, 0, protocolSyncEvent, map[string]any{"text": marker})
		if s.await(func(f sseFrame) bool { return f.Payload["text"] == marker }, 300*time.Millisecond) {
			// A duplicate marker may still be in flight; it is harmless (the
			// assertions below match on the frames under test, not on a count)
			// and it must be drained so it cannot be mistaken for one.
			_ = s.await(func(f sseFrame) bool { return false }, 150*time.Millisecond)
			return
		}
	}
	t.Fatal("the gateway never delivered the subscription marker — the stream is not live")
}

// appendLiveEvent persists one durable event through the REAL service path —
// allocation, insert, post-commit publish — and returns the sequence the run's
// allocator assigned.
//
// Fabricated durable sequences stopped being usable fixtures in Batch 4.1: the
// gateway treats a durable frame the canonical log cannot supply as an
// unrecoverable ordering hole and ends the connection (fail closed), so every
// durable frame a test publishes must exist in MySQL. Going through
// AppendEvent also makes the fixture honest in the other direction — the
// frame reaches Redis exactly the way a real one does.
//
// The run must be exclusively owned by the test: the sequence is predicted
// nowhere, it is READ BACK, so a concurrent appender would change the answer.
func appendLiveEvent(t *testing.T, svc *execution.Service, runID ids.ID, eventType string, payload map[string]any) uint64 {
	t.Helper()
	ctx := context.Background()
	if err := svc.AppendEvent(ctx, runID, eventType, payload); err != nil {
		t.Fatalf("append %s: %v", eventType, err)
	}
	return runEventTail(t, svc, runID)
}

// runEventTail is the highest sequence currently persisted for the run.
//
// One page is enough for a test fixture, and asking for more than a page would
// make the answer ambiguous (a keyset page reports where it STOPPED, not where
// the log ends), so more rows than one page is a fixture bug and says so.
func runEventTail(t *testing.T, svc *execution.Service, runID ids.ID) uint64 {
	t.Helper()
	page, err := svc.ListEventPage(context.Background(), runID, 0, execution.MaxEventPageSize)
	if err != nil {
		t.Fatalf("read the run's log tail: %v", err)
	}
	if page.HasMore {
		t.Fatalf("the run holds more than %d events: this fixture assumes a single page",
			execution.MaxEventPageSize)
	}
	return page.NextAfter
}

func isLiveDelta(f sseFrame) bool {
	return f.EventType == execution.EventContentDelta && f.Payload["text"] == protocolLiveDeltaText
}

// ── legacy ──────────────────────────────────────────────────────────────

// TestSSELegacyClientReceivesNoTransientDelta: a client that declares no
// protocol still gets the durable chunk, but never the transient delta whose
// rendering would require byte-range reconciliation.
func TestSSELegacyClientReceivesNoTransientDelta(t *testing.T) {
	svc, rdb, runID := streamRun(t, "feishu_aily")
	s := openSSEStream(t, svc, rdb, runID, "")
	syncStream(t, rdb, runID, s)

	// Published in this order on purpose: the delta FIRST, the durable chunk
	// second. Redis preserves publish order, so once the chunk has arrived any
	// delta that was going to be emitted would already be in the transcript —
	// its absence is a decision, not a scheduling artefact.
	publishTransientFrame(t, rdb, runID, 0, execution.EventContentDelta,
		map[string]any{"text": protocolLiveDeltaText, "offset": 1})
	chunkSeq := appendLiveEvent(t, svc, runID, execution.EventContentChunk,
		map[string]any{"text": protocolLiveDeltaText, "offset": 1})

	if !s.await(func(f sseFrame) bool {
		return f.Sequence == chunkSeq && f.EventType == execution.EventContentChunk
	}, 5*time.Second) {
		t.Fatalf("the durable content.chunk never arrived — legacy clients must still receive the "+
			"answer; transcript=%v", frameSummary(s.all))
	}
	if s.saw(isLiveDelta) {
		t.Fatalf("legacy client received a transient content.delta: an append-only client renders "+
			"\"ABCABC\" when the durable chunk is observed first. transcript=%v", frameSummary(s.all))
	}
}

// ── v2 ──────────────────────────────────────────────────────────────────

// TestSSEProtocol2ClientReceivesOffsets: a protocol-2 client keeps the
// low-latency path AND the official one — transient delta with its absolute
// UTF-8 byte offset, followed by the durable chunk it reconciles against.
func TestSSEProtocol2ClientReceivesOffsets(t *testing.T) {
	svc, rdb, runID := streamRun(t, "feishu_aily")
	s := openSSEStream(t, svc, rdb, runID, "stream_protocol=2")
	syncStream(t, rdb, runID, s)

	publishTransientFrame(t, rdb, runID, 0, execution.EventContentDelta,
		map[string]any{"text": protocolLiveDeltaText, "offset": 7})
	chunkSeq := appendLiveEvent(t, svc, runID, execution.EventContentChunk,
		map[string]any{"text": protocolLiveDeltaText, "offset": 7})

	if !s.await(func(f sseFrame) bool { return f.Sequence == chunkSeq }, 5*time.Second) {
		t.Fatalf("the durable content.chunk never arrived; transcript=%v", frameSummary(s.all))
	}
	delta, ok := s.first(isLiveDelta)
	if !ok {
		t.Fatalf("protocol-2 client received no transient content.delta: the low-latency path "+
			"was suppressed for a client that can reconcile it. transcript=%v", frameSummary(s.all))
	}
	offset, ok := delta.Payload["offset"].(float64)
	if !ok || offset != 7 {
		t.Fatalf("transient delta offset = %v (%T), want 7 — the byte offset IS the negotiation's "+
			"payload; without it protocol 2 means nothing", delta.Payload["offset"], delta.Payload["offset"])
	}
	if !s.saw(func(f sseFrame) bool {
		return f.Sequence == chunkSeq && f.EventType == execution.EventContentChunk
	}) {
		t.Fatal("durable chunk missing for a protocol-2 client")
	}
}

// ── cursor ──────────────────────────────────────────────────────────────

// TestSSECursorSemanticsIgnoreStreamProtocol: the durable cursor and the
// rendering capability are different dimensions. Declaring protocol 2 must not
// change which durable sequences are replayed/delivered, and a legacy client
// must not change it either — the only difference is the transient delta.
func TestSSECursorSemanticsIgnoreStreamProtocol(t *testing.T) {
	cases := []struct {
		name        string
		querySuffix string
		wantDelta   bool
	}{
		{name: "protocol2", querySuffix: "&stream_protocol=2", wantDelta: true},
		{name: "legacy", querySuffix: "", wantDelta: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, rdb, runID := streamRun(t, "feishu_aily")

			// The cursor is the sequence the run's NEXT durable event will
			// take, so "at the cursor" and "above the cursor" are REAL events
			// produced by the real allocator — Batch 4.1 makes a durable frame
			// the log cannot back an unrecoverable hole, so those two cannot be
			// fabricated any more.
			cursor := runEventTail(t, svc, runID) + 1
			query := fmt.Sprintf("after=%d%s", cursor, tc.querySuffix)

			s := openSSEStream(t, svc, rdb, runID, query)
			syncStream(t, rdb, runID, s)

			// `cursor` is AT the client's position → already delivered to this
			// client before the reconnect, so it must not be re-sent. The next
			// one is above it and must arrive.
			atSeq := appendLiveEvent(t, svc, runID, execution.EventContentChunk,
				map[string]any{"text": "at-cursor", "offset": 0})
			if atSeq != cursor {
				t.Fatalf("the run's allocator gave the at-cursor event sequence %d, want %d "+
					"(the fixture assumes the run is exclusively owned by this test)", atSeq, cursor)
			}
			aboveSeq := appendLiveEvent(t, svc, runID, execution.EventContentChunk,
				map[string]any{"text": "above-cursor", "offset": 4})
			publishTransientFrame(t, rdb, runID, 0, execution.EventContentDelta,
				map[string]any{"text": protocolLiveDeltaText, "offset": 9})

			if !s.await(func(f sseFrame) bool { return f.Sequence == aboveSeq }, 5*time.Second) {
				t.Fatalf("the frame above the cursor never arrived; transcript=%v", frameSummary(s.all))
			}
			// Give the trailing delta a moment to land before judging it.
			time.Sleep(300 * time.Millisecond)
			_ = s.await(func(f sseFrame) bool { return false }, 200*time.Millisecond)

			if s.saw(func(f sseFrame) bool { return f.Sequence == cursor }) {
				t.Fatal("a durable frame AT the cursor was re-delivered: the cursor semantics changed")
			}
			if !s.saw(func(f sseFrame) bool { return f.Sequence == aboveSeq }) {
				t.Fatal("a durable frame ABOVE the cursor was dropped")
			}
			if got := s.saw(isLiveDelta); got != tc.wantDelta {
				t.Fatalf("transient delta delivered=%v, want %v for %q", got, tc.wantDelta, query)
			}
		})
	}
}

// ── terminal ────────────────────────────────────────────────────────────

// TestSSELegacyClientClosesOnTerminalEvent: suppressing transient deltas must
// not touch the lifecycle. A legacy client still terminates its stream on the
// terminal event and never hangs on keepalive.
func TestSSELegacyClientClosesOnTerminalEvent(t *testing.T) {
	svc, rdb, runID := streamRun(t, "feishu_aily")
	s := openSSEStream(t, svc, rdb, runID, "")
	syncStream(t, rdb, runID, s)

	appendLiveEvent(t, svc, runID, execution.EventRunCompleted,
		map[string]any{"status": execution.StatusSucceeded, "text": "final"})

	if !s.await(func(f sseFrame) bool { return f.EventType == execution.EventRunCompleted }, 5*time.Second) {
		t.Fatal("legacy client never received run.completed")
	}
	select {
	case <-s.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("legacy stream did not close after the terminal event")
	}
}

// frameSummary renders a transcript for failure messages — a bare "delta
// missing" gives no way to tell "suppressed" from "never published".
func frameSummary(frames []sseFrame) string {
	parts := make([]string, 0, len(frames))
	for _, f := range frames {
		text, _ := f.Payload["text"].(string)
		parts = append(parts, f.EventType+"("+text+")")
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
