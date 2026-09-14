# potal 最新代码终审报告

**评测分支：** `dev`  
**核心修复提交：** `00a4d3b1671d01e34b2afd19c4095610cd7b32fd`  
**评测基准：**《Creation Agent Studio Execution Correctness Closure 修复执行开发计划》  
**评测方式：** 修复报告逐项核验 + 最新 GitHub 代码反向审计 + GitHub Actions 状态检查

---

# 一、最终结论

本轮修复质量明显高于上一轮。

修复报告中宣称的主要结构：

- `ExecutionOwnership`
- `ClaimedRun`
- `WorkerOwnedService`
- Reaper 原子恢复
- `run.retrying`
- Finalize 单事务
- Artifact fencing
- Provider Session fencing
- Scheduler `FOR UPDATE`
- Admin staff-only
- Invariant Checker
- CI

绝大多数都已经真实进入代码。报告对本轮目标的描述是“关闭 Ownership / Lease / Retry / Terminal State 一致性问题”，并声称计划内 P0/P1/P2 已关闭。fileciteturn120file0L5-L8

我认可：

> **Execution Plane 已经从“架构正确但实现有明显竞态”，提升到“主体生产架构正确，仅剩少数边缘一致性问题需要封口”。**

但目前仍然是：

# 暂不建议直接宣布 Production Ready

主要剩余问题：

```text
Security P0
仓库公开后的历史敏感信息暴露风险

Execution P0
terminal 状态下 ownership verifier 仍可被部分写路径绕过

Execution/Schedule P0
scheduled Run 由 Reaper 最终失败时没有同步终结 Occurrence

Execution P0
Retry / Finalize 内 Lease 删除错误被直接忽略

CI P0
GitHub 后端 Integration Gate 当前实际没有跑起来
```

这些问题都不需要重新设计架构，只需要继续做一次小规模 correctness closure。

---

# 二、本轮确实修好的东西

## 2.1 Ownership 从 Run 拆出来 —— 正确

现在已经真实存在：

```go
type ExecutionOwnership struct {
    RunID      ids.ID
    WorkerID   string
    LeaseEpoch uint64
    LeaseToken ids.ID
}

type ClaimedRun struct {
    Run       *Run
    Ownership ExecutionOwnership
}
```

并通过 `WorkerOwnedService` 限制 Provider Executor 可访问的写面。

这比上一版：

```text
Run
├── LeaseEpoch
└── LeaseToken
```

可靠很多。

特别是：

```text
RefreshRun()
```

只替换 Run 业务数据，不会重新生成 Ownership。

这真正解决了上一轮 Aily Background `GetRun()` 导致 LeaseToken 丢失的问题。fileciteturn120file0L83-L114

这一项我给：

**✅ 完全通过。**

---

# 三、Claim + Lease 已经正确原子化

当前 `ClaimRun()`：

```text
BEGIN

CAS queued → running
lease_epoch++
读取 lease_epoch
创建 run_leases
写入 lease_epoch
COMMIT
```

Lease 创建失败会 rollback，原来的：

```text
Run = running
Lease = missing
```

Claim 窗口已经不存在。

这部分现在已经达到我希望的结构。

**✅ P0 原问题关闭。**

---

# 四、Reaper 原子恢复主体也修对了

现在 `RecoverExpiredLeases()`：

```text
BEGIN

expired lease FOR UPDATE
↓
run FOR UPDATE
↓
检查 status
↓
检查 lease_epoch
↓
retry / fail
↓
RunEvent
↓
Outbox
↓
DELETE lease
↓
COMMIT
```

不再：

```text
先删 Lease
↓
再处理 Run
```

因此之前最危险的：

```text
running + no lease
```

Crash Window 已经关闭。

这一项：

**✅ 主体关闭。**

---

# 五、Retry 状态机修复正确

现在 Retry：

```text
running
↓
queued
+
run.retrying
+
outbox
+
lease cleanup
```

