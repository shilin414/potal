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
-- NOTE (Provider Admission & Release Gate): the claim deliberately does
-- NOT increment attempt — attempt counts PROVIDER EXECUTIONS, not claims.
-- Provider admission requeues (inflight limit / limiter outage) must not
-- burn retry budget; only BeginProviderAttemptFenced consumes an attempt,
-- immediately before the provider submit.
UPDATE runs
SET status = 'running', started_at = CURRENT_TIMESTAMP(3),
    lease_epoch = lease_epoch + 1
WHERE id = ? AND status = 'queued'
  AND (available_at IS NULL OR available_at <= CURRENT_TIMESTAMP(3));

-- name: BeginProviderAttemptFenced :execresult
-- Consumes ONE provider execution attempt, fenced by the current lease
-- epoch. Called by the owner right before the provider submit; claim /
-- admission requeues never touch attempt. attempt < max_attempts is
-- checked under the run row lock in Go (BeginProviderAttemptOwned).
UPDATE runs
SET attempt = attempt + 1
WHERE id = ? AND status = 'running' AND lease_epoch = ?;

-- name: GetRunLeaseEpoch :one
SELECT lease_epoch FROM runs WHERE id = ?;

-- name: CASFinishRunFenced :execresult
-- Fenced terminal transition: only the current lease epoch may finish a
-- running run. 0 rows = already terminal (idempotent) OR lost ownership.
UPDATE runs
SET status = ?, output = ?, provider_status = ?, provider_finish_reason = ?,
    error_code = ?, error_message = ?, finished_at = CURRENT_TIMESTAMP(3)
WHERE id = ? AND status = 'running' AND lease_epoch = ?;

-- name: UpdateRunExternalIDFenced :execresult
-- Set-once semantic guarded in Go (only write when empty) + fence.
UPDATE runs SET external_run_id = ? WHERE id = ? AND external_run_id = '' AND lease_epoch = ?;

-- name: ListQueuedRunIDs :many
-- Provider admission order. Base priority is explicit and waiting time
-- adds a bounded bonus so scheduled/background work cannot starve.
SELECT id FROM runs
WHERE status = 'queued' AND provider = ?
  AND (available_at IS NULL OR available_at <= CURRENT_TIMESTAMP(3))
ORDER BY (
    CASE priority
        WHEN 'interactive_user' THEN 100
        WHEN 'retry' THEN 70
        WHEN 'scheduled_high' THEN 50
        WHEN 'scheduled_normal' THEN 45
        ELSE 20
    END
) + (
    CASE
        WHEN attempt > 0 THEN 25
        ELSE 0
    END + LEAST(TIMESTAMPDIFF(SECOND, queued_at, CURRENT_TIMESTAMP(3)), 40)
) DESC, queued_at, id
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
-- available_at comes from the DB clock plus the caller's retry delay in
-- microseconds. Run.available_at and the outbox row's available_at MUST
-- carry the same retry instant (P1-2: a hardcoded INTERVAL once
-- desynchronized run availability from outbox publishing, so a
-- redispatched run was invisible to the CAS until the fallback scan found
-- it ~20s later) and neither may depend on the app clock, whose skew
-- against TiDB has already caused a wakeup/claimability mismatch (Phase 3).
UPDATE runs
SET status = 'queued', priority = 'retry',
    available_at = DATE_ADD(CURRENT_TIMESTAMP(3), INTERVAL sqlc.arg(retry_delay_micros) MICROSECOND)
WHERE id = ? AND status = 'running' AND lease_epoch = ?;

-- name: RequeueRunFencedImmediate :execresult
-- Reaper recovery requeue: a crashed worker's run becomes claimable AT
-- ONCE — crash recovery must not wait out a retry backoff. available_at
-- comes from the DB clock, exactly like the dispatch outbox row created
-- in the same transaction (CreateOutboxEvent), so Run.available_at ==
-- Outbox.available_at regardless of app/DB clock skew.
UPDATE runs
SET status = 'queued', priority = 'retry', available_at = CURRENT_TIMESTAMP(3)
WHERE id = ? AND status = 'running' AND lease_epoch = ?;

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
-- lease_epoch mirrors runs.lease_epoch at claim time (migration 0010):
-- the reaper verifies an expired lease really belongs to the run's
-- CURRENT epoch before recovering it.
-- expires_at is derived from the DB clock — the lease is acquired,
-- heartbeat and expired under ONE clock authority (Phase 3), never from
-- the worker's local clock.
INSERT INTO run_leases (run_id, worker_id, lease_token, lease_epoch, heartbeat_at, expires_at)
VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP(3),
        DATE_ADD(CURRENT_TIMESTAMP(3), INTERVAL sqlc.arg(lease_micros) MICROSECOND));

