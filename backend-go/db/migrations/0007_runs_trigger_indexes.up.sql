-- 0007: claim/trigger indexes on runs (columns come from 0006).

ALTER TABLE runs
    ADD KEY idx_runs_claim_v2 (status, available_at, priority, created_at),
    ADD KEY idx_runs_trigger (trigger_type, trigger_id);
