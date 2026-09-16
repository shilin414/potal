package sse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/platform/redisx"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
)

// This file implements the PROCESS-LOCAL SSE Hub (Batch 4).
//
// The problem it solves is fan-out cost, not the wire protocol:
//
//	before:  N viewers of one run → N Redis subscriptions → N JSON decodes
//	                              → N replay/live overlap reconciliations
//	after:   N viewers of one run → 1 RunHub → 1 Redis subscription
//	                              → 1 decode → N local subscribers
//
// With three studio-stream instances and a run watched from all three, the
// ceiling becomes three subscriptions instead of one per viewer. That is the
// whole design: there is deliberately NO distributed ownership, no Redis
// lock, no leader election and no cross-instance routing in this batch.
//
// What a RunHub owns, and why each part is load-bearing:
//
//	single upstream      one Redis pub/sub subscription per (process, run)
//	subscriber capability the negotiated protocol is PER CONNECTION
//	subscriber queue     a slow browser can only drop itself
//	durable tail cache   bounded, contiguous, never transient
//	catch-up barrier     live frames buffer until the replay has drained
//	terminal boundary     no event is fanned out after the terminal one
//	idle lifecycle       short reuse window, then eviction
//
// The external SSE contract is UNCHANGED: same frames, same `id:` rule, same
// `after` / Last-Event-ID precedence, same `stream_protocol` negotiation.
// The frontend cannot tell this batch happened — which is also why no
// frontend change ships with it.

// hubEventOverheadBytes approximates the per-frame bookkeeping a queued event
// costs beyond its JSON body (struct, slice slot, envelope keys). It keeps the
// byte budget honest for payloads too small for their own length to matter.
const hubEventOverheadBytes = 64

// HubEvent is one decoded run event fanned out to local subscribers.
//
// A HubEvent is IMMUTABLE once built: every subscriber holds the same Payload
// map, so the hub must never mutate it after dispatch (no per-subscriber
// rewriting of the payload — filtering happens by not sending, never by
// editing).
type HubEvent struct {
	Sequence  uint64
	EventType string
	Payload   map[string]any

	// ApproxBytes is the flow-control weight of this event, used by the
	// subscriber byte budget. It approximates the encoded frame size, which
	// is the cost that matters for memory and for socket write time.
	ApproxBytes int
}

// IsTransient reports whether the event is a transient (non-durable) frame.
// Sequence 0 is a transport marker: it never advances a durable cursor and is
// never cached.
func (e HubEvent) IsTransient() bool { return e.Sequence == 0 }

// IsTerminal reports whether the event ends the run's lifecycle.
func (e HubEvent) IsTerminal() bool { return execution.IsTerminalEventName(e.EventType) }

func hubEvent(sequence uint64, eventType string, payload map[string]any) HubEvent {
	weight := hubEventOverheadBytes + len(eventType)
	if raw, err := json.Marshal(payload); err == nil {
		weight += len(raw)
	}
	return HubEvent{Sequence: sequence, EventType: eventType, Payload: payload, ApproxBytes: weight}
}

// decodeHubEvent parses one Redis payload into an immutable HubEvent. It runs
// ONCE per event per hub, not once per subscriber — that de-duplication is a
// large part of the point of the hub.
func decodeHubEvent(raw []byte) (HubEvent, error) {
	var frame struct {
		Sequence  uint64         `json:"sequence"`
		EventType string         `json:"event_type"`
		Payload   map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		return HubEvent{}, err
	}
	return HubEvent{
		Sequence:    frame.Sequence,
		EventType:   frame.EventType,
		Payload:     frame.Payload,
		ApproxBytes: len(raw) + hubEventOverheadBytes,
	}, nil
}

// HubOptions bounds every buffer in the hub. All values are clamped up to the
// defaults when non-positive, so a typo in configuration can never disable a
// bound (see HubOptions.normalized).
type HubOptions struct {
	CacheMaxEvents      int
	CacheMaxBytes       int64
	SubscriberMaxEvents int
	SubscriberMaxBytes  int64
	IdleTTL             time.Duration

	// UpstreamReadyTimeout bounds how long a request waits for the hub's
	// Redis subscription to be CONFIRMED. It is a latency guard, not a
	// correctness one: on expiry the request is served a durable-only replay
	// and the client reconnects, at which point the hub is normally ready.
	UpstreamReadyTimeout time.Duration
}

