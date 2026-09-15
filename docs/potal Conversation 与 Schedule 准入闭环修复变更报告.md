# potal 对话、定时任务与并发安全性能专项评测 —— 修复变更报告

> ⚠️ **后续修正（2026-09-15 复审二轮）**：本报告中的两处结论已被后续复审推翻并修复，请以
> `docs/potal 复审问题修复变更报告（Admission 收口二轮）.md` 为准：
> 1. 本报告称「Provider active 已校验」，实际当时依赖 nullable `provider_id`，**kill switch 并未生效**（已改为按 `provider_key` fail-closed）；
> 2. 本报告新增的 `AuthorizeExecution` **漏填 `timeout_seconds`/`config`/`capabilities`**，导致 Runtime Snapshot 被清零（P0，已修复并加回归测试）。

- 仓库：`shilin414/potal`，分支 `dev`
- 评测基线：`e4fe49fd118b001ce6baa976c878344c4b744210`（本报告的修复起点即该提交）
- 修复范围：评测报告 **P0×2 + P1×7**，并顺带关闭两项 P2（Sidebar `updated_at`、Scheduler 时钟）
- 结论：报告的 15 项发现**逐项核对全部属实、无误报**；本轮把「用户在 HTTP 层可见性」与「能否执行」之间的缺口、会话内并发、Schedule admission 的锁范围、以及若干生命周期/性能边界全部收口

---

## 一、修复总览

| 报告项 | 状态 | 核心改动 |
|---|---|---|
| P0-1 执行权限绕过 | ✅ 已修复 | 新增 `catalog.AuthorizeExecution` 统一准入，接入 5 个执行入口 |
| P0-2 同会话并发 / Aily Session 分叉 | ✅ 已修复 | `CreateRunInTx` 会话行锁 + 活跃 Run 计数；`EnsureAgentThread` 原子 get-or-create |
| P1-1 Schedule overlap admission 未统一 | ✅ 已修复 | `triggerSchedule` / `TriggerNow` / `admitOne` 全部在 schedules 行锁内完成判定与创建 |
| P1-2 Disable/Update 与 Tick 的 TOCTOU | ✅ 已修复 | admission 事务内**重读**当前 schedule（enabled / prompt / policy 全部生效） |
| P1-3 Clear 不清 Aily 上下文 | ✅ 已修复 | Clear 同时删除 messages 与 agent_thread（语义=重新开始） |
| P1-4 Delete 与运行中 Run 竞态 | ✅ 已修复 | 单事务级联 + 有活跃 Run 时 409 |
| P1-5 Attachment claim TOCTOU | ✅ 已修复 | claim 移入 CreateRun 事务，`RowsAffected!=1` 整体回滚 |
| P1-6 schedule_occurrences 缺索引 | ✅ 已修复 | 迁移 0014 新增 run_id/status 等索引（含 runs/conversations/messages） |
| P1-7 缺用户级 Admission | ✅ 已修复 | 每用户 QPS + outstanding 上限 + schedule 数量上限 |
| §十二 删 Schedule 破坏 pending Delivery | ✅ 已修复 | schedules 软删除（0015），行与 Name 保留 |
| §十三 Sidebar 性能/正确性 | ✅ 部分 | 消息落库同事务 touch `updated_at` + 新增索引；反范式与 keyset 分页留待后续 |
| §十七 Scheduler 用应用时钟 | ✅ 已修复 | 每 tick 取 DB 时钟（Clock Authority） |
| §十四 SSE Hub / §十五 Worker dispatcher / §十六 事件序列去 COUNT / §十九#10 幂等键 | ⏸ 未纳入本轮 | 见「六、未纳入项」 |

---

## 二、逐项修复说明

### P0-1 统一执行准入（`AuthorizeExecution`）

**问题（已核实）**：展示层 `visible()` 有完整的可见性策略，但执行侧完全绕过它。

- `internal/transport/http/run_handlers.go` 创建 Run 时只调用 `CatalogRepo.EnabledBinding`，该 SQL 仅过滤 `runtime_bindings.enabled = 1`；
- `internal/app/app.go` 的 `schedulableChecker.SchedulableApplication(ctx, appID, 0)` 把 owner 传成 `0` 并被忽略，且不检查 `is_public`；
- Scheduler 的 `bindingResolver.EnabledBinding` 只加载 binding，因此**已停用**的应用仍可能被定时触发。

