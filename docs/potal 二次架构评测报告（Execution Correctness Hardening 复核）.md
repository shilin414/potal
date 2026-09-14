# potal 二次架构评测报告

评测对象：`shilin414/potal`  
评测分支：`dev`  
重点功能提交：`21ebbf39ea4303b17b408cffd8ddd5629b5cafe0`  
评测依据：修复报告 + 修改后的实际代码 + 新增集成测试。

该提交的实际变更范围与修复报告基本一致，包括 lease_epoch、ClaimAndLease、Scheduler、GCRA、delta 聚合、登录路由和故障注入测试。

---

# 一、总体结论

这轮修改比上一版**明显前进了一大步**。

第一次评测中最直接的三个执行面问题，不再停留在文档层：

P0-1 Outbox UUID Bug 已真正修复；P0-2 Claim + Lease 已真正变为单事务；P0-3 也已经建立了 `lease_epoch + lease_token + heartbeat cancel` 的正确基础模型。

同时，Scheduler 的 `fire_once / skip / catch_up`、Run Now queue admission、Redis 限频降级、content.delta 聚合、RuntimeSnapshot typed validation，以及 Feishu 登录入口都进行了实际改造。修复报告中这些项目的状态与大部分代码能够互相印证。

但我的最终判断是：

> **Execution Correctness Hardening 已完成第一阶段，但 fencing 还没有真正做到“所有 Worker canonical write 都必须持有不可伪造的 Ownership”。**

因此当前版本已经不再是之前大约 **70/100 的生产就绪度**，我会提高到：

> **约 80/100。**

但我暂时不会给到 90+，也不建议把“P0 全部关闭”作为最终结论。

---

# 二、原问题复核结果

| 原评测问题 | 本次复核 |
|---|---|
| P0-1 Outbox BINARY(16) → raw string | ✅ **完全关闭** |
| P0-2 CAS Claim 后 Lease 创建失败 | ✅ **Claim 路径关闭**；⚠️ Reaper 存在类似孤儿窗口 |
| P0-3 stale worker fencing | ⚠️ **核心机制已建立，但尚未全链路关闭** |
| fire_once 退化 catch_up | ✅ **关闭** |
| catch_up 无上限 | ✅ **关闭** |
| Run Now overlap=queue 并行 | ✅ 单 Scheduler 正常；⚠️ 多 Scheduler admission 仍有竞态 |
| 普通用户通用登录页 | ✅ 前端已收敛；⚠️ 后端仍保留本地登录旁路 |
| content.delta 写放大 | ✅ **基本关闭** |
| RuntimeSnapshot panic | ✅ **关闭** |
| Redis limiter fail-open | ✅ **实现关闭**，建议补 Redis 故障测试 |
| Aily 附件整文件进内存 | ⏳ 未处理，与报告一致 |
| Legacy workflow RuntimeAdapter 化 | ⏳ 未处理，与报告一致 |

---

# 三、确认已经修好的部分

## 3.1 P0-1 Outbox UUID：可以正式关闭

原来：

```go
"run_id": string(row.AggregateID)
```

已经变成：

```go
"run_id": mustID(row.AggregateID).String()
```

即：

```text
BINARY(16)
→ ids.ID
→ canonical UUID
→ Redis
→ ids.Parse()
```

这是正确的 ID Boundary。

这一项我不再保留风险标记。

---

# 四、Claim + Lease 原子化：主路径修复正确

新的：

```go
ClaimAndLease(...)
```

确实在一个 TiDB transaction 中完成：

```text
CAS queued → running
↓
lease_epoch++
↓
读取 claim epoch
↓
INSERT run_leases
↓
COMMIT
```

Lease INSERT 失败会回滚整个事务，所以原先：

```text
running + no lease
```

由 Claim 过程产生的永久孤儿状态已经消除。

而 Worker 的 Redis Stream 路径和 fallback scan 路径现在都统一进入 `ClaimAndLease → claimAndExecute`，没有继续走旧的 CASClaim + AcquireLease 两阶段流程。

这一部分质量是明显提升的。

---

# 五、lease_epoch + lease_token 的方向也是正确的

迁移 0009 已加入：

```sql
lease_epoch BIGINT UNSIGNED NOT NULL DEFAULT 0
```

每次 claim 自增。

SQL 也已经存在：

```text
CASFinishRunFenced
UpdateRunExternalIDFenced
RequeueRunFenced
FailRunFenced
HeartbeatLeaseFenced
DeleteLeaseFenced
```

