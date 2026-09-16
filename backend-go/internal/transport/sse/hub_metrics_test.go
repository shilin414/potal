package sse

import (
	"context"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/platform/config"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
)

// gatherFamilies reads the registry once; the assertions below walk it by name.
func gatherFamilies(t *testing.T, m *telemetry.Metrics) []*dto.MetricFamily {
	t.Helper()
	families, err := m.Registry.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	return families
}

// TestHubDefaultsMatchConfigDefaults is a cross-package drift guard.
//
// config.SSEConfig and sse.DefaultHubOptions are two copies of the same five
// numbers, and they cannot be unified: config sits at the bottom of the import
// graph (redisx imports it), so it may not import this package. If they drift,
// the defaults documented to operators are no longer the defaults the hub
// actually applies — the worst kind of documentation bug, because the
// configuration file looks right.
//
// It also pins the second safety net: HubOptions.normalized() must fill every
// non-positive bound with the default, so a programmatically constructed config
// cannot disable a bound either.
func TestHubDefaultsMatchConfigDefaults(t *testing.T) {
	for _, key := range []string{
		"SSE_HUB_CACHE_EVENTS",
		"SSE_HUB_CACHE_BYTES",
		"SSE_SUBSCRIBER_QUEUE_EVENTS",
		"SSE_SUBSCRIBER_QUEUE_BYTES",
		"SSE_HUB_IDLE_TTL",
	} {
		t.Setenv(key, "")
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	def := DefaultHubOptions()
	if cfg.SSE.HubCacheEvents != def.CacheMaxEvents ||
		cfg.SSE.HubCacheBytes != def.CacheMaxBytes ||
		cfg.SSE.SubscriberEvents != def.SubscriberMaxEvents ||
		cfg.SSE.SubscriberBytes != def.SubscriberMaxBytes ||
		cfg.SSE.HubIdleTTL != def.IdleTTL {
		t.Fatalf("config.SSEConfig defaults (%+v) and sse.DefaultHubOptions (%+v) have drifted",
			cfg.SSE, def)
	}

	if got := (HubOptions{}).normalized(); got != def {
		t.Fatalf("HubOptions{}.normalized() = %+v, want %+v", got, def)
	}
	if got := (HubOptions{CacheMaxEvents: -1, CacheMaxBytes: -1, SubscriberMaxEvents: -1,
		SubscriberMaxBytes: -1, IdleTTL: -time.Second}).normalized(); got != def {
		t.Fatalf("negative HubOptions normalized to %+v, want the defaults %+v "+
			"(a bound must never degrade to \"unlimited\")", got, def)
	}
	if got := (HubOptions{UpstreamReadyTimeout: 0}).normalized(); got.UpstreamReadyTimeout != def.UpstreamReadyTimeout {
		t.Fatalf("UpstreamReadyTimeout = %s, want %s", got.UpstreamReadyTimeout, def.UpstreamReadyTimeout)
	}
}

// TestHubMetricLabelsStayBounded drives EVERY drop and failure path and asserts
// the metric labels stay inside their documented enumeration.
//
// This is the same class of defect 第九轮补丁 3.3.1-B fixed for
// `stream_protocol`: a label that can carry a run id, a user id or an error
// string is an unbounded time series, and an SSE metric is written per
// connection — so one chatty tenant can take down a Prometheus instance. The
// paths below are the only ones that write these labels, which is why driving
// them (rather than reading the constants) is the assertion that counts.
func TestHubMetricLabelsStayBounded(t *testing.T) {
	metrics := telemetry.NewMetrics("test")
	opts := DefaultHubOptions()
	opts.SubscriberMaxEvents = 2
	up := newFakeUpstream()
	mgr := NewHubManagerWithUpstream(context.Background(), up, metrics, opts)

	isClosed := func(sub *Subscriber) bool {
		select {
		case <-sub.Done():
			return true
		default:
			return false
		}
	}

	// slow_consumer: a queue that overflows.
	slowHub := mgr.GetOrCreate("run-label-slow")
	slow, _ := slowHub.Subscribe(StreamProtocolRangeDelta)
	waitFor(t, "the first upstream registration", func() bool { return up.SubscribeCount() == 1 })
	for i := 1; i <= 6; i++ {
		up.publish(t, "run-label-slow", chunkEvent(uint64(i)))
	}
	waitFor(t, "the slow subscriber to be dropped", func() bool { return isClosed(slow) })

	// upstream_closed: the transport dies under a live hub.
	upHub := mgr.GetOrCreate("run-label-upstream")
	upSub, _ := upHub.Subscribe(StreamProtocolRangeDelta)
	waitFor(t, "the second upstream registration", func() bool { return up.SubscribeCount() == 2 })
	up.failChannel("run-label-upstream")
	waitFor(t, "the subscriber of the failed upstream", func() bool { return isClosed(upSub) })

	// client_gone: a browser that simply goes away. It must be recorded as the
	// reason but NEVER counted, or the drop metric becomes noise.
	goneHub := mgr.GetOrCreate("run-label-gone")
	gone, _ := goneHub.Subscribe(StreamProtocolRangeDelta)
	gone.Close(DropReasonClientGone)
	if got := gone.DropReason(); got != DropReasonClientGone {
		t.Fatalf("drop reason = %q, want %q", got, DropReasonClientGone)
	}

	// hub_closed: an explicit shutdown.
	closeHub := mgr.GetOrCreate("run-label-close")
	closeSub, _ := closeHub.Subscribe(StreamProtocolRangeDelta)
	mgr.Close()
	waitFor(t, "the subscriber of the closed hub", func() bool { return isClosed(closeSub) })

	reasons := labelValues(t, metrics, "studio_sse_hub_subscriber_dropped_total", "reason")
	allowedReasons := map[string]bool{
		DropReasonSlowConsumer:   true,
		DropReasonHubClosed:      true,
		DropReasonUpstreamClosed: true,
	}
	for reason := range reasons {
		if !allowedReasons[reason] {
			t.Fatalf("studio_sse_hub_subscriber_dropped_total has reason=%q — the label is not "+
				"a bounded enumeration (a run id or an error string here mints time series per "+
				"connection)", reason)
		}
	}
	if reasons[DropReasonClientGone] {
		t.Fatalf("a client disconnect was counted as a hub drop (reason=%q): the real slow-consumer "+
			"signal would drown in normal churn", DropReasonClientGone)
	}
	for reason := range allowedReasons {
		if !reasons[reason] {
			t.Errorf("drop reason %q was never recorded although its path ran", reason)
		}
	}

	stages := labelValues(t, metrics, "studio_sse_hub_upstream_failure_total", "stage")
	allowedStages := map[string]bool{
		"subscribe": true, "receive": true, "channel_closed": true, "decode": true,
	}
	for stage := range stages {
		if !allowedStages[stage] {
			t.Fatalf("studio_sse_hub_upstream_failure_total has stage=%q, want one of "+
				"subscribe|receive|channel_closed|decode", stage)
		}
	}
	if !stages["receive"] {
		t.Errorf("a subscription that died under a live hub was not attributed to the receive stage: got %v", stages)
	}

	// Every gauge must balance back to zero; an unbalanced Inc/Dec is how an
	// SSE gauge starts reporting phantom hubs and connections.
	for _, name := range []string{
		"studio_sse_hubs_active",
		"studio_sse_hub_upstreams_active",
		"studio_sse_hub_subscribers_active",
	} {
		waitFor(t, name+" to return to zero", func() bool { return gaugeSum(t, metrics, name) == 0 })
	}
}

// labelValues collects the distinct values of one label on a metric family.
func labelValues(t *testing.T, m *telemetry.Metrics, family, label string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, f := range gatherFamilies(t, m) {
		if f.GetName() != family {
			continue
		}
		for _, metric := range f.GetMetric() {
			for _, lp := range metric.GetLabel() {
				if lp.GetName() == label {
					out[lp.GetValue()] = true
				}
			}
		}
	}
	return out
}

// gaugeSum sums every series of a (possibly labeled) gauge family.
func gaugeSum(t *testing.T, m *telemetry.Metrics, family string) float64 {
	t.Helper()
	var sum float64
	for _, f := range gatherFamilies(t, m) {
		if f.GetName() != family {
			continue
		}
		for _, metric := range f.GetMetric() {
			if g := metric.GetGauge(); g != nil {
				sum += g.GetValue()
			}
		}
	}
	return sum
}

// TestStaleHubCannotRestoreCacheMetricsAfterEviction is AC-4.1-7 and kills
// Mutation K (§34).
//
// A hub can be past the point where it released hub.mu — inside `remember`, not
// yet at the gauge report — when the idle timer evicts it. Without an identity
// check the evicted generation writes its numbers back into `cacheStats` for a
// run that now has NO hub: `hubs_active=0` with `cache_bytes>0`, which is a
// state no dashboard can explain and no new event will ever correct, because a
// finished run produces none. The stream is fine; every decision made from the
// metrics is not.
func TestStaleHubCannotRestoreCacheMetricsAfterEviction(t *testing.T) {
	metrics := telemetry.NewMetrics("test")
	mgr := NewHubManagerWithUpstream(context.Background(), newFakeUpstream(), metrics, HubOptions{})
	t.Cleanup(mgr.Close)

	hub := mgr.GetOrCreate("run-stale-metrics")
	hub.RememberDurable(1, execution.EventContentChunk, map[string]any{"text": "one"})
	if got := gaugeSum(t, metrics, "studio_sse_hub_cache_events"); got != 1 {
		t.Fatalf("studio_sse_hub_cache_events = %v after one cached event, want 1", got)
	}

	// The hub leaves the registry (idle eviction / upstream failure).
	if !mgr.removeIfSame("run-stale-metrics", hub) {
		t.Fatal("removeIfSame refused to evict the current hub")
	}
	if got := gaugeSum(t, metrics, "studio_sse_hub_cache_events"); got != 0 {
		t.Fatalf("studio_sse_hub_cache_events = %v after eviction, want 0", got)
	}

	// The straggling report from the evicted generation.
	mgr.reportCache("run-stale-metrics", hub, 10, 4096)

	if got := mgr.HubCount(); got != 0 {
		t.Fatalf("HubCount = %d, want 0", got)
	}
	if got := gaugeSum(t, metrics, "studio_sse_hub_cache_events"); got != 0 {
		t.Fatalf("studio_sse_hub_cache_events = %v: an evicted hub restored its cache gauge, so the "+
			"process now reports cache for a hub that does not exist", got)
	}
	if got := gaugeSum(t, metrics, "studio_sse_hub_cache_bytes"); got != 0 {
		t.Fatalf("studio_sse_hub_cache_bytes = %v: phantom cache bytes from an evicted generation", got)
	}
}

// TestOldHubCannotOverwriteNewHubCacheMetrics is the second half of AC-4.1-7:
// the fence is per GENERATION, not merely "is the run tracked".
//
// After a reconnect the run has a NEW hub, and the old generation's delayed
// report arrives. It must be discarded on identity, not accepted because the
// run id is still present.
func TestOldHubCannotOverwriteNewHubCacheMetrics(t *testing.T) {
	metrics := telemetry.NewMetrics("test")
	mgr := NewHubManagerWithUpstream(context.Background(), newFakeUpstream(), metrics, HubOptions{})
	t.Cleanup(mgr.Close)

	old := mgr.GetOrCreate("run-generation")
	old.RememberDurable(1, execution.EventContentChunk, map[string]any{"text": "old"})
	if !mgr.removeIfSame("run-generation", old) {
		t.Fatal("removeIfSame refused to evict the first generation")
	}

	current := mgr.GetOrCreate("run-generation")
	if current == old {
		t.Fatal("GetOrCreate returned the evicted generation")
	}
	for seq := uint64(1); seq <= 2; seq++ {
		current.RememberDurable(seq, execution.EventContentChunk, map[string]any{"text": "new"})
	}
	if got := gaugeSum(t, metrics, "studio_sse_hub_cache_events"); got != 2 {
		t.Fatalf("studio_sse_hub_cache_events = %v for the new generation, want 2", got)
	}

	// The old generation reports late, with values nobody should believe.
	mgr.reportCache("run-generation", old, 99, 999999)

	if got := gaugeSum(t, metrics, "studio_sse_hub_cache_events"); got != 2 {
		t.Fatalf("studio_sse_hub_cache_events = %v, want 2: a superseded hub overwrote the live "+
			"generation's cache gauge", got)
	}
	if got := gaugeSum(t, metrics, "studio_sse_hub_cache_bytes"); got > 99999 {
		t.Fatalf("studio_sse_hub_cache_bytes = %v, want the LIVE hub's value", got)
	}
	if got := mgr.HubCount(); got != 1 {
		t.Fatalf("HubCount = %d, want 1", got)
	}
}

// TestGetOrCreateSweepsAnUnusableHubInPlace pins the registry half of the
// Batch 4.1 lock-order refactor.
//
// `Serving()` takes hub.mu, and GetOrCreate must call it with manager.mu
// RELEASED — which is only safe because the lookup, the replacement and the
// sweep are re-done in a loop rather than held together. This test reproduces
// the window failUpstream leaves open (the hub is marked unusable under hub.mu,
// the map has not been swept yet) and pins the observable contract: a fresh
// generation takes the run's slot, the run has exactly one hub, and the
// replaced generation's cache statistics do not survive it.
func TestGetOrCreateSweepsAnUnusableHubInPlace(t *testing.T) {
	metrics := telemetry.NewMetrics("test")
	mgr := NewHubManagerWithUpstream(context.Background(), newFakeUpstream(), metrics, HubOptions{})
	t.Cleanup(mgr.Close)

	hub := mgr.GetOrCreate("run-nonserving")
	hub.RememberDurable(1, execution.EventContentChunk, map[string]any{"text": "x"})

	// The in-between state, set the way the hub sets it itself.
	hub.mu.Lock()
	hub.closed = true
	hub.unhealthy = true
	hub.mu.Unlock()

	replacement := mgr.GetOrCreate("run-nonserving")
	if replacement == hub {
		t.Fatal("GetOrCreate handed back a hub that cannot serve live events")
	}
	if got := mgr.HubCount(); got != 1 {
		t.Fatalf("HubCount = %d after replacing a dead hub, want 1", got)
	}
	if got := replacement.CacheLen(); got != 0 {
		t.Fatalf("the replacement hub started with %d cached events, want 0", got)
	}
	if got := gaugeSum(t, metrics, "studio_sse_hub_cache_events"); got != 0 {
		t.Fatalf("studio_sse_hub_cache_events = %v: the replaced generation's cache statistics "+
			"survived the sweep", got)
	}
}
