package database

import (
	"context"
	"os"
	"testing"

	"github.com/creation-agent-studio/backend-go/internal/platform/config"
)

// TestSessionTimezoneUTC pins the DSN contract: the session clock must be
// UTC so DB-side CURRENT_TIMESTAMP matches the driver's loc=UTC parsing.
// Opt-in (needs TiDB): STUDIO_TEST_TIDB=1.
func TestSessionTimezoneUTC(t *testing.T) {
	if os.Getenv("STUDIO_TEST_TIDB") != "1" {
		t.Skip("set STUDIO_TEST_TIDB=1")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	db, err := Open(context.Background(), cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT @@session.time_zone, NOW() = UTC_TIMESTAMP()")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var tz string
		var nowIsUTC bool
		if err := rows.Scan(&tz, &nowIsUTC); err != nil {
			t.Fatal(err)
		}
		if !nowIsUTC {
			t.Fatalf("session time_zone = %q; NOW() must equal UTC_TIMESTAMP()", tz)
		}
		t.Logf("session time_zone = %q, NOW() == UTC ✓", tz)
	}
}
