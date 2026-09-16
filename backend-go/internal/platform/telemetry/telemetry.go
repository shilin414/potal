// Package telemetry wires OpenTelemetry tracing and Prometheus metrics.
//
// Both are first-class from day one (observability is not a tail task):
// every HTTP request and every Run execution emits spans and metrics.
package telemetry

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/creation-agent-studio/backend-go/internal/platform/config"
)

// Metrics aggregates the core Prometheus instruments.
type Metrics struct {
	Registry *prometheus.Registry

	HTTPDuration  *prometheus.HistogramVec
	HTTPRequests  *prometheus.CounterVec
	SSEActive     prometheus.Gauge
	QueueDepth    *prometheus.GaugeVec
	RunDuration   *prometheus.HistogramVec
	RunTotal      *prometheus.CounterVec
	LeaseExpired  prometheus.Counter
	OutboxBacklog prometheus.Gauge
	ProviderCalls *prometheus.CounterVec
	Provider429   *prometheus.CounterVec
	Reconciles    prometheus.Counter
	DBLatency     *prometheus.HistogramVec
	RedisLatency  *prometheus.HistogramVec

	ScheduleTriggerDelay  *prometheus.HistogramVec
	ScheduleQueueDelay    *prometheus.HistogramVec
	ScheduleMisfireTotal  prometheus.Counter
	OverlapSkippedTotal   prometheus.Counter
	DeliveryDuration      *prometheus.HistogramVec
	DeliveryFailuresTotal *prometheus.CounterVec
	// DeliverySendsTotal{channel, idempotency}: external send attempts,
	// split by whether a stable idempotency key was attached. External
	// delivery is AT LEAST ONCE — this counter makes the duplicate window
	// observable instead of silent.
	DeliverySendsTotal *prometheus.CounterVec

	// Execution Correctness Closure (修复计划 §42-51).
	InvariantViolation       *prometheus.CounterVec // {type}
	ProviderLimiterDegraded  prometheus.Gauge
	ProviderInflight         *prometheus.GaugeVec
	ProviderInflightRejected *prometheus.CounterVec

	// Provider EFFECTIVE capacity (第九轮补丁 3.3-A §二十). One gauge, three
	// kinds, because the interesting question is not "how much capacity is
	// used" but "how much of it nobody controls":
	//
	//	effective    admission's real bound — distinct live slots UNION
	//	             non-settled sending/unknown/accepted submissions
	//	controlled   live ownership-scoped slots a worker holds right now
	//	uncontrolled non-settled sending/unknown/accepted submissions with NO
	//	             live slot
	//
	// Healthy operation is effective ≈ controlled and uncontrolled ≈ 0. A
	// SUSTAINED rise in uncontrolled is the leading indicator: the effective
	// bound stays correct, but real provider work is escaping worker control
	// (crash between accept and release, waiting_external build-up, an
	// acceptance-persistence failure, reconciliation backlog). Warn on
	// uncontrolled > 0, alert when it keeps climbing.
	ProviderCapacityDepth *prometheus.GaugeVec // {provider, kind}

	// ProviderCapacityRejectTotal is the reason-tagged rejection counter for
	// THE capacity decision (as opposed to ProviderInflightRejected, which
	// predates the effective bound and is kept for continuity).
	ProviderCapacityRejectTotal *prometheus.CounterVec // {provider, reason}

	// SSEStreamProtocolTotal counts accepted streams per negotiated protocol,
	// which is the ratio the 3.3 rollout watches while old and new frontends
	// coexist: it goes to 0 for protocol 1 only after the frontend rollout
	// completes.
	SSEStreamProtocolTotal *prometheus.CounterVec // {protocol}

	// Production alerting surface (剩余问题报告 P3): one decision counter for
	// provider admission, a reaper counter, a confirmed ownership-loss
	// counter, fair-dispatch accounting and delivery retries. These are the
	// series the on-call alerts are defined on:
	//
	//	orphan provider slot  > 0   (ProviderInflight vs. real executions)
	//	provider_slot_lost    > 0   (ProviderAdmission{result="provider_slot_lost"})
	//	ownership loss spikes       (RunOwnershipLostTotal)
	//	reaper spikes               (RunReaperTotal)
	//	capacity_rejected sustained (ProviderAdmission{result="capacity_rejected"})
	//
	// Since 第九轮补丁 3.3-A the inflight reading to compare against real
	// provider executions is ProviderCapacityDepth{kind="controlled"}
	// (ProviderInflight is kept as its historical alias), while the bound
	// admission actually enforces is ProviderCapacityDepth{kind="effective"}.
	// The gap between them is the uncontrolled kind.
	ProviderAdmission     *prometheus.CounterVec // {provider, result}
	RunReaperTotal        prometheus.Counter
	RunOwnershipLostTotal prometheus.Counter
	PriorityDispatchTotal *prometheus.CounterVec // {class}
	DeliveryRetryTotal    prometheus.Counter

	// 第九轮 P0/P1 surface.
	//
	//	RunIdempotencyReplayTotal   — a resend of an identical POST /v2/runs
	//	                              was answered from the original run
	//	RunIdempotencyConflictTotal — the same client_request_id arrived with
	//	                              a DIFFERENT payload (a client bug or a
	//	                              shared id); the client is told 409
	//	ProviderSubmissionUnknownTotal
	//	                            — ALERT ON > 0: a provider request may
	//	                              have been delivered and cannot be
	//	                              confirmed. This is the only series that
	//	                              reports a genuinely uncertain EXTERNAL
	//	                              side effect.
	//	ProviderSubmissionDedupTotal
	//	                            — a re-claim found an already-accepted
	//	                              submission and reconciled instead of
	//	                              submitting a second provider chat
	//	SSEReplayEventsTotal        — durable events fetched from the MySQL
	//	                              replay path (Batch 4 narrowed this from
	//	                              "every replayed frame": cache replays
	//	                              are counted by SSEHubCacheReplayTotal
	//	                              instead, so the two together show
	//	                              whether the Hub is actually removing
	//	                              database work rather than just moving it).
	//	                              Batch 4.1's live gap repair reads the
	//	                              canonical log here too — it IS a MySQL
	//	                              replay, just a triggered one, and
	//	                              SSELiveGapRepairTotal explains why.
	RunIdempotencyReplayTotal      prometheus.Counter
	RunIdempotencyConflictTotal    prometheus.Counter
	ProviderSubmissionUnknownTotal prometheus.Counter
	ProviderSubmissionDedupTotal   prometheus.Counter
	SSEReplayEventsTotal           prometheus.Counter

	// Batch 4 — SSE Hub. These are the series that prove the fan-out change
	// happened, and they are read as RATIOS, never in isolation:
	//
	//	SSEHubActive             — live hubs (= runs being watched here)
	//	SSEHubUpstreamsActive    — confirmed Redis subscriptions
	//	SSEHubSubscribersActive  — HTTP connections carried by hubs
	//
	// The healthy relationship is subscribers ≫ hubs ≈ upstreams. If
	// upstreams tracks CONNECTIONS instead of hubs, a fallback path has
	// restored per-connection Redis subscriptions and the batch is not doing
	// its job.
	//
	// SSEHubSubscriberDroppedTotal{reason} is the one to alert on:
	// slow_consumer means real clients are outrunning their own queue
	// (consider the limits), while hub_closed/upstream_closed means the hub
	// is ending streams it should not have to.
	//
	// No label here may carry a run/user/conversation id: hop cardinality per
	// HTTP connection is how an SSE metric takes down a Prometheus instance.
	SSEHubActive                 prometheus.Gauge
	SSEHubSubscribersActive      *prometheus.GaugeVec // {protocol}
	SSEHubUpstreamsActive        prometheus.Gauge
	SSEHubCreatedTotal           prometheus.Counter
	SSEHubCacheReplayTotal       prometheus.Counter
	SSEHubCacheMissTotal         prometheus.Counter
	SSEHubSubscriberDroppedTotal *prometheus.CounterVec // {reason}
	SSEHubUpstreamFailureTotal   *prometheus.CounterVec // {stage}
	SSEHubCacheEvents            prometheus.Gauge
	SSEHubCacheBytes             prometheus.Gauge

	// SSELiveGapRepairTotal{result} is Batch 4.1's own series, and it answers
	// a different question from SSEReplayEventsTotal: NOT "how many events
	// came from MySQL" but "how often did the live path have to stop and
	// repair an ordering hole". A steady non-zero `repaired` rate means Redis
	// publish order and DB sequence order are drifting apart often enough to
	// matter; `failed` means a connection was closed to avoid losing an
	// event, which is a page, not a slow burn.
	//
	// `result` is a closed two-value enumeration, for the usual reason.
	SSELiveGapRepairTotal *prometheus.CounterVec // {result}
}

