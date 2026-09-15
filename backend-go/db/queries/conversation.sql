-- ───────────────────────────────────────────────────────── conversation ──

-- name: CreateConversation :execresult
INSERT INTO conversations (user_id, application_id, organization_id, title)
VALUES (?, ?, NULL, ?);

-- name: GetConversationByID :one
SELECT id, user_id, application_id, organization_id, title, created_at, updated_at
FROM conversations WHERE id = ?;

-- name: ListConversationsByApplication :many
SELECT id, user_id, application_id, organization_id, title, created_at, updated_at
FROM conversations
WHERE user_id = ? AND application_id = ?
ORDER BY updated_at DESC;

-- name: GetAgentThreadByConversation :one
SELECT id, conversation_id, provider, remote_id, status, auth_mode, auth_subject_key,
       config, created_at, updated_at
FROM agent_threads WHERE conversation_id = ?;

-- name: CreateAgentThread :execresult
INSERT INTO agent_threads (id, conversation_id, provider, remote_id, status, auth_mode, auth_subject_key, config)
VALUES (?, ?, ?, '', 'idle', ?, ?, NULL);

-- name: BindAgentThreadSession :exec
UPDATE agent_threads SET remote_id = ?, status = 'active' WHERE id = ?;

-- name: BindAgentThreadSessionOwned :execresult
-- Set-once session bind (修复计划 §27-28): binding succeeds when the
-- remote_id is empty OR already equals the value (idempotent re-bind by
-- the same session). 0 rows = a DIFFERENT session owns the thread — the
-- caller must treat that as a conflict, never overwrite.
UPDATE agent_threads
SET remote_id = ?, status = 'active'
WHERE id = ? AND (remote_id = '' OR remote_id = ?);

-- name: GetAgentThreadByID :one
SELECT id, conversation_id, provider, remote_id, status, auth_mode, auth_subject_key,
       config, created_at, updated_at
FROM agent_threads WHERE id = ?;

-- name: CreateMessage :execresult
INSERT INTO messages (conversation_id, role, content, metadata) VALUES (?, ?, ?, ?);

-- name: ListMessagesByConversation :many
SELECT id, conversation_id, role, content, metadata, created_at
FROM messages WHERE conversation_id = ? ORDER BY id;

-- name: CountMessagesByConversation :one
SELECT COUNT(*) AS n FROM messages WHERE conversation_id = ?;

-- name: ListConversationsForUser :many
-- Sidebar history: latest message + count via correlated scalar subqueries
-- in the SELECT list (window functions are MySQL 8 only — the project must
-- stay 5.7-compatible).
SELECT c.id, c.title, c.application_id, c.updated_at,
       (SELECT COUNT(*) FROM messages m WHERE m.conversation_id = c.id) AS message_count,
       (SELECT m2.role FROM messages m2 WHERE m2.conversation_id = c.id ORDER BY m2.id DESC LIMIT 1) AS last_role,
       (SELECT m2.content FROM messages m2 WHERE m2.conversation_id = c.id ORDER BY m2.id DESC LIMIT 1) AS last_content,
       (SELECT m2.created_at FROM messages m2 WHERE m2.conversation_id = c.id ORDER BY m2.id DESC LIMIT 1) AS last_created
FROM conversations c
WHERE c.user_id = ?
ORDER BY c.updated_at DESC
LIMIT 200;

-- name: ListConversationsForUserApp :many
SELECT c.id, c.title, c.application_id, c.updated_at,
       (SELECT COUNT(*) FROM messages m WHERE m.conversation_id = c.id) AS message_count,
       (SELECT m2.role FROM messages m2 WHERE m2.conversation_id = c.id ORDER BY m2.id DESC LIMIT 1) AS last_role,
       (SELECT m2.content FROM messages m2 WHERE m2.conversation_id = c.id ORDER BY m2.id DESC LIMIT 1) AS last_content,
       (SELECT m2.created_at FROM messages m2 WHERE m2.conversation_id = c.id ORDER BY m2.id DESC LIMIT 1) AS last_created
FROM conversations c
WHERE c.user_id = ? AND c.application_id = ?
ORDER BY c.updated_at DESC
LIMIT 200;

-- name: DeleteConversationMessages :exec
DELETE FROM messages WHERE conversation_id = ?;

-- name: DeleteConversationRuns :many
-- Returns run ids so the caller can purge their children in Go.
SELECT id FROM runs WHERE conversation_id = ?;

-- name: DeleteRunEvents :exec
DELETE FROM run_events WHERE run_id = ?;

-- name: DeleteRunArtifacts :exec
DELETE FROM run_artifacts WHERE run_id = ?;

-- name: DeleteRunCommands :exec
DELETE FROM run_commands WHERE run_id = ?;

-- name: DeleteRunLeaseByRun :exec
DELETE FROM run_leases WHERE run_id = ?;

-- name: DeleteRunsByConversation :exec
DELETE FROM runs WHERE conversation_id = ?;

-- name: UnbindConversationAttachments :exec
DELETE FROM runtime_attachments WHERE conversation_id = ?;

-- name: DeleteThreadByConversation :exec
DELETE FROM agent_threads WHERE conversation_id = ?;

-- name: DeleteConversation :exec
DELETE FROM conversations WHERE id = ?;

-- name: GetConversationRowForUpdate :one
-- Conversation admission lock (评测 P0-2): CreateRunInTx takes this lock so
-- the active-run count is re-read under it — two concurrent submits to the
-- same conversation serialize and the loser sees the winner's run.
SELECT id FROM conversations WHERE id = ? FOR UPDATE;

-- name: CountActiveRunsByConversation :one
-- Active-run count under the conversations row lock. Backed by
-- idx_runs_conversation_status (migration 0014).
SELECT COUNT(*) AS n FROM runs
WHERE conversation_id = ? AND status IN ('queued', 'running');

-- name: CountScheduledRunsByConversation :one
-- Lifetime guard (评测 P1): a run created by the scheduler owns an
-- occurrence and may still owe a pending delivery. Physically deleting it
-- would leave schedule_occurrences.run_id dangling and make the delivery
-- worker's GetRunByID fail → a timed-out notification.
SELECT COUNT(*) AS n FROM runs
WHERE conversation_id = ? AND trigger_type = 'scheduled';

-- name: CountDeliveriesByConversation :one
-- Same guard, direct: any delivery execution hanging off this
-- conversation's runs must keep them alive.
SELECT COUNT(*) AS n FROM delivery_executions d
JOIN runs r ON r.id = d.run_id
WHERE r.conversation_id = ?;

-- name: TouchConversationUpdated :exec
-- Sidebar ordering (评测 §十三): conversations.updated_at must move when a
-- message lands, otherwise an old conversation never returns to the top of
-- the list. Called in the same transaction as the message insert.
UPDATE conversations SET updated_at = CURRENT_TIMESTAMP(3) WHERE id = ?;
