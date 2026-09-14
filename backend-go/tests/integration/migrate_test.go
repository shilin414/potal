package integration

import (
	"context"
	"os"
	"testing"

	"github.com/creation-agent-studio/backend-go/internal/app"
	"github.com/creation-agent-studio/backend-go/internal/platform/config"
)

// TestMigrateUpAgainstRealDatabase is the migration-path regression test
// against the real database.
//
// It proves the two properties the switch depends on:
//
//  1. MigrateUp applies the full db/migrations set on MySQL 5.7 with no
//     engine-specific DSN workaround. An earlier revision probed the server
//     version and injected an isolation-level skip flag for a database that
//     refused SERIALIZABLE; that probe is gone, because the schema-version
//     write golang-migrate performs is plain MySQL. If a workaround were
//     still needed, this test would be the one to fail.
//  2. Running it again is a no-op (ErrNoChange is absorbed) and leaves
//     schema_migrations clean: a dirty flag or a duplicate-table error here
//     would mean the migration set is not idempotent.
func TestMigrateUpAgainstRealDatabase(t *testing.T) {
	if os.Getenv("STUDIO_TEST_DB") != "1" {
		t.Skip("set STUDIO_TEST_DB=1 to run the migration path against a real database")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	ctx := context.Background()
	// Relative path with forward slashes: golang-migrate's file source
	// parses an absolute Windows path ("C:\...") as a URL host and fails,
	// and the binaries use the same relative form.
	if err := app.MigrateUp(ctx, cfg.Database.DSN(), "../../db/migrations"); err != nil {
		t.Fatalf("migrate up (first run): %v", err)
	}
	// Second run must be a clean no-op rather than "duplicate table" /
	// "duplicate column" — the CI gate applies migrations before every test
	// run, and a non-idempotent set would fail here.
	if err := app.MigrateUp(ctx, cfg.Database.DSN(), "../../db/migrations"); err != nil {
		t.Fatalf("migrate up (re-run must be a no-op): %v", err)
	}

	svc, _ := testEnv(t)
	var version uint64
	var dirty bool
	if err := svc.DB.QueryRowContext(ctx, "SELECT version, dirty FROM schema_migrations LIMIT 1").
		Scan(&version, &dirty); err != nil {
		t.Fatalf("load schema_migrations: %v", err)
	}
	if version == 0 {
		t.Fatal("schema_migrations version = 0, want the applied migration set")
	}
	if dirty {
		t.Fatal("schema_migrations is dirty after a successful MigrateUp")
	}
	if version < 13 {
		t.Fatalf("schema_migrations version = %d, want >= 13 (0013_provider_admission_lock_write)", version)
	}
}
