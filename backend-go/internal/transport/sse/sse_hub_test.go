package sse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
)

// These tests drive the REAL Gateway.Stream over a fake durable reader and a
// fake Redis upstream, which is what makes the two most dangerous properties of
// the hub — "the subscriber is registered BEFORE the replay" and "a live
// transient frame is never interleaved into the historical replay" — testable
// without MySQL. Both properties lose user-visible text when they regress, and
// both are the kind of thing a plausible-looking refactor reverses silently.

// ─────────────────────────── fake durable reader ───────────────────────────

type fakeRunReader struct {
	mu       sync.Mutex
	runID    ids.ID
	events   []execution.EventRecord
	requests []uint64

	// readErr, when set, fails page reads. failFrom is the 1-based read whose
	// failure starts (0 = every read once readErr is set). It models MySQL
	// going away exactly when the gateway needs it — the gap-repair path that
	// must fail closed rather than guess.
	readErr  error
	failFrom int

	// onFirstRead runs ONCE, concurrently with the first page read — the
	// window between "the subscriber is registered" and "the replay has
	// finished" that the hub exists to protect.
	onFirstRead func()
	fired       bool
}

func newFakeRunReader(runID ids.ID, events []execution.EventRecord) *fakeRunReader {
	return &fakeRunReader{runID: runID, events: events}
}

func (r *fakeRunReader) ListEventPage(_ context.Context, _ ids.ID, after uint64, limit int) (*execution.EventPage, error) {
	r.mu.Lock()
	r.requests = append(r.requests, after)
	if r.readErr != nil && (r.failFrom <= 0 || len(r.requests) >= r.failFrom) {
		err := r.readErr
		r.mu.Unlock()
		return nil, err
	}
	hook := r.onFirstRead
	if !r.fired {
		r.fired = true
	} else {
		hook = nil
	}
	events := append([]execution.EventRecord(nil), r.events...)
	r.mu.Unlock()

	if hook != nil {
		hook()
	}

	page := &execution.EventPage{NextAfter: after}
	for _, ev := range events {
		if ev.Sequence <= after {
			continue
		}
		page.Items = append(page.Items, ev)
		page.NextAfter = ev.Sequence
		if limit > 0 && len(page.Items) >= limit {
			break
		}
	}
	page.HasMore = limit > 0 && len(page.Items) == limit
	return page, nil
}

// appendEvents adds rows to the log AFTER the reader is already serving.
//
// The page snapshot is taken before onFirstRead runs, so a hook that appends
// here models precisely the production shape the gap repair exists for: the
// client reconciled against a log that did not yet contain the event, and the
// event exists by the time a LATER read asks for it.
func (r *fakeRunReader) appendEvents(events ...execution.EventRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, events...)
}

// failReadsFrom makes read number `n` (1-based) and every later read fail.
func (r *fakeRunReader) failReadsFrom(n int, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failFrom = n
	r.readErr = err
}

func (r *fakeRunReader) observed() []uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]uint64(nil), r.requests...)
}

func (r *fakeRunReader) setFirstReadHook(fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onFirstRead = fn
	r.fired = false
}

// ─────────────────────────── thread-safe SSE writer ───────────────────────────

// streamWriter is an http.ResponseWriter + http.Flusher the test can read while
// the handler goroutine is still writing.
type streamWriter struct {
	mu      sync.Mutex
	header  http.Header
	body    bytes.Buffer
	status  int
	flushes int

	// done is closed when Stream returns, so a test can assert that the
	// handler ENDED (on a terminal event) rather than merely stopped writing.
	done chan struct{}
}

func newStreamWriter() *streamWriter {
	return &streamWriter{header: http.Header{}, done: make(chan struct{})}
}

// waitDone asserts the handler returned within timeout. A stream that keeps
// the connection open after a terminal event is a hang from the browser's
// point of view, so "it returned" is part of the contract.
func (w *streamWriter) waitDone(t *testing.T, timeout time.Duration) {
	t.Helper()
	select {
	case <-w.done:
	case <-time.After(timeout):
		t.Fatalf("Stream did not return within %s", timeout)
	}
}

func (w *streamWriter) Header() http.Header { return w.header }

func (w *streamWriter) WriteHeader(status int) {
	w.mu.Lock()
	w.status = status
	w.mu.Unlock()
}

