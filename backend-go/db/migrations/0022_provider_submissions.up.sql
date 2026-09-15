-- Provider submit idempotency + the unknown-outcome state (第九轮 P0-2).
--
-- The hazard: the submit order is
--
--     Gate 2 → BeginProviderAttempt → HTTP provider submit
--            → agent_chat_id → UpdateExternalRunID
--
-- A crash between "the provider accepted the request" and "we persisted the
-- external id" leaves runs.external_run_id = '' and NOTHING local that says
-- the external side effect may already exist. Lease fencing cannot help:
-- it prevents a stale worker from writing canonical state, it cannot undo an
-- HTTP request the provider already processed. Retrying that submit creates
-- a second provider chat.
--
-- `provider_submissions` records the intent BEFORE the HTTP call and the
-- outcome after it, so a later attempt can tell the three cases apart:
--
--     state='sending'   the request was (or is being) transmitted; the
--                       outcome is not yet known locally
--     state='accepted'  the provider answered with an external id
--     state='unknown'   the request MAY have been delivered and the provider
--                       cannot be asked (no native idempotency key, no
--                       lookup by request key) → the run must NOT be blindly
--                       retried; it goes to waiting_external
--     state='rejected'  the provider definitively refused (4xx, auth, ...)
--                       → a normal retry keeps its meaning
--
-- idempotency_key is STABLE across transport retries of the same business
-- submission — `potal:run:<run_uuid>:submit:1` — and deliberately does NOT
-- embed the attempt counter: several HTTP retries are still ONE external
-- action, and a per-attempt key would hand the provider N distinct requests.
-- UNIQUE(provider, idempotency_key) is what makes that claim enforceable.
--
-- submission_no exists so a provider that can eventually accept a genuinely
-- NEW submission (e.g. after a resolved unknown) gets its own row instead of
-- overwriting the first one's outcome.
CREATE TABLE provider_submissions (
    run_id           BINARY(16)      NOT NULL,
    submission_no    INT UNSIGNED    NOT NULL DEFAULT 1,
    provider         VARCHAR(64)     NOT NULL,
    idempotency_key  VARCHAR(128)    NOT NULL,
    request_hash     BINARY(32)      NOT NULL,
    state            VARCHAR(24)     NOT NULL,
    attempt          INT UNSIGNED    NOT NULL DEFAULT 0,
    external_run_id  VARCHAR(255)    NOT NULL DEFAULT '',
    last_error       TEXT            NULL,
    created_at       DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at       DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
                                     ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (run_id, submission_no),
    UNIQUE KEY uniq_provider_submit_key (provider, idempotency_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
