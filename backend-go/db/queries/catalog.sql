-- ───────────────────────────────────────────────────────────── catalog ──

-- name: GetProviderByKey :one
SELECT id, provider_key, name, description, supported_runtime_types, capabilities,
       start_rate_limit, max_inflight, poll_rate_limit, artifact_rate_limit,
       timeout_seconds, retry_policy, circuit_breaker, secret_ref, base_url, status,
       created_at, updated_at
FROM providers WHERE provider_key = ?;

-- name: GetExecutionAuthBundle :one
-- AuthorizeExecution (评测 P0-1): ONE query that joins every fact the run /
-- schedule admission must verify. Visibility is enforced server-side —
-- staff see everything; regular users only public, enabled applications.
-- The join itself cannot express the staff bypass, so the Go layer calls
-- it with show_all for staff and is_public=1 for regular users (mirrors
-- ListApplicationsByVisibility).
--
-- The provider is joined by provider_key, NOT provider_id (评测 P1):
-- provider_id is nullable and was left NULL by bindings created through
-- the API, which silently disabled the provider kill switch. provider_key
-- is the business key that always exists. A missing/inactive provider row
-- fails the check closed in Go.
SELECT a.id AS app_id, a.slug AS app_slug, a.name AS app_name, a.kind AS app_kind,
       a.is_public AS app_is_public, a.enabled AS app_enabled,
       b.id AS binding_id, b.provider_id AS binding_provider_id, b.provider_key AS binding_provider_key,
       b.runtime_type AS binding_runtime_type, b.external_resource_id AS binding_external_resource_id,
       b.endpoint_key AS binding_endpoint_key, b.identity_mode AS binding_identity_mode,
       b.execution_mode AS binding_execution_mode, b.session_policy AS binding_session_policy,
       b.artifact_policy AS binding_artifact_policy,
       b.capabilities AS binding_capabilities, b.input_schema AS binding_input_schema,
       b.output_schema AS binding_output_schema, b.config AS binding_config,
       b.secret_ref AS binding_secret_ref, b.timeout_seconds AS binding_timeout_seconds,
       b.enabled AS binding_enabled, b.created_at AS binding_created_at, b.updated_at AS binding_updated_at,
       p.status AS provider_status
FROM applications a
JOIN runtime_bindings b ON b.application_id = a.id AND b.enabled = 1
LEFT JOIN providers p ON p.provider_key = b.provider_key
WHERE a.id = ? AND a.enabled = 1 AND a.kind = 'chat'
  AND (sqlc.arg('show_all') OR a.is_public = 1)
ORDER BY b.id DESC
LIMIT 1;

-- name: ListActiveProviders :many
SELECT id, provider_key, name, description, supported_runtime_types, capabilities,
       start_rate_limit, max_inflight, poll_rate_limit, artifact_rate_limit,
       timeout_seconds, retry_policy, circuit_breaker, secret_ref, base_url, status,
       created_at, updated_at
FROM providers WHERE status = 'active' ORDER BY provider_key;

-- name: GetCategoryBySlug :one
SELECT id, slug, name, description, icon, sort_order, created_at, updated_at
FROM application_categories WHERE slug = ?;

-- name: GetCategoryByName :one
SELECT id, slug, name, description, icon, sort_order, created_at, updated_at
FROM application_categories WHERE name = ?;

-- name: CreateCategory :execresult
INSERT INTO application_categories (slug, name, description, icon, sort_order)
VALUES (?, ?, '', '', 0);

-- name: CreateApplication :execresult
INSERT INTO applications (slug, name, description, icon, avatar_key, color, kind,
    renderer_key, executor_key, category_id, is_public, is_default_agent,
    tags, default_config, created_by, organization_id)
VALUES (?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, 0, NULL, NULL, ?, NULL);

-- name: GetApplicationByID :one
SELECT a.id, a.slug, a.name, COALESCE(a.description, '') AS description, a.icon, a.avatar_key, a.color,
       a.kind, a.renderer_key, a.executor_key, a.category_id, a.is_public, a.is_default_agent, a.enabled,
       a.usage_count, a.tags, a.default_config, a.created_by, a.organization_id,
       a.created_at, a.updated_at,
       c.slug AS category_slug, c.name AS category_name
