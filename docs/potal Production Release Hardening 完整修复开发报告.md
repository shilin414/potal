# potal Production Release Hardening 完整修复开发报告

**项目：** Creation Agent Studio / potal  
**目标分支：** `dev`  
**评审基准提交：** `00a4d3b1671d01e34b2afd19c4095610cd7b32fd`  
**报告性质：** Execution Correctness Closure 后续生产加固开发报告  
**目标：** 在不推翻现有架构的前提下，关闭剩余 Execution、Schedule、Delivery、CI 和仓库安全风险，使核心运行面达到正式生产上线标准。

---

# 1. 报告结论

当前版本已经完成了上一轮 Execution Correctness Closure 的主体工作，包括：

- `ExecutionOwnership`
- `ClaimedRun`
- `WorkerOwnedService`
- Claim + Lease 原子化
- Atomic Reaper
- `run.retrying`
- Retry 单事务
- Finalize 单事务
- Artifact fencing
- Provider Session fencing
- Scheduler `FOR UPDATE`
- Admin staff-only
- Invariant Checker
- 基础 CI

修复报告中声明的主体改造已经真实进入代码，而不是停留在文档层。fileciteturn120file0L5-L8

尤其当前 `ClaimRun()` 已经把：

```text
queued → running
lease_epoch++
Create Lease
COMMIT
```

放在同一事务中，避免 `running + no lease`。

Retry、Reaper、Finalize 也已经按正确方向收敛。  

因此：

> 本轮禁止再次进行 Execution Plane 大规模重构。

后续开发应定义为：

# Production Release Hardening

只修复剩余 correctness gap，并补齐生产调度、Delivery、CI、兼容性与安全门禁。

---

# 2. 当前生产就绪度

当前建议评估：

```text
Execution Core           ≈ 90 / 100
Aily Runtime             ≈ 93 / 100
Scheduler                ≈ 89 / 100
Delivery Durability      ≈ 80 / 100
Queue Admission          ≈ 77 / 100
CI Gate                  ≈ 65 / 100
Repository Security      需要立即处理
```

综合 Production Readiness：

```text
约 84 / 100
```

当前状态：

```text
内部测试              GO
低并发定时任务测试      GO
大规模正式调度          CONDITIONAL
生产正式发布            NO-GO
```

剩余问题均有明确边界，不要求推翻现有模型。

---

# 3. 本轮修复优先级

## P0：Release Blocker

必须在正式生产发布之前全部关闭：

```text
P0-1  Strict Active Ownership
P0-2  External Run ID Fencing
P0-3  Lease Cleanup Strong Consistency
P0-4  ScheduleOccurrence Terminal Convergence
P0-5  Reaper Schedule Convergence
P0-6  GitHub Integration CI 真正执行
P0-7  Repository Secret / History Security
```

---

## P1：生产稳定性与规模化能力

```text
P1-1  Delivery Durable Outbox
P1-2  Interactive / Scheduled / Retry Admission Priority
P1-3  Distributed Provider max_inflight
P1-4  run.cancelled 正确 Terminal Event
P1-5  MySQL 5.7 Compatibility CI
P1-6  Execution / Schedule Invariant 扩展
```

---

## P2：工程质量与后续优化

```text
P2-1  Background Ownership 单测修正
P2-2  Claim post-commit read robustness
P2-3  sqlc 查询语义清理
P2-4  RunEvent sequence 性能优化
P2-5  run_attempts 历史模型
P2-6  Attachment streaming 优化
```

---

# 4. P0-1：拆分 Active Ownership 与 Finalize Ownership

## 4.1 当前问题

当前：

```go
verifyOwnershipTx(...)
```

在 Run 已经是 terminal 时：

```go
if IsTerminal(row.Status) {
    return row, true, nil
}
```

不会再验证：

```text
status = running
lease_epoch = caller epoch
```



这个行为只适合：

```text
Finalize idempotency
```

不适合：

```text
Artifact
Session
External Run ID
其他 Worker canonical write
```

---

# 5. 当前实际风险

例如：

```text
Worker A
epoch=10

↓ lease expires

Worker B
epoch=11

↓
Run succeeded

↓
Worker A 收到迟到 Aily response

↓
BindProviderSessionOwned()
```

当前 `BindProviderSessionOwned()` 复用了 `verifyOwnershipTx()`。

而 terminal Run 会返回：

```text
terminal=true
err=nil
```

之后代码仍然可能继续更新 `AgentThread.remote_id`。

这意味着：

> stale worker 在 Run terminal 后仍存在修改 canonical provider state 的可能。

这与：

```text
stale worker 必须失去全部 canonical write capability
```

的设计目标不一致。

---

# 6. P0-1 修复设计

必须拆成两个 verifier。

## 6.1 `verifyActiveOwnershipTx`

新增：

```go
func verifyActiveOwnershipTx(
    ctx context.Context,
    tx *sql.Tx,
    own ExecutionOwnership,
) (db.GetRunForUpdateRow, error)
```

语义必须严格：

```text
run exists
AND
status = running
AND
lease_epoch = ownership.LeaseEpoch
```

否则统一：

```text
ErrLostOwnership
```

---

## 6.2 Active verifier 使用范围

以下全部使用：

```text
verifyActiveOwnershipTx
```

包括：

