# potal 最新架构终审暨 Production Hardening 开发执行报告

**审查分支：** `dev`  
**最新核心提交：** `a86729a32d7c9640dff9f84f8ab0798f6b2920b0`  
**排除范围：** Git 历史、仓库公开状态、历史敏感信息、密钥轮换  
**审查重点：** Execution / Lease / Scheduler / Delivery / Provider Admission / Rate Limit / CI / MySQL 5.7 Compatibility

---

# 第一部分：最新总体结论

本轮修复以后，系统已经不需要继续进行 Execution Core 大重构。

之前存在的：

```text
stale worker write
running + no lease
Occurrence 不收敛
Delivery commit 后丢失
Lease cleanup best effort
```

等核心问题，目前主体已经真正解决。

最新代码已经形成比较清晰的：

```text
Run
↓
Claim + ExecutionOwnership
↓
Provider Admission
↓
Provider Runtime
↓
Final Reconciliation
↓
Atomic Finalize
↓
Durable Delivery
```

架构。

我现在对核心 Execution Plane 的评价已经提高到：

```text
约 96 / 100
```

但是整个生产运行体系暂时仍然不是 100% Production GO。

当前真正需要继续处理的主要是：

```text
P0

1. Provider Inflight Semaphore 缺少 attempt-level fencing
2. Provider Admission 会错误消耗 Run Attempt
3. GitHub Backend CI 当前仍然没有真正执行
```

以及几个 P1：

```text
4. 正常 Redis 主队列实际上仍然是 FIFO，priority 只影响 fallback
5. scheduled 在持续 interactive 流量下仍可能 starvation
6. retry 的 available_at 和 Outbox 发布时间不一致
7. Feishu 外部发送仍存在 send-success / DB-crash 的重复发送窗口
```

---

# 第二部分：上一轮问题复核

| 上一轮问题 | 最新状态 |
|---|---|
| Strict Active Ownership | ✅ 完全关闭 |
| Finalize terminal idempotency 与普通 write 分离 | ✅ 完全关闭 |
| ExternalRunID stale write | ✅ 关闭 |
| Lease delete error 被吞 | ✅ 关闭 |
| Finalize Occurrence error 被吞 | ✅ 关闭 |
| Reaper exhausted 不结束 Occurrence | ✅ 关闭 |
| run.cancelled 映射错误 | ✅ 关闭 |
| Delivery 依赖 post-commit hook | ✅ 关闭 |
| Durable Delivery Outbox | ✅ 已实现 |
| Schedule / Run invariant | ✅ 加强 |
| Provider Priority | ⚠️ 部分实现 |
| Provider max_inflight | ⚠️ 已实现但存在新的 fencing 问题 |
| MySQL 5.7 CI | ⚠️ 已加入，但尚未实际跑起来 |
| TiDB + Redis GitHub CI | ❌ 当前 workflow 自身失败 |

---

# 第三部分：已经完全修好的核心问题

## 1. Active Ownership 和 Finalize Ownership 已真正拆开

现在已经有：

```text
verifyActiveOwnershipTx()
```

严格要求：

```text
Run.status = running

AND

Run.lease_epoch = Ownership.LeaseEpoch
```

而：

```text
verifyFinalizeOwnershipTx()
```

才允许：

```text
terminal
→ idempotent no-op
```

这正是上一轮要求的设计。

因此：

```text
Artifact
Provider Session
ExternalRunID
Retry
Event
```

不再能够因为 Run 已 terminal 而绕过 Ownership。

这一项可以正式关闭。

---

# 2. ExternalRunID 已经变成真正的 set-once

现在：

```text
UpdateExternalRunIDOwned
↓
Transaction
↓
verifyActiveOwnershipTx
↓
检查现有 external_run_id
```

语义已经明确：

```text
empty
→ set

same value
→ idempotent success

different value
→ ErrExternalRunIDConflict
```

而不是直接裸 UPDATE。

这比单纯：

```sql
WHERE lease_epoch = ?
```

更完整。

---

# 3. Lease Cleanup 已经强一致

目前新增：

```text
deleteOwnedLeaseTx
```

