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
LEFT JOIN providers p ON p.id = b.provider_id
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
