package sse

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/execution"
)

// ─────────────────────────── fake upstream ───────────────────────────
//
// The hub's production invariants are about how many Redis subscriptions it
// opens, how many times a payload is decoded, and which subscriber receives
// what. None of that needs a live Redis, and asserting it against one would be
// slower and weaker (a real broker accepts any number of subscriptions, so
// "exactly one" must be counted, not observed). The fake counts.

type fakeUpstream struct {
	mu         sync.Mutex
	channels   map[string][]*fakeChannel
	subscribes int
	failErr    error
}

func newFakeUpstream() *fakeUpstream {
	return &fakeUpstream{channels: map[string][]*fakeChannel{}}
}

func (f *fakeUpstream) Subscribe(_ context.Context, runID string) (HubUpstreamChannel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subscribes++
	if f.failErr != nil {
		return nil, f.failErr
	}
	ch := &fakeChannel{runID: runID, out: make(chan []byte, 4096)}
	f.channels[runID] = append(f.channels[runID], ch)
	return ch, nil
}

// SubscribeCount is the core Batch 4 measurement: one per RUN, not one per
// connection.
func (f *fakeUpstream) SubscribeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.subscribes
}

// ActiveChannels counts subscriptions that have not been closed.
func (f *fakeUpstream) ActiveChannels() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, list := range f.channels {
		for _, ch := range list {
			ch.mu.Lock()
			if !ch.closed {
				n++
			}
			ch.mu.Unlock()
		}
	}
	return n
}

// publish encodes ev exactly like the execution service does and hands it to
// the newest channel of runID.
func (f *fakeUpstream) publish(t *testing.T, runID string, ev HubEvent) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"sequence":   ev.Sequence,
		"event_type": ev.EventType,
		"payload":    ev.Payload,
	})
	if err != nil {
		t.Fatalf("encode event: %v", err)
	}
	if !f.publishRaw(runID, raw) {
		t.Fatalf("no live upstream channel for run %s: the payload went nowhere "+
			"(this is what a per-connection subscription regression looks like)", runID)
	}
}

// tryPublish is publish without failing the test — used where a mutation is
// allowed to make the payload vanish, so the assertion can report the missing
// event instead.
func (f *fakeUpstream) tryPublish(runID string, ev HubEvent) bool {
	raw, err := json.Marshal(map[string]any{
		"sequence":   ev.Sequence,
		"event_type": ev.EventType,
		"payload":    ev.Payload,
	})
	if err != nil {
		return false
	}
	return f.publishRaw(runID, raw)
}

func (f *fakeUpstream) publishRaw(runID string, raw []byte) bool {
	f.mu.Lock()
	list := f.channels[runID]
	var ch *fakeChannel
	if len(list) > 0 {
		ch = list[len(list)-1]
	}
	f.mu.Unlock()
	if ch == nil {
		return false
	}
	return ch.send(raw)
}

// failChannel simulates the transport dying while the hub is still using it.
func (f *fakeUpstream) failChannel(runID string) {
	f.mu.Lock()
	list := f.channels[runID]
	var ch *fakeChannel
	if len(list) > 0 {
		ch = list[len(list)-1]
	}
	f.mu.Unlock()
	if ch != nil {
		_ = ch.closeWith(errUpstreamChannelClosed)
	}
}

type fakeChannel struct {
	runID string
	out   chan []byte

	mu     sync.Mutex
	closed bool
	err    error
}

func (c *fakeChannel) Payloads() <-chan []byte { return c.out }

func (c *fakeChannel) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *fakeChannel) Close() error { return c.closeWith(nil) }

func (c *fakeChannel) closeWith(err error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	c.err = err
	close(c.out)
	return nil
}

func (c *fakeChannel) send(raw []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	select {
	case c.out <- raw:
		return true
	default:
		return false
	}
}

// ─────────────────────────── helpers ───────────────────────────

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

type collected struct {
	mu     sync.Mutex
	events []HubEvent
}

func (c *collected) append(ev HubEvent) {
	c.mu.Lock()
	c.events = append(c.events, ev)
	c.mu.Unlock()
}

func (c *collected) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

func (c *collected) snapshot() []HubEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]HubEvent(nil), c.events...)
}

func (c *collected) sequences() []uint64 {
	out := []uint64{}
	for _, ev := range c.snapshot() {
		out = append(out, ev.Sequence)
	}
	return out
}

func (c *collected) types() []string {
	out := []string{}
	for _, ev := range c.snapshot() {
		out = append(out, ev.EventType)
	}
	return out
}

// drainAsync consumes a subscriber forever, releasing each event's byte budget
// exactly as the HTTP writer does. Used wherever a test needs a subscriber that
// behaves like a healthy browser.
func drainAsync(sub *Subscriber) *collected {
	c := &collected{}
	go func() {
		for {
			select {
			case <-sub.Done():
				for {
					select {
					case ev := <-sub.Events():
						sub.Release(ev)
						c.append(ev)
					default:
						return
					}
				}
			case ev := <-sub.Events():
				sub.Release(ev)
				c.append(ev)
			}
		}
	}()
	return c
}

