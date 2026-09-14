# potal 最新代码复审报告暨剩余问题开发执行计划

**审查分支：** `dev`  
**最新仓库 HEAD：** `757af9b`（文档提交）  
**最新实际代码 HEAD：** `d098e4c`  
**CI 验证 Run：** `34804570324`  
**审查范围：** Provider Admission / Execution / Schedule / Delivery / Rate Limit / Retry / CI / TiDB / MySQL 5.7  
**明确不讨论：** Git 历史、仓库安全、历史密钥、密钥轮换。

---

# 一、总体结论

这轮修复质量是明显合格的。

上传的修复报告声称已经关闭 Provider Inflight fencing、Attempt 语义、优先级 Admission、Retry 时序、Delivery 幂等语义以及 CI Release Gate。fileciteturn217file0L46-L69

经过代码复核，其中绝大多数结论成立：

| 项目 | 本轮复核 |
|---|---|
| ProviderSlot 改为 ownership-scoped | ✅ 真正完成 |
| stale worker 不能 release 新 owner slot | ✅ 完成 |
| stale worker 不能 renew 新 owner slot | ✅ 完成 |
| Renew 不会凭空重建 slot | ✅ 完成 |
| Claim 不再 `attempt++` | ✅ 完成 |
| Provider 真正调用前才 `attempt++` | ✅ 完成 |
| Retry Run / Outbox 同一个 retryAt | ✅ 完成 |
| Reaper 恢复即时可 claim | ✅ 完成 |
| 三类 Redis priority stream | ✅ 完成 |
| Delivery 外部副作用显式 At-Least-Once | ✅ 完成 |
| TiDB fresh migration | ✅ CI 实证 |
| MySQL 5.7 migration + integration | ✅ CI 实证 |
| Backend CI 三个 Job | ✅ 全绿 |
| Weighted Scheduler 永不饿死 | ❌ **仍存在一个实际 bug** |
| `max_inflight` 在 Redis 丢状态后仍严格成立 | ❌ **尚不能成立** |

所以目前已经不是“核心执行架构需要重写”的状态。

现在剩余风险集中在：

```text
Admission / Capacity Accounting
       ↓
Priority Fairness
       ↓
Distributed Clock / Redis Failure Semantics
       ↓
Production Load Validation
```

---

# 二、上一轮 P0：ProviderSlot Fencing 已真正关闭

现在 `ProviderSlot` 已经是：

```go
type ProviderSlot struct {
    RunID      ids.ID
    LeaseEpoch uint64
    LeaseToken ids.ID
    Member     string
}
```

实际 Member：

```text
{run_id}:{lease_epoch}:{lease_token}
```

Acquire、Renew、Release 都按完整 Member 操作。

这意味着：

```text
Worker A
run=R epoch=1 token=A

Worker B
run=R epoch=2 token=B
```

对应：

```text
R:1:A
R:2:B
```

A 的：

```text
ZREM R:1:A
```

已经不可能删除：

```text
R:2:B
```

这一点我确认已经完全解决。

---

# 三、Renew XX-only 也正确落地

现在 `Renew()`：

```text
ZSCORE member
存在 → ZADD 更新 expiry
不存在 → ErrProviderSlotLost
```

不会因为旧 Worker 苏醒而：

```text
重新创建 stale slot
```

代码和报告描述一致。

这一项可以正式关闭。

---

# 四、Heartbeat 顺序已经修正

现在 heartbeat 是：

```text
Heartbeat Run Lease
        ↓
   Ownership lost?
    /           \
  YES            NO
   ↓              ↓
cancel        Renew ProviderSlot
release slot
```

也就是先确认 Run Ownership，再操作 Provider Slot。

上一版：

```text
Renew ProviderSlot
↓
再发现 Run Lease 已丢
```

的问题已经不存在。

---

# 五、Attempt 语义已经真正修正

报告定义：

```text
attempt = provider execution count
claim ≠ attempt
admission requeue ≠ attempt
```

