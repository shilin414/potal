package integration

// 第五轮 P2-5: migration 0019 completes the provider_id reconciliation.
//
// 0017 joins runtime_bindings to providers with an INNER JOIN on
// provider_key, so it can only repair rows whose key still resolves. A
// binding whose provider_key points at a provider that no longer exists
// is never matched, and its stale provider_id survives — even though it
// resolves to a DIFFERENT, still-live provider. provider_id has no FK,
// so nothing catches it.
//
// 0019 is the idempotent completion pass: LEFT JOIN, and NULL the column
// when the key no longer resolves.

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/catalog"
)

func applyMigrationFile(t *testing.T, d *sql.DB, file string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations", file))
	if err != nil {
		t.Fatalf("read migration %s: %v", file, err)
	}
	if _, err := d.ExecContext(context.Background(), string(raw)); err != nil {
		t.Fatalf("apply migration %s: %v", file, err)
	}
}

// seedBindingFixture creates a live provider + application + binding and
// returns (providerKey, providerID, applicationID, bindingID).
func seedBindingFixture(t *testing.T, d *sql.DB, suffix string) (string, int64, int64, int64) {
	t.Helper()
	uniq := fmt.Sprintf("%d", time.Now().UnixNano())
	provKey := "itest_bind_" + suffix + "_" + uniq
	provID := seedAuthzProvider(t, d, provKey, "active")
	appID := seedAuthzFixture(t, d, authzFixture{
		slug: "itest-bind-" + suffix + "-" + uniq, kind: "chat",
		isPublic: true, enabled: true, provider: provKey, providerID: provID,
		resource: "agent_bind_" + suffix,
	})
	var bindingID int64
	if err := d.QueryRow(
		`SELECT id FROM runtime_bindings WHERE application_id = ? ORDER BY id DESC LIMIT 1`, appID).
		Scan(&bindingID); err != nil {
		t.Fatalf("load binding: %v", err)
	}
	return provKey, provID, appID, bindingID
}

func setBindingProvider(t *testing.T, d *sql.DB, bindingID int64, key string, providerID any) {
	t.Helper()
	if _, err := d.ExecContext(context.Background(),
		`UPDATE runtime_bindings SET provider_key = ?, provider_id = ? WHERE id = ?`,
		key, providerID, bindingID); err != nil {
		t.Fatalf("rewrite binding %d: %v", bindingID, err)
	}
}

func bindingProviderID(t *testing.T, d *sql.DB, bindingID int64) sql.NullInt64 {
	t.Helper()
	var got sql.NullInt64
	if err := d.QueryRowContext(context.Background(),
		`SELECT provider_id FROM runtime_bindings WHERE id = ?`, bindingID).Scan(&got); err != nil {
		t.Fatalf("read provider_id: %v", err)
	}
	return got
}

