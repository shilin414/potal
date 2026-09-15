-- Normalize the legacy `interrupted` run status (第五轮 P2-1).
--
-- Contract: `interrupted` is a PRE-CLOSURE terminal alias, equivalent to
-- `failed`. New code never writes it (retry emits run.retrying and keeps
-- streaming; terminal failure emits run.failed), so the canonical terminal
-- set is exactly {cancelled, succeeded, failed}.
--
-- Why it matters operationally: the "active run" predicate is
-- `status NOT IN ('cancelled','succeeded','failed','interrupted')`
-- (CountActiveRunsByConversation / CountOutstandingRunsByUser /
-- CASFinishRun). A single pre-closure interrupted row would otherwise
-- occupy a conversation (every later send → 409) and a user's outstanding
-- slot forever. This migration makes the historical data match the
-- contract instead of relying on the predicate's fourth value forever.
--
-- 0017 is already applied in every environment — do NOT edit it. This is
-- a new, separately versioned migration.

-- 1. Occurrences: a legacy interrupted run's occurrence must not stay
--    `pending`/`running` forever (the scheduler would keep seeing an
--    unfinished slot and the delivery worker would never fire).
UPDATE schedule_occurrences o
JOIN runs r ON r.id = o.run_id
SET o.status      = 'failed',
    o.finished_at = COALESCE(o.finished_at, CURRENT_TIMESTAMP(3))
WHERE r.status = 'interrupted'
  AND o.status NOT IN ('succeeded', 'failed');

-- 2. Legacy events: replaying run.interrupted today renders it as a
--    failure (frontend + SSE synthetic frame). Persist that reading so
--    the event log and the canonical event vocabulary agree.
UPDATE run_events
SET event_type = 'run.failed'
WHERE event_type = 'run.interrupted';

-- 3. Terminal-event completeness (invariant D: a terminal run must own a
--    terminal event). A legacy interrupted run that was interrupted
--    WITHOUT an event row would, after step 4, be a `failed` run with an
--    empty event log — the invariant checker flags exactly that, and the
--    SSE replay would have nothing to close the stream with. Synthesize
--    the missing run.failed BEFORE the status is rewritten (afterwards
--    the row is indistinguishable from any other failed run).
--    Sequence continues after the run's last event; the whole statement
--    is a no-op on a second run (no row is `interrupted` any more).
INSERT INTO run_events (run_id, sequence, event_type, payload)
SELECT r.id,
       COALESCE((SELECT MAX(e.sequence) FROM run_events e WHERE e.run_id = r.id), 0) + 1,
       'run.failed',
       JSON_OBJECT('migrated_from', 'interrupted')
FROM runs r
WHERE r.status = 'interrupted'
  AND NOT EXISTS (
      SELECT 1 FROM run_events e2
      WHERE e2.run_id = r.id
        AND e2.event_type IN ('run.completed', 'run.failed', 'run.cancelled')
  );

-- 4. Runs: interrupted → failed. Keep an existing error_code; only rows
--    without one get a marker saying where the status came from.
UPDATE runs
SET status       = 'failed',
    error_code   = IF(error_code IS NULL OR error_code = '',
                      'legacy_interrupted',
                      error_code),
    finished_at  = COALESCE(finished_at, updated_at, CURRENT_TIMESTAMP(3))
WHERE status = 'interrupted';
