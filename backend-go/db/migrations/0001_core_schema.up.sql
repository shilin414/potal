-- Creation Agent Studio — clean target schema (Go backend).
--
-- Constraints honoured throughout:
--   * MySQL 5.7 compatible: no MySQL-8-only syntax, no functional indexes,
--     no DEFAULT on TEXT/JSON columns, utf8mb4, DATETIME(3) UTC.
--   * Run-domain resources use app-generated UUIDv7 stored BINARY(16)
--     (never NULL); conversion happens in Go, SQL stays portable.
--   * Every hot query path has a covering index (EXPLAIN-verified in
--     integration tests).

SET NAMES utf8mb4;

-- ─────────────────────────────────────────────────────────── identity ──

CREATE TABLE users (
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    username        VARCHAR(150)    NOT NULL,
    password_hash   VARCHAR(255)    NOT NULL DEFAULT '',
    display_name    VARCHAR(255)    NOT NULL DEFAULT '',
    display_id      VARCHAR(64)     NOT NULL DEFAULT '',
    email           VARCHAR(254)    NOT NULL DEFAULT '',
    avatar_url      VARCHAR(1000)   NOT NULL DEFAULT '',
    bio             TEXT            NULL,
    role            VARCHAR(20)     NOT NULL DEFAULT 'creator',
    auth_source     VARCHAR(20)     NOT NULL DEFAULT 'feishu',
    is_staff        TINYINT(1)      NOT NULL DEFAULT 0,
    is_active       TINYINT(1)      NOT NULL DEFAULT 1,
    created_at      DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at      DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uniq_users_username (username)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE feishu_identities (
    id                         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id                    BIGINT UNSIGNED NOT NULL,
    open_id                    VARCHAR(64)     NULL,
    union_id                   VARCHAR(64)     NOT NULL DEFAULT '',
    feishu_user_id             VARCHAR(64)     NULL,
    display_name               VARCHAR(255)    NOT NULL DEFAULT '',
    avatar_url                 VARCHAR(1000)   NOT NULL DEFAULT '',
    refresh_token_enc          TEXT            NULL,
    refresh_token_expires_at   DATETIME(3)     NULL,
    last_login_at              DATETIME(3)     NULL,
    created_at                 DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at                 DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uniq_feishu_user (user_id),
    UNIQUE KEY uniq_feishu_open_id (open_id),
    KEY idx_feishu_user_id (feishu_user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- ───────────────────────────────────────────────────────────── catalog ──

CREATE TABLE application_categories (
    id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    slug        VARCHAR(100)    NOT NULL,
    name        VARCHAR(100)    NOT NULL,
    description TEXT            NULL,
    icon        VARCHAR(50)     NOT NULL DEFAULT '',
    sort_order  INT             NOT NULL DEFAULT 0,
    created_at  DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at  DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uniq_category_slug (slug),
    UNIQUE KEY uniq_category_name (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE providers (
    id                     BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    provider_key           VARCHAR(64)     NOT NULL,
    name                   VARCHAR(120)    NOT NULL,
    description            TEXT            NULL,
    supported_runtime_types JSON          NULL,
    capabilities           JSON            NULL,
    start_rate_limit       VARCHAR(32)     NOT NULL DEFAULT '',
    max_inflight           INT UNSIGNED    NOT NULL DEFAULT 100,
    poll_rate_limit        VARCHAR(32)     NOT NULL DEFAULT '',
    artifact_rate_limit    VARCHAR(32)     NOT NULL DEFAULT '',
    timeout_seconds        INT UNSIGNED    NOT NULL DEFAULT 300,
    retry_policy           JSON            NULL,
    circuit_breaker        JSON            NULL,
    secret_ref             VARCHAR(200)    NOT NULL DEFAULT '',
    base_url               VARCHAR(500)    NOT NULL DEFAULT '',
    status                 VARCHAR(20)     NOT NULL DEFAULT 'active',
    created_at             DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at             DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uniq_provider_key (provider_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE applications (
    id               BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    slug             VARCHAR(100)    NOT NULL,
    name             VARCHAR(100)    NOT NULL,
    description      TEXT            NULL,
    icon             VARCHAR(50)     NOT NULL DEFAULT '',
    avatar_key       VARCHAR(500)    NOT NULL DEFAULT '',
    color            VARCHAR(20)     NOT NULL DEFAULT '',
    kind             VARCHAR(20)     NOT NULL DEFAULT 'chat',
    renderer_key     VARCHAR(100)    NOT NULL DEFAULT '',
    executor_key     VARCHAR(100)    NOT NULL DEFAULT '',
    category_id      BIGINT UNSIGNED NULL,
    is_public        TINYINT(1)      NOT NULL DEFAULT 1,
    is_default_agent TINYINT(1)      NOT NULL DEFAULT 0,
    usage_count      INT UNSIGNED    NOT NULL DEFAULT 0,
    tags             JSON            NULL,
    default_config   JSON            NULL,
    created_by       BIGINT UNSIGNED NULL,
    organization_id  BIGINT UNSIGNED NULL,
    created_at       DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at       DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uniq_application_slug (slug),
    KEY idx_applications_category (category_id),
    KEY idx_applications_created_by (created_by),
    KEY idx_applications_kind_public (kind, is_public, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE application_favorites (
    id             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id        BIGINT UNSIGNED NOT NULL,
    application_id BIGINT UNSIGNED NOT NULL,
    created_at     DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uniq_favorite (user_id, application_id),
    KEY idx_favorites_application (application_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE runtime_bindings (
    id                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    application_id      BIGINT UNSIGNED NOT NULL,
    provider_id         BIGINT UNSIGNED NULL,
    provider_key        VARCHAR(64)     NOT NULL,
    runtime_type        VARCHAR(20)     NOT NULL DEFAULT 'agent',
    external_resource_id VARCHAR(255)   NOT NULL DEFAULT '',
    endpoint_key        VARCHAR(120)    NOT NULL DEFAULT '',
    identity_mode       VARCHAR(20)     NOT NULL DEFAULT 'user',
    execution_mode      VARCHAR(20)     NOT NULL DEFAULT 'interactive',
    session_policy      VARCHAR(20)     NOT NULL DEFAULT 'lazy',
    artifact_policy     VARCHAR(30)     NOT NULL DEFAULT 'external_refresh',
    capabilities        JSON            NULL,
    input_schema        JSON            NULL,
    output_schema       JSON            NULL,
    config              JSON            NULL,
    secret_ref          VARCHAR(200)    NOT NULL DEFAULT '',
    timeout_seconds     INT UNSIGNED    NOT NULL DEFAULT 300,
    enabled             TINYINT(1)      NOT NULL DEFAULT 1,
    created_at          DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at          DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uniq_binding_per_runtime (application_id, runtime_type, provider_key),
    KEY idx_bindings_application_enabled (application_id, enabled),
    KEY idx_bindings_provider_runtime (provider_key, runtime_type)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- ──────────────────────────────────────────────────────── conversation ──

CREATE TABLE conversations (
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id         BIGINT UNSIGNED NOT NULL,
    application_id  BIGINT UNSIGNED NULL,
    organization_id BIGINT UNSIGNED NULL,
    title           VARCHAR(200)    NOT NULL DEFAULT '',
    created_at      DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at      DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    KEY idx_conversations_user_app_updated (user_id, application_id, updated_at),
    KEY idx_conversations_application (application_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE agent_threads (
    id               BINARY(16)      NOT NULL,
    conversation_id  BIGINT UNSIGNED NOT NULL,
    provider         VARCHAR(32)     NOT NULL DEFAULT '',
    remote_id        VARCHAR(255)    NOT NULL DEFAULT '',
    status           VARCHAR(20)     NOT NULL DEFAULT 'idle',
    auth_mode        VARCHAR(20)     NOT NULL DEFAULT '',
    auth_subject_key VARCHAR(128)    NOT NULL DEFAULT '',
    config           JSON            NULL,
    created_at       DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at       DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uniq_thread_conversation (conversation_id),
    KEY idx_threads_provider_remote (provider, remote_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE messages (
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    conversation_id BIGINT UNSIGNED NOT NULL,
    role            VARCHAR(20)     NOT NULL,
    content         MEDIUMTEXT      NOT NULL,
    metadata        JSON            NULL,
    created_at      DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    KEY idx_messages_conversation_created (conversation_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- ─────────────────────────────────────────────────────────── execution ──

CREATE TABLE runs (
    id                    BINARY(16)      NOT NULL,
    user_id               BIGINT UNSIGNED NULL,
    application_id        BIGINT UNSIGNED NULL,
    conversation_id       BIGINT UNSIGNED NULL,
    runtime_binding_id    BIGINT UNSIGNED NULL,
    organization_id       BIGINT UNSIGNED NULL,
    provider              VARCHAR(64)     NOT NULL,
    runtime_type          VARCHAR(20)     NOT NULL DEFAULT '',
    external_run_id       VARCHAR(255)    NOT NULL DEFAULT '',
    status                VARCHAR(20)     NOT NULL DEFAULT 'queued',
    provider_status       VARCHAR(64)     NOT NULL DEFAULT '',
    provider_finish_reason VARCHAR(64)    NOT NULL DEFAULT '',
    input                 JSON            NULL,
    output                JSON            NULL,
    runtime_snapshot      JSON            NULL,
    attempt               INT UNSIGNED    NOT NULL DEFAULT 0,
    max_attempts          INT UNSIGNED    NOT NULL DEFAULT 3,
    queued_at             DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    started_at            DATETIME(3)     NULL,
    finished_at           DATETIME(3)     NULL,
    error_code            VARCHAR(64)     NOT NULL DEFAULT '',
    error_message         TEXT            NULL,
    created_at            DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at            DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    KEY idx_runs_claim (status, provider, queued_at),
    KEY idx_runs_conversation_created (conversation_id, created_at),
    KEY idx_runs_user_created (user_id, created_at),
    KEY idx_runs_external (provider, external_run_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE run_events (
    id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    run_id     BINARY(16)      NOT NULL,
    sequence   BIGINT UNSIGNED NOT NULL,
    event_type VARCHAR(64)     NOT NULL,
    payload    JSON            NULL,
    created_at DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uniq_run_event_sequence (run_id, sequence),
    KEY idx_run_events_type (event_type)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE run_commands (
    id           BINARY(16)      NOT NULL,
    run_id       BINARY(16)      NOT NULL,
    command_type VARCHAR(20)     NOT NULL,
    payload      JSON            NULL,
    status       VARCHAR(20)     NOT NULL DEFAULT 'pending',
    created_by   BIGINT UNSIGNED NULL,
    created_at   DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    resolved_at  DATETIME(3)     NULL,
    PRIMARY KEY (id),
    KEY idx_run_commands_run (run_id, status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE run_leases (
    id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    run_id       BINARY(16)      NOT NULL,
    worker_id    VARCHAR(128)    NOT NULL,
    lease_token  BINARY(16)      NOT NULL,
    acquired_at  DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    heartbeat_at DATETIME(3)     NULL,
    expires_at   DATETIME(3)     NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uniq_lease_run (run_id),
    KEY idx_leases_expires (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE runtime_attachments (
    id                    BINARY(16)      NOT NULL,
    run_id                BINARY(16)      NULL,
    conversation_id       BIGINT UNSIGNED NULL,
    provider              VARCHAR(64)     NOT NULL,
    external_attachment_id VARCHAR(255)   NOT NULL DEFAULT '',
    attachment_type       VARCHAR(20)     NOT NULL DEFAULT 'file',
    name                  VARCHAR(255)    NOT NULL DEFAULT '',
    source_type           VARCHAR(20)     NOT NULL DEFAULT 'upload',
    source_url            VARCHAR(512)    NOT NULL DEFAULT '',
    storage_key           VARCHAR(500)    NOT NULL DEFAULT '',
    content_type          VARCHAR(120)    NOT NULL DEFAULT '',
    size_bytes            BIGINT UNSIGNED NOT NULL DEFAULT 0,
    auth_mode             VARCHAR(20)     NOT NULL DEFAULT '',
    auth_subject_key      VARCHAR(128)    NOT NULL DEFAULT '',
    status                VARCHAR(20)     NOT NULL DEFAULT 'pending',
    metadata              JSON            NULL,
    created_by            BIGINT UNSIGNED NULL,
    created_at            DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at            DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    KEY idx_attachments_provider_external (provider, external_attachment_id),
    KEY idx_attachments_user_created (created_by, created_at),
    KEY idx_attachments_run (run_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE run_artifacts (
    id                    BINARY(16)      NOT NULL,
    run_id                BINARY(16)      NOT NULL,
    provider              VARCHAR(64)     NOT NULL,
    external_artifact_id  VARCHAR(255)    NOT NULL DEFAULT '',
    provider_artifact_type VARCHAR(64)    NOT NULL DEFAULT '',
    name                  VARCHAR(255)    NOT NULL DEFAULT '',
    normalized_type       VARCHAR(32)     NOT NULL DEFAULT 'file',
    storage_type          VARCHAR(20)     NOT NULL DEFAULT 'external',
    cached_external_url   TEXT            NULL,
    cached_url_fetched_at DATETIME(3)     NULL,
    cached_url_expires_at DATETIME(3)     NULL,
    storage_key           VARCHAR(500)    NOT NULL DEFAULT '',
    resolution_status     VARCHAR(20)     NOT NULL DEFAULT 'pending',
    metadata              JSON            NULL,
    created_at            DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at            DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uniq_artifact_external (run_id, external_artifact_id),
    KEY idx_artifacts_provider_external (provider, external_artifact_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- ──────────────────────────────────────────────────────────── outbox ──

CREATE TABLE outbox_events (
    id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    aggregate    VARCHAR(32)     NOT NULL,
    aggregate_id BINARY(16)      NOT NULL,
    event_type   VARCHAR(64)     NOT NULL,
    payload      JSON            NULL,
    status       VARCHAR(20)     NOT NULL DEFAULT 'pending',
    available_at DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    published_at DATETIME(3)     NULL,
    retry_count  INT UNSIGNED    NOT NULL DEFAULT 0,
    last_error   TEXT            NULL,
    created_at   DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    KEY idx_outbox_dispatch (status, available_at, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- ────────────────────────────────────────────────────────── governance ──

CREATE TABLE audit_logs (
    id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id      BIGINT UNSIGNED NULL,
    action       VARCHAR(64)     NOT NULL,
    resource     VARCHAR(64)     NOT NULL DEFAULT '',
    resource_id  VARCHAR(64)     NOT NULL DEFAULT '',
    detail       JSON            NULL,
    created_at   DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    KEY idx_audit_user_created (user_id, created_at),
    KEY idx_audit_resource (resource, resource_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE quota_policies (
    id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    scope        VARCHAR(32)     NOT NULL,
    scope_key    VARCHAR(64)     NOT NULL DEFAULT '',
    metric       VARCHAR(32)     NOT NULL,
    limit_value  BIGINT          NOT NULL DEFAULT 0,
    period       VARCHAR(32)     NOT NULL DEFAULT 'day',
    enabled      TINYINT(1)      NOT NULL DEFAULT 1,
    created_at   DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at   DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    KEY idx_quota_scope (scope, scope_key, metric)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
