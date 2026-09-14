# Creation Agent Studio  
# Execution Correctness Closure 修复执行开发计划

版本：V1.0  
适用项目：`shilin414/potal`  
目标分支：`dev`

---

# 1. 本轮开发定位

本轮不是新功能迭代。

本轮唯一目标：

> **彻底关闭 Creation Agent Studio Execution Plane 中剩余的 Ownership、Lease、Retry、Terminal State 一致性问题，使 Run 执行内核达到正式生产运行的基础正确性标准。**

当前架构总体保持不变：

```text
Application
    ↓
RuntimeBinding
    ↓
Run
    ↓
Transactional Outbox
    ↓
Redis Streams
    ↓
Worker
    ↓
Claim + Lease
    ↓
RuntimeAdapter
    ↓
Provider
```

继续坚持：

```text
TiDB = Source of Truth
Redis = Queue Wake-up / Realtime Fanout / Cache
```

不改变：

- Go 后端
- sqlc
- BINARY(16) UUIDv7
- Transactional Outbox
- Redis Streams
- CAS Claim
- Run / RunEvent
- Schedule / Occurrence / Delivery
- Aily RuntimeAdapter
- Desktop / Mobile Shell
- Feishu OAuth 主登录方式

---

# 2. 当前状态基线

上一轮已经完成：

```text
✅ Outbox BINARY(16) → canonical UUID
✅ Claim + Lease 单事务
✅ runs.lease_epoch
✅ lease_token heartbeat
✅ heartbeat lost → cancel local context
✅ fire_once / skip / catch_up
✅ MaxCatchUpSlots
✅ Run Now pending admission
✅ Redis GCRA local fallback
✅ content.delta transient + content.chunk
✅ RuntimeSnapshot typed validation
✅ /login Feishu OAuth
✅ /login/admin local admin
```

ClaimAndLease 当前已经在单个 TiDB transaction 内完成 queued→running、lease_epoch 自增以及 Lease INSERT，这部分保留。

Worker 也已经拥有基于 lease token 的 Heartbeat，并在 Lease 丢失时取消本地 Execution Context。

因此本轮禁止重新设计这些已经正确的基础设施。

---

# 3. 本轮必须解决的问题

按照优先级分为三个等级。

## P0：Execution Correctness

必须全部完成才能认为本轮结束：

```text
P0-1 Reaper recovery 非原子
P0-2 ReleaseInterrupted 仍存在 unfenced side effect
P0-3 run.interrupted 的 retry / terminal 语义冲突
P0-4 Aily retryOrFail 绕过 fenced release
P0-5 Background execution refresh 丢失 Ownership
P0-6 Worker canonical writes 仍可绕过 Ownership
```

## P1：Canonical State 完整性

```text
P1-1 Artifact persistence fencing
P1-2 AgentThread session fencing
P1-3 Terminal Run + RunEvent 原子一致
P1-4 Assistant Message 与 Run Finalize 一致性
P1-5 Scheduler multi-instance admission 竞态
P1-6 AdminLogin 必须 staff-only
```

## P2：Production Hardening

```text
P2-1 invariant checker
P2-2 degraded rate limiter metric
P2-3 Redis outage limiter test
P2-4 CI
P2-5 Aily attachment streaming
```

Aily attachment streaming 和 Legacy Workflow RuntimeAdapter 收敛仍属于后续迭代，不要求塞进本轮核心闭环。

---

# 4. 本轮最重要的架构调整

不要继续采用：

```text
Run
├── LeaseEpoch
└── LeaseToken
```

然后依赖开发者记住：

```text
GetRun 后要 preserveOwnership()
```

这种方式。

应该正式把：

> **Execution Data**

与：

> **Execution Ownership**

拆开。

---

# 5. 新增 ExecutionOwnership

建议在：

```text
backend-go/internal/execution/ownership.go
```

新增：

```go
type ExecutionOwnership struct {
    RunID      ids.ID
    WorkerID   string
    LeaseEpoch uint64
    LeaseToken ids.ID
}
```

要求：

```text
ExecutionOwnership
```

一旦由：

```text
ClaimAndLease
```

产生，在本次 Attempt 生命周期内：

> **不可刷新、不可从数据库重新获取、不可被新的 Run 查询覆盖。**

它代表：

```text
“当前 Worker 对该 Run canonical state 的写权限”
```

而不是 Run 本身的业务数据。

---

# 6. 新增 ClaimedRun

建议：

```go
type ClaimedRun struct {
    Run       *Run
    Ownership ExecutionOwnership
}
```

将当前：

```go
ClaimAndLease(...) → ownership
GetRun(...)
run.LeaseEpoch = ...
run.LeaseToken = ...
```

