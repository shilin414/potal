-- Backfill only the latest historical success row. Older rows are not used as
-- the shrink baseline. Existing deployments created before 0031 otherwise
-- have zeros in the new active-count columns and would skip the first guard.
UPDATE directory_sync_runs r
JOIN (
  SELECT id
  FROM (
    SELECT id
    FROM directory_sync_runs
    WHERE status = 'success'
    ORDER BY id DESC
    LIMIT 1
  ) latest_success_inner
) latest_success ON latest_success.id = r.id
SET r.active_users_count = (
      SELECT COUNT(*) FROM directory_users WHERE is_active = 1
    ),
    r.active_memberships_count = (
      SELECT COUNT(*)
      FROM directory_user_departments dud
      JOIN directory_users du ON du.id = dud.directory_user_id
      WHERE du.is_active = 1
    )
WHERE r.active_users_count = 0
  AND r.active_memberships_count = 0;
