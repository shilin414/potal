-- ─────────────────────────────────────────────────────────── execution ──

-- name: CreateRun :execresult
INSERT INTO runs (id, user_id, application_id, conversation_id, runtime_binding_id,
    provider, runtime_type, external_run_id, status, provider_status, provider_finish_reason,
    input, output, runtime_snapshot, attempt, max_attempts, trigger_type, trigger_id,
    priority, available_at, error_code, error_message)
VALUES (?, ?, ?, ?, ?, ?, ?, '', 'queued', '', '', ?, NULL, ?, 0, ?, ?, ?, ?, ?, '', '');

-- name: GetRunByID :one
SELECT id, user_id, application_id, conversation_id, runtime_binding_id, organization_id,
       provider, runtime_type, external_run_id, status, provider_status, provider_finish_reason,
       input, output, runtime_snapshot, attempt, max_attempts, queued_at, started_at,
       finished_at, error_code, error_message, created_at, updated_at,
       trigger_type, trigger_id, priority, available_at, lease_epoch
FROM runs WHERE id = ?;

-- name: ListRunsByConversation :many
SELECT id, user_id, application_id, conversation_id, runtime_binding_id, organization_id,
       provider, runtime_type, external_run_id, status, provider_status, provider_finish_reason,
       input, output, runtime_snapshot, attempt, max_attempts, queued_at, started_at,
       finished_at, error_code, error_message, created_at, updated_at,
       trigger_type, trigger_id, priority, available_at, lease_epoch
FROM runs WHERE conversation_id = ?
ORDER BY created_at DESC;

-- name: UpdateRunExternalID :exec
-- Set-once semantic guarded in Go (only write when empty).
UPDATE runs SET external_run_id = ? WHERE id = ? AND external_run_id = '';

-- name: CASClaimRun :execresult
-- CAS claim: exactly one worker wins; affected_rows == 1 means success.
-- lease_epoch bump is the fencing token: every later write by the winning
-- worker must match the new epoch, so stale workers lose ownership.
UPDATE runs
SET status = 'running', started_at = CURRENT_TIMESTAMP(3), attempt = attempt + 1,
    lease_epoch = lease_epoch + 1
WHERE id = ? AND status = 'queued';

-- name: GetRunLeaseEpoch :one
SELECT lease_epoch FROM runs WHERE id = ?;

-- name: CASFinishRunFenced :execresult
-- Fenced terminal transition: only the current lease epoch may finish a
-- running run. 0 rows = already terminal (idempotent) OR lost ownership.
UPDATE runs
SET status = ?, output = ?, provider_status = ?, provider_finish_reason = ?,
    error_code = ?, error_message = ?, finished_at = CURRENT_TIMESTAMP(3)
WHERE id = ? AND status = 'running' AND lease_epoch = ?;

-- name: UpdateRunExternalIDFenced :exec
-- Set-once semantic guarded in Go (only write when empty) + fence.
UPDATE runs SET external_run_id = ? WHERE id = ? AND external_run_id = '' AND lease_epoch = ?;

-- name: ListQueuedRunIDs :many
SELECT id FROM runs
WHERE status = 'queued' AND provider = ?
ORDER BY queued_at
LIMIT ?;

-- name: CASFinishRun :execresult
-- Terminal-only transition; 0 rows affected = already terminal (idempotent).
UPDATE runs
SET status = ?, output = ?, provider_status = ?, provider_finish_reason = ?,
    error_code = ?, error_message = ?, finished_at = CURRENT_TIMESTAMP(3)
WHERE id = ? AND status NOT IN ('cancelled', 'succeeded', 'failed');

-- name: RequeueRun :exec
UPDATE runs SET status = 'queued' WHERE id = ? AND status = 'running';

-- name: RequeueRunFenced :execresult
UPDATE runs SET status = 'queued' WHERE id = ? AND status = 'running' AND lease_epoch = ?;

-- name: FailExpiredRun :execresult
UPDATE runs
SET status = 'failed', error_code = 'lease_expired', error_message = ?,
    finished_at = CURRENT_TIMESTAMP(3)
