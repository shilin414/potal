-- Provider admission serialization hardening.
--
-- The admission decision ("delete expired → count active → insert") must be
-- serialized per provider. A locking read (SELECT ... FOR UPDATE) is NOT
-- sufficient: TiDB may execute the admission transaction optimistically, in
-- which case a concurrent decision is not blocked at all and every contender
-- sees zero active slots. CI observed exactly that — eight simultaneous first
-- admissions on a fresh provider were ALL admitted
-- (TestConcurrentProviderAdmissionOnFreshProvider: admitted=8 rejected=0),
-- and the real worker admitted two runs with max_inflight=1.
--
-- Making the serialization a CONFLICTING WRITE on this shared row fixes it in
-- both transaction modes: pessimistically the losers wait for the row lock,
-- optimistically they abort at commit with a 9007 write conflict which the
-- caller retries with a fresh snapshot (so it recounts and rejects). The row
-- is seeded by migration 0011 and self-heals through
-- EnsureProviderAdmissionLock.
ALTER TABLE provider_admission_locks
    ADD COLUMN admissions BIGINT UNSIGNED NOT NULL DEFAULT 0;
