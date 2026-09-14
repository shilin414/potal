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
	ProviderAdmission     *prometheus.CounterVec // {provider, result}
	RunReaperTotal        prometheus.Counter
	RunOwnershipLostTotal prometheus.Counter
	PriorityDispatchTotal *prometheus.CounterVec // {class}
	DeliveryRetryTotal    prometheus.Counter
}

// Provider admission results (ProviderAdmission label values).
const (
	AdmissionAdmitted         = "admitted"
	AdmissionCapacityRejected = "capacity_rejected"
	AdmissionLostOwnership    = "lost_ownership"
	AdmissionProviderSlotLost = "provider_slot_lost"
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
		m.PriorityDispatchTotal, m.DeliveryRetryTotal,
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