改成：

```go
ClaimRun(...)
↓
ClaimedRun
```

概念上：

```text
ClaimedRun
├── Run
└── Ownership
```

Worker Handler 接口由：

```go
Execute(ctx context.Context, run *Run) error
```

逐步调整为：

```go
Execute(
    ctx context.Context,
    claimed *ClaimedRun,
) error
```

或者：

```go
Execute(
    ctx context.Context,
    run *Run,
    ownership ExecutionOwnership,
) error
```

推荐前者。

---

# 7. 为什么必须拆 Ownership

当前 `GetRun()` 会重新构建：

```text
Run
```

但数据库只能恢复：

```text
lease_epoch
```

不能恢复：

```text
lease_token
```

因此 Background Execution 当前出现了：

```text
Claim
↓
Run 有 token
↓
GetRun
↓
新的 Run
↓
token 丢失
```

Streaming reconcile 已经通过 `preserveOwnership()` 特殊处理，但 Background 路径没有完全遵循这个规则。

这种问题应该通过类型设计消灭，而不是继续增加：

```go
preserveOwnership(...)
```

调用点。

---

# 8. Ownership 强制规则

完成重构后定义以下工程规则：

> Worker 执行期间，所有 canonical state 写操作必须接收 `ExecutionOwnership`。

不得存在：

```text
Worker
→ 普通 AppendEvent
Worker
→ 普通 RequeueRun
Worker
→ 普通 DeleteLease
Worker
→ 普通 UpsertArtifact
```

只能：

```text
Worker
→ Owned/Fenced API
```

---

# 9. Service API 重构

建议逐步淘汰：

```go
AppendEvent()
ReleaseInterrupted()
UpdateRunExternalID()
DeleteLease()
HeartbeatLease()
```

在 Worker Provider Runtime 中的直接使用。

新增统一：

```go
AppendOwnedEvent()
UpdateExternalRunIDOwned()
HeartbeatOwned()
RequeueOwned()
FailOwned()
FinalizeOwned()
PersistArtifactOwned()
BindProviderSessionOwned()
```

接口示例：

```go
func (s *Service) AppendOwnedEvent(
    ctx context.Context,
    own ExecutionOwnership,
    eventType string,
    payload map[string]any,
) error
```

所有 owned API：

```text
必须验证
runs.id
AND
runs.status='running'
AND
runs.lease_epoch=ownership.LeaseEpoch
```

涉及 Lease 本身时再验证：

```text
run_leases.lease_token
```

---

# 10. 禁止“0 epoch = 绕过 fencing”进入 Worker

目前存在：

```go
if epoch == 0 {
    return nil
}
```

这种 system/unfenced semantics。

这个能力本身可以保留，但必须隔离。

建议拆成：

```text
WorkerOwnedService
SystemExecutionService
```

而不是在同一个方法中：

```go
epoch == 0
→ 自动绕过 fence
```

Worker Runtime 不应该有机会拿到 unfenced writer。

---

# 11. 阶段一：修复 Reaper 原子性

## 当前问题

当前 Recovery：

```text
ListExpiredLeaseRunIDs
↓
DeleteLeaseIfExpired
↓
GetRun
↓
ReleaseInterrupted
```

存在：

```text
Delete Lease 成功
↓
进程 crash
↓
Run 仍 running
```

的窗口。

因此可能再次形成：

```text
running
+
no lease
```

当前代码确实先删除 expired lease，再单独处理 Run。

---

# 12. 新增 RecoverExpiredLeaseTx

建议：

```go
RecoverExpiredLeaseTx(
    ctx,
    runID,
) (RecoveryResult, error)
```

一个事务完成：

```text
BEGIN

SELECT lease
WHERE run_id=?
AND expires_at <= NOW()
FOR UPDATE

SELECT run
WHERE id=?
FOR UPDATE

校验：
run.status == running

保存旧：
lease_token
lease_epoch

根据 attempt/max_attempts：

A. 可以 retry
    UPDATE runs
    SET status='queued'

    INSERT outbox_events(run.dispatch)

B. 已达到 max attempts
    UPDATE runs
    SET status='failed'
        error_code='lease_expired'

    INSERT terminal RunEvent

DELETE run_leases
WHERE run_id=?
AND lease_token=?

COMMIT
```

整个过程中禁止：

```text
先 DELETE Lease
再操作 Run
```

---

# 13. Reaper ownership 语义

Reaper 不属于普通 Worker Owner。

它属于：

> **Recovery Coordinator**

所以它不应该模拟 Worker ownership。

应该有独立：

```text
Recovery CAS
```

条件：

```text
Run.status = running
AND
Run.lease_epoch = expiredLease对应epoch
AND
Lease 确实 expired
```

