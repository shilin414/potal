// studio-worker — the execution plane. Consumes provider queues
// (Outbox → Redis Streams → CAS claim → lease → handler) and can be
// scaled per provider (--provider=feishu_aily).
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

	var wg sync.WaitGroup

	// Outbox relay: TiDB → Redis Streams (§22). Also routes delivery
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
		// Provider worker pool.
		worker := &execution.Worker{
			Svc:             a.Runs,
			RDB:             a.Redis,
			Provider:        *provider,
			WorkerID:        cfg.Runner.WorkerID,
			Group:           "workers",
			Handler:         a.AilyExecutor,
			Concurrency:     cfg.Runner.Concurrency,
			Lease:           cfg.Runner.LeaseSeconds,
			Heartbeat:       cfg.Runner.HeartbeatInterval,
			ScanEvery:       cfg.Runner.ReaperInterval,
			Log:             logger,
			ProviderSlots:   a.ProviderSlots,
			PriorityWeights: cfg.Runner.PriorityWeights,
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			logger.Info("worker consuming", "provider", *provider, "concurrency", cfg.Runner.Concurrency)
			worker.Run(runCtx)
		}()
	}

	// Provider limiter degraded metric (修复计划 §51): expose the Redis
	// GCRA fallback state so a Redis outage is visible on the dashboard.
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
				if a.AilyExecutor.ChatsL.Degraded() || a.AilyExecutor.PollsL.Degraded() || a.AilyExecutor.ArtifactsL.Degraded() {
					degraded = 1.0
				}
				a.Metrics.ProviderLimiterDegraded.Set(degraded)
				if depth, err := a.ProviderSlots.Depth(runCtx); err == nil {
					a.Metrics.ProviderInflight.WithLabelValues("feishu_aily").Set(float64(depth))
				}
			}
		}
	}()

	<-runCtx.Done()
	logger.Info("worker shutting down (in-flight runs keep their leases; the reaper recovers orphans)")
	wg.Wait()
}