而不再：

```text
run.interrupted
```

`run.retrying` 是非终态，所以浏览器不会提前结束 SSE。

报告中对此的定义已经正确收敛：

```text
run.completed
run.failed
run.cancelled
```

才属于真正 Terminal Event。fileciteturn120file0L116-L149

`RetryOwnedRun()` 当前也确实把 Run requeue、retry event、Outbox、Lease cleanup 放在一个 transaction 中。

这一项：

**✅ 通过。**

---

# 六、Finalize 事务化整体实现很好

现在已经真正做到：

```text
Run terminal
+
terminal RunEvent
+
assistant Message
+
ScheduleOccurrence
+
Lease Cleanup
```

尝试放进一个事务。

这解决了上一版两个非常重要的问题：

```text
Run succeeded
但没有 run.completed
```

以及：

```text
Run succeeded
但重新打开对话时 AI 回答消失
```

当前代码也明确把 Redis Pub/Sub、metrics、Delivery hook 放在 commit 后。

这一方向完全正确。

---

# 七、新发现的 Execution P0：`verifyOwnershipTx()` 对 Terminal Run 直接放行

这是本轮最重要的新发现。

当前：

```go
func verifyOwnershipTx(...) {
    ...

    if IsTerminal(row.Status) {
        return row, true, nil
    }

    if row.Status != StatusRunning ||
       row.LeaseEpoch != own.LeaseEpoch {
        return ..., ErrLostOwnership
    }
}
```

也就是说：

> Run 一旦 terminal，函数根本不验证 caller 的 LeaseEpoch。



这个行为对：

```text
FinalizeOwnedRun()
```

是合理的。

因为重复 finalize：

```text
发现已经终态
→ idempotent no-op
```

是我们想要的。

但这个 verifier 被其他普通 canonical write 重用了。

于是出现安全漏洞。

---

# 八、`BindProviderSessionOwned()` 可以在 Run terminal 后修改 Session

当前：

```go
if _, _, err := verifyOwnershipTx(...); err != nil {
    return err
}
```

完全忽略：

```text
isTerminal
```

返回值。

之后继续：

```text
BindAgentThreadSession
```

甚至允许 owner rebind 新 session。

假设：

```text
Worker A
epoch=10
开始 Aily

↓

Lease lost

↓

Worker B
epoch=11
完成 Run
Run = succeeded

↓

Worker A 收到一个迟到 Provider Response

↓

BindProviderSessionOwned()
```

因为 Run 已经 terminal：

```text
verifyOwnershipTx()
→ terminal=true
→ err=nil
```

Worker A 就可能继续修改：

```text
AgentThread.remote_id
```

即：

> **stale worker 仍能修改 canonical provider session。**

这直接违反本轮最重要的承诺：

> “所有 Worker canonical writes 必须带合法 Ownership”。

报告宣称 stale worker 在任何写路径都会 `ErrLostOwnership`。fileciteturn120file0L191-L202

实际这里还没有做到。

---

# 九、正确修法：拆两个 Verifier

不要继续使用：

```text
verifyOwnershipTx()
```

同时承担：

```text
Active Write
+
Finalize Idempotency
```

两套语义。

建议拆：

```go
verifyActiveOwnershipTx()
```

严格要求：

```text
status = running
AND
lease_epoch = ownership.LeaseEpoch
```

只要：

```text
terminal
queued
其他 epoch
```

全部：

```text
ErrLostOwnership
```

然后：

```go
verifyFinalizeOwnershipTx()
```

才允许：

```text
terminal
→ already finalized
→ idempotent no-op
```

使用关系：

```text
AppendEvent
Artifact
Provider Session
External Run ID
Retry
任何正常 Worker 写入

→ verifyActiveOwnershipTx


Finalize

→ verifyFinalizeOwnershipTx
```

这样从 API 层完全消除歧义。

---

# 十、`UpdateExternalRunIDOwned()` 也需要加强