func (w *streamWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.Write(p)
}

func (w *streamWriter) Flush() {
	w.mu.Lock()
	w.flushes++
	w.mu.Unlock()
}

func (w *streamWriter) text() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.String()
}

func (w *streamWriter) headerValue(key string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.header.Get(key)
}

type wireFrame struct {
	Sequence  uint64         `json:"sequence"`
	EventType string         `json:"event_type"`
	Payload   map[string]any `json:"payload"`
}

// frames parses whatever complete SSE frames the body holds so far.
func (w *streamWriter) frames() []wireFrame {
	var out []wireFrame
	for _, block := range strings.Split(w.text(), "\n\n") {
		for _, line := range strings.Split(block, "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var f wireFrame
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &f); err == nil {
				out = append(out, f)
			}
		}
	}
	return out
}

func (w *streamWriter) sequences() []uint64 {
	out := []uint64{}
	for _, f := range w.frames() {
		out = append(out, f.Sequence)
	}
	return out
}

// ─────────────────────────── harness ───────────────────────────

type gatewayHarness struct {
	gw     *Gateway
	reader *fakeRunReader
	up     *fakeUpstream
	mgr    *HubManager
	run    *execution.Run
	runID  ids.ID
	base   string
}

func newGatewayHarness(t *testing.T, opts HubOptions, status string, events ...execution.EventRecord) *gatewayHarness {
	t.Helper()
	runID := ids.New()
	run := &execution.Run{ID: runID, Status: status, Output: map[string]any{}}
	reader := newFakeRunReader(runID, events)
	up := newFakeUpstream()
	metrics := telemetry.NewMetrics("test")
	mgr := NewHubManagerWithUpstream(context.Background(), up, metrics, opts)
	gw := &Gateway{Runs: reader, Hub: mgr, Metrics: metrics, Keepalive: time.Hour}
	t.Cleanup(mgr.Close)
	return &gatewayHarness{
		gw: gw, reader: reader, up: up, mgr: mgr, run: run, runID: runID,
		base: fmt.Sprintf("http://example.test/v2/runs/%s/stream", runID.String()),
	}
}

// open starts one Stream call and returns its writer. The stream is cancelled
// and awaited at test end, so a handler that never returns fails the test
// instead of leaking into the next one.
func (h *gatewayHarness) open(t *testing.T, query string) *streamWriter {
	t.Helper()
	w := newStreamWriter()
	url := h.base
	if query != "" {
		url += "?" + query
	}
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, url, nil).WithContext(ctx)
	go func() {
		defer close(w.done)
		h.gw.Stream(w, req, h.run)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-w.done:
		case <-time.After(5 * time.Second):
			t.Errorf("Stream did not return after its request context was cancelled")
		}
	})
	return w
}

// waitFrames blocks until the stream has emitted at least want complete frames.
func waitFrames(t *testing.T, w *streamWriter, want int) []wireFrame {
	t.Helper()
	waitFor(t, fmt.Sprintf("%d SSE frames", want), func() bool { return len(w.frames()) >= want })
	return w.frames()
}

func runEvent(seq uint64, eventType string) execution.EventRecord {
	return execution.EventRecord{
		RunID:     ids.New().String(),
		Sequence:  seq,
		EventType: eventType,
		Payload:   map[string]any{"text": fmt.Sprintf("durable-%d", seq)},
	}
}

func runEvents(from, to uint64, eventType string) []execution.EventRecord {
	var out []execution.EventRecord
	for seq := from; seq <= to; seq++ {
		out = append(out, runEvent(seq, eventType))
	}
	return out
}

func counterValue(t *testing.T, m *telemetry.Metrics, name string) float64 {
	t.Helper()
	families, err := m.Registry.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, metric := range f.GetMetric() {
			if c := metric.GetCounter(); c != nil {
				return c.GetValue()
			}
			if g := metric.GetGauge(); g != nil {
				return g.GetValue()
			}
		}
	}
	return 0
}

// ─────────────────────────── tests ───────────────────────────