func chunkEvent(seq uint64) HubEvent {
	return HubEvent{
		Sequence:    seq,
		EventType:   execution.EventContentChunk,
		Payload:     map[string]any{"text": fmt.Sprintf("chunk-%d", seq)},
		ApproxBytes: 64,
	}
}

func deltaEvent(offset int) HubEvent {
	return HubEvent{
		Sequence:    0,
		EventType:   execution.EventContentDelta,
		Payload:     map[string]any{"text": "streaming", "offset": offset},
		ApproxBytes: 64,
	}
}

func terminalEvent(seq uint64, name string) HubEvent {
	return HubEvent{Sequence: seq, EventType: name, Payload: map[string]any{"status": "succeeded"}, ApproxBytes: 64}
}

// ─────────────────────────── tests ───────────────────────────

// TestHubManagerSharesOneUpstreamPerRun is the core acceptance test (AC-1,
// §36 Test 1) and the falsification for Mutation A.
//
// 100 viewers of one run must produce ONE Redis subscription. The count comes
// from the fake upstream, not from inspecting hub internals, because "one
// subscription" is the property that matters — a hub map that happened to have
// one entry while each connection subscribed separately would still be the
// defect.
func TestHubManagerSharesOneUpstreamPerRun(t *testing.T) {
	up := newFakeUpstream()
	mgr := NewHubManagerWithUpstream(context.Background(), up, nil, HubOptions{})
	defer mgr.Close()

	// Concurrent first touch: 40 goroutines racing for the same run must
	// still leave exactly one hub (§5.1).
	var wg sync.WaitGroup
	hubs := make([]*RunHub, 40)
	for i := range hubs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			hubs[i] = mgr.GetOrCreate("run-shared")
		}(i)
	}
	wg.Wait()
	for i, hub := range hubs {
		if hub == nil {
			t.Fatalf("goroutine %d got a nil hub", i)
		}
		if hub != hubs[0] {
			t.Fatalf("goroutine %d got a DIFFERENT hub instance: the registry is not enforcing one hub per run", i)
		}
	}

	for i := 0; i < 100; i++ {
		if _, ok := hubs[0].Subscribe(StreamProtocolRangeDelta); !ok {
			t.Fatalf("subscriber %d was refused", i)
		}
	}

	waitFor(t, "the hub's upstream registration", func() bool { return up.SubscribeCount() == 1 })
	if got := up.SubscribeCount(); got != 1 {
		t.Fatalf("Redis Subscribe called %d times for one run, want 1", got)
	}
	if got := mgr.HubCount(); got != 1 {
		t.Fatalf("HubCount = %d, want 1", got)
	}
	if got := mgr.SubscriberCount(); got != 100 {
		t.Fatalf("SubscriberCount = %d, want 100", got)
	}
	if got := mgr.UpstreamCount(); got != 1 {
		t.Fatalf("UpstreamCount = %d, want 1 (upstreams must track RUNS, not connections)", got)
	}
}

// TestHubManagerIsolatesRuns (§36 Test 2): fan-out sharing must not collapse
// runs into each other.
func TestHubManagerIsolatesRuns(t *testing.T) {
	up := newFakeUpstream()
	mgr := NewHubManagerWithUpstream(context.Background(), up, nil, HubOptions{})
	defer mgr.Close()

	plan := map[string]int{"run-a": 10, "run-b": 20, "run-c": 30}
	for runID, viewers := range plan {
		hub := mgr.GetOrCreate(runID)
		for i := 0; i < viewers; i++ {
			if _, ok := hub.Subscribe(StreamProtocolRangeDelta); !ok {
				t.Fatalf("%s: subscriber %d refused", runID, i)
			}
		}
	}

	waitFor(t, "three upstream registrations", func() bool { return up.SubscribeCount() == 3 })
	if up.SubscribeCount() != 3 {
		t.Fatalf("Redis Subscribe called %d times for 3 runs, want 3", up.SubscribeCount())
	}
	if got := mgr.HubCount(); got != 3 {
		t.Fatalf("HubCount = %d, want 3", got)
	}
	if got := mgr.SubscriberCount(); got != 60 {
		t.Fatalf("SubscriberCount = %d, want 60", got)
	}

	// A live event for run-b reaches run-b's subscribers only.
	bSub, _ := mgr.Lookup("run-b").Subscribe(StreamProtocolRangeDelta)
	bCollected := drainAsync(bSub)
	aCollected := drainAsync(func() *Subscriber {
		sub, _ := mgr.Lookup("run-a").Subscribe(StreamProtocolRangeDelta)
		return sub
	}())

	up.publish(t, "run-b", chunkEvent(7))
	waitFor(t, "run-b's subscriber to receive seq 7", func() bool { return bCollected.len() == 1 })
	if got := aCollected.len(); got != 0 {
		t.Fatalf("run-a received %d events published to run-b: hubs are leaking across runs", got)
	}
}