当前 SQL：

```sql
UPDATE runs
SET external_run_id = ?
WHERE id = ?
AND external_run_id = ''
AND lease_epoch = ?
```

但是没有：

```sql
AND status = 'running'
```



执行后才：

```text
CheckOwnership()
```

问题是：

> CheckOwnership 失败不能撤销前面已经 commit 的 UPDATE。

因此理论上同一个 epoch 的 late callback 在 Finalize 之后仍可能写 `external_run_id`，然后函数返回 `ErrLostOwnership`。

建议直接：

```sql
UPDATE runs
SET external_run_id = ?
WHERE id = ?
AND status = 'running'
AND external_run_id = ''
AND lease_epoch = ?
```

最好也放进 Owned transaction。

---

# 十一、新 Schedule P0：Reaper 最终 Fail 没有收敛 ScheduleOccurrence

当前 Reaper 最后一次 Attempt：

```text
running Run
↓
lease expired
↓
MaxAttempts reached
↓
Run → failed
↓
run.failed
↓
DELETE lease
```

但代码没有：

```text
ScheduleOccurrence → failed
```



这对普通 Interactive Run 没关系。

但对：

```text
Scheduled Run
```

问题很明显。

Claim 时 Occurrence 已经：

```text
running
```

Run 最后被 Reaper 判定：

```text
failed
```

Occurrence 却仍然：

```text
running
```

于是 Scheduler 以后判断：

```text
HasActiveOccurrence()
```

可能长期认为：

> 这条 Schedule 仍有一个任务正在运行。

最终造成：

```text
overlap=queue
→ 后续任务一直 pending

overlap=skip
→ 后续任务可能持续被 skip
```

这是定时任务核心功能，所以我把它定为：

# Schedule P0

---

# 十二、修法

在 Reaper exhausted 分支的**同一个事务**加入：

```text
scheduled Run
↓
CAS ScheduleOccurrence
running → failed
```

并且：

```text
任何错误
→ transaction rollback
```

不能 best-effort。

另外建议 Invariant Checker 增加：

```text
terminal scheduled Run
AND
Occurrence still queued/running
```

作为新的：

```text
schedule_run_occurrence_mismatch
```

Invariant。

---

# 十三、Finalize 也有类似问题：Occurrence 更新错误被直接吞掉

现在代码：

```go
_, _ = q.CASFinishOccurrenceByRun(...)
```

也就是：

```text
错误忽略
RowsAffected 忽略
```



所以理论上：

```text
Run succeeded
```

但：

```text
Occurrence 更新失败
```

Finalize transaction 仍然可以 commit。

这与代码注释宣称的：

> Run terminal + Occurrence 收敛同事务

并不完全一致。

正确：

```go
res, err := q.CASFinishOccurrenceByRun(...)
if err != nil {
    return err
}
```

必要时：

```text
RowsAffected == 1
```

也做 invariant 检查。

---

# 十四、新 Execution P0：Retry / Finalize 都在忽略 Lease Delete 错误

`RetryOwnedRun()`：

```go
_, _ = q.DeleteLeaseByToken(...)
```



`FinalizeOwnedRun()` 同样：

```go
_, _ = q.DeleteLeaseByToken(...)
```



这会削弱你这轮最重要的 invariant：

```text
running ⇔ active lease
```

例如：

```text
Run → queued
↓
DELETE lease SQL 出错
↓
错误被吞
↓
COMMIT
```

最终：

```text
queued
+
旧 lease
```

或者：

```text
Run succeeded
+
旧 lease
```

虽然 Invariant Checker 会检测到，但：

> Checker 不是 correctness mechanism。

---

# 十五、Lease Cleanup 必须是强一致操作