```text
PersistArtifactOwned
BindProviderSessionOwned
UpdateExternalRunIDOwned
Append Worker Event
RetryOwnedRun
其他所有 Provider Executor canonical write
```

---

# 7. `verifyFinalizeOwnershipTx`

Finalize 单独使用：

```go
func verifyFinalizeOwnershipTx(...)
```

允许：

```text
running + correct epoch
→ 当前 worker 可以 finalize

terminal
→ already finalized
→ idempotent no-op

running + wrong epoch
→ ErrLostOwnership
```

禁止把这种 terminal idempotency 语义扩散到普通写路径。

---

# 8. 验收测试

新增：

```text
T14 TerminalRunRejectsArtifactWrite
T15 TerminalRunRejectsSessionBind
T16 TerminalRunRejectsExternalRunIDWrite
T17 StaleOwnerCannotWriteAfterNewOwnerFinalize
T18 DuplicateFinalizeIsStillIdempotent
```

核心验收：

```text
Run terminal 之后：
除 finalize duplicate check 外
任何旧 worker canonical write 必须失败
```

---

# 9. P0-2：External Run ID 必须加入 status fencing

当前：

```sql
UPDATE runs
SET external_run_id = ?
WHERE id = ?
AND external_run_id = ''
AND lease_epoch = ?
```

没有：

```text
status = running
```



后续虽然会调用：

```text
CheckOwnership()
```

但 UPDATE 已经执行。

后面的 Ownership 检查无法回滚之前单独完成的 SQL。

---

# 10. SQL 修复

调整为：

```sql
UPDATE runs
SET external_run_id = ?
WHERE id = ?
  AND status = 'running'
  AND external_run_id = ''
  AND lease_epoch = ?;
```

并建议从：

```text
exec
```

改成：

```text
execresult
```

检查：

```text
RowsAffected
```

---

# 11. 推荐更严格实现

建议最终实现：

```text
BEGIN

SELECT run FOR UPDATE
verifyActiveOwnershipTx

IF external_run_id = ''
    UPDATE
ELSE IF external_run_id = same value
    idempotent success
ELSE
    conflict

COMMIT
```

避免：

```text
provider A id
↓
后续 provider B id
```

被静默覆盖。

---

# 12. P0-3：Lease Cleanup 禁止吞错误

当前 Retry：

```go
_, _ = q.DeleteLeaseByToken(...)
```



Finalize 同样：

```go
_, _ = q.DeleteLeaseByToken(...)
```



这是本轮必须修掉的模式。

---

# 13. 为什么这是 correctness 问题

Retry：

```text
Run running
↓
Run queued
↓
DELETE lease 失败
↓
错误被吞
↓
COMMIT
```

会产生：

```text
queued Run
+
旧 active lease
```

Finalize：

```text
Run succeeded
+
Lease 仍存在
```

同样违反 Execution invariant。

Invariant Checker 只能：

```text
发现
```

不能：

```text
阻止错误 commit
```

---

# 14. 正确实现

统一增加 helper：

```go
func deleteOwnedLeaseTx(
    ctx context.Context,
    q *db.Queries,
    own ExecutionOwnership,
) error
```

逻辑：

```go
res, err := q.DeleteLeaseByToken(...)
if err != nil {
    return err
}

affected, err := res.RowsAffected()
if err != nil {
    return err
}

if affected != 1 {
    return ErrLostOwnership
}

return nil
```

---

# 15. 使用范围

必须覆盖：

```text
RetryOwnedRun
FinalizeOwnedRun
Reaper recovery
任何主动 release ownership 的路径
```

除 orphan cleanup 等明确允许：

```text
0 rows
```

的系统恢复路径外，Worker-owned path 应严格检查。

---

# 16. P0-4：Finalize 必须强一致更新 ScheduleOccurrence

当前 Finalize 已经尝试：

```text
Run terminal
+
Occurrence terminal
```

同事务处理。

但调用：

```go
_, _ = q.CASFinishOccurrenceByRun(...)
```

直接忽略错误。

因此：

```text
Run succeeded
Occurrence running
```

理论上仍能 commit。

---

# 17. 修复要求

改为：

```go
res, err := q.CASFinishOccurrenceByRun(...)
if err != nil {
    return err
}
```

建议进一步检查：

```text
scheduled Run
且存在 trigger_id

→ RowsAffected 必须满足预期
```

如果：

```text
RowsAffected = 0
```

不能简单忽略。

至少应该：

```text
读取 occurrence
判断是否已处于同一个 terminal state
```

允许：

```text
同状态 idempotent
```

拒绝：

```text
状态不一致
```

---

# 18. Schedule terminal mapping

建议统一函数：

```go
func occurrenceTerminalStatus(runStatus string) (string, error)
```

至少：

```text
Run succeeded
→ Occurrence succeeded

Run failed
→ Occurrence failed

Run cancelled
→ Occurrence cancelled
```

如果 Occurrence 暂不支持 cancelled：

```text
明确使用 failed + cancel reason
```

但不能静默映射。

---

# 19. P0-5：Reaper Final Failure 必须收敛 Occurrence

当前 Reaper 在 exhausted 时：

```text
Run running
↓
lease expired
↓
Run failed
↓
run.failed
↓
delete lease
```

并没有同步把 scheduled occurrence 置为 failed。

---

# 20. 可能造成的后果

Occurrence 可能长期保持：

```text
running
```

之后 Scheduler 判断：

```text
HasActiveOccurrence = true
```

