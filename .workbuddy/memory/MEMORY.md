# Creation Agent Studio — 项目长期记忆

## Provider Admission & Release Gate Closure（2026-09-14 完成，最新）

变更报告：`docs/potal Provider Admission & Release Gate Closure 修复变更报告.md`。
执行内核（Ownership/Claim/Reaper/Finalize）已定型，**后续不要再改**；本轮只动准入层。

- **attempt 语义**：`runs.attempt = provider execution count`，**claim 不 +attempt**；
  唯一消耗点是 `Service.BeginProviderAttemptOwned`（Aily 在 Auth → Rate Limit → Begin 之后才 Submit）；
  预算耗尽返回 `ErrProviderAttemptsExhausted`；admission requeue 不消耗预算
- **ProviderSlot 是 attempt-scoped**：member = `{run_id}:{lease_epoch}:{lease_token}`，
  `Acquire(ctx, ExecutionOwnership)` 单 Lua、`Renew` XX-only（丢 slot → `ErrProviderSlotLost`，绝不重建）、
  `Release` 只 ZREM 精确 member；worker heartbeat **先 Run Lease 后 Slot**
- **队列布局**：`queue:<provider>:interactive|retry|scheduled` 三条 Stream，
  权重默认 7:1:2（`RUN_PRIORITY_WEIGHTS`），策略在 `internal/execution/priority.go` 的 `classScheduler`；
  outbox payload 必须带 `priority_class`；旧单流仅作兼容/兜底（无 class 的行、delivery 事件）
- **retry 时序**：`RetryOwnedRunAt(..., retryAt)` 单事务内 Run.available_at ≡ Outbox.available_at；
  provider 失败退避用 `RUN_REQUEUE_DELAY`，admission 竞争用 `AdmissionRequeueDelay=1s`
- **delivery 语义**：外部发送是 **At Least Once**（DB 侧 DeliveryExecution 幂等，外部副作用不保证）；
  `Sender.Send(ctx, DeliveryRequest)`，`IdempotencyKey = DeliveryExecution.ID`
- **配置权威**：provider 并发上限以 catalog `providers.max_inflight` 为权威，`AILY_MAX_INFLIGHT` 仅 bootstrap

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
- CI：`.github/workflows/backend.yml`（actionlint + gofmt/vet/build/unit + 真实 TiDB/Redis integration）、
  `.github/workflows/frontend.yml`（tsc/vitest/vite build）

## 本地验证注意事项

- 集成测试前先确认没有遗留的 studio-worker / studio-scheduler / studio-api 进程在共享 dev TiDB/Redis 上抢跑
- **测试里取 Redis/DB 配置一律用 `config.Load()`**（`.env.local` 里有密码）；只读 `REDIS_HOST` 之类的环境变量会 NOAUTH
- 改了 `db/queries/*.sql` 必须跑 `sqlc generate`（本机 `~/go/bin/sqlc` 可用），再 build
- 本机 Git Bash 下 pnpm shim 仍是坏的（`C:\c\Users\...` 路径错误）；前端项目用 package-lock.json，
  直接用 `npm test` / `npm run build`
- `go run` 在本机可能被杀软拦截，优先 `go build -o ./x.exe && ./x.exe`
