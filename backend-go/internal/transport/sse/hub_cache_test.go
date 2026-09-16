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

	t.Run("newest event always survives", func(t *testing.T) {
		// A single event larger than the whole budget must still be held:
		// a ring that evicted the event it was just handed could never serve
		// any cursor, and first/last would be meaningless.
		ring := NewDurableRing(10, 10)
		ring.Remember(durableEvent(1, 4096))
		if ring.Len() != 1 || ring.HighWater() != 1 {
			t.Fatalf("oversized single event was dropped: len=%d high=%d, want 1/1", ring.Len(), ring.HighWater())
		}
	})
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
