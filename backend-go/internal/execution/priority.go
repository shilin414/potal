package execution

// classScheduler implements the weighted fair scheduling POLICY across the
// priority class streams (P1-1). It is deliberately pure: the worker's
// read loop owns the Redis probes, this type only decides which class to
// try next and how the credit accounting evolves.
//
// Rules (Admission Fairness & Distributed Lease Hardening, Phase 1):
//
//  1. only classes with remaining credit participate in a round, highest
//     weight first — interactive (7) outranks retry (1) and scheduled (2);
//  2. a credited class whose queue is EMPTY forfeits the rest of its round
//     credit (markEmpty). An idle class can never pin the round open: the
//     previous policy refilled only when every credit reached 0, so an
//     empty retry stream left the credits pinned at [0,1,0] forever and
//     scheduled work starved behind continuous interactive traffic;
//  3. once every credit is exhausted a new round starts;
//  4. an active class keeps its full share: with all three classes busy a
//     round serves exactly 7:1:2 before refilling, and with only one class
//     busy that class receives 100% of the worker's capacity (the refill
//     happens as soon as its quota and the idle classes' quotas are spent).
//
// The class order is [interactive, retry, scheduled] (see PriorityClasses);
// weights are configurable via RUN_PRIORITY_WEIGHTS and validated to be
// strictly positive, so a class can never be silently starved by weight 0.
type classScheduler struct {
	weights []int
	credits []int
}

func newClassScheduler(weights []int) *classScheduler {
	w := make([]int, len(weights))
	copy(w, weights)
	s := &classScheduler{weights: w, credits: make([]int, len(w))}
	copy(s.credits, w)
	return s
}

// order lists the class indexes to probe, in priority order, for the next
// message. Only classes with remaining credit are returned: a class without
// credit either consumed its round share or was marked empty. An empty
// result means there is nothing left to probe this round (no classes
// configured, or no non-zero weights).
func (s *classScheduler) order() []int {
	if len(s.weights) == 0 {
		return nil
	}
	if s.allExhausted() {
		copy(s.credits, s.weights)
	}
	out := make([]int, 0, len(s.weights))
	for i, credit := range s.credits {
		if credit > 0 {
			out = append(out, i)
		}
	}
	return out
}

// consume records that class idx served a message: one credit per message.
func (s *classScheduler) consume(idx int) {
	if idx < 0 || idx >= len(s.credits) {
		return
	}
	if s.credits[idx] > 0 {
		s.credits[idx]--
	}
}

// markEmpty records that class idx had NOTHING queued when probed: the class
// forfeits whatever credit it had left for this round. Without this an idle
// credited class keeps the round open forever (allExhausted never turns
// true), which is exactly how an empty retry stream starved scheduled work.
// The class becomes active again automatically after the next refill — no
// state needs restoring.
func (s *classScheduler) markEmpty(idx int) {
	if idx < 0 || idx >= len(s.credits) {
		return
	}
	s.credits[idx] = 0
}

func (s *classScheduler) allExhausted() bool {
	for _, credit := range s.credits {
		if credit > 0 {
			return false
		}
	}
	return true
}