FROM applications a
LEFT JOIN application_categories c ON c.id = a.category_id
WHERE a.id = ?;

-- name: GetApplicationBySlug :one
SELECT a.id, a.slug, a.name, COALESCE(a.description, '') AS description, a.icon, a.avatar_key, a.color,
       a.kind, a.renderer_key, a.executor_key, a.category_id, a.is_public, a.is_default_agent, a.enabled,
       a.usage_count, a.tags, a.default_config, a.created_by, a.organization_id,
       a.created_at, a.updated_at,
       c.slug AS category_slug, c.name AS category_name
FROM applications a
LEFT JOIN application_categories c ON c.id = a.category_id
WHERE a.slug = ?;

-- name: ApplicationSlugExists :one
SELECT COUNT(*) AS n FROM applications WHERE slug = ?;

-- name: UpdateApplication :execresult
UPDATE applications
SET name = ?, description = ?, icon = ?, color = ?, is_public = ?,
    category_id = ?, renderer_key = ?
WHERE id = ?;

-- name: GetApplicationDefaultConfigForUpdate :one
-- Read-modify-write guard for default_config (the only JSON column the Go
-- backend WRITES). FOR UPDATE, not a bare read: 技能配置 replaces just the
-- `skills` key and must not clobber the other keys a legacy editor stored
-- there (guided_entry_prompt_key), so the merge needs the row pinned for the
-- duration of the transaction.
SELECT default_config FROM applications WHERE id = ? FOR UPDATE;

-- name: UpdateApplicationDefaultConfig :exec
UPDATE applications SET default_config = ? WHERE id = ?;

-- name: UpdateApplicationAvatar :exec
UPDATE applications SET avatar_key = ? WHERE id = ?;

-- name: SetApplicationEnabled :exec
UPDATE applications SET enabled = ? WHERE id = ?;

-- name: IncrementApplicationUsage :exec
UPDATE applications SET usage_count = usage_count + 1 WHERE id = ?;

-- name: ClearDefaultAgent :exec
UPDATE applications SET is_default_agent = 0 WHERE is_default_agent = 1;

-- name: SetDefaultAgent :exec
UPDATE applications SET is_default_agent = 1 WHERE id = ?;

-- name: GetDefaultAgent :one
SELECT a.id, a.slug, a.name, COALESCE(a.description, '') AS description, a.icon, a.avatar_key, a.color,
       a.kind, a.renderer_key, a.executor_key, a.category_id, a.is_public, a.is_default_agent, a.enabled,
       a.usage_count, a.tags, a.default_config, a.created_by, a.organization_id,
       a.created_at, a.updated_at,
       c.slug AS category_slug, c.name AS category_name
FROM applications a
LEFT JOIN application_categories c ON c.id = a.category_id
WHERE a.is_default_agent = 1
LIMIT 1;

-- name: DeleteApplication :exec
DELETE FROM applications WHERE id = ?;

-- name: CountConversationByApplication :one
SELECT COUNT(*) AS n FROM conversations WHERE application_id = ?;

-- name: CountRunByApplication :one
SELECT COUNT(*) AS n FROM runs WHERE application_id = ?;

-- name: CreateFavorite :exec
INSERT IGNORE INTO application_favorites (user_id, application_id) VALUES (?, ?);

-- name: DeleteFavorite :exec
DELETE FROM application_favorites WHERE user_id = ? AND application_id = ?;

-- name: ListFavorites :many
SELECT application_id FROM application_favorites WHERE user_id = ?;

-- name: CreateBinding :execresult
INSERT INTO runtime_bindings (application_id, provider_id, provider_key, runtime_type,
    external_resource_id, endpoint_key, identity_mode, execution_mode, session_policy,
    artifact_policy, capabilities, input_schema, output_schema, config, secret_ref,
    timeout_seconds, enabled)
VALUES (?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, NULL, NULL, ?, '', ?, 1);

-- name: UpdateBinding :execresult
UPDATE runtime_bindings
SET provider_id = ?, external_resource_id = ?, identity_mode = ?, execution_mode = ?,
    session_policy = ?, artifact_policy = ?, capabilities = ?, config = ?,
    timeout_seconds = ?, enabled = 1