建议 `run_leases` 同时保存：

```text
lease_epoch
```

即迁移：

```sql
ALTER TABLE run_leases
ADD COLUMN lease_epoch BIGINT UNSIGNED NOT NULL;
```

Claim Lease 时写：

```text
runs.lease_epoch == run_leases.lease_epoch
```

这样 Recovery 可以直接确认：

```text
这个 expired lease 确实属于当前 Run epoch
```

比仅依赖 token 更明确。

---

# 14. 阶段二：重新定义 Retry State Machine

这是本轮第二个核心。

当前：

```text
ReleaseInterrupted
↓
run.interrupted
↓
requeue
```

但前端和 SSE 又把：

```text
run.interrupted
```

作为终态。

必须彻底消除这一语义冲突。

---

# 15. 推荐 RunAttempt 概念

长期最优架构建议增加：

```text
Run
└── Attempt
```

但本轮暂时不一定要增加完整 `run_attempts` 表。

先从 Event Protocol 开始。

新增：

```text
run.retrying
run.attempt_interrupted
```

推荐只选一个。

建议：

```text
run.retrying
```

Payload：

```json
{
  "attempt": 1,
  "max_attempts": 3,
  "reason": "aily_rate_limit",
  "retry_at": "...",
  "previous_worker": "..."
}
```

---

# 16. Terminal Events 必须唯一

严格定义：

```text
run.completed
run.failed
run.cancelled
```

才是 terminal。

如果保留：

```text
run.interrupted
```

建议定义为：

```text
真正不可恢复地结束
```

否则删除 terminal classification。

不要同时存在：

```text
Run status = queued
RunEvent = terminal
```

这种组合。

---

# 17. 修改 SSE Gateway

当前：

```text
run.completed
run.failed
run.interrupted
```

都会关闭 SSE。

修改为与 Run terminal state 完全一致：

```text
run.completed
run.failed
run.cancelled
```

如果系统暂无 cancel：

```text
run.completed
run.failed
```

即可。

`run.retrying`：

```text
不得关闭 SSE。
```

---

# 18. 修改前端 Event Reducer

当前：

```ts
TERMINAL = new Set([
  'run.completed',
  'run.failed',
  'run.interrupted'
])
```

需要对应调整。

新增：

```ts
case 'run.retrying':
```

UI 建议显示：

```text
正在重试…
```

并保持：

```text
activeRunId = run.id
status = streaming
```

不能：

```text
activeRunId = null
```

---

# 19. 阶段三：重写 ReleaseInterrupted

废弃当前容易混淆的：

```go
ReleaseInterrupted()
ReleaseInterruptedFenced()
```

建议换成两个语义明确的方法。

### RetryOwnedRun

```go
RetryOwnedRun(
    ctx,
    own,
    reason,
    retryAt,
) error
```

一个事务：

```text
verify ownership
↓
Run running → queued
↓
append run.retrying
↓
create outbox dispatch
↓
delete own lease
↓
commit
```

### FailOwnedRun

```go
FailOwnedRun(
    ctx,
    own,
    errorCode,
    message,
) error
```

进入真正 terminal。

这样避免：

```text
“Interrupted 到底是 retry 还是 terminal？”
```

---

# 20. 所有 Retry 操作必须事务化

`RetryOwnedRun` 必须保证：

```text
Run requeue
+
retry Event
+
Outbox
+
Lease cleanup
```

同一 transaction。

否则仍会存在：

```text
Run queued
Outbox 未创建
```

或：

```text
Outbox 已创建
Run 仍 running
```

的窗口。

---

# 21. 阶段四：修 Aily Background Ownership

修改：

```text
backend-go/internal/integrations/aily/executor.go
```

当前 Background Submit 后：

```go
refreshed, err := e.Svc.GetRun(...)
if err == nil {
    run = refreshed
}
```

不得再覆盖 Ownership。

完成 ExecutionOwnership 拆分后自然解决。

如果在重构前临时修：

```go
run = preserveOwnership(run, refreshed)
```

但最终不建议保留这种模式。

---

# 22. Background Poll Event 必须 Owned

当前：

```go
Svc.AppendEvent(
    EventRunPoll,
)
```

改成：

```text
AppendOwnedEvent
```

并确保：

```text
ErrLostOwnership
```

立即：

```text
return ErrLostOwnership
```

不能：

```text
log warning + continue poll
```

---

# 23. retryOrFail 必须 Owned

当前 Aily：

```text
retryOrFail
↓
ReleaseInterrupted
```

必须改为：

```text
RetryOwnedRun
```

不得再进入普通 unfenced Release。

Worker Provider Executor 整个包建议禁止 import/use：