// Live gap repair results (SSELiveGapRepairTotal label values).
const (
	// LiveGapRepaired: the hole was filled from MySQL and the connection
	// continued with a contiguous cursor.
	LiveGapRepaired = "repaired"
	// LiveGapFailed: the canonical log could not supply the hole, so the
	// connection was ENDED (fail closed) rather than skipping frames. The
	// client reconnects from its last contiguous cursor.
	LiveGapFailed = "failed"
)

// Provider admission results (ProviderAdmission label values).
const (
	AdmissionAdmitted         = "admitted"
	AdmissionCapacityRejected = "capacity_rejected"
	AdmissionLostOwnership    = "lost_ownership"
	AdmissionProviderSlotLost = "provider_slot_lost"
	// Execution-time kill switch (复审 P1-2): the gate blocked the run
	// before any provider interaction.
	AdmissionGateKilled = "gate_killed"
	AdmissionGatePaused = "gate_paused"
	// Execution gate returned an UNREADABLE verdict (第三轮 P2-E): the run
	// was deferred fail-closed instead of submitted. Any sustained rate
	// here is a gate implementation bug, not traffic.
	AdmissionGateUnknown = "gate_unknown"
)

// Provider capacity depth kinds (ProviderCapacityDepth label values).
const (
	// CapacityEffective is what admission enforces: the distinct union of
	// live slots and non-settled sending/unknown/accepted submissions.
	CapacityEffective = "effective"
	// CapacityControlled is the live ownership-scoped slot count.
	CapacityControlled = "controlled"
	// CapacityUncontrolled is provider work with no live slot owning it.
	CapacityUncontrolled = "uncontrolled"
)