改成：

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
```

然后 transaction rollback。

同样原则应用到：

```text
CASFinishRun
RequeueRun
Occurrence terminal update
```

这些关键状态修改。

不要：

```text
_, _ =
```

---

# 十六、`terminalEventName()` 有一个 latent bug

当前：

```go
switch status {
case failed, interrupted:
    return run.failed
default:
    return run.completed
}
```

所以：

```text
StatusCancelled
```

会变成：

```text
run.completed
```



虽然当前 Aily：

```text
cancel=false
```

所以暂时不会频繁触发，但 Execution Core 已经定义：

```text
run.cancelled
```

这个终态。

建议：

```go
switch status {
case StatusSucceeded:
    return EventRunCompleted
case StatusFailed:
    return EventRunFailed
case StatusCancelled:
    return EventRunCancelled
default:
    return error
}
```

不要 `default → completed`。

这一项定为：

**P1。**

---

# 十七、Delivery 还有一个真正的 Durability Gap

当前 Finalize 成功以后：

```text
COMMIT
↓
OnRunSucceeded(...)
↓
Delivery Dispatcher
```

是在内存 Hook 中执行。

App wiring 也确实：

```go
runs.OnRunSucceeded = func(...) {
    disp.OnRunSucceeded(...)
}
```



假设：

```text
Run succeeded
事务 COMMIT

↓

Worker 进程刚好 crash

↓

OnRunSucceeded 还没执行
```

结果：

```text
AI Run ✅

DeliveryExecution ❌ 没创建

用户/群组永远收不到消息
```

因为现有 Delivery due scan 只能恢复：

```text
已经存在的 DeliveryExecution
```

不能发现：

```text
应该有 DeliveryExecution
但从来没有创建
```

---

# 十八、对于你的产品，这项应该从“未来优化”升级为 P1

修复报告把：

> Delivery fan-out 从内存 Hook 迁移到 domain outbox

列为未来增强。fileciteturn120file0L354-L365

但你的定时任务产品定义本身就是：

> “到时间问智能体，回复后选择是否以个人身份发给指定飞书用户/群组。”

所以 Delivery 不是边缘功能。

建议：

```text
Finalize Transaction

Run succeeded
+
terminal event
+
assistant message
+
occurrence succeeded
+
scheduled_run.succeeded Outbox
+
lease delete

COMMIT
```

然后：

```text
Domain Outbox
↓
Delivery Dispatcher
↓
DeliveryExecution
↓
Delivery Outbox
↓
Feishu Worker
```

`OnRunSucceeded` 最多作为 latency optimization，不能承担唯一可靠触发职责。

---

# 十九、定时任务限频架构还有两个此前定下的能力没有真正落地

## 19.1 Priority 字段存在，但 Queue 不使用 Priority

Run 已经有：

```text
priority
```

但是 fallback：

```sql
SELECT id
FROM runs
WHERE status='queued'
AND provider=?
ORDER BY queued_at
```

完全没有 `priority`。

Redis 也只有：

```text
queue:{provider}
```

一个 Stream。

因此假设：

```text
08:30
500 个 Schedule Run 先入队

08:30:01
一个用户手动发消息
```

用户的 Interactive Run 可能排在 500 个定时任务后面。

这偏离我们之前定下的：

```text
Interactive > Scheduled
```

优先级设计。

---

# 二十、建议正式增加 Admission Dispatcher

至少分：

```text
interactive
scheduled
retry
```

三个逻辑优先级。

不一定必须三个 Redis Stream。

可以：

```text
Priority Queue
+
Weighted Fair Scheduling
+
Aging
```

比如：

```text
有 Interactive 时：
优先 Interactive

没有 Interactive：
全部额度给 Schedule