```text
ReleaseInterrupted
CASFinishRun
DeleteLease
AppendEvent
```

普通版本。

---

# 24. 阶段五：Artifact Ownership Closure

当前 Artifact Upsert 需要改成：

```text
PersistArtifactOwned
```

推荐事务：

```text
BEGIN

SELECT runs
WHERE id=?
AND status='running'
AND lease_epoch=?
FOR UPDATE

UPSERT artifact

读取实际 artifact row

INSERT artifact.discovered event

COMMIT
```

这样 stale worker 无法：

```text
写 Artifact
写 Artifact Event
```

---

# 25. 修复 Artifact ID Bug

当前逻辑生成：

```text
newArtifactID
```

执行：

```text
UPSERT
```

如果 duplicate：

```text
数据库保留 old ID
```

但 Event 仍可能发送：

```text
newArtifactID
```

应该修改为：

```text
UPSERT
↓
SELECT actual row
↓
event.artifact_id = actualRow.ID
```

不能使用预生成但未真正落库的 ID。

---

# 26. Final Reconciliation Artifact 幂等

Streaming：

```text
artifact.discovered
```

Final reconciliation：

```text
同 artifact 再发现一次
```

必须最终得到：

```text
同一个 RunArtifact
同一个 local artifact_id
```

Event 可以重复或者 dedupe，但：

```text
Artifact ID
```

必须稳定。

---

# 27. 阶段六：AgentThread Session Ownership

当前 Provider Session Binding：

```text
AgentThread.remote_id
```

也属于 canonical provider state。

建议新增：

```text
BindProviderSessionOwned
```

流程：

```text
verify run ownership
↓
AgentThread identity/provider check
↓
remote_id empty
    → bind
remote_id == current
    → idempotent success
remote_id != current
    → conflict / provider_session_conflict
```

禁止普通：

```sql
UPDATE agent_threads
SET remote_id=?
```

任意覆盖。

---

# 28. Session 不允许 stale worker 覆盖

场景：

```text
Worker A → session_A
Lease lost
Worker B → session_B
Worker A response late arrive
```

必须：

```text
A bind → ErrLostOwnership
```

而不是：

```text
remote_id=session_A
```

---

# 29. 阶段七：Terminal Finalize Transaction

目前：

```text
Run terminal
↓
terminal Event
↓
Assistant Message
```

不是同一事务。

长期应该改成：

```text
FinalizeOwnedRunTx
```

---

# 30. FinalizeOwnedRunTx

单事务内：

```text
1. verify ownership

2. CAS runs
   running → succeeded/failed/cancelled

3. INSERT terminal RunEvent

4. INSERT assistant Message
   如有最终回答

5. Update ScheduleOccurrence terminal
   如 scheduled Run

6. 删除 own Lease

7. 如果 scheduled succeeded：
   创建 Delivery fan-out / Delivery Outbox
   或写可靠的 domain outbox

COMMIT
```

然后：

```text
Redis Pub/Sub
```

全部在 commit 后。

---

# 31. Terminal Event 必须 durable

要保证：

```text
Run.status = succeeded
```

则：

```text
数据库必然存在 run.completed
```

而不是 best-effort。

建议 invariant：

```text
terminal Run
→ exactly one terminal RunEvent
```

通过：

```text
UNIQUE / transaction / idempotent logic
```

保护。

---

# 32. Assistant Message 也必须 durable

目标 invariant：

```text
succeeded Run
AND output.text != empty
AND conversation_id != null

⇒
存在对应 assistant Message
```

否则重新打开 Conversation 时不能出现：

```text
Run 已完成
但回答消失
```

---

# 33. Delivery Fan-out 不应直接作为内存 Hook

现在：

```text
OnRunSucceeded
```

虽然可用，但长期更优方案：

```text
Run Finalize Transaction
↓
domain_outbox
↓
Delivery Dispatcher
```

可以复用现有 Outbox Pattern。

建议新增 Event：

```text
scheduled_run.succeeded
```

Dispatcher 消费后：

```text
DeliveryExecution fanout
```

这样：

```text
API/Worker crash
```

不会丢失 Delivery。

如果当前 Delivery 已经具有独立幂等表，可以先保留 Hook，但最终应向 durable event 收敛。

---

# 34. 阶段八：Scheduler Multi-instance Admission

当前：

```text
pending occurrence
↓
Count active
↓
transaction
↓
Count active again
↓
create Run
```

在两个 Scheduler 同时处理两个 pending occurrence 时仍存在 write skew。

---

# 35. Schedule Admission Lock

在 `admitOne()` transaction 内：

```sql
SELECT id
FROM schedules
WHERE id = ?
FOR UPDATE;
```