// TestHubSubscriberProtocolIsolation pins AC-2/AC-3 and kills Mutation B.
//
// The protocol lives on the SUBSCRIBER. A hub-level protocol would either strip
// transient deltas from v2 clients or hand them to append-only ones — and the
// legacy client would render "ABCABC" because the durable chunk legitimately
// arrives before the buffered transient delta for the same bytes.
func TestHubSubscriberProtocolIsolation(t *testing.T) {
	up := newFakeUpstream()
	mgr := NewHubManagerWithUpstream(context.Background(), up, nil, HubOptions{})
	defer mgr.Close()

	hub := mgr.GetOrCreate("run-proto")
	legacy, ok := hub.Subscribe(StreamProtocolLegacy)
	if !ok {
		t.Fatal("legacy subscriber refused")
	}
	v2, ok := hub.Subscribe(StreamProtocolRangeDelta)
	if !ok {
		t.Fatal("v2 subscriber refused")
	}
	legacySeen := drainAsync(legacy)
	v2Seen := drainAsync(v2)

	waitFor(t, "the upstream registration", func() bool { return up.SubscribeCount() == 1 })
	up.publish(t, "run-proto", deltaEvent(1000))
	up.publish(t, "run-proto", chunkEvent(10))

	waitFor(t, "the v2 subscriber to receive both frames", func() bool { return v2Seen.len() == 2 })
	waitFor(t, "the legacy subscriber to receive the durable chunk", func() bool { return legacySeen.len() == 1 })

	if got := legacySeen.types(); len(got) != 1 || got[0] != execution.EventContentChunk {
		t.Fatalf("protocol-1 subscriber received %v, want only [%s]", got, execution.EventContentChunk)
	}
	got := v2Seen.types()
	if len(got) != 2 || got[0] != execution.EventContentDelta || got[1] != execution.EventContentChunk {
		t.Fatalf("protocol-2 subscriber received %v, want [%s %s]",
			got, execution.EventContentDelta, execution.EventContentChunk)
	}
}

// TestHubFanoutIsNonBlocking pins AC-6 and kills Mutation C.
//
// A blocking fan-out (`sub.events <- ev`) would stall the hub's only upstream
// goroutine on one browser that stopped reading, which stops every other viewer
// on the run AND lets the Redis channel backlog grow until the stream collapses.
// The correct behaviour is to disconnect that subscriber alone.
func TestHubFanoutIsNonBlocking(t *testing.T) {
	up := newFakeUpstream()
	opts := DefaultHubOptions()
	opts.SubscriberMaxEvents = 4
	mgr := NewHubManagerWithUpstream(context.Background(), up, nil, opts)
	defer mgr.Close()

	hub := mgr.GetOrCreate("run-slow")
	fast, _ := hub.Subscribe(StreamProtocolRangeDelta)
	slow, _ := hub.Subscribe(StreamProtocolRangeDelta)
	fastSeen := drainAsync(fast)

	waitFor(t, "the upstream registration", func() bool { return up.SubscribeCount() == 1 })

	// The slow subscriber never reads a single event. Publishing one at a
	// time and waiting for the healthy subscriber between publishes makes the
	// healthy subscriber's survival deterministic: its queue never exceeds
	// one entry, so it can only be dropped if the fan-out is broken.
	for i := 1; i <= 20; i++ {
		up.publish(t, "run-slow", chunkEvent(uint64(i)))
		target := i
		waitFor(t, fmt.Sprintf("the healthy subscriber to receive seq %d", target), func() bool {
			return fastSeen.len() >= target
		})
	}

	waitFor(t, "the slow subscriber to be dropped", func() bool {
		select {
		case <-slow.Done():
			return true
		default:
			return false
		}
	})
	if got := slow.DropReason(); got != DropReasonSlowConsumer {
		t.Fatalf("slow subscriber drop reason = %q, want %q", got, DropReasonSlowConsumer)
	}
	if fast.DropReason() != "" {
		t.Fatalf("the healthy subscriber was dropped (%s): one slow consumer must not affect others",
			fast.DropReason())
	}
	if got := fastSeen.len(); got != 20 {
		t.Fatalf("healthy subscriber received %d of 20 events", got)
	}

	// The hub and its single upstream survive.
	if !hub.Serving() || !hub.UpstreamActive() {
		t.Fatal("hub stopped serving after one subscriber was dropped")
	}
	if got := up.ActiveChannels(); got != 1 {
		t.Fatalf("active upstream channels = %d, want 1", got)
	}
	if got := up.SubscribeCount(); got != 1 {
		t.Fatalf("Redis Subscribe called %d times, want 1 (the drop must not churn the subscription)", got)
	}
}

