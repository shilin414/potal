-- 0006 down: restore runs to pre-schedule shape.

ALTER TABLE runs
    DROP COLUMN available_at,
    DROP COLUMN priority,
    DROP COLUMN trigger_id,
    DROP COLUMN trigger_type;
