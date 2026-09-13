# potal 完整架构实施评测报告

评测对象：`shilin414/potal`  
评测分支：`dev`  
评测基准：Creation Agent Studio 最终 To-Be 架构  
评测重点：Application Runtime Portal、Go 执行面、TiDB、Redis Streams、Run/Lease/Outbox、Aily、Scheduler/Delivery、身份认证、PC/移动端 Shell、测试与生产可靠性。

---

# 一、执行摘要

当前版本已经完成了一次比较成功的“架构换骨”。

尤其值得肯定的是：

- 已从原来偏 Django 业务系统的结构，演进成独立的 Go Runtime Backend；
- 已拆出 API / Stream / Scheduler / Worker 等执行角色；
- 已实现 Application、Runtime Binding、Provider、Run、RunEvent、Outbox、Lease 等核心抽象；
- 已采用 TiDB CAS，而没有重新走 `SKIP LOCKED` 路线；
- 已实现 Redis Streams + TiDB fallback 的双层执行机制；
- Aily 已经真正作为 Runtime Provider，而不是写死到 Application；
- Aily Session / Chat / Artifact / Attachment / UAT/TAT 的建模方向基本正确；
- 前端已经形成 AppShell + WorkspaceHost；
- DesktopAppShell / MobileAppShell 已经真正拆开；
- Scheduler / Occurrence / Delivery 也已经进入独立领域模型，而不是简单 cron；
- 已开始建立 TiDB、CAS、Schedule、SSE 的集成测试。

项目结构本身已经比较接近我们之前定义的：

> **企业 AI Application Runtime Portal**

而不是：

> “给飞书 Aily 套一个网页”。

这一点非常重要。

---

# 二、总体评分

我给当前 `dev` 分支的评分如下：

| 领域 | 评分 | 评价 |
|---|---:|---|
| 总体架构方向 | **91/100** | 基本没有走偏 |
| Application / Runtime 解耦 | **92/100** | 很好 |
| Aily Provider 设计 | **88/100** | 已较成熟 |
| Run / Event 模型 | **82/100** | 模型好，执行正确性仍有坑 |
| Worker / Queue / Outbox | **63/100** | 有 P0 |
| Lease / 故障恢复 | **55/100** | 当前最大风险区 |
| TiDB 兼容设计 | **87/100** | CAS 思路正确 |
| Scheduler | **72/100** | 模型完整，部分策略语义未真正落地 |
| Delivery | **80/100** | 独立建模是对的 |
| 分布式限频 | **82/100** | GCRA 很好，Redis 故障策略需调整 |
| 身份 / Aily Credential | **86/100** | 后端设计方向对 |
| 登录产品体验 | **58/100** | 明确偏离最终架构 |
| PC / Mobile Shell | **92/100** | 高度符合目标 |
| 测试体系 | **78/100** | 已有基础，但缺故障型测试 |
| 当前生产就绪度 | **约 70/100** | 修完 P0 后可明显提升 |

### 最终判断

**架构设计：通过。**

**代码重构方向：通过。**

**大规模推翻重做：不需要。**

**现在直接作为正式生产执行内核承载重要任务：暂不建议。**

主要原因不是 Application、Aily 或前端设计，而是 **Worker 的 lease/fencing 正确性**。

---

# 三、与最终架构的一致性

## 3.1 Application ≠ Runtime ≠ Provider

✅ **高度一致。**

Aily 已被实现为独立 Runtime Adapter，代码明确维护：

- `binding.external_resource_id → agent_id`
- `AgentThread.remote_id → Aily session_id`
- `Run.external_run_id → agent_chat_id`
- attachment external ID
- artifact external ID

并且能力矩阵明确：

- streaming = true
- async_execution = true
- conversation = true
- attachment = true
- artifact = true
- visibility = true
- cancel = false
- resume = false

这正是我们之前要求的 Provider Capability 模型。

尤其 `cancel=false`、`resume=false` 很重要，说明没有为了前端功能统一而伪造 Aily 不存在的能力。

**建议保持。**

---

# 四、P0：必须优先修复的问题

目前确认有 **3 个 P0**。

---

## P0-1：Outbox 的 UUID 序列化实际上破坏了 Redis 快速调度链路

### 代码现状

Run ID 采用：

```text
UUIDv7
↓
BINARY(16)
```

这个方案本身很好，`ids.ID` 也已经正确提供：

```go
ID.String()
ID.Bytes()
ids.Parse()
ID.Scan()
```

即：

> DB 使用 16 字节二进制，API/Redis/JSON 使用 canonical UUID。



但是 Outbox Relay 中：

```go
"run_id": string(row.AggregateID),
```