fileciteturn217file0L109-L121

代码已经与这个定义一致。

`CASClaimRun` 现在只做：

```sql
queued → running
lease_epoch++
```

没有：

```sql
attempt++
```



新的：

```text
BeginProviderAttemptOwned
```

在 Ownership 锁下：

```text
attempt >= max_attempts
    → ErrProviderAttemptsExhausted

否则
    → attempt++
```



而 Aily 调用顺序确实已经改成：

```text
Auth
↓
Rate Limit
↓
BeginProviderAttempt
↓
Stream / Submit
```



因此之前：

> Provider 满载，根本没调用 Aily，却把 3 次 retry budget 消耗光

的问题已经彻底关闭。

---

# 六、Retry 时序修复也成立

现在：

```text
Run.available_at
=
Outbox.available_at
=
retryAt
```

在同一个事务里写入。

Provider failure retry 与：

```text
Admission contention
```

也已经分开。

Provider retry：

```text
RUN_REQUEUE_DELAY
```

Admission：

```text
1s
```

这比之前合理很多。

同时 Reaper 已经改成：

```text
available_at = CURRENT_TIMESTAMP(3)
```

立即恢复，不再错误套 provider retry delay。

---

# 七、CI 修复已经经过真实 Runner 验证

这一点本轮可以完全确认，不再是“本地推测”。

GitHub Actions Run：

```text
34804570324
```

对应：

```text
head = d098e4c
conclusion = success
```



我进一步核对了 Jobs：

```text
check        success
integration  success
mysql57      success
```

所以报告中：

> Backend CI GREEN / TiDB Integration GREEN / MySQL57 GREEN

这几个结论现在是可信的。报告本身也已经更新为该状态。fileciteturn217file0L224-L235

之前 fresh TiDB 上：

```text
SERIALIZABLE 不支持
```

的问题，也已经通过 TiDB 专用 DSN 参数处理，而不是要求提前修改数据库配置。

这一点处理得正确。

---

# 八、Delivery 当前模型可以接受

现在 Sender 接口已经明确：

```text
Delivery semantics = AT LEAST ONCE
```

并携带：

```text
ExecutionID
IdempotencyKey
```



Worker 使用：

```text
IdempotencyKey = DeliveryExecution.ID
```



这意味着没有再错误宣称：

```text
飞书消息 Exactly Once
```

这一点是正确的。

目前无需为了追求理论上的 Exactly Once 重构 Delivery。

---

# 九、本轮新发现 P0：Weighted Fair Scheduler 仍然会饿死 Scheduled

这是这次最明确的代码 bug。

当前权重：

```text
interactive = 7
retry       = 1
scheduled   = 2
```

`classScheduler` 只在：

```text
所有 credit == 0
```

时 refill。

问题在于：

> **空队列的 credit 永远不会被消费。**

---

# 十、实际可复现的饥饿场景

最常见的生产状态完全可能是：

```text
interactive：持续有请求
retry：空
scheduled：持续有任务
```

初始：

```text
credits = [7, 1, 2]
```

先执行：

```text
7 interactive
```

变成：

```text
[0, 1, 2]
```

然后：

```text
retry
```

为空。

执行：

```text
2 scheduled
```

得到：

```text
[0, 1, 0]
```

问题出现了。

`retry` 的 credit：

```text
1
```

因为 retry queue 永远为空，所以一直不被 consume。

于是：

```text
allExhausted() = false
```

永远不 refill。

此时 `order()` 永远类似：

```text
retry
interactive
scheduled
```

retry 空：

```text
↓
interactive 有数据
↓
每次都被 interactive 抢到
```

而 interactive 此时属于 borrow：

```text
credit 已经为 0
```

所以 consume 也不会改变 credit。

最终：

```text
credits 永久 = [0,1,0]
```

Scheduled 会在前两个任务后：

> **永久饥饿。**

---

# 十一、为什么现有测试没有发现

