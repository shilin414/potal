-- O(1) run-event sequence allocation (第九轮 P1-3).
--
-- The old allocator computed the next sequence as COUNT(*) + 1 under the run
-- row lock. It was CORRECT (every event writer takes that lock first, so the
-- count cannot move between the read and the INSERT) but its cost grows with
-- the run's history:
--
--     event 1       COUNT 0
--     event 10 000  COUNT 9 999
--     event 100 000 COUNT 99 999
--
-- For a long streaming answer this turns every durable append into a scan of
-- the run's whole event table, exactly when the run is producing the most
-- events.
--
-- next_event_sequence is a monotonic per-run counter, so allocation becomes
-- a read of one row that the caller already locked:
--
--     SELECT next_event_sequence FROM runs WHERE id = ? FOR UPDATE
--     UPDATE runs SET next_event_sequence = next_event_sequence + 1 WHERE id = ?
--     INSERT run_events (sequence = <the value read>)
--
-- All three statements stay inside the writer's existing transaction, so the
-- sequence remains gap-free and unique per run, enforced as before by
-- UNIQUE(run_id, sequence).
--
-- NOTE: the UPDATE assigns `next_event_sequence + 1` rather than a computed
-- literal on purpose — MySQL reports CHANGED rows, and a same-value write
-- would report 0 and be indistinguishable from a lost row lock.
ALTER TABLE runs
    ADD COLUMN next_event_sequence BIGINT UNSIGNED NOT NULL DEFAULT 1
        AFTER lease_epoch;

-- Backfill from the existing history. COALESCE(MAX(...), 0) + 1 is the
-- established form for this project's migrations: a run with no events starts
-- at 1, and the down/up pair must never leave the counter at 0 (a 0 would
-- collide with the transient-event sequence sentinel).
--
-- The correlated subquery reads run_events, not runs, so MySQL's
-- "cannot update a table selected from" restriction does not apply.
UPDATE runs
SET next_event_sequence = COALESCE(
    (SELECT MAX(e.sequence) + 1 FROM run_events e WHERE e.run_id = runs.id),
    1
);
