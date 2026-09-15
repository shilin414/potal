# potal 对话、定时任务与并发安全性能专项【复审】—— 修复变更报告

- 仓库：`shilin414/potal`，分支 `dev`
- 复审基线：`418f2f18`（上一轮修复提交 `01eed35b` 之后的 HEAD）
- 输入：《potal 对话、定时任务与并发安全性能专项复审报告》（P0×1 / P1×5 / P2×3）
- 本轮处理：**P0 × 1 全部关闭；P1 × 5 全部关闭；P2 × 3 全部关闭**
- 上一轮变更报告：`docs/potal Conversation 与 Schedule 准入闭环修复变更报告.md`

---

## 一、总览

| 复审项 | 等级 | 状态 | 关键改动 |
|---|---|---|---|
| AuthorizeExecution 返回的 Binding 缺 `timeout_seconds` / `config` / `capabilities`，Runtime Snapshot 被清零 | **P0** | ✅ 已修复 | 新增 `bindingFromExecutionAuthRow`，与 `bindingFromRow` 字段对齐；新增 snapshot 回归测试 |
| Provider active kill switch 实际不生效（`provider_id` 为 NULL → fail open） | P1 | ✅ 已修复 | 授权 SQL 改按 **provider_key** 匹配且 **fail-closed**；binding 创建/更新写入 `provider_id`；迁移 `0016` 回填；新增测试 |
| `admitOne` 的 binding 在 schedule 行锁之外解析（TOCTOU） | P1 | ✅ 已修复 | `resolveBinding` 移入锁内、基于锁内重读的行；新增 A→B 变更测试 |
| Delete Conversation 破坏 Scheduled Run / pending Delivery | P1 | ✅ 已修复 | 删除下沉为 `DeleteConversationCascade` + 生命周期守卫（拒绝硬删）；新增测试 |
| 用户限流：每请求 new limiter（Redis 故障 fail-open）+ outstanding 非原子 | P1 | ✅ 已修复 | 长生命周期 limiter + `AllowKey` 每键有界本地状态；outstanding 改用户行锁内同事务判定 |
| TriggerNow 取 DB now 后又用本地时钟 | P2 | ✅ 已修复 | 统一使用 tick 的 `now` |
| Schedule Update 校验的是管理员权限而非 Owner 权限 | P2 | ✅ 已修复 | 执行权校验改用 `cur.OwnerUserID` |
| Schedule 数量上限软上限 | P2 | ✅ 已修复 | 用户行锁内计数 + 插入，同一事务 |
| Schedule 删除的锁顺序反转（deliveries → schedules） | P1/P2 | ✅ 已修复 | 软删除时**保留** `schedule_deliveries`，不再物理删除 |

---

## 二、P0：AuthorizeExecution 的 Binding 映射补全

**问题**：`AuthorizeExecution` 从联查行重建 `Binding` 时只填了 ID/Provider/Identity/Execution/Session/Artifact/Enabled，漏掉了

```
timeout_seconds / config / capabilities
```

而 `Binding.Snapshot()` 会**无条件**写入这三个字段，于是每个新 Run 的 `runtime_snapshot` 变成：

```json
{"timeout_seconds": 0, "config": null, ...}
```

后果（复审分析准确）：
- **Background/异步**：`SnapshotInt("timeout_seconds", 300)` 因字段**存在且为 0** 而返回 0 → deadline = now → 提交成功后立即进入 poll timeout，异步执行功能直接回归；
- **Interactive**：超时被 floor 到 lease（120s+30s≈150s），长回答比设计（≈330s）提前约 180s 被取消。

**修复**（`internal/catalog/authz.go`）：

```go
// bindingFromExecutionAuthRow 与 bindingFromRow 输出的 Binding 形状完全一致
TimeoutSeconds: int64(row.BindingTimeoutSeconds),
...
_ = json.Unmarshal(row.BindingCapabilities, &b.Capabilities)
_ = json.Unmarshal(row.BindingConfig, &b.Config)
```

并加了明确注释说明这是**承重字段**：任何新增的授权路径都必须复用同一转换函数，禁止手写「部分 Binding」。

**回归测试**（`TestAuthorizeExecutionPreservesRuntimeSnapshot`）：
- binding 配 `timeout_seconds=321`、`config={"foo":"bar"}`、`external_resource_id=agent_snap`；
- 断言 `AuthorizeExecution().Binding.Snapshot()` 三者完整；
- 且端到端断言：`CreateRun` 后 `runs.runtime_snapshot` 读出 `timeout_seconds=321`、`external_resource_id=agent_snap`。