现在：

```text
TestScheduledNeverStarvesUnderContinuousInteractive
```

使用的是：

```text
interactive = 1000
retry       = 50
scheduled   = 50
```



也就是说：

```text
retry 从来不是空的
```

因此它恰好绕过了这个 bug。

这是典型的：

> 测试验证了 7:1:2 满负载，却没覆盖部分 class idle 时的公平性。

---

# 十二、P0 修复方案

不建议继续在当前 borrowing 逻辑上打补丁。

更清晰的算法是：

> **空的 credited class 立即放弃本轮剩余 credit。**

增加：

```go
func (s *classScheduler) markEmpty(idx int) {
    if idx < 0 || idx >= len(s.credits) {
        return
    }
    s.credits[idx] = 0
}
```

Worker：

```text
probe class
   ↓
有 message
   → consume credit
   → return

无 message
   → markEmpty(class)
   → probe next
```

当：

```text
[0,1,0]
```

且 retry 为空时：

```text
markEmpty(retry)

↓

[0,0,0]

↓

下一轮 refill

↓

[7,1,2]
```

Scheduled 就不会被饿死。

---

# 十三、建议顺便简化 borrowing 模型

实际上可以不再采用：

```text
zero-credit class 永久 borrow
```

这种模式。

推荐：

```text
本轮：
有 credit 的 class 才参与

如果有 credit 但 queue empty：
credit = 0

全部 credit = 0：
refill
```

这样当只有 scheduled 有流量时：

```text
scheduled 消耗 2
↓
其他 class 被判 empty
↓
立即 refill
↓
scheduled 再消耗 2
```

仍然可以获得 100% 空闲容量。

而当三类都繁忙：

```text
7 : 1 : 2
```

仍严格成立。

逻辑反而更简单。

---

# 十四、必须补的 Scheduler 测试

至少补：

```text
TestScheduledNeverStarvesWhenRetryEmpty

interactive = infinite
retry       = empty
scheduled   = infinite
```

以及：

```text
TestRetryNeverStarvesWhenScheduledEmpty

TestEmptyCreditedClassCannotPinRound

TestTwoActiveClassesReceiveRelativeShare

TestClassBecomesActiveAgainAfterBeingMarkedEmpty
```

这是当前第一优先级。

---

# 十五、本轮新发现 P0/P1：Redis Slot 丢失后 max_inflight 不再严格成立

现在 heartbeat：

```go
if ProviderInflight.Renew(...) == ErrProviderSlotLost {
    recordProviderAdmission("provider_slot_lost")
}
```

然后：

> **继续执行 Provider Run。**



这意味着：

```text
Run Lease = valid
Provider Slot = lost
Provider Call = still running
```

状态是允许存在的。

---

# 十六、Redis 重启即可触发

假设：

```text
真实 Aily active = 100
max_inflight = 100
```

Redis 重启：

```text
provider:feishu_aily:inflight
```

整个 ZSET 丢失。

此时：

```text
100 个真实 Provider Call
```

还在继续。

Heartbeat Renew：

```text
ErrProviderSlotLost
```

但当前代码只记录指标。

Redis：

```text
depth = 0
```

新的 Worker 接下来：

```text
Acquire
```

会再放入最多：

```text
100 slots
```

最终真实 Provider 并发可能：

```text
100 old
+
100 new
=
200
```

所以报告中：

> `max_inflight` 因此在真正并发路径上成立

需要增加一个限定：

> **仅在 Redis semaphore state 未丢失期间成立。**

---

# 十七、为什么单纯 cancel 旧 Worker 也不完全解决

即便发现：

```text
ProviderSlotLost
```

马上：

```text
cancel(execCtx)
```

也不能证明远端 Aily 执行已经停止。

HTTP/SSE 本地断开：

```text
≠
Provider 远端 Run 被取消
```

所以仍然可能短期存在：

```text
old remote execution
+
new remote execution
```

---

