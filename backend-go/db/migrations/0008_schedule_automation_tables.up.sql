-- 0008: schedule automation tables (§Creation Agent Studio 架构文档 Schedule/Delivery).
-- Schedule = 什么时候自动创建 Run; Run = 一次真实执行; Delivery = 结果发给谁.

CREATE TABLE schedules (
    id                       BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    owner_user_id            BIGINT UNSIGNED NOT NULL,
    name                     VARCHAR(200)    NOT NULL,
    description              TEXT            NULL,
    application_id           BIGINT UNSIGNED NOT NULL,
    input_payload            JSON            NULL,
    schedule_type            VARCHAR(20)     NOT NULL DEFAULT 'daily',
    cron_expression          VARCHAR(120)    NOT NULL DEFAULT '',
    trigger_config           JSON            NULL,
    timezone                 VARCHAR(64)     NOT NULL DEFAULT 'Asia/Shanghai',
    run_at                   DATETIME(3)     NULL,
    enabled                  TINYINT(1)      NOT NULL DEFAULT 1,
    conversation_policy      VARCHAR(20)     NOT NULL DEFAULT 'new_each_run',
    conversation_id          BIGINT UNSIGNED NULL,
    overlap_policy           VARCHAR(20)     NOT NULL DEFAULT 'queue',
    misfire_policy           VARCHAR(20)     NOT NULL DEFAULT 'fire_once',
    execution_window_seconds INT UNSIGNED    NOT NULL DEFAULT 0,
    deadline_policy          VARCHAR(20)     NOT NULL DEFAULT 'execute_anyway',
    next_run_at              DATETIME(3)     NULL,
    last_run_at              DATETIME(3)     NULL,
    created_at               DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at               DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    KEY idx_schedules_due (enabled, next_run_at),
    KEY idx_schedules_owner (owner_user_id, created_at),
    KEY idx_schedules_application (application_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE schedule_occurrences (
    id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    schedule_id  BIGINT UNSIGNED NOT NULL,
    scheduled_at DATETIME(3)     NOT NULL,
    enqueued_at  DATETIME(3)     NULL,
    admitted_at  DATETIME(3)     NULL,
    run_id       BINARY(16)      NULL,
    status       VARCHAR(20)     NOT NULL DEFAULT 'pending',
    triggered_at DATETIME(3)     NULL,
    finished_at  DATETIME(3)     NULL,
    created_at   DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at   DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uniq_occurrence_slot (schedule_id, scheduled_at),
    KEY idx_occurrences_schedule_created (schedule_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE schedule_deliveries (
    id                   BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    schedule_id          BIGINT UNSIGNED NOT NULL,
    channel              VARCHAR(20)     NOT NULL DEFAULT 'feishu',
    sender_identity_mode VARCHAR(20)     NOT NULL DEFAULT 'owner_user',
    target_type          VARCHAR(20)     NOT NULL,
    target_id            VARCHAR(128)    NOT NULL,
    target_name          VARCHAR(200)    NOT NULL DEFAULT '',
    content_mode         VARCHAR(20)     NOT NULL DEFAULT 'summary',
    enabled              TINYINT(1)      NOT NULL DEFAULT 1,
    created_at           DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at           DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uniq_schedule_delivery_target (schedule_id, channel, target_type, target_id),
    KEY idx_schedule_deliveries_schedule (schedule_id, enabled)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- 执行实体使用 BINARY(16) id，与 runs/outbox aggregate_id 对齐。
CREATE TABLE delivery_executions (
    id                   BINARY(16)      NOT NULL,
    occurrence_id        BIGINT UNSIGNED NOT NULL,
    run_id               BINARY(16)      NOT NULL,
    schedule_delivery_id BIGINT UNSIGNED NOT NULL,
    sender_user_id       BIGINT UNSIGNED NOT NULL,
    target_type          VARCHAR(20)     NOT NULL,
    target_id            VARCHAR(128)    NOT NULL,
    status               VARCHAR(20)     NOT NULL DEFAULT 'pending',
    external_message_id  VARCHAR(255)    NOT NULL DEFAULT '',
    attempt              INT UNSIGNED    NOT NULL DEFAULT 0,
    max_attempts         INT UNSIGNED    NOT NULL DEFAULT 5,
    next_attempt_at      DATETIME(3)     NULL,
    error_code           VARCHAR(64)     NOT NULL DEFAULT '',
    error_message        TEXT            NULL,
    created_at           DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    sent_at              DATETIME(3)     NULL,
    updated_at           DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uniq_delivery_occurrence_target (occurrence_id, schedule_delivery_id),
    KEY idx_deliveries_pending (status, next_attempt_at),
    KEY idx_deliveries_run (run_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