// Provider capacity rejection reasons (ProviderCapacityRejectTotal label
// values).
const (
	// CapacityRejectEffectiveInflightLimit: another run may still hold a real
	// provider execution, so this run was requeued instead of admitted.
	CapacityRejectEffectiveInflightLimit = "effective_inflight_limit"
)

func NewMetrics(service string) *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	m := &Metrics{
		Registry: reg,
		HTTPDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "studio_http_request_duration_seconds",
			Help:    "HTTP request latency by route/method/status.",
			Buckets: prometheus.ExponentialBuckets(0.005, 2, 14),
		}, []string{"route", "method", "status"}),
		HTTPRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "studio_http_requests_total",
			Help: "HTTP requests processed.",
		}, []string{"route", "method", "status"}),
		SSEActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "studio_sse_active_connections",
			Help: "Currently open SSE streams.",
		}),
		QueueDepth: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "studio_run_queue_depth",
			Help: "Run queue depth by provider.",
		}, []string{"provider"}),
		RunDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "studio_run_execution_seconds",
			Help:    "End-to-end run execution time.",
			Buckets: prometheus.ExponentialBuckets(0.1, 2, 14),
		}, []string{"provider", "status"}),
		RunTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "studio_runs_total",
			Help: "Runs by provider and terminal status.",
		}, []string{"provider", "status"}),
		LeaseExpired: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "studio_run_lease_expired_total",
			Help: "Leases expired and recovered by the reaper.",
		}),
		OutboxBacklog: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "studio_outbox_backlog",
			Help: "Unpublished outbox events.",
		}),
		ProviderCalls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "studio_provider_calls_total",
			Help: "Provider API calls by kind and outcome.",
		}, []string{"provider", "kind", "outcome"}),
		Provider429: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "studio_provider_rate_limited_total",
			Help: "Provider 429 responses.",
		}, []string{"provider", "kind"}),
		Reconciles: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "studio_aily_reconciles_total",
			Help: "Final reconciliations via GET chat result.",
		}),
		DBLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "studio_db_query_seconds",
			Help:    "SQL query latency.",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 12),
		}, []string{"op"}),
		RedisLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "studio_redis_op_seconds",
			Help:    "Redis operation latency.",
			Buckets: prometheus.ExponentialBuckets(0.0005, 2, 12),
		}, []string{"op"}),
		ScheduleTriggerDelay: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "studio_schedule_trigger_delay_seconds",
			Help:    "Delay between scheduled time and occurrence creation.",
			Buckets: prometheus.ExponentialBuckets(0.1, 2, 14),
		}, []string{"result"}),
		ScheduleQueueDelay: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "studio_schedule_queue_delay_seconds",
			Help:    "Delay between scheduled time and run start.",
			Buckets: prometheus.ExponentialBuckets(0.1, 2, 14),
		}, []string{"provider"}),
		ScheduleMisfireTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "studio_schedule_misfire_total",
			Help: "Misfire decisions by policy.",
		}),
		OverlapSkippedTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "studio_schedule_overlap_skipped_total",
			Help: "Occurrences skipped by overlap policy.",
		}),
		DeliveryDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "studio_schedule_delivery_seconds",
			Help:    "Delivery send duration by channel and status.",
			Buckets: prometheus.ExponentialBuckets(0.05, 2, 12),
		}, []string{"channel", "status"}),
		DeliveryFailuresTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "studio_schedule_delivery_failures_total",
			Help: "Delivery failures by channel and error code.",
		}, []string{"channel", "code"}),
		DeliverySendsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "studio_schedule_delivery_send_total",
			Help: "External delivery send attempts by channel and idempotency key presence (at-least-once semantics).",
		}, []string{"channel", "idempotency"}),
		InvariantViolation: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "studio_execution_invariant_violation_total",
			Help: "Execution invariant violations detected by the checker (detect-only, never auto-repaired).",
		}, []string{"type"}),
		ProviderLimiterDegraded: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "studio_provider_limiter_degraded",
			Help: "1 while the provider rate limiter runs on its local (Redis-unreachable) fallback.",
		}),
		ProviderInflight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "studio_provider_inflight",
			Help: "Current provider executions by provider.",
		}, []string{"provider"}),
		ProviderInflightRejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "studio_provider_inflight_rejected_total",
			Help: "Runs not admitted because the provider concurrency limit was reached.",
		}, []string{"provider", "reason"}),
		ProviderAdmission: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "studio_provider_admission_total",
			Help: "Provider admission decisions: admitted, capacity_rejected, lost_ownership, provider_slot_lost.",
		}, []string{"provider", "result"}),
		ProviderCapacityDepth: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "studio_provider_capacity_depth",
			Help: "Provider concurrency capacity by kind: effective (the admission bound: distinct live slots UNION " +
				"non-settled sending/unknown/accepted submissions), controlled (live slots) and uncontrolled " +
				"(provider work with no live slot — alert when this keeps rising).",
		}, []string{"provider", "kind"}),
		ProviderCapacityRejectTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "studio_provider_capacity_reject_total",
			Help: "Runs not admitted because another run may still hold a real provider execution.",
		}, []string{"provider", "reason"}),
		SSEStreamProtocolTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "studio_sse_stream_protocol_total",
			Help: "Accepted SSE streams by negotiated stream_protocol (1 = durable frames only, 2 = transient deltas included).",
		}, []string{"protocol"}),
		RunReaperTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "studio_run_reaper_total",
			Help: "Expired run leases recovered (requeued or failed) by the reaper.",
		}),
		RunOwnershipLostTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "studio_run_ownership_lost_total",
			Help: "Fenced heartbeats that proved the caller no longer owns its run lease.",
		}),
		PriorityDispatchTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "studio_priority_dispatch_total",
			Help: "Run queue messages dispatched by priority class (weighted fair scheduling).",
		}, []string{"class"}),
		DeliveryRetryTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "studio_delivery_retry_total",
			Help: "Feishu delivery attempts requeued with backoff after a send failure.",
		}),
		RunIdempotencyReplayTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "studio_run_idempotency_replay_total",
			Help: "POST /v2/runs resends answered from the original run (same client_request_id, same payload).",
		}),
		RunIdempotencyConflictTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "studio_run_idempotency_conflict_total",
			Help: "POST /v2/runs rejected because a client_request_id was reused with a different payload.",
		}),
		ProviderSubmissionUnknownTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "studio_provider_submission_unknown_total",
			Help: "Provider submits whose outcome could not be confirmed; the run is parked in waiting_external " +
				"instead of being retried (alert on > 0: an external side effect may exist).",
		}),
		ProviderSubmissionDedupTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "studio_provider_submission_dedup_total",
			Help: "Re-claims that found an already-accepted provider submission and reconciled instead of resubmitting.",
		}),
		SSEReplayEventsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "studio_sse_replay_events_total",
			Help: "Durable run events fetched from the MySQL SSE replay path (cache replays are counted by " +
				"studio_sse_hub_cache_replay_total instead).",
		}),
		SSEHubActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "studio_sse_hubs_active",
			Help: "Process-local SSE run hubs (one per run being streamed by this instance).",
		}),
		SSEHubSubscribersActive: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "studio_sse_hub_subscribers_active",
			Help: "SSE connections carried by run hubs, by the protocol they negotiated (1 = durable only, " +
				"2 = transient deltas included).",
		}, []string{"protocol"}),
		SSEHubUpstreamsActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "studio_sse_hub_upstreams_active",
			Help: "Confirmed Redis pub/sub subscriptions held by run hubs. Healthy: ≈ hubs, ≪ connections.",
		}),
		SSEHubCreatedTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "studio_sse_hub_created_total",
			Help: "Run hubs created. A rate far above the run-creation rate means hubs are churning " +
				"(idle TTL too short, or upstreams failing).",
		}),
		SSEHubCacheReplayTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "studio_sse_hub_cache_replay_total",
			Help: "Durable events served from a hub's bounded cache instead of MySQL.",
		}),
		SSEHubCacheMissTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "studio_sse_hub_cache_miss_total",
			Help: "Replay requests the hub cache could not serve (empty, evicted, or a sequence gap): " +
				"these fall back to MySQL by design.",
		}),
		SSEHubSubscriberDroppedTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "studio_sse_hub_subscriber_dropped_total",
			Help: "Subscribers disconnected by the hub itself, by reason. slow_consumer = the connection " +
				"outran its queue (only that connection is dropped); hub_closed / upstream_closed = the hub " +
				"ended the stream. A client that simply disconnected is NOT counted here.",
		}, []string{"reason"}),
		SSEHubUpstreamFailureTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "studio_sse_hub_upstream_failure_total",
			Help: "Redis upstream failures by stage: subscribe (SUBSCRIBE not confirmed), receive " +
				"(subscription ended unexpectedly), channel_closed, decode (malformed event payload).",
		}, []string{"stage"}),
		SSEHubCacheEvents: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "studio_sse_hub_cache_events",
			Help: "Durable events held across all hub caches (bounded by SSE_HUB_CACHE_EVENTS per hub).",
		}),
		SSEHubCacheBytes: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "studio_sse_hub_cache_bytes",
			Help: "Approximate payload bytes held across all hub caches (bounded by SSE_HUB_CACHE_BYTES per hub).",
		}),
		SSELiveGapRepairTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "studio_sse_live_gap_repair_total",
			Help: "Durable live gaps repaired from the canonical event log, by result. repaired = the " +
				"missing range was read from MySQL and sent in order; failed = it could not be read, so " +
				"the connection ended instead of skipping frames. These events are also counted by " +
				"studio_sse_replay_events_total, because they ARE a MySQL replay.",
		}, []string{"result"}),
	}
	reg.MustRegister(
		m.HTTPDuration, m.HTTPRequests, m.SSEActive, m.QueueDepth,
		m.RunDuration, m.RunTotal, m.LeaseExpired, m.OutboxBacklog,
		m.ProviderCalls, m.Provider429, m.Reconciles, m.DBLatency, m.RedisLatency,
		m.ScheduleTriggerDelay, m.ScheduleQueueDelay, m.ScheduleMisfireTotal,
		m.OverlapSkippedTotal, m.DeliveryDuration, m.DeliveryFailuresTotal,
		m.DeliverySendsTotal,
		m.InvariantViolation, m.ProviderLimiterDegraded,
		m.ProviderInflight, m.ProviderInflightRejected,
		m.ProviderAdmission, m.RunReaperTotal, m.RunOwnershipLostTotal,
		m.ProviderCapacityDepth, m.ProviderCapacityRejectTotal,
		m.SSEStreamProtocolTotal,
		m.PriorityDispatchTotal, m.DeliveryRetryTotal,
		m.RunIdempotencyReplayTotal, m.RunIdempotencyConflictTotal,
		m.ProviderSubmissionUnknownTotal, m.ProviderSubmissionDedupTotal,
		m.SSEReplayEventsTotal,
		m.SSEHubActive, m.SSEHubSubscribersActive, m.SSEHubUpstreamsActive,
		m.SSEHubCreatedTotal, m.SSEHubCacheReplayTotal, m.SSEHubCacheMissTotal,
		m.SSEHubSubscriberDroppedTotal, m.SSEHubUpstreamFailureTotal,
		m.SSEHubCacheEvents, m.SSEHubCacheBytes,
		m.SSELiveGapRepairTotal,
	)
	return m
}

