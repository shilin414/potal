-- Repair the default-agent invariant (三次复审 P0-R1, 2026-09-18).
--
-- promoteDefaultTx used to run from Create() and Update() without the
-- eligibility checks the dedicated SetDefaultAgent endpoint applies, so rows
-- like these could exist and stay the workspace default:
--
--   * is_default_agent = 1 on a DISABLED application (PATCH
--     {enabled:false, set_default_agent:true} re-promoted the row it had
--     just disabled, because Update cleared the flag first and promoted
--     afterwards);
--   * on a non-chat application;
--   * on a chat application whose CURRENT enabled binding is gone, or whose
--     binding's provider is missing or not active.
--
-- The repair mirrors EXACTLY the in-transaction eligibility rule
-- promoteDefaultIfEligibleTx enforces from now on — including the
-- "current effective binding" choice: the NEWEST enabled binding wins
-- (the same anti-join GetEnabledBinding, the catalog page and the bootstrap
-- groups use), so a stale old enabled binding cannot keep a row eligible.
UPDATE applications a
LEFT JOIN (
    SELECT b.application_id, b.provider_key
    FROM runtime_bindings b
    LEFT JOIN runtime_bindings newer_b
      ON newer_b.application_id = b.application_id
     AND newer_b.enabled = 1
     AND newer_b.id > b.id
    WHERE b.enabled = 1
      AND newer_b.id IS NULL
) eb ON eb.application_id = a.id
LEFT JOIN providers p ON p.provider_key = eb.provider_key
SET a.is_default_agent = 0
WHERE a.is_default_agent = 1
  AND (
    a.enabled = 0
    OR a.kind <> 'chat'
    OR eb.application_id IS NULL
    OR p.id IS NULL
    OR p.status <> 'active'
  );
