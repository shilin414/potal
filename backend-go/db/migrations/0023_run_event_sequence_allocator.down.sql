-- The allocator column is an implementation detail of sequence assignment;
-- dropping it does not change any stored run_events row (the events keep the
-- sequence numbers the allocator handed out).
ALTER TABLE runs
    DROP COLUMN next_event_sequence;
