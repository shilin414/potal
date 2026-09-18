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
       trigger_type, trigger_id, priority, available_at, lease_epoch, next_event_sequence
FROM runs WHERE id = ?;

-- name: ListRunsByConversation :many
SELECT id, user_id, application_id, conversation_id, runtime_binding_id, organization_id,
       provider, runtime_type, external_run_id, status, provider_status, provider_finish_reason,
       input, output, runtime_snapshot, attempt, max_attempts, queued_at, started_at,
       finished_at, error_code, error_message, created_at, updated_at,
       trigger_type, trigger_id, priority, available_at, lease_epoch, next_event_sequence
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
--
-- NOTE (第四轮 P2): the claim does NOT stamp started_at either. started_at
-- means "this run was allowed to execute", not "a worker touched it" — a
-- run killed by the execution gate must not carry a start time it never
-- earned. The owner stamps it with MarkRunStartedFenced after the gate
-- allows the run (same point as the run.started event).
UPDATE runs
SET status = 'running',
    lease_epoch = lease_epoch + 1
WHERE id = ? AND status = 'queued'
  AND (available_at IS NULL OR available_at <= CURRENT_TIMESTAMP(3));

-- name: MarkRunStartedFenced :execresult
-- Stamps started_at once, fenced by the current lease epoch, at the point
-- the run is actually allowed to execute (第四轮 P2). COALESCE keeps the
-- FIRST start time: a run deferred for a paused provider and later
-- re-claimed keeps its original start, so RunDuration measures wall-clock
-- execution rather than the last requeue.
UPDATE runs
SET started_at = COALESCE(started_at, CURRENT_TIMESTAMP(3))
WHERE id = ? AND status = 'running' AND lease_epoch = ?;

-- name: GetRunStartedAt :one
-- Reads back the canonical started_at the UPDATE above just wrote
-- (第五轮 P2-4). The caller needs the DATABASE's timestamp, not a
-- locally generated one: started_at is already DB-clock authoritative and
-- COALESCE may have kept an earlier value, so the only correct source is
-- the row. It lets the worker refresh its in-memory Run snapshot so
-- FinalizeOwnedRun can observe studio_run_duration.
SELECT started_at FROM runs
WHERE id = ? AND status = 'running' AND lease_epoch = ?;

-- name: GetRunTimestamps :one
-- Duration-metric observation ONLY (第七轮 P2-1). Called AFTER the finalize
-- transaction commits, never inside it: a metrics read is a post-commit side
-- effect, so a failure here must not be able to roll back the terminal
-- transition (that would put a finished run back to running and drop its
-- terminal event). Terminal rows are immutable, so reading them later is
-- safe. Both bounds stay on the DB clock — the pair is only meaningful
-- because MySQL wrote both.
SELECT started_at, finished_at
FROM runs
WHERE id = ?;

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

-- name: GetRunGateState :one
-- Execution-time kill switch: mutable application/binding/provider state plus
-- the current enterprise ACL. ACL denial intentionally produces no row; the
-- catalog service maps only sql.ErrNoRows to GateKill, while real DB failures
-- remain retryable infrastructure errors.
SELECT a.enabled AS app_enabled,
       b.enabled AS binding_enabled,
       p.status  AS provider_status
FROM runs r
LEFT JOIN applications a ON a.id = r.application_id
LEFT JOIN runtime_bindings b ON b.id = r.runtime_binding_id
LEFT JOIN providers p ON p.provider_key = b.provider_key
LEFT JOIN users gate_user ON gate_user.id = r.user_id
WHERE r.id = ?
  AND COALESCE(gate_user.is_active, 0) = 1
  AND (sqlc.arg('acl_enabled') = 0 OR COALESCE(gate_user.is_staff, 0) = 1 OR EXISTS (
    SELECT 1 FROM directory_users acl_du
    WHERE acl_du.local_user_id = r.user_id
      AND acl_du.is_active = 1 AND acl_du.is_resigned = 0 AND acl_du.active_status = 2
      AND (a.access_mode = 'all' OR (a.access_mode = 'assigned' AND (
        EXISTS (SELECT 1 FROM application_user_grants acl_ug
                WHERE acl_ug.application_id=a.id AND acl_ug.directory_user_id=acl_du.id)
        OR EXISTS (SELECT 1 FROM directory_user_departments acl_dud
                   JOIN directory_department_closure acl_dc ON acl_dc.descendant_id=acl_dud.department_id
                   JOIN application_department_grants acl_dg ON acl_dg.department_id=acl_dc.ancestor_id
                    AND (acl_dg.include_children=1 OR acl_dc.depth=0)
                   WHERE acl_dud.directory_user_id=acl_du.id AND acl_dg.application_id=a.id)
      )))
  ));

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
-- `interrupted` counts as settled too (第五轮 P2-1): a legacy pre-closure
-- row must never be re-finished, same as the three canonical statuses.
UPDATE runs
SET status = ?, output = ?, provider_status = ?, provider_finish_reason = ?,
    error_code = ?, error_message = ?, finished_at = CURRENT_TIMESTAMP(3)