于是：

```text
queue overlap policy
→ 后续 occurrence 不再 admission

skip overlap policy
→ 后续执行持续被跳过
```

最终表现为：

> 定时任务永久卡死。

这是 Schedule 核心 P0。

---

# 21. Reaper 正确事务

最终 exhausted 分支改为：

```text
BEGIN

expired lease FOR UPDATE
run FOR UPDATE

确认：
run.running
lease current epoch
lease expired
attempt exhausted

↓

Run → failed

↓

RunEvent run.failed

↓

如果 scheduled:
ScheduleOccurrence → failed

↓

DELETE current lease

COMMIT
```

任何一步失败：

```text
ROLLBACK
```

---

# 22. Retry 分支 Occurrence 行为

Reaper 发生 retry 时：

```text
Run
running → queued
```

Occurrence 可以继续保持：

```text
running
```

因为它代表：

> 这一 occurrence 尚未最终结束，只是在重试同一个 Run。

不应该在 retry 时：

```text
Occurrence → queued
```

除非未来显式设计 occurrence attempt 状态机。

目前最简单正确的方式：

```text
Run retrying
Occurrence running
```

---

# 23. P0-6：GitHub Backend Integration CI 必须真正执行

当前 GitHub Actions：

Frontend：

```text
SUCCESS
```

Backend：

```text
FAILURE
```

Backend 中：

```text
check
gofmt     success
go vet    success
go build  success
unit      success
```

但 integration job：

```text
Initialize containers  failure

checkout               skipped
setup-go               skipped
wait for TiDB           skipped
migration               skipped
integration tests       skipped
```

也就是说：

> GitHub CI 目前没有真正执行一次真实 TiDB + Redis Integration Test。

当前 Actions 状态可直接确认这一点。

---

# 24. 当前 Workflow

当前配置为：

```yaml
services:
  tidb:
    image: pingcap/tidb:v8.0.0

  redis:
    image: redis:7-alpine
```

并使用 service container health check。

必须先定位：

```text
Initialize containers
```

失败原因。

---

# 25. CI 修复目标

最终 Backend CI 至少必须完整出现：

```text
checkout                 ✅
setup-go                 ✅
TiDB ready               ✅
Redis ready              ✅
Create DB                ✅
Migrations               ✅
Integration Tests        ✅
```

不接受：

```text
本地 Integration Tests 已通过
```

代替 CI。

---

# 26. 建议 CI 结构

```text
backend-check

backend-integration-tidb

backend-integration-mysql57

frontend-check

security-scan
```

其中：

```text
backend-check
```

包含：

```text
gofmt
go vet
go build
go test unit
```

---

# 27. P0-7：公开仓库历史安全治理

GitHub 当前返回：

```text
repository.private = false
```

即仓库处于公开状态。

而仓库历史安全审计材料曾记录：

```text
Git 历史存在测试凭据
内部基础设施信息
个人信息
敏感测试资产
```

并建议公开前进行历史清理及凭据轮换。

因此必须把历史数据视为：

```text
potentially exposed
```

---

# 28. Security 修复动作

## 第一阶段：凭据轮换

所有曾进入 Git 历史的：

```text
password
token
secret
API key
internal credential
test account
```

全部轮换。

即使目前已经删除：

> 仍按泄露处理。

---

## 第二阶段：历史清理

使用：

```text
git filter-repo
```

或等价工具清理历史。

重点：

```text
.env
memory
secret fixtures
screenshots
credentials
内部地址说明
```

---

## 第三阶段：停止跟踪工作记忆

将：

```text
.workbuddy/memory/
```

等本地 Agent memory / scratch 文件移出 Git。

加入：

```gitignore
.workbuddy/
.env
.env.*
*.local
```

必要时使用白名单：

```text
.env.example
```

---

## 第四阶段：Secret Scan

GitHub：

```text
Secret Scanning
Push Protection
```

CI：

```text
gitleaks
```

或等价 secret scanner。

---

# 29. P1-1：Delivery 必须从内存 Hook 升级为 Durable Outbox

当前：

```text
Finalize commit
↓
OnRunSucceeded()
↓
Delivery Dispatcher
```

是在数据库事务 commit 以后执行。

App wiring 也确实将 `OnRunSucceeded` 接到 Delivery。

---

# 30. 当前 Crash Window

```text
Run succeeded
COMMIT

↓ process crash

OnRunSucceeded 没执行
```

结果：

```text
Run 成功
Occurrence 成功
Assistant Message 存在

但是：
DeliveryExecution 从未创建
```

现有 Delivery retry 只能恢复：

```text
已经创建的 DeliveryExecution
```

无法恢复：

```text
应该创建、但从来没创建
```

---

# 31. 正确架构

Finalize Transaction 内增加：

```text
delivery.requested domain outbox
```

完整：

```text
Run succeeded
+
RunEvent completed
+
Assistant Message
+
ScheduleOccurrence succeeded
+
Domain Outbox delivery.requested
+
Lease delete

COMMIT
```

之后：

```text
Domain Outbox Relay
↓
Delivery Dispatcher
↓
DeliveryExecution
↓
Feishu Sender
```

---

# 32. Delivery 幂等键

建议：

```text
schedule_occurrence_id
+
delivery_target_id
```

或：

```text
run_id
+
target_type
+
target_id
```

建立 unique constraint。

这样即使：

