package execution

import (
	"testing"
)

// serveNext serves one message using the real scheduler policy: classes are
// probed in the order the worker probes them, a class with queued work is
// served (consume), and an idle class forfeits the rest of its round credit
// (markEmpty) — exactly what Worker.readWeighted does. Returns the served
// class index or -1 when every queue is empty.
//
// When the first probe pass only forfeits empty classes, the worker pauses
// and probes again on a fresh round; the second pass models that refill.
func serveNext(sched *classScheduler, left []int) int {
	for pass := 0; pass < 2; pass++ {
		for _, idx := range sched.order() {
			if idx < len(left) && left[idx] > 0 {
				left[idx]--
				sched.consume(idx)
				return idx
			}
			sched.markEmpty(idx)
		}
	}
	return -1
}

// simulate serves n messages with the given per-class availability using
// the real scheduler policy: a class can serve a message only while it
// still has queued work.
func simulate(weights []int, available []int, n int) []int {
	sched := newClassScheduler(weights)
	left := make([]int, len(available))
	copy(left, available)
	served := make([]int, 0, n)
	for i := 0; i < n; i++ {
		picked := serveNext(sched, left)
		if picked < 0 {
			break // nothing queued anywhere
		}
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

// TestScheduledNeverStarvesWhenRetryEmpty — the P0 starvation bug from the
// code review: interactive and scheduled are continuously busy while the
// retry stream is EMPTY. The old policy only refilled when every credit hit
// 0, and an empty retry stream never consumed its credit — the credits got
// pinned at [0,1,0] and scheduled was starved forever after the first two
// messages. With markEmpty the empty class forfeits its credit and the round
// refills, so scheduled keeps receiving ~2/9 of the worker's capacity.
func TestScheduledNeverStarvesWhenRetryEmpty(t *testing.T) {
	const n = 1000
	served := simulate(DefaultPriorityWeights, []int{1000, 0, 1000}, n)
	if len(served) != n {
		t.Fatalf("served %d messages, want %d", len(served), n)
	}
	got := count(served, 2)
	if got < 150 {
		t.Fatalf("scheduled served only %d/%d while retry is empty (starvation regression)", got, n)
	}
	if got > 300 {
		t.Fatalf("scheduled served %d/%d — more than its 2/9 share, interactive lost its priority", got, n)
	}
	if count(served, 1) != 0 {
		t.Fatalf("retry (empty backlog) served %d messages", count(served, 1))
	}
	if count(served, 0) <= got {
		t.Fatalf("interactive=%d scheduled=%d: interactive must keep priority", count(served, 0), got)
	}
}

// TestRetryNeverStarvesWhenScheduledEmpty: the symmetric case — scheduled
// is empty while interactive and retry are busy. Retry must keep its 1/9
// share instead of being frozen out by the empty scheduled credits.
func TestRetryNeverStarvesWhenScheduledEmpty(t *testing.T) {
	const n = 1000
	served := simulate(DefaultPriorityWeights, []int{1000, 1000, 0}, n)
	got := count(served, 1)
	if got < 80 {
		t.Fatalf("retry served only %d/%d while scheduled is empty (starvation regression)", got, n)
	}
	if got > 180 {
		t.Fatalf("retry served %d/%d — more than its 1/9 share", got, n)
	}
	if count(served, 2) != 0 {
		t.Fatalf("scheduled (empty backlog) served %d messages", count(served, 2))
	}
}

// TestEmptyCreditedClassCannotPinRound: the direct regression for the bug —
// an empty credited class must not keep the round open. After interactive
// exhausts its quota and retry is probed empty, the round must still offer
// the scheduled class, and after scheduled spends its own quota the round
// must refill instead of being pinned by retry's leftover credit.
func TestEmptyCreditedClassCannotPinRound(t *testing.T) {
	sched := newClassScheduler(DefaultPriorityWeights)
	for i := 0; i < DefaultPriorityWeights[0]; i++ {
		sched.consume(0) // interactive spends its whole quota
	}
	sched.markEmpty(1) // retry is empty
	if sched.allExhausted() {
		t.Fatal("empty retry credit pinned the round: allExhausted()=true with scheduled credit left")
	}
	order := sched.order()
	if len(order) != 1 || order[0] != 2 {
		t.Fatalf("order after interactive quota + empty retry = %v, want [scheduled]", order)
	}
	// Scheduled spends its quota: the round is now exhausted and refills.
	sched.consume(2)
	sched.consume(2)
	if !sched.allExhausted() {
		t.Fatal("round never exhausts after the active classes spent their quotas")
	}
	if next := sched.order(); next[0] != 0 {
		t.Fatalf("after refill the first class = %s, want interactive", name(next[0]))
	}
}

// TestTwoActiveClassesReceiveRelativeShare: with only interactive and
// scheduled active (retry idle), the two classes share the capacity in
// their configured 7:2 ratio — the idle class wastes nothing and skews
// nothing.
func TestTwoActiveClassesReceiveRelativeShare(t *testing.T) {
	const n = 900
	served := simulate(DefaultPriorityWeights, []int{1000, 0, 1000}, n)
	interactive, scheduled := count(served, 0), count(served, 2)
	if interactive+scheduled != n {
		t.Fatalf("only two classes are active: served %d interactive + %d scheduled != %d",
			interactive, scheduled, n)
	}
	// 7:2 over 900 messages → 700:200.
	if interactive < 650 || interactive > 750 {
		t.Fatalf("interactive share = %d/%d, want ~700 (7:2 with the idle class forfeiting)", interactive, n)
	}
	if scheduled < 150 || scheduled > 250 {
		t.Fatalf("scheduled share = %d/%d, want ~200 (7:2 with the idle class forfeiting)", scheduled, n)
	}
}

// TestClassBecomesActiveAgainAfterBeingMarkedEmpty: forfeiting credit is not
// a permanent demotion — once work arrives the class is credited again on
// the next round and is probed in its normal priority position.
func TestClassBecomesActiveAgainAfterBeingMarkedEmpty(t *testing.T) {
	sched := newClassScheduler(DefaultPriorityWeights)
	sched.markEmpty(0) // interactive was idle
	sched.markEmpty(1) // retry was idle
	// Scheduled spends its quota: the round is exhausted and refills, which
	// must credit interactive again — being marked empty is not a permanent
	// demotion.
	sched.consume(2)
	sched.consume(2)
	if order := sched.order(); len(order) == 0 || order[0] != 0 {
		t.Fatalf("interactive did not return to the head of the round: order=%v", order)
	}
}

// TestIdleClassQuotaIsNotWasted: when only one class has work it receives
// 100% of the worker's capacity — the other classes' credits are forfeited
// instead of being held (which used to split the capacity unevenly and, in
// the retry-empty case, stall the round entirely).
func TestIdleClassQuotaIsNotWasted(t *testing.T) {
	const n = 60
	served := simulate(DefaultPriorityWeights, []int{0, 0, 200}, n)
	if got := count(served, 2); got != n {
		t.Fatalf("with interactive/retry idle, scheduled served %d/%d — idle quota wasted", got, n)
	}
	if count(served, 0) != 0 || count(served, 1) != 0 {
		t.Fatalf("served from empty classes: %v", served)
	}
}

// TestInteractiveReclaimsShareWhenItReturns: a class that was idle (and got
// marked empty) must take its full share the moment traffic returns — no
// starvation debt in either direction.
func TestInteractiveReclaimsShareWhenItReturns(t *testing.T) {
	sched := newClassScheduler(DefaultPriorityWeights)
	left := []int{0, 0, 100}
	for i := 0; i < 20; i++ {
		if got := serveNext(sched, left); got != 2 {
			t.Fatalf("while only scheduled is active, served class %s, want scheduled", name(got))
		}
	}
	left[0] = 50 // interactive returns
	if got := serveNext(sched, left); got != 0 {
		t.Fatalf("first probed class after interactive returns = %s, want interactive", name(got))
	}
}

// TestZeroWeightClassIsNeverProbed: a zero-weight class is disabled — it is
// never probed and never served. Production configuration rejects zero
// weights (parseWeights), this pins the scheduler's own behaviour.
func TestZeroWeightClassIsNeverProbed(t *testing.T) {
	sched := newClassScheduler([]int{2, 0, 1})
	left := []int{5, 5, 5}
	for i := 0; i < 12; i++ {
		if got := serveNext(sched, left); got == 1 {
			t.Fatal("zero-weight class was probed and served")
		}
	}
	if left[1] != 5 {
		t.Fatalf("zero-weight class consumed %d messages", 5-left[1])
	}
}

// TestAllZeroWeightsIsANoOp: even with a (rejected) all-zero weight vector
// the scheduler must not panic, loop or serve anything.
func TestAllZeroWeightsIsANoOp(t *testing.T) {
	sched := newClassScheduler([]int{0, 0, 0})
	if got := serveNext(sched, []int{5, 5, 5}); got != -1 {
		t.Fatalf("all-zero weights served class %s, want nothing", name(got))
	}
	if order := sched.order(); len(order) != 0 {
		t.Fatalf("all-zero weights produced an order: %v", order)
	}
}

// TestUnconfiguredSchedulerIsANoOp: no configured classes = nothing to do.
func TestUnconfiguredSchedulerIsANoOp(t *testing.T) {
	sched := newClassScheduler(nil)
	if got := serveNext(sched, nil); got != -1 {
		t.Fatalf("unconfigured scheduler served class %d", got)
	}
}
