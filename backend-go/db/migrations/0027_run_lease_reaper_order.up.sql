-- Deterministic bounded reaper batches: satisfy
-- WHERE expires_at <= ... ORDER BY expires_at, run_id LIMIT ? from one index.
ALTER TABLE run_leases
    ADD KEY idx_leases_expires_run (expires_at, run_id);