// TestGatewayRegistersSubscriberBeforeReplay pins AC-4 and kills Mutation D
// (§38).
//
// The subscriber is registered FIRST, so every frame published while the
// historical replay is still running is buffered in its queue and drained
// afterwards. Swapping the two steps opens a window in which a live event
// published during the replay reaches nobody: the reconnect cursor has already
// moved past it, so it is lost for good.
func TestGatewayRegistersSubscriberBeforeReplay(t *testing.T) {
	h := newGatewayHarness(t, HubOptions{}, execution.StatusRunning, runEvents(11, 14, execution.EventContentChunk)...)

	// Publish 15 and 16 in the middle of the replay — the exact window the
	// ordering rule protects.
	h.reader.setFirstReadHook(func() {
		if !h.up.tryPublish(h.runID.String(), chunkEvent(15)) {
			// Under the reversed order there is no subscription yet and the
			// payload goes nowhere. Record it and let the assertion below
			// report the loss.
			return
		}
		h.up.tryPublish(h.runID.String(), chunkEvent(16))
	})

	w := h.open(t, "after=10")
	waitFrames(t, w, 6)

	got := w.sequences()
	want := []uint64{11, 12, 13, 14, 15, 16}
	if len(got) != len(want) {
		t.Fatalf("delivered sequences = %v, want %v (a missing 15/16 means live events "+
			"published during the replay were dropped)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("delivered sequences = %v, want %v (in order, exactly once)", got, want)
		}
	}
	if got := h.up.SubscribeCount(); got != 1 {
		t.Fatalf("Redis Subscribe called %d times, want 1", got)
	}
}

// TestGatewayBuffersTransientDeltaUntilCatchUpCompletes pins AC-5 (§39).
//
// A transient delta for bytes far ahead of the replay position must NOT reach
// the socket first: the client's rendered byte offset would jump forward and
// the historical bytes in between would then be discarded as an old range,
// leaving a hole in the middle of the answer. It is buffered with everything
// else and drained in arrival order once the replay is done.
func TestGatewayBuffersTransientDeltaUntilCatchUpCompletes(t *testing.T) {
	h := newGatewayHarness(t, HubOptions{}, execution.StatusRunning, runEvents(1, 3, execution.EventContentChunk)...)

	h.reader.setFirstReadHook(func() {
		h.up.tryPublish(h.runID.String(), deltaEvent(1000))
	})

	w := h.open(t, "stream_protocol=2&after=0")
	waitFrames(t, w, 4)

	frames := w.frames()
	for i, f := range frames[:3] {
		if f.Sequence != uint64(i+1) {
			t.Fatalf("frame %d = %+v, want durable sequence %d", i, f, i+1)
		}
	}
	lastFrame := frames[len(frames)-1]
	if lastFrame.EventType != execution.EventContentDelta {
		t.Fatalf("last frame = %+v, want the transient delta AFTER every replayed chunk: "+
			"a transient frame interleaved into the historical replay breaks byte-offset progress", lastFrame)
	}
	if lastFrame.Sequence != 0 {
		t.Fatalf("transient frame carried sequence %d, want 0", lastFrame.Sequence)
	}
	if !strings.Contains(w.text(), "id: 3\n") {
		t.Fatal("durable frames lost their `id:` line")
	}
	if strings.Contains(w.text(), "id: 0\n") {
		t.Fatal("a transient frame wrote an `id: 0` line: the browser's next Last-Event-ID " +
			"would be 0 and every replay would restart from the beginning")
	}
}

// TestGatewayCacheHitSkipsHistoricalDBRead pins AC-7's payoff and the §29 metric
// split: a cache hit must serve the range without a database read for it, and
// only the cache's high-water mark may be reconciled against MySQL.
func TestGatewayCacheHitSkipsHistoricalDBRead(t *testing.T) {
	h := newGatewayHarness(t, HubOptions{}, execution.StatusRunning, runEvents(101, 200, execution.EventContentChunk)...)

	hub := h.mgr.GetOrCreate(h.runID.String())
	for seq := uint64(101); seq <= 200; seq++ {
		hub.RememberDurable(seq, execution.EventContentChunk, map[string]any{"text": fmt.Sprintf("cached-%d", seq)})
	}

	w := h.open(t, "after=150")
	waitFor(t, "50 cached frames", func() bool { return len(w.frames()) >= 50 })

	got := w.sequences()[:50]
	for i, seq := range got {
		if seq != uint64(151+i) {
			t.Fatalf("frame %d = %d, want %d", i, seq, 151+i)
		}
	}
	requests := h.reader.observed()
	if len(requests) != 1 || requests[0] != 200 {
		t.Fatalf("durable reads = %v, want exactly [200]: a cache hit must not re-read the "+
			"history it just served, only reconcile the tail", requests)
	}
	if got := counterValue(t, h.gw.Metrics, "studio_sse_hub_cache_replay_total"); got != 50 {
		t.Fatalf("studio_sse_hub_cache_replay_total = %v, want 50", got)
	}
	if got := counterValue(t, h.gw.Metrics, "studio_sse_replay_events_total"); got != 0 {
		t.Fatalf("studio_sse_replay_events_total = %v, want 0 (cache replays are not DB reads)", got)
	}
}