```text
Domain Outbox
at-least-once delivery
```

也不会重复创建 DeliveryExecution。

---

# 33. P1-2：Provider Admission Priority

当前 Run 表已经存在：

```text
priority
```

但 fallback queue query：

```sql
ORDER BY queued_at
```

并没有使用 priority。

Redis 当前也是：

```text
queue:{provider}
```

单 Stream。

---

# 34. 当前规模化问题

例如：

```text
08:30:00
1000 Scheduled Run 入队

08:30:01
真实用户发送一条 Interactive 请求
```

当前 Interactive 请求可能长时间排在 Scheduled 后。

这会直接伤害：

```text
用户实时体验
```

---

# 35. 推荐优先级

定义：

```text
Interactive = 100
Retry       = 70
Scheduled   = 50
Background  = 20
```

但不要做绝对 starvation。

采用：

```text
Weighted Priority + Aging
```

---

# 36. Admission 算法

推荐：

```text
Interactive:
高优先

Retry:
避免已经开始的业务长期无法恢复

Scheduled:
正常运行

Aging:
等待时间越久有效 priority 越高
```

示例：

```text
effective_priority
=
base_priority
+
wait_time_bonus
```

---

# 37. Redis 实现方式

不要求一定拆多个 Stream。

方案 A：

```text
queue:feishu_aily:interactive
queue:feishu_aily:retry
queue:feishu_aily:scheduled
```

Worker weighted read。

方案 B：

Redis Sorted Set 作为 Admission Queue。

方案 C：

TiDB truth + Redis wakeup + Scheduler Dispatcher。

从现有架构连续性考虑，建议：

> 保留 Redis Streams 做 delivery signal，引入一个 Admission Dispatcher 决定可执行顺序。

---

# 38. P1-3：Provider `max_inflight`

当前已经有：

```text
Chats Rate Limit
Poll Rate Limit
Artifact Rate Limit
Delivery Rate Limit
```

App wiring 已确认。

但：

```text
QPS
```

不是：

```text
Concurrency
```

---

# 39. 为什么必须单独限制

如果：

```text
start rate = 10/s
average run duration = 120 s
```

理论稳定在途可能达到：

```text
≈ 1200 runs
```

如果上游 Provider 或本系统能承受的实际并发只有：

```text
100
```

仅限制 10 QPS 远远不够。

---

# 40. Distributed Semaphore

增加：

```text
xiaoan3:provider:feishu_aily:inflight
```

建议实现为：

```text
Redis ZSET
```

成员：

```text
run_id
```

score：

```text
lease expiry timestamp
```

Acquire：

```text
Lua atomic:
remove expired
count
if count < max_inflight
    add run
    success
else
    reject
```

Release：

```text
ZREM run_id
```

---

# 41. 为什么必须带 TTL/Lease

否则：

```text
Worker crash
```

会永久占用 inflight slot。

所以：

```text
Provider Slot Lease
```

必须有过期时间。

可以与：

```text
RunLease heartbeat
```

同步续约。

---

# 42. 执行顺序

Worker：

```text
Claim Run

↓

Acquire Provider Inflight Slot

↓

Acquire Rate Limit

↓

Start Provider Request

↓

Execution

↓

Finalize / Retry

↓

Release Provider Slot
```

异常退出依靠：

```text
Provider Slot TTL
```

恢复。

---

# 43. P1-4：修复 `run.cancelled`

当前：

```go
switch status {
case failed, interrupted:
    return run.failed
default:
    return run.completed
}
```

意味着：

```text
cancelled
```

也会映射：

```text
run.completed
```



虽然当前 Aily Cancel capability 可能没有启用，但 Execution Core 不应保留错误状态映射。

---

# 44. 修改

```go
func terminalEventName(status string) (string, error) {
    switch status {
    case StatusSucceeded:
        return EventRunCompleted, nil

    case StatusFailed:
        return EventRunFailed, nil

    case StatusCancelled:
        return EventRunCancelled, nil

    default:
        return "", ErrInvalidTerminalStatus
    }
}
```

禁止：

```text
unknown terminal state
→ completed
```

---

# 45. P1-5：MySQL 5.7 Compatibility CI

目标架构此前已经确定：

```text
Primary:
TiDB 8.0

Minimum SQL Compatibility:
MySQL 5.7
```

当前 CI 只有：

```text
TiDB 8
Redis
```



所以：

```text
MySQL 5.7 compatible
```

目前没有自动证明。

---

# 46. 新 CI Job

新增：

```text
backend-integration-mysql57
```

Service：

```yaml
mysql:
  image: mysql:5.7
```

执行：

```text
Migrations
Repository Integration
Execution Integration
Schedule Integration
Identity Integration
Artifact Integration
```

---

# 47. MySQL 5.7 CI 强制覆盖

重点防止未来开发误用：

```text
CTE
Window Function
SKIP LOCKED
RETURNING
JSON_TABLE
MySQL 8-only syntax
TiDB-only hints
```

---

# 48. P1-6：Invariant Checker 扩展

现有 A-G Invariant Checker 建议继续保留。

新增：

## H：Scheduled Run / Occurrence mismatch

```text
terminal scheduled Run
AND
Occurrence in queued/running
```

---

## I：Non-running Run has Lease

检测：

```text
runs.status != running
AND
run_lease exists
```

---

## J：Running Run without Lease

```text
status = running
AND
no active current-epoch lease
```