严格检查：

```text
SQL error
RowsAffected
LeaseToken
```

要求：

```text
RowsAffected == 1
```

否则整个 Transaction 失败。

现在：

```text
Retry
Finalize
Reaper
```

都不再使用：

```go
_, _ = DeleteLease(...)
```

这种危险模式。  

所以：

```text
queued + lease
terminal + lease
```

由正常 Worker 生命周期产生的可能性已经大幅收紧。

---

# 4. ScheduleOccurrence 现在已经进入 Finalize 强事务

`FinalizeOwnedRun()` 当前已经做到：

```text
terminal RunEvent
+
Run terminal CAS
+
Assistant Message
+
ScheduleOccurrence terminal
+
DeliveryExecution
+
Delivery Outbox
+
Lease Cleanup
```

同一个 TiDB transaction。

任何一部分失败：

```text
ROLLBACK
```

这是目前非常重要的架构进步。

---

# 5. Reaper exhausted 已经收敛 Occurrence

当前 Reaper exhausted：

```text
Run → failed

↓

run.failed

↓

ScheduleOccurrence → failed

↓

Lease delete

↓

COMMIT
```

已经真正实现。

因此之前：

```text
Run failed
Occurrence running forever
```

导致 Schedule 永远认为还有 active occurrence 的 P0 已关闭。

---

# 6. Delivery Durability 主体已经正确

之前：

```text
Finalize COMMIT
↓
内存 OnRunSucceeded
↓
创建 Delivery
```

存在进程 crash 窗口。

现在改成：

```text
Finalize Transaction

↓

CreateDeliveryExecutionsTx

↓

DeliveryExecution
+
delivery.dispatch Outbox

↓

COMMIT
```

已经是 durable request。

`Dispatcher.CreateInTx()` 还使用：

```text
Occurrence + Delivery Target
```

幂等屏障，重复 fan-out 不会重复创建 DeliveryExecution。

这部分我现在给：

```text
96 / 100
```

---

# 7. `run.cancelled` 已修正

当前：

```text
succeeded → run.completed

cancelled → run.cancelled

failed → run.failed
```

已经符合状态机。

此前：

```text
cancelled → run.completed
```

的问题已经关闭。

---

# 8. Invariant Checker 也已经加强

目前除了：

```text
running_without_lease
queued_with_lease
terminal_with_lease
terminal_without_terminal_event
lease_epoch_mismatch
schedule_overlap
outbox_age
```

之外，又加入了：

```text
scheduled_run_occurrence_mismatch

missing_delivery_execution
```



说明目前已经从：

> 出问题以后看日志

开始进入：

> 主动验证分布式系统 invariant

这个阶段。

这是正确方向。

---

# 第四部分：目前最重要的新 P0

# P0-1：Provider Inflight Semaphore 缺少 Ownership Fencing

这是我本轮发现最重要的新问题。

你已经新增了：

```text
InflightLimiter
```

使用：

```text
Redis ZSET
```

维护全局 Provider 并发。

整体方向正确。

但是目前 Redis ZSET 的 Member 是：

```go
runID.String()
```

而不是：

```text
Run Attempt / ExecutionOwnership
```

---

# 9. 为什么只用 RunID 不够

假设：

```text
Worker A

Run 123
epoch = 1
```

Acquire：

```text
ZSET member = run-123
```

之后 A Lease 失效。

Reaper：

```text
Run → queued
```

Worker B：

```text
Run 123
epoch = 2
```

重新 Acquire。

B 使用的依然是：

```text
member = run-123
```

此时 A 和 B 的 Provider Slot 在 Redis 中无法区分。

---

# 10. stale worker 可以删除新 worker slot

Worker 执行结束时：

```go
ProviderInflight.Release(runID)
```

本质：

```text
ZREM provider:...:inflight run-123
```



如果：

```text
A lease lost
B 已经重新取得 Ownership + Provider Slot

A 晚一点退出
```

A 的 defer：

```text
Release(run-123)
```

可以把：

```text
B 的 Provider Slot
```

直接删除。

结果：