-- name: GetExpiredLeaseForUpdate :one
-- Reaper row lock: the lease is re-validated under lock inside the
-- recovery transaction — a heartbeat that lands first wins the row.
SELECT id, run_id, worker_id, lease_token, lease_epoch, acquired_at, heartbeat_at, expires_at
FROM run_leases
WHERE run_id = ? AND expires_at <= CURRENT_TIMESTAMP(3)
FOR UPDATE;

-- name: GetRunForUpdate :one
-- Lock the run row inside an ownership-verified transaction (finalize /
-- retry / recovery). Returns the lease_epoch so the caller can verify
-- the fence under the lock.
SELECT id, user_id, external_run_id, status, lease_epoch, attempt, max_attempts,
       trigger_type, trigger_id, conversation_id
FROM runs WHERE id = ? FOR UPDATE;

-- name: DeleteLeaseByToken :execresult
-- Delete exactly one lease row, identified by token (used inside
-- finalize/retry/recovery transactions; RowsAffected proves ownership).
DELETE FROM run_leases WHERE run_id = ? AND lease_token = ?;

-- name: HeartbeatLease :execresult
-- Lease extension from the DB clock (Phase 3). A negative microsecond delay
-- expires the lease immediately — test fixtures use that instead of passing
-- an absolute app-clock instant.
UPDATE run_leases
SET heartbeat_at = CURRENT_TIMESTAMP(3),
    expires_at = DATE_ADD(CURRENT_TIMESTAMP(3), INTERVAL sqlc.arg(lease_micros) MICROSECOND)
WHERE run_id = ? AND worker_id = ?;

-- name: HeartbeatLeaseFenced :execresult
-- Ownership-checked by lease token (not worker_id): a recycled worker id
-- cannot renew a lease it no longer owns. Extension is DB-clock based, so a
-- skewed worker clock can neither extend nor shorten the lease.
UPDATE run_leases
SET heartbeat_at = CURRENT_TIMESTAMP(3),
    expires_at = DATE_ADD(CURRENT_TIMESTAMP(3), INTERVAL sqlc.arg(lease_micros) MICROSECOND)
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

-- ───────────────────────────────────────── provider execution slots ──
-- Provider Inflight Durable Truth (Admission Fairness & Distributed Lease
-- Hardening, Phase 2): max_inflight is a safety capacity state and lives in
-- TiDB, not in a transient Redis semaphore. Every timestamp decision uses
-- the DB clock; Redis restart/flush can never raise real provider
-- concurrency above the configured limit.

-- name: EnsureProviderAdmissionLock :exec
-- Materializes the per-provider serialization row (seeded by migration
-- 0011; a provider registered later self-heals here).
INSERT INTO provider_admission_locks (provider) VALUES (?)
ON DUPLICATE KEY UPDATE provider = provider;

-- name: LockProviderAdmission :one
-- Acquire holds this row lock for the rest of its transaction, so
-- "delete expired → count active → insert" is atomic on TiDB and MySQL 5.7
-- without table locks. Row missing = error (owner recovers by requeue).
SELECT provider FROM provider_admission_locks WHERE provider = ? FOR UPDATE;

-- name: CurrentDBTime :one
-- Authoritative clock read. State written in the same transaction derives
-- its timestamps from this value or from CURRENT_TIMESTAMP(3) directly —
-- never from the application clock (Phase 3).
SELECT CURRENT_TIMESTAMP(3) AS now;

-- name: CountRunningRunAtEpoch :one
-- Ownership probe for provider slot Acquire: only the CURRENT owner of a
-- running run may hold provider capacity, so a stale worker that wakes up
-- after its run was reclaimed/reaped cannot pollute the semaphore.
SELECT COUNT(*) AS n FROM runs
WHERE id = ? AND status = 'running' AND lease_epoch = ?;