**修复**：新增 `internal/catalog/authz.go`，以**一条联查**（`GetExecutionAuthBundle`）作为唯一权威判定：

```
application 存在
application.enabled = 1          ← 停用应用对任何身份都不可执行（管理员 kill switch）
application.kind = 'chat'
普通调用方 → is_public = 1        ← 可见性即执行权
runtime_bindings.enabled = 1
providers.status = 'active'      ← provider 行存在时校验
```

错误语义刻意分级：普通调用方**只得到 404**（不泄漏应用是否存在/是否私有/是否停用）；staff 得到精确原因（`ErrExecutionDisabled` / `ErrExecutionNotChat` / `ErrExecutionNoBinding` / `ErrExecutionProviderInactive` / `ErrExecutionNotFound`）。

**接入点（5 个执行入口全部经过）**：

| 入口 | 位置 |
|---|---|
| `POST /api/v2/runs` | `run_handlers.go` → `CreateRun` |
| `POST /api/v2/applications/{id}/attachments` | `run_handlers.go` → `UploadApplicationAttachment` |
| `POST /api/v2/schedules` | `schedule.Service.Create` → `schedulableChecker`（**传真实 owner**） |
| `PATCH /api/v2/schedules/{id}` | `schedule.Service.Update` → 应用变更时重新校验 |
| `POST /api/v2/schedules/{id}/run-now` + Scheduler 每次触发 | `scheduler.resolveBinding` → `OwnerAwareResolver.EnabledBindingFor`（按 **schedule owner** 身份重新授权，staff owner 保留权限） |

**验收对应**：普通用户对 private/disabled 应用调用 Run / Schedule / run-now → 拒绝；已存在的 Schedule 在应用停用后 → **不再产生 Run**（记录一条 failed occurrence，`run_id` 为空）。

### P0-2 会话内串行化 + AgentThread 原子化

**问题（已核实）**：`EnsureAgentThread` 是 `SELECT → INSERT`，并非原子 get-or-create；`CreateRunInTx` 也没有 conversation 维度的活跃互斥，两个 Run 可同时进入 running 并各自向 Aily 发起会话。

**修复**：

1. `CreateRunInTx`（`internal/execution/service.go`）在写消息/运行之前：
   - `SELECT id FROM conversations WHERE id = ? FOR UPDATE`（会话行锁）；
   - 在该锁下 `CountActiveRunsByConversation`（`status IN ('queued','running')`）；
   - `> 0` → 返回 `ErrConversationBusy`，HTTP 层映射 **409 `previous turn is still running`**。
   两个并发提交在行锁上串行，后到者一定看见先到者已提交的 queued run。
2. `EnsureAgentThread`（`internal/execution/ownership.go`）改为**原子 get-or-create**：并发首次对话时 `UNIQUE(conversation_id)` 决定胜负，败者**重读**胜者行而非报错；身份/provider 校验抽成 `checkThreadIdentity`，仍拒绝复用他人在先的会话。
3. 惰性建会话挪进事务（见 P1-5）：会话与 Run 同生共死，不再产生「空会话」垃圾行。

### P1-1 / P1-2 统一 Schedule Admission

**问题（已核实）**：只有 `admitOne` 使用 `GetScheduleRowForUpdate`；`triggerSchedule` 与 `TriggerNow` 都是「无锁 `HasActiveOccurrence` → 再在独立事务里创建」，且 `processDue` 后续使用 scan 阶段的内存快照。

**修复**：把「判定 + 创建」合并进**同一个持有 schedules 行锁的事务**，并在锁内**重读当前行**：

- `GetScheduleRowForUpdate` 由 `SELECT id ... FOR UPDATE` 升级为 **返回整行**；
- `triggerSchedule`：锁内重读 → `enabled`/`deleted_at` 判定 → overlap 判定 → 执行窗口 → misfire 策略 → 逐次授权 → 创建 occurrence + conversation + run + outbox + 推进 `next_run_at`，全部同事务；
- `TriggerNow` 同样单事务完成（skip 直接拒绝；queue 在锁内写 pending occurrence）；
- `admitOne` 在锁内重读**最新**行（此前用锁外的旧快照判 `enabled`）；
- 新增 `skipPastInTx` / `fireMisfiredOnceInTx` / `createOccurrenceAndRunTx` / `enqueuePendingOccurrenceTx`，删除了旧的非事务版本与已无调用方的 `recordSlot`。

