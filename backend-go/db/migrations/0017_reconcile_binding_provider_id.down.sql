-- Irreversible data repair: the previous (wrong) provider_id values
-- cannot be reconstructed, and 0016 already made NULL rows
-- unrecoverable. The authoritative mapping is provider_key.
SELECT 1;