-- name: DeleteExpiredProviderSlots :execresult
-- Crash recovery inside Acquire: a worker that died without releasing must
-- not pin provider capacity beyond its DB-clock lease.
DELETE FROM provider_execution_slots
WHERE provider = ? AND expires_at <= CURRENT_TIMESTAMP(3);

-- name: GetProviderSlotForUpdate :one
-- Idempotent re-acquire: the SAME ownership (run, claim epoch, token)
-- already holds a slot → refresh it instead of consuming a second one.
SELECT id FROM provider_execution_slots
WHERE provider = ? AND run_id = ? AND lease_epoch = ? AND lease_token = ?
FOR UPDATE;

-- name: TouchProviderSlot :execresult
-- Renew (XX-only): the slot must already exist. 0 rows = expired/released →
-- ErrProviderSlotLost; a lost slot is NEVER recreated by a renewal.
UPDATE provider_execution_slots
SET heartbeat_at = CURRENT_TIMESTAMP(3),
    expires_at = DATE_ADD(CURRENT_TIMESTAMP(3), INTERVAL sqlc.arg(lease_micros) MICROSECOND)
WHERE provider = ? AND run_id = ? AND lease_epoch = ? AND lease_token = ?;

-- name: CountActiveProviderSlots :one
-- Active = not past its DB-clock expiry.
SELECT COUNT(*) AS n FROM provider_execution_slots
WHERE provider = ? AND expires_at > CURRENT_TIMESTAMP(3);

-- name: CreateProviderSlot :exec
INSERT INTO provider_execution_slots
    (provider, run_id, lease_epoch, lease_token, worker_id,
     acquired_at, heartbeat_at, expires_at)
VALUES (?, ?, ?, ?, ?,
        CURRENT_TIMESTAMP(3), CURRENT_TIMESTAMP(3),
        DATE_ADD(CURRENT_TIMESTAMP(3), INTERVAL sqlc.arg(lease_micros) MICROSECOND));

-- name: DeleteProviderSlotByToken :execresult
-- Release deletes exactly this ownership's slot: a stale worker's release
-- touches neither the new owner's slot nor any other attempt's.
DELETE FROM provider_execution_slots
WHERE provider = ? AND run_id = ? AND lease_epoch = ? AND lease_token = ?;

-- name: DeleteProviderSlotsUpToEpoch :execresult
-- Ownership-transition cleanup: finalize / retry / reaper delete the run's
-- slots (current and older epochs) in the SAME transaction as the canonical
-- state write, so "run stopped running ⇒ no provider slot" holds.
DELETE FROM provider_execution_slots WHERE run_id = ? AND lease_epoch <= ?;

-- name: DeleteExpiredProviderSlotsAll :execresult
-- Reaper hygiene sweep: expiry is already enforced on read; this only keeps
-- the table small.
DELETE FROM provider_execution_slots WHERE expires_at <= CURRENT_TIMESTAMP(3);

-- name: CountOrphanProviderSlots :one
-- Invariant M: an ACTIVE provider slot must belong to a running run at the
-- matching lease epoch.
SELECT COUNT(*) AS n FROM provider_execution_slots s
LEFT JOIN runs r
  ON r.id = s.run_id AND r.status = 'running' AND r.lease_epoch = s.lease_epoch
WHERE s.expires_at > CURRENT_TIMESTAMP(3) AND r.id IS NULL;

-- ──────────────────────────────────────────────── invariant checks ──
-- Execution invariant checker queries (修复计划 §43-49): detect only,
-- never auto-repair.

-- name: CountRunningWithoutLease :one
-- Invariant A: a running run MUST have a lease row.
SELECT COUNT(*) AS n FROM runs r
LEFT JOIN run_leases l ON l.run_id = r.id
WHERE r.status = 'running' AND l.run_id IS NULL;

-- name: CountQueuedWithLease :one
-- Invariant B: a queued run must NOT hold a lease.
SELECT COUNT(*) AS n FROM runs r
JOIN run_leases l ON l.run_id = r.id
WHERE r.status = 'queued';