// Handler serves /metrics.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})
}

// InitTracer sets up the OTLP trace exporter; returns a no-op shutdown when
// no endpoint is configured.
func InitTracer(ctx context.Context, cfg config.OTelConfig) (func(context.Context) error, error) {
	res, err := sdkresource.New(ctx,
		sdkresource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(version()),
		),
	)
	if err != nil {
		return nil, err
	}
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	if cfg.Endpoint == "" {
		// No collector: install a tracer provider with a parent-based
		// sampler so span context (trace IDs) still propagates.
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithResource(res),
			sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.NeverSample())),
		)
		otel.SetTracerProvider(tp)
		return tp.Shutdown, nil
	}
	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(cfg.Endpoint),
		otlptracehttp.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("otlp exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(exporter, sdktrace.WithBatchTimeout(2*time.Second)),
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(cfg.SamplingRate)),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// Tracer is the shared tracer.
var Tracer = otel.Tracer("github.com/creation-agent-studio/backend-go")

// SpanFromContext starts a span with the studio tracer.
func SpanFromContext(ctx context.Context, name string) (context.Context, trace.Span) {
	return Tracer.Start(ctx, name)
}

func version() string {
	if v := os.Getenv("APP_VERSION"); v != "" {
		return v
	}
	return "dev"
}