```text
真实 Provider inflight = 100

Redis 认为 = 99

↓

允许新的 Run 进入

↓

真实 inflight = 101
```

所以：

> `max_inflight` 目前并不能严格成立。

---

# 11. Heartbeat 还有同样的问题

当前 Run heartbeat loop：

```text
HeartbeatOwned()

↓

ProviderInflight.Renew(runID)
```

Provider slot renew 也是：

```text
ZADD runID
```



而 `Renew()` 没有：

```text
XX only
Ownership token
Lease epoch
ProviderSlot token
```

它甚至可以重新创建已经不存在的 slot。

更关键的是目前执行顺序是：

```text
Run Lease Heartbeat

Provider Slot Renew

然后才判断 Run ownership 是否已经 lost
```

所以一次发现：

```text
Run Lease 已丢失
```

的 heartbeat，本轮仍可能先续一次 Provider Slot。

---

# 12. 正确模型：ProviderSlot 必须是 Attempt Scoped

新增：

```go
type ProviderSlot struct {
    RunID      ids.ID
    LeaseEpoch uint64
    LeaseToken ids.ID
    Member     string
}
```

Member 建议：

```text
{run_id}:{lease_epoch}:{lease_token}
```

例如：

```text
0199...:4:01AB...
```

---

# 13. Acquire 接口

从：

```go
Acquire(ctx, runID)
```

改成：

```go
Acquire(
    ctx,
    ownership ExecutionOwnership,
) (*ProviderSlot, bool, error)
```

---

# 14. Renew

必须：

```text
只续当前 ProviderSlot.Member
```

并且：

```text
member 不存在
→ 不允许重新创建
```

即：

```text
ZADD XX
```

或者 Lua：

```text
if ZSCORE(member) == nil
    return SLOT_LOST

ZADD XX expiry member
```

---

# 15. Release

必须：

```text
ZREM exact ProviderSlot.Member
```

而不是：

```text
ZREM runID
```

因此：

```text
Worker A
```

永远无法删除：

```text
Worker B
```

的 slot。

---

# 16. Worker tracking 也要调整

当前：

```text
trackInflight()
```

在 Provider Slot Acquire 之前就已经登记。

建议：

```go
type executionControl struct {
    cancel       context.CancelFunc
    own          ExecutionOwnership
    providerSlot *ProviderSlot
}
```

流程：

```text
Claim Run
↓
track ExecutionOwnership
↓
Acquire ProviderSlot
↓ success
attach ProviderSlot to executionControl
```

Heartbeat：

```text
Heartbeat RunLease

↓

如果 !ok
    cancel()
    不 Renew ProviderSlot
    Release 自己的 ProviderSlot

↓

如果 ok && ProviderSlot != nil
    Renew exact ProviderSlot
```

---

# 17. Provider Slot 测试

必须新增：

```text
TestInflightStaleReleaseCannotDeleteNewOwnerSlot

TestInflightStaleRenewCannotExtendNewOwnerSlot

TestInflightRenewMissingSlotDoesNotRecreate

TestInflightRejectedRunNeverGetsSlotFromHeartbeat

TestInflightGlobalMaxAcrossWorkers

TestInflightCrashExpiresSlot
```

这是当前最大的 Execution Release Blocker。

---

# P0-2：Provider Admission 正在错误消耗 Attempt

这是第二个很重要的问题。

当前：

```sql
CASClaimRun
```

直接：

```sql
attempt = attempt + 1
```



但是 Provider Inflight 是：

```text
Claim 成功以后
```

才检查。

---

# 18. 这会导致什么

例如：

```text
max_attempts = 3
```

Provider 满载。

第一次：

```text
Claim
attempt 0 → 1

max_inflight full
↓
requeue
```

第二次：

```text
attempt 1 → 2

max_inflight full
↓
requeue
```

第三次：

```text
attempt 2 → 3

max_inflight full
↓
requeue
```

实际上：

```text
Aily = 一次都没有调用
```

但是 Run 已经认为：

```text
3 attempts
```

---

# 19. Aily 现在确实依赖 Attempt 判断是否继续重试

当前：

```go
if claimed.Run.Attempt < claimed.Run.MaxAttempts {
    Retry
}

Fail
```



