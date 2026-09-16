package sse

import "sync"

// DurableRing is the Hub's bounded, CONTIGUOUS tail cache of DURABLE run
// events (sequence > 0).
//
// Two invariants define it, and both are load-bearing:
//
//  1. TRANSIENT frames are never cached. `content.delta` carries sequence 0 —
//     a transport marker, not a position — so it can never satisfy a durable
//     cursor. A reconnect asks "everything after sequence N"; a cached
//     transient frame cannot answer that question, and caching it would burn
//     the byte budget the durable tail needs to answer it. Remember() refuses
//     every sequence-0 event, whatever its type.
//
//  2. The cached range is always CONTIGUOUS. Redis pub/sub is at-most-once:
//     a subscriber can miss a message (a reconnect inside the transport, a
//     slow local reader hitting WithChannelSendTimeout). If the ring simply
//     appended 107 after 103 it would claim to cover 104-106, and a client
//     resuming from 103 would be told "cache hit" — it would never be sent
//     104-106 while its cursor kept advancing past them. So a sequence gap
//     DISCARDS the previous segment and restarts at the new sequence: every
//     older cursor then falls back to MySQL. Fail-closed, never fail-open.
//
// The cache is an OPTIMISATION, never a source of truth — MySQL can always
// answer the same question — and that is exactly what makes "reset on gap"
// safe. A wrong "hit" loses events; a wrong "miss" costs one database read.
//
// The ring is bounded by event COUNT *and* bytes. A count-only bound is not
// enough: one `content.chunk` can carry a large slice of an answer, so a
// ring that holds 2048 of them can hold far more than the memory budget.
type DurableRing struct {
	mu sync.Mutex

	// buf[head:] are the live entries, ascending by Sequence. Entries before
	// head are already evicted and their payload references cleared.
	buf  []HubEvent
	head int

	bytes int64

	maxEvents int
	maxBytes  int64

	firstSeq uint64
	lastSeq  uint64
}

// NewDurableRing builds a ring bounded by maxEvents AND maxBytes. A
// non-positive bound disables that half of the limit; a ring with both
// disabled is unbounded and must never be wired from configuration (the
// normalized HubOptions always supply both defaults).
func NewDurableRing(maxEvents int, maxBytes int64) *DurableRing {
	return &DurableRing{maxEvents: maxEvents, maxBytes: maxBytes}
}

// Remember offers one event to the cache. It reports how many cached events
// were evicted (the caller folds that into the cache gauges) and whether this
// event was DISCONTINUOUS with what the ring held — i.e. the previous
// segment was thrown away because a sequence was missing.
//
// Events at or below the current high-water mark are ignored: the same
// durable event legitimately arrives twice (a re-publish, the overlap between
// the DB replay and the live path), and appending a copy would break the
// "contiguous, ascending" invariant the replay path depends on. The ring never
// moves backwards: re-seeing an OLD event must not resurrect a cursor the
// ring already failed to cover.
func (r *DurableRing) Remember(ev HubEvent) (evicted int, discontinuous bool) {
	if ev.Sequence == 0 {
		return 0, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	live := len(r.buf) - r.head
	switch {
	case live == 0:
		r.firstSeq = ev.Sequence
	case ev.Sequence <= r.lastSeq:
		return 0, false
	case ev.Sequence == r.lastSeq+1:
		// Contiguous: the normal live path.
	default:
		evicted = live
		r.buf = r.buf[:0]
		r.head = 0
		r.bytes = 0
		r.firstSeq = ev.Sequence
		discontinuous = true
	}

	r.buf = append(r.buf, ev)
	r.bytes += int64(ev.ApproxBytes)
	r.lastSeq = ev.Sequence
	r.compactLocked()
	return evicted + r.evictLocked(), discontinuous
}

// After returns the cached events with sequence > after and whether the cache
// may serve that cursor at all.
//
// A true second value means "this cache is contiguous from the oldest entry
// through lastSeq, and `after` is not older than that" — i.e. whatever the
// client is missing at the tail is here. A false value is NOT an error: it is
// the signal to replay from MySQL instead, which is why it is a boolean and
// not an error.
func (r *DurableRing) After(after uint64) ([]HubEvent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	live := len(r.buf) - r.head
	if live == 0 {
		return nil, false
	}
	// Every cached event has sequence >= 1, so firstSeq-1 cannot underflow;
	// `after == firstSeq-1` is the first cursor the ring can serve in full.
	if after < r.firstSeq-1 {
		return nil, false
	}
	if after >= r.lastSeq {
		// The client is already at (or ahead of) the cache high-water mark.
		// Nothing to replay from here, but the caller still runs its DB tail
		// reconciliation from `after`, which is what finds anything newer.
		return nil, true
	}
	out := make([]HubEvent, 0, live)
	for _, ev := range r.buf[r.head:] {
		if ev.Sequence > after {
			out = append(out, ev)
		}
	}
	return out, true
}

// HighWater is the newest cached sequence (0 when empty).
func (r *DurableRing) HighWater() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.buf) == r.head {
		return 0
	}
	return r.lastSeq
}

// Len is the number of cached events.
func (r *DurableRing) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.buf) - r.head
}

// Bytes is the approximate payload footprint of the cached events.
func (r *DurableRing) Bytes() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bytes
}

// FirstSeq is the oldest cached sequence (0 when empty). Test/observability
// accessor for the continuity assertion.
func (r *DurableRing) FirstSeq() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.buf) == r.head {
		return 0
	}
	return r.firstSeq
}

// evictLocked drops the oldest entries until the ring fits both bounds. At
// least the newest entry always survives: a cache that evicted the event it
// was just handed could never serve any cursor, and dropping below one entry
// would leave firstSeq/lastSeq meaningless.
func (r *DurableRing) evictLocked() int {
	evicted := 0
	for {
		live := len(r.buf) - r.head
		if live <= 1 {
			break
		}
		overEvents := r.maxEvents > 0 && live > r.maxEvents
		overBytes := r.maxBytes > 0 && r.bytes > r.maxBytes
		if !overEvents && !overBytes {
			break
		}
		r.bytes -= int64(r.buf[r.head].ApproxBytes)
		r.buf[r.head] = HubEvent{} // release the payload map
		r.head++
		evicted++
	}
	if len(r.buf) > r.head {
		r.firstSeq = r.buf[r.head].Sequence
	}
	return evicted
}

// compactLocked reclaims the evicted prefix. Without it, `buf[head:]` keeps
// the stale head slots reachable from the slice header, so the ring would
// hold roughly twice its bound between compactions. Compaction runs only once
// the dead prefix is at least half the array, keeping the amortised cost O(1)
// per event.
func (r *DurableRing) compactLocked() {
	if r.head == 0 || r.head*2 < len(r.buf) {
		return
	}
	n := copy(r.buf, r.buf[r.head:])
	for i := n; i < len(r.buf); i++ {
		r.buf[i] = HubEvent{}
	}
	r.buf = r.buf[:n]
	r.head = 0
}
