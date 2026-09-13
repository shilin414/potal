-- Conversation share snapshots. A share pins the selected message ids at
-- creation time; the public read endpoint returns ONLY those messages, so a
-- shared link can never unlock the rest of the conversation (the reference
-- implementation filtered ?msg=... on the client — the API returned the full
-- message list and stripping the suffix revealed everything).
CREATE TABLE conversation_shares (
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    token           VARCHAR(64)     NOT NULL,
    conversation_id BIGINT UNSIGNED NOT NULL,
    user_id         BIGINT UNSIGNED NOT NULL,
    message_ids     JSON            NOT NULL,
    created_at      DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    revoked_at      DATETIME(3)     NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uniq_conversation_shares_token (token),
    KEY idx_conversation_shares_conversation (conversation_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
