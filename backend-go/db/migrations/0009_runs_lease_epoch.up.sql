-- Lease fencing (评测报告 P0-3): every claim bumps a monotonically
-- increasing lease_epoch on the run. Workers capture the epoch at claim
-- time and every canonical write must match it — a stale worker (lease
-- expired, run reclaimed) fails the fence and loses write ownership.
ALTER TABLE runs
    ADD COLUMN lease_epoch BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER attempt;