const (
	// DefaultHubCacheEvents / DefaultHubCacheBytes: 2048 events AND 8 MiB per
	// active run. Both are needed — one content.chunk can be large.
	DefaultHubCacheEvents = 2048
	DefaultHubCacheBytes  = int64(8 << 20)
	// DefaultHubSubscriberEvents / DefaultHubSubscriberBytes: the pending
	// live queue of ONE connection (1024 frames / 4 MiB). Exceeding either
	// drops that connection only.
	DefaultHubSubscriberEvents = 1024
	DefaultHubSubscriberBytes  = int64(4 << 20)
	// DefaultHubIdleTTL is how long a hub survives with zero subscribers.
	// The frontend reconnects ~2s after a drop; evicting instantly would
	// destroy and rebuild the Redis subscription on every blip.
	DefaultHubIdleTTL = 30 * time.Second
	// DefaultUpstreamReadyTimeout is the cap on waiting for SUBSCRIBE
	// confirmation before a request degrades to a durable-only replay.
	DefaultUpstreamReadyTimeout = 10 * time.Second
)

// DefaultHubOptions returns the production defaults (also the fallbacks used
// by HubOptions.normalized).
func DefaultHubOptions() HubOptions {
	return HubOptions{
		CacheMaxEvents:       DefaultHubCacheEvents,
		CacheMaxBytes:        DefaultHubCacheBytes,
		SubscriberMaxEvents:  DefaultHubSubscriberEvents,
		SubscriberMaxBytes:   DefaultHubSubscriberBytes,
		IdleTTL:              DefaultHubIdleTTL,
		UpstreamReadyTimeout: DefaultUpstreamReadyTimeout,
	}
}

// normalized fills every unset or non-positive bound with its default. A
// zero/negative limit is never interpreted as "unlimited": these are memory
// bounds on a long-lived process, and the only safe reading of a malformed
// value is the documented default.
func (o HubOptions) normalized() HubOptions {
	def := DefaultHubOptions()
	if o.CacheMaxEvents <= 0 {
		o.CacheMaxEvents = def.CacheMaxEvents
	}
	if o.CacheMaxBytes <= 0 {
		o.CacheMaxBytes = def.CacheMaxBytes
	}
	if o.SubscriberMaxEvents <= 0 {
		o.SubscriberMaxEvents = def.SubscriberMaxEvents
	}
	if o.SubscriberMaxBytes <= 0 {
		o.SubscriberMaxBytes = def.SubscriberMaxBytes
	}
	if o.IdleTTL <= 0 {
		o.IdleTTL = def.IdleTTL
	}
	if o.UpstreamReadyTimeout <= 0 {
		o.UpstreamReadyTimeout = def.UpstreamReadyTimeout
	}
	return o
}

// HubUpstream is the Redis pub/sub surface one RunHub needs: open ONE
// subscription for a run and hand back its messages.
//
// It is an interface so the hub's real invariants — one subscription per run,
// decode once, drop only the slow subscriber — can be asserted without a live
// Redis. Tests count Subscribe calls; production wires RedisUpstream.
type HubUpstream interface {
	Subscribe(ctx context.Context, runID string) (HubUpstreamChannel, error)
}

// HubUpstreamChannel is one confirmed subscription.
type HubUpstreamChannel interface {
	// Payloads yields raw event JSON. The channel is closed when the
	// subscription ends.
	Payloads() <-chan []byte
	// Err explains why Payloads ended, when it ended for a reason other than
	// the hub closing it. It is read once, by the hub, after the channel
	// closes.
	Err() error
	Close() error
}

// errUpstreamChannelClosed marks a subscription that ended while the hub was
// still interested in it — the "receive" failure stage.
var errUpstreamChannelClosed = errors.New("sse hub: redis pub/sub channel closed")