// TestGatewayCacheMissReplaysFromMySQL: when the cache cannot prove continuity
// the client still gets everything, from MySQL (§41).
func TestGatewayCacheMissReplaysFromMySQL(t *testing.T) {
	h := newGatewayHarness(t, HubOptions{}, execution.StatusRunning, runEvents(11, 600, execution.EventContentChunk)...)

	hub := h.mgr.GetOrCreate(h.runID.String())
	for seq := uint64(1000); seq <= 1200; seq++ {
		hub.RememberDurable(seq, execution.EventContentChunk, map[string]any{"text": "recent"})
	}

	w := h.open(t, "after=10")
	waitFor(t, "590 frames", func() bool { return len(w.frames()) >= 590 })

	got := w.sequences()[:590]
	for i, seq := range got {
		if seq != uint64(11+i) {
			t.Fatalf("frame %d = %d, want %d", i, seq, 11+i)
		}
	}
	if requests := h.reader.observed(); len(requests) == 0 || requests[0] != 10 {
		t.Fatalf("durable reads = %v, want the first one from the client cursor 10", requests)
	}
	if got := counterValue(t, h.gw.Metrics, "studio_sse_hub_cache_miss_total"); got != 1 {
		t.Fatalf("studio_sse_hub_cache_miss_total = %v, want 1", got)
	}
	if got := counterValue(t, h.gw.Metrics, "studio_sse_hub_cache_replay_total"); got != 0 {
		t.Fatalf("studio_sse_hub_cache_replay_total = %v, want 0 on a miss", got)
	}
}

// TestGatewayCacheGapFallsBackToMySQL pins AC-8 end-to-end and kills Mutation F
// (§42).
//
// The hub observed 100,101 and then 105,106: 102-104 are MISSING. A client
// resuming from 101 must be sent 102,103,104 from MySQL. Treating the cache as
// covering that range would silently skip three events while advancing the
// client's cursor past them — the failure mode that makes a wrong "cache hit"
// worse than no cache at all.
func TestGatewayCacheGapFallsBackToMySQL(t *testing.T) {
	h := newGatewayHarness(t, HubOptions{}, execution.StatusRunning, runEvents(102, 104, execution.EventContentChunk)...)

	hub := h.mgr.GetOrCreate(h.runID.String())
	for _, seq := range []uint64{100, 101, 105, 106} {
		hub.RememberDurable(seq, execution.EventContentChunk, map[string]any{"text": "cached"})
	}
	if high := hub.CacheHighWater(); high != 106 {
		t.Fatalf("cache high-water = %d, want 106", high)
	}
	if first := hub.cache.FirstSeq(); first != 105 {
		t.Fatalf("cache first sequence = %d, want 105: the gap must invalidate the older segment", first)
	}

	w := h.open(t, "after=101")
	waitFrames(t, w, 3)

	got := w.sequences()
	want := []uint64{102, 103, 104}
	if len(got) != len(want) {
		t.Fatalf("delivered = %v, want %v: events inside a cache gap must come from MySQL, "+
			"never be skipped", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("delivered = %v, want %v", got, want)
		}
	}
	if requests := h.reader.observed(); len(requests) == 0 || requests[0] != 101 {
		t.Fatalf("durable reads = %v, want the first one from the client cursor 101", requests)
	}
}