---

## K：Lease Epoch mismatch

```text
run.lease_epoch != lease.lease_epoch
```

---

## L：Delivery missing

对于：

```text
successful scheduled Run
AND
configured delivery target
```

却不存在：

```text
DeliveryExecution / delivery domain outbox
```

需要报警。

---

# 49. Invariant Checker 原则

Invariant Checker：

```text
只报警
```

默认不要自动修改业务数据。

对于自动修复，应由：

```text
Recovery Coordinator
```

执行明确恢复流程。

---

# 50. P2-1：修复 Background Ownership Test

当前某 ownership preservation 测试存在 self-comparison 风险。

正确写法：

```go
before := claimed.Ownership

err := executor.RefreshRun(...)

require.NoError(t, err)

after := claimed.Ownership

require.Equal(t, before.LeaseToken, after.LeaseToken)
require.Equal(t, before.LeaseEpoch, after.LeaseEpoch)
require.Equal(t, before.WorkerID, after.WorkerID)
```

---

# 51. 再增加真正 stale test

流程：

```text
Worker A Claim epoch=1

↓

模拟 lease expiration

↓

Reaper retry

↓

Worker B Claim epoch=2

↓

Worker A 再写 Artifact / Session / Event

↓

必须 ErrLostOwnership
```

这比单纯结构断言更有价值。

---

# 52. P2-2：Claim post-commit read

当前 Claim：

```text
COMMIT

↓

s.GetRun()
```



如果 commit 成功，但后续读取遭遇瞬时 DB error：

```text
Caller 看见 Claim error
```

但实际上：

```text
Run 已 running
Lease 已创建
```

系统最终可以靠 Reaper 恢复，但 Worker 会丢掉本次 Ownership。

不是 correctness P0，但建议优化。

---

# 53. 推荐改法

在 Claim transaction 内，把后续 Executor 所需的 Run 数据一起：

```text
SELECT / RETURN from locked row
```

组装 `ClaimedRun`。

如果必须 commit 后读取，则：

```text
commit successful
+
read failed
```

需要返回特殊错误：

```text
ErrClaimCommittedButLoadFailed
```

并主动：

```text
release / retry claim
```

而不是当作普通失败。

---

# 54. Outbox 当前设计评价

当前：

```text
TiDB Outbox
→ Redis Streams
→ Worker
```

方向正确。

Outbox Relay 明确采用 at-least-once，Worker 用 CAS 保证重复 dispatch 不导致重复 claim。

这一部分不需要重新设计。

---

# 55. 但 Outbox 还应增加状态观测

建议 metrics：

```text
outbox_pending_total
outbox_oldest_pending_seconds
outbox_publish_fail_total
outbox_publish_latency
```

报警：

```text
oldest_pending > 30s
```

或按实际 SLA 设置。

---

# 56. RunEvent Durable / Live 分层继续保持

当前架构方向：

```text
TiDB = durable truth
Redis = live acceleration
```

正确。

继续坚持：

```text
content.delta
→ Redis live

content.snapshot
→ durable

terminal event
→ durable first
```

不要把 token-level 所有事件写入 TiDB。

---

# 57. Final Reconciliation 继续保持

Aily：

```text
Streaming End
```

不等于：

```text
最终业务状态绝对确定
```

仍然保持：

```text
Final GET Reconciliation
```

作为 Provider canonical state。

特别是：

```text
timeout
stream disconnect
browser disconnect
```

不能直接把 Run 标 failed。

---

# 58. Admin Auth 本轮保持

当前 local admin 已调整：

```text
staff / superuser only
```

这一方向正确。

不要恢复：

```text
普通 local account login
```

给员工使用。

普通员工仍：

```text
Feishu OAuth
```

---

# 59. Provider Identity 原则继续保持

正常 Aily 调用：

```text
必须使用当前用户真实 UAT
```

禁止：

```text
UAT 失败
↓
偷偷 fallback TAT
```

否则会破坏：

```text
用户权限边界
Aily 可见性
审计主体
```

这一原则本轮不修改。

---

# 60. 数据库事务规范

完成本轮后，Execution 的核心事务必须统一满足：

## Claim

```text
Run queued → running
+
epoch increment
+
Lease create
```

---

## Retry

```text
Run running → queued
+
run.retrying
+
new outbox
+
lease delete
```

---

## Reaper Retry

```text
expired lease lock
+
Run running → queued
+
run.retrying
+
outbox
+
lease delete
```

---

## Reaper Exhausted

```text
Run running → failed
+
run.failed
+
Occurrence failed
+
lease delete
```

---

## Finalize

```text
Run terminal
+
terminal event
+
assistant message
+
Occurrence terminal
+
delivery domain outbox
+
lease delete
```

---

# 61. 核心原则

对于这些 transaction：

> 不允许出现“业务正确性 SQL best-effort”。

因此禁止：

```go
_, _ = ...
```

用于：

```text
Lease
Occurrence
Run terminal
Outbox
Message
Terminal Event
```

---

# 62. 允许 best-effort 的内容

只能是：

```text
Redis Pub/Sub
Prometheus metrics
logging
telemetry
cache
non-critical notification
```

即：

> 丢失只影响性能或可观察性，不影响业务真相。

---

# 63. 建议 SQL 审查

对 `backend-go/db/queries/execution.sql` 全面搜索：