# 十八、我更推荐的最终架构：Provider Slot 回 TiDB 做 Truth

从架构原则看：

```text
Redis = fast dispatch / realtime / transient rate state

TiDB = correctness truth
```

那么：

```text
max_inflight
```

这种**安全容量状态**更适合作为 TiDB Truth。

新增：

```text
provider_execution_slots
```

建议字段：

```text
provider
run_id
lease_epoch
lease_token
acquired_at
heartbeat_at
expires_at
```

唯一键：

```text
(provider, run_id, lease_epoch)
```

索引：

```text
(provider, expires_at)
```

---

# 十九、Provider Slot Acquire

事务：

```text
BEGIN

SELECT provider
FOR UPDATE

DELETE expired slots

检查同 ownership slot
    有 → refresh / success

COUNT active slots

>= max
    → reject

INSERT provider_execution_slot

COMMIT
```

Provider Start 官方频率本身就是低 QPS 场景，因此：

```text
每个 provider 一次短事务锁
```

不会成为实际性能瓶颈。

反而会换来：

```text
Redis restart
Redis flush
Redis failover
worker clock skew
```

都不会破坏 `max_inflight`。

---

# 二十、Provider Slot Heartbeat 可以与 Run Lease 合并

最佳模型：

```text
BEGIN

Heartbeat RunLease

Heartbeat ProviderExecutionSlot

COMMIT
```

这样：

```text
Run Ownership alive
⇔
Provider Slot alive
```

形成真正 invariant。

Reaper：

```text
Run requeue / fail
+
delete ProviderExecutionSlot
+
delete RunLease
```

也可以一个事务完成。

这比现在：

```text
TiDB RunLease
+
Redis ProviderSlot
```

两个独立 lease 系统更强。

---

# 二十一、如果暂时坚持 Redis Semaphore

最低限度必须做到：

```text
ProviderSlotLost
→ 进入 degraded gate
→ 暂停新的 Provider Admission
→ 对现有 Run 做 reconciliation
→ 恢复 active slot state
→ 才重新打开 admission
```

而不是：

```text
slot lost
→ metric++
→ 继续
```

但是实现复杂度其实已经接近做 TiDB slot table。

所以我推荐：

> **直接采用 TiDB ProviderExecutionSlot。**

---

# 二十二、P1：当前 ProviderSlot 使用 Worker 本地时间

现在 Redis Lua 的：

```text
now
expiry
```

都是 Go：

```go
time.Now()
```

算出来再传入 Redis。

分布式系统里不应该让：

```text
Worker A clock
Worker B clock
Worker C clock
```

共同决定同一个 Redis semaphore 的过期。

---

# 二十三、可能产生的问题

例如：

```text
Worker A 时间正常
Worker B 时钟快 3 分钟
```

B Acquire 时执行：

```text
ZREMRANGEBYSCORE -inf B.now
```

可能直接把：

```text
仍然有效的 A slot
```

判为过期并删除。

然后又产生：

```text
过度 Admission
```

---

# 二十四、Redis 内部状态应该使用 Redis TIME

Lua 内：

```lua
local t = redis.call('TIME')
local now =
    tonumber(t[1]) * 1000
    + math.floor(tonumber(t[2]) / 1000)
```

Acquire 参数只传：

```text
max
lease_ms
member
```

Redis 自己计算：

```text
now
expiry = now + lease_ms
```

Renew、Depth 同理。

---

# 二十五、RateLimiter 也存在同样的时间源问题

当前 GCRA：

```go
nowMicro := time.Now().UnixMicro()
```

然后把：

```text
now
```

传给 Redis Lua。

Distributed Rate Limiter 应该同样：

```text
Redis-owned state
→ Redis TIME
```

这样所有 Worker 都共享一个权威时间源。

建议这一轮一起改掉。

---

# 二十六、P1：RunLease 也应该使用 DB Clock

现在：

```text
heartbeat_at
```

由：

```sql
CURRENT_TIMESTAMP(3)
```

