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

// publishLiveFrame pushes one raw live frame onto a run's pub/sub channel —
// exactly what the executor's live fan-out publishes.
func publishLiveFrame(t *testing.T, rdb *redisx.Client, runID ids.ID, seq uint64, eventType string, payload map[string]any) {
	t.Helper()
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
const protocolSyncSequence = 9000

func syncStream(t *testing.T, rdb *redisx.Client, runID ids.ID, s *sseStream) {
	t.Helper()
	const marker = "protocol-sync"
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		publishLiveFrame(t, rdb, runID, protocolSyncSequence, "run.started", map[string]any{"text": marker})
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
	publishLiveFrame(t, rdb, runID, 0, execution.EventContentDelta,
		map[string]any{"text": protocolLiveDeltaText, "offset": 1})
	publishLiveFrame(t, rdb, runID, 9001, execution.EventContentChunk,
		map[string]any{"text": protocolLiveDeltaText, "offset": 1})

	if !s.await(func(f sseFrame) bool { return f.Sequence == 9001 }, 5*time.Second) {
		t.Fatal("the durable content.chunk never arrived — legacy clients must still receive the answer")
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

	publishLiveFrame(t, rdb, runID, 0, execution.EventContentDelta,
		map[string]any{"text": protocolLiveDeltaText, "offset": 7})
	publishLiveFrame(t, rdb, runID, 9001, execution.EventContentChunk,
		map[string]any{"text": protocolLiveDeltaText, "offset": 7})

	if !s.await(func(f sseFrame) bool { return f.Sequence == 9001 }, 5*time.Second) {
		t.Fatal("the durable content.chunk never arrived")
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
		return f.Sequence == 9001 && f.EventType == execution.EventContentChunk
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
		name      string
		query     string
		wantDelta bool
	}{
		{name: "protocol2", query: "after=100&stream_protocol=2", wantDelta: true},
		{name: "legacy", query: "after=100", wantDelta: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, rdb, runID := streamRun(t, "feishu_aily")
			s := openSSEStream(t, svc, rdb, runID, tc.query)
			syncStream(t, rdb, runID, s)

			// 100 is AT the cursor → already delivered to this client before
			// the reconnect, so it must not be re-sent. 101 is above it.
			publishLiveFrame(t, rdb, runID, 100, execution.EventContentChunk,
				map[string]any{"text": "at-cursor", "offset": 0})
			publishLiveFrame(t, rdb, runID, 101, execution.EventContentChunk,
				map[string]any{"text": "above-cursor", "offset": 4})
			publishLiveFrame(t, rdb, runID, 0, execution.EventContentDelta,
				map[string]any{"text": protocolLiveDeltaText, "offset": 9})

			if !s.await(func(f sseFrame) bool { return f.Sequence == 101 }, 5*time.Second) {
				t.Fatal("the frame above the cursor never arrived")
			}
			// Give the trailing delta a moment to land before judging it.
			time.Sleep(300 * time.Millisecond)
			_ = s.await(func(f sseFrame) bool { return false }, 200*time.Millisecond)

			if s.saw(func(f sseFrame) bool { return f.Sequence == 100 }) {
				t.Fatal("a durable frame AT the cursor was re-delivered: the cursor semantics changed")
			}
			if !s.saw(func(f sseFrame) bool { return f.Sequence == 101 }) {
				t.Fatal("a durable frame ABOVE the cursor was dropped")
			}
			if got := s.saw(isLiveDelta); got != tc.wantDelta {
				t.Fatalf("transient delta delivered=%v, want %v for %q", got, tc.wantDelta, tc.query)
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

	publishLiveFrame(t, rdb, runID, 9002, execution.EventRunCompleted,
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