---

## 三、P1：Provider kill switch 改为按 provider_key fail-closed

**问题**（已在库中实证）：`runtime_bindings.provider_id` 由 API 创建/更新时**从未写入**（`bindingFromInput` 不填、两处调用点拿到 `provider` 却丢弃），授权 SQL 又是

```sql
LEFT JOIN providers p ON p.id = b.provider_id   -- NULL → provider_status NULL → Go 放行
```

于是 `provider.status = 'inactive'` 对这类 binding **完全无效**。我实测了目标库：

```
TOTAL bindings=18  provider_id IS NULL=16  providers=1
```

**修复三件套**：

1. **授权改为按业务键匹配 + 失败关闭**（`db/queries/catalog.sql`）：

```sql
LEFT JOIN providers p ON p.provider_key = b.provider_key
```

Go 侧：`provider_status` 为 NULL（无 provider 行）或非 `active` → **一律拒绝**（普通用户 404 语义、staff 得到 `ErrExecutionProviderMissing` / `ErrExecutionProviderInactive`）。

2. **创建/更新 binding 时写入 provider FK**（`internal/catalog/service.go` 两处）：`b.ProviderID = &provider.ID`，并保留 `validateRuntimeInput` 原有的「provider 必须 active」前置校验。

3. **数据回填**（迁移 `0016_backfill_binding_provider_id`）：

```sql
UPDATE runtime_bindings b JOIN providers p ON p.provider_key = b.provider_key
SET b.provider_id = p.id WHERE b.provider_id IS NULL;
```

**测试**：
- `TestInactiveProviderRejectedEvenWithNullProviderID`：`provider_id` 故意为 NULL、`provider_key` 指向 inactive provider → 普通用户/staff **都必须被拒**；provider_key 无对应行 → `ErrExecutionProviderMissing`；provider 改回 active → 立即恢复可执行。
- `TestAuthorizeExecutionGates` 同步重写：fixture 现在显式 seed provider 行，并同时覆盖「provider_id 有值」与「provider_id 为 NULL」两种形状。
- ⚠️ **修正了上一轮测试的错误期待**：旧测试注释曾把「provider_id 为 NULL 时放行」写成合法行为，现已在代码与测试中彻底消除。

---

## 四、P1：`admitOne` 的授权移入 schedule 行锁

**问题**：pending admission 在**锁外**先 `GetScheduleByID` + `resolveBinding`，再进事务 `FOR UPDATE` 重读。若两者之间发生 `PATCH application A→B`，会写出自相矛盾的 Run：

```
Run.ApplicationID = B
Run.RuntimeBindingID = A.binding
Run.Provider = A.provider
Run.RuntimeSnapshot = A 的快照
```

同理，若期间管理員停用了应用，锁内也不再重新授权。

**修复**（`internal/automation/scheduler/scheduler.go`）：删除锁前解析，改为锁内、基于 `cur`（锁内重读的最新行）解析：

```go
curRow, _ := q.GetScheduleRowForUpdate(...)
cur := schedule.FromDBRow(curRow)
...
// 任何会影响 Run Snapshot 的事实，都必须在最终 Admission Lock 之后读取
binding, err := s.resolveBinding(ctx, cur.ApplicationID, cur.OwnerUserID)
if binding == nil { 记 failed occurrence; commit; return ErrNotSchedulable }
createRunForOccurrenceTx(ctx, tx, cur, occRow.ID, binding, now)
```

**测试**：
- `TestPendingOccurrenceAdmissionReauthorizesApplicationChange`：pending occurrence 存在后把 schedule 的 application 改为 B → admission 产出的 Run 的 `application_id`、`runtime_binding_id`、`runtime_snapshot.external_resource_id` **必须全部来自 B**；
- `TestDisabledApplicationStopsPendingAdmission`：pending 期间 `applications.enabled=0` → occurrence 记 `failed`、`run_id` 为空、**0 Run**。

---

## 五、P1：Conversation 删除不再破坏 Scheduled Run / pending Delivery

**问题**：Scheduler 创建的定时 Run 挂在普通 Conversation 下；`DeleteConversation` 会物理删除 runs / events / artifacts / commands / leases / messages / thread，而 `schedule_occurrences.run_id` 与 delivery worker 的 `GetRunByID(row.RunID)`（读 `run.Output.text`）仍然引用它们 → 孤链 + pending Delivery 必然 failed。