**强语义达成**：
- 同一 Schedule 任意时刻最多一个 active occurrence（tick、run-now、pending 准入三者共享同一把锁）；
- `/disable` 返回后不再产生新的 occurrence/run（锁内重读 `enabled`）；
- `PATCH` 改 prompt/policy 后，下一次触发使用**最新**内容，不会用 scan 时的旧快照。

### P1-3 / P1-4 会话生命周期

- **Clear**（`ClearConversation`）：语义确定为「重新开始」——同一事务内删除 messages **并删除 agent_thread**（下一条消息会惰性新建 thread → 全新 Aily session）；有活跃 Run 时返回 **409**。
- **Delete**（`DeleteConversation`）：整条级联（runs 及 events/artifacts/commands/leases、messages、thread、shares、attachments、conversation）收进**单事务**、错误不再被 `_ =` 吞掉；先取会话行锁并检查活跃 Run，有则 **409**，避免 Worker `FinalizeOwnedRun` 在「半删除」会话上写 assistant message。

### P1-5 附件原子 claim

- 新增 `ClaimAttachmentForRun`：`UPDATE ... SET run_id=? WHERE id=? AND created_by=? AND status='pending' AND run_id IS NULL`；
- 挪进 `CreateRunInTx`，逐个检查 `RowsAffected == 1`，任一失败即 `ErrAttachmentClaimed` **整体回滚**（HTTP 409）。输掉竞争的 Run 完全不存在，不再出现「input 里带着仍属于别人的附件」。

### P1-6 索引（迁移 0014）

`schedule_occurrences`：`UNIQUE(run_id)`、`(status, scheduled_at, id)`、`(schedule_id, status)`；另有 `runs(conversation_id, status)`、`runs(user_id, status)`、`conversations(user_id, updated_at, id)`、`messages(conversation_id, id)`。`run_id` 可空（pending occurrence），MySQL 5.7 的 UNIQUE 允许多个 NULL，安全。

### P1-7 用户级 Admission

`admitUserRun`（进入 Run 创建前）：

- 每用户 GCRA QPS（`RUN_USER_QPS`，默认 5/s，Redis，键 `rate:runs:user:<id>`）；
- outstanding（queued+running）上限（`RUN_USER_MAX_OUTSTANDING`，默认 20）；
- 超限 → **429**（消息含重试秒数）。
`Schedule` 侧新增每用户数量上限（`SCHEDULE_USER_MAX`，默认 50）。

### §十二 Schedule 软删除（迁移 0015）

`schedules.deleted_at` + `idx_schedules_owner_alive`；`DeleteSchedule` 改为软删（同时 `enabled=0`）；扫描/owner 列表/`Get` 全部按 `deleted_at IS NULL` 过滤，而投递侧直接 `GetScheduleByID` 仍能取到行与 `Name` —— **pending Delivery 不会因为删除 Schedule 而永久失败**。

### §十三 / §十七

- 写消息的两处（`CreateRunInTx` 与 `FinalizeOwnedRun` 的 assistant message）在**同一事务**内 `TouchConversationUpdated`，Sidebar 顺序恢复正常；
- Scheduler 每 tick 先 `SELECT CURRENT_TIMESTAMP(3)`（`DBNow`），due / misfire / window / advancePast 统一使用 DB 时钟；DB 读取失败才回退本地时钟。

---

## 三、数据库迁移

| 迁移 | 内容 |
|---|---|
| `0014_conversation_serial_and_occurrence_indexes` | P0-2/P1-6/§十三/P1-7 所需索引 |
| `0015_schedule_soft_delete` | `schedules.deleted_at` + 索引 |

二者均已对目标库（`xiaoan`）实际执行，且**二次执行干净 no-op**。

---

## 四、验证结果

```
go build ./...                                ✅
go vet ./...                                  ✅
gofmt -l（改动文件）                            ✅ 无输出
go test ./internal/...                        ✅ 全部通过
STUDIO_TEST_DB=1 STUDIO_TEST_REDIS=1 \
  go test ./tests/integration/ -count=1       ✅ ok (32.3s)   ← 含既有全部回归
./studio-migrate.exe -dir db/migrations       ✅ 幂等
```

