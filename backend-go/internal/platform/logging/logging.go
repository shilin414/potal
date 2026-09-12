// Package logging configures structured slog JSON logging.
package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

// Keys used across the codebase for correlation.
const (
	KeyTraceID   = "trace_id"
	KeyRunID     = "run_id"
	KeyRequestID = "request_id"
	KeyWorkerID  = "worker_id"
	KeyUserID    = "user_id"
)

// New builds the root JSON logger. level is debug|info|warn|error.
func New(level string) *slog.Logger {
	lvl := slog.LevelInfo
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: lvl,
	})
	return slog.New(handler)
}

// With returns a child logger carrying the given correlation keys.
func With(logger *slog.Logger, args ...any) *slog.Logger {
	return logger.With(args...)
}

type ctxKey string

const loggerKey ctxKey = "studio.logger"

// IntoContext stores a request-scoped logger in the context.
func IntoContext(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, logger)
}

// FromContext returns the request-scoped logger or the default one.
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}
