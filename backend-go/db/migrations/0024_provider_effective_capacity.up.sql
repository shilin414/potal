-- Provider EFFECTIVE capacity: the index the admission decision now needs
-- (第九轮补丁 3.3-A).
--
-- Until 3.3, admission sized itself from provider_execution_slots alone:
--
--     SELECT COUNT(*) FROM provider_execution_slots
--     WHERE provider = ? AND expires_at > CURRENT_TIMESTAMP(3)
--
-- That count is only the locally CONTROLLED portion of real provider work.
-- A provider request leaves the local lease behind the moment it enters
--
--     sending → unknown → accepted
--
-- and the external execution can outlive — or never have been covered by —
-- the slot:
--
--     Run A: slot exists → provider accepts chat-123 (submission=accepted)
--            worker crashes → slot expires → chat-123 keeps running
--     provider_execution_slots = 0, real provider executions = 1
--
-- so "active slot count" is NOT a safe upper bound. 3.3 therefore makes
-- provider_submissions part of the capacity fact, because that table is
-- already the durable ledger of provider-side side effects (migration 0022)
-- and adding a second uncertain-slot table would create two sources of truth.
--
-- The decision query is a DISTINCT union over both sources:
--
--     SELECT s.run_id FROM provider_execution_slots s WHERE ... UNION
--     SELECT ps.run_id FROM provider_submissions ps JOIN runs r ...
--
-- There is no index that serves the second branch. provider_submissions
-- carries only
--
--     PRIMARY KEY (run_id, submission_no)
--     UNIQUE KEY uniq_provider_submit_key (provider, idempotency_key)
--
-- so `WHERE provider = ? AND state IN (...)` can neither use the primary key
-- (run_id leads) nor the unique key (idempotency_key leads): it degrades into
-- a full table scan. Admission runs on EVERY claim, while the table only
-- grows (a submission row is history and is never deleted when its run
-- settles), so the scan gets slower exactly as the system gets busier.
--
-- (provider, state, run_id) resolves the filter and carries run_id so the
-- candidate rows are read straight from the secondary index — no lookup back
-- into the clustered index for the join to runs.
--
-- The index is additive: no column, row or state semantics change, so it is
-- safe to apply while 0022-era code is still online.
ALTER TABLE provider_submissions
ADD KEY idx_provider_submissions_capacity (
    provider,
    state,
    run_id
);
