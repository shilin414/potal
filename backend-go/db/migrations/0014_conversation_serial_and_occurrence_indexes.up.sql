-- Conversation admission (P0-2) & schedule occurrence runtime indexes (P1-6).
--
-- 1. run admission check inside CreateRunInTx counts active runs per
--    conversation; (conversation_id, status) makes that lookup index-only.
--    MySQL 5.7 UNIQUE on run_id (NULL for legacy/manual rows) absorbs the
--    "one occurrence ⇔ one run" link: multiple NULLs are allowed.
-- 2. (status, scheduled_at, id) serves ListAdmissiblePendingOccurrences
--    and ListStuckPendingOccurrences scans.
-- 3. (schedule_id, status) serves HasActiveOccurrence /
--    CountActiveOccurrencesExcluding without scanning history.
ALTER TABLE schedule_occurrences
    ADD UNIQUE KEY uniq_occurrence_run (run_id),
    ADD KEY idx_occ_status_scheduled (status, scheduled_at, id),
    ADD KEY idx_occ_schedule_status (schedule_id, status);

-- Conversation-serial admission counter (P0-2): the CreateRunInTx path
-- locks the conversations row FOR UPDATE and re-reads the active-run
-- count; without an index on (conversation_id, status) that count would
-- degenerate into a scan over the conversation's whole run history.
ALTER TABLE runs
    ADD KEY idx_runs_conversation_status (conversation_id, status);

-- Per-user outstanding-run admission (P1-7): COUNT queued/running per user.
ALTER TABLE runs
    ADD KEY idx_runs_user_status (user_id, status);

-- Sidebar (评测 §十三): the sidebar orders by updated_at and resolves the
-- latest message per conversation; both need a matching index to stay
-- cheap as data grows.
ALTER TABLE conversations
    ADD KEY idx_conversations_user_updated (user_id, updated_at, id);

ALTER TABLE messages
    ADD KEY idx_messages_conversation_id (conversation_id, id);