把 `BINARY(16)` 原始 UUID 字节直接转成了 Go string。

Worker 随后：

```go
runIDStr := ...
runID, err := ids.Parse(runIDStr)
```

要求的是：

```text
0199xxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
```

这种 canonical UUID。解析失败后 Worker：

```go
ACK
return
```



### 实际后果

因此正常流程：

```text
TiDB Outbox
   ↓
Redis Stream
   ↓
Worker
```

很可能变成：

```text
Outbox BINARY(16)
   ↓
16 byte raw string
   ↓
ids.Parse()
   ↓
失败
   ↓
XACK
```

也就是说：

> Redis Stream 主唤醒通道实际上可能一直没有真正执行 Run。

现在之所以系统还可能“能跑”，是因为 Worker 还有：

```text
TiDB fallback scanner
```

不断扫描 queued Run 并 CAS Claim。

所以这种问题非常危险：

> 功能测试看起来正常，但实际上主执行通道已经坏了，只是灾备通道长期在承担主链路。

### 修复

不要修改 `BINARY(16)` 设计。

只需要统一边界：

```go
id := mustID(row.AggregateID)

fields := map[string]any{
    "run_id": id.String(),
}
```

更推荐建立唯一 ID codec：

```text
DB Boundary
BINARY(16)
   ↕
ids.ID
   ↕
Transport Boundary
canonical UUID string
```

整个代码库禁止：

```go
string(uuidBytes)
```

承担 UUID 编码职责。

### 还应该增加

测试：

```text
CreateRun
→ Outbox
→ Relay
→ Redis Stream
→ Worker
→ CASClaim
```

真正跑完整链路。

目前已有 TiDB CAS/Schedule/SSE 集成测试，但是这个跨组件链路显然没有覆盖到，否则该问题应该会立即被发现。当前 integration 目录主要已有 CAS、Schedule、SSE、PubSub 等测试。

---

# P0-2：CAS Claim 成功、Lease 创建失败会产生永久 running Run

这是我认为当前最容易出现“任务莫名卡死”的问题。

Worker 当前逻辑：

```text
CASClaim
queued → running
↓
AcquireLease
↓
执行 Handler
```

但是代码里：

```go
won, err := CASClaim(...)
...
if err := AcquireLease(...); err != nil {
    ACK
    return
}
```

也就是说：

```text
Run = running
Lease = 创建失败
Redis Message = ACK
```



scan fallback 分支也存在同一结构：

```text
CASClaim
↓
AcquireLease error
↓
continue
```

### 为什么是 P0

Lease Reaper 恢复的是：

```text
expired lease
```

但这个 Run：

```text
根本没有 Lease
```

fallback scanner 又扫描：

```text
queued run
```

而这个 Run 已经是：

```text
running
```

最终形成：

```text
running
+
无 lease
+
无 Redis message
+
不会被 fallback 扫描
+
不会被 expired lease reaper 找到
```

=> **永久卡死。**

---

## 推荐修法

### 最优方案

把：

```text
CAS Claim
+
Create Lease
```

变成一个 TiDB transaction。

例如：

```text
BEGIN

UPDATE runs
SET status='running',
    attempt=attempt+1
WHERE id=?
AND status='queued'

affected_rows == 1

INSERT INTO run_leases ...

COMMIT
```

Lease INSERT 失败：

```text
ROLLBACK
```

Run 自动仍是 queued。

### 这是 TiDB 非常适合做的

不需要：

```sql
SELECT ... FOR UPDATE SKIP LOCKED
```

仍然保持：

```text
CAS + transaction
```

与现在的设计一致。

---

# P0-3：Lease 缺少真正的 Fencing Token

目前创建 Lease 时虽然已经生成：

```go
LeaseToken
```

这是好事。

但问题是：

> 后续执行基本没有使用这个 Token 作为写入 fencing 条件。

Heartbeat 只是：

```text
run_id
+
worker_id
```

而 Finish：

```text
CASFinishRun
WHERE run_id
AND non-terminal
```

不是：

```text
WHERE run_id
AND lease_token
```

而且 Finish 后：

```text
DeleteLease(run_id)
```

还是无条件删除。代码甚至明确写着：

> Lease removal happens unconditionally.



---

## 典型事故场景

Worker A：

```text
取得 Run
lease=A
开始请求 Aily
```

随后：

```text
网络暂停 / GC / Redis/TiDB 抖动
```

A 的 lease 过期。

Reaper：

```text
回收
→ Run queued
```

Worker B：

```text
取得 Run
lease=B
重新执行
```

然后 Worker A 恢复。

此时 A 仍可能：

