package telemetry

import (
	"testing"

	dto "github.com/prometheus/client_model/go"
)

// gatherNames returns every metric family currently exported by the registry.
func gatherNames(t *testing.T, m *Metrics) map[string]*dto.MetricFamily {
	t.Helper()
	families, err := m.Registry.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	out := make(map[string]*dto.MetricFamily, len(families))
	for _, f := range families {
		out[f.GetName()] = f
	}
	return out
}

// TestProductionAlertingMetricsAreExported pins the P3 surface of the
// remaining-issues report: the alert rules are defined on these series, so a
// silent rename/removal must fail here instead of in production.

func TestSessionRevokeFailureMetricIsExported(t *testing.T) {
	m := NewMetrics("test")
	m.SessionRevokeFailuresTotal.Add(0)
	if gatherNames(t, m)["studio_session_revoke_failures_total"] == nil {
		t.Fatal("studio_session_revoke_failures_total is not exported")
	}
}

func TestProductionAlertingMetricsAreExported(t *testing.T) {
	m := NewMetrics("test")

	// Initialize every labeled series (vectors only export used labels) and
	// keep the unlabeled counters alive.
	m.ProviderInflight.WithLabelValues("feishu_aily").Set(0)
	m.ProviderInflightRejected.WithLabelValues("feishu_aily", "provider_inflight_limit").Add(0)
	m.ProviderAdmission.WithLabelValues("feishu_aily", AdmissionAdmitted).Add(0)
	m.ProviderAdmission.WithLabelValues("feishu_aily", AdmissionCapacityRejected).Add(0)
	m.ProviderAdmission.WithLabelValues("feishu_aily", AdmissionLostOwnership).Add(0)
	m.ProviderAdmission.WithLabelValues("feishu_aily", AdmissionProviderSlotLost).Add(0)
	m.RunReaperTotal.Add(0)
	m.RunOwnershipLostTotal.Add(0)
	m.PriorityDispatchTotal.WithLabelValues("interactive").Add(0)
	m.DeliveryRetryTotal.Add(0)
	m.InvariantViolation.WithLabelValues("A").Add(0)
	m.ProviderLimiterDegraded.Set(0)

	got := gatherNames(t, m)
	for _, name := range []string{
		"studio_provider_inflight",
		"studio_provider_admission_total",
		"studio_run_reaper_total",
		"studio_run_ownership_lost_total",
		"studio_priority_dispatch_total",
		"studio_delivery_retry_total",
		"studio_execution_invariant_violation_total",
		"studio_provider_limiter_degraded",
	} {
		if _, ok := got[name]; !ok {
			t.Errorf("metric %s is not exported (P3 alerting surface)", name)
		}
	}

	// Label contracts the alerts are written against.
	admission := got["studio_provider_admission_total"]
	if admission == nil {
		t.Fatal("studio_provider_admission_total missing")
	}
	labels := map[string]bool{}
	for _, metric := range admission.GetMetric() {
		for _, lp := range metric.GetLabel() {
			labels[lp.GetName()] = true
		}
	}
	for _, want := range []string{"provider", "result"} {
		if !labels[want] {
			t.Errorf("studio_provider_admission_total is missing label %q", want)
		}
	}
	if _, ok := got["studio_priority_dispatch_total"]; ok {
		for _, metric := range got["studio_priority_dispatch_total"].GetMetric() {
			if len(metric.GetLabel()) != 1 || metric.GetLabel()[0].GetName() != "class" {
				t.Fatalf("studio_priority_dispatch_total labels = %v, want [class]", metric.GetLabel())
			}
		}
	}
}

