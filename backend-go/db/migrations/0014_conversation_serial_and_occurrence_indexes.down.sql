ALTER TABLE schedule_occurrences
    DROP KEY uniq_occurrence_run,
    DROP KEY idx_occ_status_scheduled,
    DROP KEY idx_occ_schedule_status;

ALTER TABLE runs
    DROP KEY idx_runs_conversation_status,
    DROP KEY idx_runs_user_status;

ALTER TABLE conversations
    DROP KEY idx_conversations_user_updated;

ALTER TABLE messages
    DROP KEY idx_messages_conversation_id;
