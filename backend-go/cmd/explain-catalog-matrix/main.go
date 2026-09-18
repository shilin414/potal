// explain-catalog-matrix — 三次复审 P2-R1 (2026-09-18).
//
// Runs the catalog page query in every scenario the review report demands
// (chat/fixed/all × public/manage/consume × category/search/cursor) plus the
// workspace bootstrap group queries, prefixed with EXPLAIN, against the
// configured database (DB_* env / .env.local) and prints one plan line per
// joined table. The point is to RE-MEASURE on the target MySQL version
// whenever the query shape, the indexes or the catalog scale change — the
// recorded conclusions live in
// docs/potal 三次复审 EXPLAIN 矩阵实测（MySQL 5.7.32）.md and were measured
// on MySQL 5.7.32 with the post-0025 index set.
//
// Usage:
//
//	go run ./cmd/explain-catalog-matrix
//	go run ./cmd/explain-catalog-matrix -synthetic \
//	    -applications 5000 -bindings 10000 -runs 100000
//
// Synthetic mode (四次复审 P2-R3) is opt-in and creates ONLY rows whose
// slug/provider/name carry the `expsyn_` prefix, in the configured database,
// then deletes them in FK order at the end. It exists so the scale thresholds
// already recorded in the review docs can be re-measured on an isolated test
// database instead of waiting for shared dev data to grow naturally. NEVER
// point it at a production database.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/platform/config"
	"github.com/creation-agent-studio/backend-go/internal/platform/database"
)

const syntheticPrefix = "expsyn_"

// syntheticConfig is the fixture size requested on the command line. The
// values are deliberately explicit so a run is reproducible from its command
// line alone.
type syntheticConfig struct {
	Applications int
	Bindings     int
	RunsPerUser  int
	Users        int
	// IsolatedDB is the explicit "yes, this database is disposable" switch.
	// The name check below is deliberately strict: a typo like `xiaoan` must
	// never seed 100k runs into a shared dev database.
	IsolatedDB string
}

func syntheticFixtureEnabled(fs *flag.FlagSet, args []string) (bool, syntheticConfig) {
	enabled := fs.Bool("synthetic", false, "seed an isolated expsyn_ fixture, run bootstrap timing, then clean it up")
	applications := fs.Int("applications", 5000, "synthetic applications to seed")
	bindings := fs.Int("bindings", 10000, "synthetic bindings to seed")
	runsPerUser := fs.Int("runs", 100000, "synthetic runs per user to seed")
	users := fs.Int("users", 1, "synthetic users to seed")
	isolatedDB := fs.String("isolated-db", "", "REQUIRED confirmation: exact database name reserved for this benchmark")
	_ = fs.Parse(args)
	return *enabled, syntheticConfig{
		Applications: *applications,
		Bindings:     *bindings,
		RunsPerUser:  *runsPerUser,
		Users:        *users,
		IsolatedDB:   *isolatedDB,
	}
}

