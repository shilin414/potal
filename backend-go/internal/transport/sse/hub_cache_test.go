package sse

import (
	"testing"

	"github.com/creation-agent-studio/backend-go/internal/execution"
)

// durableEvent builds a cacheable durable event for ring tests.
func durableEvent(seq uint64, weight int) HubEvent {
	return HubEvent{Sequence: seq, EventType: execution.EventContentChunk, ApproxBytes: weight}
}

// TestDurableRingCachesOnlyDurableEvents pins the "never cache a transient
// frame" rule (§10).
//
// `content.delta` carries sequence 0, which is a transport marker rather than
// a position: a client resuming from `after=N` can never be served by it, so
// caching it would spend the byte budget that the durable tail needs in order
// to answer the only question reconnects actually ask.
func TestDurableRingCachesOnlyDurableEvents(t *testing.T) {
	ring := NewDurableRing(64, 1<<20)
	evicted, discontinuous := ring.Remember(HubEvent{
		Sequence:    0,
		EventType:   execution.EventContentDelta,
		Payload:     map[string]any{"text": "live", "offset": 1000},
		ApproxBytes: 64,
	})
	if evicted != 0 || discontinuous {
		t.Fatalf("transient Remember reported (evicted=%d, discontinuity=%v), want (0,false)", evicted, discontinuous)
	}
	if ring.Len() != 0 || ring.Bytes() != 0 {
		t.Fatalf("transient frame was cached: len=%d bytes=%d, want 0/0", ring.Len(), ring.Bytes())
	}
	if _, covered := ring.After(0); covered {
		t.Fatal("an empty (transient-only) ring claimed to cover cursor 0")
	}
	if high := ring.HighWater(); high != 0 {
		t.Fatalf("HighWater after a transient frame = %d, want 0 (sequence 0 is not a position)", high)
	}
}

// TestDurableRingBoundsByEventCountAndBytes pins AC-7: BOTH limits are
// enforced.
//
// An event-count-only ring is not bounded in memory — one `content.chunk` can
// carry a large slice of an answer — so the byte budget has to bite even when
// the ring holds far fewer events than its count limit. The second half of the
// test is the inverse: a huge count limit must still stop at the byte budget.
func TestDurableRingBoundsByEventCountAndBytes(t *testing.T) {
	t.Run("event count", func(t *testing.T) {
		ring := NewDurableRing(4, 1<<30)
		for seq := uint64(1); seq <= 100; seq++ {
			ring.Remember(durableEvent(seq, 8))
		}
		if got := ring.Len(); got != 4 {
			t.Fatalf("Len = %d after 100 events with maxEvents=4, want 4", got)
		}
		if got := ring.FirstSeq(); got != 97 {
			t.Fatalf("FirstSeq = %d, want 97: the ring must keep the NEWEST tail", got)
		}
		if got := ring.HighWater(); got != 100 {
			t.Fatalf("HighWater = %d, want 100", got)
		}
	})

	t.Run("byte budget", func(t *testing.T) {
		// Four enormous events, a count limit that would happily hold them
		// all, and a byte budget that must not.
		ring := NewDurableRing(1000, 300)
		for seq := uint64(1); seq <= 10; seq++ {
			ring.Remember(durableEvent(seq, 100))
		}
		if got := ring.Bytes(); got > 300 {
			t.Fatalf("Bytes = %d after 10x100B with maxBytes=300, want <= 300", got)
		}
		if got := ring.Len(); got != 3 {
			t.Fatalf("Len = %d, want 3 (the byte budget, not the count, must bite)", got)
		}
		if got := ring.HighWater(); got != 10 {
			t.Fatalf("HighWater = %d, want 10", got)
		}
	})
}