// RedisUpstream is the production HubUpstream. Every hub gets ONE of these
// per run, which is the entire point of Batch 4.
type RedisUpstream struct {
	redis *redisx.Client
}

// NewRedisUpstream wraps the shared Redis client. A nil client yields an
// upstream that always fails, so a process without Redis degrades to
// durable-only streams instead of panicking.
func NewRedisUpstream(redis *redisx.Client) *RedisUpstream { return &RedisUpstream{redis: redis} }

// upstreamForwardBuffer is the hop between go-redis's message channel and the
// hub's payload channel. The hub decodes without blocking the transport.
const upstreamForwardBuffer = 256

func (u *RedisUpstream) Subscribe(ctx context.Context, runID string) (HubUpstreamChannel, error) {
	if u == nil || u.redis == nil {
		return nil, errors.New("sse hub: redis upstream not configured")
	}
	pubsub := u.redis.Subscribe(ctx, u.redis.RunEventsChannel(runID))
	// Wait for the SUBSCRIBE confirmation BEFORE returning. Until this
	// succeeds the hub cannot claim "no event can be missed", and the
	// register-before-replay guarantee is built on exactly that claim.
	if _, err := pubsub.Receive(ctx); err != nil {
		_ = pubsub.Close()
		return nil, fmt.Errorf("subscribe %s: %w", u.redis.RunEventsChannel(runID), err)
	}
	// The channel parameters are the pre-Batch-4 ones: the hub lowers the
	// subscription COUNT, it does not change the transport's buffering.
	raw := pubsub.Channel(
		goredis.WithChannelSize(4096),
		goredis.WithChannelSendTimeout(5*time.Second),
	)
	c := &redisUpstreamChannel{pubsub: pubsub, out: make(chan []byte, upstreamForwardBuffer)}
	go c.forward(ctx, raw)
	return c, nil
}

type redisUpstreamChannel struct {
	pubsub *goredis.PubSub
	out    chan []byte

	mu  sync.Mutex
	err error
}

func (c *redisUpstreamChannel) Payloads() <-chan []byte { return c.out }

func (c *redisUpstreamChannel) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *redisUpstreamChannel) Close() error { return c.pubsub.Close() }

// forward funnels go-redis's *Message channel into a payload channel so the
// hub depends on the payloads, not on the driver type.
//
// A close while the hub is still interested is reported as an error: that is
// the "receive" failure stage (a dropped connection, a server-side
// unsubscribe). A close after the hub cancelled its context is the hub's own
// shutdown and carries no error.
func (c *redisUpstreamChannel) forward(ctx context.Context, raw <-chan *goredis.Message) {
	defer close(c.out)
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-raw:
			if !ok {
				if ctx.Err() == nil {
					c.mu.Lock()
					if c.err == nil {
						c.err = errUpstreamChannelClosed
					}
					c.mu.Unlock()
				}
				return
			}
			select {
			case c.out <- []byte(msg.Payload):
			case <-ctx.Done():
				return
			}
		}
	}
}

// unavailableUpstream fails every subscription. It is what a manager built
// without Redis uses, so "no Redis" means "durable-only streams" rather than
// a nil dereference.
type unavailableUpstream struct{}

func (unavailableUpstream) Subscribe(context.Context, string) (HubUpstreamChannel, error) {
	return nil, errors.New("sse hub: redis upstream not configured")
}

// HubManager is the process-local registry of RunHubs.
//
// ONE HUB PER RUN is enforced here, under a single mutex, because the
// alternative — letting each request build its own hub — silently restores
// the per-connection Redis subscription this batch removes.
//
// Lock discipline: manager.mu is NEVER held while a RunHub method that takes
// hub.mu is called, and hub.mu is never held while manager.mu is taken. The
// two locks are therefore independent, not nested.
type HubManager struct {
	mu     sync.Mutex
	hubs   map[string]*RunHub
	closed bool

	// cacheStats mirrors each live hub's cache size so the process-wide
	// cache gauges can be a real SUM (several runs have several hubs).
	cacheStats map[string]hubCacheStat

	// ctx is the RUNTIME context: derived from context.Background(), not from
	// the caller's startup context (see NewHubManager).
	ctx    context.Context
	cancel context.CancelFunc

	upstream HubUpstream
	metrics  *telemetry.Metrics
	opts     HubOptions

	nextSubscriberID atomic.Uint64
}