```text
AppendEvent
Update external_run_id
写 Artifact
写 Message
Finish Run
DeleteLease
```

甚至可能：

```text
把 Worker B 的 Lease 删除掉
```

这就是经典：

> **stale worker problem**

---

# 五、Lease 正确实现建议

不要只修 DeleteLease。

建议升级成：

```text
Run
├── lease_epoch
```

或者：

```text
RunLease
├── run_id
├── worker_id
├── lease_token
├── lease_epoch
├── expires_at
```

每次 claim：

```text
lease_epoch++
```

Worker 获得：

```text
ExecutionOwnership {
    run_id
    worker_id
    lease_token
    lease_epoch
}
```

之后所有 Worker 写操作都必须携带这个 ownership。

例如：

```sql
UPDATE runs
SET ...
WHERE id = ?
AND status = 'running'
AND lease_epoch = ?
```

或者通过 Lease existence/fence 校验。

需要 fencing 的不仅仅是：

```text
Finish
```

而是至少：

```text
UpdateExternalRunID
AppendEvent
Persist Message
Persist Artifact
Retry/Requeue
Finish
DeleteLease
```

如果：

```text
affected_rows = 0
```

Worker 必须认为：

> “我已经失去这个 Run 的所有权。”

然后立即停止写入。

---

# 六、Heartbeat 当前还缺少“失去 Lease → 停止旧 Worker”

当前 Heartbeat：

```go
ok, err := HeartbeatLease(...)
if err != nil || !ok {
    Log.Warn("heartbeat lost lease")
}
```

然后：

```text
仅记录日志
```

原来的 Provider Execute 仍继续执行。

应该把：

```go
inflight map[runID]bool
```

升级成：

```go
inflight map[runID]*ExecutionControl

type ExecutionControl struct {
    Cancel     context.CancelFunc
    LeaseToken ...
    LeaseEpoch ...
}
```

Heartbeat：

```text
续租失败 / ownership lost
↓
cancel()
↓
Provider stream/poll 本地停止
↓
禁止继续持久化
```

对于已经提交给 Aily 的远程 Chat：

```text
无法真正 cancel Aily
```

没有关系。

正确语义应该是：

> Aily 可能继续运行，但旧 Worker 已经失去“写 Studio canonical state”的权利。

这就是 fencing 的意义。

---

# 七、Scheduler：整体设计正确，但 Misfire 语义需要纠正

Scheduler 现在整体结构很好。

尤其做对了：

```text
Schedule
→ Occurrence
→ Conversation
→ Message
→ Run
→ Outbox
```

放在同一个 transaction 中。

同时：

```text
(schedule_id, scheduled_at)
```

作为幂等槽位，也是正确方向。

---

# 八、P1：`fire_once` 当前实际上会演变为“逐槽补历史任务”

Scheduler 目前只有：

```go
if delay > misfireGrace &&
   MisfirePolicy == MisfireSkip
```

进行了特殊处理。

其他 MisfirePolicy：

```text
fire_once
catch_up
```

实际上没有独立算法。

假设：

```text
每天一次
服务器停了 10 天
next_run_at = 10 天前
misfire = fire_once
```

当前逻辑会：

```text
执行 Day1
advance → Day2
↓
下一轮 scheduler
执行 Day2
advance → Day3
...
```

也就是说：

> fire_once 实际逐渐退化成 catch_up。

如果 overlap=queue，则会一个一个慢慢补。

---

# 九、推荐重新定义三种 Misfire

### skip

```text
发现已经错过
↓
不执行
↓
next_run_at 快进到第一个 > now 的槽位
```

### fire_once

```text
发现 N 个错过槽位
↓
仅创建 1 个 Run
↓
Occurrence 保留原始计划时间/增加 misfire metadata
↓
next_run_at 一次性快进到未来
```

不是逐个 advance。

### catch_up

允许补跑，但必须有：

```text
max_catch_up
```

比如：

```text
10
```

否则系统停机 30 天，而配置每分钟执行一次：

```text
43200 个 Run
```

可能直接把恢复后的系统打爆。

---

# 十、P1：Run Now 在 overlap=queue 时会突破“不并行”语义

`TriggerNow()` 当前：

```go
HasActiveOccurrence()

if active && overlap == skip {
    reject
}
```

只有 `skip` 被拦截。

如果：

```text
overlap = queue
```

有正在运行的 Occurrence 时：

```text
TriggerNow
↓
createOccurrenceAndRun
↓
直接创建 Run
```



这就可能同时存在：

```text
Schedule Run A = running
Manual Run B = queued
```

而 Worker 并不知道这个 Schedule 的 overlap queue 约束。

