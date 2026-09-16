// studio-worker — the execution plane. Consumes provider queues
// (Outbox → Redis Streams → CAS claim → lease → handler) and can be
// scaled per provider (--provider=feishu_aily).
//
// Batch 5: the concrete executor is NO LONGER bound here. The execution
// branch resolves its --provider through the worker dispatch registry and
// runs the returned plan; an unregistered provider fails the process
// before any consumer exists. feishu_delivery stays a separate role — it
// delivers scheduled results, it is not a Run runtime.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/app"
	"github.com/creation-agent-studio/backend-go/internal/delivery"
	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/platform/logging"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
	"github.com/creation-agent-studio/backend-go/internal/workerdispatch"
)

func main() {
	provider := flag.String("provider", "feishu_aily", "provider queue to consume (feishu_aily | feishu_delivery)")
	once := flag.Bool("migrate", false, "run migrations before starting")
	flag.Parse()

	cfg, err := app.LoadConfig()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}
	logger := logging.New(cfg.LogLevel)
	slog.SetDefault(logger)
	logger = logger.With(logging.KeyWorkerID, cfg.Runner.WorkerID)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if *once {
		if err := app.MigrateUp(ctx, cfg.Database.DSN(), "db/migrations"); err != nil {
			logger.Error("migrate failed", "err", err)
			os.Exit(1)
		}
	}

	a, err := app.Build(ctx, cfg)
	if err != nil {
		logger.Error("build app", "err", err)
		os.Exit(1)
	}
	defer a.Close()
	a.Log = logger

	shutdownTracer, err := telemetry.InitTracer(ctx, cfg.OTel)
	if err != nil {
		logger.Warn("tracer init failed", "err", err)
	} else {
		defer func() { _ = shutdownTracer(context.Background()) }()
	}

	runCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Execution provider plan (Batch 5 §24): resolved BEFORE any consumer
	// goroutine starts. An unregistered provider is a startup failure with
	// exit code 2 — it must never create a consumer group, XREADGROUP,
	// claim a Run or call a provider (§26).
	var plan *workerdispatch.Plan
	if *provider != delivery.ProviderKey {
		plan, err = a.WorkerDispatch.ResolveProvider(*provider)
		if err != nil {
			logger.Error("worker provider is not registered",
				"provider", *provider, "err", err)
			os.Exit(2)
		}
		logger.Info("worker provider registered",
			"provider", plan.Provider,
			"runtime_types", plan.RuntimeTypes,
			"concurrency", cfg.Runner.Concurrency)
	}

	var wg sync.WaitGroup

	// Outbox relay: MySQL → Redis Streams (§22). Also routes delivery
	// outbox events to the feishu_delivery stream.
	relay := execution.NewRelay(a.Runs, a.Redis, 200)
	wg.Add(1)
	go func() {
		defer wg.Done()
		relay.Run(runCtx, cfg.Runner.RelayInterval)
	}()

	if *provider == delivery.ProviderKey {
		// Delivery consumer pool: scheduled-run results → Feishu IM.
		worker := delivery.NewWorker(a.DB, a.Redis, cfg.Runner.WorkerID,
			a.DeliverySender, a.DeliveryLimiter, logger, a.Metrics)
		wg.Add(1)
		go func() {
			defer wg.Done()
			logger.Info("delivery worker consuming", "queue", delivery.ProviderKey)
			worker.Run(runCtx)
		}()
	} else {
		// Provider worker pool (Batch 5 §25): the plan owns the Handler and
		// the provider-wide slots — this binary never names a concrete
		// executor anymore.
		worker := &execution.Worker{
			Svc:             a.Runs,
			RDB:             a.Redis,
			Provider:        plan.Provider,
			WorkerID:        cfg.Runner.WorkerID,
			Group:           "workers",
			Handler:         plan.Handler,
			Concurrency:     cfg.Runner.Concurrency,
			Lease:           cfg.Runner.LeaseSeconds,
			Heartbeat:       cfg.Runner.HeartbeatInterval,
			ScanEvery:       cfg.Runner.ReaperInterval,
			Log:             logger,
			ProviderSlots:   plan.Slots,
			Gate:            app.NewExecutionGate(a.Catalog),
			PriorityWeights: cfg.Runner.PriorityWeights,
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			logger.Info("worker consuming", "provider", plan.Provider, "concurrency", cfg.Runner.Concurrency)
			worker.Run(runCtx)
		}()
	}

	// Provider execution health monitor (Batch 5 §28/§29): only an
	// execution worker has a provider plan, so only it samples provider
	// capacity and limiter health — the delivery worker no longer samples
	// Aily state it does not use.
	if plan != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-runCtx.Done():
					return
				case <-ticker.C:
					degraded := 0.0
					if plan.Health != nil && plan.Health.LimiterDegraded() {
						degraded = 1.0
					}
					a.Metrics.ProviderLimiterDegraded.Set(degraded)
					// Provider capacity, all three faces (第九轮补丁 3.3-A §二十):
					// effective is the bound admission enforces; controlled is what
					// a live worker owns; uncontrolled is real provider work nobody
					// owns. effective ≈ controlled and uncontrolled ≈ 0 is healthy —
					// a sustained uncontrolled rise is the alert.
					if plan.Slots != nil {
						if depth, err := plan.Slots.Depth(runCtx); err == nil {
							a.Metrics.ProviderInflight.WithLabelValues(plan.Provider).Set(float64(depth))
							a.Metrics.ProviderCapacityDepth.
								WithLabelValues(plan.Provider, telemetry.CapacityEffective).Set(float64(depth))
						}
						if depth, err := plan.Slots.ControlledDepth(runCtx); err == nil {
							a.Metrics.ProviderCapacityDepth.
								WithLabelValues(plan.Provider, telemetry.CapacityControlled).Set(float64(depth))
						}
						if depth, err := plan.Slots.UncontrolledDepth(runCtx); err == nil {
							a.Metrics.ProviderCapacityDepth.
								WithLabelValues(plan.Provider, telemetry.CapacityUncontrolled).Set(float64(depth))
							if depth > 0 {
								logger.Warn("provider capacity is uncontrolled: real provider work has no live slot",
									"provider", plan.Provider, "uncontrolled_depth", depth)
							}
						}
					}
				}
			}
		}()
	}

	<-runCtx.Done()
	logger.Info("worker shutting down (in-flight runs keep their leases; the reaper recovers orphans)")
	wg.Wait()
}