type hubCacheStat struct {
	events int
	bytes  int64
}

// NewHubManager builds the production manager over Redis.
//
// startupCtx is the context the process used to BOOTSTRAP (studio-stream calls
// app.Build with a 30-second timeout). It is deliberately NOT the parent of
// the hub runtime context: deriving the hubs from it would cancel every open
// stream 30 seconds after process start — a defect that only shows up in a
// long-running process, never in a test that finishes quickly. The hub runtime
// lives until Close (wired to App.Close, before Redis.Close).
func NewHubManager(startupCtx context.Context, redis *redisx.Client, metrics *telemetry.Metrics, opts HubOptions) *HubManager {
	return newHubManager(startupCtx, NewRedisUpstream(redis), metrics, opts)
}

// NewHubManagerWithUpstream is NewHubManager with an injectable upstream, for
// tests that must count subscriptions without a live Redis.
func NewHubManagerWithUpstream(startupCtx context.Context, upstream HubUpstream, metrics *telemetry.Metrics, opts HubOptions) *HubManager {
	return newHubManager(startupCtx, upstream, metrics, opts)
}

func newHubManager(startupCtx context.Context, upstream HubUpstream, metrics *telemetry.Metrics, opts HubOptions) *HubManager {
	// startupCtx is intentionally unused beyond documenting intent (AC-11).
	_ = startupCtx
	if upstream == nil {
		upstream = unavailableUpstream{}
	}
	runtimeCtx, cancel := context.WithCancel(context.Background())
	return &HubManager{
		hubs:       make(map[string]*RunHub),
		cacheStats: make(map[string]hubCacheStat),
		ctx:        runtimeCtx,
		cancel:     cancel,
		upstream:   upstream,
		metrics:    metrics,
		opts:       opts.normalized(),
	}
}

// Lookup returns the hub for runID when it exists AND can still serve live
// events. It never creates one.
func (m *HubManager) Lookup(runID string) *RunHub {
	m.mu.Lock()
	hub := m.hubs[runID]
	m.mu.Unlock()
	if hub == nil || !hub.Serving() {
		return nil
	}
	return hub
}

// GetOrCreate returns the run's hub, creating it (and starting its single
// Redis upstream) on first use. Concurrent callers cannot create two hubs for
// one run: the map is consulted and updated under one lock, and the check is
// on the CURRENT entry, so a hub that has become unusable is replaced rather
// than reused.
func (m *HubManager) GetOrCreate(runID string) *RunHub {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	if hub := m.hubs[runID]; hub != nil && hub.Serving() {
		m.mu.Unlock()
		return hub
	}
	hub := newRunHub(m, runID)
	m.hubs[runID] = hub
	if m.metrics != nil {
		m.metrics.SSEHubActive.Set(float64(len(m.hubs)))
		m.metrics.SSEHubCreatedTotal.Inc()
	}
	m.mu.Unlock()

	go hub.runUpstream()
	return hub
}

// removeIfSame deletes runID's hub ONLY if the map still holds this exact
// instance (AC-10).
//
// This is the idle-timer generation guard. Sequence: an old hub's idle timer
// fires after the old hub was already replaced by a new one for the same run;
// a bare `delete(m.hubs, runID)` would evict the NEW hub, and its Redis
// subscription would die with it — a reconnect storm would keep reproducing
// the race. Comparing the pointer makes the stale timer a no-op.
func (m *HubManager) removeIfSame(runID string, hub *RunHub) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hubs[runID] != hub {
		return false
	}
	delete(m.hubs, runID)
	delete(m.cacheStats, runID)
	m.refreshGaugesLocked()
	return true
}

// reportCache publishes one hub's cache size and re-derives the process-wide
// cache gauges. Called with NO hub lock held.
func (m *HubManager) reportCache(runID string, events int, bytes int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.cacheStats[runID] = hubCacheStat{events: events, bytes: bytes}
	m.refreshCacheGaugesLocked()
}