**修复**：整条级联下沉为 `execution.Service.DeleteConversationCascade`（单事务），并在删除前加**生命周期守卫**：

| 守卫 | 判定 | 结果 |
|---|---|---|
| 会话行锁 | 与 `CreateRunInTx` 共享同一把锁 | 删除期间不可能再插入新 Run |
| 活跃 Run | `status IN ('queued','running')` | 409 `conversation has an active run` |
| 定时 Run | `trigger_type = 'scheduled'` | 409 `contains scheduled runs or pending deliveries` |
| 投递依赖 | 该会话下任意 Run 存在 `delivery_executions` | 同上 |

**为什么本轮选「拒绝」而不是 Conversation 软删除**：软删除需要同步改造 Sidebar 列表、Owner 可见性、消息读取、SSE、分享等全部读路径，改动面大、回归风险高；复审报告也把「至少禁止硬删」列为可接受方案。**建议下一轮**按 Run=执行审计实体的原则引入 Conversation 软删除（hide），届时可放开该限制。

**测试**：`TestDeleteConversationBlockedByScheduledRuns`（终态 + scheduled 的 Run → 拒绝；去掉 scheduled 归属后删除成功且会话消失）。
另把 `ClearConversation` 同样下沉（`execution.Service.ClearConversation`）并补测 `TestClearConversationResetsAgentThread`：清空后 messages 与 agent_thread 均为 0；有活跃 Run 时 409。

---

## 六、P1：用户准入的两个缺陷

### 6.1 limiter 变为长生命周期 + 每键有界本地状态

**问题**：每请求 `NewRateLimiter(...)` → Redis 故障时 `localTat` 每次归零 → 降级退化为 fail-open。

**修复**（`internal/execution/ratelimit.go`）：
- 新增 `AllowKey(ctx, key)`，`Allow` 变成 `AllowKey(l.key)`；
- 本地降级状态从单个 `atomic.Int64` 改为 **`map[key]tat`**（`localStateMax = 4096`，超出整体丢弃，保持有界）；
- 增加 **nil Redis 保护**：未配置 Redis（dev/test 常以 nil 构造 service）时同样走降级路径，而不是 panic —— 这同时修掉了一个潜在崩溃点（此前 provider limiter 若以 nil rdb 构造并调用会空指针）。
- `Server.RunAdmission` / `App.RunAdmissionRL`：App 启动时创建**唯一**实例，按用户动态 key 计费。

**测试**：`TestUserAdmissionRedisDownStillBounded`（nil Redis → 20 次请求 limit=3 只放行 ≤3 次、且 `Degraded()` 为真、不同 key 预算独立）。

### 6.2 outstanding 上限改为原子上限

**问题**：`COUNT` 与 `CreateRun` 不在同一事务 → 并发下 n=19 的多请求可同时通过。

**修复**：新增 `execution.Service.CreateRunAdmitted(ctx, in, maxOutstanding)`：

```
BEGIN
  SELECT id FROM users WHERE id = ? FOR UPDATE        -- 用户行锁
  SELECT COUNT(*) FROM runs WHERE user_id=? AND status IN ('queued','running')
  COUNT >= max → ErrUserOutstandingExceeded（429）
  CreateRunInTx(...)                                   -- 建会话/消息/Run/outbox/附件
COMMIT
```

新增查询 `LockUserRow`；HTTP 层的 429 由 `ErrUserOutstandingExceeded` 映射。锁顺序恒为 `users → conversations`，与其它路径无环。

> 说明：定时 Run 不走该上限（其并发由 Schedule admission 约束），仅交互式 Run 受控，符合复审意图。

---

## 七、P2 三项

1. **TriggerNow 全程 DB now**：`createOccurrenceAndRunTx(..., now, "run_now")`（原先又调了一次 `s.nowFunc()`），`scheduled_at / admitted_at / last_run_at` 不再受本机时钟影响。
2. **Schedule Update 按 owner 校验执行权**：`s.validate(ctx, next, cur.OwnerUserID)` —— 管理员代改他人的 Schedule 时，执行权按**所有者**判定，避免「保存成功但每次触发必失败」。修改权仍由 caller/isStaff 决定。
3. **Schedule 配额原子化 + 删除保留 deliveries**：
   - `Create` 在事务内 `LockUserRow(owner)` → `CountSchedulesByOwner` → 插入，并发创建不再越过 50；
   - `Delete` 只做 `UPDATE schedules SET deleted_at=..., enabled=0`，**保留** `schedule_deliveries`：既消除 `deliveries→schedules` 与调度器 `schedules→deliveries` 的锁序反转（死锁风险），也保住旧 occurrence（`delivery_snapshot_at IS NULL`）的 fallback 与审计完整性。