```text
CAS
Requeue
Lease
Occurrence
Finish
Fail
UpdateExternal
```

把所有：

```text
exec
```

重新评估是否应该使用：

```text
execresult
```

用于检查：

```text
RowsAffected
```

---

# 64. 推荐结果分类

SQL update 后统一分：

```text
RowsAffected = 1
→ success

RowsAffected = 0
→ idempotent / lost ownership / conflict

SQL error
→ hard failure
```

不要把：

```text
0 rows
```

和：

```text
SQL error
```

混成同一种结果。

---

# 65. Scheduler Admission 继续使用数据库强一致

当前 Scheduler 已采用：

```text
Schedule row FOR UPDATE
```

来串行 admission。

这一部分建议保持。

不要为了提升一点 Scheduler TPS 改成：

```text
Redis distributed lock only
```

TiDB 仍然是：

```text
schedule correctness truth
```

Redis 可以：

```text
wake up
cache
rate limit
```

不能代替 schedule state transaction。

---

# 66. Schedule 大规模优化路线

先确保正确，再优化吞吐。

如果以后大量 Schedule：

```text
100k+
```

可以做：

```text
sharded scheduler
schedule partition
next_fire_at index
batch discovery
per-schedule row lock
```

但当前不要引入。

---

# 67. Provider Concurrency 配置建议

Provider 配置明确区分：

```text
start_rate_limit
max_inflight
poll_rate_limit
artifact_rate_limit
```

例如：

```yaml
provider:
  feishu_aily:
    start_rate_limit: 10
    max_inflight: 200
    poll_rate_limit: 10
    artifact_rate_limit: 50
```

具体值必须通过压测与官方限制调整。

---

# 68. Worker 本地并发

除了 Redis Distributed Semaphore，还应有：

```text
local semaphore
```

避免单个进程：

```text
goroutine 爆炸
```

例如：

```text
cluster max_inflight = 200

worker local max = 64
```

两层同时限制。

---

# 69. Worker HTTP Transport

继续复用共享：

```text
http.Client
http.Transport
```

不能 per Run 创建。

建议生产参数压测：

```text
MaxIdleConns
MaxIdleConnsPerHost
MaxConnsPerHost
IdleConnTimeout
TLSHandshakeTimeout
ResponseHeaderTimeout
```

---

# 70. 数据库连接池

分别为：

```text
API
Stream
Worker
```

设置独立 pool。

特别是 Worker：

```text
max inflight
```

必须与：

```text
DB max open connections
```

协同。

不要：

```text
2000 worker goroutines
50 DB connections
```

造成排队抖动。

---

# 71. Stream Gateway

SSE 继续独立 process role：

```text
studio-stream
```

不要重新合回 API。

原因：

```text
长连接生命周期
慢客户端
backpressure
连接池特征
deploy scaling
```

与普通 API 完全不同。

---

# 72. 慢客户端

必须保证：

```text
浏览器读取慢
```

不能反向阻塞：

```text
Worker
Provider stream
TiDB
```

Stream Gateway 使用：

```text
bounded buffer
```

超过阈值：

```text
disconnect slow client
```

客户端依靠：

```text
Last-Event-ID / sequence replay
```

恢复。

---

# 73. 新增测试矩阵

在现有 T1-T13 基础上新增。

## T14

```text
Terminal Run rejects Artifact write
```

---

## T15

```text
Terminal Run rejects Session bind
```

---

## T16

```text
Terminal Run rejects ExternalRunID write
```

---

## T17

```text
Stale Worker after new-owner finalize cannot canonical-write
```

---

## T18

```text
Finalize duplicate remains idempotent
```

---

## T19

```text
Retry lease-delete failure rolls back transaction
```

---

## T20

```text
Finalize lease-delete failure rolls back transaction
```

---

## T21

```text
Scheduled finalization occurrence failure rolls back Run finalize
```

---

## T22

```text
Reaper exhausted scheduled Run
→ occurrence failed atomically
```

---

## T23

```text
Reaper retry keeps occurrence active
```

---

## T24

```text
Cancelled Run emits run.cancelled
```

---

## T25

```text
Delivery domain outbox survives process crash after finalize
```

---

## T26

```text
Duplicate delivery.requested does not duplicate DeliveryExecution
```

---

## T27

```text
Interactive Run admitted ahead of large Scheduled backlog
```

---

## T28

```text
Scheduled Run eventually admitted under continuous Interactive traffic
```

验证 Aging / fairness。

---

## T29

```text
Distributed max_inflight respected across 3 workers
```

---

## T30

```text
Worker crash releases inflight slot after TTL
```

---

# 74. Chaos Test

正式发布前至少跑：

```text
kill worker during streaming

kill worker immediately before finalize

kill worker immediately after finalize commit

Redis restart

TiDB transient disconnect

Aily 429

Aily timeout

Aily 5-minute long run

duplicate Redis Stream message

slow SSE client

network disconnect after provider accepted request
```

---

# 75. 必须证明的系统性质

完成后必须能够证明：

## Property A

```text
One Run has at most one valid current Worker ownership.
```

---

## Property B

```text
Stale Worker cannot modify canonical state.
```

---

## Property C

```text
running Run always corresponds to current execution lease.
```

---

## Property D

```text
terminal Run always owns exactly one terminal lifecycle result.
```

---

## Property E

```text
successful conversational Run cannot lose assistant answer.
```

---

## Property F