WHERE id = ? AND status NOT IN ('cancelled', 'succeeded', 'failed', 'interrupted');

-- name: RequeueRun :exec
UPDATE runs SET status = 'queued' WHERE id = ? AND status = 'running';

-- name: RequeueRunFenced :execresult
-- available_at comes from the DB clock plus the caller's retry delay in
-- microseconds. Run.available_at and the outbox row's available_at MUST
-- carry the same retry instant (P1-2: a hardcoded INTERVAL once
-- desynchronized run availability from outbox publishing, so a
-- redispatched run was invisible to the CAS until the fallback scan found
-- it ~20s later) and neither may depend on the app clock, whose skew
-- against the database has already caused a wakeup/claimability mismatch (Phase 3).
UPDATE runs
SET status = 'queued', priority = 'retry',
    available_at = DATE_ADD(CURRENT_TIMESTAMP(3), INTERVAL sqlc.arg(retry_delay_micros) MICROSECOND)
WHERE id = ? AND status = 'running' AND lease_epoch = ?;

-- name: DeferRunFenced :execresult
-- Defer (第三轮 P1-C): a gate PAUSE requeues the run WITHOUT demoting its
-- business priority. Unlike RequeueRunFenced this deliberately does NOT
-- touch `priority` — an interactive_user / scheduled_high run paused by an
-- inactive provider keeps its original admission class, so fairness is
-- restored intact when the provider comes back. Pause is "delayed
-- execution", not a provider-failure retry; attempt is not consumed
-- either (the caller never reached BeginProviderAttempt on this path).
UPDATE runs
SET status = 'queued',
    available_at = DATE_ADD(CURRENT_TIMESTAMP(3), INTERVAL sqlc.arg(defer_delay_micros) MICROSECOND)
WHERE id = ? AND status = 'running' AND lease_epoch = ?;

-- name: DeferScheduledOccurrence :execresult
-- Occurrence state convergence (第三轮 §12): a requeued scheduled run must
-- not leave its occurrence in 'running' — the run is back in the queue,
-- so the occurrence goes back to 'queued' in the SAME transaction. Both
-- are active states, so overlap accounting is unaffected; this only keeps
-- the UI and the Run/Occurrence state invariant honest.
UPDATE schedule_occurrences
SET status = 'queued'
WHERE run_id = ? AND status = 'running';

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
-- The ONE event writer. The sequence is ALWAYS supplied by the caller,
-- which obtained it from AllocRunEventSequence* under the run row lock it
-- already holds (第六轮 P2-1, 第九轮 P1-3). No writer may ask the database
-- for "the next value" here: a COUNT(*)+1 computed at INSERT time would be
-- both O(history) and a second source of truth. A mismatch between the
-- allocated and the stored sequence can only mean a lost fence and fails
-- loudly on uniq_run_event_sequence instead of silently renumbering.
INSERT INTO run_events (run_id, sequence, event_type, payload)
VALUES (?, ?, ?, ?);

-- name: AllocRunEventSequence :one
-- System-plane allocation: takes the run row lock (serializing every event
-- writer for this run) and hands back the next sequence. O(1) — the old
-- COUNT(*)+1 grew with the run's event history and got slower exactly when
-- a long streaming answer was producing the most events.
--
-- The read is a locking read on purpose: it IS the serialization point, so
-- "read the counter, then INSERT" cannot interleave with another writer.
SELECT next_event_sequence FROM runs WHERE id = ? FOR UPDATE;

-- name: AllocRunEventSequenceFenced :one
-- Worker-owned allocation: the same locking read plus the lease fence, so
-- a stale worker (lease reclaimed elsewhere) is rejected before any write
-- instead of appending to a run it no longer owns.
SELECT next_event_sequence FROM runs
WHERE id = ? AND status = 'running' AND lease_epoch = ?
FOR UPDATE;

-- name: BumpRunEventSequence :execresult
-- Claims the sequence read above. Written as `x + 1` rather than a computed
-- literal because MySQL reports CHANGED rows: assigning the already-read
-- value would report 0 and be indistinguishable from a missing run row.
UPDATE runs SET next_event_sequence = next_event_sequence + 1 WHERE id = ?;

