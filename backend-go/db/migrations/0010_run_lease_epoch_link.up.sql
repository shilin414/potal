-- Execution Correctness Closure: link run_leases to the runs.lease_epoch
-- they were created under (修复计划 §13). The reaper can then verify that
-- an expired lease really belongs to the run's CURRENT epoch before
-- recovering it — stronger than a token-only predicate and it makes the
-- recovery transaction a single atomic unit (run CAS + outbox/event +
-- lease delete).
ALTER TABLE run_leases
    ADD COLUMN lease_epoch BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER lease_token;

-- Backfill: leases created before this migration belong to whatever the
-- run's current epoch is (they were written right after the epoch bump).
UPDATE run_leases l
JOIN runs r ON r.id = l.run_id
SET l.lease_epoch = r.lease_epoch;