其中 heartbeat 已从 `worker_id` 提升到 `lease_token`，这是关键改进。

Worker heartbeat 一旦续租失败，还会调用当前 execution context 的 `cancel()`，使本地 polling / SSE 尽快停止。

因此：

> fencing 的**基础设施设计已经正确**。

问题主要发生在：

> **部分业务代码仍绕开了 fenced API。**

---

# 六、新 P0：Reaper 又存在一次 `running + no lease` 非原子窗口

这是本轮最值得优先修的问题之一。

当前 `RecoverExpiredLeases()` 是：

```text
1. DeleteLeaseIfExpired
2. GetRun
3. ReleaseInterrupted
```

三个独立 DB 操作。

假设：

```text
删除 expired lease 成功
↓
进程 crash / TiDB 短暂异常
↓
ReleaseInterrupted 尚未执行
```

此时又会出现：

```text
Run.status = running
run_leases = 无
```

之后：

```text
ListExpiredLeaseRunIDs
```

也找不到它，因为 Lease 已经不存在。

于是它和上一轮 P0-2 的最终故障形态其实一样：

> **running orphan。**

区别只是以前发生在 Claim，现在发生在 Reaper。

### 推荐修改

Reaper 应该变成单事务：

```text
BEGIN
↓
确认 lease expired + token/epoch
↓
CAS running → queued/failed
↓
创建 redis dispatch outbox（需要 retry 时）
↓
删除对应 lease
↓
COMMIT
```

最好不要：

```text
先删 lease
再修 Run
```

而要：

> Run ownership 状态转换和 Lease 清理成为同一个事务单元。

因此 P0-2 我现在重新定义为：

> **Claim 路径已关闭，整个“running 必须有 active lease”的系统 invariant 尚未完全关闭。**

---

# 七、新 P0：`ReleaseInterruptedFenced` 实际并没有完整 fencing

修复报告写的是 Worker 的 AppendEvent / Finish / Requeue / Fail / DeleteLease 等都已经进入 fenced 链路。

但实际代码中：

```go
func releaseInterrupted(...) {
    _ = s.AppendEvent(...)
```

这里首先写的是：

```text
未 fenced 的 AppendEvent
```

然后即使：

```text
RequeueRunFenced
```

影响行数为 `0`，代码也没有检查 `RowsAffected()`，随后仍可能创建新的：

```text
run.dispatch Outbox
```

最后才尝试 fenced delete lease。

所以 Worker A 已经失去 ownership 后，仍然可能：

```text
写入 run.interrupted
创建多余 dispatch
```

虽然它已经不能再直接把 Worker B 的 fenced terminal state 改掉。

这说明 fencing 当前属于：

> **数据库主状态部分 fencing**

而不是：

> **所有 canonical side effect fencing。**

---

# 八、这里还有一个更严重的状态机问题：`run.interrupted` 同时代表“重试”和“终态”

当前：

```go
ReleaseInterrupted(...)
```

无论后面是：

```text
Run → queued
准备重试
```

还是最终失败，都先发：

```text
run.interrupted
```



但是 `run.interrupted` 在另外两个地方被明确当成“终态”：

后端 SSE：

```text
run.completed
run.failed
run.interrupted
```

任何一个出现都会关闭 SSE。 

前端同样把：

```text
run.interrupted
```

放在 TERMINAL 集合中，收到以后会：

```text
status = failed
activeRunId = null
finalizeRun()
```

 

因此现在可能出现：

```text
Aily 429
↓
Run 准备重试
↓
ReleaseInterrupted
↓
发布 run.interrupted
↓
Browser 认为任务结束
↓
SSE 关闭
↓
Run 实际已经重新 queued
↓
Worker 后面继续执行第二次 attempt
```

这不是单纯的 UI bug。

它破坏了：

> **Run 生命周期语义。**

### 推荐重新定义事件

重排队时应该使用：

```text
run.retrying
```

或者：

```text
run.attempt_interrupted
```

它是非终态事件。

只有一个 Run 真正进入不可再次执行的 terminal 状态时，才能发布：

```text
run.completed
run.failed
run.cancelled
run.interrupted
```

如果 `interrupted` 仍然允许 retry，那么它就不应该是 terminal event。

必须二选一。

---

# 九、新 P0：Aily `retryOrFail()` 仍直接调用未 fenced 的 ReleaseInterrupted

这是比上一个问题更直接的 fencing 漏口。

