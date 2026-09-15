-- Irreversible status normalization: which historical rows were
-- `interrupted` (as opposed to genuinely `failed`) is not recoverable once
-- every run_events row has been rewritten to run.failed, and the
-- occurrence status has no legacy value to restore.
--
-- The forward direction stays safe to re-run because it is guarded by
-- `status = 'interrupted'` / `event_type = 'run.interrupted'`, i.e. it is
-- a no-op once the data has been normalized.
SELECT 1;
