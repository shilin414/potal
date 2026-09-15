-- ─────────────────────────────────────────────────────────── automation ──
-- Schedule / Occurrence / Delivery queries (0006_schedule_automation).

-- name: DBNow :one
-- Clock Authority (评测 §十七): the scheduler resolves "now" from the
-- database so due / misfire / window decisions are identical across
-- scheduler hosts regardless of their local clock skew.
SELECT CURRENT_TIMESTAMP(3) AS now;

-- name: CreateSchedule :execresult
INSERT INTO schedules (owner_user_id, name, description, application_id, input_payload,
    schedule_type, cron_expression, trigger_config, timezone, run_at, enabled,
    conversation_policy, conversation_id, overlap_policy, misfire_policy,
    execution_window_seconds, deadline_policy, next_run_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetScheduleByID :one
SELECT id, owner_user_id, name, description, application_id,
       COALESCE(input_payload, '{}') AS input_payload,
       schedule_type, cron_expression, COALESCE(trigger_config, '{}') AS trigger_config,
       timezone, run_at, enabled,
       conversation_policy, conversation_id, overlap_policy, misfire_policy,
       execution_window_seconds, deadline_policy, next_run_at, last_run_at,
       created_at, updated_at, deleted_at
FROM schedules WHERE id = ?;

-- name: ListSchedulesByOwner :many
-- status: all | running | paused | failed (UI filters). Soft-deleted
-- schedules (deleted_at) never appear.
SELECT s.id, s.owner_user_id, s.name, s.description, s.application_id,
       COALESCE(s.input_payload, '{}') AS input_payload,
       s.schedule_type, s.cron_expression, COALESCE(s.trigger_config, '{}') AS trigger_config,
       s.timezone, s.run_at, s.enabled,
       s.conversation_policy, s.conversation_id, s.overlap_policy, s.misfire_policy,
       s.execution_window_seconds, s.deadline_policy, s.next_run_at, s.last_run_at,
       s.created_at, s.updated_at, s.deleted_at
FROM schedules s
WHERE s.owner_user_id = ?
  AND s.deleted_at IS NULL
  AND CASE
        WHEN sqlc.arg('status') = 'running' THEN s.enabled = 1
        WHEN sqlc.arg('status') = 'paused' THEN s.enabled = 0
        WHEN sqlc.arg('status') = 'failed' THEN EXISTS (
            SELECT 1 FROM schedule_occurrences o
            WHERE o.schedule_id = s.id AND o.status = 'failed'
              AND o.id = (SELECT MAX(o2.id) FROM schedule_occurrences o2 WHERE o2.schedule_id = s.id)
        )
        ELSE TRUE
      END
  AND (sqlc.arg('before_id') = 0 OR s.id < sqlc.arg('before_id'))
ORDER BY s.id DESC
LIMIT ?;

-- name: CountSchedulesByOwner :one
-- Per-user schedule cap (评测 P1-7): only live (non-deleted) schedules
-- count against the quota.
SELECT COUNT(*) AS n FROM schedules WHERE owner_user_id = ? AND deleted_at IS NULL;

-- name: ListLatestOccurrencesForSchedules :many
SELECT o.id, o.schedule_id, o.scheduled_at, o.enqueued_at, o.admitted_at, o.run_id,
       o.status, o.triggered_at, o.finished_at, o.created_at, o.updated_at,
       o.delivery_snapshot_at
FROM schedule_occurrences o
JOIN (
    SELECT x.schedule_id, MAX(x.id) AS max_id
    FROM schedule_occurrences x
    WHERE x.schedule_id IN (sqlc.slice('ids'))
    GROUP BY x.schedule_id
) m ON o.schedule_id = m.schedule_id AND o.id = m.max_id;

-- name: UpdateSchedule :execresult
UPDATE schedules
SET name = ?, description = ?, input_payload = ?, schedule_type = ?, cron_expression = ?,
    trigger_config = ?, timezone = ?, run_at = ?, conversation_policy = ?, conversation_id = ?,
    overlap_policy = ?, misfire_policy = ?, execution_window_seconds = ?, deadline_policy = ?,
    next_run_at = ?
WHERE id = ?;

-- name: SetScheduleEnabled :execresult
UPDATE schedules SET enabled = ? WHERE id = ?;

-- name: TouchScheduleRunTimes :execresult
UPDATE schedules SET last_run_at = ?, next_run_at = ? WHERE id = ?;

-- name: SetScheduleLastRun :execresult
-- run-now bookkeeping: last_run_at moves, next_run_at stays untouched.
UPDATE schedules SET last_run_at = ? WHERE id = ?;

-- name: SetScheduleNextRun :execresult
UPDATE schedules SET next_run_at = ? WHERE id = ?;

-- name: SetScheduleConversation :execresult
-- Lazy binding for conversation_policy=reuse on first run.
UPDATE schedules SET conversation_id = ? WHERE id = ?;

-- name: DeleteSchedule :execresult
-- Soft delete (评测 §十二): pending delivery executions still resolve
-- schedule.Name to build their message; the row must survive. The
-- scheduler scan and owner lists filter deleted_at IS NULL.
UPDATE schedules SET deleted_at = CURRENT_TIMESTAMP(3), enabled = 0 WHERE id = ? AND deleted_at IS NULL;

-- name: ListDueSchedules :many
-- The scheduler scan never picks up disabled or soft-deleted rows.
SELECT id, owner_user_id, name, description, application_id,
       COALESCE(input_payload, '{}') AS input_payload,
       schedule_type, cron_expression, COALESCE(trigger_config, '{}') AS trigger_config,
       timezone, run_at, enabled,
       conversation_policy, conversation_id, overlap_policy, misfire_policy,
       execution_window_seconds, deadline_policy, next_run_at, last_run_at,
       created_at, updated_at, deleted_at
FROM schedules
WHERE enabled = 1 AND deleted_at IS NULL AND next_run_at IS NOT NULL AND next_run_at <= ?
ORDER BY next_run_at
LIMIT ?;

-- ──────────────────────────────────────────────────── schedule_occurrences ──

-- name: CreateScheduleOccurrence :execresult
-- UNIQUE (schedule_id, scheduled_at) is the idempotency barrier: a losing
-- concurrent insert must be detected in Go via duplicate-key error.
INSERT INTO schedule_occurrences (schedule_id, scheduled_at, status)
VALUES (?, ?, 'pending');

-- name: GetScheduleOccurrenceByID :one
SELECT id, schedule_id, scheduled_at, enqueued_at, admitted_at, run_id, status,
       triggered_at, finished_at, created_at, updated_at, delivery_snapshot_at
FROM schedule_occurrences WHERE id = ?;

-- name: GetScheduleOccurrenceBySlot :one
SELECT id, schedule_id, scheduled_at, enqueued_at, admitted_at, run_id, status,
       triggered_at, finished_at, created_at, updated_at, delivery_snapshot_at
FROM schedule_occurrences WHERE schedule_id = ? AND scheduled_at = ?;

-- name: ListOccurrencesBySchedule :many
SELECT id, schedule_id, scheduled_at, enqueued_at, admitted_at, run_id, status,
       triggered_at, finished_at, created_at, updated_at, delivery_snapshot_at
FROM schedule_occurrences
WHERE schedule_id = ? AND (sqlc.arg('before_id') = 0 OR id < sqlc.arg('before_id'))
ORDER BY id DESC
LIMIT ?;

-- name: LatestOccurrenceBySchedule :one
SELECT id, schedule_id, scheduled_at, enqueued_at, admitted_at, run_id, status,
       triggered_at, finished_at, created_at, updated_at, delivery_snapshot_at
FROM schedule_occurrences WHERE schedule_id = ?
ORDER BY id DESC
LIMIT 1;

-- name: MarkOccurrenceQueued :execresult
UPDATE schedule_occurrences
SET status = 'queued', enqueued_at = CURRENT_TIMESTAMP(3), run_id = ?, admitted_at = ?
WHERE id = ? AND status = 'pending';

-- name: MarkOccurrenceStatus :execresult
UPDATE schedule_occurrences SET status = ? WHERE id = ?;

-- name: MarkOccurrenceRunningByRun :execresult
-- Run claim fan-out: the occurrence linked to this run enters 'running'.
UPDATE schedule_occurrences
SET status = 'running', triggered_at = CURRENT_TIMESTAMP(3)
WHERE run_id = ? AND status = 'queued';

-- name: CASFinishOccurrenceByRun :execresult
-- Run terminal fan-out: occurrence converges with the run's outcome
-- (the delivery layer stays independent — see delivery_executions).
UPDATE schedule_occurrences
SET status = ?, finished_at = CURRENT_TIMESTAMP(3)
WHERE run_id = ? AND status IN ('queued', 'running');

-- name: HasActiveOccurrence :one
SELECT COUNT(*) AS n FROM schedule_occurrences
WHERE schedule_id = ? AND status IN ('pending', 'queued', 'running');

-- name: CountPendingOccurrences :one
-- Run-now pending cap (复审 P1-3): pending occurrences are future work no
-- outstanding-run limit sees, so the manual queue must be bounded.
-- Counted under the schedules row lock by the caller.
SELECT COUNT(*) AS n FROM schedule_occurrences
WHERE schedule_id = ? AND status = 'pending';

-- name: CountActiveOccurrencesExcluding :one
-- Admission check for a pending occurrence: does anything OTHER than
-- itself still hold the schedule's execution slot (queued/running)?
-- A pending row does not block its own admission.
SELECT COUNT(*) AS n FROM schedule_occurrences
WHERE schedule_id = ? AND id != ? AND status IN ('queued', 'running');

-- name: ListAdmissiblePendingOccurrences :many
-- Occurrence admission queue (overlap=queue semantics): pending rows are
-- converted into runs once the schedule has no active execution.
-- FIFO per schedule: scheduled_at first, id as the tiebreaker.
SELECT id, schedule_id, scheduled_at, enqueued_at, admitted_at, run_id, status,
       triggered_at, finished_at, created_at, updated_at, delivery_snapshot_at
FROM schedule_occurrences
WHERE status = 'pending'
ORDER BY scheduled_at, id
LIMIT ?;

-- name: GetScheduleRowForUpdate :one
-- Admission lock (修复计划 §35, 评测 P1-1/P1-2): serializes concurrent
-- admissions for the same schedule so two schedulers can never both
-- observe "no active occurrence" and create parallel runs (write-skew
-- guard). Now returns the FULL row: the admission path re-reads the
-- current schedule under the lock instead of trusting the scan-time
-- snapshot (disable / prompt edits become visible before the run is
-- created).
SELECT id, owner_user_id, name, description, application_id,
       COALESCE(input_payload, '{}') AS input_payload,
       schedule_type, cron_expression, COALESCE(trigger_config, '{}') AS trigger_config,
       timezone, run_at, enabled,
       conversation_policy, conversation_id, overlap_policy, misfire_policy,
       execution_window_seconds, deadline_policy, next_run_at, last_run_at,
       created_at, updated_at, deleted_at
FROM schedules WHERE id = ? AND deleted_at IS NULL FOR UPDATE;

-- name: CountSkippedOccurrencesForSlot :one
SELECT COUNT(*) AS n FROM schedule_occurrences
WHERE schedule_id = ? AND scheduled_at = ? AND status = 'skipped';

-- name: ListStuckPendingOccurrences :many
-- Occurrences stuck in pending longer than the grace period: their
-- creating scheduler died between INSERT and the run-creating commit.
SELECT id, schedule_id, scheduled_at, enqueued_at, admitted_at, run_id, status,
       triggered_at, finished_at, created_at, updated_at
FROM schedule_occurrences
WHERE status = 'pending' AND created_at < ?
ORDER BY id
LIMIT ?;

-- ─────────────────────────────────────────────────── schedule_deliveries ──

-- name: CaptureOccurrenceDeliveryExpectations :exec
-- Freeze the enabled delivery policy exactly once. The NULL marker makes
-- repeated calls safe and prevents a later schedule edit from adding new
-- expectations to an already-created occurrence.
INSERT INTO occurrence_delivery_expectations
    (occurrence_id, schedule_delivery_id, channel, sender_identity_mode,
     target_type, target_id, target_name, content_mode)
SELECT o.id, d.id, d.channel, d.sender_identity_mode,
       d.target_type, d.target_id, d.target_name, d.content_mode
FROM schedule_occurrences o
JOIN schedule_deliveries d
  ON d.schedule_id = o.schedule_id AND d.enabled = 1
WHERE o.id = ? AND o.delivery_snapshot_at IS NULL
ON DUPLICATE KEY UPDATE occurrence_id = VALUES(occurrence_id);

-- name: MarkOccurrenceDeliverySnapshotCaptured :execresult
UPDATE schedule_occurrences
SET delivery_snapshot_at = CURRENT_TIMESTAMP(3)
WHERE id = ? AND delivery_snapshot_at IS NULL;

-- name: ListOccurrenceDeliveryExpectations :many
SELECT occurrence_id, schedule_delivery_id, channel, sender_identity_mode,
       target_type, target_id, target_name, content_mode, created_at
FROM occurrence_delivery_expectations
WHERE occurrence_id = ?
ORDER BY schedule_delivery_id;

-- name: UpsertScheduleDelivery :execresult
INSERT INTO schedule_deliveries (schedule_id, channel, sender_identity_mode, target_type,
    target_id, target_name, content_mode, enabled)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
    target_name = VALUES(target_name), content_mode = VALUES(content_mode),
    enabled = VALUES(enabled), updated_at = CURRENT_TIMESTAMP(3);

-- name: ListDeliveriesBySchedule :many
SELECT id, schedule_id, channel, sender_identity_mode, target_type, target_id,
       target_name, content_mode, enabled, created_at, updated_at
FROM schedule_deliveries WHERE schedule_id = ?
ORDER BY id;

-- name: ListEnabledDeliveriesBySchedule :many
SELECT id, schedule_id, channel, sender_identity_mode, target_type, target_id,
       target_name, content_mode, enabled, created_at, updated_at
FROM schedule_deliveries WHERE schedule_id = ? AND enabled = 1;

-- name: DeleteScheduleDeliveries :exec
DELETE FROM schedule_deliveries WHERE schedule_id = ?;

-- name: GetScheduleDeliveryByID :one
SELECT id, schedule_id, channel, sender_identity_mode, target_type, target_id,
       target_name, content_mode, enabled, created_at, updated_at
FROM schedule_deliveries WHERE id = ?;

-- ───────────────────────────────────────────────────── delivery_executions ──

-- name: CreateDeliveryExecution :execresult
-- UNIQUE (occurrence_id, schedule_delivery_id) absorbs at-least-once
-- fan-out: duplicate inserts lose and are ignored in Go.
INSERT IGNORE INTO delivery_executions (id, occurrence_id, run_id, schedule_delivery_id,
    sender_user_id, target_type, target_id, status)
VALUES (?, ?, ?, ?, ?, ?, ?, 'pending');

-- name: GetDeliveryExecutionByID :one
SELECT id, occurrence_id, run_id, schedule_delivery_id, sender_user_id, target_type,
       target_id, status, external_message_id, attempt, max_attempts, next_attempt_at,
       error_code, error_message, created_at, sent_at, updated_at
FROM delivery_executions WHERE id = ?;

-- name: ListDeliveryExecutionsByOccurrence :many
SELECT id, occurrence_id, run_id, schedule_delivery_id, sender_user_id, target_type,
       target_id, status, external_message_id, attempt, max_attempts, next_attempt_at,
       error_code, error_message, created_at, sent_at, updated_at
FROM delivery_executions WHERE occurrence_id = ?
ORDER BY id;

-- name: ListDeliveryExecutionsByRun :many
SELECT id, occurrence_id, run_id, schedule_delivery_id, sender_user_id, target_type,
       target_id, status, external_message_id, attempt, max_attempts, next_attempt_at,
       error_code, error_message, created_at, sent_at, updated_at
FROM delivery_executions WHERE run_id = ?
ORDER BY id;

-- name: ListDeliveryExecutionsByOccurrences :many
-- Batch fetch for the occurrences page (delivery results per slot).
SELECT id, occurrence_id, run_id, schedule_delivery_id, sender_user_id, target_type,
       target_id, status, external_message_id, attempt, max_attempts, next_attempt_at,
       error_code, error_message, created_at, sent_at, updated_at
FROM delivery_executions
WHERE occurrence_id IN (sqlc.slice('ids'))
ORDER BY id;

-- name: CASClaimDelivery :execresult
-- CAS claim: one delivery worker wins; 0 rows = someone else got it.
-- Only pending→sending: a duplicate stream message can never re-claim a
-- row another worker is already sending (no double Feishu messages).
UPDATE delivery_executions
SET status = 'sending', attempt = attempt + 1
WHERE id = ? AND status = 'pending';

-- name: CASFinishDelivery :execresult
UPDATE delivery_executions
SET status = ?, external_message_id = ?, error_code = ?, error_message = ?,
    sent_at = IF(? = 'succeeded', CURRENT_TIMESTAMP(3), sent_at)
WHERE id = ? AND status = 'sending';

-- name: RequeueDelivery :execresult
-- Retry time = DB clock + the worker's backoff in microseconds (Clock
-- Authority, Phase 3): the due-scan below compares against the DB clock, so
-- a skewed worker clock can neither delay nor rush a delivery retry.
UPDATE delivery_executions
SET status = 'pending',
    next_attempt_at = DATE_ADD(CURRENT_TIMESTAMP(3), INTERVAL sqlc.arg(backoff_micros) MICROSECOND),
    error_code = ?, error_message = ?