WHERE id = ?;

-- name: GetBindingByID :one
SELECT id, application_id, provider_id, provider_key, runtime_type, external_resource_id,
       endpoint_key, identity_mode, execution_mode, session_policy, artifact_policy,
       capabilities, input_schema, output_schema, config, secret_ref, timeout_seconds,
       enabled, created_at, updated_at
FROM runtime_bindings WHERE id = ?;

-- name: ListBindingsByApplication :many
SELECT id, application_id, provider_id, provider_key, runtime_type, external_resource_id,
       endpoint_key, identity_mode, execution_mode, session_policy, artifact_policy,
       capabilities, input_schema, output_schema, config, secret_ref, timeout_seconds,
       enabled, created_at, updated_at
FROM runtime_bindings WHERE application_id = ? ORDER BY id;

-- name: GetEnabledBinding :one
SELECT id, application_id, provider_id, provider_key, runtime_type, external_resource_id,
       endpoint_key, identity_mode, execution_mode, session_policy, artifact_policy,
       capabilities, input_schema, output_schema, config, secret_ref, timeout_seconds,
       enabled, created_at, updated_at
FROM runtime_bindings
WHERE application_id = ? AND enabled = 1
ORDER BY id DESC
LIMIT 1;

-- name: FindBinding :one
SELECT id, application_id, provider_id, provider_key, runtime_type, external_resource_id,
       endpoint_key, identity_mode, execution_mode, session_policy, artifact_policy,
       capabilities, input_schema, output_schema, config, secret_ref, timeout_seconds,
       enabled, created_at, updated_at
FROM runtime_bindings
WHERE application_id = ? AND runtime_type = ? AND provider_key = ?;

-- name: ListEnabledBindings :many
SELECT id, application_id, provider_id, provider_key, runtime_type, external_resource_id,
       endpoint_key, identity_mode, execution_mode, session_policy, artifact_policy,
       capabilities, input_schema, output_schema, config, secret_ref, timeout_seconds,
       enabled, created_at, updated_at
FROM runtime_bindings WHERE enabled = 1;

-- name: UserUsageByApplication :many
SELECT application_id, COUNT(*) AS usage_count, MAX(created_at) AS last_used_at
FROM runs WHERE user_id = ? AND application_id IS NOT NULL
GROUP BY application_id;

-- name: ListApplicationsByVisibility :many
-- show_all lets staff bypass the SQL pre-filter; the authoritative
-- scope check still happens in Go (visible()).
SELECT a.id, a.slug, a.name, a.description, a.icon, a.avatar_key, a.color, a.kind, a.renderer_key,
       a.executor_key, a.category_id, a.is_public, a.is_default_agent, a.enabled, a.usage_count, a.tags,
       a.default_config, a.created_by, a.organization_id, a.created_at, a.updated_at,
       c.slug AS category_slug, c.name AS category_name
FROM applications a
LEFT JOIN application_categories c ON c.id = a.category_id
WHERE (sqlc.arg('show_all') OR a.is_public = ? OR a.created_by = ?)
ORDER BY a.created_at
LIMIT ?;