func (m *HubManager) refreshGaugesLocked() {
	if m.metrics == nil {
		return
	}
	m.metrics.SSEHubActive.Set(float64(len(m.hubs)))
	m.refreshCacheGaugesLocked()
}

func (m *HubManager) refreshCacheGaugesLocked() {
	if m.metrics == nil {
		return
	}
	var events int
	var bytes int64
	for _, st := range m.cacheStats {
		events += st.events
		bytes += st.bytes
	}
	m.metrics.SSEHubCacheEvents.Set(float64(events))
	m.metrics.SSEHubCacheBytes.Set(float64(bytes))
}

func (m *HubManager) onSubscriberAdded(protocol int) {
	if m.metrics == nil {
		return
	}
	m.metrics.SSEHubSubscribersActive.WithLabelValues(strconv.Itoa(protocol)).Inc()
}

func (m *HubManager) onSubscriberRemoved(protocol int) {
	if m.metrics == nil {
		return
	}
	m.metrics.SSEHubSubscribersActive.WithLabelValues(strconv.Itoa(protocol)).Dec()
}

func (m *HubManager) onUpstreamUp() {
	if m.metrics == nil {
		return
	}
	m.metrics.SSEHubUpstreamsActive.Inc()
}

func (m *HubManager) onUpstreamDown() {
	if m.metrics == nil {
		return
	}
	m.metrics.SSEHubUpstreamsActive.Dec()
}

func (m *HubManager) upstreamFailure(stage string) {
	if m.metrics == nil {
		return
	}
	m.metrics.SSEHubUpstreamFailureTotal.WithLabelValues(stage).Inc()
}

// Close tears down every hub. It is idempotent.
//
// It MUST run before the Redis client is closed (AC-12): the hubs own pub/sub
// subscriptions on that client, and closing the client first would leave them
// failing in ways that look like a Redis outage.
func (m *HubManager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	hubs := make([]*RunHub, 0, len(m.hubs))
	for _, hub := range m.hubs {
		hubs = append(hubs, hub)
	}
	m.hubs = make(map[string]*RunHub)
	m.cacheStats = make(map[string]hubCacheStat)
	m.refreshGaugesLocked()
	m.mu.Unlock()

	m.cancel()
	for _, hub := range hubs {
		// Each hub closes its own subscribers; its removeIfSame is a no-op
		// because the map is already empty.
		hub.shutdown(DropReasonHubClosed)
	}
}

// HubCount is the number of live hubs (metrics/tests).
func (m *HubManager) HubCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.hubs)
}

// SubscriberCount is the number of attached subscribers across all hubs.
func (m *HubManager) SubscriberCount() int {
	m.mu.Lock()
	hubs := make([]*RunHub, 0, len(m.hubs))
	for _, hub := range m.hubs {
		hubs = append(hubs, hub)
	}
	m.mu.Unlock()
	total := 0
	for _, hub := range hubs {
		total += hub.SubscriberCount()
	}
	return total
}

// UpstreamCount is the number of RUNS with a confirmed Redis upstream. The
// production acceptance criterion is upstreams ≈ hubs, and ≪ connections.
func (m *HubManager) UpstreamCount() int {
	m.mu.Lock()
	hubs := make([]*RunHub, 0, len(m.hubs))
	for _, hub := range m.hubs {
		hubs = append(hubs, hub)
	}
	m.mu.Unlock()
	total := 0
	for _, hub := range hubs {
		if hub.UpstreamActive() {
			total++
		}
	}
	return total
}

// RunHub owns ONE run's Redis upstream and every local subscriber of it.
type RunHub struct {
	runID   string
	manager *HubManager

	ctx    context.Context
	cancel context.CancelFunc

	// ready closes once the upstream registration has settled, successfully
	// or not. Requests wait on it before replaying so that "SUBSCRIBE first,
	// replay second" still holds — the property that makes the replay/live
	// handover lossless.
	ready     chan struct{}
	readyOnce sync.Once

	mu          sync.Mutex
	subscribers map[uint64]*Subscriber
	cache       *DurableRing
	upstreamUp  bool
	terminal    bool
	terminalSeq uint64
	closed      bool
	unhealthy   bool
	idleTimer   *time.Timer
}