当前 Aily Executor：

```go
func retryOrFail(...) {
    if run.Attempt < run.MaxAttempts {
        return e.Svc.ReleaseInterrupted(ctx, run, code)
    }
}
```

而不是：

```go
ReleaseInterruptedFenced
```



如果 Worker A：

```text
请求 Provider
↓
Lease 过期
↓
Worker B 接管
↓
A 恢复并得到一个 retryable / 429
```

A 会进入未 fenced：

```text
AppendEvent
RequeueRun
CreateOutbox
DeleteLease
```

最危险的是普通 `DeleteLease(run_id)`。

这意味着 Worker A 仍然可能：

> **删除 Worker B 的新 Lease。**

所以目前 P0-3 不能标为完全关闭。

这一处应该马上改成：

```go
return e.Svc.ReleaseInterruptedFenced(ctx, run, code)
```

更进一步，我建议 Worker Runtime 层以后**根本不要暴露 unfenced ReleaseInterrupted 给 Provider Executor**。

---

# 十、Aily Background 模式还会丢失 Lease Token

Streaming reconciliation 已经正确用了：

```go
preserveOwnership(run, refreshed)
```

避免 stale worker 从数据库读取新的 epoch。

但是 Background 模式：

```go
refreshed, err := e.Svc.GetRun(...)
if err == nil {
    run = refreshed
}
```

直接覆盖了 Worker claim 时保存的 `LeaseToken`。

数据库里的 Run 只有：

```text
lease_epoch
```

没有：

```text
lease_token
```

因此刷新后：

```text
LeaseEpoch = 当前 DB 值
LeaseToken = zero
```

之后 `Finish()` 虽然能通过 epoch CAS，但：

```text
DeleteLeaseFenced(... zero token ...)
```

无法删除真实 Lease。

结果可能是：

```text
Run = succeeded
Lease 仍存在
```

直到 Reaper 再去清。

而且 Background polling 中还有：

```go
Svc.AppendEvent(...)
```

不是：

```go
AppendEventFenced(...)
```

所以 stale background worker 仍然能写 `run.poll` 事件。

### 修复非常简单

Background refresh 也必须：

```go
run = preserveOwnership(run, refreshed)
```

同时 polling：

```go
AppendEventFenced(...)
```

这一项应该加一个专门测试：

```text
Background Run
→ Submit
→ DB refresh
→ Finish
→ lease 必须消失
```

以及 stale background worker test。

---

# 十一、Artifact fencing 也还没有闭环

`recordArtifact()` 当前顺序是：

```text
UpsertRunArtifact
↓
查询实际 row
↓
AppendEvent（未 fenced）
```

没有在 Upsert 前调用：

```text
CheckOwnership
```

而且 Append 仍是普通 `AppendEvent`。

所以 stale Worker 理论上仍可以污染：

```text
run_artifacts
```

这和修复报告所说的“artifact upsert 前 CheckOwnership”没有完全对应上。

还有一个独立 bug：

```go
artifactID := ids.New()
UpsertRunArtifact(...)
row := ListRunArtifactsByExternalID(...)
_ = row
AppendEvent(... artifactID.String())
```

如果这是同一个 artifact 的第二次发现，例如：

```text
Streaming 阶段发现一次
Final Reconciliation 又发现一次
```

`ON DUPLICATE KEY UPDATE` 会保留旧 row。

但事件里发送的是：

```text
新生成但并没有实际插入的 artifactID
```

而不是查询得到的真实 `row.ID`。

前端可能因此拿到一个不存在的 local artifact id。

这个我定为：

> **P1，建议一起修。**

---

# 十二、AgentThread Session 也应该加入 ownership 保护

`bindThreadAndRun()` 中：

```text
external_run_id
```

走了 fenced API，这是对的。

但：

```go
BindAgentThreadSession(...)
```

仍是普通：

```sql
UPDATE agent_threads
SET remote_id=?, status='active'
WHERE id=?
```

并且错误被忽略。 

如果 stale Provider 请求晚返回：

```text
session_A
```

而新 Worker 已经建立：

```text
session_B
```

旧 Worker 仍可能覆盖 conversation 的 Aily session。

多轮对话里这个风险比普通事件脏写更麻烦。

建议：

```text
CheckOwnership
↓
Bind session CAS
```

并且尽可能：

```text
remote_id empty → set once
```

而不是任意覆盖。

---

# 十三、建议把 Ownership 从 Run 模型中彻底拆出来