```text
scheduled Run terminal state converges with ScheduleOccurrence.
```

---

## Property G

```text
successful scheduled Run requiring delivery eventually creates durable delivery work.
```

---

## Property H

```text
Redis message duplication cannot cause duplicate execution ownership.
```

---

# 76. CI 最终门禁

Merge 到：

```text
dev
```

至少要求：

```text
Frontend            PASS
Go Format           PASS
Go Vet              PASS
Go Build            PASS
Unit Tests          PASS
TiDB Integration    PASS
MySQL57 Integration PASS
Security Scan       PASS
```

---

# 77. Main 分支门禁

建议：

```text
dev
↓
all checks
↓
staging deploy
↓
smoke test
↓
manual approval
↓
main
```

禁止：

```text
直接 push main
```

---

# 78. Staging Smoke Test

必须真实执行：

```text
Feishu OAuth login

Create interactive Run

Aily streamed response

Multi-turn session reuse

Attachment upload

Artifact open

Scheduled Run

Scheduled retry

Delivery to user

Delivery to group

Worker restart recovery

SSE reconnect
```

---

# 79. 监控指标

新增或确认：

## Execution

```text
run_total{status}
run_duration_seconds
run_retry_total
run_lost_ownership_total
run_finalize_conflict_total
```

---

## Lease

```text
run_lease_active
run_lease_expired_total
run_lease_mismatch_total
```

---

## Queue

```text
queue_depth{priority}
queue_wait_seconds{priority}
```

---

## Provider

```text
provider_start_rate
provider_inflight
provider_rate_limit_wait_seconds
provider_429_total
provider_timeout_total
```

---

## Schedule

```text
schedule_due_total
schedule_admitted_total
schedule_skipped_total
schedule_pending_total
schedule_occurrence_stuck_total
```

---

## Delivery

```text
delivery_requested_total
delivery_execution_pending
delivery_success_total
delivery_failed_total
delivery_retry_total
```

---

## Outbox

```text
outbox_pending
outbox_oldest_age
outbox_publish_failure
```

---

# 80. 关键告警

至少：

```text
running_without_lease > 0

nonrunning_with_lease > 0

terminal_without_terminal_event > 0

terminal_schedule_occurrence_mismatch > 0

outbox_oldest_age > threshold

provider_inflight near max

delivery backlog > threshold

reaper recovery spike

lost ownership spike
```

---

# 81. 日志规范

所有 Worker execution logs 建议带：

```text
run_id
application_id
conversation_id
provider
worker_id
lease_epoch
attempt
external_run_id
```

禁止：

```text
UAT
refresh token
cookie token
Feishu secret
plaintext credential
```

---

# 82. 数据迁移

本轮修复原则：

> 尽量不做大 schema migration。

必须增加的 schema 仅限真正需要：

```text
delivery domain outbox event
provider inflight metadata（如果不用纯 Redis）
相关 unique/index
```

Ownership verifier、Lease、Occurrence 大部分属于：

```text
code + SQL query fix
```

无需数据库大改。

---

# 83. Index Review

确保：

```text
run_leases(run_id)
run_leases(expires_at)

runs(status, provider, queued_at)
runs(status, available_at, priority, created_at)

schedule_occurrences(schedule_id, status)
schedule_occurrences(run_id)

outbox_events(status, available_at, created_at)

delivery_executions(status, available_at)
```

具体以实际 schema EXPLAIN 为准。

---

# 84. Queue 查询修改

Fallback scan 至少改为：

```sql
ORDER BY priority DESC, queued_at ASC
```

但这只是 fallback。

真正生产优先级仍由：

```text
Admission Dispatcher
```

负责。

---

# 85. 性能原则

继续坚持：

```text
Correctness
>
Reliability
>
Performance
>
Maintainability
>
Development convenience
```

但 correctness 闭环后，性能必须依靠：

```text
pprof
EXPLAIN
benchmark
load test
metrics
```

而不是预想。

---

# 86. 第一轮压测

建议：

```text
100 concurrent Interactive Runs

500 Scheduled Runs burst

1000 SSE connections

mixed:
20 interactive QPS
+
large scheduled backlog
```

观察：

```text
Interactive P95 wait
Scheduled max wait
DB pool
Redis latency
Provider inflight
memory
goroutines
```

---

# 87. 第二轮故障压测

运行过程中：

```text
kill 50% workers
```

要求：

```text
Run 不丢失
Lease 自动过期
Reaper 恢复
Retry 不重复 terminal
Scheduled Occurrence 最终收敛
Delivery 最终执行
```

---

# 88. 本轮明确不做

继续保持当前 Application First 范围。

本轮不开发：

```text
Aily Workflow
Codex Runtime
GraphFlow Runtime
Multi-Agent Team
Cross-App Orchestration
Evaluation Platform
```

这些与当前生产加固无关。

---

# 89. 开发阶段划分

## Phase 1：Correctness Closure Final

完成：

```text
Strict Active Ownership
ExternalRunID fence
Lease delete strong check
Occurrence strong check
Reaper occurrence convergence
Cancelled event
```

完成后 Execution 目标：

```text
95+/100
```

---

# 90. Phase 2：CI + Security

完成：

```text
GitHub Integration CI
MySQL 5.7 CI
Secret rotation
Git history cleanup
Secret scanning
```

---

# 91. Phase 3：Delivery Durability

完成：

