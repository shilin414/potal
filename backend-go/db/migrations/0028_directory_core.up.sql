CREATE TABLE directory_departments (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    open_department_id VARCHAR(64) NOT NULL,
    name VARCHAR(255) NOT NULL DEFAULT '',
    parent_id BIGINT UNSIGNED NULL,
    parent_open_department_id VARCHAR(64) NOT NULL DEFAULT '0',
    order_weight VARCHAR(64) NOT NULL DEFAULT '',
    is_active TINYINT(1) NOT NULL DEFAULT 1,
    sync_generation BIGINT UNSIGNED NULL,
    last_synced_at DATETIME(3) NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uniq_directory_department_open_id (open_department_id),
    KEY idx_directory_department_parent (parent_id),
    KEY idx_directory_department_active (is_active, name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE directory_users (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    open_id VARCHAR(64) NOT NULL,
    name VARCHAR(255) NOT NULL DEFAULT '',
    avatar_url VARCHAR(1000) NOT NULL DEFAULT '',
    active_status INT NOT NULL DEFAULT 0,
    is_resigned TINYINT(1) NOT NULL DEFAULT 0,
    local_user_id BIGINT UNSIGNED NULL,
    is_active TINYINT(1) NOT NULL DEFAULT 1,
    sync_generation BIGINT UNSIGNED NULL,
    last_synced_at DATETIME(3) NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uniq_directory_user_open_id (open_id),
    UNIQUE KEY uniq_directory_user_local (local_user_id),
    KEY idx_directory_user_active_name (is_active, name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE directory_user_departments (
    directory_user_id BIGINT UNSIGNED NOT NULL,
    department_id BIGINT UNSIGNED NOT NULL,
    is_primary TINYINT(1) NOT NULL DEFAULT 0,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (directory_user_id, department_id),
    KEY idx_directory_membership_department (department_id, directory_user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE directory_department_closure (
    ancestor_id BIGINT UNSIGNED NOT NULL,
    descendant_id BIGINT UNSIGNED NOT NULL,
    depth INT UNSIGNED NOT NULL,
    PRIMARY KEY (ancestor_id, descendant_id),
    KEY idx_directory_closure_descendant (descendant_id, ancestor_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
