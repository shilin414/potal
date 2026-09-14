-- Provider admission serialization hardening.
--
-- The admission decision ("delete expired → count active → insert") must be
-- serialized per provider. A locking read (SELECT ... FOR UPDATE) is NOT
-- sufficient on its own: a locking read does not block a concurrent decision
-- from observing the same pre-insert depth, so every contender sees zero
-- active slots. CI observed exactly that — eight simultaneous first
-- admissions on a fresh provider were ALL admitted
-- (TestConcurrentProviderAdmissionOnFreshProvider: admitted=8 rejected=0),
-- and the real worker admitted two runs with max_inflight=1.
--
-- Making the serialization a CONFLICTING WRITE on this shared row fixes it:
-- a concurrent decision waits for the row lock, and a loser that still fails
-- with 1213 (deadlock) or 1205 (lock wait timeout) is retried by the caller
-- with a fresh snapshot, so it recounts and rejects. The row is seeded by
-- migration 0011 and self-heals through EnsureProviderAdmissionLock.
ALTER TABLE provider_admission_locks
    ADD COLUMN admissions BIGINT UNSIGNED NOT NULL DEFAULT 0;
