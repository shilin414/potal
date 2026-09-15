# potal 二轮复审整改变更报告

（P1-1 Scheduler 错误分类 · P1-2 执行时 Kill Switch · P1-3 run-now pending 上限 · §七 Schedule CRUD 行锁）

## 0. 基线与范围

- 复审基线：`dev` `b2112ad796f88a8eca1045e1b260cc2916d3ca08`（二轮复审报告结论 91/A-）
- 本轮按复审报告 §十三「建议上线前做 + 建议近期做」整改全部四项；P2 及架构优化项（§八/§九/§十及 SSE/幂等键等）留待下轮
- Kill Switch 语义为产品确认决策：**Application/Binding 停用 = 硬 Kill（取消未执行 Run）；Provider 停用 = Pause（保留等待）；已真正提交给 Provider 的 Run 不做伪强杀**

## 1. P1-1 Scheduler Resolver 不再吞基础设施错误

**问题**：`bindingResolver.EnabledBindingFor` 把任何 error（含 MySQL 瞬时故障、context deadline）都转成 `binding=nil, err=nil`，Scheduler 视为"策略拒绝"→ 永久 failed occurrence、该时间槽丢失。`authorizeForOwner` 中身份查询失败同样被静默降级为 `isStaff=false`。

**修复**（`internal/app/app.go`）：

1. 新增 `executionDenied(err) bool`：只有 7 个策略哨兵（`ErrExecutionForbidden/NotFound/Disabled/NotChat/NoBinding/ProviderInactive/ProviderMissing`，含 wrap 链）才算拒绝；其余一律向上传播。
2. `EnabledBindingFor` / `EnabledBinding`：策略拒绝 → `(nil, nil)`（failed occurrence、推进 next_run_at）；基础设施错误 → `(nil, err)`（triggerSchedule/admitOne/TriggerNow 事务回滚，下个 tick 重试同一槽）。
3. `authorizeForOwner`：`identity.ErrNotFound` → 按 policy 走非 staff 规则；**其余错误向上传播**，DB 故障不再伪装成 "private app" 拒绝。
4. 连带修复：`schedulableChecker.SchedulableApplication` 区分两类错误——新增哨兵 `schedule.ErrApplicationNotSchedulable`，只有策略拒绝映射为 400 级 `ValidationError`，基础设施错误以原始错误上抛（此前 DB 故障会让 Create/Update Schedule 返回 400 "application is not schedulable"）。

Scheduler 侧无需改动：`triggerSchedule`/`admitOne`/`TriggerNow`/`fireMisfiredOnceInTx` 原本就对 `err != nil` 走回滚路径，现在终于能收到真实的 err。

## 2. P1-2 执行时 Kill Switch（Worker Gate）

**问题**：admission 门禁只在 CreateRun/调度时生效；已 queued 的 Run 在 Claim 后不再复查 `application.enabled / binding.enabled / provider.status`，管理员停用后 Worker 仍会照常调用 Provider。

**修复**：

- `internal/execution/worker.go`：新增 `RunGate` 接口与 `GateAction`（allow/pause/kill），`Worker.Gate` 字段；`execute()` 在 **ProviderSlot Acquire 之前、Handler 之前**执行门禁——被 gate 拦下的 Run 永不占用 provider 容量、永不接触 Provider：
  - `GateKill` → `FinalizeOwnedRun(Status=cancelled, error_code="execution_disabled")`，走既有终态事务（terminal event、occurrence 收敛、lease/slot 清理全复用）；
  - `GatePause` → `RetryOwnedRunAfter(reason="provider_disabled", delay=GatePauseRequeueDelay=30s)`，**不消耗 attempt**，Provider 恢复后自动继续；30s 而非 1s 是避免停用期间的 hot-loop；
  - gate 查询自身失败 → 视为基础设施故障，按 admission requeue 处理（1s），绝不因查询失败销毁 Run。
- `db/queries/execution.sql` 新增 `GetRunGateState`：`runs → LEFT JOIN applications/runtime_bindings/providers(按 provider_key)` 只取三个可撤销事实，**刻意不读 runtime_snapshot**（冻结快照决定"怎么执行"，gate 只决定"是否还允许开始"）。缺 app/binding 行 → fail-closed（kill）；provider 缺失或非 active → pause。
- `internal/catalog/authz.go`：新增 `RunGateState` 读取方法（NULL join 语义由调用方分类）。
- `internal/app/app.go`：`ExecutionGate` 适配器实现分类并映射到 GateAction；`cmd/worker/main.go` 注入 `Gate: app.NewExecutionGate(a.Catalog)`。
- 观测：`telemetry` 新增 `ProviderAdmission{result="gate_killed"/"gate_paused"}`。

**边界说明**：`runtime_binding_id` 为 NULL 的 Run 按 fail-closed 处理为 kill（无 binding 的 Run 本就无法执行）；已提交 Provider 的 Run 不在 gate 覆盖范围内（按产品决策不做伪强杀，与 lease/fence 机制一致）。

## 3. P1-3 run-now pending 队列上限

**问题**：`TriggerNow` 在 overlap=queue 时每次 `enqueuePendingOccurrenceTx` 都插入一条 pending occurrence，无任何上限——pending 无 Run、不受 `RUN_USER_MAX_OUTSTANDING` 约束，`/run-now` 成为 backlog 旁路。

