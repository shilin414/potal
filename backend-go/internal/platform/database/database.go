// Package database opens and tunes the MySQL connection pool.
package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/creation-agent-studio/backend-go/internal/platform/config"
)

// Open connects to MySQL 5.7 with conservative pool defaults.
func Open(ctx context.Context, cfg config.DatabaseConfig) (*sql.DB, error) {
	db, err := sql.Open("mysql", cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping database %s:%d/%s: %w", cfg.Host, cfg.Port, cfg.Name, err)
	}
	return db, nil
}

// WithTimeout runs fn under the standard per-query timeout.
func WithTimeout(ctx context.Context, cfg config.DatabaseConfig, fn func(ctx context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, cfg.QueryTimeout)
	defer cancel()
	return fn(ctx)
}
