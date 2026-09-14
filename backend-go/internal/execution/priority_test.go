package execution

import (
	"testing"
)

// simulate serves n messages with the given per-class availability using
// the real scheduler policy: a class can serve a message only while it
// still has queued work.
func simulate(weights []int, available []int, n int) []int {
	sched := newClassScheduler(weights)
	left := make([]int, len(available))
	copy(left, available)
	served := make([]int, 0, n)
	for i := 0; i < n; i++ {
		picked := -1
		for _, idx := range sched.order() {
			if idx < len(left) && left[idx] > 0 {
				picked = idx
				break
			}
		}
		if picked < 0 {
			break // nothing queued anywhere
		}
		left[picked]--
		sched.consume(picked)
		served = append(served, picked)
	}
	return served
}

func count(served []int, idx int) int {
	n := 0
	for _, s := range served {
		if s == idx {
			n++
		}
	}
	return n
}

func name(idx int) string {
	if idx < 0 || idx >= len(PriorityClasses) {
		return "?"
	}
	return PriorityClasses[idx]
}

// TestDefaultPriorityWeightsShape pins the configured order and shares.
func TestDefaultPriorityWeightsShape(t *testing.T) {
	if len(PriorityClasses) != 3 ||
		PriorityClasses[0] != PriorityClassInteractive ||
		PriorityClasses[1] != PriorityClassRetry ||
		PriorityClasses[2] != PriorityClassScheduled {
		t.Fatalf("class order = %v, want [interactive retry scheduled]", PriorityClasses)
	}
	if len(DefaultPriorityWeights) != 3 || DefaultPriorityWeights[0] <= DefaultPriorityWeights[2] {
		t.Fatalf("default weights = %v, want interactive > scheduled", DefaultPriorityWeights)
	}
	if DefaultPriorityWeights[2] < 1 || DefaultPriorityWeights[1] < 1 {
		t.Fatalf("default weights = %v, every class must keep a non-zero share", DefaultPriorityWeights)
	}
}

// TestPriorityClassOfMapping: run priorities map to the three classes.
func TestPriorityClassOfMapping(t *testing.T) {
	cases := map[string]string{
		"interactive_user": PriorityClassInteractive,
		"":                 PriorityClassInteractive,
		"retry":            PriorityClassRetry,
		"scheduled":        PriorityClassScheduled,
		"scheduled_high":   PriorityClassScheduled,
		"scheduled_normal": PriorityClassScheduled,
	}
	for in, want := range cases {
		if got := PriorityClassOf(in); got != want {
			t.Fatalf("PriorityClassOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestWeightedFairShareIsSevenOneTwo: with every class saturated, the
// scheduler serves a real 7:1:2 share per round — interactive jumps ahead
// of a scheduled backlog while scheduled keeps a guaranteed slice.
func TestWeightedFairShareIsSevenOneTwo(t *testing.T) {
	const n = 200
	served := simulate(DefaultPriorityWeights, []int{1000, 1000, 1000}, n)
	if len(served) != n {
		t.Fatalf("served %d messages, want %d", len(served), n)
	}
	got := []int{count(served, 0), count(served, 1), count(served, 2)}
	want := []int{140, 20, 40} // 7:1:2 over 200 messages
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("served shares = %v (%s/%s/%s), want %v",
				got, name(0), name(1), name(2), want)
		}
	}
	// The first message of every round must be interactive: users never
	// wait behind a scheduled backlog.
	roundSize := 0
	for _, w := range DefaultPriorityWeights {
		roundSize += w
	}
	for i := 0; i*roundSize < n; i++ {
		if served[i*roundSize] != 0 {
			t.Fatalf("round %d starts with %s, want interactive", i, name(served[i*roundSize]))
		}
	}
}

// TestScheduledNeverStarvesUnderContinuousInteractive: a sustained
// interactive flow must not starve scheduled work — the scheduler keeps
// serving scheduled messages every round.
func TestScheduledNeverStarvesUnderContinuousInteractive(t *testing.T) {
	const n = 100
	// Interactive backlog is effectively unlimited (1000), scheduled and
	// retry have a bounded backlog.
	served := simulate(DefaultPriorityWeights, []int{1000, 50, 50}, n)
	if got := count(served, 2); got < 10 {
		t.Fatalf("scheduled served only %d/%d under continuous interactive traffic (starvation)", got, n)
	}
	if got := count(served, 1); got < 5 {
		t.Fatalf("retry served only %d/%d under continuous interactive traffic", got, n)
	}
	// Interactive still dominates.
	if got := count(served, 0); got <= count(served, 2) {
		t.Fatalf("interactive=%d scheduled=%d: interactive must keep priority", got, count(served, 2))
	}
}

// TestIdleClassQuotaIsBorrowed: when the interactive class is empty its
// capacity is handed to the other classes instead of being wasted.
func TestIdleClassQuotaIsBorrowed(t *testing.T) {
	const n = 60
	served := simulate(DefaultPriorityWeights, []int{0, 0, 200}, n)
	if got := count(served, 2); got != n {
		t.Fatalf("with interactive/retry idle, scheduled served %d/%d — quota not borrowed", got, n)
	}
	if count(served, 0) != 0 || count(served, 1) != 0 {
		t.Fatalf("served from empty classes: %v", served)
	}
}

// TestBorrowReleasesWhenInteractiveReturns: borrowing must not create a
// starvation debt — the moment interactive traffic arrives it takes its
// full share again.
func TestBorrowReleasesWhenInteractiveReturns(t *testing.T) {
	sched := newClassScheduler(DefaultPriorityWeights)
	// Drain credits while only scheduled has work (borrowing).
	left := []int{0, 0, 100}
	for i := 0; i < 20; i++ {
		for _, idx := range sched.order() {
			if left[idx] > 0 {
				left[idx]--
				sched.consume(idx)
				break
			}
		}
	}
	// Interactive returns: it must be served first (its credits were never
	// spent while it was idle).
	if got := sched.order()[0]; got != 0 {
		t.Fatalf("first probed class after interactive returns = %s, want interactive", name(got))
	}
}

// TestClassSchedulerRefillsAndBorrows documents the two edge cases the
// worker relies on: refill on exhaustion, borrow without credit.
func TestClassSchedulerRefillsAndBorrows(t *testing.T) {
	sched := newClassScheduler([]int{2, 0, 1})
	got := sched.order()
	want := []int{0, 2, 1} // credited [0 2] first, then the borrowed retry
	if len(got) != len(want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
	sched.consume(0)
	sched.consume(0)
	sched.consume(2)
	// All credits spent → the next order refills instead of returning only
	// borrows.
	if next := sched.order(); next[0] != 0 {
		t.Fatalf("after exhaustion the first class = %s, want interactive (refilled)", name(next[0]))
	}
	// A zero-weight class is borrowable but never accumulates credit.
	if sched.credits[1] != 0 {
		t.Fatalf("retry credit = %d, want 0", sched.credits[1])
	}
}
