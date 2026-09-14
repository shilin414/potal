package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	mysqldrv "github.com/golang-migrate/migrate/v4/database/mysql"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

// MigrateUp applies all pending migrations with clear operator errors
// (e.g. the database itself does not exist yet).
//
// TiDB compatibility: golang-migrate's mysql driver records the schema
// version inside a SERIALIZABLE transaction, and TiDB refuses that
// isolation level by default:
//
//	Error 8048 (HY000): The isolation level 'SERIALIZABLE' is not
//	supported. Set tidb_skip_isolation_level_check=1 to skip this error
//
// A vanilla TiDB (fresh container, production default) therefore fails
// on the FIRST migration, so the migration connection carries the skip
// flag when the server is TiDB. MySQL is left untouched — the variable
// only exists on TiDB.
func MigrateUp(ctx context.Context, dsn, migrationsDir string) error {
	dsn = tidbCompatibleDSN(ctx, dsn)

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("cannot reach database (does it exist? see README bootstrap): %w", err)
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

// tidbCompatibleDSN returns a DSN that can run migrations against TiDB.
// Detection is done on the server itself (SELECT VERSION()), and the flag
// is passed as a connection parameter so EVERY pooled connection (the
// migration driver uses its own) starts with it set.
func tidbCompatibleDSN(ctx context.Context, dsn string) string {
	probe, err := sql.Open("mysql", dsn)
	if err != nil {
		return dsn
	}
	defer probe.Close()
	var version string
	if err := probe.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil {
		return dsn // let the caller surface the connection error
	}
	if !strings.Contains(version, "TiDB") {
		return dsn
	}
	slog.Info("tidb detected: allowing the migration driver's isolation level",
		"version", version, "param", "tidb_skip_isolation_level_check=1")
	return appendDSNParam(dsn, "tidb_skip_isolation_level_check=1")
}

// appendDSNParam adds a go-sql-driver parameter (unknown keys become
// session system variables) without breaking the existing query string.
func appendDSNParam(dsn, param string) string {
	if strings.Contains(dsn, "?") {
		return dsn + "&" + param
	}
	return dsn + "?" + param
}