-- name: ListRunEventsAfter :many
-- Keyset pagination over (run_id, sequence) — the existing UNIQUE index is
-- exactly the right shape, so no OFFSET is ever needed. The LIMIT is
-- mandatory for long histories (第九轮 P1-3): an unbounded read of a run
-- with 100k events would materialize the whole log in one query, both for
-- the HTTP replay endpoint and for the SSE gateway's initial replay.
SELECT id, run_id, sequence, event_type, payload, created_at
FROM run_events WHERE run_id = ? AND sequence > ?
ORDER BY sequence
LIMIT ?;

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

-- name: GetActiveLeaseForUpdate :one
-- Worker-owned mutations lock and validate the exact live lease after
-- locking the run row. Expired ownership cannot be revived or used in the
-- interval before the reaper observes it.
SELECT id, run_id, worker_id, lease_token, lease_epoch, acquired_at, heartbeat_at, expires_at
FROM run_leases
WHERE run_id = ? AND lease_epoch = ? AND lease_token = ?
  AND expires_at > CURRENT_TIMESTAMP(3)
FOR UPDATE;

-- name: GetRunForUpdate :one
-- Lock the run row inside an ownership-verified transaction (finalize /
-- retry / recovery). Returns the lease_epoch so the caller can verify
-- the fence under the lock, plus the DB-clock started_at/finished_at so
-- the finalize path can observe studio_run_duration from ONE clock
-- authority (第六轮 P2: started_at is written by MySQL, so measuring it
-- against the worker host clock skews or even negates the duration).
SELECT id, user_id, external_run_id, status, lease_epoch, attempt, max_attempts,
       trigger_type, trigger_id, conversation_id, started_at, finished_at
FROM runs WHERE id = ? FOR UPDATE;

-- name: DeleteLeaseByToken :execresult
-- Delete exactly one lease row, identified by token (used inside
-- finalize/retry/recovery transactions; RowsAffected proves ownership).
DELETE FROM run_leases WHERE run_id = ? AND lease_token = ?;

-- name: HeartbeatLease :execresult
-- Lease extension from the DB clock (Phase 3). A negative microsecond delay
-- expires the lease immediately — test fixtures use that instead of passing
-- an absolute app-clock instant.
--
-- heartbeat_at is written MONOTONICALLY on purpose (第七轮 CI 复盘): MySQL
-- reports CHANGED rows, not matched ones, so a renewal that lands in the same
-- millisecond as the previous write computes the very same values and reports
-- 0 — indistinguishable from "this lease does not exist". Callers read 0 as
-- lost ownership, so a perfectly healthy renewal would raise a false
-- ownership-loss signal (observed in CI on provider slot renew). Writing
-- GREATEST(now, stored + 1ms) always differs from the stored value, so
-- changed rows == matched rows again. Same idea as
-- provider_admission_locks.admissions, which increments instead of assigning,
-- for exactly this reason.
--
-- 第八轮 P3 (documentation only): heartbeat_at is an OBSERVATIONAL column and
-- does NOT participate in fencing — `expires_at` does, and it is always
-- recomputed as CURRENT_TIMESTAMP + lease, never accumulated. Under repeated
-- renewals inside one frozen DB millisecond (only reachable with a pinned
-- session clock; real heartbeat periods are far larger than 1ms) the stored
-- value can therefore drift a few ms ahead of DB now, one renewal at a time,
-- rather than staying within a strict 1ms bound. That is harmless: nothing
-- reads heartbeat_at for a correctness decision, and no caller may start.
UPDATE run_leases
SET heartbeat_at = GREATEST(CURRENT_TIMESTAMP(3), DATE_ADD(heartbeat_at, INTERVAL 1000 MICROSECOND)),
    expires_at = DATE_ADD(CURRENT_TIMESTAMP(3), INTERVAL sqlc.arg(lease_micros) MICROSECOND)
WHERE run_id = ? AND worker_id = ?;

-- name: HeartbeatLeaseFenced :execresult
-- Ownership-checked by lease token (not worker_id): a recycled worker id
-- cannot renew a lease it no longer owns. Extension is DB-clock based, so a
-- skewed worker clock can neither extend nor shorten the lease. An expired
-- lease is terminal ownership loss and cannot be revived before the reaper.
--
-- heartbeat_at is written monotonically for the reason spelled out on
-- HeartbeatLease: a same-millisecond renewal must still report 1 changed row,
-- or the caller reads "0" as lost ownership and abandons a healthy run.
UPDATE run_leases
SET heartbeat_at = GREATEST(CURRENT_TIMESTAMP(3), DATE_ADD(heartbeat_at, INTERVAL 1000 MICROSECOND)),
    expires_at = DATE_ADD(CURRENT_TIMESTAMP(3), INTERVAL sqlc.arg(lease_micros) MICROSECOND)