于是 B 完全可能被另一个 Worker 立即取走。

最终：

```text
A + B 并行
```

---

## 正确做法

Run Now 也必须进入统一：

```text
Occurrence Admission
```

有 active occurrence：

### overlap=skip

```text
409 / skipped
```

### overlap=queue

```text
创建 Occurrence
status=pending
不创建 Run
```

当前 Run 结束：

```text
pending occurrence
↓
admit
↓
创建 Run
```

这样：

> overlap policy 属于 Scheduler/Admission，不属于 UI 行为。

---

# 十一、Aily Provider：目前是整个后端完成度最高的部分之一

这里总体评价较高。

### 已正确实现

Aily Runtime Capability。

Lazy Session。

AgentThread：

```text
provider
auth_mode
auth_subject_key
remote_id
```

并在 session reuse 前检查：

```text
provider
identity mode
subject
```

避免：

```text
用户 A 的 Aily Session
被用户 B 复用
```



### Streaming

交互模式：

```text
Aily SSE
↓
Unified Event
↓
Studio
```

并且 Stream 正常结束、异常断开之后：

```text
都不会直接认为 Run 成功
```

而是：

```text
GET chat result
↓
Final Reconciliation
```

这完全符合之前定稿。

### Background

实现：

```text
Submit
↓
poll 1s
↓
2s
↓
3s
↓
5s
```

也符合原来的设计。

---

# 十二、P1：目前仍会持久化大量 content.delta

在 streaming executor 中：

```go
case EventContentDelta:
    Svc.AppendEvent(...)
```

即：

```text
每一个统一 content.delta
↓
RunEvent
↓
TiDB
```



这与我们的原设计有一个重要差异：

> 高频 token/chunk 不应全部持久化。

如果 Aily SSE 很碎，例如：

```text
200~1000 delta / 回答
```

在并发用户增加后，TiDB 的热点会逐渐变成：

```text
run_events INSERT
+
sequence allocation
+
Redis publish
```

而不是 Provider API。

---

## 建议

分两层：

```text
Provider raw delta
       ↓
Transient Stream Buffer
Redis / memory
       ↓
50~150ms coalesce
       ↓
Browser SSE
```

DB 只存：

```text
content.chunk
content.completed
artifact.discovered
run.started
run.completed
run.failed
```

或者：

```text
每 500~2000 字符一个 persisted chunk
```

最终完整 Message 永久落库。

---

# 十三、P1/P2：Aily 附件目前整文件进入内存

Adapter 接口目前通过：

```go
in.Data
```

并用：

```go
len(in.Data)
```

检查 5MB / 40MB。

意味着：

```text
40MB 文件
=
至少 40MB Go Heap
```

10 个并发上传：

```text
≈400MB+
```

还没有计算 multipart copy、HTTP buffer、GC。

### 推荐

将：

```go
[]byte
```

换成：

```go
io.Reader
```

并携带：

```text
Size
Filename
ContentType
```

使用：

```text
io.Pipe
+
multipart.Writer
```

直接流向 Aily。

如果未来加入对象存储：

```text
Browser
→ Object Storage
→ Worker streaming
→ Aily
```

更好。

---

# 十四、P1：RuntimeSnapshot 不应该在 Worker 中直接 type assertion

当前 Aily Executor 中有类似：

```go
snapshot["external_resource_id"].(string)
snapshot["identity_mode"].(string)
```



如果数据库里：

```json
{
  "identity_mode": null
}
```

或者配置迁移异常：

```text
panic
```

虽然 Worker 有：

```text
recover()
```

这避免了整个 Worker 崩溃，但：

> 配置问题不应该进入 Worker panic/retry 体系。

### 推荐

创建 Run 时：

```text
RuntimeBinding
↓
TypedRuntimeConfig.Validate()
↓
RuntimeSnapshot
```

Worker 再：

```go
cfg, err := ParseAilySnapshot(...)
```

错误直接：

```text
RUN_CONFIG_INVALID
```

终止。

---

# 十五、分布式限频：设计很好，但 Redis 故障不应该完全 fail-open

你现在采用 Redis Lua GCRA。

这是一个好设计，比固定时间窗口更合理，可以避免：

```text
0.99 秒请求 10 个
1.01 秒又请求 10 个
```

这种边界突发。

并且是：

```text
所有 Worker 共用 Redis limiter
```

符合架构要求。

但是现在：

```go
Redis error
→ return allowed=true
```

随后：

```go
Acquire()
→ err
→ return nil
```

即：

> Redis 限频故障 = 完全无限流。

如果 Redis 故障恰好又有 Run backlog：