// seedSyntheticFixture writes ONLY expsyn_-prefixed rows and returns a
// cleanup function that removes them in FK order. It refuses to run unless
// the caller confirms the EXACT disposable database name, so a typo can never
// seed 100k runs into a shared dev database.
func seedSyntheticFixture(ctx context.Context, db *sql.DB, cfg syntheticConfig) (func(), error) {
	var dbName string
	if err := db.QueryRowContext(ctx, `SELECT DATABASE()`).Scan(&dbName); err != nil {
		return nil, err
	}
	if dbName == "" {
		return nil, fmt.Errorf("synthetic mode needs an explicit database")
	}
	if cfg.IsolatedDB == "" || cfg.IsolatedDB != dbName {
		return nil, fmt.Errorf(
			"synthetic mode refused: pass -isolated-db=%s to confirm this disposable database (got %q)",
			dbName, cfg.IsolatedDB)
	}
	if cfg.Applications <= 0 || cfg.Bindings <= 0 || cfg.RunsPerUser < 0 || cfg.Users <= 0 {
		return nil, fmt.Errorf("synthetic sizes must be positive (runs may be 0)")
	}
	cleanup := func() {}
	// Providers first: one active provider backs every synthetic binding.
	if _, err := db.ExecContext(ctx, `
INSERT INTO providers (provider_key, name, supported_runtime_types, status)
VALUES (?, ?, '["agent"]', 'active')
ON DUPLICATE KEY UPDATE status = 'active'`,
		syntheticPrefix+"provider", "synthetic benchmark provider"); err != nil {
		return nil, fmt.Errorf("seed provider: %w", err)
	}
	providerID := int64(0)
	if err := db.QueryRowContext(ctx,
		`SELECT id FROM providers WHERE provider_key = ?`,
		syntheticPrefix+"provider").Scan(&providerID); err != nil {
		return nil, fmt.Errorf("load provider: %w", err)
	}

	appIDs := make([]int64, 0, cfg.Applications)
	for i := 0; i < cfg.Applications; i++ {
		slug := fmt.Sprintf("%sapp_%06d", syntheticPrefix, i)
		res, err := db.ExecContext(ctx, `
INSERT INTO applications (slug, name, description, kind, renderer_key, is_public, enabled, created_by, created_at)
VALUES (?, ?, ?, 'chat', 'chat', 1, 1, 424242, FROM_UNIXTIME(?))`,
			slug, fmt.Sprintf("Synthetic Agent %06d", i),
			"synthetic benchmark fixture", time.Now().Add(-time.Duration(i)*time.Second).Unix())
		if err != nil {
			return nil, fmt.Errorf("seed application %d: %w", i, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nil, err
		}
		appIDs = append(appIDs, id)
	}
	// One binding per (application, runtime_type, provider_key) by schema; the
	// requested binding count is therefore capped at one per application.
	bindingCount := cfg.Bindings
	if bindingCount > len(appIDs) {
		bindingCount = len(appIDs)
	}
	for i := 0; i < bindingCount; i++ {
		appID := appIDs[i%len(appIDs)]
		if _, err := db.ExecContext(ctx, `
INSERT INTO runtime_bindings (application_id, provider_id, provider_key, runtime_type,
  external_resource_id, identity_mode, execution_mode, session_policy, artifact_policy,
  timeout_seconds, enabled)
VALUES (?, ?, ?, 'agent', ?, 'per_user', 'sync', 'per_conversation', 'none', 300, 1)`,
			appID, providerID, syntheticPrefix+"provider",
			fmt.Sprintf("%sresource_%06d", syntheticPrefix, i)); err != nil {
			return nil, fmt.Errorf("seed binding %d: %w", i, err)
		}
	}
	// Runs are the expensive dimension: generate them in bounded batches.
	runBatch := 2000
	for userOffset := 0; userOffset < cfg.Users; userOffset++ {
		userID := int64(424242 + userOffset)
		remaining := cfg.RunsPerUser
		for remaining > 0 {
			n := runBatch
			if n > remaining {
				n = remaining
			}
			values := make([]string, 0, n)
			args := make([]any, 0, n*4)
			for i := 0; i < n; i++ {
				values = append(values, "(UNHEX(REPLACE(UUID(), '-', '')), ?, ?, 'succeeded', '{}', FROM_UNIXTIME(?))")
				args = append(args, userID, appIDs[(i+userOffset)%len(appIDs)],
					time.Now().Add(-time.Duration(i)*time.Second).Unix())
			}
			query := `INSERT INTO runs (id, user_id, application_id, status, output, created_at) VALUES ` + strings.Join(values, ",")
			if _, err := db.ExecContext(ctx, query, args...); err != nil {
				return nil, fmt.Errorf("seed runs user=%d remaining=%d: %w", userID, remaining, err)
			}
			remaining -= n
		}
	}
	cleanup = func() { cleanupSyntheticFixture(ctx, db) }
	return cleanup, nil
}

// cleanupSyntheticFixture is idempotent and safe to call both after a
// successful run and after a partial seed failure.
func cleanupSyntheticFixture(ctx context.Context, db *sql.DB) {
	_, _ = db.ExecContext(ctx,
		`DELETE FROM runs WHERE application_id IN (SELECT id FROM applications WHERE slug LIKE ?)`,
		syntheticPrefix+"%")
	_, _ = db.ExecContext(ctx,
		`DELETE FROM runtime_bindings WHERE provider_key = ?`, syntheticPrefix+"provider")
	_, _ = db.ExecContext(ctx,
		`DELETE FROM applications WHERE slug LIKE ?`, syntheticPrefix+"%")
	_, _ = db.ExecContext(ctx,
		`DELETE FROM providers WHERE provider_key = ?`, syntheticPrefix+"provider")
}

// bootstrapQuery is the same shape the EXPLAIN matrix records for the default
// group. Timing it over the synthetic fixture is what turns the review's
// threshold table into an executable gate.
const bootstrapQuery = `
SELECT a.id, COALESCE(u.usage_count,0) AS personal_usage_count, u.last_used_at
FROM applications a
LEFT JOIN runtime_bindings b
  ON b.application_id = a.id AND b.enabled = 1
LEFT JOIN runtime_bindings newer_b
  ON newer_b.application_id = b.application_id
 AND newer_b.enabled = 1
 AND newer_b.id > b.id
LEFT JOIN providers p ON p.provider_key = b.provider_key
LEFT JOIN (SELECT r.application_id, COUNT(*) AS usage_count, MAX(r.created_at) AS last_used_at
           FROM runs r WHERE r.user_id = ? GROUP BY r.application_id) u
  ON u.application_id = a.id
WHERE newer_b.id IS NULL AND a.enabled = 1 AND a.kind = 'chat'
  AND b.id IS NOT NULL AND p.id IS NOT NULL AND p.status = 'active'
  AND a.is_public = 1
ORDER BY a.is_default_agent DESC, a.created_at, a.id LIMIT 8`

func runSyntheticBootstrap(ctx context.Context, db *sql.DB, callerID int64) error {
	const iterations = 40
	durations := make([]time.Duration, 0, iterations)
	for i := 0; i < iterations; i++ {
		start := time.Now()
		rows, err := db.QueryContext(ctx, bootstrapQuery, callerID)
		if err != nil {
			return fmt.Errorf("synthetic bootstrap query %d: %w", i, err)
		}
		for rows.Next() {
			var id int64
			var usage int64
			var last sql.NullTime
			if err := rows.Scan(&id, &usage, &last); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan synthetic bootstrap: %w", err)
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("iterate synthetic bootstrap: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close synthetic bootstrap: %w", err)
		}
		durations = append(durations, time.Since(start))
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	pick := func(p float64) time.Duration {
		idx := int(float64(len(durations)-1) * p)
		return durations[idx]
	}
	fmt.Printf("synthetic bootstrap p50=%s p95=%s p99=%s (n=%d)\n",
		pick(0.50), pick(0.95), pick(0.99), len(durations))
	return nil
}

// page builds the ListApplicationPage query (db/queries/catalog.sql) with
// inlined literals. The flags mirror sqlc's `sqlc.arg(bool)` → `?` emission:
// 0/1 integers for the boolean flags, NULL-shaped predicates for the narg
// probes (search / category).
func page(scenario string, showAll, mineOnly, publicOnly, consumeOnly, kindChatOnly, kindFixedOnly, kindAll, allowUnbound int, search string, categorySlug string, categoryIsNull int, cursor string, limit int) {
	searchPred := "NULL IS NULL"
	if search != "" {
		searchPred = fmt.Sprintf("(0 IS NULL OR a.name COLLATE utf8mb4_unicode_ci LIKE '%%%s%%' OR COALESCE(a.description,'') COLLATE utf8mb4_unicode_ci LIKE '%%%s%%' OR COALESCE(c.name,'') COLLATE utf8mb4_unicode_ci LIKE '%%%s%%')", search, search, search)
	}
	categoryPred := "NULL IS NULL"
	if categorySlug != "" {
		if categoryIsNull == 1 {
			categoryPred = fmt.Sprintf("(NULL IS NULL OR (1 AND a.category_id IS NULL) OR c.slug = '%s')", categorySlug)
		} else {
			categoryPred = fmt.Sprintf("('%s' IS NULL OR (0 AND a.category_id IS NULL) OR c.slug = '%s')", categorySlug, categorySlug)
		}
	}
	// First page: the anchor sits far in the past, so `created_at > ?` is a
	// no-op — the same shape the handler sends for cursor="".
	cursorPred := "a.created_at > '2000-01-01 00:00:00' OR (a.created_at = '2000-01-01 00:00:00' AND a.id > 0)"
	if cursor != "" {
		cursorPred = fmt.Sprintf("a.created_at > '%s' OR (a.created_at = '%s' AND a.id > 500)", cursor, cursor)
	}
	sql := fmt.Sprintf(`
SELECT a.id, a.slug, a.name, COALESCE(a.description,'') AS description, a.icon, a.avatar_key, a.color,
       a.kind, a.renderer_key, a.executor_key, a.category_id, a.is_public, a.is_default_agent, a.enabled,
       a.usage_count, a.tags, a.default_config, a.created_by, a.organization_id,
       a.created_at, a.updated_at,
       c.slug AS category_slug, c.name AS category_name,
       b.id AS binding_id, b.provider_id AS binding_provider_id, b.provider_key AS binding_provider_key,
       b.runtime_type AS binding_runtime_type, b.external_resource_id AS binding_external_resource_id,
       b.identity_mode AS binding_identity_mode, b.execution_mode AS binding_execution_mode,
       b.session_policy AS binding_session_policy, b.artifact_policy AS binding_artifact_policy,
       b.capabilities AS binding_capabilities, b.config AS binding_config,
       b.timeout_seconds AS binding_timeout_seconds, b.enabled AS binding_enabled
FROM applications a
LEFT JOIN application_categories c ON c.id = a.category_id
LEFT JOIN runtime_bindings b ON b.application_id = a.id AND b.enabled = 1
LEFT JOIN runtime_bindings newer_b ON newer_b.application_id = b.application_id AND newer_b.enabled = 1 AND newer_b.id > b.id
LEFT JOIN providers p ON p.provider_key = b.provider_key
WHERE newer_b.id IS NULL
  AND (%d OR (a.enabled = 1 AND ((%d AND a.created_by = 42) OR (%d AND a.is_public = 1))))
  AND (%d = 0 OR (a.enabled = 1 AND (a.kind <> 'chat' OR (b.id IS NOT NULL AND p.id IS NOT NULL AND p.status = 'active'))))
  AND ((%d AND a.kind = 'chat') OR (%d AND a.kind <> 'chat') OR %d)
  AND (a.kind <> 'chat' OR %d OR b.id IS NOT NULL)
  AND %s
  AND %s
  AND (%s)
ORDER BY a.created_at, a.id
LIMIT %d`,
		showAll, mineOnly, publicOnly, consumeOnly, kindChatOnly, kindFixedOnly, kindAll, allowUnbound,
		searchPred, categoryPred, cursorPred, limit)
	explain(scenario, sql)
}

func explain(scenario, sql string) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Println("config:", err)
		os.Exit(1)
	}
	db, err := database.Open(context.Background(), cfg.Database)
	if err != nil {
		fmt.Println("open:", err)
		os.Exit(1)
	}
	defer db.Close()
	rows, err := db.Query("EXPLAIN " + sql)
	if err != nil {
		fmt.Printf("== %s == EXPLAIN ERROR: %v\n", scenario, err)
		return
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	vals := make([][]byte, len(cols))
	ptrs := make([]interface{}, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	fmt.Printf("== %s ==\n", scenario)
	fmt.Println("  id | sel | table | type | possible_keys | key | key_len | rows | filtered | Extra")
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			fmt.Println("  scan:", err)
			return
		}
		pick := map[string]string{}
		for i, c := range cols {
			v := ""
			if vals[i] != nil {
				v = string(vals[i])
			}
			pick[c] = v
		}
		fmt.Printf("  %s | %s | %s | %s | %s | %s | %s | %s | %s | %s\n",
			pick["id"], pick["select_type"], pick["table"], pick["type"],
			strings.ReplaceAll(pick["possible_keys"], ",", "+"), strings.ReplaceAll(pick["key"], ",", "+"),
			pick["key_len"], pick["rows"], pick["filtered"], pick["Extra"])
	}
}