之后再：

```text
Count queued/running
↓
如果 0
↓
admit 最早 pending occurrence
```

同一个 Schedule：

```text
同一时刻只能一个 Scheduler 做 admission。
```

---

# 36. Pending 顺序

推荐：

```sql
ORDER BY enqueued_at, id
```

确保：

```text
FIFO
```

不要两个 pending 中随机挑一个。

---

# 37. 一次只 Admit 一个

针对：

```text
overlap=queue
```

一个 Schedule 在同一时间：

```text
最多一个：
queued/running
```

因此一次 transaction：

```text
只 admission 一个 pending occurrence。
```

---

# 38. Scheduler 新测试

新增：

```text
TestDualSchedulerPendingAdmissionNoParallel
```

构造：

```text
Schedule overlap=queue
两个 pending occurrences

Scheduler A
Scheduler B
同时 ProcessDue
```

断言：

```text
queued/running occurrence <= 1
```

以及：

```text
对应 Run <= 1
```

---

# 39. 阶段九：Admin Auth Policy

前端 `/login/admin` 已经独立，但后端必须再收紧。

当前 `VerifyLocalAdmin()` 主要验证本地密码，并未在该逻辑中明确要求 staff。

修改为：

```text
password valid
AND
is_staff = true
```

如果存在：

```text
is_superuser
```

则：

```text
is_staff || is_superuser
```

---

# 40. 禁止普通本地密码账户登录

普通用户：

```text
Feishu OAuth Only
```

Local password：

```text
Admin Only
```

因此旧接口：

```text
/api/auth/login/
```

建议：

### 最优

直接删除。

### 如果为了兼容暂留

必须：

```text
staff-only
deprecated
```

并且加：

```text
Deprecation / Sunset
```

注释或 Header。

OpenAPI 当前仍保留这个 transition endpoint。

---

# 41. Admin Login 安全增强

本轮建议一起加入：

```text
IP based rate-limit
username based rate-limit
failed-login counter
audit log
```

例如：

```text
5 次 / 5 分钟
```

不需要现在做复杂 IAM。

但管理员登录必须写：

```text
audit_logs
```

包括：

```text
admin.login.success
admin.login.failed
```

不要记录密码。

---

# 42. 阶段十：Execution Invariant Checker

新增：

```text
cmd/invariant-checker
```

或者先作为：

```text
worker 内定时巡检
```

推荐最终独立。

---

# 43. 第一批 Invariant

### Invariant A

```text
running Run
必须存在 active lease
```

SQL：

```text
runs.status='running'
AND
LEFT JOIN run_leases
WHERE lease missing
```

数量必须：

```text
0
```

---

# 44. Invariant B

```text
queued Run
不得存在 active lease
```

---

# 45. Invariant C

```text
terminal Run
不得存在 active lease
```

---

# 46. Invariant D

```text
terminal Run
必须存在 terminal RunEvent
```

---

# 47. Invariant E

```text
running Run lease_epoch
=
run_leases.lease_epoch
```

如果按照本计划把 epoch 加入 run_leases。

---

# 48. Invariant F

```text
Schedule overlap=queue

同一 Schedule：
queued + running occurrence <= 1
```

---

# 49. Invariant G

```text
pending Outbox oldest age
不得超过阈值
```

比如：

```text
30s
```

---

# 50. 不要自动“修复”全部 invariant

Checker 第一阶段：

```text
detect
metric
log
alert
```

不要直接随意修改数据库。

只有 Reaper 是明确的自动 Recovery Coordinator。

---

# 51. Metrics 补全

增加：

```text
execution_invariant_violation_total{type}
```

具体：

```text
running_without_lease
queued_with_lease
terminal_with_lease
terminal_without_terminal_event
schedule_overlap_violation
```

以及：

```text
provider_limiter_degraded
```

修复报告已经暴露 `Degraded()`，但指标注册仍在遗留项里。

---

# 52. 本轮测试矩阵

必须新增以下测试。

---

# 53. T1 Reaper Atomic Crash Safety

模拟：

```text
Run running
Lease expired
```

在 Recovery transaction 中故意让：

```text
Outbox INSERT
或
Run UPDATE
```

失败。

断言：

```text
不能产生
running + no lease
```

要么：

```text
running + old expired lease
```

等待再次 Recovery。

要么：

```text
queued + no lease + outbox
```

完整成功。

---

# 54. T2 Stale Worker Retry

场景：

```text
Worker A claim
↓
A provider request
↓
lease expired
↓
B reclaim
↓
A 收到 429
↓
retryOrFail
```

断言：

```text
A 无法：
写 retry Event
Requeue B Run
创建新 Outbox
删除 B Lease
```