WHERE run_id = ? AND lease_token = ?
  AND expires_at > CURRENT_TIMESTAMP(3);

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
ORDER BY expires_at, run_id
LIMIT ?;

-- ───────────────────────────────────────── provider execution slots ──
-- Provider Inflight Durable Truth (Admission Fairness & Distributed Lease
-- Hardening, Phase 2): max_inflight is a safety capacity state and lives in
-- MySQL, not in a transient Redis semaphore. Every timestamp decision uses
-- the DB clock; Redis restart/flush can never raise real provider
-- concurrency above the configured limit.

-- name: EnsureProviderAdmissionLock :exec
-- Materializes the per-provider serialization row (seeded by migration
-- 0011; a provider registered later self-heals here).
INSERT INTO provider_admission_locks (provider) VALUES (?)
ON DUPLICATE KEY UPDATE provider = provider;

-- name: LockProviderAdmission :execresult
-- Serialize the admission decision per provider with a CONFLICTING WRITE on
-- the shared row (migration 0013). A locking read is not enough: SELECT ...
-- FOR UPDATE alone does not stop a concurrent decision from observing the same
-- pre-insert depth, so every contender counts zero active slots (observed in
-- CI as admitted=8/8 on a fresh provider). Writing this row forces a real
-- conflict, so "delete expired → count active → insert" stays atomic; a loser
-- that still fails with 1213 (deadlock) or 1205 (lock wait timeout) is retried
-- with a fresh snapshot. Affected rows must be 1; 0 means the row is missing
-- and the caller fails closed (the next Acquire re-materializes it).
UPDATE provider_admission_locks
SET admissions = admissions + 1
WHERE provider = ?;

-- name: CurrentDBTime :one
-- Authoritative clock read. State written in the same transaction derives
-- its timestamps from this value or from CURRENT_TIMESTAMP(3) directly —
-- never from the application clock (Phase 3).
SELECT CURRENT_TIMESTAMP(3) AS now;

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
--
-- heartbeat_at is written monotonically for the reason spelled out on
-- HeartbeatLease. This is the statement that surfaced the hazard: a renewal
-- right after Acquire landed in the same millisecond, reported 0 changed rows
-- for a slot that existed and was owned, and made Renew return
-- ErrProviderSlotLost on CI (local runs never hit it — the LAN round trip
-- always crossed a millisecond boundary, while a localhost MySQL does not).
--
-- 第八轮 P1: because 0 now means "the row really is gone" (not "unchanged"),
-- it is strong enough to act on — the merged heartbeat rolls the run-lease
-- renewal back with it and the worker self-fences, instead of continuing to
-- run provider calls that no longer count against max_inflight.
UPDATE provider_execution_slots
SET heartbeat_at = GREATEST(CURRENT_TIMESTAMP(3), DATE_ADD(heartbeat_at, INTERVAL 1000 MICROSECOND)),
    expires_at = DATE_ADD(CURRENT_TIMESTAMP(3), INTERVAL sqlc.arg(lease_micros) MICROSECOND)
WHERE provider = ? AND run_id = ? AND lease_epoch = ? AND lease_token = ?
  AND expires_at > CURRENT_TIMESTAMP(3);

-- name: CountActiveProviderSlots :one
-- Active = not past its DB-clock expiry.
--
-- This is the CONTROLLED depth: slots a live worker currently owns. It is
-- kept with its original meaning (diagnostics, orphan detection, the
-- controlled leg of capacity metrics) and must NOT be used on its own as the
-- admission bound — a provider execution can outlive its slot (worker crash
-- after accept, waiting_external after an unknown submit). Admission uses
-- CountProviderEffectiveInflight* below.
SELECT COUNT(*) AS n FROM provider_execution_slots
WHERE provider = ? AND expires_at > CURRENT_TIMESTAMP(3);