所以 admission contention 会直接影响：

```text
Provider retry budget
```

这两个语义不应该混在一起。

---

# 20. 正确语义

必须定义：

```text
Claim Count
≠
Provider Attempt
```

当前 `runs.attempt` 应该明确代表：

> 真正进入 Provider Execution 的次数。

因此：

```text
Claim
```

不应该自动：

```text
attempt++
```

---

# 21. 建议新增 BeginProviderAttemptOwned

SQL：

```sql
UPDATE runs
SET attempt = attempt + 1
WHERE id = ?
  AND status = 'running'
  AND lease_epoch = ?
  AND attempt < max_attempts;
```

然后返回当前：

```text
attempt
max_attempts
```

---

# 22. 最佳调用位置

流程：

```text
Claim
↓
Provider Inflight Acquire
↓
Provider Rate Limit Acquire
↓
BeginProviderAttemptOwned
↓
Aily StartChat / Submit
```

也就是说只有：

> 准备真正开始一次 Provider execution

才消耗 Attempt。

---

# 23. Admission Requeue 不消费 Attempt

以下原因：

```text
provider_inflight_limit

provider_inflight_unavailable

admission_wait
```

都应该：

```text
requeue
```

但：

```text
attempt 不变
```

---

# 24. 建议保留 Attempt 的含义

建议文档正式定义：

```text
attempt
=
provider execution attempt number
```

未来如果需要记录：

```text
多少次 claim
多少次 admission
```

使用独立 metrics：

```text
run_claim_total
provider_admission_requeue_total
```

不要污染业务 Retry Count。

---

# 25. 测试

新增：

```text
TestProviderAdmissionDoesNotConsumeAttempt

TestProviderAttemptIncrementsBeforeSubmit

TestThreeInflightRequeuesStillLeavesAttemptZero

TestReaperBeforeProviderAttemptDoesNotExhaustRetries

TestProviderFailureConsumesAttempt
```

---

# P0-3：GitHub Backend CI 当前仍然不可用

这是明确事实。

最新 commit 对应的 GitHub Actions：

```text
conclusion = failure
```



更重要的是 Jobs：

```text
total_count = 0
jobs = []
```



说明不是：

```text
某个测试失败
```

而是 Workflow 根本没有正常实例化 Jobs。

---

# 26. 当前 backend.yml 有明确 YAML 问题

`integration` 中现在出现：

```yaml
env:
  DEBIAN_FRONTEND: noninteractive

services:
  ...

env:
  STUDIO_TEST_TIDB: '1'
  ...
```

同一级重复：

```yaml
env:
```

`mysql57` 也一样。

这必须先合并。

---

# 27. 正确方式

例如：

```yaml
integration:
  runs-on: ubuntu-latest

  env:
    DEBIAN_FRONTEND: noninteractive
    STUDIO_TEST_TIDB: '1'
    STUDIO_TEST_REDIS: '1'
    DB_HOST: 127.0.0.1
    DB_PORT: 4000
    ...
```

MySQL57 同理。

---

# 28. TiDB Health Check 也建议改

当前：

```yaml
--health-cmd "curl ... || mysqladmin ..."
```

这些命令是在：

```text
TiDB container 内部
```

执行。

而：

```text
Install database clients
```

是在 GitHub Runner 主机上执行。

两者不是一个环境。

因此不能假设：

```text
pingcap/tidb
```

镜像一定包含：

```text
curl
mysqladmin
```

最稳妥方式：

> 不给 TiDB service container 配复杂 health-cmd。

启动以后：

```text
Runner
↓
安装 MySQL client
↓
循环 mysql -h127.0.0.1 -P4000 SELECT 1
```

由 host-side wait 做 readiness。

---

# 29. CI 建议顺便加入 actionlint

这样以后类似：

```text
重复 YAML key
非法 workflow
```

提交之前就能发现。

Gate：

```text
actionlint
gofmt
go vet
go test
go build
```

---

# 30. MySQL 5.7 Job 已加入，这是正确进展

当前 backend workflow 已经增加：

```text
mysql:5.7
```

