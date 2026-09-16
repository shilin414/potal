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
//  3. The bounds are HARD, including the byte bound (Batch 4.1). An event
//     whose own weight exceeds maxBytes is not retained at all — it is not
//     truncated and not squeezed in as "the newest one". A cache is allowed to
//     miss; a memory bound that one `run.completed` carrying the whole answer
//     can blow through is not a bound. The segment is invalidated instead, so
//     every cursor below it falls back to MySQL, where the event still exists
//     in full. With that rule the ring can always evict its way back under both
//     limits, so `Bytes() <= maxBytes` holds at every instant.
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

	// lastObservedSeq is the highest sequence the ring has ever been OFFERED,
	// which is not the same thing as lastSeq (§19): the segment can be thrown
	// away (a gap, or an event too large to retain) while the fact that we are
	// past that sequence stays true. Keeping them apart is what stops a late
	// Redis frame from walking the ring backwards — after an oversized event at
	// 5 has invalidated the segment, a straggler carrying 3 must not be
	// appended to a segment that now starts at 6.
	lastObservedSeq uint64
}

// NewDurableRing builds a ring bounded by maxEvents AND maxBytes. A
// non-positive bound disables that half of the limit; a ring with both
// disabled is unbounded and must never be wired from configuration (the
// normalized HubOptions always supply both defaults).
func NewDurableRing(maxEvents int, maxBytes int64) *DurableRing {
	return &DurableRing{maxEvents: maxEvents, maxBytes: maxBytes}
}

// Remember offers one event to the cache. It reports how many cached events
// were discarded (the caller folds that into the cache gauges) and whether the
// RETAINED SEGMENT was invalidated — i.e. the ring no longer covers what it
// held before, because a sequence was missing or because this event is too
// large to retain.
//
// Events at or below the observed high-water mark are ignored: the same
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

	if ev.Sequence <= r.lastObservedSeq {
		return 0, false
	}

	live := len(r.buf) - r.head
	if r.lastObservedSeq > 0 && ev.Sequence != r.lastObservedSeq+1 {
		// A sequence is missing, so nothing retained can answer a cursor
		// inside the hole. Fail closed: drop the whole segment.
		evicted = live
		r.resetSegmentLocked()
		discontinuous = true
	}

	r.lastObservedSeq = ev.Sequence

	if r.maxBytes > 0 && int64(ev.ApproxBytes) > r.maxBytes {
		// Bigger than the entire budget. Retaining it would make
		// `Bytes() <= maxBytes` false no matter how much else is evicted —
		// and truncating it is not an option either, because a cached event
		// must be the event. So retain NOTHING here: the ring keeps its
		// high-water mark (it has seen this sequence) and loses its
		// coverage, which turns every cursor at or below this event into a
		// MySQL read. The event itself is untouched on the wire and in the
		// log — not cached ≠ not sent ≠ lost (AC-4.1-2).
		evicted += len(r.buf) - r.head
		r.resetSegmentLocked()
		return evicted, true
	}

	if len(r.buf) == r.head {
		r.firstSeq = ev.Sequence
	}
	r.buf = append(r.buf, ev)
	r.bytes += int64(ev.ApproxBytes)
	r.lastSeq = ev.Sequence
	r.compactLocked()
	return evicted + r.evictLocked(), discontinuous
}

// resetSegmentLocked discards every retained entry. lastObservedSeq is NOT
// touched: the ring has still seen that position, and forgetting it would let
// a late frame re-open a range the ring already gave up on.
func (r *DurableRing) resetSegmentLocked() {
	for i := range r.buf {
		r.buf[i] = HubEvent{}
	}
	r.buf = r.buf[:0]
	r.head = 0
	r.bytes = 0
	r.firstSeq = 0
	r.lastSeq = 0
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

// ObservedHighWater is the highest sequence the ring has been offered,
// retained or not (§19). It is deliberately NOT HighWater: an event too large
// to cache is still an event the ring has moved past, and the difference is
// what keeps a late frame from reopening an abandoned range.
func (r *DurableRing) ObservedHighWater() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastObservedSeq
}

// evictLocked drops the oldest entries until the ring fits both bounds.
//
// It may empty the ring: an oversized event is refused before it is appended
// (see Remember), so every retained entry is individually within the byte
// budget and dropping entries always brings the ring back under both limits.
// The old "keep the newest entry whatever its size" exemption is exactly what
// made maxBytes advisory — one 4 KiB `run.completed` under `CacheMaxBytes=1024`
// stayed resident forever (Batch 4.1, AC-4.1-1).
//
// An empty ring is a MISS, not a wrong hit: firstSeq/lastSeq reset to 0 and
// After() refuses every cursor, which sends the client to MySQL for a full
// replay. That is the cheap direction of the only trade this cache makes.
func (r *DurableRing) evictLocked() int {
	evicted := 0
	for {
		live := len(r.buf) - r.head
		if live == 0 {
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
	} else {
		r.firstSeq = 0
		r.lastSeq = 0
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