```text
所有 Worker
↓
同时放开
↓
Aily
↓
429 storm
```

### 更好的降级

```text
Redis GCRA
↓ failure
进程级 local limiter
↓
provider
```

而不是：

```text
Redis failure
↓
unlimited
```

可以同时增加：

```text
provider_limiter_degraded = 1
```

监控。

---

# 十六、Outbox 方向是正确的

这里不要因为 UUID Bug 就推翻 Outbox。

你现在：

```text
Run
+
Outbox
```

在一个 DB transaction 中创建。

然后：

```text
Relay
↓
Redis Stream
```

发布。

失败则：

```text
Outbox 保留
↓
重试
```



这是比：

```text
INSERT Run
↓
直接 Redis
```

可靠很多的架构。

所以：

> **保留 Outbox，只修 ID Boundary 和测试。**

---

# 十七、Redis Streams + TiDB fallback 设计值得保留

Worker 的整体原则：

```text
Redis Stream = fast wake-up
TiDB         = source of truth
CAS          = correctness
fallback scan = recovery
```

这是正确的。

同时已经有：

```text
Consumer Group
XAUTOCLAIM
Pending reclaim
Fallback scan
Lease reaper
```

基础设施思路已经比较完整。

因此这里不是架构错误，而是：

> **lease ownership 的最后一公里没有封死。**

---

# 十八、TiDB 方案：基本符合最终架构

特别值得肯定：

Run 使用：

```text
UUIDv7
+
BINARY(16)
```

同时明确保持：

```text
MySQL 5.7 SQL compatibility
```

不依赖：

```text
UUID_TO_BIN
```



这对于你后续：

```text
TiDB
MySQL
TiProxy
```

兼容非常有价值。

Scheduler 也没有重新引入：

```text
SKIP LOCKED
```

而是：

```text
unique barrier
+
CAS
```

方向正确。

---

# 十九、Scheduler + Delivery 分离做得很好

代码已经明确：

```text
Run ≠ Delivery
```

DeliveryExecution 独立存在。

这是非常重要的。

正确业务语义就是：

```text
AI Run succeeded
```

不代表：

```text
飞书消息发送成功
```

而现在：

```text
Scheduled Run succeeded
↓
Delivery Dispatcher
↓
每 target 创建 DeliveryExecution
↓
Delivery Outbox
```

并通过：

```text
UNIQUE(occurrence_id, schedule_delivery_id)
```

做 fan-out 幂等。

这个设计建议继续保留。

---

# 二十、认证体系：后端方向好，产品入口偏离明显

OAuth 本身已经做得比较规范：

```text
signed OAuth state
HMAC-SHA256
10 minute TTL
safe return_to
refresh token AES-GCM
```



Refresh Token：

```text
不是明文入库
```

这很好。

---

# 二十一、明确架构偏离：普通用户仍然进入通用登录页

我们最终明确过：

```text
普通用户
↓
进入系统
↓
无 Studio Session
↓
自动 Feishu OAuth
↓
Workspace
```

只有：

```text
/login/admin
```

才出现：

```text
username
password
```

但现在 ProtectedRoute：

```tsx
if (!isAuthenticated) {
    return <Navigate to="/auth/login" />
}
```



然后 `/auth/login` 页面同时存在：

```text
用户名密码
飞书登录按钮
企业 SSO
立即注册
```



所以当前产品实际上变成：

> 通用 SaaS Identity Portal

而不是我们确定的：

> Feishu SSO Only + Local Admin Fallback。

---

# 二十二、建议纠正登录路由

最终建议：

```text
/
│
├─ authenticated
│      ↓
│   Workspace
│
└─ unauthenticated
       ↓
/api/identity/oauth/start
       ↓
Feishu
       ↓
/auth/feishu/callback
       ↓
/
```

用户甚至不应该看到“登录页”。

---

## 管理员

增加：

```text
/login/admin
```

展示：

```text
username
password
MFA（未来）
```

---

## `/login`

可以：

```text
/login
↓
自动 redirect Feishu OAuth
```

---

## 建议逐步移除普通入口

普通用户不展示：

```text
用户名密码
立即注册
企业 SSO
```

除非你现在有意把产品方向改成“面向多企业通用门户”。

如果确实要做通用产品，那么这不是技术错误。

但如果仍按照我们的最终架构：

> 这是明确偏离。

---

# 二十三、前端 Shell：这部分实现得非常好

路由已经真正做到：

```text
<AppShell>
    <Outlet/>
</AppShell>
```

并且：