兼容 Job。

这符合：

```text
TiDB 8 Primary
MySQL 5.7 Compatibility Floor
```

架构。

但是由于整个 Workflow 当前无法正常生成 Job：

> 目前只能说“CI 配置已经添加”，不能说“MySQL 5.7 compatibility 已经验证”。

---

# 第五部分：P1 问题

# P1-1：Priority 目前只影响 DB fallback，不影响正常 Redis 主路径

你已经在 DB query 加入：

```text
interactive_user = 100
retry            = 70
scheduled_high   = 50
scheduled_normal = 45
```

再加 waiting bonus。

这个方向很好。

但是正常 Worker：

```text
XREADGROUP
↓
取到 Redis message 里的指定 run_id
↓
Claim 那个 run_id
```



也就是说：

```text
Redis Stream
```

仍然是 FIFO。

Priority query 只用于：

```text
Fallback Scan
```

---

# 31. 实际例子

08:30：

```text
1000 scheduled Run
```

已经全部进入：

```text
queue:feishu_aily
```

08:30:01：

用户发来：

```text
Interactive Run
```

Redis 消费顺序仍然可能是：

```text
schedule #1
schedule #2
...
schedule #1000
interactive
```

所以：

> “Interactive 优先于 Scheduled”

目前在主执行路径上还没有真正成立。

---

# 32. 当前 Aging 也不能保证 Scheduled 永不饿死

scheduled_normal：

```text
45
```

waiting bonus 最大：

```text
40
```

最终：

```text
85
```

interactive：

```text
100
```

所以持续有 Interactive 流量时：

```text
Scheduled 永远无法靠 aging 超过 Interactive
```

即仍有 starvation 理论可能。

---

# 33. 推荐最终设计：Weighted Priority Streams

我建议不要继续把这一层做成越来越复杂的 SQL CASE。

直接明确三类 Queue：

```text
queue:feishu_aily:interactive

queue:feishu_aily:retry

queue:feishu_aily:scheduled
```

需要时 scheduled 再细分：

```text
scheduled_high
scheduled_normal
```

---

# 34. Worker 使用 Weighted Fair Scheduling

例如默认：

```text
Interactive  7
Retry        1
Scheduled    2
```

仅作为可配置初始权重。

如果：

```text
Interactive empty
```

其额度自动借给其他 Queue。

这样：

```text
大量 Schedule
```

不会卡住用户。

同时：

```text
持续 Interactive
```

也不会让 Schedule 永远不执行。

---

# 35. Outbox payload 加 priority_class

目前 Run Outbox：

```json
{
  "run_id": "...",
  "provider": "feishu_aily"
}
```

建议变：

```json
{
  "run_id": "...",
  "provider": "feishu_aily",
  "priority_class": "interactive"
}
```

Relay 根据：

```text
provider + priority_class
```

选 Stream。

---

# P1-2：Retry available_at 与 Outbox 发布时间不同步

当前 Retry：

```sql
Run status = queued

priority = retry

available_at = NOW() + 1 SECOND
```



但 Retry 同时立即创建：

```text
run.dispatch Outbox
```

而 Outbox 本身：

```text
available_at = CURRENT_TIMESTAMP
```



---

# 36. 实际行为

```text
t = 0
Retry
Run available_at = t+1s

↓

Outbox immediately relay

↓

t = 0.5s
Worker 收到消息

↓

CASClaimRun
available_at 尚未到

↓

CAS = 0 rows

↓

Worker ACK message
```

现在这个 Run 已经：

```text
没有新的 Redis wakeup
```

只能等：

```text
Fallback Scan
```

---

# 37. 当前 fallback 默认间隔

配置默认：

```text
RUN_REAPER_INTERVAL = 20s
```



所以一个设计成：

```text
1 秒后重试
```

的任务可能变成：

```text
约 20 秒后
```

才能再次被发现。

---

# 38. 正确设计

Run 和 Outbox 必须共享：

```text
retry_at
```

例如：

```text
retryAt := now + RequeueDelay
```

事务：

```text
Run.available_at = retryAt

Outbox.available_at = retryAt
```