// TestGatewaySettledRunOpensNoUpstream (§20): a finished run must never pay for
// a Redis subscription — the durable replay plus the synthetic terminal frame
// is the whole contract.
func TestGatewaySettledRunOpensNoUpstream(t *testing.T) {
	h := newGatewayHarness(t, HubOptions{}, execution.StatusSucceeded,
		runEvent(1, execution.EventContentChunk),
		runEvent(2, execution.EventRunCompleted),
	)

	w := h.open(t, "")
	waitFor(t, "the terminal frame", func() bool { return len(w.frames()) >= 2 })

	if got := h.up.SubscribeCount(); got != 0 {
		t.Fatalf("Redis Subscribe called %d times for an already-settled run, want 0", got)
	}
	if got := h.mgr.HubCount(); got != 0 {
		t.Fatalf("HubCount = %d for an already-settled run, want 0", got)
	}
	if got := w.sequences(); len(got) != 2 || got[1] != 2 {
		t.Fatalf("delivered = %v, want the replayed history ending at the terminal sequence", got)
	}
}

// TestGatewaySettledRunReusesRetainedHubCache (§21): a hub retained from the
// run's live phase keeps serving from its cache for IdleTTL — WITHOUT opening a
// second upstream.
func TestGatewaySettledRunReusesRetainedHubCache(t *testing.T) {
	h := newGatewayHarness(t, HubOptions{}, execution.StatusSucceeded)
	hub := h.mgr.GetOrCreate(h.runID.String())
	waitFor(t, "the upstream registration", func() bool { return h.up.SubscribeCount() == 1 })
	for seq := uint64(1); seq <= 5; seq++ {
		hub.RememberDurable(seq, execution.EventContentChunk, map[string]any{"text": "retained"})
	}

	w := h.open(t, "after=2")
	waitFrames(t, w, 3)

	if got := w.sequences()[:3]; got[0] != 3 || got[1] != 4 || got[2] != 5 {
		t.Fatalf("delivered = %v, want the retained cache to serve [3 4 5]", got[:3])
	}
	if got := h.up.SubscribeCount(); got != 1 {
		t.Fatalf("Redis Subscribe called %d times, want 1: a settled run reuses the retained hub, never subscribes again", got)
	}
}

// TestGatewaySharedHubKeepsPerConnectionProtocol is the end-to-end version of
// AC-2/AC-3: two REAL streams of the same run, different protocols, one hub.
func TestGatewaySharedHubKeepsPerConnectionProtocol(t *testing.T) {
	h := newGatewayHarness(t, HubOptions{}, execution.StatusRunning, runEvent(1, execution.EventContentChunk))

	legacy := h.open(t, "stream_protocol=1&after=0")
	v2 := h.open(t, "stream_protocol=2&after=0")
	waitFrames(t, legacy, 1)
	waitFrames(t, v2, 1)

	if got := legacy.headerValue(StreamProtocolHeader); got != "1" {
		t.Fatalf("legacy %s = %q, want \"1\"", StreamProtocolHeader, got)
	}
	if got := v2.headerValue(StreamProtocolHeader); got != "2" {
		t.Fatalf("v2 %s = %q, want \"2\"", StreamProtocolHeader, got)
	}
	if got := h.up.SubscribeCount(); got != 1 {
		t.Fatalf("Redis Subscribe called %d times for two viewers of one run, want 1", got)
	}

	h.up.publish(t, h.runID.String(), deltaEvent(4096))
	h.up.publish(t, h.runID.String(), chunkEvent(2))

	waitFrames(t, legacy, 2)
	waitFrames(t, v2, 3)

	for _, f := range legacy.frames() {
		if f.EventType == execution.EventContentDelta {
			t.Fatal("the protocol-1 connection received a transient delta: it has no byte offset to reconcile it with")
		}
	}
	var sawDelta bool
	for _, f := range v2.frames() {
		if f.EventType == execution.EventContentDelta {
			sawDelta = true
		}
	}
	if !sawDelta {
		t.Fatal("the protocol-2 connection did not receive the transient delta")
	}
}

// TestGatewayEndsStreamOnLiveTerminal: a terminal event delivered through the
// hub closes every stream on that run — the pre-Batch-4 contract, now enforced
// once for N connections.
func TestGatewayEndsStreamOnLiveTerminal(t *testing.T) {
	h := newGatewayHarness(t, HubOptions{}, execution.StatusRunning, runEvent(1, execution.EventContentChunk))

	w := h.open(t, "after=0")
	waitFrames(t, w, 1)

	h.up.publish(t, h.runID.String(), terminalEvent(2, execution.EventRunCompleted))

	// The handler must return ON ITS OWN — not when the request context is
	// cancelled. Waiting for the context would keep the connection open after
	// the client was told the run ended (keepalive forever).
	w.waitDone(t, 3*time.Second)

	got := w.sequences()
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("delivered = %v, want [1 2] ending at the terminal frame", got)
	}
	if got := h.up.ActiveChannels(); got != 0 {
		t.Fatalf("active upstream channels after the terminal = %d, want 0", got)
	}
}

