ALTER TABLE directory_sync_runs
  ADD COLUMN active_users_count INT UNSIGNED NOT NULL DEFAULT 0 AFTER users_count,
  ADD COLUMN active_memberships_count INT UNSIGNED NOT NULL DEFAULT 0 AFTER memberships_count;