func newRunHub(m *HubManager, runID string) *RunHub {
	ctx, cancel := context.WithCancel(m.ctx)
	hub := &RunHub{
		runID:       runID,
		manager:     m,
		ctx:         ctx,
		cancel:      cancel,
		ready:       make(chan struct{}),
		subscribers: make(map[uint64]*Subscriber),
		cache:       NewDurableRing(m.opts.CacheMaxEvents, m.opts.CacheMaxBytes),
	}
	// A hub starts idle: if nobody attaches (a request that resolved the hub
	// and then failed), the timer evicts it instead of leaking it forever.
	hub.mu.Lock()
	hub.armIdleTimerLocked()
	hub.mu.Unlock()
	return hub
}

// RunID is the run this hub fans out.
func (h *RunHub) RunID() string { return h.runID }

// Serving reports whether the hub may accept new subscribers.
func (h *RunHub) Serving() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.closed && !h.unhealthy
}

// Terminal reports whether a terminal event has been observed.
func (h *RunHub) Terminal() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.terminal
}

// TerminalSeq is the sequence of the terminal event (0 while running).
func (h *RunHub) TerminalSeq() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.terminalSeq
}

// UpstreamActive reports whether the Redis subscription is confirmed.
func (h *RunHub) UpstreamActive() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.upstreamUp
}

// SubscriberCount is the number of attached local subscribers.
func (h *RunHub) SubscriberCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subscribers)
}

// CacheLen / CacheBytes / CacheHighWater expose the bounded cache for tests and
// gauges. CacheHighWater is the newest cached sequence (0 when empty).
func (h *RunHub) CacheLen() int { return h.cache.Len() }

// CacheBytes is the cache's approximate payload footprint.
func (h *RunHub) CacheBytes() int64 { return h.cache.Bytes() }

// CacheHighWater is the newest cached durable sequence (0 when empty).
func (h *RunHub) CacheHighWater() uint64 { return h.cache.HighWater() }

// WaitReady blocks until the upstream registration has settled. It returns
// false when ctx ended or the readiness guard expired first — in both cases
// the caller serves a durable-only replay and the client reconnects, which
// loses nothing because every reconnect re-derives from the durable cursor.
func (h *RunHub) WaitReady(ctx context.Context) bool {
	timeout := h.manager.opts.UpstreamReadyTimeout
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-h.ready:
		return true
	case <-ctx.Done():
		return false
	case <-timer.C:
		return false
	}
}

func (h *RunHub) closeReady() {
	h.readyOnce.Do(func() { close(h.ready) })
}

// Subscribe registers one connection. It must be called BEFORE the caller
// replays history (AC-4/AC-5): from this instant the connection's queue
// captures live frames, including the transient deltas that must not be
// interleaved into the historical replay.
func (h *RunHub) Subscribe(protocol int) (*Subscriber, bool) {
	h.mu.Lock()
	if h.closed || h.unhealthy {
		h.mu.Unlock()
		return nil, false
	}
	h.stopIdleTimerLocked()
	sub := newSubscriber(
		h.manager.nextSubscriberID.Add(1),
		protocol,
		h.manager.opts.SubscriberMaxEvents,
		h.manager.opts.SubscriberMaxBytes,
	)
	sub.onClose = h.removeSubscriber
	h.subscribers[sub.id] = sub
	h.mu.Unlock()

	h.manager.onSubscriberAdded(protocol)
	return sub, true
}

// ReplayAfter returns the cache's answer for a durable cursor: the events to
// send, plus whether the cache is CONTIGUOUS for that cursor (AC-8).
func (h *RunHub) ReplayAfter(after uint64) ([]HubEvent, bool) {
	return h.cache.After(after)
}