// TestGatewayKeepaliveIsConnectionScoped (§27): the 15s silence comment belongs
// to the HTTP connection, not the hub. The hub fans out run events only — a
// keepalive fanned out per run would be wrong (it must be per connection) and
// would also pollute every subscriber's queue.
func TestGatewayKeepaliveIsConnectionScoped(t *testing.T) {
	h := newGatewayHarness(t, HubOptions{}, execution.StatusRunning)
	h.gw.Keepalive = 20 * time.Millisecond

	w := h.open(t, "")
	waitFor(t, "a keepalive comment", func() bool {
		return strings.Contains(w.text(), ": keepalive")
	})
	if got := w.frames(); len(got) != 0 {
		t.Fatalf("keepalive produced %d data frames, want 0", len(got))
	}
	if got := h.up.SubscribeCount(); got != 1 {
		t.Fatalf("Redis Subscribe called %d times for an idle live run, want 1", got)
	}
}

// TestGatewayDegradesToDurableReplayWhenUpstreamFails: when the hub cannot carry
// live events the client still receives the durable history and then the stream
// ends, so the browser reconnects with its cursor instead of hanging on a
// connection that can never advance.
func TestGatewayDegradesToDurableReplayWhenUpstreamFails(t *testing.T) {
	runID := ids.New()
	run := &execution.Run{ID: runID, Status: execution.StatusRunning, Output: map[string]any{}}
	reader := newFakeRunReader(runID, runEvents(1, 3, execution.EventContentChunk))
	up := newFakeUpstream()
	up.failErr = fmt.Errorf("redis is down")
	opts := DefaultHubOptions()
	opts.UpstreamReadyTimeout = 50 * time.Millisecond
	mgr := NewHubManagerWithUpstream(context.Background(), up, nil, opts)
	defer mgr.Close()
	gw := &Gateway{Runs: reader, Hub: mgr, Keepalive: time.Hour}

	w := newStreamWriter()
	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("http://example.test/v2/runs/%s/stream?after=0", runID.String()), nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		gw.Stream(w, req, run)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stream hung on a hub whose SUBSCRIBE failed: it must degrade to a durable replay and return")
	}
	if got := w.sequences(); len(got) != 3 {
		t.Fatalf("delivered %v, want the full durable replay [1 2 3]", got)
	}
}

// ───────────── Batch 4.1: durable live ordering (P1-1) ─────────────
//
// These three tests all rest on one production fact: a durable event is
// published AFTER its transaction commits, so two writers can allocate 101
// then 102, commit in that order, and still reach Redis in the other order.
// The publisher cannot be asked to fix it and the sequence allocator is not
// broken — the gateways's job is to never let the CLIENT see the hole.

