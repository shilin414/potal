CREATE TABLE directory_sync_configs (
    id BIGINT UNSIGNED NOT NULL,
    enabled TINYINT(1) NOT NULL DEFAULT 0,
    schedule_type VARCHAR(20) NOT NULL DEFAULT 'interval',
    interval_minutes INT UNSIGNED NOT NULL DEFAULT 360,
    daily_time VARCHAR(5) NOT NULL DEFAULT '02:00',
    timezone VARCHAR(64) NOT NULL DEFAULT 'Asia/Shanghai',
    next_run_at DATETIME(3) NULL,
    last_run_at DATETIME(3) NULL,
    last_success_at DATETIME(3) NULL,
    lease_owner VARCHAR(128) NULL,
    lease_until DATETIME(3) NULL,
    updated_by BIGINT UNSIGNED NULL,
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

INSERT INTO directory_sync_configs (id, enabled, schedule_type, interval_minutes, daily_time, timezone)
VALUES (1, 0, 'interval', 360, '02:00', 'Asia/Shanghai');

CREATE TABLE directory_sync_runs (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    trigger_type VARCHAR(20) NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'pending',
    departments_count INT UNSIGNED NOT NULL DEFAULT 0,
    users_count INT UNSIGNED NOT NULL DEFAULT 0,
    memberships_count INT UNSIGNED NOT NULL DEFAULT 0,
    started_at DATETIME(3) NULL,
    finished_at DATETIME(3) NULL,
    error_code VARCHAR(100) NOT NULL DEFAULT '',
    error_message VARCHAR(1000) NOT NULL DEFAULT '',
    created_by BIGINT UNSIGNED NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    KEY idx_directory_sync_runs_status (status, created_at),
    KEY idx_directory_sync_runs_created (created_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE directory_sync_department_stage (
    run_id BIGINT UNSIGNED NOT NULL,
    open_department_id VARCHAR(64) NOT NULL,
    name VARCHAR(255) NOT NULL DEFAULT '',
    parent_open_department_id VARCHAR(64) NOT NULL DEFAULT '0',
    order_weight VARCHAR(64) NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, open_department_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE directory_sync_user_stage (
    run_id BIGINT UNSIGNED NOT NULL,
    open_id VARCHAR(64) NOT NULL,
    name VARCHAR(255) NOT NULL DEFAULT '',
    avatar_url VARCHAR(1000) NOT NULL DEFAULT '',
    active_status INT NOT NULL DEFAULT 0,
    is_resigned TINYINT(1) NOT NULL DEFAULT 0,
    PRIMARY KEY (run_id, open_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE directory_sync_user_department_stage (
    run_id BIGINT UNSIGNED NOT NULL,
    user_open_id VARCHAR(64) NOT NULL,
    department_open_id VARCHAR(64) NOT NULL,
    is_primary TINYINT(1) NOT NULL DEFAULT 0,
    PRIMARY KEY (run_id, user_open_id, department_open_id),
    KEY idx_directory_sync_membership_department (run_id, department_open_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
