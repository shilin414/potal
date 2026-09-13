-- 0007 down.

ALTER TABLE runs
    DROP KEY idx_runs_trigger,
    DROP KEY idx_runs_claim_v2;