-- name: CountProviderEffectiveInflight :one
-- Provider EFFECTIVE inflight (第九轮补丁 3.3-A): the conservative count of
-- real provider executions the admission plane must assume exist.
--
-- DISTINCT over two sources:
--
--   controlled — live (non-expired, DB-clock) provider slots;
--   unresolved — non-settled runs whose provider submission is
--                sending / unknown / accepted, i.e. a provider call that was
--                transmitted and whose external action may still exist.
--
-- UNION (never UNION ALL) is load-bearing: during NORMAL execution the same
-- run owns a slot AND has a sending/accepted submission, so the two legs are
-- one execution. Adding the counts would double-count it.
--
-- rejected is deliberately excluded: the provider definitively refused, so
-- no external action exists and counting it would leak capacity on every
-- 400/401/403/429. Settled runs are excluded too: provider_submissions is
-- history and keeps state='accepted' after the run succeeds, so without the
-- runs.status filter every completed run would pin a slot forever.
--
-- The unresolved leg is not unbounded: ExpireParkedExternalRuns settles a
-- waiting_external run after the configured unresolved-execution grace (~10
-- min by default), which releases its reservation. See the ProviderSlots
-- doc comment for the exact wording of the guarantee.
--
-- DRIVING SIDE (第九轮补丁 3.3.1-A). The remote leg is written runs-first and
-- pinned with STRAIGHT_JOIN on purpose:
--
--     runs (active)                          ← driving side, bounded
--       → provider_submissions (run_id PK)   ← one lookup per active run
--
-- provider_submissions is HISTORY: a submission row is never deleted when its
-- run settles, so `provider = ? AND state = 'accepted'` selects every provider
-- execution since the table was created, and a history-driven plan pays for
-- all of it on every claim (admission runs on EVERY Acquire, not in a report).
-- Migration 0024 removed the full scan; it did not make the candidate RANGE
-- bounded — that is what this rewrite does. The predicate is unchanged, so the
-- capacity semantics are unchanged, only the traversal direction is.
--
-- `r.status IN (...)` is the explicit complement of IsSettled()
-- (cancelled/succeeded/failed/interrupted) and is spelled as an IN list, not
-- as NOT IN, because `status` leads idx_runs_claim(status, provider,
-- queued_at): a NOT IN cannot use that index range and would push the plan
-- back onto a scan. The five statuses are pinned against the status enum by
-- TestProviderCapacityStatusListIsExactComplementOfSettled.
--
-- STRAIGHT_JOIN (not merely stating the FROM order) is load-bearing: the
-- optimizer is free to reorder an inner join, and if it decides the
-- ('sending','unknown','accepted') range is more selective it will drive from
-- the ledger again. Both MySQL 5.7 and TiDB honour STRAIGHT_JOIN for exactly
-- this purpose. FORCE INDEX is deliberately NOT used: whether
-- idx_runs_claim is chosen should be proven by EXPLAIN first
-- (TestProviderCapacityQueryIsDrivenByActiveRuns).
SELECT COUNT(*) AS n
FROM (
    SELECT s.run_id
    FROM provider_execution_slots s
    WHERE s.provider = ?
      AND s.expires_at > CURRENT_TIMESTAMP(3)

    UNION

    SELECT r.id AS run_id
    FROM runs r
    STRAIGHT_JOIN provider_submissions ps
      ON ps.run_id = r.id
     AND ps.provider = r.provider
    WHERE r.provider = ?
      AND r.status IN (
          'queued',
          'running',
          'waiting_input',
          'waiting_external',
          'cancelling'
      )
      AND ps.state IN ('sending', 'unknown', 'accepted')
) capacity_runs;

-- name: CountProviderEffectiveInflightExcludingRun :one
-- Same as CountProviderEffectiveInflight, minus one run — the run whose own
-- admission is being decided.
--
-- Admission MUST exclude itself. A run whose submission is unresolved and
-- whose slot expired (worker crash, or a reaper requeue) is still counted in
-- the effective depth, so a global count would make it reject ITSELF:
--
--     max_inflight = 1
--     Run A: submission=sending, slot expired, reaper requeued it
--     new worker claims A → Acquire(A) → effective=1 → 1 >= max → rejected
--
-- A can then never re-enter the executor, and the queue entry that would
-- have parked it in waiting_external is never consumed — a self-deadlock
-- that no lease or timeout can break. Excluding self means a run is counted
-- exactly once: either by its own remote reservation (before this acquired
-- decision) or by the slot it is about to be granted, never twice and never
-- zero.
--
-- Same runs-first / STRAIGHT_JOIN driving side as CountProviderEffectiveInflight
-- (第九轮补丁 3.3.1-A); only the self-exclusion is added, on BOTH legs.
--
-- The remote-leg exclusion is spelled `ps.run_id <> ?` rather than `r.id <> ?`
-- so the generated parameter keeps its existing name (RunID_2) and the
-- admission call site does not have to change: the join is
-- `ps.run_id = r.id`, and SQL equality propagation makes the two spellings the
-- same predicate with the same index access.
SELECT COUNT(*) AS n
FROM (
    SELECT s.run_id
    FROM provider_execution_slots s
    WHERE s.provider = ?
      AND s.expires_at > CURRENT_TIMESTAMP(3)
      AND s.run_id <> ?

    UNION

    SELECT r.id AS run_id
    FROM runs r
    STRAIGHT_JOIN provider_submissions ps
      ON ps.run_id = r.id
     AND ps.provider = r.provider
    WHERE r.provider = ?
      AND ps.run_id <> ?
      AND r.status IN (
          'queued',
          'running',
          'waiting_input',
          'waiting_external',
          'cancelling'
      )
      AND ps.state IN ('sending', 'unknown', 'accepted')
) capacity_runs;