返回：

```text
ErrLostOwnership
```

---

# 55. T3 Retry Event Is Non-Terminal

```text
Run running
↓
RetryOwnedRun
```

断言：

```text
Event = run.retrying
Run = queued
SSE 不认为 terminal
```

前端单测：

```text
activeRunId 仍保留
assistant status 不变 done/failed
```

---

# 56. T4 Background Ownership Preservation

```text
Background Aily Run
↓
Submit
↓
DB refresh
↓
Polling
↓
Finish
```

断言：

```text
LeaseToken 没丢
Finalize 成功
Lease 被删除
```

---

# 57. T5 Background Stale Worker

```text
A background worker
↓
Lease lost
↓
B takes over
↓
A poll returns
```

断言：

```text
A 无法 Append run.poll
A 无法 Finalize
```

---

# 58. T6 Artifact Fencing

```text
A owns
↓
Lease lost
↓
B owns
↓
A discovers artifact
```

断言：

```text
artifact 不写入
artifact event 不写入
```

---

# 59. T7 Artifact Idempotency

Streaming：

```text
artifact X
```

Final reconciliation：

```text
artifact X
```

断言：

```text
DB exactly one artifact
两次发现时 local artifact id 一致
```

---

# 60. T8 AgentThread Session Fence

```text
A session_A
A loses lease
B session_B
A late bind
```

断言：

```text
remote_id = session_B
```

---

# 61. T9 Terminal Transaction Failure

故意让：

```text
Message insert
```

失败。

断言：

```text
Run 不得已经 commit succeeded
```

整个 Finalize rollback。

---

# 62. T10 Terminal Event Guarantee

所有：

```text
succeeded
failed
cancelled
```

Run：

```text
必须恰好有一个 terminal RunEvent
```

---

# 63. T11 Dual Scheduler Admission

两个 Scheduler 并发：

```text
同 Schedule
两个 pending
```

断言：

```text
active occurrences <= 1
```

---

# 64. T12 Admin Non-staff Rejection

创建：

```text
PasswordSet=true
IsStaff=false
```

调用：

```text
POST /api/identity/admin/login
```

必须：

```text
401
```

---

# 65. T13 Redis Limiter Outage

使用不可访问 Redis：

```text
20 concurrent Allow()
limit=5/s
```

断言：

```text
本进程即时允许 <= 5
Degraded() == true
```

Redis 恢复后：

```text
Degraded() == false
```

---

# 66. 数据库迁移计划

建议新增：

```text
0010_run_lease_epoch_link
```

内容：

```sql
ALTER TABLE run_leases
ADD COLUMN lease_epoch BIGINT UNSIGNED NOT NULL;
```

新 Lease：

```text
lease_epoch = Claim 时 runs.lease_epoch
```

如果开发库可以重置，也可以更彻底整理 Schema。

但生产迁移：

```text
只做 forward migration
```

不要重新修改历史 0001/0009。

---

# 67. SQL 兼容原则

继续要求：

```text
TiDB 8.0
+
MySQL 5.7 compatible SQL
```

不得引入：

```text
SKIP LOCKED
RETURNING
MySQL 8 only window feature
UUID_TO_BIN
CHECK enforcement依赖
```

普通：

```sql
SELECT ... FOR UPDATE
```

允许使用。

---

# 68. 推荐开发顺序

严格按照下面顺序。

## Phase 1 — Ownership Core

完成：

```text
ExecutionOwnership
ClaimedRun
Worker Handler signature
Owned Service APIs
```

先不改业务语义。

目标：

> 编译通过。

---

# 69. Phase 2 — Retry State Machine

完成：

```text
run.retrying
RetryOwnedRun
FailOwnedRun
删除 retry 场景中的 run.interrupted
前端/SSE terminal classification
```

目标：

> Retry 不再被当成 Run Terminal。

---

# 70. Phase 3 — Reaper

完成：

```text
run_leases.lease_epoch
RecoverExpiredLeaseTx
atomic recovery
```

目标：

```text
永远不产生 running + no lease
```

---

# 71. Phase 4 — Aily Ownership

改完：

```text
Streaming
Background
Polling
Retry
Reconcile
ExternalRunID
```

所有路径。

目标：

> Aily Executor 不存在 Worker-owned 的 unfenced write。

---

# 72. Phase 5 — Artifact + AgentThread

完成：

```text
PersistArtifactOwned
BindProviderSessionOwned
artifact stable ID
```

---

# 73. Phase 6 — Finalize Transaction

完成：

```text
Run terminal
terminal event
assistant message
occurrence state
lease cleanup
delivery durable trigger
```

统一事务。

这是本轮最重要的 durability 提升。

---