现在代码已经暴露了一个结构性问题：

```text
Run 从 DB 可以重新加载
Ownership 不能从 DB 重新加载
```

但是你把：

```text
LeaseEpoch
LeaseToken
```

挂在 `Run` struct 上。

这就是为什么 Background `GetRun()` 一覆盖，token 就丢了。

我更建议下一步改成：

```text
Run
+
immutable ExecutionOwnership
```

概念上类似：

```go
type ExecutionOwnership struct {
    RunID ids.ID
    Epoch uint64
    Token ids.ID
}
```

Claim 返回：

```text
Run + Ownership
```

所有 Worker 写接口：

```text
AppendOwnedEvent(run, ownership)
FinishOwned(run, ownership)
RequeueOwned(run, ownership)
UpdateExternalRunIDOwned(...)
UpsertArtifactOwned(...)
BindThreadOwned(...)
```

这样：

```text
GetRun()
```

永远不可能偷偷改变 ownership。

这是比继续依赖：

```go
preserveOwnership()
```

更牢靠的长期实现。

---

# 十四、Scheduler 三种 Misfire 本轮修得不错

这一部分我确认是实质修复。

现在：

```text
skip
```

会直接 fast-forward 到未来。

```text
fire_once
```

只创建一个补偿 Run，再 `advancePast()`。

```text
catch_up
```

只有 missed slot 数量没有超过 `MaxCatchUpSlots=10` 时才允许逐槽执行，超限直接 fast-forward。

相关 TiDB 集成测试也确实存在：

```text
fire_once 10 天停机只执行一次
skip 快进
catch_up 超限不补
catch_up 小积压有界补
```



这一项可以认为基本关闭。

---

# 十五、Run Now overlap queue 单实例语义已经正确

现在：

```text
有 active occurrence
+
overlap=queue
↓
创建 pending occurrence
↓
不立即创建 Run
```

之后：

```text
admitPending
```

才将其转成 Run。

测试也验证了：

```text
active 时 pending
active 完成后才产生 Run
```



这一部分相比上一版是正确的。

---

# 十六、但多 Scheduler 下 admission 仍有一个并发竞态

`admitOne()` 当前虽然：

```text
transaction 前检查 active
transaction 内再检查 active
```

但事务内只是普通：

```text
COUNT queued/running
```

没有锁定这个 Schedule，也没有唯一的 active-admission CAS。

如果同一个 Schedule 有两个 pending occurrence：

```text
Scheduler A 处理 pending #1
Scheduler B 处理 pending #2
```

两边可能同时：

```text
COUNT active = 0
```

然后分别创建两个 Run，最后两个 occurrence 都变 queued。

事务本身并不能自动阻止这种 write skew。

### 推荐

在 admission transaction 内先：

```sql
SELECT id
FROM schedules
WHERE id = ?
FOR UPDATE;
```

然后：

```text
re-check active
→ admit one
```

普通 `FOR UPDATE` 没问题。

我们之前禁止的是：

```text
SKIP LOCKED
```

不是禁止所有行锁。

应补：

```text
TestDualSchedulerPendingAdmissionNoParallel
```

用两个 Scheduler 并发处理两个 pending occurrence。

---

# 十七、Redis GCRA 降级实现是正确方向

Redis 调用异常时，现在会：

```text
Redis GCRA
↓ error
Local process GCRA
```

而不是：

```text
unlimited
```

并且暴露：

```go
Degraded()
```

这点我认可。

不过现有 `ratelimit_test.go` 主要验证真实 Redis GCRA 的 burst 行为，没有看到“故意让 Redis 不可用 → local GCRA 仍限制流量”的自动测试。

因此：

> 实现可通过，测试建议再补一条。

---

# 十八、delta 聚合也真正落地了

现在 Provider delta：

```text
content.delta
↓
PublishTransient
↓
Redis Pub/Sub
↓
Browser
```

不再每个 delta INSERT TiDB。

Aily Executor 每：

```text
2000 bytes
或
500ms
```

聚合成：

```text
content.chunk
```

并携带累计 snapshot。

前端遇到 `content.chunk.snapshot`：

```text
replace
```

而不是继续 append，因此 replay 能自愈，不容易重复内容。

SSE Gateway 对：

```text
sequence=0
```

的 transient frame 也不会被 persisted replay 的 dedup 逻辑错误丢弃。

这一部分设计不错。

---

# 十九、Finish 和 terminal RunEvent 目前仍非原子

当前 `Finish()`：