// TestGatewayRepairsOutOfOrderDurableLiveEvent is AC-4.1-3 and kills
// Mutation H (§32).
//
// Redis delivers 102 and then 101. A gateway that writes what it is given
// hands the client 102 first, and the client's durable cursor — the highest
// sequence it RENDERED — is then past 101. Reconnect with `after=102` and 101
// is gone for good: not delayed, not reordered, LOST. The fix is to refuse the
// forward jump and fill [101] from MySQL, which is the ordering authority.
func TestGatewayRepairsOutOfOrderDurableLiveEvent(t *testing.T) {
	h := newGatewayHarness(t, HubOptions{}, execution.StatusRunning)

	h.reader.setFirstReadHook(func() {
		// Both halves are load-bearing: the events must be MISSING from the
		// replay snapshot (so the client's cursor really is 100 with 101
		// unseen) and present when the repair asks again.
		h.reader.appendEvents(
			runEvent(101, execution.EventContentChunk),
			runEvent(102, execution.EventToolCompleted),
		)
		h.up.tryPublish(h.runID.String(), chunkEvent(102))
		h.up.tryPublish(h.runID.String(), chunkEvent(101))
	})

	w := h.open(t, "after=100")
	waitFrames(t, w, 2)

	got := w.sequences()
	want := []uint64{101, 102}
	if len(got) != len(want) {
		t.Fatalf("delivered = %v, want %v (101 must be repaired into the stream, not skipped)",
			got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("delivered = %v, want %v in order, exactly once", got, want)
		}
	}
	// The late 101 that Redis delivered afterwards must be a no-op: it is at
	// or below the repaired cursor.
	if n := countSeq(got, 101); n != 1 {
		t.Fatalf("101 was delivered %d times, want exactly 1", n)
	}

	if requests := h.reader.observed(); len(requests) != 2 || requests[0] != 100 || requests[1] != 100 {
		t.Fatalf("durable reads = %v, want [100 100]: the replay from the client cursor, then the "+
			"repair from the same contiguous position", requests)
	}
	if got := counterValue(t, h.gw.Metrics, "studio_sse_live_gap_repair_total"); got != 1 {
		t.Fatalf("studio_sse_live_gap_repair_total = %v, want 1", got)
	}
	if results := labelValues(t, h.gw.Metrics, "studio_sse_live_gap_repair_total", "result"); !results[telemetry.LiveGapRepaired] || len(results) != 1 {
		t.Fatalf("studio_sse_live_gap_repair_total results = %v, want exactly {%q}: the label is a "+
			"closed enumeration, never a run id, a sequence or an error string", results,
			telemetry.LiveGapRepaired)
	}
	if got := counterValue(t, h.gw.Metrics, "studio_sse_replay_events_total"); got != 2 {
		t.Fatalf("studio_sse_replay_events_total = %v, want 2: a repaired frame IS a MySQL replay",
			got)
	}
}

// TestGatewayRepairsGapBeforeTerminal is AC-4.1-4.
//
// Redis delivers ONLY run.completed(102) while 101 is missing. Writing the
// terminal would make the client set `sawTerminal` and stop reading — 101 then
// has no route to the browser at all, because the cursor it resumes from is
// already 102. The terminal is a hard boundary, but the frames BEFORE it are
// not optional.
func TestGatewayRepairsGapBeforeTerminal(t *testing.T) {
	h := newGatewayHarness(t, HubOptions{}, execution.StatusRunning)

	h.reader.setFirstReadHook(func() {
		h.reader.appendEvents(
			runEvent(101, execution.EventContentChunk),
			runEvent(102, execution.EventRunCompleted),
		)
		h.up.tryPublish(h.runID.String(), terminalEvent(102, execution.EventRunCompleted))
	})

	w := h.open(t, "after=100")
	w.waitDone(t, 3*time.Second)

	got := w.sequences()
	want := []uint64{101, 102}
	if len(got) != len(want) || got[0] != 101 || got[1] != 102 {
		t.Fatalf("delivered = %v, want %v: the run.completed that arrived out of order must NOT be "+
			"written on its own — the client would stop reading and never see 101", got, want)
	}
	frames := w.frames()
	if last := frames[len(frames)-1]; last.EventType != execution.EventRunCompleted {
		t.Fatalf("last frame = %+v, want %s", last, execution.EventRunCompleted)
	}
}

// TestGatewayFailsClosedWhenGapRepairFails is AC-4.1-5 and kills Mutation I
// (§32).
//
// The hole cannot be filled (MySQL is gone), so there are two bad answers and
// one right one. Sending the frame that exposed the hole loses 101 silently;
// waiting forever hangs the stream. The right one is to END the connection
// WITHOUT writing anything: the client reconnects from its last CONTIGUOUS
// cursor and the normal replay delivers the range.
func TestGatewayFailsClosedWhenGapRepairFails(t *testing.T) {
	h := newGatewayHarness(t, HubOptions{}, execution.StatusRunning)
	// Read 1 is the replay (fine); read 2 is the repair, and MySQL is gone.
	h.reader.failReadsFrom(2, errors.New("mysql gone away"))

	h.reader.setFirstReadHook(func() {
		h.up.tryPublish(h.runID.String(), chunkEvent(2))
	})

	w := h.open(t, "after=0")
	w.waitDone(t, 3*time.Second)

	if got := w.sequences(); len(got) != 0 {
		t.Fatalf("delivered %v, want NOTHING: after 0 the next contiguous sequence is 1, and writing "+
			"2 would advance the client's cursor past a frame it never saw", got)
	}
	if requests := h.reader.observed(); len(requests) != 2 {
		t.Fatalf("durable reads = %v, want 2 (the replay and the repair attempt): a repair that never "+
			"asked MySQL is not a repair", requests)
	}
	if got := counterValue(t, h.gw.Metrics, "studio_sse_live_gap_repair_total"); got != 1 {
		t.Fatalf("studio_sse_live_gap_repair_total = %v, want 1 (failed)", got)
	}
	if results := labelValues(t, h.gw.Metrics, "studio_sse_live_gap_repair_total", "result"); !results[telemetry.LiveGapFailed] || len(results) != 1 {
		t.Fatalf("studio_sse_live_gap_repair_total results = %v, want exactly {%q}", results,
			telemetry.LiveGapFailed)
	}
	if got := counterValue(t, h.gw.Metrics, "studio_sse_replay_events_total"); got != 0 {
		t.Fatalf("studio_sse_replay_events_total = %v, want 0: nothing was written from the log", got)
	}
}

