# Creation Agent Studio — 项目长期记忆

## Execution Correctness Closure（2026-09-14 完成）

- 复核报告中的 P0/P1/P2 本轮计划项已全部关闭；变更报告：
  `docs/potal Execution Correctness Closure 修复变更报告.md`
- 执行内核正确性结构：
  - `ExecutionOwnership` / `ClaimedRun` / `WorkerOwnedService` 是 Worker 唯一 canonical 写面，
    `GetRun()` 刷新永远不能覆盖 ownership
  - 终态事件只有 `run.completed` / `run.failed` / `run.cancelled`；`run.retrying` 永远非终态，
    前端收到后保留 `activeRunId` 并继续 streaming
  - `RecoverExpiredLeases`、`RetryOwnedRun`、`FinalizeOwnedRun` 均为单 TiDB 事务，
    保证 running ⇔ active lease、terminal run ⇔ terminal event + assistant message
  - Scheduler admission 使用 schedules 行锁 + FIFO（`ORDER BY enqueued_at, id`），单次只 admit 一个
  - Admin 本地登录强制 `is_staff || is_superuser`，带限流与审计；旧 `/api/auth/login/` deprecated + staff-only
- 数据库迁移已到 `0010_run_lease_epoch_link`（`run_leases.lease_epoch` 与 `runs.lease_epoch` 对齐）
- Invariant watchdog：`backend-go/cmd/invariant-checker`（A–G 七类 invariant，detect/metric/log/alert，
  不自动修复）
- CI：`.github/workflows/backend.yml`（gofmt/vet/build/unit + 真实 TiDB/Redis integration）、
  `.github/workflows/frontend.yml`（tsc/vitest/vite build）

## 本地验证注意事项

- 集成测试前先确认没有遗留的 studio-worker / studio-scheduler / studio-api 进程在共享 dev TiDB/Redis 上抢跑
- 本机 Git Bash 下 pnpm shim 仍是坏的（`C:\c\Users\...` 路径错误）；前端项目用 package-lock.json，
  直接用 `npm test` / `npm run build`
- `go run` 在本机可能被杀软拦截，优先 `go build -o ./x.exe && ./x.exe`