-- name: CountTerminalWithLease :one
-- Invariant C: a terminal run must NOT hold a lease.
SELECT COUNT(*) AS n FROM runs r
JOIN run_leases l ON l.run_id = r.id
WHERE r.status IN ('succeeded', 'failed', 'cancelled');

-- name: CountTerminalWithoutTerminalEvent :one
-- Invariant D: a terminal run MUST have exactly one terminal RunEvent.
SELECT COUNT(*) AS n FROM runs r
LEFT JOIN run_events e ON e.run_id = r.id AND e.event_type IN ('run.completed', 'run.failed', 'run.cancelled')
WHERE r.status IN ('succeeded', 'failed', 'cancelled') AND e.id IS NULL;

-- name: CountRunningLeaseEpochMismatch :one
-- Invariant E: a running run's lease_epoch must equal its lease row's.
SELECT COUNT(*) AS n FROM runs r
JOIN run_leases l ON l.run_id = r.id
WHERE r.status = 'running' AND r.lease_epoch != l.lease_epoch;

-- name: CountScheduleOverlapViolations :many
-- Invariant F: overlap=queue schedules must never run in parallel.
-- Returns one row per violating schedule.
SELECT o.schedule_id, COUNT(*) AS n
FROM schedule_occurrences o
JOIN schedules s ON s.id = o.schedule_id
WHERE s.overlap_policy = 'queue' AND o.status IN ('queued', 'running')
GROUP BY o.schedule_id
HAVING COUNT(*) > 1;

-- name: CountScheduledRunOccurrenceMismatch :one
-- Invariant H: a terminal scheduled run and its occurrence must converge.
SELECT COUNT(*) AS n
FROM runs r
LEFT JOIN schedule_occurrences o ON o.run_id = r.id
WHERE r.trigger_type = 'scheduled'
  AND r.status IN ('succeeded', 'failed', 'cancelled')
  AND (o.id IS NULL
       OR (r.status = 'succeeded' AND o.status != 'succeeded')
       OR (r.status IN ('failed', 'cancelled') AND o.status != 'failed'));

-- name: CountMissingDeliveryExecutions :one
-- Invariant L: every enabled target of a succeeded occurrence has a
-- durable delivery execution row. The created_at guard avoids false
-- positives on historical occurrences: a target enabled AFTER the
-- occurrence finished was never supposed to receive it.
SELECT COUNT(*) AS n
FROM schedule_occurrences o
JOIN schedule_deliveries d
  ON d.schedule_id = o.schedule_id AND d.enabled = 1
  AND d.created_at <= COALESCE(o.finished_at, o.updated_at)
LEFT JOIN delivery_executions de
  ON de.occurrence_id = o.id AND de.schedule_delivery_id = d.id
WHERE o.status = 'succeeded' AND de.id IS NULL;

-- name: OldestPendingOutboxAgeSeconds :one
-- Invariant G: the outbox relay must not lag (pending events older than
-- the threshold mean dispatch is stuck).
SELECT CAST(COALESCE(TIMESTAMPDIFF(SECOND, MIN(available_at), CURRENT_TIMESTAMP(3)), 0) AS SIGNED) AS n
FROM outbox_events WHERE status = 'pending';

-- name: GetLease :one
SELECT id, run_id, worker_id, lease_token, acquired_at, heartbeat_at, expires_at
FROM run_leases WHERE run_id = ?;

-- name: CreateOutboxEvent :execresult
INSERT INTO outbox_events (aggregate, aggregate_id, event_type, payload, status, available_at)
VALUES (?, ?, ?, ?, 'pending', CURRENT_TIMESTAMP(3));

-- name: CreateOutboxEventAt :execresult
-- Delayed-publish variant: the row stays invisible to the relay until the
-- DB clock passes the given microsecond delay. RetryOwnedRunAfter passes
-- the SAME delay to RequeueRunFenced, so a retried run is dispatched
-- exactly when it becomes claimable (P1-2) — no earlier, no later — with
-- the DB clock as the single authority for both (Phase 3).
INSERT INTO outbox_events (aggregate, aggregate_id, event_type, payload, status, available_at)
VALUES (?, ?, ?, ?, 'pending', DATE_ADD(CURRENT_TIMESTAMP(3), INTERVAL sqlc.arg(retry_delay_micros) MICROSECOND));

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
