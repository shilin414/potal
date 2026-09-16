// 第十轮 Batch 4.1 端到端验证：durable live ordering（复审报告 §15）。
//
// This property cannot be shown by the package tests alone, because it lives on
// the boundary between THREE systems:
//
//	Durable Live Ordering   Redis may deliver a durable frame out of order
//	                        (publish happens after COMMIT, so wall-clock order
//	                        is not sequence order) while MySQL — the ordering
//	                        authority — can always supply the hole. The proof is
//	                        that a real client watching real Redis ends up with
//	                        a contiguous transcript regardless.
//
// The cache-bound half of Batch 4.1 is a separate file
// (review10_patch41_cache_test.go), because it is a separate guarantee.
//
// The hub's internal accounting is asserted in internal/transport/sse, where
// subscriptions and cache bytes can be COUNTED rather than inferred.
package integration

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/platform/dbtypes"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
	"github.com/creation-agent-studio/backend-go/internal/platform/redisx"
)

// seedDurableEvent persists one durable event at an EXPLICIT sequence WITHOUT
// publishing it. The subsequent publish (publishDurableFrame) is what the test
// orders by hand.
//
// This is a faithful model of production, not a shortcut around it. A durable
// event is published AFTER its transaction commits, so the order of two
// publishes is independent of the order of the commits that produced their
// sequences — and the only way to reproduce that split deterministically is to
// separate the two steps. appendLiveEvent cannot: it publishes inside the
// append, which is exactly why the gateway has a repair path at all.
//
// runs.next_event_sequence is advanced with the row so the fixture keeps the
// allocator's own invariant (the counter is always the next FREE sequence).
// Leaving it behind would make a later append collide with the unique key on
// (run_id, sequence) instead of failing for a reason anyone could read.
func seedDurableEvent(t *testing.T, svc *execution.Service, runID ids.ID, seq uint64, eventType string, payload map[string]any) {
	t.Helper()
	ctx := context.Background()
	raw, err := dbtypes.MarshalJSON(payload)
	if err != nil {
		t.Fatalf("encode the payload of event %d: %v", seq, err)
	}
	if _, err := svc.DB.ExecContext(ctx,
		`INSERT INTO run_events (run_id, sequence, event_type, payload) VALUES (?, ?, ?, ?)`,
		runID.Bytes(), seq, eventType, raw,
	); err != nil {
		t.Fatalf("insert durable event %d: %v", seq, err)
	}
	if _, err := svc.DB.ExecContext(ctx,
		`UPDATE runs SET next_event_sequence = ? WHERE id = ?`, seq+1, runID.Bytes(),
	); err != nil {
		t.Fatalf("advance the sequence counter past %d: %v", seq, err)
	}
}

// publishDurableFrame pushes one durable frame onto a run's pub/sub channel by
// hand.
//
// The row MUST already exist in MySQL under the same sequence: Batch 4.1 makes
// a durable frame the canonical log cannot back an unrecoverable ordering hole,
// so the gateway will (correctly) end the connection rather than deliver it.
// That is why this helper lives next to seedDurableEvent and not next to
// publishTransientFrame, which refuses non-zero sequences outright.
func publishDurableFrame(t *testing.T, rdb *redisx.Client, runID ids.ID, seq uint64, eventType string, payload map[string]any) {
	t.Helper()
	ctx := context.Background()
	if err := rdb.Publish(ctx, rdb.RunEventsChannel(runID.String()), mustJSON(map[string]any{
		"run_id":     runID.String(),
		"sequence":   seq,
		"event_type": eventType,
		"payload":    payload,
		"created_at": time.Now().UTC().Format(time.RFC3339Nano),
	})).Err(); err != nil {
		t.Fatalf("publish %s(%d): %v", eventType, seq, err)
	}
}

// durableSequences is the transcript's durable positions, in arrival order. The
// subscription marker carries sequence 0 (a transport position that does not
// exist) and is excluded by construction — as is every transient frame.
func durableSequences(s *sseStream) []uint64 {
	out := []uint64{}
	for _, f := range s.all {
		if f.Sequence != 0 {
			out = append(out, f.Sequence)
		}
	}
	return out
}

// TestSSEHubRepairsDurableRedisReorder is AC-4.1-3 over real MySQL and real
// Redis: Redis delivers 102 before 101, and the CLIENT still renders
// 101 then 102, exactly once.
//
// The failure this prevents is permanent content loss, not a cosmetic order
// slip. A client derives its reconnect cursor from the highest sequence it has
// RENDERED; a consumer that took 102 and skipped 101 would ask for `after=102`
// next time and 101 would never reach it. With the terminal event in the
// sequence, it is worse still: the client would stop reading before its
// predecessor arrived.
func TestSSEHubRepairsDurableRedisReorder(t *testing.T) {
	svc, rdb, runID := streamRun(t, "feishu_aily")
	run, err := svc.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("load run: %v", err)
	}
	ts, _ := startHubSSEServer(t, svc, rdb, run)
	t.Cleanup(ts.Close)

	const older, newer = uint64(101), uint64(102)
	clientCursor := uint64(100)

	// The client connects with a cursor BELOW both events, and the log is empty
	// at that instant: everything the repair needs arrives afterwards.
	s := openStreamAt(t, runURL(ts, runID, "after="+strconv.FormatUint(clientCursor, 10)))
	syncStream(t, rdb, runID, s)

	// The events commit in order...
	seedDurableEvent(t, svc, runID, older, execution.EventContentChunk, map[string]any{"text": "one"})
	seedDurableEvent(t, svc, runID, newer, execution.EventContentChunk, map[string]any{"text": "two"})

	// ...and PUBLISH in the other one. This is legal in production: the
	// publishes happen after the commits, so any scheduling delay between them
	// reorders them, and Redis pub/sub preserves publish order, not commit
	// order.
	publishDurableFrame(t, rdb, runID, newer, execution.EventContentChunk, map[string]any{"text": "two"})
	publishDurableFrame(t, rdb, runID, older, execution.EventContentChunk, map[string]any{"text": "one"})

	// The gateway must refuse the forward jump, read [101, 102] from the log,
	// write them in order, and then treat the late 101 as the duplicate it is.
	if !s.await(func(f sseFrame) bool { return f.Sequence == newer }, 5*time.Second) {
		t.Fatalf("the repaired range never reached the client; transcript=%v", frameSummary(s.all))
	}
	// Give the late 101 a moment to land (it must be dropped, not appended).
	time.Sleep(300 * time.Millisecond)
	_ = s.await(func(f sseFrame) bool { return false }, 200*time.Millisecond)

	got := durableSequences(s)
	want := []uint64{older, newer}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("durable transcript = %v, want %v in order, exactly once: a client that saw 102 "+
			"before 101 would resume from 102 on its next reconnect and lose 101 for good", got, want)
	}

	// And the client's own resume cursor is the contiguous position, which is
	// what makes the reconnect above safe.
	if cursor := s.contentCursor(); cursor != newer {
		t.Fatalf("the client's durable cursor = %d, want %d", cursor, newer)
	}
}
