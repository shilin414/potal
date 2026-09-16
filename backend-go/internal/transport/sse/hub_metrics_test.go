package sse

import (
	"context"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"

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