-- name: CountProviderUncontrolledInflight :one
-- UNCONTROLLED depth (diagnostics only, never an admission bound): provider
-- executions that may still be running while no live worker owns a slot for
-- them.
--
--     submission IN (sending, unknown, accepted)
--     AND run non-settled
--     AND no live provider_execution_slots row for that run
--
-- Healthy operation keeps this at ~0 (effective ≈ controlled). A SUSTAINED
-- non-zero value means one of:
--
--     worker crash between accept and release
--     waiting_external accumulation (unknown submit outcome)
--     acceptance-persistence failure (ErrProviderAcceptancePersistence)
--     provider reconciliation backlog
--
-- It is the leading indicator for the alert in the 3.3 metrics section: the
-- effective count stays correct, but the share of capacity that no worker
-- controls is growing.
--
-- Same runs-first / STRAIGHT_JOIN driving side as CountProviderEffectiveInflight
-- (第九轮补丁 3.3.1-A): this is a periodic metrics read, so a history-driven
-- plan here would regress scrape latency as the ledger grows.
SELECT COUNT(DISTINCT r.id)
FROM runs r
STRAIGHT_JOIN provider_submissions ps
  ON ps.run_id = r.id
 AND ps.provider = r.provider
LEFT JOIN provider_execution_slots s
  ON s.provider = r.provider
 AND s.run_id = r.id
 AND s.expires_at > CURRENT_TIMESTAMP(3)
WHERE r.provider = ?
  AND r.status IN (
      'queued',
      'running',
      'waiting_input',
      'waiting_external',
      'cancelling'
  )
  AND ps.state IN ('sending', 'unknown', 'accepted')
  AND s.run_id IS NULL;

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
-- Invariant L: every target captured for a succeeded occurrence has a
-- durable delivery execution row. The expectation is immutable history;
-- later schedule edits cannot reinterpret an old occurrence.
SELECT COUNT(*) AS n
FROM occurrence_delivery_expectations e
JOIN schedule_occurrences o ON o.id = e.occurrence_id
LEFT JOIN delivery_executions de
  ON de.occurrence_id = e.occurrence_id
 AND de.schedule_delivery_id = e.schedule_delivery_id
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
--
-- 第十一轮 P0-6: name is backfilled. The streaming discovery frame often
-- carries no name while the final reconciliation does (or vice versa), so
-- the SECOND write must be able to fill in what the first one lacked.
-- IF(VALUES(name) = '', name, VALUES(name)) makes it monotone in one
-- direction only: an empty incoming name never erases a known one, and a
-- late real name always lands. Without this a multi-image answer kept one
-- artifact row nameless forever, which is what made the second image render
-- without a label / stay unreferenced by the markdown rewriter.
INSERT INTO run_artifacts (id, run_id, provider, external_artifact_id, provider_artifact_type,
    name, normalized_type, storage_type, resolution_status, metadata)
VALUES (?, ?, ?, ?, ?, ?, ?, 'external', 'pending', NULL)
ON DUPLICATE KEY UPDATE
    provider_artifact_type = VALUES(provider_artifact_type),
    normalized_type = VALUES(normalized_type),
    name = IF(VALUES(name) = '', name, VALUES(name));

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

-- name: ListClaimedAttachmentsByRun :many
-- 第十一轮 P0-2: the worker attachment bridge reads the attachments THIS
-- run owns. The run_id predicate is the ownership scope — the caller must
-- pass the run id from its ExecutionOwnership fence, never a request value.
SELECT id, run_id, conversation_id, provider, external_attachment_id, attachment_type,
       name, source_type, source_url, storage_key, content_type, size_bytes,
       auth_mode, auth_subject_key, status, metadata, created_by, created_at, updated_at
FROM runtime_attachments
WHERE run_id = ?
ORDER BY created_at;

-- name: MarkAttachmentUploadedFenced :execresult
-- 第十一轮 P0-3: fenced attachment writeback. Both the attachment id AND
-- its run_id are predicates, so a stale worker (or a mistyped id) can
-- never stamp an external id onto another run's attachment. RowsAffected
-- != 1 means the attachment is gone / not this run's → the caller stops.
UPDATE runtime_attachments
SET external_attachment_id = ?, status = 'uploaded'
WHERE id = ? AND run_id = ? AND external_attachment_id = '';

-- name: DeleteAttachment :exec
DELETE FROM runtime_attachments WHERE id = ?;

