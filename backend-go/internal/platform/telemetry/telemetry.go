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
}

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
			Help:    "TiDB query latency.",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 12),
		}, []string{"op"}),
		RedisLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "studio_redis_op_seconds",
			Help:    "Redis operation latency.",
			Buckets: prometheus.ExponentialBuckets(0.0005, 2, 12),
		}, []string{"op"}),
	}
	reg.MustRegister(
		m.HTTPDuration, m.HTTPRequests, m.SSEActive, m.QueueDepth,
		m.RunDuration, m.RunTotal, m.LeaseExpired, m.OutboxBacklog,
		m.ProviderCalls, m.Provider429, m.Reconciles, m.DBLatency, m.RedisLatency,
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
