package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/golang-migrate/migrate/v4"
	mysqldrv "github.com/golang-migrate/migrate/v4/database/mysql"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

// MigrateUp applies all pending migrations with clear operator errors
// (e.g. the database itself does not exist yet).
//
// MySQL 5.7 is the only supported database baseline, so the DSN is used
// exactly as configured: no server probing, no per-database compatibility
// flags. Schema evolution is driven by db/migrations, which is the single
// authoritative schema source.
func MigrateUp(ctx context.Context, dsn, migrationsDir string) error {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("cannot reach mysql database (does it exist? see README bootstrap): %w", err)
	}
	driver, err := mysqldrv.WithInstance(db, &mysqldrv.Config{})
	if err != nil {
		return err
	}
	m, err := migrate.NewWithDatabaseInstance("file://"+migrationsDir, "mysql", driver)
	if err != nil {
		return err
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	slog.Info("migrations applied")
	return nil
}
