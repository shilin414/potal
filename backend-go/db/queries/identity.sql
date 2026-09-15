-- ─────────────────────────────────────────────────────────── identity ──

-- name: LockUserRow :one
-- Per-user admission lock (评测 P1-7): the user's own row is the natural
-- serialization point for "count my outstanding runs / schedules, then
-- create one" — it makes those caps real instead of best-effort. Callers
-- hold it inside the same transaction that inserts the run/schedule.
SELECT id FROM users WHERE id = ? FOR UPDATE;

-- name: CreateUser :execresult
INSERT INTO users (username, password_hash, display_name, display_id, email, role, auth_source, is_staff)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetUserByID :one
SELECT id, username, password_hash, display_name, display_id, email, avatar_url, bio, role, auth_source, is_staff, is_active, created_at, updated_at
FROM users WHERE id = ?;

-- name: GetUserByUsername :one
SELECT id, username, password_hash, display_name, display_id, email, avatar_url, bio, role, auth_source, is_staff, is_active, created_at, updated_at
FROM users WHERE username = ?;

-- name: UsernameExists :one
SELECT COUNT(*) AS n FROM users WHERE username = ?;

-- name: UpdateUserProfile :execresult
UPDATE users SET username = ?, email = ?, avatar_url = ?, bio = ?, display_id = ? WHERE id = ?;

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = ? WHERE id = ?;

-- name: GetFeishuIdentityByOpenID :one
SELECT id, user_id, open_id, union_id, feishu_user_id, display_name, avatar_url,
       refresh_token_enc, refresh_token_expires_at, last_login_at, created_at, updated_at
FROM feishu_identities WHERE open_id = ?;

-- name: GetFeishuIdentityByFeishuUserID :one
SELECT id, user_id, open_id, union_id, feishu_user_id, display_name, avatar_url,
       refresh_token_enc, refresh_token_expires_at, last_login_at, created_at, updated_at
FROM feishu_identities WHERE feishu_user_id = ?;

-- name: GetFeishuIdentityByLocalUser :one
SELECT id, user_id, open_id, union_id, feishu_user_id, display_name, avatar_url,
       refresh_token_enc, refresh_token_expires_at, last_login_at, created_at, updated_at
FROM feishu_identities WHERE user_id = ?;

-- name: CreateFeishuIdentity :execresult
INSERT INTO feishu_identities (user_id, open_id, union_id, feishu_user_id, display_name, avatar_url, refresh_token_enc, refresh_token_expires_at, last_login_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: UpdateFeishuIdentityLogin :exec
UPDATE feishu_identities
SET display_name = ?, avatar_url = ?, refresh_token_enc = ?, refresh_token_expires_at = ?,
    feishu_user_id = IF(sqlc.arg('fu_user_id') = '', feishu_user_id, sqlc.arg('fu_user_id')),
    last_login_at = CURRENT_TIMESTAMP(3)
WHERE id = ?;

-- name: RotateRefreshToken :exec
UPDATE feishu_identities SET refresh_token_enc = ?, refresh_token_expires_at = ? WHERE id = ?;

-- name: CreateAuditLog :exec
-- Admin login auditing (修复计划 §41): records success/failure without
-- ever storing credentials.
INSERT INTO audit_logs (user_id, action, resource, resource_id, detail)
VALUES (?, ?, 'auth', ?, ?);