Relay 本来已经：

```sql
WHERE available_at <= CURRENT_TIMESTAMP
```

所以不需要额外 Scheduler。

---

# 39. 建议新增

```text
CreateOutboxEventAt
```

并让：

```go
RetryOwnedRun(
    ...,
    retryAt time.Time,
)
```

接受明确 retry time。

当前：

```text
RUN_REQUEUE_DELAY
```

配置已经存在，但 SQL 实际硬编码了：

```text
1 SECOND
```

这两者也应该合并。

---

# P1-3：Feishu Delivery 仍然是 At-Least-Once External Side Effect

数据库 Delivery 现在已经很可靠。

但是：

```text
Feishu Send
```

属于外部副作用。

当前 Worker：

```text
Send Feishu
↓
CAS Delivery → succeeded
↓
ACK
```



---

# 40. Crash Window

如果：

```text
Feishu 实际发送成功
↓
Worker crash
↓
DeliveryExecution 尚未写 succeeded
```

Reclaimer：

```text
sending → pending
```

之后重新发送。

结果用户可能收到：

```text
同一条消息两遍
```

---

# 41. 当前 Sender 没有 Idempotency Key

目前接口是：

```go
Send(
    ctx,
    senderUserID,
    Target,
) error
```

并没有：

```text
delivery_execution_id
idempotency_key
```

传入底层发送。

---

# 42. 推荐调整接口

```go
type DeliveryRequest struct {
    ExecutionID   ids.ID
    SenderUserID  int64
    Target        Target
    IdempotencyKey string
}
```

其中：

```text
IdempotencyKey
=
DeliveryExecution.ID
```

然后 Provider Adapter：

```text
如果飞书接口支持幂等参数
→ 直接传递

如果不支持
→ 明确定义 Delivery 为 At-Least-Once
```

不能宣称：

```text
Exactly-once Feishu Delivery
```

因为当前架构无法证明。

这不是 TiDB/Worker 设计错误，而是典型：

> 外部副作用 Exactly Once 问题。

---

# 第六部分：P2 / 工程质量问题

# 43. Invariant L 可能误报历史任务

当前：

```text
所有 succeeded occurrence
```

与：

```text
当前 enabled schedule_deliveries
```

直接 JOIN。

假设：

```text
昨天任务成功
当时没有发送给张三

今天新增加：
张三
```

Invariant Checker 可能认为：

```text
昨天那个 occurrence
缺少张三的 DeliveryExecution
```

但这其实不是 bug。

长期建议记录：

```text
Occurrence Delivery Snapshot
```

或者至少按：

```text
delivery.created_at <= occurrence.finished_at
```

做近似判断。

这属于 P2 observability 精度，不影响运行正确性。

---

# 44. Provider MaxInflight 有两个配置源

当前：

```text
Provider catalog
```

已经增加：

```text
MaxInflight
```

但 Runtime wiring 实际使用：

```text
cfg.Aily.MaxInflight
```

构造 limiter。

这会出现：

```text
DB Provider Config = 200

ENV AILY_MAX_INFLIGHT = 100
```

到底谁是 Source of Truth 的问题。

建议最终：

```text
Provider policy
```

成为权威配置。

环境变量仅做：

```text
bootstrap/default/override
```

并明确 precedence。

---

# 45. Session Bind RowsAffected 仍有小型错误处理问题

`BindProviderSessionOwned()` 中有：

```go
if n, _ := res.RowsAffected(); n == 0
```

RowsAffected error 被忽略。

不属于核心风险，但建议改为：

```go
n, err := res.RowsAffected()
if err != nil {
    return err
}
```

---

# 46. terminalEventName default 仍然过于宽松

虽然 `FinalizeOwnedRun()` 已经先：

```text
IsTerminal()
```

校验，所以正常情况下不会进入未知状态。

但：

```go
default:
    return EventRunCompleted
```

仍然不是最理想。

建议：

```go
terminalEventName(status) (string, error)
```

未知状态：

```text
ErrInvalidTerminalStatus
```

而不是默认“成功”。

P2。

---

# 第七部分：最新评分

