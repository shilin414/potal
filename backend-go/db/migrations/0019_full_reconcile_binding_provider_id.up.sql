-- Full reconciliation of runtime_bindings.provider_id (第五轮 P2-5).
--
-- 0017 used an INNER JOIN on provider_key, so it could only repair rows
-- whose provider_key resolves to an existing provider. Left untouched:
--
--   provider_key = a provider that no longer exists
--   provider_id  = still pointing at another (still existing) provider
--
-- The INNER JOIN never matches those rows, so the two join styles stay
-- inconsistent. provider_id has no FK constraint, so nothing enforces it.
--
-- Execution safety is NOT affected: authorization and the kill switch
-- join on provider_key, and 0017 is deliberately left in place (migrations
-- already applied elsewhere must not be rewritten). This migration is the
-- completion pass, and it is IDEMPOTENT — safe to re-run.
--
--   provider_key resolves            → provider_id = that provider's id
--   provider_key does NOT resolve    → provider_id = NULL
UPDATE runtime_bindings b
LEFT JOIN providers p ON p.provider_key = b.provider_key
SET b.provider_id = p.id
WHERE
    (
        p.id IS NULL
        AND b.provider_id IS NOT NULL
    )
    OR
    (
        p.id IS NOT NULL
        AND (
            b.provider_id IS NULL
            OR b.provider_id <> p.id
        )
    );
