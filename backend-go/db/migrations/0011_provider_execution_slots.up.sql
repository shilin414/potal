-- Provider Inflight Durable Truth (Admission Fairness & Distributed Lease
-- Hardening, Phase 2).
--
-- max_inflight is a SAFETY capacity state, so it belongs to the correctness
-- plane (MySQL), not to a transient Redis semaphore: a Redis restart / flush /
-- failover used to drop the whole inflight ZSET while real provider calls
-- kept running, and the next workers admitted a fresh full batch — the real
-- provider concurrency could reach 2× the configured limit.
--
-- Invariant: an active provider execution ⇔ an active provider_execution_slots
-- row. Acquire, Renew, Release and expiry all use the DB clock.
CREATE TABLE provider_execution_slots (
    id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    provider     VARCHAR(64)     NOT NULL,
    run_id       BINARY(16)      NOT NULL,
    lease_epoch  BIGINT UNSIGNED NOT NULL,
    lease_token  BINARY(16)      NOT NULL,
    worker_id    VARCHAR(128)    NOT NULL,
    acquired_at  DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    heartbeat_at DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    expires_at   DATETIME(3)     NOT NULL,
    PRIMARY KEY (id),
    -- Ownership-scoped uniqueness: one slot per (run, claim epoch). A stale
    -- attempt can never collide with, extend or delete the new owner's slot.
    UNIQUE KEY uniq_provider_slot_ownership (provider, run_id, lease_epoch),
    KEY idx_provider_slots_provider_expires (provider, expires_at),
    KEY idx_provider_slots_run (run_id, lease_epoch)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- Per-provider admission serialization row. Migration 0013 upgrades the
-- serialization from a locking read to a CONFLICTING WRITE on this row, so
-- "delete expired → count active → insert" is atomic without table-level or
-- advisory locks. Rows are seeded for existing providers; unknown providers
-- self-heal on first Acquire.
CREATE TABLE provider_admission_locks (
    provider   VARCHAR(64)     NOT NULL,
    created_at DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (provider)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

INSERT INTO provider_admission_locks (provider)
SELECT provider_key FROM providers;