// TestProviderCapacityMetricsAreExported pins the 3.3-A observability surface
// (第九轮补丁 §二十): the three capacity faces are what the rollout watches,
// and the alert is defined on `kind="uncontrolled"` — so a rename or a dropped
// label must fail here rather than silently un-arm the alert.
func TestProviderCapacityMetricsAreExported(t *testing.T) {
	m := NewMetrics("test")

	for _, kind := range []string{CapacityEffective, CapacityControlled, CapacityUncontrolled} {
		m.ProviderCapacityDepth.WithLabelValues("feishu_aily", kind).Set(0)
	}
	m.ProviderCapacityRejectTotal.
		WithLabelValues("feishu_aily", CapacityRejectEffectiveInflightLimit).Add(0)
	m.SSEStreamProtocolTotal.WithLabelValues("1").Add(0)
	m.SSEStreamProtocolTotal.WithLabelValues("2").Add(0)

	got := gatherNames(t, m)
	for _, name := range []string{
		"studio_provider_capacity_depth",
		"studio_provider_capacity_reject_total",
		"studio_sse_stream_protocol_total",
	} {
		if _, ok := got[name]; !ok {
			t.Errorf("metric %s is not exported (3.3 capacity surface)", name)
		}
	}

	// Every capacity face must be individually addressable, otherwise the
	// "uncontrolled keeps rising" alert cannot be written.
	kinds := map[string]bool{}
	for _, metric := range got["studio_provider_capacity_depth"].GetMetric() {
		for _, lp := range metric.GetLabel() {
			if lp.GetName() == "kind" {
				kinds[lp.GetValue()] = true
			}
		}
	}
	for _, want := range []string{CapacityEffective, CapacityControlled, CapacityUncontrolled} {
		if !kinds[want] {
			t.Errorf("studio_provider_capacity_depth has no kind=%q series", want)
		}
	}

	// The three capacity kinds must be DIFFERENT label values — collapsing
	// them would make the gauge read as a single number again, which is the
	// defect 3.3-A removed from the underlying count.
	depth := got["studio_provider_capacity_depth"]
	for _, metric := range depth.GetMetric() {
		var kind string
		for _, lp := range metric.GetLabel() {
			if lp.GetName() == "kind" {
				kind = lp.GetValue()
			}
		}
		if kind == CapacityEffective {
			if metric.GetGauge().GetValue() != 0 {
				t.Fatalf("effective gauge = %v, want the pinned 0", metric.GetGauge().GetValue())
			}
		}
	}
}

// TestSSEStreamProtocolMetricLabelShape pins the telemetry half of the 3.3.1-B
// invariant: `studio_sse_stream_protocol_total` is labeled by the NEGOTIATED
// protocol only, and by nothing else.
//
// The cardinality property itself (that the negotiation can never produce more
// than the two values this server implements) is asserted in the sse package,
// where the negotiation and the label write are visible together
// (TestSSEStreamProtocolMetricCardinalityIsBounded). What can only be checked
// here is the SHAPE: a second label — a run id, a user, a raw requested version
// kept "for debugging" — would multiply the series per client, and the fix in
// the parser would not save it.
func TestSSEStreamProtocolMetricLabelShape(t *testing.T) {
	m := NewMetrics("test")
	m.SSEStreamProtocolTotal.WithLabelValues("1").Add(0)
	m.SSEStreamProtocolTotal.WithLabelValues("2").Add(0)

	got := gatherNames(t, m)["studio_sse_stream_protocol_total"]
	if got == nil {
		t.Fatal("studio_sse_stream_protocol_total is not exported")
	}
	if len(got.GetMetric()) != 2 {
		t.Fatalf("studio_sse_stream_protocol_total series = %d, want 2 (protocol 1 and 2)",
			len(got.GetMetric()))
	}
	for _, metric := range got.GetMetric() {
		lps := metric.GetLabel()
		if len(lps) != 1 || lps[0].GetName() != "protocol" {
			t.Fatalf("studio_sse_stream_protocol_total labels = %v, want exactly [protocol] — "+
				"every extra label multiplies the cardinality of a client-controlled value", lps)
		}
		v := lps[0].GetValue()
		if v != "1" && v != "2" {
			t.Fatalf("studio_sse_stream_protocol_total has protocol=%q, want only \"1\" or \"2\"", v)
		}
	}
}

