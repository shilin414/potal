-- Down migration for 0026_repair_catalog_invariants.
--
-- The up migration CLEARS is_default_agent on rows that violate the
-- default-agent invariant (disabled / non-chat / no effective binding /
-- provider not active). The previous values cannot be restored: which of the
-- cleared rows was "correctly" default before the invariant existed is not
-- recorded anywhere, and re-setting them would re-introduce the violation.
-- The workspace bootstrap falls back to the first eligible chat application
-- on its own, so leaving the flags cleared is the safe direction.
SELECT 1;