Schedule 等太久：
逐步提高 priority
```

仍然共享：

```text
Aily chat.start Global Rate Limiter
```

绝不能分开算 Provider 额度。

这一项：

**P1，尤其在正式推广定时任务前。**

---

# 二十一、`max_inflight` 仍然没有全局实现

当前实际 wiring 有：

```text
ChatsL
PollsL
ArtifactsL
DeliveryLimiter
```

即：

```text
Rate Limit
```

已经有了。

但是没有看到真正使用 Provider：

```text
max_inflight
```

作为**分布式并发上限**。

这两者不能等价。

例如：

```text
10 starts/s
```

如果平均执行：

```text
120 秒
```

理论在途量可以快速增长到：

```text
1200
```

而如果：

```text
worker-1
worker-2
worker-3
```

每台自己限制 goroutine，并不能保证：

```text
集群 max_inflight
```

---

# 二十二、建议增加 Distributed Provider Semaphore

Redis：

```text
provider:feishu_aily:inflight
```

必须带：

```text
lease / TTL
```

Worker：

```text
RateLimiter Acquire
↓
Inflight Semaphore Acquire
↓
Provider Request
↓
release
```

Crash：

```text
TTL / lease recovery
```

自动归还。

也可以利用当前 RunLease 与 worker lifecycle 辅助 reconciliation。

最终：

```text
start_rate
```

控制：

> 每秒新启动多少

```text
max_inflight
```

控制：

> 整个平台同时跑多少

这也是我们之前架构中特别强调过的区别。

**P1。**

---

# 二十三、Scheduler Multi-instance Admission 本轮修得不错

我核了这一块，确实已经有：

```text
Schedule row FOR UPDATE
↓
active check
↓
FIFO pending
↓
一次 admission 一个
```

不是报告里只写了没实现。

所以之前：

```text
两个 Scheduler
分别看到 active=0
各自 admit 一个 pending
```

的 write skew 已经基本关闭。

对应 T11 测试也存在。修复报告中列出的 Scheduler 并发测试是实际有实现的。fileciteturn120file0L225-L243

**✅ 这一项通过。**

---

# 二十四、Aily Executor 这次已经基本达到目标

这一轮最大的改善之一，就是 Aily Executor 不再拿整个：

```text
execution.Service
```

随便写。

而是：

```text
WorkerOwnedService
```

App wiring 已明确只将 owned write surface 交给 Executor。

Background refresh、polling、retry、artifact、session 等路径也已经大幅收敛。

我认为：

> Aily Runtime 本身已经不再是当前最大的架构风险。

剩余问题主要来自 Execution owned verifier 本身，而不是 Aily provider 设计。

---

# 二十五、测试矩阵确实大幅增加

你报告里列出的 T1–T13 确实与本轮代码方向吻合，包括：

```text
Reaper atomic
stale retry
run.retrying
background ownership
artifact fencing
artifact stable ID
session fence
finalize transaction
dual scheduler
admin staff
Redis degradation
```

fileciteturn120file0L225-L243

这是明显进步。

---

# 二十六、但有一个测试本身存在无效断言

`TestBackgroundRefreshPreservesOwnership` 中有类似：

```go
claimed.Ownership.LeaseToken !=
claimed.Ownership.LeaseToken
```

这种 self-comparison。

它永远不会成立。

正确应该：

```go
before := claimed.Ownership

RefreshRun(...)

after := claimed.Ownership

assert before.LeaseToken == after.LeaseToken
assert before.LeaseEpoch == after.LeaseEpoch
assert before.WorkerID == after.WorkerID
```

不过后续 Finalize + Lease Cleanup 测试会间接验证一些行为，所以这不意味着功能一定坏。

定为：

**P2 测试质量问题。**

---

# 二十七、CI 目前没有真正闭环

修复报告写的是：

```text
integration ✅
真实 TiDB + Redis 全部通过
```

这是你本地执行报告中的结果。fileciteturn120file0L263-L299

但是我查看了 GitHub Actions 当前真实状态：

Frontend workflow：

```text
success
```

Backend workflow：

```text
failure
```

而且失败的不是测试。

Backend：

```text
check
gofmt    ✅
go vet   ✅
go build ✅
unit     ✅
```

Integration：

```text
Initialize containers ❌
checkout              skipped
setup-go              skipped
migration             skipped
integration tests     skipped
```

也就是说：

# GitHub 上一次真实 Integration Test 都没有开始执行。

当前 Actions 记录明确显示 frontend 成功、backend 失败。

---

# 二十八、Backend Workflow 本身还要修

当前：

```yaml
services:
  tidb:
    image: pingcap/tidb:v8.0.0
