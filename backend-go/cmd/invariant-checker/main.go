// studio-invariant-checker — the execution correctness watchdog
// (修复计划 §42). Periodically scans the canonical state and reports
// invariant violations via logs + Prometheus metrics (detect / metric /
// log / alert — never auto-repair; the reaper is the only recovery
// coordinator).
//
// Flags mirror the other runners; the HTTP listener (default :9091)
// serves /metrics for scraping.
package main

import (
	"context"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/app"
	"github.com/creation-agent-studio/backend-go/internal/execution/invariant"
	"github.com/creation-agent-studio/backend-go/internal/platform/logging"
)

func main() {
	interval := flag.Duration("interval", 30*time.Second, "check interval")
	addr := flag.String("addr", ":9091", "metrics listen address")
	once := flag.Bool("once", false, "run a single check round and exit (non-zero on violations)")
	flag.Parse()

	cfg, err := app.LoadConfig()
	if err != nil {
		logger := logging.New("info")
		logger.Error("config", "err", err)
		os.Exit(1)
	}
	logger := logging.New(cfg.LogLevel)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	a, err := app.Build(ctx, cfg)
	cancel()
	if err != nil {
		logger.Error("build app", "err", err)
		os.Exit(1)
	}
	defer a.Close()

	checker := &invariant.Checker{
		DB:      a.DB,
		Log:     a.Log,
		Metrics: a.Metrics,
	}

	runCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *once {
		res := checker.RunOnce(runCtx)
		if res.TotalViolations > 0 {
			logger.Error("invariant check FAILED", "violations", res.TotalViolations)
			os.Exit(1)
		}
		logger.Info("invariant check passed")
		return
	}

	// Metrics endpoint (scraper target).
	mux := http.NewServeMux()
	mux.Handle("/metrics", a.Metrics.Handler())
	srv := &http.Server{Addr: *addr, Handler: mux}
	go func() {
		logger.Info("invariant checker metrics listening", "addr", *addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("metrics server", "err", err)
		}
	}()

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	logger.Info("invariant checker started", "interval", interval.String())
	for {
		select {
		case <-runCtx.Done():
			shutdownCtx, c2 := context.WithTimeout(context.Background(), 5*time.Second)
			defer c2()
			_ = srv.Shutdown(shutdownCtx)
			return
		case <-ticker.C:
			res := checker.RunOnce(runCtx)
			if res.TotalViolations > 0 {
				logger.Error("invariant round found violations", "count", res.TotalViolations)
			}
		}
	}
}
