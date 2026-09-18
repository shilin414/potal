ALTER TABLE applications
  ADD COLUMN access_mode VARCHAR(20) NOT NULL DEFAULT 'admin_only' AFTER is_public,
  ADD KEY idx_applications_access (kind, enabled, access_mode, created_at, id);

UPDATE applications
SET access_mode = CASE WHEN is_public = 1 THEN 'all' ELSE 'admin_only' END;

CREATE TABLE application_department_grants (
    application_id BIGINT UNSIGNED NOT NULL,
    department_id BIGINT UNSIGNED NOT NULL,
    include_children TINYINT(1) NOT NULL DEFAULT 1,
    created_by BIGINT UNSIGNED NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (application_id, department_id),
    KEY idx_app_department_grant_department (department_id, application_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE application_user_grants (
    application_id BIGINT UNSIGNED NOT NULL,
    directory_user_id BIGINT UNSIGNED NOT NULL,
    created_by BIGINT UNSIGNED NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (application_id, directory_user_id),
    KEY idx_app_user_grant_user (directory_user_id, application_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