-- name: ListApplicationPage :many
-- True keyset pagination for the catalog (执行报告 §14–§21, 2026-09-17).
--
-- ONE query produces the page: the enabled binding is anti-joined (the
-- newest enabled binding wins, mirroring GetEnabledBinding's
-- `ORDER BY id DESC LIMIT 1`), which replaces the legacy
-- ListEnabledBindings + N×ApplicationByID walk (the N+1 the report calls
-- out in §18) AND covers unbound rows for free (b.id IS NULL).
--
-- The WHERE clause fully encodes the visible() access policy so the Go
-- layer does NOT re-filter (re-filtering would under-fill pages and break
-- cursor determinism):
--   staff            → everything (show_all);
--   regular users    → enabled = 1, plus
--     scope=mine     → own rows (private included),
--     scope=public/manage → is_public = 1.
-- `mode` (二次复审 P0-5) is ORTHOGONAL to scope: scope is "who may SEE the
-- row" (a management concern), mode is "may anyone actually USE it"
-- (consumption).
--   mode=consume   → additionally `enabled = 1` AND, for kind='chat', an
--                    enabled runtime binding (b.id IS NOT NULL). That is the
--                    SAME predicate AuthorizeExecution enforces, so a
--                    consumer surface can never list something the run API
--                    would then refuse. Staff is deliberately NOT exempt:
--                    seeing a disabled agent in 智能体市场 is a management
--                    need, opening it from the switcher is not.
-- kind: exclude_fixed → chat only; exclude_chat → non-chat ("fixed");
-- neither → all. exclude_unbound drops binding-less rows (the legacy
-- include_unbound=false semantics). Search is a case-insensitive substring
-- match on name / description / category name, mirroring the previous
-- client-side filter.
-- ⚠️ The COLLATE is LOAD-BEARING (执行报告 §7 / P0-R2): `applications` is
-- `DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin`, so a bare `a.name LIKE ?`
-- compares BYTE-wise and "sales" never matches "Sales Agent" — which the
-- OpenAPI contract ("case-insensitive substring match") promises it does,
-- and which the pre-pagination client-side filter did (JS toLowerCase).
-- Chinese hides the defect, so only an ASCII test can catch it. Overriding
-- the collation per comparison is cheaper and far safer than changing the
-- column/table collation (that would also make slug uniqueness
-- case-insensitive). The same collation is applied to the category name,
-- because mobile's local filter always searched it (filterMobileCatalog) —
-- leaving it out would keep the two modes disagreeing.
-- The cursor is the (created_at, id) keyset in ascending order — the same
-- order the legacy list used, so page one keeps the existing UI ordering.
SELECT a.id, a.slug, a.name, COALESCE(a.description, '') AS description, a.icon, a.avatar_key, a.color,
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
LEFT JOIN runtime_bindings b
  ON b.application_id = a.id AND b.enabled = 1
LEFT JOIN runtime_bindings newer_b
  ON newer_b.application_id = b.application_id
 AND newer_b.enabled = 1
 AND newer_b.id > b.id
WHERE newer_b.id IS NULL
  AND (sqlc.arg('show_all') OR (a.enabled = 1 AND (
        (sqlc.arg('mine_only') AND a.created_by = sqlc.arg('page_caller_id'))
        OR (sqlc.arg('public_only') AND a.is_public = 1))))
  AND (sqlc.arg('consume_only') = 0
       OR (a.enabled = 1 AND (a.kind <> 'chat' OR b.id IS NOT NULL)))
  AND ((sqlc.arg('kind_chat_only') AND a.kind = 'chat')
       OR (sqlc.arg('kind_fixed_only') AND a.kind <> 'chat')
       OR sqlc.arg('kind_all'))
  AND (sqlc.arg('allow_unbound') OR b.id IS NOT NULL)
  AND (sqlc.narg('search') IS NULL
       OR a.name COLLATE utf8mb4_unicode_ci LIKE sqlc.arg('search_name_like')
       OR COALESCE(a.description, '') COLLATE utf8mb4_unicode_ci LIKE sqlc.arg('search_desc_like')
       OR COALESCE(c.name, '') COLLATE utf8mb4_unicode_ci LIKE sqlc.arg('search_desc_like'))
  AND (sqlc.narg('category_slug') IS NULL
       OR (sqlc.arg('category_is_null') AND a.category_id IS NULL)
       OR c.slug = sqlc.arg('category_slug'))
  AND (a.created_at > sqlc.arg('cursor_created_gt')
       OR (a.created_at = sqlc.arg('cursor_created_eq') AND a.id > sqlc.arg('cursor_id_gt')))
ORDER BY a.created_at, a.id
LIMIT ?;

-- name: UserUsageByApplications :many
-- Per-page personal usage (执行报告 §20): only the ids on the current page
-- are aggregated, instead of the user's ENTIRE run history on every list
-- call. idx_runs_user_application_created (migration 0025) keeps it cheap.
SELECT application_id, COUNT(*) AS usage_count, MAX(created_at) AS last_used_at
FROM runs
WHERE user_id = ? AND application_id IN (sqlc.slice('page_app_ids'))
GROUP BY application_id;