// countInconsistentBindings is the invariant 0019 exists to establish:
// provider_id is a derived cache of provider_key, so for EVERY row it is
// either the id of the provider the key resolves to, or NULL when the key
// resolves to nothing.
func countInconsistentBindings(t *testing.T, d *sql.DB) int64 {
	t.Helper()
	var n int64
	if err := d.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM runtime_bindings b
		LEFT JOIN providers p ON p.provider_key = b.provider_key
		WHERE (p.id IS NULL AND b.provider_id IS NOT NULL)
		   OR (p.id IS NOT NULL AND (b.provider_id IS NULL OR b.provider_id <> p.id))`).
		Scan(&n); err != nil {
		t.Fatalf("count inconsistent bindings: %v", err)
	}
	return n
}

// The gap 0017 cannot close: a key that resolves to nothing, with a stale
// provider_id pointing at another live provider.
func TestMigration0019ClearsProviderIDForOrphanProviderKey(t *testing.T) {
	env := newScheduleEnv(t)
	_, _, appID, bindingID := seedBindingFixture(t, env.db, "orphan")
	otherID := seedAuthzProvider(t, env.db,
		fmt.Sprintf("itest_bind_other_%d", time.Now().UnixNano()), "active")
	t.Cleanup(func() { cleanupBindingFixture(env.db, appID, otherID) })

	ghostKey := fmt.Sprintf("ghost_provider_%d", time.Now().UnixNano())
	setBindingProvider(t, env.db, bindingID, ghostKey, otherID)

	// 0017 must NOT be able to repair this row (INNER JOIN never matches).
	applyMigrationFile(t, env.db, "0017_reconcile_binding_provider_id.up.sql")
	if got := bindingProviderID(t, env.db, bindingID); !got.Valid || got.Int64 != otherID {
		t.Fatalf("after 0017 provider_id = %v, want %d (unchanged): this is the row "+
			"0017 structurally cannot reach", got, otherID)
	}

	applyMigrationFile(t, env.db, "0019_full_reconcile_binding_provider_id.up.sql")
	if got := bindingProviderID(t, env.db, bindingID); got.Valid {
		t.Fatalf("after 0019 provider_id = %d, want NULL: the key %q resolves to "+
			"no provider, so the derived column must be cleared, not left stale",
			got.Int64, ghostKey)
	}
}

// Management DTOs and resolve must use the provider selected by provider_key,
// even while a historical provider_id cache points at another active row.
func TestConsumptionBundleUsesProviderKeyWhenProviderIDIsWrong(t *testing.T) {
	env := newScheduleEnv(t)
	providerKey, providerID, appID, bindingID := seedBindingFixture(t, env.db, "bundle-ssot")
	wrongKey := fmt.Sprintf("itest_bind_wrong_%d", time.Now().UnixNano())
	wrongID := seedAuthzProvider(t, env.db, wrongKey, "active")
	t.Cleanup(func() {
		cleanupBindingFixture(env.db, appID, wrongID)
		_, _ = env.db.ExecContext(context.Background(), `DELETE FROM providers WHERE id = ?`, providerID)
	})
	if _, err := env.db.ExecContext(context.Background(),
		`UPDATE providers SET capabilities = ? WHERE id = ?`,
		`{"attachments":true}`, providerID); err != nil {
		t.Fatalf("set provider capabilities: %v", err)
	}
	setBindingProvider(t, env.db, bindingID, providerKey, wrongID)

	repo := &catalog.Repo{DB: env.db}
	bundle, err := repo.ConsumptionBundleByID(context.Background(), appID)
	if err != nil {
		t.Fatalf("load consumption bundle: %v", err)
	}
	if bundle.Provider == nil || bundle.Provider.Key != providerKey {
		t.Fatalf("bundle provider = %#v, want key %q rather than provider_id key %q",
			bundle.Provider, providerKey, wrongKey)
	}
	if got, _ := bundle.Provider.Capabilities["attachments"].(bool); !got {
		t.Fatalf("bundle capabilities came from the wrong provider: %#v", bundle.Provider.Capabilities)
	}

	if _, err := env.db.ExecContext(context.Background(),
		`UPDATE providers SET status = 'inactive' WHERE id = ?`, providerID); err != nil {
		t.Fatalf("deactivate key provider: %v", err)
	}
	bundle, err = repo.ConsumptionBundleByID(context.Background(), appID)
	if err != nil {
		t.Fatalf("reload consumption bundle: %v", err)
	}
	if bundle.ProviderStatus == nil || *bundle.ProviderStatus != "inactive" {
		t.Fatalf("bundle status = %v, want inactive from provider_key row", bundle.ProviderStatus)
	}
	if catalog.Consumable(catalog.ConsumptionFacts{
		Application: bundle.Application, Binding: bundle.Binding, ProviderStatus: bundle.ProviderStatus,
	}) {
		t.Fatal("active wrong provider_id incorrectly made an inactive provider_key consumable")
	}

	ghostKey := fmt.Sprintf("ghost_bundle_%d", time.Now().UnixNano())
	setBindingProvider(t, env.db, bindingID, ghostKey, wrongID)
	bundle, err = repo.ConsumptionBundleByID(context.Background(), appID)
	if err != nil {
		t.Fatalf("load missing-key bundle: %v", err)
	}
	if bundle.Provider != nil || bundle.ProviderStatus != nil {
		t.Fatalf("missing provider_key fell back to provider_id: provider=%#v status=%v",
			bundle.Provider, bundle.ProviderStatus)
	}
}

// A key that resolves, but provider_id points at the wrong provider.
func TestMigration0019RepairsWrongProviderID(t *testing.T) {
	env := newScheduleEnv(t)
	provKey, provID, appID, bindingID := seedBindingFixture(t, env.db, "wrong")
	otherID := seedAuthzProvider(t, env.db,
		fmt.Sprintf("itest_bind_other_%d", time.Now().UnixNano()), "active")
	t.Cleanup(func() { cleanupBindingFixture(env.db, appID, otherID) })

	setBindingProvider(t, env.db, bindingID, provKey, otherID)
	applyMigrationFile(t, env.db, "0019_full_reconcile_binding_provider_id.up.sql")

	if got := bindingProviderID(t, env.db, bindingID); !got.Valid || got.Int64 != provID {
		t.Fatalf("provider_id = %v, want %d (the provider %q resolves to)",
			got, provID, provKey)
	}
}

// NULL provider_id with a resolvable key (the 0016 case, re-covered).
func TestMigration0019RepairsMissingProviderID(t *testing.T) {
	env := newScheduleEnv(t)
	provKey, provID, appID, bindingID := seedBindingFixture(t, env.db, "null")
	t.Cleanup(func() { cleanupBindingFixture(env.db, appID, 0) })

	setBindingProvider(t, env.db, bindingID, provKey, nil)
	applyMigrationFile(t, env.db, "0019_full_reconcile_binding_provider_id.up.sql")

	if got := bindingProviderID(t, env.db, bindingID); !got.Valid || got.Int64 != provID {
		t.Fatalf("provider_id = %v, want %d", got, provID)
	}
}

// Idempotent + globally consistent: a second run changes nothing, and no
// inconsistent row is left anywhere in the table.
func TestMigration0019IsIdempotentAndLeavesNoInconsistency(t *testing.T) {
	env := newScheduleEnv(t)
	provKey, _, appID, bindingID := seedBindingFixture(t, env.db, "idem")
	otherID := seedAuthzProvider(t, env.db,
		fmt.Sprintf("itest_bind_other_%d", time.Now().UnixNano()), "active")
	t.Cleanup(func() { cleanupBindingFixture(env.db, appID, otherID) })
	setBindingProvider(t, env.db, bindingID, provKey, otherID)

	applyMigrationFile(t, env.db, "0019_full_reconcile_binding_provider_id.up.sql")
	if n := countInconsistentBindings(t, env.db); n != 0 {
		t.Fatalf("%d inconsistent binding(s) remain after 0019, want 0", n)
	}
	first := bindingProviderID(t, env.db, bindingID)

	applyMigrationFile(t, env.db, "0019_full_reconcile_binding_provider_id.up.sql")
	if second := bindingProviderID(t, env.db, bindingID); second != first {
		t.Fatalf("second run changed provider_id from %v to %v: 0019 must be idempotent",
			first, second)
	}
	if n := countInconsistentBindings(t, env.db); n != 0 {
		t.Fatalf("%d inconsistent binding(s) after the second run, want 0", n)
	}
}

func cleanupBindingFixture(d *sql.DB, appID int64, extraProviderID int64) {
	ctx := context.Background()
	_, _ = d.ExecContext(ctx, `DELETE FROM runtime_bindings WHERE application_id = ?`, appID)
	_, _ = d.ExecContext(ctx, `DELETE FROM applications WHERE id = ?`, appID)
	if extraProviderID != 0 {
		_, _ = d.ExecContext(ctx, `DELETE FROM providers WHERE id = ?`, extraProviderID)
	}
}