WHERE id = ? AND status = 'sending';

-- name: ListDueDeliveries :many
-- "Due" is decided by the DB clock, not by the caller's clock.
SELECT id, occurrence_id, run_id, schedule_delivery_id, sender_user_id, target_type,
       target_id, status, external_message_id, attempt, max_attempts, next_attempt_at,
       error_code, error_message, created_at, sent_at, updated_at
FROM delivery_executions
WHERE status = 'pending'
  AND (next_attempt_at IS NULL OR next_attempt_at <= CURRENT_TIMESTAMP(3))
ORDER BY created_at
LIMIT ?;

-- name: ReclaimStuckDeliveries :execresult
-- Crash recovery: rows stuck in 'sending' (worker died before ACK/commit)
-- return to pending when their DB-clock lease lapsed.
UPDATE delivery_executions
SET status = 'pending', next_attempt_at = CURRENT_TIMESTAMP(3)
WHERE status = 'sending'
  AND updated_at < DATE_SUB(CURRENT_TIMESTAMP(3), INTERVAL sqlc.arg(lease_micros) MICROSECOND)
  AND attempt < max_attempts;

-- name: FailStuckDeliveries :execresult
UPDATE delivery_executions
SET status = 'failed', error_code = 'lease_expired', error_message = ?
WHERE status = 'sending'
  AND updated_at < DATE_SUB(CURRENT_TIMESTAMP(3), INTERVAL sqlc.arg(lease_micros) MICROSECOND)
  AND attempt >= max_attempts;
