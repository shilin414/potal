-- Irreversible data repair: the previous NULL provider_id values cannot be
-- reconstructed (and are meaningless once the gate joins on provider_key).
SELECT 1;