// TestDurableRingRejectsSingleOversizedEvent is AC-4.1-1/AC-4.1-2 and kills
// Mutation J (§33).
//
// The pre-4.1 ring kept the newest event whatever its size ("a ring that
// evicted the event it was just handed could never serve any cursor"), which
// made CacheMaxBytes advisory: ONE oversized durable event stayed resident no
// matter the budget. That is not academic — the terminal event carries the
// run's whole answer in `payload.text`, so `run.completed` > 8 MiB is
// structurally possible, and N such hubs multiply it.
//
// The correct answer is neither truncation (a cached event must BE the event)
// nor exemption: retain NOTHING for it. Coverage is lost, the cursor falls
// back to MySQL, and the log still holds the frame in full — not cached is not
// the same as not sent, and not the same as lost.
func TestDurableRingRejectsSingleOversizedEvent(t *testing.T) {
	ring := NewDurableRing(10, 10)
	evicted, discontinuous := ring.Remember(durableEvent(1, 4096))
	if evicted != 0 {
		t.Fatalf("evicted = %d for a first oversized event in an empty ring, want 0", evicted)
	}
	if !discontinuous {
		t.Fatal("an oversized event was reported as continuous: the ring retains nothing, " +
			"so nothing it held can be claimed as covered")
	}
	if ring.Len() != 0 || ring.Bytes() != 0 {
		t.Fatalf("ring = len:%d bytes:%d, want 0/0: an event larger than the whole byte "+
			"budget must never be retained (AC-4.1-1: Bytes() <= maxBytes, always)",
			ring.Len(), ring.Bytes())
	}
	if _, covered := ring.After(0); covered {
		t.Fatal("the ring claimed to cover cursor 0 while holding nothing")
	}
	if got := ring.ObservedHighWater(); got != 1 {
		t.Fatalf("observed high-water = %d, want 1: rejecting a payload must not forget the "+
			"position, or a late frame could reopen the range", got)
	}
}

// TestDurableRingOversizedEventBreaksCoverage pins the coverage half of §21:
// after an oversized barrier every cursor at or below it is a MISS, so the
// client goes to MySQL for the range instead of being told "nothing to send".
func TestDurableRingOversizedEventBreaksCoverage(t *testing.T) {
	ring := NewDurableRing(64, 256)
	for seq := uint64(1); seq <= 4; seq++ {
		if _, discontinuous := ring.Remember(durableEvent(seq, 8)); discontinuous {
			t.Fatalf("seq %d reported a discontinuity inside a contiguous run", seq)
		}
	}
	if ring.Len() != 4 {
		t.Fatalf("Len = %d before the barrier, want 4", ring.Len())
	}

	evicted, discontinuous := ring.Remember(durableEvent(5, 4096))
	if !discontinuous {
		t.Fatal("the oversized event did not invalidate the retained segment")
	}
	if evicted != 4 {
		t.Fatalf("evicted = %d, want 4 (the invalidated 1..4 segment)", evicted)
	}
	if ring.Len() != 0 || ring.Bytes() != 0 {
		t.Fatalf("ring = len:%d bytes:%d after the barrier, want 0/0", ring.Len(), ring.Bytes())
	}
	if _, covered := ring.After(4); covered {
		t.Fatal("cache claimed to cover after=4 although 5 is not retained: a client resuming " +
			"there must be sent 5 from MySQL")
	}
	if got := ring.ObservedHighWater(); got != 5 {
		t.Fatalf("observed high-water = %d, want 5", got)
	}
}

// TestDurableRingResumesSegmentAfterOversizedBarrier: the cache must recover —
// a new contiguous segment starts at the first event AFTER the barrier, and
// exactly one cursor (the barrier itself) can serve it.
//
//	after=4 → MISS   (5 is the barrier: not retained anywhere but MySQL)
//	after=5 → HIT    ([6 7])
func TestDurableRingResumesSegmentAfterOversizedBarrier(t *testing.T) {
	ring := NewDurableRing(64, 256)
	for seq := uint64(1); seq <= 4; seq++ {
		ring.Remember(durableEvent(seq, 8))
	}
	ring.Remember(durableEvent(5, 4096))
	for seq := uint64(6); seq <= 7; seq++ {
		if _, discontinuous := ring.Remember(durableEvent(seq, 8)); discontinuous {
			t.Fatalf("seq %d did not resume the segment after the barrier", seq)
		}
	}

	if _, covered := ring.After(4); covered {
		t.Fatal("after=4 must MISS: 5 is not retained")
	}
	events, covered := ring.After(5)
	if !covered {
		t.Fatal("after=5 must HIT: the barrier itself is the last position the client holds, " +
			"and 6..7 are retained contiguously")
	}
	if want := []uint64{6, 7}; len(events) != len(want) || events[0].Sequence != 6 || events[1].Sequence != 7 {
		t.Fatalf("After(5) = %v, want [6 7]", sequencesOf(events))
	}
	if got := ring.FirstSeq(); got != 6 {
		t.Fatalf("FirstSeq = %d, want 6", got)
	}

	// A straggler below the barrier must not be appended: the ring has moved
	// past 5, and re-opening the abandoned range would make the segment
	// non-contiguous in the other direction.
	if evicted, discontinuous := ring.Remember(durableEvent(3, 8)); evicted != 0 || discontinuous {
		t.Fatalf("stale seq 3 after the barrier reported (evicted=%d, discontinuous=%v), want (0,false)",
			evicted, discontinuous)
	}
	if ring.Len() != 2 || ring.FirstSeq() != 6 {
		t.Fatalf("ring = len:%d first:%d after a stale frame, want 2/6", ring.Len(), ring.FirstSeq())
	}
}

