-- Reconcile runtime_bindings.provider_id with provider_key (第四轮 P2).
--
-- 0016 only repaired NULL provider_id rows. A historical binding whose
-- provider_id points at ANOTHER provider (written before the API started
-- setting it, or by a manual import) is left inconsistent: the FK still
-- resolves, but to the wrong provider.
--
-- Execution safety is NOT affected — authorization and the kill switch
-- join on provider_key (评测 P1) — so this is pure data hygiene: it makes
-- the two join styles agree for every row, not just for NULLs.
UPDATE runtime_bindings b
JOIN providers p ON p.provider_key = b.provider_key
SET b.provider_id = p.id
WHERE b.provider_id IS NULL
   OR b.provider_id <> p.id;