生成。

但：

```text
expires_at
```

由 Go：

```go
time.Now().UTC().Add(leaseSeconds)
```

传入。

而 Reaper 判断：

```sql
expires_at <= CURRENT_TIMESTAMP(3)
```



这仍然混合：

```text
App Clock
DB Clock
```

你这一轮已经因为：

```text
App / DB clock skew
```

真实踩过 Reaper retryAt 的问题。

同一个原则应该继续贯彻到 Lease。

---

# 二十七、RunLease 推荐改法

不要传绝对：

```text
ExpiresAt
```

改成：

```sql
expires_at =
DATE_ADD(
    CURRENT_TIMESTAMP(3),
    INTERVAL ? MICROSECOND
)
```

Claim 和 Heartbeat 都如此。

形成：

```text
Lease acquisition
Lease heartbeat
Lease expiry detection
```

全部由：

```text
DB Clock
```

控制。

---

# 二十八、RetryAt 也建议最终采用 DB Clock

当前：

```go
retryAt := time.Now().UTC().Add(delay)
```

虽然：

```text
Run.available_at
=
Outbox.available_at
```

已经一致，但实际“5 秒后”仍由 App Clock 决定。

建议在事务中：

```text
SELECT CURRENT_TIMESTAMP(3)
```

然后：

```text
retryAt = dbNow + delay
```

或直接 SQL 计算。

这不是当前 P0，但做 Clock Authority Hardening 时应该一起处理。

---

# 二十九、P1：CI 还没有 Gate 真实 Redis ProviderSlot 测试

报告里本地执行过：

```text
STUDIO_TEST_REDIS=1
go test ./internal/execution/
```

ProviderSlot 测试 7/7。fileciteturn217file0L192-L200

但是当前 GitHub `integration` Job 实际运行的是：

```text
go test ./tests/integration/...
```

而：

```text
ProviderSlot tests
GCRA Redis tests
```

位于：

```text
internal/execution/
```

当前 CI workflow 没有在：

```text
STUDIO_TEST_REDIS=1
```

环境下运行这个 package。

因此：

> ProviderSlot 的真实 Redis 测试目前是“本地验证”，还不是“CI Release Gate”。

---

# 三十、CI 建议调整

Integration Job：

```text
STUDIO_TEST_TIDB=1
STUDIO_TEST_REDIS=1
```

增加：

```text
go test ./internal/execution/... -count=1
go test ./internal/delivery/... -count=1
go test ./tests/integration/... -count=1
```

或者确认所有 package 都适合以后直接：

```text
go test ./... -count=1
```

---

# 三十一、建议增加 Race Gate

完整架构里执行面本身对并发要求很高。

当前 CI：

```text
gofmt
vet
build
unit
integration
mysql57
```

已经不错。

但还缺：

```text
-race
```

建议增加：

```text
go test -race \
  ./internal/execution/... \
  ./internal/delivery/... \
  ./internal/automation/...
```

不一定每次把所有真实 DB Integration 都开 race。

但：

```text
Worker
Heartbeat
executionControl
Scheduler
Delivery Worker
```

应该进入 race gate。

---

# 三十二、P2：TiDB readiness 当前会错误打印 error annotation

当前 workflow：

```text
for ...
    ready → break
done

echo "::error title=tidb not ready::..."
mysql CREATE DATABASE
```

也就是说即使：

```text
第 1 次就 ready
```

后面仍然会打印：

```text
::error tidb not ready
```



所以虽然 Job：

```text
success
```

GitHub UI 仍可能出现误导性的 error annotation。

建议：

```bash
ready=0
for (( attempt=0; attempt<90; attempt++ )); do
  if mysql ...; then
    ready=1
    break
  fi
  sleep 2
done

if [ "$ready" -ne 1 ]; then
  echo "::error title=tidb not ready::..."
  exit 1
fi
```

---

# 三十三、P2：Invariant L 仍只是近似