// TestHubSubscriberByteLimitDropsSlowConsumer (§44): the byte budget must bite
// even when the queue holds far fewer events than its count limit.
func TestHubSubscriberByteLimitDropsSlowConsumer(t *testing.T) {
	up := newFakeUpstream()
	// Calibrate from ONE encoded frame so the budget admits exactly one.
	bigPayload := map[string]any{"text": strings.Repeat("x", 4096)}
	raw, err := json.Marshal(map[string]any{
		"sequence":   uint64(1),
		"event_type": execution.EventContentChunk,
		"payload":    bigPayload,
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	oneFrame := int64(len(raw) + hubEventOverheadBytes)

	opts := DefaultHubOptions()
	opts.SubscriberMaxEvents = 64 // deliberately far above what the bytes allow
	opts.SubscriberMaxBytes = 2*oneFrame - 1
	mgr := NewHubManagerWithUpstream(context.Background(), up, nil, opts)
	defer mgr.Close()

	hub := mgr.GetOrCreate("run-bytes")
	sub, _ := hub.Subscribe(StreamProtocolRangeDelta)
	waitFor(t, "the upstream registration", func() bool { return up.SubscribeCount() == 1 })

	big := HubEvent{Sequence: 1, EventType: execution.EventContentChunk, Payload: bigPayload}
	if !up.tryPublish("run-bytes", big) {
		t.Fatal("first publish was refused by the fake transport")
	}
	waitFor(t, "the byte budget to be charged", func() bool { return sub.QueuedBytes() > 0 })

	big.Sequence = 2
	if !up.tryPublish("run-bytes", big) {
		t.Fatal("second publish was refused by the fake transport")
	}
	waitFor(t, "the subscriber to be dropped on bytes", func() bool {
		select {
		case <-sub.Done():
			return true
		default:
			return false
		}
	})
	if got := sub.DropReason(); got != DropReasonSlowConsumer {
		t.Fatalf("drop reason = %q, want %q: an event-count-only bound does not bound memory",
			got, DropReasonSlowConsumer)
	}
}

// TestHubReleasesQueueBytesOnConsume pins §18.
//
// If the byte budget were only settled at Close, queuedBytes would grow with
// connection LIFETIME, so a long-lived healthy subscriber would eventually be
// mistaken for a slow one. The test publishes far more total bytes than the
// budget allows while consuming every event: a missing Release turns this into
// a drop.
func TestHubReleasesQueueBytesOnConsume(t *testing.T) {
	up := newFakeUpstream()
	opts := DefaultHubOptions()
	opts.SubscriberMaxBytes = 4096 // room for a handful of frames
	mgr := NewHubManagerWithUpstream(context.Background(), up, nil, opts)
	defer mgr.Close()

	hub := mgr.GetOrCreate("run-release")
	sub, _ := hub.Subscribe(StreamProtocolRangeDelta)
	collected := drainAsync(sub)
	waitFor(t, "the upstream registration", func() bool { return up.SubscribeCount() == 1 })

	const rounds = 200
	for i := 1; i <= rounds; i++ {
		up.publish(t, "run-release", chunkEvent(uint64(i)))
		target := i
		waitFor(t, fmt.Sprintf("seq %d to be consumed", target), func() bool { return collected.len() >= target })
	}
	if got := sub.DropReason(); got != "" {
		t.Fatalf("subscriber dropped (%s) after %d consumed events: the byte budget is never released on consume",
			got, rounds)
	}
	if queued := sub.QueuedBytes(); queued > opts.SubscriberMaxBytes {
		t.Fatalf("queuedBytes = %d after consumption, want <= %d", queued, opts.SubscriberMaxBytes)
	}
}

// TestHubTerminalIsAHardBoundary pins AC-9 and kills Mutation G.
func TestHubTerminalIsAHardBoundary(t *testing.T) {
	up := newFakeUpstream()
	mgr := NewHubManagerWithUpstream(context.Background(), up, nil, HubOptions{})
	defer mgr.Close()

	hub := mgr.GetOrCreate("run-terminal")
	sub, _ := hub.Subscribe(StreamProtocolRangeDelta)
	collected := drainAsync(sub)
	waitFor(t, "the upstream registration", func() bool { return up.SubscribeCount() == 1 })

	up.publish(t, "run-terminal", terminalEvent(100, execution.EventRunCompleted))
	waitFor(t, "the terminal frame", func() bool { return collected.len() == 1 })

	// Dirty history: something published AFTER the terminal must never reach a
	// subscriber, even though the transport still delivers it to the hub.
	up.tryPublish("run-terminal", terminalEvent(101, execution.EventRunFailed))
	time.Sleep(50 * time.Millisecond)

	if got := collected.sequences(); len(got) != 1 || got[0] != 100 {
		t.Fatalf("subscriber received %v, want exactly [100]: a terminal event is a hard boundary", got)
	}
	if !hub.Terminal() || hub.TerminalSeq() != 100 {
		t.Fatalf("hub terminal=%v seq=%d, want true/100", hub.Terminal(), hub.TerminalSeq())
	}
	if got := up.ActiveChannels(); got != 0 {
		t.Fatalf("active upstream channels after the terminal = %d, want 0: the subscription must be released", got)
	}
}

// TestHubNeverCachesTransientButWarmsFromDurableEvents pins the cache feeding
// rules (§10, §13).
func TestHubNeverCachesTransientButWarmsFromDurableEvents(t *testing.T) {
	up := newFakeUpstream()
	mgr := NewHubManagerWithUpstream(context.Background(), up, nil, HubOptions{})
	defer mgr.Close()

	hub := mgr.GetOrCreate("run-cache-feed")
	sub, _ := hub.Subscribe(StreamProtocolRangeDelta)
	collected := drainAsync(sub)
	waitFor(t, "the upstream registration", func() bool { return up.SubscribeCount() == 1 })

	up.publish(t, "run-cache-feed", deltaEvent(500))
	waitFor(t, "the transient frame", func() bool { return collected.len() == 1 })
	if got := hub.CacheLen(); got != 0 {
		t.Fatalf("CacheLen = %d after a transient frame, want 0", got)
	}

	// A DB-sourced durable event warms the cache and is NOT fanned out: it is
	// already on its way through the upstream, and delivering it twice is the
	// duplicate the durable cursor exists to prevent.
	hub.RememberDurable(1, execution.EventContentChunk, map[string]any{"text": "warm"})
	if got := hub.CacheLen(); got != 1 {
		t.Fatalf("CacheLen = %d after RememberDurable, want 1", got)
	}
	time.Sleep(30 * time.Millisecond)
	if got := collected.len(); got != 1 {
		t.Fatalf("RememberDurable fanned out an event (subscriber saw %d frames, want 1)", got)
	}
}

// TestHubCacheGapFallsBackToMySQL is the hub-level half of AC-8: the cache must
// never claim continuity it cannot prove.
func TestHubCacheGapFallsBackToMySQL(t *testing.T) {
	up := newFakeUpstream()
	mgr := NewHubManagerWithUpstream(context.Background(), up, nil, HubOptions{})
	defer mgr.Close()

	hub := mgr.GetOrCreate("run-gap")
	waitFor(t, "the upstream registration", func() bool { return up.SubscribeCount() == 1 })

	for _, seq := range []uint64{100, 101} {
		up.publish(t, "run-gap", chunkEvent(seq))
	}
	// Wait on the HIGH-WATER MARK, not on the length: the length is already 2
	// before the second segment arrives, so waiting on it would race ahead of
	// the hub and assert against a still-contiguous cache.
	waitFor(t, "the first segment", func() bool { return hub.CacheHighWater() == 101 })

	// 102-104 were missed: the transport dropped them.
	for _, seq := range []uint64{105, 106} {
		up.publish(t, "run-gap", chunkEvent(seq))
	}
	waitFor(t, "the new segment", func() bool {
		return hub.CacheHighWater() == 106 && hub.CacheLen() == 2
	})

	if _, covered := hub.ReplayAfter(101); covered {
		t.Fatal("hub reported a cache hit for after=101 although 102-104 were never seen: " +
			"the client would lose three events and keep advancing its cursor")
	}
	if _, covered := hub.ReplayAfter(103); covered {
		t.Fatal("hub reported a cache hit for after=103 although 104 was never seen")
	}
	if events, covered := hub.ReplayAfter(104); !covered || len(events) != 2 {
		t.Fatalf("ReplayAfter(104) = %v (covered=%v), want [105 106] covered", sequencesOf(events), covered)
	}
}

// TestHubIdleReuseKeepsTheSameUpstream (§46): a reconnect a couple of seconds
// after a drop must NOT rebuild the Redis subscription.
func TestHubIdleReuseKeepsTheSameUpstream(t *testing.T) {
	up := newFakeUpstream()
	opts := DefaultHubOptions()
	opts.IdleTTL = 5 * time.Second
	mgr := NewHubManagerWithUpstream(context.Background(), up, nil, opts)
	defer mgr.Close()

	hub := mgr.GetOrCreate("run-idle")
	first, _ := hub.Subscribe(StreamProtocolRangeDelta)
	waitFor(t, "the upstream registration", func() bool { return up.SubscribeCount() == 1 })
	first.Close(DropReasonClientGone)

	// The frontend reconnects ~2s after a drop; 50ms is already generous.
	time.Sleep(50 * time.Millisecond)
	if got := mgr.Lookup("run-idle"); got != hub {
		t.Fatalf("a reconnect within IdleTTL got %p, want the SAME hub %p", got, hub)
	}
	second, ok := hub.Subscribe(StreamProtocolRangeDelta)
	if !ok {
		t.Fatal("reconnect refused")
	}
	defer second.Close(DropReasonClientGone)
	if got := up.SubscribeCount(); got != 1 {
		t.Fatalf("Redis Subscribe called %d times across a reconnect, want 1", got)
	}
}

// TestHubIdleEvictionClosesUpstream (§47).
func TestHubIdleEvictionClosesUpstream(t *testing.T) {
	up := newFakeUpstream()
	opts := DefaultHubOptions()
	opts.IdleTTL = 40 * time.Millisecond
	mgr := NewHubManagerWithUpstream(context.Background(), up, nil, opts)
	defer mgr.Close()

	hub := mgr.GetOrCreate("run-evict")
	sub, _ := hub.Subscribe(StreamProtocolRangeDelta)
	waitFor(t, "the upstream registration", func() bool { return up.SubscribeCount() == 1 })
	sub.Close(DropReasonClientGone)

	waitFor(t, "the idle hub to be evicted", func() bool { return mgr.HubCount() == 0 })
	if got := mgr.Lookup("run-evict"); got != nil {
		t.Fatal("Lookup returned an evicted hub")
	}
	waitFor(t, "the upstream to close", func() bool { return up.ActiveChannels() == 0 })
}

// TestStaleIdleTimerCannotDeleteTheNewHub pins AC-10 and §23 — the race that a
// bare `delete(manager.hubs, runID)` gets wrong.
//
// The dangerous sequence is: an old hub's timer is armed → the old hub is
// replaced by a new one for the SAME run → the old timer fires. Without the
// pointer comparison the fire evicts the NEW hub, killing a live Redis
// subscription and every viewer attached to it.
func TestStaleIdleTimerCannotDeleteTheNewHub(t *testing.T) {
	up := newFakeUpstream()
	opts := DefaultHubOptions()
	opts.IdleTTL = time.Hour // the timer must NOT fire on its own here
	mgr := NewHubManagerWithUpstream(context.Background(), up, nil, opts)
	defer mgr.Close()

	old := mgr.GetOrCreate("run-generation")

	// Simulate the replacement: a new hub instance takes the registry slot.
	replacement := newRunHub(mgr, "run-generation")
	mgr.mu.Lock()
	mgr.hubs["run-generation"] = replacement
	mgr.mu.Unlock()

	// The OLD hub's idle timer fires now. It has no subscribers and is not
	// closed, so it walks straight into removeIfSame.
	old.evictIfIdle()

	if got := mgr.Lookup("run-generation"); got != replacement {
		t.Fatalf("the stale idle timer evicted the NEW hub: Lookup = %p, want %p", got, replacement)
	}
	if got := mgr.HubCount(); got != 1 {
		t.Fatalf("HubCount = %d after a stale timer fired, want 1", got)
	}

	// The same pointer check must still let a CURRENT hub be removed.
	if !mgr.removeIfSame("run-generation", replacement) {
		t.Fatal("removeIfSame refused to remove the current hub")
	}
	if got := mgr.HubCount(); got != 0 {
		t.Fatalf("HubCount = %d after removing the current hub, want 0", got)
	}
}

// TestUpstreamFailureClosesSubscribersAndRemovesHub (§24/§49).
//
// Batch 4 deliberately has NO in-hub reconnection state machine: the client
// already reconnects and replays from its durable cursor, and a second recovery
// system layered on the first adds races without adding a guarantee.
func TestUpstreamFailureClosesSubscribersAndRemovesHub(t *testing.T) {
	up := newFakeUpstream()
	mgr := NewHubManagerWithUpstream(context.Background(), up, nil, HubOptions{})
	defer mgr.Close()

	hub := mgr.GetOrCreate("run-dead-upstream")
	sub, _ := hub.Subscribe(StreamProtocolRangeDelta)
	waitFor(t, "the upstream registration", func() bool { return up.SubscribeCount() == 1 })

	up.failChannel("run-dead-upstream")

	waitFor(t, "the subscriber to be closed", func() bool {
		select {
		case <-sub.Done():
			return true
		default:
			return false
		}
	})
	if got := sub.DropReason(); got != DropReasonUpstreamClosed {
		t.Fatalf("drop reason = %q, want %q", got, DropReasonUpstreamClosed)
	}
	waitFor(t, "the hub to leave the registry", func() bool { return mgr.HubCount() == 0 })
	if hub.Serving() {
		t.Fatal("a hub whose upstream died still reports Serving")
	}
	if got := mgr.GetOrCreate("run-dead-upstream"); got == hub {
		t.Fatal("GetOrCreate reused the failed hub instead of building a fresh one")
	}
}

// TestHubUpstreamNotReadyDegradesToReplayOnly: a hub can be built while the
// SUBSCRIBE fails outright. Readiness must SETTLE immediately (so the request
// is not delayed), and the hub must be unusable — the caller then serves a
// durable-only replay.
//
// WaitReady's boolean means "the registration settled", not "it succeeded":
// both outcomes have to release the waiting request, and which one happened is
// answered by the next question, Serving().
func TestHubUpstreamNotReadyDegradesToReplayOnly(t *testing.T) {
	up := newFakeUpstream()
	up.failErr = fmt.Errorf("redis is down")
	opts := DefaultHubOptions()
	opts.UpstreamReadyTimeout = 3 * time.Second
	mgr := NewHubManagerWithUpstream(context.Background(), up, nil, opts)
	defer mgr.Close()

	hub := mgr.GetOrCreate("run-no-upstream")
	start := time.Now()
	if !hub.WaitReady(context.Background()) {
		t.Fatal("WaitReady reported a timeout although SUBSCRIBE failed immediately: " +
			"a settled failure must not be held until the readiness guard expires")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("WaitReady waited %s for an already-failed registration", elapsed)
	}
	if hub.Serving() {
		t.Fatal("a hub whose SUBSCRIBE failed still reports Serving")
	}
	if _, ok := hub.Subscribe(StreamProtocolRangeDelta); ok {
		t.Fatal("a hub whose SUBSCRIBE failed accepted a subscriber")
	}
	if got := mgr.HubCount(); got != 0 {
		t.Fatalf("HubCount = %d after a failed SUBSCRIBE, want 0 (the hub must leave the registry)", got)
	}
}

// blockingUpstream never settles its subscription until it is told to, so the
// readiness guard is the only thing that can release a waiting request.
type blockingUpstream struct {
	release chan struct{}
}

func (b *blockingUpstream) Subscribe(ctx context.Context, _ string) (HubUpstreamChannel, error) {
	select {
	case <-b.release:
	case <-ctx.Done():
	}
	return nil, ctx.Err()
}

// TestHubUpstreamReadyGuardIsBounded: a hung SUBSCRIBE must not hang the HTTP
// request forever. WaitReady gives up on its own budget and the request falls
// back to a durable-only replay — which loses nothing, because the client's
// next reconnect replays from its durable cursor.
func TestHubUpstreamReadyGuardIsBounded(t *testing.T) {
	blocked := &blockingUpstream{release: make(chan struct{})}
	defer close(blocked.release)

	opts := DefaultHubOptions()
	opts.UpstreamReadyTimeout = 60 * time.Millisecond
	mgr := NewHubManagerWithUpstream(context.Background(), blocked, nil, opts)
	defer mgr.Close()

	hub := mgr.GetOrCreate("run-hung-subscribe")
	start := time.Now()
	if hub.WaitReady(context.Background()) {
		t.Fatal("WaitReady reported a settled registration although SUBSCRIBE never returned")
	}
	if elapsed := time.Since(start); elapsed < opts.UpstreamReadyTimeout {
		t.Fatalf("WaitReady gave up after %s, before its %s guard", elapsed, opts.UpstreamReadyTimeout)
	}
	// The hub is not UNHEALTHY — the subscription may still come up, and a
	// later reconnect can legitimately reuse it — so it stays in the registry
	// and keeps Serving.
	if !hub.Serving() {
		t.Fatal("a hub whose SUBSCRIBE is merely slow was marked unusable")
	}
	if got := mgr.HubCount(); got != 1 {
		t.Fatalf("HubCount = %d, want 1 (a slow SUBSCRIBE must not evict the hub)", got)
	}
}

// TestHubRuntimeContextSurvivesStartupContextExpiry pins AC-11 and kills
// Mutation E.
//
// studio-stream builds its App from a context that expires 30 seconds after
// process start. If the hubs inherited that context, every SSE stream would be
// cancelled 30s in — a defect invisible to any test that finishes quickly, and
// invisible in production until it is happening to every user at once.
func TestHubRuntimeContextSurvivesStartupContextExpiry(t *testing.T) {
	up := newFakeUpstream()
	buildCtx, cancelBuild := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelBuild()

	mgr := NewHubManagerWithUpstream(buildCtx, up, nil, HubOptions{})
	defer mgr.Close()

	hub := mgr.GetOrCreate("run-long-lived")
	sub, ok := hub.Subscribe(StreamProtocolRangeDelta)
	if !ok {
		t.Fatal("subscriber refused")
	}
	collected := drainAsync(sub)
	waitFor(t, "the upstream registration", func() bool { return up.SubscribeCount() == 1 })

	<-buildCtx.Done()
	time.Sleep(50 * time.Millisecond)

	if !hub.Serving() {
		t.Fatal("the hub died with the startup context: the runtime context is inherited from app.Build")
	}
	if !hub.UpstreamActive() {
		t.Fatal("the hub's Redis subscription was torn down when the startup context expired: " +
			"the runtime context is inherited from app.Build, so every stream would die " +
			"30 seconds into the process")
	}
	up.publish(t, "run-long-lived", chunkEvent(1))
	waitFor(t, "an event after the startup context expired", func() bool { return collected.len() == 1 })
}

// TestHubManagerCloseIsIdempotentAndComplete (§51, AC-12).
func TestHubManagerCloseIsIdempotentAndComplete(t *testing.T) {
	up := newFakeUpstream()
	// A long idle TTL proves the teardown is Close and not the timer.
	opts := DefaultHubOptions()
	opts.IdleTTL = time.Hour
	mgr := NewHubManagerWithUpstream(context.Background(), up, nil, opts)

	var subs []*Subscriber
	for _, runID := range []string{"run-1", "run-2", "run-3"} {
		hub := mgr.GetOrCreate(runID)
		for i := 0; i < 10; i++ {
			sub, ok := hub.Subscribe(StreamProtocolRangeDelta)
			if !ok {
				t.Fatalf("%s: subscriber refused", runID)
			}
			subs = append(subs, sub)
		}
	}
	waitFor(t, "three upstream registrations", func() bool { return up.SubscribeCount() == 3 })

	mgr.Close()

	for i, sub := range subs {
		select {
		case <-sub.Done():
		default:
			t.Fatalf("subscriber %d is still open after Close", i)
		}
		if got := sub.DropReason(); got != DropReasonHubClosed {
			t.Fatalf("subscriber %d drop reason = %q, want %q", i, got, DropReasonHubClosed)
		}
	}
	if got := mgr.HubCount(); got != 0 {
		t.Fatalf("HubCount = %d after Close, want 0", got)
	}
	waitFor(t, "all upstreams to close", func() bool { return up.ActiveChannels() == 0 })

	// Close must be idempotent (a second call in a defer must not panic) and
	// the manager must refuse new work afterwards.
	mgr.Close()
	if got := mgr.GetOrCreate("run-after-close"); got != nil {
		t.Fatal("GetOrCreate returned a hub after Close")
	}
	if got := mgr.Lookup("run-1"); got != nil {
		t.Fatal("Lookup returned a hub after Close")
	}
}

// TestHubClientDisconnectIsNotADrop: a browser that simply goes away must not
// be counted as a hub-initiated drop, or the drop metric becomes noise and the
// real slow-consumer signal disappears.
func TestHubClientDisconnectIsNotADrop(t *testing.T) {
	up := newFakeUpstream()
	mgr := NewHubManagerWithUpstream(context.Background(), up, nil, HubOptions{})
	defer mgr.Close()

	hub := mgr.GetOrCreate("run-client-gone")
	sub, _ := hub.Subscribe(StreamProtocolRangeDelta)
	waitFor(t, "the upstream registration", func() bool { return up.SubscribeCount() == 1 })

	sub.Close(DropReasonClientGone)
	waitFor(t, "the hub to unregister the subscriber", func() bool { return hub.SubscriberCount() == 0 })
	if got := sub.DropReason(); got != DropReasonClientGone {
		t.Fatalf("drop reason = %q, want %q", got, DropReasonClientGone)
	}
	if !hub.Serving() {
		t.Fatal("a client disconnect closed the hub")
	}
}

// TestHubConcurrentLifecycle runs the races Batch 4 introduces under -race:
// subscribers arriving and leaving while events fan out, plus a shutdown
// concurrent with both.
func TestHubConcurrentLifecycle(t *testing.T) {
	up := newFakeUpstream()
	opts := DefaultHubOptions()
	opts.IdleTTL = 10 * time.Millisecond // exercise the idle timer too
	mgr := NewHubManagerWithUpstream(context.Background(), up, nil, opts)

	hub := mgr.GetOrCreate("run-race")
	waitFor(t, "the upstream registration", func() bool { return up.SubscribeCount() == 1 })

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Publisher.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := uint64(1); i <= 500; i++ {
			up.tryPublish("run-race", chunkEvent(i))
		}
	}()

	// Subscriber churn.
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			protocol := StreamProtocolLegacy
			if w%2 == 0 {
				protocol = StreamProtocolRangeDelta
			}
			for i := 0; i < 25; i++ {
				sub, ok := hub.Subscribe(protocol)
				if !ok {
					return
				}
				go drainAsync(sub)
				time.Sleep(time.Millisecond)
				sub.Close(DropReasonClientGone)
			}
		}(w)
	}

	// Closer.
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-stop:
		case <-time.After(20 * time.Millisecond):
		}
		mgr.Close()
	}()

	wg.Wait()
	close(stop)
	if got := mgr.HubCount(); got != 0 {
		t.Fatalf("HubCount = %d after concurrent Close, want 0", got)
	}
}