```text
delivery.requested domain outbox
idempotent delivery execution
crash recovery tests
```

---

# 92. Phase 4：Provider Admission

完成：

```text
Priority
Aging
Distributed max_inflight
local semaphore
metrics
```

---

# 93. Phase 5：Production Validation

完成：

```text
Chaos Tests
Load Tests
Staging UAT
Observability verification
Release Gate
```

---

# 94. 推荐提交拆分

不要一次做一个超大 commit。

建议：

```text
fix(execution): enforce strict active ownership fencing

fix(execution): make lease cleanup transaction-fatal

fix(schedule): atomically converge occurrence terminal state

fix(execution): emit correct cancelled terminal event

fix(ci): make TiDB Redis integration gate executable

ci: add mysql57 compatibility gate

fix(delivery): persist durable delivery request outbox

feat(execution): add provider admission priority

feat(execution): add distributed inflight semaphore

chore(security): remove tracked local memory and secret artifacts
```

---

# 95. Code Review Checklist

每个 PR 必须回答：

```text
是否修改 canonical business state？

如果是：
是否在 TiDB transaction？

是否 Worker-owned？

如果是：
是否验证 ExecutionOwnership？

是否需要 Lease？

是否检查 RowsAffected？

Crash 在任意两条 SQL 之间会不会破坏 invariant？

Redis 挂掉是否会丢业务？

重复消息是否幂等？

stale worker 是否还能写？
```

---

# 96. 禁止新增的实现模式

本轮开始明确禁止：

```go
_, _ = criticalSQL(...)
```

禁止：

```text
Worker 直接拿完整 execution.Service
```

禁止：

```text
Redis = business truth
```

禁止：

```text
terminal event before durable terminal state exists outside transaction
```

禁止：

```text
post-commit in-memory hook 作为唯一业务触发
```

禁止：

```text
fallback TAT masquerade user
```

---

# 97. Done Definition：P0

P0 完成必须满足：

```text
所有 canonical worker write 使用 strict active ownership

Terminal stale worker 无法改 artifact/session/external id

Retry lease delete error 会 rollback

Finalize lease delete error 会 rollback

Finalize occurrence error 会 rollback

Reaper exhausted scheduled Run 会同步 fail occurrence

GitHub TiDB Integration CI 真实全绿

历史敏感信息完成轮换与治理
```

---

# 98. Done Definition：P1

P1 完成必须满足：

```text
Delivery trigger durable

Interactive 有明显优先级

Scheduled 无 starvation

Provider global max_inflight

Cancelled 语义正确

MySQL 5.7 CI 全绿

新增 execution/schedule invariants
```

---

# 99. Release Gate

最终只有以下全部成立才能：

```text
Production GO
```

要求：

```text
P0 = 0

Critical Security Findings = 0

Backend Integration CI = GREEN

Frontend CI = GREEN

TiDB Tests = GREEN

MySQL57 Tests = GREEN

Real Aily UAT = GREEN

Chaos Recovery = GREEN

Schedule End-to-End = GREEN

Delivery End-to-End = GREEN

Invariant Checker = 0 violation
```

---

# 100. 最终建议

当前代码已经不需要重新设计 Execution Plane。

上一轮最重要的设计：

```text
ExecutionOwnership
WorkerOwnedService
RunLease
lease_epoch
Transactional Outbox
Atomic Retry
Atomic Reaper
Atomic Finalize
```

全部应该保留。

本轮开发的重点应当从：

```text
重构
```

切换成：

```text
证明系统不会在边界条件下破坏 invariant
```

---

# 101. 最终目标状态

完成本报告后，Execution Plane 应满足：

```text
Stale Worker：
无权写

Current Worker：
所有关键写带 fence

Crash：
事务回滚或 Recovery 恢复

Redis Failure：
不丢 business truth

Duplicate Delivery：
幂等

Provider 429：
限流等待

Worker Crash：
Lease / inflight 自动恢复

Schedule：
不会永久 running

Delivery：
不会因 commit 后 crash 丢失

Interactive：
不会被大批 Schedule 饿死
```

最终系统路径：

```text
Application
↓
Conversation
↓
Run
↓
Transactional Outbox
↓
Admission
↓
Claim + ExecutionOwnership
↓
Provider Rate Limit + Inflight
↓
Aily
↓
Reconciliation
↓
Atomic Finalize
↓
Durable Delivery Outbox
↓
Feishu Delivery
```

并继续保持：

```text
TiDB = correctness truth

Redis = dispatch / live / cache / limiter

Worker = execution

Stream = realtime

API = request/control plane
```

这是当前项目下一阶段最合理的生产架构终点。

---

# 102. 开发执行优先顺序

最终建议严格按照：

```text
1. Strict Active Ownership

2. ExternalRunID status fencing

3. Lease cleanup strong error handling

4. ScheduleOccurrence transaction correctness

5. Reaper final occurrence convergence

6. run.cancelled

7. Backend Integration CI

8. Repository security cleanup

9. Delivery durable outbox

10. Provider Priority Admission

11. Distributed max_inflight

12. MySQL 5.7 CI

13. Chaos + Load Testing

14. Production Release
```

不要把：

```text
Priority
性能
UI
新 Runtime
```

提前到前六项之前。

这一次应当把：

> Execution Correctness Closure

真正结束，然后把项目正式推进到：

# Production Ready Application Runtime Platform