```text
CAS Run → succeeded/failed
↓
Delete Lease
↓
Append terminal RunEvent
```

是不同事务。

如果：

```text
Run 已变 succeeded
↓
进程 crash
↓
run.completed 尚未 Append
```

则数据库：

```text
Run = terminal
RunEvent = 没有 terminal event
```

SSE Gateway 对一个已经 terminal 的 Run：

```text
replay persisted events
↓
直接 return
```

并不会根据 Run status 自动补一条 terminal event。

而前端自动重连依赖看到 terminal event 才停止。

这会造成：

```text
Run 实际已完成
前端却可能不断连接一个立即关闭的 SSE
```

### 推荐

最好做到：

```text
Run terminal CAS
+
terminal RunEvent
```

同一 TiDB transaction。

Redis publish：

```text
after commit
```

如果短期不想重构，至少 SSE Gateway 检测：

```text
Run terminal
+
replay 中无 terminal event
```

时根据 Run 当前状态合成 terminal frame。

长期还是建议事务一致。

---

# 二十、Assistant Message 也有类似 durability gap

Aily 的 `finish()`：

```text
Svc.Finish()
↓
Create assistant Message
```

也是两个独立持久化动作。

如果：

```text
Run succeeded
↓
程序崩溃
↓
assistant Message 尚未插入
```

那么：

```text
Run.output 有答案
Conversation history 没答案
```

当前实时页面可能看过答案，但重新打开 Conversation 就消失。

建议最终把：

```text
Run Finish
Assistant Message
terminal RunEvent
```

至少放在一个“finalize transaction”中。

对于 Artifact 可以通过 idempotent upsert 补偿。

---

# 二十一、登录前端已经符合目标，但后端还没完全收口

前端现在：

```text
ProtectedRoute
↓
/login
↓
FeishuAutoLoginPage
↓
自动 /api/identity/oauth/start
```

已经符合我们之前确定的普通用户体验。 

管理员：

```text
/login/admin
```

独立本地密码页，也已经落地。

但是后端还有两个问题。

`VerifyLocalAdmin()` 实际只验证：

```text
username
password_hash
```

没有看到：

```text
is_staff == true
```

或：

```text
is_superuser == true
```

的要求。

所以如果数据库中有一个：

```text
非管理员 + PasswordSet=true
```

的用户，也能走 `/api/identity/admin/login`。

另外 OpenAPI 仍然保留：

```text
POST /api/auth/login/
```

作为本地用户名密码入口。

后端 handler 也确实仍调用 `VerifyLocalAdmin()` 建立 Session。

所以严格来说：

> **前端 Feishu SSO Only 已完成；后端 Auth Policy 还没有做到 Feishu SSO Only。**

### 建议

`VerifyLocalAdmin` 至少必须：

```text
Password valid
AND
IsStaff == true
```

如果有 `is_superuser`：

```text
IsStaff || IsSuperuser
```

更明确。

然后把旧：

```text
/api/auth/login/
```

删除，或者至少也限制为管理员且标记 deprecated。

---

# 二十二、UAT 这一条目前仍然正确

Aily `identity_mode=user` 时会调用：

```text
UserAccessToken(userID)
```

从当前用户 Feishu Identity 获取 refresh token 并刷新 UAT。

只有显式：

```text
identity_mode=tenant
```

才会走 TAT。

因此之前你特别强调的：

> 正常用户调用 Aily 必须用用户身份，不能静默退回应用身份。

这一点目前没有被本轮改造破坏。

---

# 二十三、测试质量有明显提升，但还不能把报告中的“全部验证”当成 CI 证明

修复报告声明：

```text
go build ./...
go vet ./...
go test ./...
真实 TiDB/Redis 22 个集成用例 × 3
tsc
vitest 112/112
```

全部通过。

而代码里也确实新增了针对：

```text
Outbox UUID
Claim+Lease
Stale worker fencing
heartbeat token
misfire
RunNow queue
```

的真实 TiDB/Redis 测试。

所以我认可：

> 测试体系有实质性进步。

但从 GitHub 当前仓库来看，我没有看到这个 commit 对应的 GitHub Actions status，而且 `.github/workflows` 当前也不存在。

因此我能独立确认的是：

> **测试代码存在并覆盖了报告中的主要场景。**

报告所说：

> **22 个用例连续三轮实际执行全绿**

属于修复报告中的验证结果，而不是我通过 GitHub CI 再独立验证的结果。