-- name: FavoritesByApplications :many
-- Favorites restricted to one page's ids (same §20 rationale).
SELECT application_id FROM application_favorites
WHERE user_id = ? AND application_id IN (sqlc.slice('favorite_app_ids'));

-- ─────────────────────────────────────────────────────────────────────────
-- Workspace bootstrap groups (二次复审 P1-1)
--
-- The first version of the bootstrap ran ONE `ListApplicationPage` with
-- `Limit = 5000` and derived the groups in Go. That made the HTTP RESPONSE
-- constant-size but not the COST: rows examined, Go memory and CPU all grew
-- with the catalog, and past row 5000 the result was silently WRONG (a
-- category, the top 推荐 agent or the only valid default fallback that sorts
-- after row 5000 simply disappeared).
--
-- These queries ask the database for the business answer directly, so the
-- cost is bounded by the groups themselves (≤ 8 rows each):
--
--   query count      fixed (one per group + one row fetch)
--   rows examined    bounded by the indexes, not the catalog
--   Go objects       ≤ ~40 applications, regardless of catalog size
--
-- Every one of them applies, IN SQL:
--
--   * the visibility policy  — `show_all` (staff) OR `is_public = 1`;
--   * the CONSUME policy     — `enabled = 1` AND, for kind='chat', an
--                              enabled runtime binding (P0-5); a bootstrap
--                              group is a shortcut the user will click, so
--                              it must never offer something the run API
--                              would refuse.
--
-- `ONLY_FULL_GROUP_BY` (MySQL 5.7 default) is why the usage aggregate is a
-- DERIVED TABLE joined on application_id instead of a `GROUP BY a.id` over
-- the selected application columns: grouping by the primary key alone is not
-- enough for the server to accept the other selected columns.
-- ─────────────────────────────────────────────────────────────────────────

-- name: BootstrapDefaultApplication :one
-- 默认智能体: the explicit main agent wins; otherwise the first enabled +
-- bound chat application in catalog order (created_at, id) — the same
-- fallback the old Go-side `buildBootstrapGroups` implemented, so a fresh
-- install with nothing configured still gets a working composer.
--
-- `enabled = 1` is NEW (二次复审 P0-6): a disabled application used to stay
-- the default if it was promoted before being switched off, which bound the
-- home composer to an agent that cannot execute.
SELECT a.id,
       COALESCE(u.usage_count, 0) AS personal_usage_count,
       u.last_used_at
FROM applications a
LEFT JOIN runtime_bindings b
  ON b.application_id = a.id AND b.enabled = 1
LEFT JOIN runtime_bindings newer_b
  ON newer_b.application_id = b.application_id
 AND newer_b.enabled = 1
 AND newer_b.id > b.id
LEFT JOIN (SELECT r.application_id, COUNT(*) AS usage_count, MAX(r.created_at) AS last_used_at
           FROM runs r WHERE r.user_id = sqlc.arg('caller_id')
           GROUP BY r.application_id) u
  ON u.application_id = a.id
WHERE newer_b.id IS NULL
  AND a.enabled = 1
  AND a.kind = 'chat'
  AND b.id IS NOT NULL
  AND (sqlc.arg('show_all') OR a.is_public = 1)
ORDER BY a.is_default_agent DESC, a.created_at, a.id
LIMIT 1;

-- name: BootstrapFavoriteApplications :many
-- 收藏: chat applications this caller starred, most recently used first.
-- NULL `last_used_at` sorts LAST under DESC, matching the old Go `usedAt()`
-- (a missing timestamp meant "never used", i.e. the oldest possible).
SELECT a.id,
       COALESCE(u.usage_count, 0) AS personal_usage_count,
       u.last_used_at
FROM application_favorites f
JOIN applications a ON a.id = f.application_id
LEFT JOIN runtime_bindings b
  ON b.application_id = a.id AND b.enabled = 1
LEFT JOIN runtime_bindings newer_b
  ON newer_b.application_id = b.application_id
 AND newer_b.enabled = 1
 AND newer_b.id > b.id