WHERE id = ? AND status = 'running';

-- name: FailRunFenced :execresult
UPDATE runs
SET status = 'failed', error_code = ?, error_message = ?,
    finished_at = CURRENT_TIMESTAMP(3)
WHERE id = ? AND status = 'running' AND lease_epoch = ?;

-- name: AppendRunEvent :execresult
INSERT INTO run_events (run_id, sequence, event_type, payload)
VALUES (?, ?, ?, ?);

-- name: CountRunEvents :one
SELECT COUNT(*) AS n FROM run_events WHERE run_id = ?;

-- name: ListRunEventsAfter :many
SELECT id, run_id, sequence, event_type, payload, created_at
FROM run_events WHERE run_id = ? AND sequence > ?
ORDER BY sequence;

-- name: ListAllRunEvents :many
SELECT id, run_id, sequence, event_type, payload, created_at
FROM run_events WHERE run_id = ?
ORDER BY sequence;

-- name: GetRunStatus :one
SELECT status FROM runs WHERE id = ?;

-- name: CreateRunCommand :execresult
INSERT INTO run_commands (id, run_id, command_type, payload, status, created_by)
VALUES (?, ?, ?, ?, 'pending', ?);

-- name: GetRunCommandByID :one
SELECT id, run_id, command_type, payload, status, created_by, created_at, resolved_at
FROM run_commands WHERE id = ?;

-- name: CreateRunLease :exec
INSERT INTO run_leases (run_id, worker_id, lease_token, heartbeat_at, expires_at)
VALUES (?, ?, ?, CURRENT_TIMESTAMP(3), ?);

-- name: HeartbeatLease :execresult
UPDATE run_leases
SET heartbeat_at = CURRENT_TIMESTAMP(3), expires_at = ?
WHERE run_id = ? AND worker_id = ?;

-- name: HeartbeatLeaseFenced :execresult
-- Ownership-checked by lease token (not worker_id): a recycled worker id
-- cannot renew a lease it no longer owns.
UPDATE run_leases
SET heartbeat_at = CURRENT_TIMESTAMP(3), expires_at = ?
WHERE run_id = ? AND lease_token = ?;

-- name: DeleteLease :exec
DELETE FROM run_leases WHERE run_id = ?;

-- name: DeleteLeaseFenced :exec
-- A worker may only delete its own lease; a stale worker can never drop
-- the new owner's lease row.
DELETE FROM run_leases WHERE run_id = ? AND lease_token = ?;

-- name: DeleteLeaseIfExpired :execresult
DELETE FROM run_leases WHERE run_id = ? AND expires_at <= CURRENT_TIMESTAMP(3);

-- name: ListExpiredLeaseRunIDs :many
SELECT run_id FROM run_leases
WHERE expires_at <= CURRENT_TIMESTAMP(3)
LIMIT ?;

-- name: GetLease :one
SELECT id, run_id, worker_id, lease_token, acquired_at, heartbeat_at, expires_at
FROM run_leases WHERE run_id = ?;

-- name: CreateOutboxEvent :execresult
INSERT INTO outbox_events (aggregate, aggregate_id, event_type, payload, status, available_at)
VALUES (?, ?, ?, ?, 'pending', CURRENT_TIMESTAMP(3));

-- name: ListPendingOutbox :many
SELECT id, aggregate, aggregate_id, event_type, payload, status, available_at,
       published_at, retry_count, last_error, created_at
FROM outbox_events
WHERE status = 'pending' AND available_at <= CURRENT_TIMESTAMP(3)
ORDER BY id
LIMIT ?;

-- name: MarkOutboxPublished :exec
UPDATE outbox_events SET status = 'published', published_at = CURRENT_TIMESTAMP(3) WHERE id = ?;

-- name: FailOutboxEvent :exec
UPDATE outbox_events
SET retry_count = retry_count + 1, last_error = ?,
    available_at = DATE_ADD(CURRENT_TIMESTAMP(3), INTERVAL LEAST(retry_count + 1, 10) SECOND)
WHERE id = ?;

-- name: CountPendingOutbox :one
SELECT COUNT(*) AS n FROM outbox_events WHERE status = 'pending';

