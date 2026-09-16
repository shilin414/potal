package sse

import (
	"sync"
	"sync/atomic"

	"github.com/creation-agent-studio/backend-go/internal/execution"
)

// Subscriber drop reasons. They are the `reason` label of
// studio_sse_hub_subscriber_dropped_total and are therefore a bounded
// enumeration — never a run id, a user id or an error string.
const (
	// DropReasonSlowConsumer: the local queue hit its event or byte limit.
	// Only this subscriber is disconnected; the durable cursor lets it
	// reconnect and replay what it missed.
	DropReasonSlowConsumer = "slow_consumer"
	// DropReasonHubClosed: the hub was shut down (manager Close, idle
	// eviction).
	DropReasonHubClosed = "hub_closed"
	// DropReasonUpstreamClosed: the Redis upstream died, so this hub can no
	// longer see live events; the client reconnects and gets a fresh hub.
	DropReasonUpstreamClosed = "upstream_closed"
	// DropReasonClientGone is the NORMAL end of a stream: the HTTP client
	// disconnected. It is deliberately not part of the dropped counter — it
	// is not a drop, and counting it would drown the real signal.
	DropReasonClientGone = "client_gone"
)

// Subscriber is one HTTP SSE connection's local queue inside its RunHub.
//
// The struct carries the two things that MUST stay per-connection (AC-2):
//
//   - protocol — the rendering capability this connection negotiated, so a
//     legacy client never receives a transient delta while a v2 client on the
//     same run does;
//   - the queue itself — a slow browser must not be able to apply
//     backpressure to the hub, the Redis upstream, or any other viewer.
//
// It deliberately does NOT carry a durable cursor. The cursor lives in the
// HTTP writer (Gateway.Stream), because that is the only place that knows
// what has actually been written to the socket.
type Subscriber struct {
	id       uint64
	protocol int

	events chan HubEvent
	done   chan struct{}

	// maxBytes bounds the pending payload of this queue. The event count is
	// bounded by the channel capacity, which the hub sizes from
	// SubscriberMaxEvents — the two limits are enforced together because a
	// count-only or byte-only bound is trivially defeated by the other
	// dimension.
	maxBytes    int64
	queuedBytes atomic.Int64

	closeOnce sync.Once
	reason    atomic.Value // string

	// onClose runs exactly once, from whichever goroutine wins the close
	// race. The hub uses it to unregister the subscriber and update the
	// gauges, so a subscriber can be closed by the HTTP handler (client
	// disconnect) without the hub leaking a map entry.
	onClose func(*Subscriber)
}

func newSubscriber(id uint64, protocol, queueEvents int, maxBytes int64) *Subscriber {
	if queueEvents < 1 {
		queueEvents = 1
	}
	return &Subscriber{
		id:       id,
		protocol: protocol,
		events:   make(chan HubEvent, queueEvents),
		done:     make(chan struct{}),
		maxBytes: maxBytes,
	}
}

// ID is the hub-local subscriber identity (monotonic, never reused).
func (s *Subscriber) ID() uint64 { return s.id }

// Protocol is the negotiated rendering capability of THIS connection.
func (s *Subscriber) Protocol() int { return s.protocol }

// Events is the queue the HTTP writer drains.
func (s *Subscriber) Events() <-chan HubEvent { return s.events }

// Done is closed when the subscriber is dropped (slow consumer, hub closed,
// upstream lost). The writer selects on it so the stream ends promptly
// instead of waiting for an event that will never come.
func (s *Subscriber) Done() <-chan struct{} { return s.done }

// DropReason is "" until Close has been called.
func (s *Subscriber) DropReason() string {
	if v, ok := s.reason.Load().(string); ok {
		return v
	}
	return ""
}

// QueuedBytes is the pending payload footprint of this queue. Exposed for
// tests and for the memory-bounded acceptance criterion.
func (s *Subscriber) QueuedBytes() int64 { return s.queuedBytes.Load() }

// accepts reports whether this subscriber may receive ev.
//
// Capability filtering belongs HERE, at the subscriber, never at the hub
// (AC-2/AC-3): the hub has no protocol, and a hub-level protocol would either
// strip transient deltas from v2 clients or hand them to legacy ones.
//
// ONLY content.delta is filtered. Sequence 0 is a transport marker, not a
// feature: a future transient control frame that needs no byte-range
// reconciliation must still reach legacy clients, so "every sequence-0 frame"
// would be the wrong predicate. The durable chunk path is untouched either
// way, which is what keeps the final answer complete for legacy clients.
func (s *Subscriber) accepts(ev HubEvent) bool {
	if ev.Sequence == 0 && ev.EventType == execution.EventContentDelta &&
		s.protocol < StreamProtocolRangeDelta {
		return false
	}
	return true
}

// Offer enqueues ev without ever blocking the caller. It returns "" on
// success, or the drop reason when this subscriber must be disconnected.
//
// Blocking fan-out (`sub.events <- ev`) is the single failure mode this
// method exists to prevent: one browser that stops reading would block the
// hub's fan-out loop, which stops every other viewer on the run AND lets the
// Redis channel backlog grow until the whole stream collapses.
//
// Byte reservation happens first so an oversized event is refused even when
// the queue is nearly empty; the channel's non-blocking send then enforces
// the event-count bound. Both paths roll the reservation back.
func (s *Subscriber) Offer(ev HubEvent) string {
	select {
	case <-s.done:
		return DropReasonHubClosed
	default:
	}
	if s.maxBytes > 0 {
		queued := s.queuedBytes.Add(int64(ev.ApproxBytes))
		if queued > s.maxBytes {
			s.queuedBytes.Add(-int64(ev.ApproxBytes))
			return DropReasonSlowConsumer
		}
	}
	select {
	case s.events <- ev:
		return ""
	default:
		if s.maxBytes > 0 {
			s.queuedBytes.Add(-int64(ev.ApproxBytes))
		}
		return DropReasonSlowConsumer
	}
}

// Release returns an event's bytes to the budget. The writer MUST call it for
// every event it takes off the channel (§18): releasing only at Close would
// make queuedBytes grow monotonically with connection lifetime, so every
// long-lived subscriber would eventually be misclassified as slow.
func (s *Subscriber) Release(ev HubEvent) {
	if s.maxBytes > 0 {
		s.queuedBytes.Add(-int64(ev.ApproxBytes))
	}
}

// Close drops the subscriber, recording WHY. It is idempotent and reports
// whether this call won the race — the hub increments the dropped counter
// only for the winning call, so a disconnect that the client initiated is
// never counted as a hub-initiated drop.
//
// The events channel is intentionally NOT closed here: Close can run
// concurrently with an Offer from the fan-out loop, and closing a channel a
// sender may still write to panics. Consumers observe Done() instead, which
// is safe from any goroutine.
func (s *Subscriber) Close(reason string) bool {
	closed := false
	s.closeOnce.Do(func() {
		s.reason.Store(reason)
		close(s.done)
		if s.onClose != nil {
			s.onClose(s)
		}
		closed = true
	})
	return closed
}