# 74. Phase 7 — Scheduler Admission

加入：

```text
Schedule FOR UPDATE admission lock
FIFO pending
```

---

# 75. Phase 8 — Identity Closure

完成：

```text
Admin IsStaff
legacy local login closure
login audit
```

---

# 76. Phase 9 — Invariants / Metrics / CI

最后加入：

```text
Invariant Checker
Prometheus
Redis degraded metric
CI
```

---

# 77. CI 最低要求

建议增加：

```text
.github/workflows/backend.yml
.github/workflows/frontend.yml
```

PR 必跑：

```text
go fmt check
go vet ./...
go test ./...
go build ./...

npm ci
tsc --noEmit
vitest run
vite build
```

---

# 78. Integration CI

由于真实 TiDB/Redis 可能不适合开放 GitHub Runner，可以：

```text
普通 CI
+
self-hosted Integration CI
```

或者：

```text
Docker TiDB
Redis
```

运行：

```text
STUDIO_TEST_TIDB=1
STUDIO_TEST_REDIS=1
```

至少 `dev` merge 前必须有 Integration Gate。

---

# 79. 明确禁止事项

本轮禁止：

```text
❌ 新增其他 Provider
❌ 做 Codex
❌ 做 GraphFlow
❌ 大规模扩 Workflow
❌ 继续扩 Schedule UI
❌ 再做一套 Queue
❌ Redis 成 Source of Truth
❌ 为了赶进度跳过 Transaction
❌ 为了少改代码继续保留 ownership 在 Run 内
```

目标只有：

> Correctness Closure。

---

# 80. 不要为了兼容旧代码保留双套 Execution API

这点非常重要。

如果新：

```text
Owned API
```

已经完成，则逐步删除或 internalize：

```text
CASClaim
AcquireLease
ReleaseInterrupted
AppendEvent（Worker路径）
DeleteLease（Worker路径）
HeartbeatLease(worker_id)
```

否则后面新的开发者还是可能绕过 Ownership。

---

# 81. 编译期约束优先于 Code Review 约束

本轮最大的架构原则：

> **不能依赖“开发者记得使用 Fenced 方法”。**

应该做到：

```text
Provider Executor
只能拿到 WorkerOwnedExecutionService
```

该 Service 根本没有：

```text
unfenced write
```

这是最佳解。

---

# 82. 推荐目录调整

```text
internal/execution/
├── domain.go
├── ownership.go
├── claim.go
├── retry.go
├── finalize.go
├── recovery.go
├── events.go
├── artifacts.go
├── outbox.go
├── worker.go
└── invariant/
    ├── checker.go
    └── metrics.go
```

不一定必须立即拆成这么多文件。

但逻辑边界建议最终如此。

---

# 83. Provider Executor 理想依赖

Aily Executor 最终不要拿到整个：

```go
*execution.Service
```

而是依赖接口：

```go
type OwnedRunWriter interface {
    AppendEvent(...)
    UpdateExternalRunID(...)
    PersistArtifact(...)
    BindProviderSession(...)
    Retry(...)
    Finalize(...)
}
```

Provider 只能执行 Provider 逻辑。

Execution Ownership 由 Execution Layer 管理。

---

# 84. 最终状态机

最终 Run 状态建议保持：

```text
queued
  ↓
running
  ├──── provider transient problem
  │
  └→ queued      + run.retrying
       ↓
     running
       │
       ├→ succeeded + run.completed
       ├→ failed    + run.failed
       └→ cancelled + run.cancelled
```

不要：

```text
running
→ run.interrupted
→ queued
```

同时又声明：

```text
run.interrupted = terminal
```

---

# 85. Attempt 建模建议

当前：

```text
runs.attempt
```

可以继续使用。

后续如果需要更强审计：

```text
run_attempts
```

可以单独建设：

```text
id
run_id
attempt_no
worker_id
lease_epoch
started_at
ended_at
end_reason
provider_external_run_id
```

但本轮不强制。

---

# 86. 本轮验收标准

只有以下全部满足才能关闭：

### Execution

```text
□ running Run 不可能无 Lease
□ stale worker 无法写任何 canonical state
□ stale worker 无法创建 retry outbox
□ stale worker 无法删除新 owner lease
□ Background 与 Streaming fencing 一致
```

### Retry

```text
□ retry 使用 run.retrying
□ retry 不关闭 SSE
□ retry 不清 activeRunId
□ terminal event 只对应 terminal Run
```

### Artifact / Session

```text
□ artifact stale write impossible
□ artifact retry idempotent
□ Session stale overwrite impossible
```

### Finalization

```text
□ terminal Run + terminal Event 原子
□ succeeded answer + assistant Message 原子
□ Lease terminal cleanup 原子
```