```text
/
→ WorkspaceHost(home)

/chat/:applicationSlug
→ WorkspaceHost(chat)

/app/:applicationSlug
→ WorkspaceHost(page)

/workflow/:applicationSlug
→ WorkspaceHost(workflow)
```



这基本就是我们之前设计的目标结构。

最关键的是：

> AppShell 是 layout route，所以 Application 切换不会把整个 Shell 卸载。

这一点非常正确。

---

# 二十四、PC / Mobile 双 Shell 也已经落地

现在已经存在：

```text
AppShell
DesktopAppShell
MobileAppShell
useIsMobile
```



所以你没有走：

```text
一个 PC 页面
↓
CSS 压成 375px
```

这种错误路线。

这和我们定稿的：

```text
Shared Domain
+
Desktop Shell
+
Mobile Shell
```

完全一致。

这部分无需大改。

---

# 二十五、仍然存在一个“过渡期架构”

Router 中仍存在：

```text
/workspace
/workspace/:id
```

并明确标注：

```text
Template-workflow workspace:
still on the legacy agent engine.
```

同时：

```text
/workflow-runs/:runId
```

仍然是 fullscreen console，而不是完整统一到 WorkspaceHost。

这个我暂时不认定为错误。

属于：

> **可接受的迁移期偏离。**

但是应该列入最终技术债。

长期目标仍然应该：

```text
Legacy Agent Engine
        ↓
RuntimeAdapter
        ↓
Run
        ↓
RunEvent
```

最终不再维护两套 execution semantics。

---

# 二十六、Go 重写本身不是架构偏离

我们早期建议过：

```text
不为了换技术而重写 Django
```

但现在已经实际形成：

```text
Go Backend Runtime
```

而且新架构中的：

```text
execution
automation
delivery
catalog
identity
integration
transport
```

边界已经比较清楚。

因此在现在这个时间点：

> 不应该因为“以前说保留 Django”再迁回 Django。

语言不是架构。

当前 Go 架构反而很适合：

```text
大量 SSE
大量 HTTP provider I/O
大量 Worker
并发 Run
Scheduler
Redis Stream
```

所以这一项我定义为：

> **可接受且目前看效果不错的实现层偏离。**

---

# 二十七、目前最需要建立的工程规则：ID Boundary

项目现在已经存在正确的：

```text
ids.ID
```

但个别代码绕过了它。

建议直接制定一条 code review rule：

> **BINARY(16) UUID 永远不得使用 `string(rawBytes)` 跨 API、Redis、JSON、日志、Event Payload 边界。**

允许：

```text
DB → ids.ID
ids.ID → String()
```

以及：

```text
String → ids.Parse()
ids.ID → Bytes()
```

甚至可以写静态检查：

```text
AggregateID
RunID
LeaseToken
Artifact UUID
DeliveryExecution ID
```

发现：

```go
string(xxxID)
```

直接 CI warning/fail。

---

# 二十八、Run Event 需要进一步控制写放大

你现在已经正确建立：

```text
RunEvent
```

而不是浏览器直接理解 Aily SSE。

这是正确的。

但建议再分：

```text
Provider Event
↓
Transient Event
↓
Normalized Durable Event
```

否则：

```text
Aily token
=
RunEvent row
```

最终 RunEvent 表会非常快膨胀。

---

# 二十九、推荐的最终执行平面

修复后建议保持：

```text
                TiDB
                  │
          Run + Outbox TX
                  │
                  ▼
             Outbox Relay
                  │
                  ▼
             Redis Stream
                  │
                  ▼
               Worker
                  │
        Claim + Lease TX
                  │
                  ▼
        Provider RuntimeAdapter
                  │
       ┌──────────┴──────────┐
       │                     │
    Aily SSE              Polling
       │                     │
       └──────────┬──────────┘
                  │
            Reconciliation
                  │
                  ▼
            fenced Finish
                  │
                  ▼
        Message / Artifact / Run
                  │
                  ▼
            Delivery Fanout
```

Browser：

```text
Browser
   │
   ▼
SSE Gateway
   │
Redis notification
   │
TiDB RunEvents
```

Browser 生命周期与 Worker 生命周期完全解耦。

---

# 三十、建议优先级

## P0 — 上生产主流量前

### P0-1
修 Outbox UUID transport。

### P0-2
Claim + Lease 必须原子化。

### P0-3
引入真正的 lease fencing，并在 lease lost 后取消旧 Worker ownership。

这三件事情建议看成一个完整迭代：

> **Execution Correctness Hardening**

而不是三个零散 Bug。

---

# 三十一、P1 — P0 后马上处理

建议顺序：

