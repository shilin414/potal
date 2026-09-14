package integration

import (
	"context"
	"os"
	"testing"

	"github.com/creation-agent-studio/backend-go/internal/app"
	"github.com/creation-agent-studio/backend-go/internal/platform/config"
)

// TestMigrateUpAgainstRealDatabase is the migration-path regression test
// for the CI failure:
//
//	Error 8048 (HY000): The isolation level 'SERIALIZABLE' is not
//	supported. Set tidb_skip_isolation_level_check=1 to skip this error
//
// golang-migrate's mysql driver records the schema version inside a
// SERIALIZABLE transaction, so on a vanilla TiDB (a fresh container, i.e.
// production defaults) the very first migration failed. MigrateUp now
// detects TiDB and carries the skip flag on the migration connection;
// this test exercises that path end to end, including the SetVersion
// write that used to abort.
func TestMigrateUpAgainstRealDatabase(t *testing.T) {
	if os.Getenv("STUDIO_TEST_TIDB") != "1" {
		t.Skip("set STUDIO_TEST_TIDB=1 to run the migration path against a real database")
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
		t.Fatalf("migrate up: %v", err)
	}
	// The version row is the artifact of the SERIALIZABLE transaction:
	// a clean, non-zero version proves the write succeeded.
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
}