新增集成测试 `tests/integration/admission_closure_test.go`（7 个，全部通过）：

| 测试 | 证明的不变量 |
|---|---|
| `TestConversationAdmissionSerializesTurns` | 同会话第二次提交 → `ErrConversationBusy` |
| `TestConversationConcurrentSubmitSingleTurn` | 12 并发提交 → **恰好 1** 个 Run，其余全部拒绝 |
| `TestAttachmentClaimSingleWinner` | 两 Run 争同一附件 → 1 成功 + 1 `ErrAttachmentClaimed`，失败方 Run 完全回滚 |
| `TestAuthorizeExecutionGates` | public 可执行；private 普通用户 403 语义 / staff 放行；disabled 对**任何人**拒绝；非 chat 拒绝；无 binding 拒绝；未知 id → NotFound |
| `TestSchedulerRecordsFailureWhenApplicationNotExecutable` | 应用不可执行 → 1 条 failed occurrence、**0 Run** |
| `TestTickAndRunNowShareOneAdmission` | run-now 已活跃时，tick 不产生第二个 active occurrence；再次 run-now 被拒 |
| `TestScheduleSoftDeleteKeepsDeliveryLookup` | 软删后 owner `Get` → NotFound、扫描/列表排除，但投递侧仍能取到 Name |

---

## 五、行为契约变化（前端/调用方需知）

| 场景 | 之前 | 现在 |
|---|---|---|
| 同会话已有非终态 Run 再提交 | 201（并发执行） | **409** `previous turn is still running` |
| Clear/Delete 时存在活跃 Run | 200（留下半状态） | **409** |
| 附件已被其他 Run 占用 | 201（错绑） | **409** |
| 普通用户执行 private/disabled 应用 | 201 | **404**（不泄漏存在性） |
| 用户超 QPS / outstanding 超限 | 无限制 | **429** |
| `DELETE /schedules/{id}` | 物理删除 | 软删除（列表/扫描不可见，历史保留） |

`client_request_id` 幂等键（报告 §十九#10）本轮未纳入，**建议前端暂时仍需自行防重**。

---

## 六、未纳入本轮（及理由）

| 项 | 理由 / 建议 |
|---|---|
| §十四 SSE 单连接=单 Redis 订阅，缺 per-user 上限 | 属架构扩容项；当前门户同时在线规模下风险低。建议下一轮做进程内 RunEventHub 多路复用 + 每用户最大 SSE 数 |
| §十五 Run Worker 空闲 3×并发 次/s Redis 探测 | 需把 Stream Reader 与执行池拆成 dispatcher + 本地队列，改动集中在中枢并发组件，单独一轮更稳 |
| §十六 RunEvent 序列用 `COUNT(*)` | 已在报告中被标注 P2（`content.delta` 已走 transient + 合并），收益有限 |
| §十三 反范式 `message_count/last_message_*` + keyset 分页 | 本轮已修正确性（touch + 索引）；反范式会引入一致性维护面，建议与分页一起做 |
| §十九#10 `client_request_id` 幂等键 | 报告正文未展开；需 runs 表加列 + 唯一键，建议与前端防重一起设计 |

---

## 七、残留风险

1. **`AuthorizeExecution` 不校验 provider 侧用户可见性**：仍依赖 Worker 用调用方自己的 UAT 访问 Aily 时的失败兜底（与既有设计一致），本次未改变该层。
2. **会话串行化是「串行」而非「排队」**：连续快速发送的后续消息会被 409 拒绝，用户需重发；若要「多条依次执行」，需引入 `conversation_turn_seq` 排队（报告建议的第二阶段）。
3. **软删除的 Schedule 仍占用唯一键空间**：`(schedule_id, scheduled_at)` 唯一约束不受影响（按 id 维度），但同名 Schedule 的语义差异需在前端文案上说明「删除后不可恢复」。
4. **迁移 0014/0015 为在线 DDL**：本次库规模小、瞬时完成；大表上线时建议评估 `ALTER` 的锁窗口（MySQL 5.7 加索引为 online DDL，`DROP/ADD COLUMN` 需关注）。