-- name: CreateRunArtifact :exec
INSERT INTO run_artifacts (id, run_id, provider, external_artifact_id, provider_artifact_type,
    name, normalized_type, storage_type, resolution_status, metadata)
VALUES (?, ?, ?, ?, ?, ?, ?, 'external', 'pending', NULL);

-- name: UpsertRunArtifact :exec
-- Guarded by unique (run_id, external_artifact_id); empty external ids get
-- their own row keyed by the generated PK.
INSERT INTO run_artifacts (id, run_id, provider, external_artifact_id, provider_artifact_type,
    name, normalized_type, storage_type, resolution_status, metadata)
VALUES (?, ?, ?, ?, ?, ?, ?, 'external', 'pending', NULL)
ON DUPLICATE KEY UPDATE
    provider_artifact_type = VALUES(provider_artifact_type),
    normalized_type = VALUES(normalized_type);

-- name: GetRunArtifactByID :one
SELECT id, run_id, provider, external_artifact_id, provider_artifact_type, name,
       normalized_type, storage_type, cached_external_url, cached_url_fetched_at,
       cached_url_expires_at, storage_key, resolution_status, metadata, created_at, updated_at
FROM run_artifacts WHERE id = ?;

-- name: ListRunArtifacts :many
SELECT id, run_id, provider, external_artifact_id, provider_artifact_type, name,
       normalized_type, storage_type, cached_external_url, cached_url_fetched_at,
       cached_url_expires_at, storage_key, resolution_status, metadata, created_at, updated_at
FROM run_artifacts WHERE run_id = ?
ORDER BY created_at;

-- name: ListRunArtifactsByExternalID :one
SELECT id, run_id, provider, external_artifact_id, provider_artifact_type, name,
       normalized_type, storage_type, cached_external_url, cached_url_fetched_at,
       cached_url_expires_at, storage_key, resolution_status, metadata, created_at, updated_at
FROM run_artifacts WHERE run_id = ? AND external_artifact_id = ?;

-- name: CacheArtifactURL :exec
UPDATE run_artifacts
SET cached_external_url = ?, cached_url_fetched_at = CURRENT_TIMESTAMP(3),
    cached_url_expires_at = ?, name = IF(? = '', name, ?), resolution_status = 'resolved'
WHERE id = ?;

-- name: CreateAttachment :execresult
INSERT INTO runtime_attachments (id, run_id, conversation_id, provider,
    external_attachment_id, attachment_type, name, source_type, source_url,
    storage_key, content_type, size_bytes, auth_mode, auth_subject_key, status, metadata, created_by)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?);

-- name: GetAttachmentByID :one
SELECT id, run_id, conversation_id, provider, external_attachment_id, attachment_type,
       name, source_type, source_url, storage_key, content_type, size_bytes,
       auth_mode, auth_subject_key, status, metadata, created_by, created_at, updated_at
FROM runtime_attachments WHERE id = ?;

-- name: GetPendingOwnedAttachment :one
-- Ownership + state check happens per id in Go (≤8 per run, Aily limit).
SELECT id, run_id, conversation_id, provider, external_attachment_id, attachment_type,
       name, source_type, source_url, storage_key, content_type, size_bytes,
       auth_mode, auth_subject_key, status, metadata, created_by, created_at, updated_at
FROM runtime_attachments
WHERE id = ? AND created_by = ? AND status = 'pending' AND run_id IS NULL;

-- name: BindAttachmentToRun :exec
UPDATE runtime_attachments SET run_id = ?, conversation_id = ? WHERE id = ?;

-- name: SetAttachmentUploaded :exec
UPDATE runtime_attachments SET external_attachment_id = ?, status = 'uploaded' WHERE id = ?;

-- name: DeleteAttachment :exec
DELETE FROM runtime_attachments WHERE id = ?;

-- name: AppendUserAttachmentToRunInput :execresult
-- Late-attachment race guard: only while queued.
UPDATE runs SET input = JSON_ARRAY_APPEND(input, '$.agent_attachment_ids', ?)
WHERE id = ? AND status = 'queued';