// TestDurableRingBytesNeverExceedConfiguredBound is AC-4.1-1 as an invariant
// over a mixed workload rather than a single shape: at every instant, after
// every Remember, the retained footprint is within the bound — including the
// moments right after a payload far larger than the whole budget.
func TestDurableRingBytesNeverExceedConfiguredBound(t *testing.T) {
	const maxBytes = 1024
	ring := NewDurableRing(1000, maxBytes)

	weights := []int{64, 64, 512, 64, 4096, 64, 64, 100, 64, 4096, 8, 2048, 32}
	for i, weight := range weights {
		seq := uint64(i + 1)
		ring.Remember(durableEvent(seq, weight))
		if got := ring.Bytes(); got > maxBytes {
			t.Fatalf("after event %d (weight %d): Bytes = %d, want <= %d — the byte bound is a "+
				"memory-safety limit, not a target", seq, weight, got, maxBytes)
		}
		if got := ring.Len(); got > 1000 {
			t.Fatalf("after event %d: Len = %d, want <= 1000", seq, got)
		}
		if got := ring.ObservedHighWater(); got != seq {
			t.Fatalf("after event %d: observed high-water = %d", seq, got)
		}
	}
}

// TestDurableRingGapInvalidatesContinuity pins AC-8 and kills Mutation F.
//
// Redis pub/sub is at-most-once, so the ring CAN observe 100,101 then 105:
// 102-104 were published while this process was not listening. Appending 105
// after 101 would make the ring claim to cover 102-104, and a client resuming
// from 101 would be told "cache hit" — it would never be sent those three
// events while its cursor advanced past them. The correct behaviour is to
// discard the stale segment and let MySQL answer.
func TestDurableRingGapInvalidatesContinuity(t *testing.T) {
	ring := NewDurableRing(64, 1<<20)
	for _, seq := range []uint64{100, 101} {
		if _, discontinuous := ring.Remember(durableEvent(seq, 16)); discontinuous {
			t.Fatalf("seq %d reported a discontinuity inside a contiguous run", seq)
		}
	}
	evicted, discontinuous := ring.Remember(durableEvent(105, 16))
	if !discontinuous {
		t.Fatal("seq 105 after 101 was not reported as a discontinuity")
	}
	if evicted != 2 {
		t.Fatalf("evicted = %d on discontinuity, want 2 (the invalidated 100,101 segment)", evicted)
	}
	if got := ring.FirstSeq(); got != 105 {
		t.Fatalf("FirstSeq = %d after the gap, want 105 (a NEW contiguous segment)", got)
	}

	// The cursor INSIDE the gap must not be declared covered.
	if _, covered := ring.After(101); covered {
		t.Fatal("cache claimed to cover after=101 although 102-104 are missing: " +
			"a client resuming there would lose three events")
	}
	if _, covered := ring.After(100); covered {
		t.Fatal("cache claimed to cover after=100 although 101-104 are missing")
	}

	// A cursor immediately below the new segment IS covered: the client
	// already holds everything before 105.
	events, covered := ring.After(104)
	if !covered {
		t.Fatal("cache refused after=104 though it holds 105,106 contiguously")
	}
	if len(events) != 1 || events[0].Sequence != 105 {
		t.Fatalf("After(104) = %v, want exactly [105]", sequencesOf(events))
	}

	// The segment keeps extending contiguously from the new base.
	ring.Remember(durableEvent(106, 16))
	events, covered = ring.After(104)
	if !covered || len(events) != 2 {
		t.Fatalf("After(104) = %v (covered=%v), want [105 106]", sequencesOf(events), covered)
	}
}