现在已经从：

```text
当前所有 enabled target
```

修成：

```text
target.created_at <= occurrence.finished_at
```



解决了：

```text
今天新增 target
却要求昨天 occurrence 已投递
```

的问题。

但是还有：

```text
昨天 target 已创建
当时 disabled

今天重新 enabled
```

这种情况。

因为：

```text
created_at 仍然早于 occurrence.finished_at
```

Invariant L 仍然可能认为：

```text
历史 occurrence 缺 delivery
```

长期最佳方案不是继续加时间条件，而是：

```text
Occurrence Delivery Snapshot
```

或者：

```text
ScheduleDelivery version/history
```

不过这属于 Observability 精度，不影响实际发送链路。

P2 即可。

---

# 三十四、P2：Priority Weight 应做生产配置校验

目前：

```text
RUN_PRIORITY_WEIGHTS
```

允许：

```text
7,0,0
```

或者：

```text
7,1,0
```

因为 parser 只禁止负数和全 0。

如果系统 invariant 是：

> Scheduled 永远不得 starvation

那么：

```text
scheduled weight = 0
```

理论上就不应该允许。

建议 production validation：

```text
interactive > 0
retry       > 0
scheduled   > 0
```

如果未来确实允许关闭某个 class：

应该显式：

```text
enabled=false
```

而不是用 weight=0 偷偷表达。

---

# 三十五、文档术语建议

报告同时定义：

```text
attempt = provider execution count
```

又称 ProviderSlot 为：

```text
attempt-scoped
```

但 ProviderSlot 实际是在：

```text
BeginProviderAttempt
```

之前就 Acquire。

它真正绑定的是：

```text
ExecutionOwnership
=
run_id + lease_epoch + lease_token
```

所以术语建议统一为：

```text
ownership-scoped ProviderSlot
```

而不是：

```text
attempt-scoped
```

避免把：

```text
claim ownership epoch
```

和：

```text
runs.attempt provider execution count
```

混成一个概念。

代码本身没错，这是文档语义优化。

---

# 三十六、当前模块评分

| 模块 | 当前评分 |
|---|---:|
| Overall Architecture | **97/100** |
| Application / Runtime Boundary | **97/100** |
| ExecutionOwnership | **99/100** |
| Claim / Lease State Machine | **97/100** |
| Attempt Accounting | **99/100** |
| Atomic Finalize | **98/100** |
| Reaper | **98/100** |
| Schedule / Occurrence | **97/100** |
| Delivery Durability | **97/100** |
| Delivery External Semantics | **94/100** |
| Retry / Outbox | **98/100** |
| Provider Inflight Fencing | **94/100** |
| Provider Inflight Failure Recovery | **78/100** |
| Priority Admission | **80/100** |
| Distributed Rate Limit | **91/100** |
| TiDB / MySQL 5.7 | **97/100** |
| CI | **93/100** |
| Observability / Invariants | **95/100** |

综合：

```text
代码成熟度：约 94 / 100

Production Readiness：约 90 / 100
```

---

# 三十七、当前 Release 判断

内部研发：

```text
GO
```

功能 UAT：

```text
GO
```

小规模真实用户：

```text
GO
```

大规模 Schedule + 持续 Interactive：

```text
暂缓 Production GO
```

目前真正需要先关闭：

```text
1. Weighted Scheduler 空 class 饥饿 bug

2. Provider max_inflight Redis-state-loss 问题
```

这两个修完以后，基本可以进入正式压测阶段。

---

# 三十八、下一轮开发计划

建议本轮命名：

# Admission Fairness & Distributed Lease Hardening

不需要再动：

```text
ExecutionOwnership
Atomic Finalize
Reaper state machine
Attempt accounting
Artifact fence
Session fence
Schedule core
Delivery transaction model
```

这些已经稳定。

---

# Phase 1：修 Weighted Scheduler

修改：

```text
internal/execution/priority.go
internal/execution/worker.go
internal/execution/priority_test.go
```

