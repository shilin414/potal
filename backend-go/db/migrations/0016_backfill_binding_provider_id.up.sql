-- Backfill runtime_bindings.provider_id from provider_key (评测 P1).
--
-- Bindings created through the API never populated provider_id, so the
-- execution gate's provider check (which joined on provider_id) matched
-- nothing and let a disabled provider keep executing. The gate now joins
-- on provider_key, and this migration repairs the FK for consistency so
-- both join styles agree.
UPDATE runtime_bindings b
JOIN providers p ON p.provider_key = b.provider_key
SET b.provider_id = p.id
WHERE b.provider_id IS NULL;