// TestDurableRingIgnoresReplaysOfOlderEvents: the same durable event legitimately
// reaches the ring twice (a duplicate publish, the overlap between a
// subscriber's DB replay and the live path). Appending the copy would break the
// ascending/contiguous invariant, and — worse — a stale event must never
// resurrect a range the ring already gave up on.
func TestDurableRingIgnoresReplaysOfOlderEvents(t *testing.T) {
	ring := NewDurableRing(64, 1<<20)
	ring.Remember(durableEvent(105, 16))
	if evicted, discontinuous := ring.Remember(durableEvent(105, 16)); evicted != 0 || discontinuous {
		t.Fatalf("duplicate seq 105 reported (evicted=%d, discontinuity=%v), want (0,false)", evicted, discontinuous)
	}
	if evicted, discontinuous := ring.Remember(durableEvent(80, 16)); evicted != 0 || discontinuous {
		t.Fatalf("stale seq 80 reported (evicted=%d, discontinuity=%v), want (0,false)", evicted, discontinuous)
	}
	if ring.Len() != 1 || ring.FirstSeq() != 105 || ring.HighWater() != 105 {
		t.Fatalf("ring = len:%d first:%d high:%d, want 1/105/105",
			ring.Len(), ring.FirstSeq(), ring.HighWater())
	}
}

// TestDurableRingCoverageBoundaries: the first cursor the ring can serve is
// firstSeq-1 (the client holds everything before the segment).
func TestDurableRingCoverageBoundaries(t *testing.T) {
	ring := NewDurableRing(64, 1<<20)
	for seq := uint64(50); seq <= 120; seq++ {
		ring.Remember(durableEvent(seq, 16))
	}

	events, covered := ring.After(80)
	if !covered {
		t.Fatal("After(80) not covered although the ring holds 50..120")
	}
	if len(events) != 40 || events[0].Sequence != 81 || events[len(events)-1].Sequence != 120 {
		t.Fatalf("After(80) = %d events [%d..%d], want 40 events [81..120]",
			len(events), first(events), last(events))
	}

	if _, covered := ring.After(48); covered {
		t.Fatal("After(48) reported covered although 49 is missing")
	}

	// The client is already at/above the high-water mark: nothing to replay,
	// but the cursor is still "covered" (the DB tail read finds anything
	// newer).
	if events, covered := ring.After(120); !covered || len(events) != 0 {
		t.Fatalf("After(120) = %v (covered=%v), want [] with covered=true", sequencesOf(events), covered)
	}
	if events, covered := ring.After(200); !covered || len(events) != 0 {
		t.Fatalf("After(200) = %v (covered=%v), want [] with covered=true", sequencesOf(events), covered)
	}
}

// TestDurableRingStaysBoundedOverLongHistory: 100k events must not be held;
// the ring must converge to its bound rather than growing with history
// (§56 — the memory acceptance criterion).
func TestDurableRingStaysBoundedOverLongHistory(t *testing.T) {
	ring := NewDurableRing(256, 1<<20)
	for seq := uint64(1); seq <= 100000; seq++ {
		ring.Remember(durableEvent(seq, 32))
	}
	if got := ring.Len(); got != 256 {
		t.Fatalf("Len = %d after 100k events with maxEvents=256, want 256", got)
	}
	if got := ring.FirstSeq(); got != 100000-255 {
		t.Fatalf("FirstSeq = %d, want %d", got, 100000-255)
	}
	if got := ring.HighWater(); got != 100000 {
		t.Fatalf("HighWater = %d, want 100000", got)
	}
	if got := ring.Bytes(); got != 256*32 {
		t.Fatalf("Bytes = %d, want %d", got, 256*32)
	}
}

// TestDurableRingAfterReturnsACopy guards the immutability contract: the
// caller must not be able to mutate the ring by editing the returned slice.
func TestDurableRingAfterReturnsACopy(t *testing.T) {
	ring := NewDurableRing(16, 1<<20)
	for seq := uint64(1); seq <= 5; seq++ {
		ring.Remember(durableEvent(seq, 8))
	}
	events, covered := ring.After(2)
	if !covered || len(events) != 3 {
		t.Fatalf("After(2) = %v (covered=%v), want [3 4 5]", sequencesOf(events), covered)
	}
	events[0] = HubEvent{Sequence: 999}
	again, _ := ring.After(2)
	if again[0].Sequence != 3 {
		t.Fatalf("mutating the returned slice changed the ring: got %d, want 3", again[0].Sequence)
	}
}

func sequencesOf(events []HubEvent) []uint64 {
	out := make([]uint64, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.Sequence)
	}
	return out
}

func first(events []HubEvent) uint64 {
	if len(events) == 0 {
		return 0
	}
	return events[0].Sequence
}

func last(events []HubEvent) uint64 {
	if len(events) == 0 {
		return 0
	}
	return events[len(events)-1].Sequence
}