```

并使用 container health check。

现阶段应该先解决：

```text
Initialize containers
```

失败原因，然后必须看到真实：

```text
migration ✅
integration tests ✅
```

才允许把：

```text
Integration Gate
```

标成关闭。

所以当前：

**CI P0。**

不是代码功能 P0，但是属于：

> 发布门禁 P0。

---

# 二十九、还缺 MySQL 5.7 CI

我们之前最终架构明确：

```text
Primary:
TiDB 8.0

Minimum Compatibility:
MySQL 5.7
```

但是当前 backend workflow 只有：

```text
TiDB 8.0
Redis
```

没有：

```text
MySQL 5.7
```



所以：

> “核心 SQL 兼容 MySQL 5.7”

现在还是架构规范，而不是 CI 可以证明的约束。

建议增加第二个数据库 Job：

```text
db-compat-mysql57
```

至少运行：

```text
全部 migration
核心 repository integration
CAS
Lease
Outbox
Schedule
Artifact
Identity
```

定为：

**P1。**

---

# 三十、一个必须单独提出的 Security P0

这个和 Execution Correctness 没关系，但严重程度比很多代码问题都高。

我检查 GitHub 当前仓库状态：

```text
private = false
visibility = public
```

即：

# 当前仓库是公开仓库。

当前 GitHub Actions 返回的仓库元数据也明确显示该仓库为 public。

但仓库自己提交的历史安全审计记录明确提到：

> Git 历史中曾存在测试凭据、内部基础设施信息、个人信息以及一些敏感测试资产，并且当时的处理建议包含“如果公开，需要重写历史并轮换凭据”。



所以现在情况已经发生变化：

```text
之前：
private repository

现在：
public repository
```

---

# 三十一、这一项建议立即处理

如果你是**有意公开代码**：

也必须马上：

```text
1. 轮换所有曾经进入 Git 历史的凭据/密码/Token

2. 清除历史中的敏感 Blob
   git filter-repo / BFG 等

3. Force push 清理后的历史

4. 删除仓库跟踪的 .workbuddy/memory/

5. 加入 .gitignore

6. 检查 docs / fixtures / screenshots / env history

7. 启用 GitHub Secret Scanning / Push Protection

