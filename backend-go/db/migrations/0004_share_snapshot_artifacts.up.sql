-- The share snapshot now carries per-message artifact references so public
-- viewers can resolve provider files through the token-scoped /open endpoint.
-- Still metadata only — file bytes keep living at the provider.
ALTER TABLE conversation_shares CHANGE COLUMN message_ids snapshot JSON NOT NULL;
