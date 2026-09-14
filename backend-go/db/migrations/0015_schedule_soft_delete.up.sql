-- Schedule soft delete (评测 §十二): deleting a schedule must not break
-- delivery executions that are still pending — the Feishu worker resolves
-- schedule.Name to build the message and would otherwise retry into a
-- foreign-key hole until the row is gone (history was kept, name was not).
--
-- deleted_at IS NULL is the new liveness predicate for the scheduler scan.
ALTER TABLE schedules
    ADD COLUMN deleted_at DATETIME(3) NULL AFTER last_run_at,
    ADD KEY idx_schedules_owner_alive (owner_user_id, deleted_at, id);