8. CI 加 gitleaks 等 secret scanning
```

最重要：

> **仅仅把文件从最新 commit 删除是不够的。**

因为：

```text
Git history
```

仍然可以被访问。

如果这次公开是误操作：

> 先立刻转回 Private，再做凭据轮换和历史清理。

即使重新 Private：

> 已经公开过的历史凭据仍然应视为已泄露，必须轮换。

我不会在这里复述仓库中记录的任何具体凭据信息。

---

# 三十二、综合评分更新

修复报告给出的方向总体值得认可。

经过代码实审后，我给目前最新版本：

| 项目 | 评分 |
|---|---:|
| 总体架构 | **96/100** |
| Application / Runtime 边界 | **95/100** |
| ExecutionOwnership 设计 | **96/100** |
| Claim + Lease | **96/100** |
| 实际 Fencing 覆盖 | **88/100** |
| Reaper | **92/100** |
| Retry 状态机 | **95/100** |
| Finalize | **90/100** |
| Aily Runtime | **93/100** |
| Scheduler | **89/100** |
| Delivery Durability | **80/100** |
| Rate Limit | **91/100** |
| Queue Admission / Priority | **77/100** |
| Authentication | **93/100** |
| SSE / Event | **94/100** |
| 测试代码 | **89/100** |
| CI 发布门禁 | **65/100** |
| 当前仓库安全状态 | **约 50/100** |

Execution 子系统单独看：

> **约 90/100。**

全项目考虑仓库安全、CI、Delivery durability 后：

> **Production Readiness 大约 84/100。**

---

# 三十三、现在的 Release 判断

## Execution 架构

**PASS WITH CONDITIONS**

已经不需要再重构整个 Execution Plane。

---

## 定时任务小规模内部测试

**可以。**

尤其单实例/低并发测试已经具备条件。

---

## 大规模正式定时任务

**暂缓。**

至少先做：

```text
Delivery durable outbox
Priority admission
global max_inflight
```

---

## 正式生产上线

目前：

# NO-GO

不是因为架构整体失败。

而是还有几个明确 Release Blocker。

---

# 三十四、建议下一轮只处理以下问题

## P0 — 必须先处理

### Security P0

公开仓库历史敏感数据治理：

```text
rotate secrets
history rewrite
remove memory files
secret scan
```

### Execution P0

拆：

```text
verifyActiveOwnershipTx
verifyFinalizeOwnershipTx
```

并修：

```text
BindProviderSessionOwned
UpdateExternalRunIDOwned
```

### Execution P0

Retry / Finalize：

```text
DeleteLeaseByToken
```

禁止吞错误和 RowsAffected。

### Schedule P0

Reaper terminal fail：

```text
Run failed
+
Occurrence failed
```

必须同事务。

Finalize 里的 Occurrence 更新错误也不得吞。

### CI P0

让 GitHub Backend Integration Job 真正：

```text
Initialize containers ✅
Migration ✅
Integration tests ✅
```

---

# 三十五、P1 — P0 后马上处理

```text
Delivery fan-out → durable outbox

Interactive / Scheduled / Retry priority admission

Distributed max_inflight

MySQL 5.7 CI

StatusCancelled → run.cancelled

补 schedule-run-occurrence invariant
```

---

# 三十六、P2

```text
修 Background ownership 单测的无效断言

Aily attachment streaming

RunEvent sequence counter 优化

Claim 后 GetRun 的 post-commit read robustness

sqlc SELECT FOR UPDATE annotation 清理

run_attempts 表
```

Legacy Workflow 仍然按当前路线放后面，不需要因为本轮审查提前做。

---

# 三十七、我最建议的下一次修改顺序

不要再做一轮很大的重构。

按照这个顺序即可：

```text
1.
strict active ownership verifier

2.
Lease / Occurrence error checking

3.
Reaper scheduled occurrence convergence

4.
Cancelled event mapping

5.
GitHub Integration CI

6.
仓库安全历史清理

7.
Delivery durable outbox

8.
Provider priority + max_inflight

9.
MySQL 5.7 CI
```

其中 1～4 都是小改动。

真正需要一点设计工作的主要是：

```text
Delivery Durable Trigger
+
Provider Admission Control
```

---

# 三十八、最终评价

这次修复已经证明：

> 前一轮制定的 Execution Correctness Closure 方向是正确的。

尤其：

```text
ExecutionOwnership
ClaimedRun
WorkerOwnedService
Atomic Reaper
run.retrying
Atomic Finalize
Scheduler row lock
```

这些都建议长期保留。

我不同意修复报告里唯一的一点是：

> “P0 / P1 / P2 中本轮计划内的全部问题已关闭。”

fileciteturn120file0L5-L8

更准确的状态应该是：

> **Execution Correctness Closure 主体已经完成约 90%，核心架构已经成立；剩余问题主要集中在少数 terminal ownership 旁路、ScheduleOccurrence 收敛、事务错误处理以及生产门禁，而不是整体架构。**

因此下一轮不需要再有：

```text
Execution Correctness Closure 3.0 大重构
```

而应该是一次：

# Production Release Hardening

把剩下这些非常具体的边缘条件封住。

封完之后，我会愿意把 Execution Plane 的评价提升到：

```text
95+/100
```

并认为它具备开始承载正式生产 Run 的条件。