### Scheduler

```text
□ 多 Scheduler 同 Schedule overlap=queue 永不并行
```

### Identity

```text
□ 普通用户不能用本地密码进入 Studio
□ /login/admin 只允许 staff/admin
```

### Testing

```text
□ go build ./...
□ go vet ./...
□ go test ./...
□ integration tests
□ tsc --noEmit
□ vitest
□ vite build
```

全部通过。

---

# 87. 生产上线 Gate

上线前专门跑 SQL：

```text
SELECT COUNT(*)
FROM runs r
LEFT JOIN run_leases l ON l.run_id = r.id
WHERE r.status='running'
AND l.run_id IS NULL;
```

必须：

```text
0
```

---

# 88. 第二个 Gate

```text
terminal Run with lease
```

必须：

```text
0
```

---

# 89. 第三个 Gate

```text
Schedule overlap queue
active occurrence > 1
```

必须：

```text
0
```

---

# 90. 第四个 Gate

抽样所有 terminal Run：

```text
Run terminal
→ terminal Event
```

必须一致。

---

# 91. 第五个 Gate

Aily：

```text
user identity
→ UAT
```

仍然必须保持。

禁止此次重构误把：

```text
User → TAT fallback
```

重新引入。

当前 Aily AuthResolver 在 user mode 下明确解析用户 UAT，仅 tenant mode 才走 TAT，这一行为必须保持。

---

# 92. 建议的提交拆分

不要一个超级大 Commit。

建议：

```text
refactor(execution): separate immutable run ownership

fix(execution): make retry state non-terminal and ownership-fenced

fix(execution): make lease recovery transactional

fix(aily): enforce ownership across streaming/background execution

fix(execution): fence artifact and provider session persistence

refactor(execution): transactional run finalization

fix(schedule): serialize pending occurrence admission

fix(identity): enforce staff-only local admin login

test(execution): add ownership/recovery chaos suite

chore(ci): add execution correctness gates
```

便于逐项回归。

---

# 93. 开发智能体执行要求

开发智能体开始前必须先读：

```text
docs/Creation Agent Studio 未来演进完整架构文档.md

docs/potal 完整架构实施评测报告（dev 分支）.md

docs/potal 架构评测问题修复报告（Execution Correctness Hardening）.md
```

然后重点检查：

```text
backend-go/internal/execution/
backend-go/internal/integrations/aily/
backend-go/internal/automation/scheduler/
backend-go/internal/transport/sse/
backend-go/internal/transport/http/
backend-go/db/queries/
backend-go/db/migrations/
backend-go/tests/integration/
frontend/src/stores/useRunChatStore.ts
frontend/src/services/runStream.ts
```

---

# 94. 开发智能体工作流程

严格按：

```text
1. 阅读架构与当前代码
2. 输出简短 Gap Analysis
3. 不等待确认
4. 开始 Phase 1
5. 每个 Phase 编译/测试
6. 完成后进入下一 Phase
7. 全量 regression
8. 输出变更报告
```

除非涉及：

```text
不可逆数据删除
生产数据风险
架构文档明显自相矛盾
```

否则不要因为小问题反复停下来询问。

---

# 95. 最终目标

本轮结束后，Creation Agent Studio 的 Execution Plane 应满足：

> **At-least-once Provider Execution + Exactly-one Canonical Writer + Idempotent Final Reconciliation**

不追求：

```text
Exactly-once remote execution
```

因为 Aily 等外部 Provider 无法提供这一保证。

我们真正保证的是：

```text
Provider 可能重复
            │
            ▼
Reconciliation 幂等
            │
            ▼
Studio Canonical State
始终只有合法 Ownership 可以写
```

---

# 96. 本轮完成后的架构成熟度目标

当前约：

```text
80/100 Production Readiness
```

完成本计划后目标：

```text
Execution Correctness     95+
Lease / Fencing           95+
Scheduler                 92+
Aily Runtime              92+
SSE / Event               92+
Identity                  92+
整体 Production Readiness ≈90~93
```

之后再进入：

```text
Aily 附件 streaming
RunEvent retention
Circuit Breaker
DLQ
Object Storage
Workflow RuntimeAdapter
更多 Provider
```

会比较合理。

---

# 97. 一句话执行原则

本轮所有设计决策都围绕这一条：

> **Ownership 不是 Run 的一个可选字段，而是 Worker 修改 canonical state 的强制能力凭证。没有 Ownership，就没有写权限。**

当这一原则从“设计文档约定”变成：

> **Go 类型系统 + Service API + TiDB Transaction + Integration Test**

共同强制的规则以后，Execution Correctness Hardening 才真正算完成。