1. 修 `fire_once`。
2. 修 `run-now + overlap=queue`。
3. catch_up 增加 `max_catch_up`。
4. 普通用户登录改成自动 Feishu OAuth。
5. 增加 `/login/admin`。
6. Aily `content.delta` 聚合后再持久化。
7. RuntimeSnapshot 改成 typed validation。
8. Redis rate limiter 故障时增加 local fallback limiter。
9. Aily attachment 改 streaming upload。
10. Legacy workflow execution 开始向 RuntimeAdapter 收敛。

---

# 三十二、P2 — 性能与长期工程质量

可以后续逐步做：

```text
RunEvent retention / archive
Artifact object storage mirror
Provider circuit breaker
DLQ
Run replay/admin diagnostics
Schedule execution audit UI
Lease debugging UI
Provider health dashboard
per-provider queue depth
per-provider concurrency control
workspace KeepAlive
mobile keyboard/safe-area专项
```

---

# 三十三、必须补充的测试矩阵

当前已经有：

```text
TiDB CAS
TiDB Schedule
SSE Gateway
Pub/Sub
Go Chat E2E
```

这是不错的基础。 

但下一阶段不能只测试：

> 正常功能是否成功。

必须开始测试：

> **系统在一半失败时是否仍然正确。**

建议增加：

### 1. Outbox UUID

```text
DB Outbox
→ Relay
→ Redis
→ Worker
→ successful claim
```

断言 Redis 里的 run_id 为 canonical UUID。

### 2. Claim/Lease atomic test

故意让：

```text
INSERT lease
```

失败。

断言：

```text
Run != running without lease
```

### 3. Lease fencing chaos test

```text
Worker A claim
↓
暂停 A
↓
Lease expiry
↓
Worker B reclaim
↓
恢复 A
```

断言：

```text
A 无法 AppendEvent
A 无法 Finish
A 无法 Delete B Lease
```

### 4. Scheduler downtime fire_once

```text
cron=* * * * *
系统停 1 小时
fire_once
```

恢复后：

```text
只创建 1 次补偿 Run
```

而不是 60 次。

### 5. catch_up limit

```text
missed=10000
maxCatchUp=10
```

断言最多创建 10。

### 6. RunNow overlap queue

```text
Occurrence A running
↓
TriggerNow
```

断言：

```text
B 不进入 running
```

### 7. Redis failure rate limit

Redis limiter shutdown：

```text
100 workers
```

断言仍然不会瞬间向 Aily 发 100 个 StartChat。

### 8. Aily stream disconnect

```text
SSE interrupted
```

断言：

```text
GET chat result
→ reconciliation
→ final canonical state
```

### 9. Browser disconnect

```text
关闭浏览器
```

断言：

```text
Run 继续执行
```

### 10. OAuth

普通访问：

```text
/
```

未登录应该：

```text
Feishu OAuth
```

而不是进入本地密码登录页。

---

# 三十四、建议新增可观测指标

当前已有部分 Metrics，说明方向正确。

建议最终至少监控：

```text
run_queue_depth{provider}
run_queue_wait_seconds
run_running_total
run_duration_seconds

outbox_pending_total
outbox_oldest_age_seconds

worker_inflight
worker_claim_conflict_total

lease_active
lease_expired_total
lease_lost_total
lease_fencing_reject_total

redis_stream_pending
redis_stream_reclaimed_total

provider_request_total
provider_429_total
provider_latency
provider_limiter_wait_seconds

aily_reconcile_total
aily_stream_transport_error_total

schedule_trigger_delay
schedule_misfire_total
schedule_catchup_total
schedule_overlap_skip_total

delivery_pending
delivery_retry_total
delivery_failed_total

sse_connection_total
sse_reconnect_total
```

其中尤其建议监控：

```text
running Run but no active lease
```

最好直接做报警。

正常情况下这个值：

```text
必须长期 = 0
```

---

# 三十五、建议增加 Execution Invariant Checker

甚至可以做一个后台巡检：

```text
Invariant 1
running run
必须有 active lease

Invariant 2
queued run
不能有 active lease

Invariant 3
terminal run
不能有 active lease

Invariant 4
scheduled Run
必须对应 Occurrence

Invariant 5
Occurrence queued/running
必须对应 Run

Invariant 6
published=false Outbox
年龄不得超过阈值
```

这对于以后生产故障定位非常有价值。

---

# 三十六、哪些地方不要改

以下部分我建议不要因为这次审查再重构：

### 不要改掉 Go

现在不值得回 Django。

### 不要改掉 BINARY(16)

问题是 serialization，不是数据类型。

### 不要放弃 Outbox

Outbox 设计是对的。

### 不要取消 Redis Streams + DB fallback

两者角色分工合理。

