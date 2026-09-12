// studio-api — the short-request control plane (REST).
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/app"
	"github.com/creation-agent-studio/backend-go/internal/platform/logging"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
)

func main() {
	migrate := flag.Bool("migrate", false, "run migrations before starting")
	flag.Parse()

	cfg, err := app.LoadConfig()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}
	logger := logging.New(cfg.LogLevel)
	slog.SetDefault(logger)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if *migrate {
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

	handler := app.NewServer(a.Cfg, a).Router()

	httpServer := &http.Server{
		Addr:              cfg.APIAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// No write timeout: SSE artifacts stream; the stream plane has its own limits.
		IdleTimeout: 120 * time.Second,
	}

	go func() {
		logger.Info("studio-api listening", "addr", cfg.APIAddr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	logger.Info("shutting down")
	graceCtx, graceCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer graceCancel()
	_ = httpServer.Shutdown(graceCtx)
}
