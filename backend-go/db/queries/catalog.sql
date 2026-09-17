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
-- kind: exclude_fixed → chat only; exclude_chat → non-chat ("fixed");
-- neither → all. exclude_unbound drops binding-less rows (the legacy
-- include_unbound=false semantics). Search is a case-insensitive substring
-- match on name / description, mirroring the previous client-side filter.
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
  AND ((sqlc.arg('kind_chat_only') AND a.kind = 'chat')
       OR (sqlc.arg('kind_fixed_only') AND a.kind <> 'chat')
       OR sqlc.arg('kind_all'))
  AND (sqlc.arg('allow_unbound') OR b.id IS NOT NULL)
  AND (sqlc.narg('search') IS NULL
       OR a.name LIKE sqlc.arg('search_name_like')
       OR COALESCE(a.description, '') LIKE sqlc.arg('search_desc_like'))
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