// RememberDurable warms the cache with an event the caller read from MySQL.
//
// It NEVER fans out: the same durable event is already on its way to the
// subscribers through the upstream, and delivering it twice from two paths is
// exactly the duplicate the durable cursor exists to prevent. Warming is what
// turns the first viewer's database replay into cache hits for everyone after
// them, and the bounded ring means the warm-up cannot pin a run's whole
// history in memory.
func (h *RunHub) RememberDurable(sequence uint64, eventType string, payload map[string]any) {
	if sequence == 0 {
		return
	}
	h.remember(hubEvent(sequence, eventType, payload))
}

func (h *RunHub) remember(ev HubEvent) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	_, _ = h.cache.Remember(ev)
	events, size := h.cache.Len(), h.cache.Bytes()
	h.mu.Unlock()
	h.manager.reportCache(h.runID, events, size)
}

// NoteTerminal records that the durable log ended, without fanning anything
// out (the caller already delivered the frame). The upstream is released
// immediately: a terminal event is the last one by definition, so holding the
// Redis subscription open could only deliver events that must be ignored.
func (h *RunHub) NoteTerminal(sequence uint64) {
	h.mu.Lock()
	if h.closed || h.terminal {
		h.mu.Unlock()
		return
	}
	h.terminal = true
	h.terminalSeq = sequence
	h.mu.Unlock()
	h.cancel()
}

func (h *RunHub) isTerminal() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.terminal
}

// runUpstream owns the ONE Redis subscription of this run. Everything the
// hub knows about live events enters through this loop.
func (h *RunHub) runUpstream() {
	channel, err := h.manager.upstream.Subscribe(h.ctx, h.runID)
	if err != nil {
		// Fail the hub BEFORE releasing the readiness wait: a waiter that
		// woke up on `ready` first would see a hub that still looked healthy
		// and register a subscriber on a hub that is being torn down.
		if h.ctx.Err() == nil {
			h.manager.upstreamFailure("subscribe")
			h.failUpstream()
		}
		h.closeReady()
		return
	}

	h.mu.Lock()
	h.upstreamUp = true
	h.mu.Unlock()
	h.manager.onUpstreamUp()
	h.closeReady()
	defer func() {
		_ = channel.Close()
		h.mu.Lock()
		h.upstreamUp = false
		h.mu.Unlock()
		h.manager.onUpstreamDown()
	}()

	payloads := channel.Payloads()
	for {
		select {
		case <-h.ctx.Done():
			return
		case raw, ok := <-payloads:
			if !ok {
				if h.ctx.Err() != nil {
					return
				}
				h.manager.upstreamFailure(upstreamFailureStage(channel.Err()))
				h.failUpstream()
				return
			}
			ev, decErr := decodeHubEvent(raw)
			if decErr != nil {
				// A malformed payload is a publisher/format problem, not a
				// transport failure: skip the frame, keep the subscription.
				h.manager.upstreamFailure("decode")
				continue
			}
			h.dispatch(ev)
			if h.isTerminal() {
				return
			}
		}
	}
}

// upstreamFailureStage maps a subscription that ended on its own to a bounded
// metric label.
func upstreamFailureStage(err error) string {
	if err != nil {
		return "receive"
	}
	return "channel_closed"
}

// dispatch records one live event in the cache and fans it out.
//
// Order matters: the terminal flag is set BEFORE the fan-out so that anything
// published after the terminal event (dirty history from a pre-fix producer)
// is dropped rather than delivered — the terminal is a hard boundary (AC-9),
// matching the pre-Batch-4 "terminal → return" semantics.
//
// The subscriber snapshot is taken under the lock and the offers happen
// OUTSIDE it, so a slow subscriber cannot hold the hub lock while its own
// drop runs.
func (h *RunHub) dispatch(ev HubEvent) {
	h.mu.Lock()
	if h.closed || h.terminal {
		h.mu.Unlock()
		return
	}
	if !ev.IsTransient() {
		_, _ = h.cache.Remember(ev)
	}
	if ev.IsTerminal() {
		h.terminal = true
		h.terminalSeq = ev.Sequence
	}
	subs := make([]*Subscriber, 0, len(h.subscribers))
	for _, sub := range h.subscribers {
		subs = append(subs, sub)
	}
	events, size := h.cache.Len(), h.cache.Bytes()
	h.mu.Unlock()

	if !ev.IsTransient() {
		h.manager.reportCache(h.runID, events, size)
	}

	for _, sub := range subs {
		if !sub.accepts(ev) {
			continue
		}
		if reason := sub.Offer(ev); reason != "" {
			h.dropSubscriber(sub, reason)
		}
	}
}