func main() {
	synthetic, syntheticCfg := syntheticFixtureEnabled(
		flag.NewFlagSet("explain-catalog-matrix", flag.ContinueOnError), os.Args[1:])
	cfg, err := config.Load()
	if err != nil {
		fmt.Println("config:", err)
		os.Exit(1)
	}
	db, err := database.Open(context.Background(), cfg.Database)
	if err != nil {
		fmt.Println("open:", err)
		os.Exit(1)
	}
	defer db.Close()
	var version string
	if err := db.QueryRow(`SELECT VERSION()`).Scan(&version); err != nil {
		fmt.Println("version:", err)
		os.Exit(1)
	}
	fmt.Println("DB_VERSION:", version)
	_ = cfg
	if synthetic {
		ctx := context.Background()
		cleanup, err := seedSyntheticFixture(ctx, db, syntheticCfg)
		if err != nil {
			// A partial seed must not leave expsyn_ rows behind even when it
			// fails before returning the cleanup closure.
			cleanupSyntheticFixture(ctx, db)
			fmt.Println("synthetic:", err)
			os.Exit(1)
		}
		defer cleanup()
		fmt.Printf("synthetic fixture ready: apps=%d bindings=%d users=%d runs/user=%d\n",
			syntheticCfg.Applications, syntheticCfg.Bindings, syntheticCfg.Users, syntheticCfg.RunsPerUser)
		if err := runSyntheticBootstrap(ctx, db, 424242); err != nil {
			fmt.Println("synthetic:", err)
			os.Exit(1)
		}
		return
	}

	// ── page scenarios (P2-R1 matrix) ──
	page("page chat/public page1", 0, 0, 1, 0, 1, 0, 0, 0, "", "", 0, "", 25)
	page("page chat/consume page1", 0, 0, 1, 1, 1, 0, 0, 0, "", "", 0, "", 25)
	page("page chat/manage(staff) page1", 1, 0, 0, 0, 1, 0, 0, 1, "", "", 0, "", 25)
	page("page fixed/consume page1", 0, 0, 1, 1, 0, 1, 0, 0, "", "", 0, "", 25)
	page("page fixed/manage page1", 0, 0, 1, 0, 0, 1, 0, 1, "", "", 0, "", 25)
	page("page all/manage page1", 1, 0, 0, 0, 0, 0, 1, 1, "", "", 0, "", 25)
	page("page chat/consume cursor-N", 0, 0, 1, 1, 1, 0, 0, 0, "", "", 0, "2026-01-01 00:00:00", 25)
	page("page chat/consume category", 0, 0, 1, 1, 1, 0, 0, 0, "", "it", 0, "", 25)
	page("page fixed/consume category-uncat", 0, 0, 1, 1, 0, 1, 0, 0, "", "__uncategorized__", 1, "", 25)
	page("page chat/consume search", 0, 0, 1, 1, 1, 0, 0, 0, "itest", "", 0, "", 25)

	explain("bootstrap default (public)", `
SELECT a.id, COALESCE(u.usage_count,0) AS personal_usage_count, u.last_used_at
FROM applications a
LEFT JOIN runtime_bindings b ON b.application_id = a.id AND b.enabled = 1
LEFT JOIN runtime_bindings newer_b ON newer_b.application_id = b.application_id AND newer_b.enabled = 1 AND newer_b.id > b.id
LEFT JOIN providers p ON p.provider_key = b.provider_key
LEFT JOIN (SELECT r.application_id, COUNT(*) AS usage_count, MAX(r.created_at) AS last_used_at
           FROM runs r WHERE r.user_id = 42 GROUP BY r.application_id) u ON u.application_id = a.id
WHERE newer_b.id IS NULL AND a.enabled = 1 AND a.kind = 'chat' AND b.id IS NOT NULL
  AND p.id IS NOT NULL AND p.status = 'active'
  AND (0 OR a.is_public = 1)
ORDER BY a.is_default_agent DESC, a.created_at, a.id LIMIT 8`)

	explain("bootstrap frequent (public)", `
SELECT a.id, u.usage_count AS personal_usage_count, u.last_used_at
FROM applications a
JOIN (SELECT r.application_id, COUNT(*) AS usage_count, MAX(r.created_at) AS last_used_at
      FROM runs r WHERE r.user_id = 42 GROUP BY r.application_id) u ON u.application_id = a.id
LEFT JOIN runtime_bindings b ON b.application_id = a.id AND b.enabled = 1
LEFT JOIN runtime_bindings newer_b ON newer_b.application_id = b.application_id AND newer_b.enabled = 1 AND newer_b.id > b.id
LEFT JOIN providers p ON p.provider_key = b.provider_key
WHERE newer_b.id IS NULL AND a.enabled = 1 AND a.kind = 'chat' AND b.id IS NOT NULL
  AND p.id IS NOT NULL AND p.status = 'active'
  AND (0 OR a.is_public = 1)
ORDER BY u.usage_count DESC, u.last_used_at DESC, a.name LIMIT 8`)

	explain("bootstrap recommended (public)", `
SELECT a.id, COALESCE(u.usage_count,0) AS personal_usage_count, u.last_used_at
FROM applications a
LEFT JOIN runtime_bindings b ON b.application_id = a.id AND b.enabled = 1
LEFT JOIN runtime_bindings newer_b ON newer_b.application_id = b.application_id AND newer_b.enabled = 1 AND newer_b.id > b.id
LEFT JOIN providers p ON p.provider_key = b.provider_key
LEFT JOIN (SELECT r.application_id, COUNT(*) AS usage_count, MAX(r.created_at) AS last_used_at
           FROM runs r WHERE r.user_id = 42 GROUP BY r.application_id) u ON u.application_id = a.id
WHERE newer_b.id IS NULL AND a.enabled = 1 AND a.kind = 'chat' AND b.id IS NOT NULL
  AND p.id IS NOT NULL AND p.status = 'active'
  AND (0 OR a.is_public = 1)
  AND NOT EXISTS (SELECT 1 FROM runs r WHERE r.user_id = 42 AND r.application_id = a.id)
  AND NOT EXISTS (SELECT 1 FROM application_favorites f WHERE f.user_id = 42 AND f.application_id = a.id)
ORDER BY a.usage_count DESC, a.name LIMIT 8`)

	explain("bootstrap recent fixed (public)", `
SELECT a.id, COALESCE(u.usage_count,0) AS personal_usage_count, u.last_used_at
FROM applications a
LEFT JOIN (SELECT r.application_id, COUNT(*) AS usage_count, MAX(r.created_at) AS last_used_at
           FROM runs r WHERE r.user_id = 42 GROUP BY r.application_id) u ON u.application_id = a.id
WHERE a.enabled = 1 AND a.kind <> 'chat' AND (0 OR a.is_public = 1)
ORDER BY u.last_used_at DESC, a.created_at, a.id LIMIT 8`)

	explain("bootstrap agent categories (public)", `
SELECT COALESCE(c.slug,'') AS category_slug, COALESCE(c.name,'') AS category_name, COUNT(*) AS category_count
FROM applications a
LEFT JOIN application_categories c ON c.id = a.category_id
LEFT JOIN runtime_bindings b ON b.application_id = a.id AND b.enabled = 1
LEFT JOIN runtime_bindings newer_b ON newer_b.application_id = b.application_id AND newer_b.enabled = 1 AND newer_b.id > b.id
LEFT JOIN providers p ON p.provider_key = b.provider_key
WHERE newer_b.id IS NULL AND a.enabled = 1 AND a.kind = 'chat' AND b.id IS NOT NULL
  AND p.id IS NOT NULL AND p.status = 'active'
  AND (0 OR a.is_public = 1)
GROUP BY a.category_id, c.slug, c.name
ORDER BY MIN(a.created_at), category_slug`)

	explain("page rows by ids (bootstrap row fetch)", `
SELECT a.id FROM applications a
LEFT JOIN application_categories c ON c.id = a.category_id
LEFT JOIN runtime_bindings b ON b.application_id = a.id AND b.enabled = 1
LEFT JOIN runtime_bindings newer_b ON newer_b.application_id = b.application_id AND newer_b.enabled = 1 AND newer_b.id > b.id
LEFT JOIN providers p ON p.provider_key = b.provider_key
WHERE newer_b.id IS NULL AND a.id IN (1,2,3,4,5,6,7,8) AND a.enabled = 1
  AND (0 OR a.is_public = 1)
  AND (a.kind <> 'chat' OR (b.id IS NOT NULL AND p.id IS NOT NULL AND p.status = 'active'))
ORDER BY a.created_at, a.id`)

	// The P1-R5 shape: every bootstrap group aggregates the user's runs.
	// idx_runs_user_application_created covers it (Using index) — re-check
	// this plan when a single user's run history reaches 100k/1m rows
	// before deciding on a user_application_usage summary table.
	explain("usage aggregate on runs (idx_runs_user_application_created)", `
SELECT r.application_id, COUNT(*) AS usage_count, MAX(r.created_at) AS last_used_at
FROM runs r WHERE r.user_id = 42 GROUP BY r.application_id`)
}
