# Findings

- Current worktree only has two untracked audit/report documents before this task.
- Prior implementation already added `ExecutionOwnership`, `ClaimedRun`, `WorkerOwnedService`, atomic retry/recovery/finalize, scheduler row-lock admission, and CI workflows.
- Final audit's remaining P0s: strict active vs finalize ownership, external run ID fencing, lease delete errors, occurrence convergence, and executable GitHub backend integration CI.
- P1s: durable delivery outbox, provider priority admission, distributed max_inflight, cancelled event mapping, MySQL 5.7 CI, and schedule/run invariants.
- User explicitly excluded git history, repository security, and secret rotation work.
- `schedule_occurrences.run_id` is BINARY(16) but sqlc exposes it as sql.NullString; production call sites pass the raw 16 bytes as a string. Tests/helpers must do the same.
- Shared integration TiDB has historical NULL `schedules.trigger_config`; scheduler reads now use `COALESCE(..., '{}')`.
- Existing integration suite passes against real TiDB/Redis after the fixes.