### 不要让 Redis 成为 Run source of truth

TiDB 保持 canonical。

### 不要把 Aily 特性写回 Application

继续通过 RuntimeAdapter。

### 不要重新把 Agent 和 Application 合并

现在的解耦是正确路线。

### 不要把 Desktop/Mobile 再合成一套“自适应 CSS 页面”

当前双 Shell 正确。

### 不要因为 Schedule 方便而直接在 API 进程启动 goroutine cron

继续保持独立 Scheduler。

---

# 三十七、当前最明显的架构偏离分类

## 必须纠偏

### 1. 普通登录流程

当前：

```text
账号密码 / 飞书 / 企业 SSO / 注册
```

目标：

```text
普通用户 = Feishu SSO Only
管理员 = /login/admin
```

### 2. Lease Ownership

模型里有 lease token，但执行语义没有真正使用 fencing。

这不是简单实现缺陷，属于：

> 架构原则只实现了一半。

### 3. Misfire fire_once

领域字段已经存在，但策略执行语义没有实现完整。

---

## 可以暂时接受

### Go 替代 Django

可接受。

### Legacy Agent/Workflow Engine 仍存在

迁移期可接受。

### `/workflow-runs/:id` 尚未完全进入 WorkspaceHost

暂时可接受。

### Enterprise SSO 模块保留

如果未来准备产品通用化，可以保留代码，只是不应该出现在当前普通 Feishu 用户入口。

---

# 三十八、建议未来 3 个迭代

## Iteration A — Execution Correctness

只做：

```text
UUID boundary
Claim+Lease transaction
fencing
heartbeat cancel
chaos tests
```

完成后：

> 执行内核才真正达到 production-safe 的基础线。

---

## Iteration B — Scheduler Correctness

做：

```text
fire_once
catch_up limit
run-now queue
schedule admission
misfire tests
delivery failure tests
```

完成后：

> Scheduler 才适合承担企业正式定时任务。

---

## Iteration C — Product Architecture Convergence

做：

```text
自动 Feishu SSO
/login/admin
隐藏普通密码注册
Legacy workflow → RuntimeAdapter
delta coalescing
streaming attachments
observability
```

完成之后整体架构成熟度大约可以从目前：

```text
70% production ready
```

提高到：

```text
90%+
```

---

# 三十九、最终评价

如果只问：

> “有没有按照我们之前完整架构实施？”

答案是：

**有，而且整体实施得比较完整。**

不是只做了数据库表和目录命名，而是真的已经落实到：

```text
Application
Runtime
Provider
Run
RunEvent
Outbox
Lease
Worker
Scheduler
Occurrence
Delivery
Identity
Aily Adapter
WorkspaceHost
Desktop/Mobile Shell
```

这些核心执行语义上。

项目现在最大的风险也已经不是：

> “架构设计错了。”

而是更高级阶段才会遇到的：

> **分布式执行正确性。**

目前我认为最需要警惕的不是 UI，也不是 Go/Django，也不是 Aily API，而是：

```text
            Lease Ownership
                   │
       ┌───────────┼───────────┐
       │           │           │
 Claim+Lease    Fencing    Lease Lost Cancel
  原子性          │           │
       └───────────┼───────────┘
                   │
            Exactly-one owner
```

严格来说我们不追求：

```text
Exactly Once Execution
```

因为 Aily 等远程 Provider 无法保证。

真正应该追求的是：

> **At-least-once execution + idempotent reconciliation + exactly-one canonical writer via fencing。**

只要这条建立起来，整个 Runtime Portal 的执行内核就真正站稳了。

---

# 四十、最终结论

当前版本我会定义为：

> **“架构主体已经落地，进入执行正确性加固阶段。”**

不是：

> “还需要重新设计。”

也不是：

> “已经完全可以放心上线。”

我的建议是：

```text
不要继续大量增加新功能
          ↓
先修 3 个 P0
          ↓
补 5 组故障测试
          ↓
修 Scheduler 语义
          ↓
纠正 Feishu 登录入口
          ↓
再继续扩 Provider / Workflow / Application
```

其中最先动的代码范围应集中在：

```text
internal/execution/outbox.go
internal/execution/worker.go
internal/execution/service.go
RunLease SQL
Run CAS SQL
execution integration tests
```

而不是再次重构：

```text
Application
Catalog
Aily Adapter
WorkspaceHost
Mobile/Desktop Shell
```

后面这些目前总体上是对的。

**因此，本轮审查给出的核心结论是：总体架构不用推翻；先把 Execution Plane 从“能恢复”提升成“可证明不会被 stale worker 写坏”，再进入正式生产阶段。**