-- 0006: runs trigger provenance columns (schedule automation).
-- 约束：ALTER 的 AFTER 子句不引用本批（multi-statement）内新加的列，
-- 因此只有 trigger_type 可以接在既有列后，其余列追加到表尾；
-- 索引则放在下一个 migration 批次（0007）。

ALTER TABLE runs
    ADD COLUMN trigger_type VARCHAR(32) NOT NULL DEFAULT 'interactive_user' AFTER max_attempts;

ALTER TABLE runs
    ADD COLUMN trigger_id   BIGINT UNSIGNED NULL;

ALTER TABLE runs
    ADD COLUMN priority     VARCHAR(32) NOT NULL DEFAULT 'interactive_user';

ALTER TABLE runs
    ADD COLUMN available_at DATETIME(3) NULL;
