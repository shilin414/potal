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
