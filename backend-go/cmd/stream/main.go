// studio-stream — the SSE streaming plane. Same handler code as the API
// binary but deployed separately so thousands of long-lived SSE
// connections never compete with short REST requests (Nginx routes
// /api/v2/runs/*/stream here).
package main

import (
	"context"
	"errors"
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
	cfg, err := app.LoadConfig()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}
	logger := logging.New(cfg.LogLevel)
	slog.SetDefault(logger)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

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
		Addr:              cfg.StreamAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       0, // SSE lives long by design
	}

	go func() {
		logger.Info("studio-stream listening", "addr", cfg.StreamAddr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	logger.Info("shutting down stream plane")
	graceCtx, graceCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer graceCancel()
	_ = httpServer.Shutdown(graceCtx)
}