建议下一步直接增加 CI。

---

# 二十四、新测试最应该补什么

下一轮测试重点已经不应该继续堆正常成功流程，而应该专门证明以下 invariant：

```text
任何时刻：
running Run
⇔
只有一个合法 Ownership
⇔
存在对应 active lease
```

尤其要覆盖 Reaper 删除 Lease 后 crash、stale worker 进入 retryOrFail、background executor DB refresh、Artifact stale write、两个 Scheduler 同时 admission、Run terminal 但 terminal event 未落库、非 staff 本地账号访问 admin login 这些场景。

这批测试价值会比再增加几十个普通 API 测试更高。

---

# 二十五、重新评分

上一轮：

```text
生产就绪度 ≈ 70/100
```

这一轮我更新为：

```text
总体架构方向             94/100
Application/Runtime      93/100
Outbox                    96/100
Claim + Lease             94/100
Lease Fencing             68/100
Scheduler                 86/100
Aily Runtime              84/100
Rate Limiting             91/100
SSE / Event               82/100
前端 Workspace/Shell      93/100
前端登录体验              94/100
后端认证策略              72/100
测试体系                  85/100
生产就绪度               ≈80/100
```

Fencing 得分没有同步涨到 90，并不是因为设计错，而是：

> **正确的 fencing 基础已经建立，但有数个调用点仍能绕过去。**

---

# 二十六、下一迭代不建议再扩功能

现在最合适的下一阶段不是：

```text
再接一个 Provider
再做更多 Workflow
再加更多 Schedule 功能
```

而是再做一个很小但非常重要的：

> **Execution Correctness Closure**

目标不是增加能力，而是让下面这个规则成为不可绕过的代码结构：

```text
Worker 获得 Ownership
          │
          ▼
所有 canonical writes
          │
          ▼
必须带 Ownership
          │
       ┌──┴──┐
       │     │
     valid  lost
       │     │
      写入   ErrLostOwnership
             │
             ▼
           stop
```

尤其建议将：

```text
Run
```

与：

```text
ExecutionOwnership
```

彻底分开。

这样能从类型层面避免：

```text
GetRun()
↓
意外丢失 token
```

这类现在已经实际发生的 bug。

---

# 二十七、我对本轮修复的最终判断

这次不是“修复报告写得很好，但代码没有跟上”。

恰恰相反：

> **绝大多数修复确实进了代码。**

尤其：

```text
Outbox UUID
ClaimAndLease transaction
lease_epoch
token heartbeat
heartbeat cancel
misfire
RunNow admission
local GCRA fallback
delta transient/chunk
typed snapshot
Feishu login frontend
```

都是真改。

但报告中这一句：

> **P0-3 已修复（lease_epoch + 全链路 fence + lease-lost 取消）**

我建议修改为：

> **P0-3 核心 fencing 机制已落地，但 Provider retry、Reaper、Background refresh、Artifact/Thread 等旁路仍需完成 fencing closure。**

目前最大的三个剩余问题是：

```text
Reaper 非原子 recovery
+
ReleaseInterrupted / retryOrFail 绕过 fencing
+
run.interrupted 的 retry/terminal 语义冲突
```

这三个修完之后，我会认为：

> **Execution Plane 已经跨过真正的 production correctness 基础线。**

再把：

```text
Background ownership
Artifact / AgentThread fence
Terminal state + terminal event transaction
Admin auth staff-only
Scheduler multi-instance admission lock
```

一并收掉，整个项目生产就绪度才比较有资格进入：

> **90/100+。**

---

# 二十八、最终结论

**这轮修复值得保留，不需要回退，也不需要再重新设计 Execution 架构。**

正确路线仍然是：

```text
TiDB Source of Truth
+
Transactional Outbox
+
Redis Stream wake-up
+
CAS Claim
+
Lease Ownership
+
Fencing
+
Final Reconciliation
```

现在的问题已经进一步收敛为：

> **不是“架构有没有设计对”，而是“Ownership 是否真的成为所有 Worker 写入不可绕开的强制条件”。**

只要把这个最后闭环完成，我对这套 Go Runtime 执行内核的评价会从目前的：

> **架构成熟、执行正确性尚有高风险边界**

提升为：

> **可以开始承担正式生产 Run 的执行内核。**

修复报告里列出的两个明确遗留——Aily 附件流式上传和 Legacy Workflow RuntimeAdapter 收敛——仍然可以按原计划放到后续，不需要抢在上述 correctness closure 前面。