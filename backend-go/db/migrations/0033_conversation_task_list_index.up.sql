CREATE INDEX idx_conversations_user_updated
ON conversations (user_id, updated_at, id);
