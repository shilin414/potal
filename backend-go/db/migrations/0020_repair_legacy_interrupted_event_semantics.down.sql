-- Irreversible historical event normalization.
--
-- The original payload shape is destroyed by the forward direction (a
-- reason-only run.failed becomes run.retrying), and the synthesized
-- terminal events carry `migrated_from` provenance with no original row
-- to restore. Re-running the forward migration is safe (both statements
-- are guard-shaped), so there is nothing meaningful to undo.
SELECT 1;
