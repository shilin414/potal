package execution

// classScheduler implements the weighted fair scheduling POLICY across the
// priority class streams (P1-1). It is deliberately pure: the worker's
// read loop owns the Redis probes, this type only decides which class to
// try next and how the credit accounting evolves.
//
// Rules:
//
//  1. classes with remaining credit are probed first, highest weight
//     first — interactive (7) outranks retry (1) and scheduled (2);
//  2. once a class's credit is exhausted it yields to the others in the
//     same round, so scheduled work gets its guaranteed share and cannot
//     starve behind a continuous interactive flow;
//  3. when every credit is exhausted a new round starts;
//  4. if all credited classes are EMPTY, the remaining classes are
//     borrowed (probed without consuming credit) — an idle class never
//     wastes the worker's capacity.
//
// A round of weights [7,1,2] therefore serves at most 7 interactive, 1
// retry and 2 scheduled messages before refilling, i.e. a real 7:1:2
// share instead of FIFO or strict priority.
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
// message: credited classes first, then the borrowed ones. An empty
// result means there is nothing to probe (no classes configured).
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
	for i, credit := range s.credits {
		if credit <= 0 {
			out = append(out, i)
		}
	}
	return out
}

// consume records that class idx served a message. Serving a class with
// credit spends one credit; a borrowed class (credit already 0) spends
// nothing — borrowing is bounded by the credited classes' emptiness, not
// by a quota.
func (s *classScheduler) consume(idx int) {
	if idx < 0 || idx >= len(s.credits) {
		return
	}
	if s.credits[idx] > 0 {
		s.credits[idx]--
	}
}

func (s *classScheduler) allExhausted() bool {
	for _, credit := range s.credits {
		if credit > 0 {
			return false
		}
	}
	return true
}