-- name: AppendUserAttachmentToRunInput :execresult
-- Late-attachment race guard: only while queued.
--
-- 第十一轮 P0-1: studio_attachment_ids is the STUDIO id list (local
-- runtime_attachments.id). The provider id is minted by the worker at
-- upload time and never stored on the run input.
--
-- JSON_SET first, JSON_ARRAY_APPEND second: a bare JSON_ARRAY_APPEND on a
-- path that does not exist yet yields NULL, which silently ERASED the whole
-- run input for every run created without attachments (measured against
-- MySQL 5.7: `JSON_ARRAY_APPEND('{"a":1}','$.k','v')` → NULL). Seeding the
-- key with an empty array makes the append total instead of destructive.
UPDATE runs
SET input = JSON_ARRAY_APPEND(
        JSON_SET(input, '$.studio_attachment_ids',
                 IF(JSON_CONTAINS_PATH(input, 'one', '$.studio_attachment_ids'),
                    JSON_EXTRACT(input, '$.studio_attachment_ids'),
                    JSON_ARRAY())),
        '$.studio_attachment_ids', ?)
WHERE id = ? AND status = 'queued';

-- name: ClaimAttachmentForRun :execresult
-- Atomic attachment claim (评测 P1-5): the run row is created first, then
-- each attachment is claimed with a run_id IS NULL guard. 0 rows affected
-- = a concurrent run already claimed it; the caller MUST roll the whole
-- CreateRun transaction back rather than create a run whose input lists an
-- attachment it does not own.
UPDATE runtime_attachments
SET run_id = ?, conversation_id = ?
WHERE id = ? AND created_by = ? AND status = 'pending' AND run_id IS NULL;

-- name: CountOutstandingRunsByUser :one
-- Per-user admission (评测 P1-7): every NON-SETTLED run counts against
-- the cap (第四轮 P2 / 第五轮 P2-1) — same predicate as
-- CountActiveRunsByConversation, so a waiting_input / waiting_external /
-- cancelling run still cannot slip past the outstanding limit, while a
-- legacy `interrupted` run no longer consumes a slot forever.
SELECT COUNT(*) AS n FROM runs
WHERE user_id = ? AND status NOT IN ('cancelled', 'succeeded', 'failed', 'interrupted');

-- ───────────────────────────────────────────────── request idempotency ──
-- POST /api/v2/runs replay protection (第九轮 P0-1). The reservation is
-- taken INSIDE the run-creation transaction, so a crash between the two
-- rolls both back: a reserved request identity always has its run.

-- name: ReserveRunRequest :execresult
-- ON DUPLICATE KEY UPDATE is deliberate: a duplicate reports 0 changed
-- rows (the assigned column keeps its value) instead of raising MySQL 1062,
-- so the caller gets a clean "already reserved" signal on the same
-- round-trip as the success case. A concurrent duplicate blocks until the
-- winning transaction commits or rolls back — if it rolled back, this
-- INSERT simply succeeds and takes the identity over.
INSERT INTO run_requests (user_id, client_request_id, run_id, request_hash)
VALUES (?, ?, ?, ?)
ON DUPLICATE KEY UPDATE user_id = user_id;

-- name: GetRunRequest :one
-- Replay lookup. request_hash is returned so the caller can distinguish
-- "same request" (replay) from "same key, different payload" (409).
SELECT run_id, request_hash FROM run_requests
WHERE user_id = ? AND client_request_id = ?;

-- ───────────────────────────────────────────── provider submissions ──
-- Durable record of the external side effect (第九轮 P0-2). Written BEFORE
-- the HTTP submit and updated after it, so the "provider may already have
-- the request" window is recoverable instead of invisible.

-- name: CreateProviderSubmission :execresult
-- 1 changed row = a new submission row; 0 = this (run, submission_no) is
-- already recorded (see ReserveRunRequest for the same idiom).
INSERT INTO provider_submissions
    (run_id, submission_no, provider, idempotency_key, request_hash, state, attempt)
VALUES (?, ?, ?, ?, ?, 'sending', ?)
ON DUPLICATE KEY UPDATE run_id = run_id;

-- name: GetLatestProviderSubmission :one
-- The newest submission for a run. sql.ErrNoRows means this run has never
-- been submitted to a provider.
SELECT run_id, submission_no, provider, idempotency_key, request_hash, state,
       attempt, external_run_id, last_error, created_at, updated_at
FROM provider_submissions
WHERE run_id = ?
ORDER BY submission_no DESC
LIMIT 1;

