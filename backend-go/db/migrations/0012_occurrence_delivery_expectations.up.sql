-- Invariant L hardening: freeze the delivery policy at occurrence creation.
-- Schedule delivery configuration is mutable; historical occurrences must
-- not gain or lose required destinations when the schedule is edited later.
ALTER TABLE schedule_occurrences
    ADD COLUMN delivery_snapshot_at DATETIME(3) NULL AFTER finished_at;

CREATE TABLE occurrence_delivery_expectations (
    occurrence_id        BIGINT UNSIGNED NOT NULL,
    schedule_delivery_id BIGINT UNSIGNED NOT NULL,
    channel              VARCHAR(20)     NOT NULL,
    sender_identity_mode VARCHAR(20)     NOT NULL,
    target_type          VARCHAR(20)     NOT NULL,
    target_id            VARCHAR(128)    NOT NULL,
    target_name          VARCHAR(200)    NOT NULL DEFAULT '',
    content_mode         VARCHAR(20)     NOT NULL DEFAULT 'summary',
    created_at           DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (occurrence_id, schedule_delivery_id),
    KEY idx_occurrence_delivery_expectations_target (schedule_delivery_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

