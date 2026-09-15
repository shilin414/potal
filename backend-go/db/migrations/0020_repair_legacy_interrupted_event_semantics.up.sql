-- Repair the overloaded pre-closure `run.interrupted` event semantics
-- (第六轮 P0).
--
-- The historical retry path was:
--
--     releaseInterrupted(run, reason):
--         AppendEvent(run, run.interrupted, {"reason": reason})
--         if run.Attempt >= run.MaxAttempts: finish(failed)   // (B)
--         else:                              requeue()        // (A)
--
-- i.e. the event was written BEFORE the requeue/fail decision, so ONE
-- event name carried TWO meanings:
--
--     A. reason-only payload + run still live  → retry / interruption
--        marker (the run comes back and may still succeed)
--     B. payload carrying "status" (written by the legacy
--        Finish(StatusInterrupted) path) → direct terminal failure
--
-- Migration 0018 rewrote EVERY run.interrupted to run.failed, which is
-- correct for (B) and WRONG for (A): a historical run that was interrupted
-- and then succeeded ends up with a terminal run.failed in the middle of
-- its log, and the frontend/SSE client closes the subscription on the
-- first terminal event — a successful run renders as 执行失败 depending on
-- network framing. This migration restores the true semantics.
--
-- NOTE: migration 0018 is already applied in every environment and is
-- deliberately NOT edited. Fixing it forward keeps all three cases safe:
-- fresh DB (0018 → 0019 → 0020), already-migrated DB (0020 repairs the
-- damage), future production upgrade (ordered run, correct end state).

-- 1. Retry-origin markers → the canonical non-terminal event. The
--    discriminator is the legacy payload shape, not the run status:
--    ReleaseInterrupted wrote ONLY {"reason": ...}; the direct terminal
--    path always wrote a "status" key as well, so those rows are left
--    alone and stay run.failed (0018 already converted them).
--    The `run.interrupted` arm is defensive: it is a no-op on a
--    fully-0018-migrated database and repairs a database where 0018
--    never ran or was partially applied.
UPDATE run_events
SET event_type = 'run.retrying'
WHERE event_type IN ('run.failed', 'run.interrupted')
  AND JSON_EXTRACT(payload, '$.reason') IS NOT NULL
  AND JSON_EXTRACT(payload, '$.status') IS NULL;

-- 2. Invariant D — "a terminal run owns at least one terminal event".
--    Two historical shapes violate it once step 1 has run:
--
--      * a run that exhausted its retries: the legacy code emitted only
--        the reason-only marker and then set runs.status = failed without
--        a second event, so after step 1 it has no canonical terminal
--        event left;
--      * a run finalized by the pre-closure code that predates atomic
--        terminal-event persistence (synthesized here for the same
--        reason 0018 already synthesized one).
--
--    Both are healed by appending the canonical terminal event derived
--    from the run status. `interrupted` is intentionally NOT in the
--    scanned status set: 0018 already turned those rows into `failed`
--    (with a synthesized event where needed), and this statement must not
--    reopen the legacy-alias question.
--    Sequence continues after the run's last event; the guard makes a
--    second run a no-op.
INSERT INTO run_events (run_id, sequence, event_type, payload)
SELECT r.id,
       COALESCE((SELECT MAX(e.sequence) FROM run_events e WHERE e.run_id = r.id), 0) + 1,
       CASE r.status
           WHEN 'succeeded' THEN 'run.completed'
           WHEN 'cancelled' THEN 'run.cancelled'
           ELSE 'run.failed'
       END,
       JSON_OBJECT(
           'status', r.status,
           'error_code', COALESCE(r.error_code, ''),
           'error_message', COALESCE(r.error_message, ''),
           'migrated_from', 'terminal_without_canonical_event'
       )
FROM runs r
WHERE r.status IN ('succeeded', 'failed', 'cancelled')
  AND NOT EXISTS (
      SELECT 1
      FROM run_events e2
      WHERE e2.run_id = r.id
        AND e2.event_type IN ('run.completed', 'run.failed', 'run.cancelled')
  );
