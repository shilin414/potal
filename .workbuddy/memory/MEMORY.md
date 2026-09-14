# Creation Agent Studio — 项目长期记忆

## Admission 闭环：Conversation / Schedule / 用户层（2026-09-15 完成，最新）

变更报告：`docs/potal Conversation 与 Schedule 准入闭环修复变更报告.md`。
**执行内核与 Provider 准入层不要动**；本轮补的是「用户 → Conversation → Run」与
「用户 → Schedule → Occurrence → Run → Delivery」这两条链上的准入/生命周期边界。

- **P0-1 执行准入只有一个门**：`catalog.Service.AuthorizeExecution(ctx, appID, userID, isStaff)`
  （`internal/catalog/authz.go`，底层 SQL `GetExecutionAuthBundle`）。判定：
  app 存在 + `enabled=1` + `kind='chat'` + 普通用户要求 `is_public=1` + binding enabled + provider active。
  **停用应用对任何人都不可执行（含 staff）**；staff 仅可执行 private。
  **新增执行入口必须接这个门**，别再用 `EnabledBinding` 当权限判断。
  错误分级：普通用户一律 404（不泄漏存在性），staff 精确原因。
- **Scheduler 每次触发都重新授权**（按 schedule owner 身份，`OwnerAwareResolver.EnabledBindingFor`）；
  不可执行 → 记一条 failed occurrence、**0 Run**、并推进 next_run_at（不重试同槽）。
- **P0-2 一个 conversation 同时只允许一个非终态 Run**：串行化在 `execution.CreateRunInTx`
  （`conversations` 行 `FOR UPDATE` + `CountActiveRunsByConversation`）→ `ErrConversationBusy` → HTTP **409**。
  这是**串行语义不是排队**（后续消息会被拒，需重发）；要排队得加 `conversation_turn_seq`。
  `EnsureAgentThread` 已改成原子 get-or-create（UNIQUE 冲突重读胜者）。
- **Schedule admission 已统一到 schedules 行锁**：`GetScheduleRowForUpdate` 返回**整行**，
  triggerSchedule / TriggerNow / admitOne 全是「锁内重读最新行 → 判定 → 创建」同一事务。
  → `/disable` 返回后必无新 occurrence/run；PATCH prompt 立即生效；
  tick 与 run-now 共享同一把锁（同 Schedule 最多一个 active occurrence）。
  旧的非事务 helper（`recordSlot`/`skipPast`/`fireMisfiredOnce`/`createOccurrenceAndRun`）已删除，
  现在是 `skipPastInTx`/`fireMisfiredOnceInTx`/`createOccurrenceAndRunTx`/`enqueuePendingOccurrenceTx`。
- **附件 claim 在 CreateRun 事务内**：`ClaimAttachmentForRun`（`run_id IS NULL AND status='pending' AND created_by=?`），
  `RowsAffected!=1` → `ErrAttachmentClaimed` → 整体回滚（409）。惰性建 conversation 也移入该事务。
- **会话生命周期**：Clear = 删 messages **+ agent_thread**（=重新开始，下次新 Aily session）；
  Clear/Delete 在有活跃 Run 时都 **409**；Delete 是单事务级联（不再 `_ =` 吞错）。
- **Schedule 是软删除**（`schedules.deleted_at`，迁移 0015）：扫描/owner 列表/`schedule.Get` 全部过滤
  `deleted_at IS NULL`，但 `GetScheduleByID` **故意不过滤**——delivery worker 还要靠它取 `Name`。
  改这个查询时注意别把投递读路径一起过滤掉。
- **用户级准入**：`RUN_USER_QPS`(5/s, Redis GCRA `rate:runs:user:<id>`) +
  `RUN_USER_MAX_OUTSTANDING`(20) + `SCHEDULE_USER_MAX`(50) → 429。
- **消息落库必须同事务 touch `conversations.updated_at`**（`TouchConversationUpdated`）：
  两处 = `CreateRunInTx`、`FinalizeOwnedRun` 的 assistant message。Sidebar 按 updated_at 排序。
- **Scheduler 时钟 = DB 时钟**（`DBNow` 查询，每 tick 一次）；本地时钟只做 DB 读取失败的回退。
- 迁移到 `0015`。新增集成测试 `tests/integration/admission_closure_test.go`（7 个并发/准入不变量）。
- 未做（下轮）：SSE Hub 多路复用、Worker 阻塞式 dispatcher、RunEvent 去 COUNT、
  `client_request_id` 幂等键、Sidebar 反范式 + keyset 分页。

## 数据库基线 = MySQL 5.7（2026-09-14 切换完成）

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

## 运行/部署关键事实（2026-09-14 冒烟测试实测）

- **delivery 是独立队列**：`studio-worker --provider=feishu_aily | feishu_delivery`。
  **只起 aily worker 时投递会永远停在 `pending`**——delivery 由 `feishu_delivery` 消费者处理
- **worker 按 provider 分片**：`ClaimCandidates(ctx, provider)`，所以 `--provider=feishu_aily` 的
  worker 看不见 `itest_*` 的 run（这也是迁移后能安全起进程的前提之一）
- **Redis Streams 携带跨环境遗留工作**：`REDIS_KEY_PREFIX` 不变时新部署会继承旧环境的
  `queue:*`。实测启动 delivery worker 后立刻消费了 98 条切换前的投递消息。
  上线前必须清 `queue:*` / 换前缀 / 换 Redis DB
- **不变量检查器可作为迁移对比工具**：`cmd/invariant-checker -once` 用同一份二进制分别指向
  源库/目标库跑，两边计数相同即证明违规是数据自带的（potal：406 == 406）
- 冒烟测试的无泄漏判据：`running=0 / run_leases=0 / provider_execution_slots=0 /
  outbox(pending)=0 / delivery(pending)=0 / schedule_occurrences(pending)=0`
- 临时排障脚本连 DB 时**没有**应用 DSN 的 `time_zone='+00:00'`，拿它比较 UTC 存储的
  `created_at` 会整体差 8 小时；要比时间就用应用侧连接或显式 `SET time_zone`

## 本地验证注意事项

- 集成测试前先确认没有遗留的 studio-worker / studio-scheduler / studio-api 进程在共享 dev 库/Redis 上抢跑；
  集成测试**会往目标库里写测试数据**，跑完若要交付「纯迁移态」必须还原（工具会先报告将丢弃多少 target-only 行）
- **测试里取 Redis/DB 配置一律用 `config.Load()`**（`.env.local` 里有密码）；只读 `REDIS_HOST` 之类的环境变量会 NOAUTH
- 改了 `db/queries/*.sql` 必须跑 `sqlc generate`（本机 `~/go/bin/sqlc` 可用），再 build
- 本机 Git Bash 下 pnpm shim 仍是坏的（`C:\c\Users\...` 路径错误）；前端项目用 package-lock.json，
  直接用 `npm test` / `npm run build`
- `go run` 在本机可能被杀软拦截，优先 `go build -o ./x.exe && ./x.exe`