// dropSubscriber disconnects exactly one subscriber (AC-6). The dropped
// counter moves only for the call that actually closed it, so a client that
// disconnected on its own is never reported as a hub-initiated drop.
func (h *RunHub) dropSubscriber(sub *Subscriber, reason string) {
	if !sub.Close(reason) {
		return
	}
	if h.manager.metrics != nil {
		h.manager.metrics.SSEHubSubscriberDroppedTotal.WithLabelValues(reason).Inc()
	}
}

// removeSubscriber is the Subscriber.onClose hook: it runs exactly once per
// subscriber, from whichever side closed it.
func (h *RunHub) removeSubscriber(sub *Subscriber) {
	h.mu.Lock()
	_, tracked := h.subscribers[sub.id]
	if tracked {
		delete(h.subscribers, sub.id)
		if len(h.subscribers) == 0 && !h.closed {
			h.armIdleTimerLocked()
		}
	}
	h.mu.Unlock()
	if tracked {
		h.manager.onSubscriberRemoved(sub.protocol)
	}
}

// failUpstream handles a transport that died while the hub still needed it
// (§24).
//
// The batch deliberately does NOT retry inside the hub: the client already
// reconnects and replays from its durable cursor, so a hub-internal
// reconnection state machine would be a SECOND recovery system layered on the
// first, with its own races and no additional guarantee. Instead the hub is
// marked unhealthy, its subscribers are closed so their streams end promptly
// (rather than hanging until keepalive), and the hub leaves the registry so
// the next request builds a fresh one.
func (h *RunHub) failUpstream() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	h.unhealthy = true
	h.stopIdleTimerLocked()
	subs := make([]*Subscriber, 0, len(h.subscribers))
	for _, sub := range h.subscribers {
		subs = append(subs, sub)
	}
	h.mu.Unlock()

	h.cancel()
	for _, sub := range subs {
		h.dropSubscriber(sub, DropReasonUpstreamClosed)
	}
	h.manager.removeIfSame(h.runID, h)
}

// shutdown closes the hub and every subscriber on it, keeping the hub's
// identity in the registry semantics (removeIfSame decides whether this
// instance is still the current one).
func (h *RunHub) shutdown(reason string) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	h.stopIdleTimerLocked()
	subs := make([]*Subscriber, 0, len(h.subscribers))
	for _, sub := range h.subscribers {
		subs = append(subs, sub)
	}
	h.mu.Unlock()

	h.cancel()
	for _, sub := range subs {
		h.dropSubscriber(sub, reason)
	}
	h.manager.removeIfSame(h.runID, h)
}

// armIdleTimerLocked starts the reuse window after the last subscriber left
// (§22). Evicting immediately would tear down and rebuild the Redis
// subscription on every ~2s frontend reconnect.
func (h *RunHub) armIdleTimerLocked() {
	if h.closed || h.idleTimer != nil {
		return
	}
	h.idleTimer = time.AfterFunc(h.manager.opts.IdleTTL, h.evictIfIdle)
}

func (h *RunHub) stopIdleTimerLocked() {
	if h.idleTimer != nil {
		h.idleTimer.Stop()
		h.idleTimer = nil
	}
}

// evictIfIdle reclaims a hub that has been subscriber-less for IdleTTL.
//
// The timer may fire for an instance that is no longer in the registry (it
// was replaced or already closed): removeIfSame makes that harmless.
func (h *RunHub) evictIfIdle() {
	h.mu.Lock()
	h.idleTimer = nil
	if h.closed || len(h.subscribers) > 0 {
		h.mu.Unlock()
		return
	}
	h.closed = true
	h.mu.Unlock()

	h.cancel()
	h.manager.removeIfSame(h.runID, h)
}