-- name: MarkProviderSubmissionAccepted :execresult
-- The provider answered with an external id: the outcome is now KNOWN, both
-- here and on the run row (written in the same transaction).
--
-- The FROM guard makes 'accepted' MONOTONIC (第九轮复审 P1): only an
-- in-flight ('sending') or unconfirmed ('unknown') submission may become
-- accepted. A stale worker that lost its lease milliseconds ago must not be
-- able to overwrite a later 'rejected' with an external id it happened to
-- learn before it was fenced out.
UPDATE provider_submissions
SET state = 'accepted', external_run_id = ?, last_error = NULL
WHERE run_id = ? AND submission_no = ? AND state IN ('sending', 'unknown');

-- name: MarkProviderSubmissionState :execresult
-- The outcome of an in-flight submit: 'unknown' ('the request may have been
-- delivered and the provider cannot be asked') or 'rejected' ('the provider
-- definitively refused, a retry is legitimate').
--
-- `AND state = 'sending'` IS the compare-and-swap (第九轮复审 P1). Without it
-- a general UPDATE could move a submission BACKWARDS — 'accepted' → 'unknown'
-- or 'rejected' → 'rejected' — which would either re-open a resend of an
-- action the provider already holds or silently re-enable one that had been
-- parked. The only legal predecessor of an outcome is 'sending'.
UPDATE provider_submissions
SET state = ?, last_error = ?
WHERE run_id = ? AND submission_no = ? AND state = 'sending';

-- name: ReopenProviderSubmission :execresult
-- The one transition that goes BACK to 'sending': a previously REJECTED
-- submission is retransmitted under the same identity/key (第九轮 P0-2).
--
-- It is deliberately a separate statement instead of a 'rejected' entry in
-- MarkProviderSubmissionState's FROM list, so "record an outcome" and "arm a
-- resend" can never be confused: only a definitive refusal arms a resend, and
-- writing it through the outcome query would make that invisible.
UPDATE provider_submissions
SET state = 'sending', attempt = ?, last_error = NULL
WHERE run_id = ? AND submission_no = ? AND state = 'rejected';

-- name: ReopenUnknownProviderSubmission :execresult
-- unknown → sending, the resend a NATIVELY IDEMPOTENT provider allows
-- (第九轮复审 P2).
--
-- It is a separate statement on purpose. A provider that can collapse a
-- resend on the stable submission key is the ONE case in which an
-- unconfirmed request may be transmitted again, and it must stay visibly
-- distinct from the 'rejected' resend above: 'rejected' means "the provider
-- holds nothing", whereas here the provider may already hold the request and
-- is trusted to deduplicate it. Fusing them would let a capability
-- declaration silently turn an at-most-once ledger into at-least-once.
UPDATE provider_submissions
SET state = 'sending', attempt = ?, last_error = NULL
WHERE run_id = ? AND submission_no = ? AND state = 'unknown';

-- name: CountProviderSubmissionsByRun :one
SELECT COUNT(*) AS n FROM provider_submissions WHERE run_id = ?;

-- ─────────────────────────────────────────────── waiting_external ──
-- Parked-unknown lifecycle (第九轮 P0-2). A run whose provider submit may
-- have been delivered but cannot be confirmed is parked, NOT retried: for a
-- provider with neither a native idempotency key nor a lookup-by-request
-- capability, at-most-once is the only honest option.

-- name: AwaitExternalRunFenced :execresult
-- Fenced running → waiting_external. Non-terminal: the run still holds its
-- conversation and its outstanding slot until the sweep below resolves it.
UPDATE runs
SET status = 'waiting_external'
WHERE id = ? AND status = 'running' AND lease_epoch = ?;

-- name: ListParkedWaitingExternalRunIDs :many
-- The sweep's input. updated_at is the parking instant: the run row is
-- written exactly once when it enters waiting_external and nothing touches
-- it afterwards, so the grace window measures from the park (not from a
-- later unrelated write), and runs touched by hand are naturally excluded
-- until they go quiet again.
--
-- The cutoff is an absolute instant derived from the DB clock by the caller
-- (CurrentDBTime minus the grace), never from the application clock — the
-- same rule every other timing decision in this package follows.
SELECT id FROM runs
WHERE status = 'waiting_external'
  AND updated_at <= sqlc.arg(quiet_before)
ORDER BY updated_at
LIMIT ?;

-- name: FailParkedExternalRun :execresult
-- Terminal resolution of an unconfirmable submit. Guarded on
-- status = 'waiting_external' so a concurrent resolution (cancel, a
-- reconciler that DID manage to confirm the call) wins cleanly.
UPDATE runs
SET status = 'failed', error_code = 'provider_submit_unknown', error_message = ?,
    finished_at = CURRENT_TIMESTAMP(3)
WHERE id = ? AND status = 'waiting_external';
