// studio-migrate — migration-only entrypoint.
//
// `studio-api -migrate` migrates and then CONTINUES into app.Build and the
// HTTP server (useful for container entrypoints), so it can never be used
// as a CI "apply migrations" step: without the runtime secrets present the
// build fails, and with them present the step would block on the server.
// This binary does exactly one thing — apply migrations and exit.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/app"
	"github.com/creation-agent-studio/backend-go/internal/platform/logging"
)

func main() {
	dir := flag.String("dir", "db/migrations", "path to the migration directory")
	flag.Parse()

	cfg, err := app.LoadConfig()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}
	logger := logging.New(cfg.LogLevel)
	slog.SetDefault(logger)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := app.MigrateUp(ctx, cfg.Database.DSN(), *dir); err != nil {
		logger.Error("migrate failed", "err", err, "dir", *dir)
		os.Exit(1)
	}
	logger.Info("migrations applied", "dir", *dir)
}
