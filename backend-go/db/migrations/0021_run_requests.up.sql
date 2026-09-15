-- End-to-end request idempotency for POST /api/v2/runs (第九轮 P0-1).
--
-- The duplicate-run hazard this closes:
--
--     Browser --POST /api/v2/runs--> Backend --COMMIT--> MySQL
--     MySQL --X(ack lost / socket reset / 504)--> Browser
--     Browser --retry POST /api/v2/runs--> Backend   → a SECOND turn
--
-- The run-creation transaction was already atomic, but it had no notion of
-- "this is a REPLAY of the request I already served". Conversation
-- serialization is NOT request idempotency: while the first run is still
-- live the retry is merely rejected with 409 (and the client never learns
-- which run won), and once the first run finishes quickly the retry creates
-- a legitimate-looking second Run, a second user message and a second
-- provider chat.
--
-- Why a dedicated table instead of UNIQUE(user_id, client_request_id) on
-- `runs`: the first request may be LAZY (conversation_id absent). The
-- conversation is created INSIDE the run transaction, so a request identity
-- that must be reserved BEFORE any run row exists cannot live on `runs`.
--
--   request_hash = SHA-256 over the NORMALIZED payload
--                  (application_id, conversation_id|NULL, content,
--                   deduplicated attachment ids)
--
-- so the same client_request_id can distinguish:
--
--   same id + same hash  → replay: return the ORIGINAL run (HTTP 200)
--   same id + other hash → HTTP 409 idempotency_key_reused
--
-- Collation utf8mb4_bin: client_request_id is an opaque token (a UUID on the
-- wire, but no format is enforced), and case-insensitive matching would let
-- two distinct tokens collide.
CREATE TABLE run_requests (
    user_id            BIGINT UNSIGNED NOT NULL,
    client_request_id  VARCHAR(64)     NOT NULL,
    run_id             BINARY(16)      NOT NULL,
    request_hash       BINARY(32)      NOT NULL,
    created_at         DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (user_id, client_request_id),
    -- One request identity maps to exactly one run; this also lets a
    -- recovery path resolve a reservation back to its run id.
    UNIQUE KEY uniq_run_requests_run (run_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