// TestGatewayDeliversOversizedTerminalWithoutCachingIt is §37: the end-to-end
// proof that the P1-2 cache rule does not turn into a delivery bug.
//
// The terminal frame carries the run's WHOLE answer, so it is the event most
// likely to exceed SSE_HUB_CACHE_BYTES. The requirements are all three at once,
// and the test asserts them in that order:
//
//	the subscriber receives it IN FULL   (not cached ≠ not sent)
//	the hub cache stays within its bound (CacheMaxBytes is a memory bound)
//	a reconnect still gets it from MySQL (not cached ≠ lost)
func TestGatewayDeliversOversizedTerminalWithoutCachingIt(t *testing.T) {
	opts := DefaultHubOptions()
	opts.CacheMaxBytes = 1024
	opts.SubscriberMaxBytes = 64 << 10

	h := newGatewayHarness(t, opts, execution.StatusRunning, runEvent(1, execution.EventContentChunk))

	w := h.open(t, "after=0")
	waitFrames(t, w, 1)

	answer := strings.Repeat("x", 4096)
	h.up.publish(t, h.runID.String(), HubEvent{
		Sequence:  2,
		EventType: execution.EventRunCompleted,
		Payload:   map[string]any{"status": execution.StatusSucceeded, "text": answer},
	})

	frames := waitFrames(t, w, 2)
	w.waitDone(t, 3*time.Second)

	live := frames[len(frames)-1]
	if live.EventType != execution.EventRunCompleted {
		t.Fatalf("last live frame = %+v, want %s", live, execution.EventRunCompleted)
	}
	if got, _ := live.Payload["text"].(string); got != answer {
		t.Fatalf("the live terminal frame carried %d bytes of text, want %d: an event too large to "+
			"cache is still delivered in full — the cache is not the transport", len(got), len(answer))
	}

	hub := h.mgr.Lookup(h.runID.String())
	if hub == nil {
		t.Fatal("the hub was evicted before the cache bound could be read")
	}
	if got := hub.CacheBytes(); got > opts.CacheMaxBytes {
		t.Fatalf("hub cache holds %d bytes with CacheMaxBytes = %d: the byte bound is a memory "+
			"safety limit, not a target", got, opts.CacheMaxBytes)
	}
	if got := hub.CacheLen(); got != 0 {
		t.Fatalf("hub cache still holds %d events after a terminal larger than the whole budget, want 0",
			got)
	}

	// The log keeps the frame, so the reconnect is served the same terminal in
	// full — no user-visible difference from a cached one.
	h.reader.appendEvents(execution.EventRecord{
		Sequence:  2,
		EventType: execution.EventRunCompleted,
		Payload:   map[string]any{"status": execution.StatusSucceeded, "text": answer},
	})

	reconnect := h.open(t, "after=1")
	again := waitFrames(t, reconnect, 1)
	reconnect.waitDone(t, 3*time.Second)

	if got, _ := again[0].Payload["text"].(string); got != answer {
		t.Fatalf("the reconnect got %d bytes of terminal text, want %d: a cache miss must fall back "+
			"to the log, where the event is complete", len(got), len(answer))
	}
}

// countSeq counts how many frames carried a given durable sequence.
func countSeq(seqs []uint64, want uint64) int {
	n := 0
	for _, seq := range seqs {
		if seq == want {
			n++
		}
	}
	return n
}
