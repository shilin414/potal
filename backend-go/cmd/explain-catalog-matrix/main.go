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
// Usage: go run ./cmd/explain-catalog-matrix
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/creation-agent-studio/backend-go/internal/platform/config"
	"github.com/creation-agent-studio/backend-go/internal/platform/database"
)

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