// TestWorkerDispatchMetricLabelShape pins the Batch 5 dispatch metric's
// shape: exactly [provider, runtime_type, result], and `result` is the
// CLOSED four-value routing enum — never a run id, a user id, a worker id
// or an ad-hoc status string. This series answers "how are claims being
// routed"; letting an unbounded label in would turn a routing health
// series into a cardinality incident.
func TestWorkerDispatchMetricLabelShape(t *testing.T) {
	m := NewMetrics("test")
	for _, result := range []string{
		"routed", "provider_mismatch", "snapshot_mismatch", "route_missing",
	} {
		m.WorkerDispatchTotal.
			WithLabelValues("feishu_aily", "agent", result).Add(0)
	}

	got := gatherNames(t, m)["studio_worker_dispatch_total"]
	if got == nil {
		t.Fatal("studio_worker_dispatch_total is not exported")
	}
	if len(got.GetMetric()) != 4 {
		t.Fatalf("studio_worker_dispatch_total series = %d, want 4 (one per closed result value)",
			len(got.GetMetric()))
	}
	results := map[string]bool{}
	for _, metric := range got.GetMetric() {
		lps := metric.GetLabel()
		if len(lps) != 3 {
			t.Fatalf("studio_worker_dispatch_total labels = %v, want exactly [provider, runtime_type, result]", lps)
		}
		// Prometheus sorts label names alphabetically — assert the NAME SET,
		// not the positions.
		names := map[string]bool{}
		for _, lp := range lps {
			names[lp.GetName()] = true
		}
		for _, want := range []string{"provider", "runtime_type", "result"} {
			if !names[want] {
				t.Fatalf("studio_worker_dispatch_total labels = %v, want [provider, runtime_type, result]", lps)
			}
		}
		if len(names) != 3 {
			t.Fatalf("studio_worker_dispatch_total labels = %v, want exactly the three names", lps)
		}
		for _, lp := range lps {
			if lp.GetName() == "result" {
				results[lp.GetValue()] = true
			}
		}
	}
	for _, want := range []string{
		"routed", "provider_mismatch", "snapshot_mismatch", "route_missing",
	} {
		if !results[want] {
			t.Errorf("studio_worker_dispatch_total has no result=%q series", want)
		}
	}
}

// TestSSEHubMetricsAreExported pins the Batch 4 (SSE Hub) observability surface.
//
// These are the series the canary reads, and the only ones that can prove the
// fan-out change actually happened: the healthy relationship is
// subscribers ≫ hubs ≈ upstreams, and `studio_sse_hub_upstreams_active` is the
// number that must NOT track connections.
//
// The VALUE enumerations for the labeled counters are asserted in the sse
// package (TestHubMetricLabelsStayBounded), where the drop and failure paths
// that write them are visible; the label SHAPES are pinned here, because a
// second label on a per-connection metric is what turns an SSE rollout into a
// Prometheus incident.
func TestSSEHubMetricsAreExported(t *testing.T) {
	m := NewMetrics("test")
	m.SSEHubSubscribersActive.WithLabelValues("1").Set(0)
	m.SSEHubSubscribersActive.WithLabelValues("2").Set(0)
	m.SSEHubSubscriberDroppedTotal.WithLabelValues("slow_consumer").Add(0)
	m.SSEHubUpstreamFailureTotal.WithLabelValues("subscribe").Add(0)
	m.SSEHubActive.Set(0)
	m.SSEHubUpstreamsActive.Set(0)
	m.SSEHubCacheEvents.Set(0)
	m.SSEHubCacheBytes.Set(0)
	m.SSEHubCreatedTotal.Add(0)
	m.SSEHubCacheReplayTotal.Add(0)
	m.SSEHubCacheMissTotal.Add(0)

	got := gatherNames(t, m)
	for _, name := range []string{
		"studio_sse_hubs_active",
		"studio_sse_hub_subscribers_active",
		"studio_sse_hub_upstreams_active",
		"studio_sse_hub_created_total",
		"studio_sse_hub_cache_replay_total",
		"studio_sse_hub_cache_miss_total",
		"studio_sse_hub_subscriber_dropped_total",
		"studio_sse_hub_upstream_failure_total",
		"studio_sse_hub_cache_events",
		"studio_sse_hub_cache_bytes",
	} {
		if _, ok := got[name]; !ok {
			t.Errorf("metric %s is not exported (Batch 4 SSE Hub surface)", name)
		}
	}

	// One label each, and it must be the documented one — never a run id, a
	// user id or a conversation id.
	for name, label := range map[string]string{
		"studio_sse_hub_subscribers_active":       "protocol",
		"studio_sse_hub_subscriber_dropped_total": "reason",
		"studio_sse_hub_upstream_failure_total":   "stage",
	} {
		family := got[name]
		if family == nil {
			t.Fatalf("metric %s is not exported", name)
		}
		for _, metric := range family.GetMetric() {
			lps := metric.GetLabel()
			if len(lps) != 1 || lps[0].GetName() != label {
				t.Fatalf("%s labels = %v, want exactly [%s]", name, lps, label)
			}
		}
	}

	// The protocol label on the subscriber gauge is the same bounded value the
	// negotiation produces.
	for _, metric := range got["studio_sse_hub_subscribers_active"].GetMetric() {
		if v := metric.GetLabel()[0].GetValue(); v != "1" && v != "2" {
			t.Fatalf("studio_sse_hub_subscribers_active has protocol=%q, want only \"1\" or \"2\"", v)
		}
	}
}