排除你明确不要求本轮讨论的仓库安全问题之后：

| 模块 | 当前评分 |
|---|---:|
| 总体架构 | **97/100** |
| Application / Runtime Boundary | **96/100** |
| ExecutionOwnership / Fencing | **98/100** |
| Claim + Lease | **97/100** |
| Reaper | **97/100** |
| Retry State Machine | **96/100** |
| Finalize Transaction | **97/100** |
| ScheduleOccurrence | **96/100** |
| Delivery Durability（DB） | **96/100** |
| Delivery External Exactly-once | **82/100** |
| Aily Runtime | **94/100** |
| Provider Rate Limit | **93/100** |
| Provider MaxInflight | **65/100** |
| Priority / Admission | **72/100** |
| Invariant / Observability | **94/100** |
| TiDB Architecture | **96/100** |
| MySQL 5.7 Compatibility Proof | **65/100** |
| GitHub CI | **40/100** |

综合 Production Readiness：

```text
约 86 / 100
```

---

# 第八部分：当前 Release 判断

## 内部开发测试

```text
GO
```

## Staging / UAT

```text
GO
```

## 小规模真实用户

修完：

```text
CI
+
Attempt Accounting
+
Inflight Fencing
```

以后：

```text
GO
```

## 正式大规模 Schedule / Production

当前：

```text
NO-GO
```

主要不是原来的 Execution Core 问题。

现在阻挡生产的是：

```text
Provider admission correctness
+
CI gate
```

---

# 第九部分：开发执行报告

建议下一轮开发名称：

# Provider Admission & Release Gate Closure

不要再叫：

```text
Execution Correctness Closure 4
```

因为 Execution Ownership 主体已经稳定。

---

# Phase 0：先修 CI

这是第一步。

原因很简单：

> 后面所有修改都需要可靠 CI 给反馈。

修改：

```text
.github/workflows/backend.yml
```

完成：

```text
合并 duplicate env

TiDB host-side readiness

MySQL57 readiness

actionlint
```

目标：

```text
check        GREEN
integration  GREEN
mysql57      GREEN
```

没有全绿之前不进入 Production Gate。

---

# Phase 1：ProviderSlot Fencing

新增：

```text
internal/execution/provider_slot.go
```

或者重构当前：

```text
inflight.go
```

模型：

```go
type ProviderSlot struct {
    RunID      ids.ID
    LeaseEpoch uint64
    LeaseToken ids.ID
    Member     string
}
```

---

## Acquire

```go
Acquire(
    ctx,
    ExecutionOwnership,
) (ProviderSlot, bool, error)
```

Member：

```text
runID:leaseEpoch:leaseToken
```

Lua：

```text
remove expired

if member exists:
    refresh own slot
    success

if ZCARD >= max:
    reject

ZADD member
success
```

---

## Renew

必须：

```text
XX only
```

不存在：

```text
ErrProviderSlotLost
```

绝不能通过 Renew 新建 slot。

---

## Release

只：

```text
ZREM ProviderSlot.Member
```

---

## Heartbeat 顺序

调整成：

```text
Heartbeat Run Ownership

↓

lost?
YES
    cancel
    release own provider slot
    stop

NO
    ↓
    Renew ProviderSlot
```

不能先 Renew Slot 再判断 Run Ownership。

---

# Phase 2：修正 Attempt 语义

修改：

```sql
CASClaimRun
```

删除：

```sql
attempt = attempt + 1
```

---

新增：

```text
BeginProviderAttemptOwned
```

在：

```text
真正 Provider Submit 前
```

执行。

建议 Aily：

```text
Auth
↓
Provider Rate Limit
↓
BeginProviderAttemptOwned
↓
Stream / Submit
```

Provider Inflight 可以放在 Worker 层。

---

## Attempt 测试

至少：

```text
100 次 inflight rejected
↓
attempt 仍为 0
```

以及：

```text
第一次真正 StartChat
↓
attempt = 1
```

---

# Phase 3：同步 Run RetryAt / Outbox RetryAt

修改：

```text
RetryOwnedRun
```

接受：

```text
retryAt
```

事务：