LEFT JOIN (SELECT r.application_id, COUNT(*) AS usage_count, MAX(r.created_at) AS last_used_at
           FROM runs r WHERE r.user_id = sqlc.arg('caller_id')
           GROUP BY r.application_id) u
  ON u.application_id = a.id
WHERE f.user_id = sqlc.arg('fav_user_id')
  AND newer_b.id IS NULL
  AND a.enabled = 1
  AND a.kind = 'chat'
  AND b.id IS NOT NULL
  AND (sqlc.arg('show_all') OR a.is_public = 1)
ORDER BY u.last_used_at DESC, a.name
LIMIT ?;

-- name: BootstrapFrequentApplications :many
-- 常用智能体: chat applications this caller has actually run, most runs first.
SELECT a.id,
       u.usage_count AS personal_usage_count,
       u.last_used_at
FROM applications a
JOIN (SELECT r.application_id, COUNT(*) AS usage_count, MAX(r.created_at) AS last_used_at
      FROM runs r WHERE r.user_id = sqlc.arg('caller_id')
      GROUP BY r.application_id) u
  ON u.application_id = a.id
LEFT JOIN runtime_bindings b
  ON b.application_id = a.id AND b.enabled = 1
LEFT JOIN runtime_bindings newer_b
  ON newer_b.application_id = b.application_id
 AND newer_b.enabled = 1
 AND newer_b.id > b.id
WHERE newer_b.id IS NULL
  AND a.enabled = 1
  AND a.kind = 'chat'
  AND b.id IS NOT NULL
  AND (sqlc.arg('show_all') OR a.is_public = 1)
ORDER BY u.usage_count DESC, u.last_used_at DESC, a.name
LIMIT ?;

-- name: BootstrapRecentApplications :many
-- 最近使用: chat applications this caller ran, most recent first (the pool's
-- created_at order breaks ties, reproducing the old stable Go sort).
SELECT a.id,
       u.usage_count AS personal_usage_count,
       u.last_used_at
FROM applications a
JOIN (SELECT r.application_id, COUNT(*) AS usage_count, MAX(r.created_at) AS last_used_at
      FROM runs r WHERE r.user_id = sqlc.arg('caller_id')
      GROUP BY r.application_id) u
  ON u.application_id = a.id
LEFT JOIN runtime_bindings b
  ON b.application_id = a.id AND b.enabled = 1
LEFT JOIN runtime_bindings newer_b
  ON newer_b.application_id = b.application_id
 AND newer_b.enabled = 1
 AND newer_b.id > b.id
WHERE newer_b.id IS NULL
  AND a.enabled = 1
  AND a.kind = 'chat'
  AND b.id IS NOT NULL
  AND (sqlc.arg('show_all') OR a.is_public = 1)
ORDER BY u.last_used_at DESC, a.created_at, a.id
LIMIT ?;

-- name: BootstrapRecommendedApplications :many
-- 推荐: chat applications this caller has NEVER used and never starred, most
-- used GLOBALLY first. "Never used" is the definition, so both exclusions
-- are NOT EXISTS — not a left join whose result is then filtered in Go.
SELECT a.id,
       COALESCE(u.usage_count, 0) AS personal_usage_count,
       u.last_used_at
FROM applications a
LEFT JOIN runtime_bindings b
  ON b.application_id = a.id AND b.enabled = 1
LEFT JOIN runtime_bindings newer_b
  ON newer_b.application_id = b.application_id
 AND newer_b.enabled = 1
 AND newer_b.id > b.id
LEFT JOIN (SELECT r.application_id, COUNT(*) AS usage_count, MAX(r.created_at) AS last_used_at
           FROM runs r WHERE r.user_id = sqlc.arg('caller_id')
           GROUP BY r.application_id) u
  ON u.application_id = a.id
WHERE newer_b.id IS NULL
  AND a.enabled = 1
  AND a.kind = 'chat'
  AND b.id IS NOT NULL
  AND (sqlc.arg('show_all') OR a.is_public = 1)
  AND NOT EXISTS (SELECT 1 FROM runs r
                  WHERE r.user_id = sqlc.arg('caller_id') AND r.application_id = a.id)
  AND NOT EXISTS (SELECT 1 FROM application_favorites f
                  WHERE f.user_id = sqlc.arg('fav_user_id') AND f.application_id = a.id)
