# Creation Agent Studio — 项目长期记忆

## 数据库基线 = MySQL 5.7（2026-09-14 切换完成，最新）

**TiDB 8.0.0 / TiProxy 已退出运行架构，不要再往仓库里加 TiDB/TiProxy 假设。**
变更报告：`docs/potal 从 TiDB 8.0.0 切换到 MySQL 5.7 执行变更报告.md`。

- 目标库：`192.168.211.26:20336` / 库 `xiaoan` / `test_user`；库级与 28 张表全部 `InnoDB`+`utf8mb4_bin`
- 代码默认值已改：`DB_PORT` 3306、`DB_NAME` `xiaoan`；**DSN 的 UTC 设计不能动**
  （`loc=UTC` + `time_zone='+00:00'` 是 Lease/Retry/Schedule 正确性的前提）
- 已删除：`tidbCompatibleDSN` / `tidb_skip_isolation_level_check` / 错误码 **9007**；
  **1213（deadlock）与 1205（lock wait timeout）必须保留**——InnoDB 一样会发生
- 测试开关：`STUDIO_TEST_TIDB` → **`STUDIO_TEST_DB`**（语义一直是"用真实数据库跑"）；
  `tests/integration/tidb_{cas,schedule}_test.go` → `database_{cas,schedule}_test.go`
- **CI 只有 2 个 job**：`check` + `integration`（mysql:5.7 + redis:7 唯一数据库闸门）。
  原 TiDB job 的覆盖（execution/delivery 包级真实 DB 测试）**已并入** `integration`，别再单独删
- CI 里 `./internal/platform/...` 是**故意**加的：`TestSessionTimezoneUTC`（断言
  `session time_zone = +00:00`）此前在 unit job 和 TiDB job 里都被静默跳过
- **迁移/校验的方法论见 skill `db-engine-switch-verify`**（含引擎间数据搬运、内容校验阶梯、
  以及踩过的 Error 3854 / TiDB 不支持 READ ONLY / seed 行自增 id 分叉等坑）
- 数据搬运用的临时工具（含凭据）**已删除**，不要提交进仓库；复现步骤在变更报告 §13
- 遗留：`sql_mode` 缺 `ONLY_FULL_GROUP_BY`（严格模式已在，需 DBA 补）；生产前按指南 §18 拆
  迁移账号/运行账号；库里带入的 `itest_*` 残留建议清理

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
- **CI（2026-09-14 起为两个 job）**：`.github/workflows/backend.yml` 的
  `check`=actionlint(pin v1.7.12)+gofmt/vet/build/unit/race、`integration`=mysql:5.7+redis:7 单一数据库闸门；
  迁移步骤用 `go run ./cmd/migrate`（`cmd/api -migrate` 会继续起 server，不可用于 CI）；
  **失败诊断靠 `::error` annotation**（job 日志需 admin，读
  `GET /repos/shilin414/potal/check-runs/<id>/annotations` 即可）
- **migration 是纯 MySQL 路径**：`app.MigrateUp` 不再探测服务端版本（老的 TiDB SERIALIZABLE 兼容已删）；
  迁移入口是 `cmd/migrate`，二次执行必须干净 no-op
- **reaper 恢复即时**：`RequeueRunFencedImmediate` + `CreateOutboxEvent`，可用时刻同取 DB 时钟
  （避免 app/DB 时钟偏差；曾因 `now+delay` 导致恢复出的 Run 1s 内不可 claim）

## Execution Correctness Closure（2026-09-14 完成）

- 复核报告中的 P0/P1/P2 本轮计划项已全部关闭；变更报告：
  `docs/potal Execution Correctness Closure 修复变更报告.md`
- 执行内核正确性结构：
  - `ExecutionOwnership` / `ClaimedRun` / `WorkerOwnedService` 是 Worker 唯一 canonical 写面，
    `GetRun()` 刷新永远不能覆盖 ownership
  - 终态事件只有 `run.completed` / `run.failed` / `run.cancelled`；`run.retrying` 永远非终态，
    前端收到后保留 `activeRunId` 并继续 streaming
  - `RecoverExpiredLeases`、`RetryOwnedRun`、`FinalizeOwnedRun` 均为单数据库事务，
    保证 running ⇔ active lease、terminal run ⇔ terminal event + assistant message
  - Scheduler admission 使用 schedules 行锁 + FIFO（`ORDER BY enqueued_at, id`），单次只 admit 一个
  - Admin 本地登录强制 `is_staff || is_superuser`，带限流与审计；旧 `/api/auth/login/` deprecated + staff-only
- 数据库迁移已到 `0010_run_lease_epoch_link`（`run_leases.lease_epoch` 与 `runs.lease_epoch` 对齐）
- Invariant watchdog：`backend-go/cmd/invariant-checker`（A–G 七类 invariant，detect/metric/log/alert，
  不自动修复）
- CI：`.github/workflows/backend.yml`（actionlint + gofmt/vet/build/unit + 真实 MySQL 5.7/Redis integration）、
  `.github/workflows/frontend.yml`（tsc/vitest/vite build）

## 本地验证注意事项

- 集成测试前先确认没有遗留的 studio-worker / studio-scheduler / studio-api 进程在共享 dev 库/Redis 上抢跑；
  集成测试**会往目标库里写测试数据**，跑完若要交付「纯迁移态」必须还原（工具会先报告将丢弃多少 target-only 行）
- **测试里取 Redis/DB 配置一律用 `config.Load()`**（`.env.local` 里有密码）；只读 `REDIS_HOST` 之类的环境变量会 NOAUTH
- 改了 `db/queries/*.sql` 必须跑 `sqlc generate`（本机 `~/go/bin/sqlc` 可用），再 build
- 本机 Git Bash 下 pnpm shim 仍是坏的（`C:\c\Users\...` 路径错误）；前端项目用 package-lock.json，
  直接用 `npm test` / `npm run build`
- `go run` 在本机可能被杀软拦截，优先 `go build -o ./x.exe && ./x.exe`