```text
Run.available_at = retryAt

Outbox.available_at = retryAt
```

---

删除 SQL 中：

```text
INTERVAL 1 SECOND
```

硬编码。

使用：

```text
RUN_REQUEUE_DELAY
```

或者 reason-specific Backoff Policy。

---

# Phase 4：实现真正的 Priority Admission

推荐：

```text
3 Priority Streams
```

第一阶段：

```text
interactive
retry
scheduled
```

Outbox 路由：

```text
provider
+
priority_class
```

---

## Worker Scheduling

使用 Weighted Round Robin：

```text
interactive : 7

retry       : 1

scheduled   : 2
```

配置化。

如果某队列为空：

```text
其他队列借用额度
```

这样同时满足：

```text
Interactive 优先
Scheduled 不饿死
```

---

# Phase 5：Delivery Idempotency

调整 Sender：

```go
type DeliveryRequest struct {
    ExecutionID    ids.ID
    SenderUserID   int64
    Target         Target
    IdempotencyKey string
}
```

所有 delivery：

```text
IdempotencyKey = DeliveryExecution.ID
```

---

如果 Provider 原生支持：

```text
request id / uuid / idempotency key
```

则映射过去。

如果 Provider 不支持：

在架构文档明确：

```text
Delivery semantics = At Least Once
```

并保留：

```text
duplicate delivery metric
```

---

# Phase 6：补自动测试

本轮重点新增：

```text
Inflight stale release
Inflight stale renew
Inflight missing renew
Inflight 3-worker max
Admission does not consume attempt
Retry outbox delayed publishing
Interactive before scheduled backlog
Scheduled fairness
Delivery idempotency
```

---

# Phase 7：压测

重点场景：

```text
1000 scheduled @ 08:30

+

持续 interactive traffic
```

验证：

```text
Interactive P95 queue wait

Scheduled max queue wait

Provider inflight <= configured max

No Run lost

No schedule starvation

No attempt exhausted without provider call
```

---

# 第十部分：建议的提交顺序

```text
fix(ci): restore backend integration and mysql57 gates

fix(execution): fence provider inflight slots by execution ownership

fix(execution): decouple provider attempts from run claims

fix(execution): align retry run and outbox availability

feat(execution): add weighted provider admission queues

fix(delivery): propagate stable delivery idempotency key

test(execution): add provider admission concurrency suite

test(delivery): add crash and duplicate-send scenarios
```

---

# 第十一部分：本轮明确不需要做什么

目前不要继续动：

```text
ExecutionOwnership
ClaimedRun
WorkerOwnedService
RunLease
Atomic Reaper
Atomic Finalize
Artifact fencing
Session fencing
Schedule row lock
DeliveryExecution model
```

这些现在都是正确资产。

也暂时不要：

```text
接 Codex
接 GraphFlow
做 Workflow
扩其他 Provider
```

先把 Provider Admission 做完整。

---

# 第十二部分：最终验收条件

只有以下全部满足，我才建议把 Production Readiness 提升到：

```text
93~95 / 100
```

要求：

```text
ProviderSlot attempt-scoped
stale attempt 不能 release/renew 新 slot

Admission 不消耗 Provider Attempt

Interactive priority 真正作用于正常队列

Scheduled 有公平执行保证

Run retryAt = Outbox retryAt

Provider max_inflight 压测不突破

Backend CI GREEN

TiDB Integration GREEN

MySQL57 Integration GREEN

Delivery duplicate semantics 已明确/处理
```

---

# 最终判断

这一轮修复以后可以确认：

> **Creation Agent Studio 的核心 Execution 架构已经基本定型。**

之前的问题主要是：

```text
Execution correctness
```

现在剩下的问题已经转移到了：

```text
Admission correctness
+
fair scheduling
+
external side-effect semantics
+
release engineering
```

这是一个很明显的成熟度提升。

接下来不应该继续修改 Execution 核心模型，而应该完成：

```text
Provider Slot Fencing
↓
Attempt Semantics
↓
Retry Timing
↓
Weighted Admission
↓
CI
```

做完以后，这套架构已经足够进入真正的生产压测和正式 UAT 阶段。