ORDER BY a.usage_count DESC, a.name
LIMIT ?;

-- name: BootstrapRecentFixedApplications :many
-- 常用应用 (应用中心 entries on the home page): enabled non-chat
-- applications, the recently opened ones first, then catalog order. No
-- runtime binding is required — a fixed application is a page delivered by
-- the build, not a provider runtime.
--
-- The group must still SHOW the fixed apps on a workspace nobody has used
-- yet, so `used` is an ORDERING key only, never a filter.
SELECT a.id,
       COALESCE(u.usage_count, 0) AS personal_usage_count,
       u.last_used_at
FROM applications a
LEFT JOIN (SELECT r.application_id, COUNT(*) AS usage_count, MAX(r.created_at) AS last_used_at
           FROM runs r WHERE r.user_id = sqlc.arg('caller_id')
           GROUP BY r.application_id) u
  ON u.application_id = a.id
WHERE a.enabled = 1
  AND a.kind <> 'chat'
  AND (sqlc.arg('show_all') OR a.is_public = 1)
ORDER BY u.last_used_at DESC, a.created_at, a.id
LIMIT ?;

-- name: BootstrapAgentCategories :many
-- 智能体分类 rail. Grouped in SQL (never in Go over a pool) and ordered by
-- first appearance (MIN(created_at)) — the order the market itself lists
-- applications in. Rows with no category come back with an EMPTY slug and
-- are rendered as the `__uncategorized__` sentinel by the caller.
SELECT COALESCE(c.slug, '') AS category_slug,
       COALESCE(c.name, '') AS category_name,
       COUNT(*) AS category_count
FROM applications a
LEFT JOIN application_categories c ON c.id = a.category_id
LEFT JOIN runtime_bindings b
  ON b.application_id = a.id AND b.enabled = 1
LEFT JOIN runtime_bindings newer_b
  ON newer_b.application_id = b.application_id
 AND newer_b.enabled = 1
 AND newer_b.id > b.id
WHERE newer_b.id IS NULL
  AND a.enabled = 1
  AND a.kind = 'chat'
  AND b.id IS NOT NULL
  AND (sqlc.arg('show_all') OR a.is_public = 1)
GROUP BY a.category_id, c.slug, c.name
ORDER BY MIN(a.created_at), category_slug;

-- name: BootstrapAppCategories :many
-- Same rail for 应用中心 (kind <> 'chat'), which needs no runtime binding.
SELECT COALESCE(c.slug, '') AS category_slug,
       COALESCE(c.name, '') AS category_name,
       COUNT(*) AS category_count
FROM applications a
LEFT JOIN application_categories c ON c.id = a.category_id
WHERE a.enabled = 1
  AND a.kind <> 'chat'
  AND (sqlc.arg('show_all') OR a.is_public = 1)
GROUP BY a.category_id, c.slug, c.name
ORDER BY MIN(a.created_at), category_slug;

-- name: ListApplicationRowsByIDs :many
-- Full catalog rows for EXACTLY the ids the bootstrap groups selected — the
-- single row-fetch that replaces materialising a 5000-row pool.
--
-- The visibility + consume predicates are repeated here deliberately: the
-- ids came from queries that already applied them, but re-applying makes
-- the row fetch safe on its own (and keeps the two from drifting).
SELECT a.id, a.slug, a.name, COALESCE(a.description, '') AS description, a.icon, a.avatar_key, a.color,
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
LEFT JOIN runtime_bindings b
  ON b.application_id = a.id AND b.enabled = 1
LEFT JOIN runtime_bindings newer_b
  ON newer_b.application_id = b.application_id
 AND newer_b.enabled = 1
 AND newer_b.id > b.id
WHERE newer_b.id IS NULL
  AND a.id IN (sqlc.slice('app_ids'))
  AND a.enabled = 1
  AND (sqlc.arg('show_all') OR a.is_public = 1)
  AND (a.kind <> 'chat' OR b.id IS NOT NULL)
ORDER BY a.created_at, a.id;
