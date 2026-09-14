DROP TABLE IF EXISTS occurrence_delivery_expectations;

ALTER TABLE schedule_occurrences
    DROP COLUMN delivery_snapshot_at;