**修复**：

- `db/queries/automation.sql` 新增 `CountPendingOccurrences`；
- `enqueuePendingOccurrenceTx` 在 **schedules 行锁内**先计数，达到 `MaxPendingManual` 即返回新增哨兵 `scheduler.ErrPendingCapReached`（锁内计数 ⇒ 并发 run-now 无法全部读到旧计数越限，与 SCHEDULE_USER_MAX 的行锁模式一致）；
- `Scheduler.MaxPendingManual` ← config `SCHEDULE_MAX_PENDING_MANUAL`，**默认 1**（复审报告建议的 UI 语义："已为你排队一次，重复点击直接拒绝"）；0 关闭该上限；
- HTTP：`POST /schedules/{id}/run-now` 对 `ErrPendingCapReached` 返回 **429**。

## 4. §七 Schedule Update / SetEnabled 进入 schedules 行锁

**问题**：管理面 `Update()` 在事务外 Get → Go 内 merge patch → 事务内全量 UPDATE，并发 PATCH 互相覆盖字段（lost update）；`SetEnabled(true)` 在事务外按旧 trigger_config 计算 next_run_at，与 PATCH 并发时配置/next_run_at 不一致。

**修复**（`internal/automation/schedule/service.go`，全系统统一规则：**凡修改 Schedule 业务状态，先锁 Schedule 行**）：

- `Update`：事务内 `GetScheduleRowForUpdate`（软删 = ErrNoRows = ErrNotFound）→ 锁内 ownership 校验 → **基于锁内最新行 merge PATCH** → validate（catalog 为普通读，无锁序风险；锁序仍为 schedules → schedule_deliveries）→ next_run_at 从合并后状态计算 → UPDATE → replaceDeliveries → COMMIT。
- `SetEnabled`：同样全程在行锁事务内，enable 分支基于锁内读到的 trigger config 计算 next_run_at，disable 分支也在锁内。
- 复用 scheduler admission 已验证的同一把锁模型，未新增任何锁序。

## 5. 新增测试

| 测试 | 位置 | 覆盖 |
|---|---|---|
| `TestExecutionDeniedClassifiesPolicyErrors` | `internal/app/authz_test.go`（纯单元） | 7 个策略哨兵（含 wrap）=拒绝；MySQL 1213/deadline 等 = 不拒绝 |
| `TestAuthorizeForOwnerPropagatesIdentityInfraError` | 同上（STUDIO_TEST_DB） | 身份查询故障必须上抛（不可达 DB），不得静默降级 non-staff |
| `TestEnabledBindingForUnknownOwnerIsPolicyDenial` | 同上（STUDIO_TEST_DB） | 对照组：未知 owner 是 policy 而非 error → (nil, nil) |
| `TestSchedulerAuthInfraErrorRetriesSlot` | `tests/integration/review2_fixes_test.go` | infra error → 0 occurrence、next_run_at 不变（槽保留）；恢复后同槽正常 fire；对照组 policy 拒绝 → failed occurrence |
| `TestQueuedRunHonorsProviderKillSwitchKill` | 同上（需 STUDIO_TEST_REDIS） | queued Run + 事后停用 app → claim 时被取消（error_code=execution_disabled），handler 0 次执行 |
| `TestQueuedRunHonorsProviderKillSwitchPause` | 同上 | queued Run + 事后停用 provider → requeue（available_at≈+30s）、attempt=0、handler 0 次执行 |
| `TestConcurrentSchedulePatchDoesNotLoseFields` | 同上 | 并发 PATCH 不同字段 → 两字段同时生效（无 lost update） |
| `TestRunNowPendingQueueHasBound` | 同上 | 顺序 + 6 并发 run-now，pending 恒等于上限 1，超限返回 ErrPendingCapReached |

测试自清理：infra/cap 两个测试结束时会软删自己的 fixture schedule，避免在共享 dev 库留下"每 tick 报错"的到期任务。

## 6. 配置项变化

| 环境变量 | 默认 | 说明 |
|---|---|---|
| `SCHEDULE_MAX_PENDING_MANUAL` | `1` | 单个 schedule 的 run-now pending 上限；0 = 不限 |

无新增迁移（本轮纯查询/代码变更，schema 未动，`cmd/migrate` 二次执行仍为干净 no-op）。

## 7. 验证结果

- `go build ./...`、`go vet ./...`、`gofmt`（本轮触碰文件）通过；`sqlc generate` 已执行（`GetRunGateState`/`CountPendingOccurrences`）
- `go test ./internal/...` 全绿（含新增 app 单元测试）
- `STUDIO_TEST_DB=1 STUDIO_TEST_REDIS=1 go test ./tests/integration/...` 全绿（含 8 个新增测试）

## 8. 遗留与下轮

按复审报告分级保留：§八 非终态 Run predicate 统一（启用 waiting/resume 前必须升 P1）；§九 RateLimiter ctx/nil-Redis 边角；§十 Schedule Service 全量 DB Clock；以及 client_request_id、SSE Hub、Worker 阻塞 dispatcher、Conversation 软删除、Sidebar keyset。
