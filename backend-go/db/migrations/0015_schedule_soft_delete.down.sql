ALTER TABLE schedules
    DROP KEY idx_schedules_owner_alive,
    DROP COLUMN deleted_at;