实现：

```text
empty credited class
→ credit = 0
```

不要让空 class 的 credit 永久阻止 round refill。

验收：

```text
interactive = infinite
retry = empty
scheduled = infinite

1000 次消费
scheduled 必须持续得到份额
```

---

# Phase 2：Provider Inflight Durable Truth

推荐新增：

```text
provider_execution_slots
```

TiDB 做：

```text
Acquire
Renew
Release
Expiry
Reaper cleanup
```

Redis ProviderSlot 可以删除，或者只保留：

```text
metric/cache
```

目标 invariant：

```text
active provider execution
⇔
active provider_execution_slot
```

Redis Restart：

```text
不得增加真实 Provider 并发上限
```

---

# Phase 3：Clock Authority Hardening

明确规则：

```text
TiDB state
→ TiDB clock

Redis state
→ Redis TIME

Application clock
→ 仅日志 / UI / 非一致性逻辑
```

修改：

```text
Run Lease expiry
Provider Slot（如果仍用 Redis）
GCRA
RetryAt
Delivery retry time
```

---

# Phase 4：CI Coverage Closure

Integration Job 增：

```text
STUDIO_TEST_REDIS=1
go test ./internal/execution/...
```

增加：

```text
go test -race
```

同时修掉 TiDB readiness 的误 error annotation。

---

# Phase 5：补故障测试

新增：

```text
Priority:
- retry empty + interactive/scheduled busy
- scheduled empty + interactive/retry busy
- class idle → active

Provider Capacity:
- Redis DEL inflight key during active runs
- Redis restart during active provider executions
- slot heartbeat loss
- provider slot recovery
- stale owner after failover

Clock:
- app clock ahead
- app clock behind
- DB clock authoritative lease expiry

CI:
- Redis-backed ProviderSlot / GCRA tests under Actions
```

---

# Phase 6：执行正式压测

你上传的报告也明确承认：

```text
1000 scheduled @ 08:30
+
continuous interactive
```

尚未执行。fileciteturn217file0L252-L257

这一轮代码修完以后必须执行。

场景建议：

```text
1000 schedules @ 08:30
5000 schedules @ 09:00
持续 interactive traffic
retry queue = 0
retry queue = burst
Redis restart
Worker kill
TiDB transient latency
Aily 429
```

重点指标：

```text
interactive queue P95/P99
scheduled max waiting time
scheduled starvation count = 0
provider actual inflight <= limit
start-chat QPS <= limit
Run loss = 0
duplicate canonical execution = 0
lease invariant violation = 0
```

---

# 三十九、建议提交顺序

```text
fix(execution): close weighted scheduler idle-class starvation

refactor(execution): make provider admission slots durable

fix(execution): use authoritative clocks for distributed leases

test(execution): cover redis reset and partial-class fairness

ci(backend): gate redis execution tests and race detector

fix(ci): correct tidb readiness annotation

test(load): add mixed scheduled and interactive admission scenario
```

---

# 四十、最终结论

这一次和上一轮相比，代码确实又前进了一大步。

上一轮最核心的：

```text
ProviderSlot stale ownership
Attempt 误消耗
Retry wakeup 错位
CI 不可用
```

都已经真正关闭。

当前我**不建议再重构 Execution Core**。

接下来应该非常集中地解决两个点：

```text
Weighted Fair Scheduling
+
Provider Inflight Durable Correctness
```

尤其第一个不是理论问题，而是当前代码在：

```text
retry queue = empty
interactive = busy
scheduled = busy
```

这个非常常见场景下可以直接复现的 Scheduled starvation。

第二个则决定你能不能真正宣称：

```text
provider max_inflight 是严格上限
```

而不只是：

```text
Redis 正常时的保护性限流。
```

这两个关闭，再补 Redis CI gate 和正式混合流量压测，我会认为这套 Application + Schedule + Aily Runtime 主干已经具备正式生产发布条件。