---

## 八、数据库迁移

| 迁移 | 内容 |
|---|---|
| `0016_backfill_binding_provider_id` | 按 `provider_key` 回填 `runtime_bindings.provider_id`（幂等；down 为 no-op，数据修复不可逆） |

已对 `xiaoan` 库执行成功；`0014`–`0016` 连续二次执行均为干净 no-op。

---

## 九、验证结果

```
go build ./... / go vet ./... / gofmt（本轮改动文件）   ✅
go test ./internal/...                                 ✅ 全部通过
STUDIO_TEST_DB=1 STUDIO_TEST_REDIS=1 \
  go test ./tests/integration/ -count=1                ✅ ok (33.6s，含全部既有回归)
./studio-migrate.exe -dir db/migrations                 ✅ 幂等
```

新增/更新测试 8 个（全部通过）：

| 测试 | 覆盖 |
|---|---|
| `TestAuthorizeExecutionPreservesRuntimeSnapshot` | **P0** snapshot 不丢 timeout/config/resource |
| `TestInactiveProviderRejectedEvenWithNullProviderID` | provider kill switch 对 `provider_id=NULL` 也生效；无 provider 行 fail-closed；恢复 active 后放行 |
| `TestAuthorizeExecutionGates`（重写） | 授权矩阵 + provider fixture |
| `TestPendingOccurrenceAdmissionReauthorizesApplicationChange` | pending 二次授权：A→B 后 Run 全量使用 B |
| `TestDisabledApplicationStopsPendingAdmission` | pending 期间停用 → failed occurrence + 0 Run |
| `TestDeleteConversationBlockedByScheduledRuns` | scheduled Run 守卫 + 正常删除仍成功 |
| `TestClearConversationResetsAgentThread` | 清空重置线程；活跃 Run 时 409 |
| `TestTickAndRunNowConcurrent` | 10 协程真竞态（tick vs run-now）→ active occurrence = 1 |
| `TestUserAdmissionRedisDownStillBounded`（单测） | Redis 故障下仍受限、每键独立、不 panic |

---

## 十、行为契约变化

| 场景 | 之前 | 现在 |
|---|---|---|
| binding 的 provider 未注册 / 非 active | 放行（fail open） | **拒绝**（普通用户 404；staff 409/明确原因） |
| 删除含定时 Run 的会话 | 200（删掉 Run，破坏投递） | **409**（保留 Run） |
| 删除 Schedule | 物理删除 delivery 配置 | 保留配置（仅软删 schedule） |
| 用户 outstanding 超限（并发下） | 可能越过上限 | **429**（原子判定） |
| 管理员代改他人 Schedule 指向私有应用 | 保存成功、永久失败 | **保存即被拒**（按 owner 判定） |

---

## 十一、仍未纳入（与前一轮一致，建议独立一轮）

| 项 | 说明 |
|---|---|
| Conversation 软删除（hide）模型 | 本轮用「拒绝硬删」的保守方案；软删除需一并改造 Sidebar/消息读取/分享/SSE |
| SSE Hub 多路复用、per-user 连接上限 | 架构扩容项 |
| Run Worker 阻塞式 dispatcher | 需拆分 Stream Reader 与执行池 |
| RunEvent 序列去 `COUNT(*)` | P2，`content.delta` 已走 transient |
| `client_request_id` 幂等键 | 需 runs 加列 + 唯一键，建议与前端防重一起设计 |

---

## 十二、残留风险

1. **`AuthorizeExecution` 与 `bindingFromRow` 是两份转换**：已通过测试锁定字段一致性，但长期看应合并为单一转换函数（待 binding 行类型统一后）。
2. **会话删除守卫会使「定时任务产生的会话」不可删**：这是本轮有意取舍；引入 Conversation 软删除后应放开。
3. **用户行锁带来每用户串行**：交互式建 Run 会短暂串行化（配 5/s QPS 上限，可接受）；若未来放开 QPS，需评估该锁的吞吐。
4. **迁移 0016 为不可逆数据修复**：down 为空实现（旧 NULL 值无业务意义，且新授权逻辑不依赖它）。
