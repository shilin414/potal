# potal 第十轮 Batch 4.1.1 整改变更报告（Hub Initial Timer Race Closure）

## 1. 基线与范围

```text
Repository   shilin414/potal
Branch       dev
复审基线     05e74c4（Batch 4.1 复审：仅新增 docs 报告，生产代码基线仍是 f360fac）
生产代码基线 f360facf84cf020fb3b44156edc530df380082c3
本批主题     Batch 4.1.1 — Hub Initial Timer Race Closure
```

按复审报告执行：关闭 1 项 **NEW P1**（初始 Idle Timer 未同步访问）与 1 项 **NEW P2**
（canonical gap repair 应显式校验权威日志连续性），并把复审 §19 指出的 AC-4.1-5 措辞修正落到
Batch 4.1 报告里。

改动全部落在 SSE Gateway / Hub 内部：

```text
backend-go/internal/transport/sse/hub.go           P1 实现
backend-go/internal/transport/sse/hub_test.go      P1 测试（4 个）
backend-go/internal/transport/sse/sse.go           P2 实现
backend-go/internal/transport/sse/sse_hub_test.go  P2 测试（2 个）
backend-go/scripts/falsify_review10_patch411.sh    Mutation L / M
```

**不动**：frontend production code、DB schema、migration、OpenAPI、`stream_protocol`、
event allocator、Provider 容量 / ProviderSlots、ownership / lease、执行内核。

---

## 2. NEW P1 — 初始 Idle Timer 的未同步访问

### 2.1 问题

Batch 4.1 为消除 `manager.mu → hub.mu` 嵌套，把初始 idle timer 留在了 `newRunHub` 内，并且
**故意不取 `hub.mu`**，理由是"Hub 还没进注册表，别的 goroutine 够不到它，那把锁只会是无竞争
的空转"。这个理由有两处错：

```text
1. armIdleTimerLocked 不是纯字段赋值
   → 它调用 time.AfterFunc，callback = evictIfIdle
   → evictIfIdle 会 h.mu.Lock() 后清 h.idleTimer、置 h.closed
   ⇒ 构造函数 goroutine 与 timer goroutine 同时访问 idleTimer / closed

2. SSE_HUB_IDLE_TTL 只校验 > 0，没有下限
   → 1ns / 1us / 1ms 全是合法配置
   ⇒ callback 完全可能在 h.idleTimer = ... 赋值完成之前就跑起来
```

合法时序（复审 §9 原文）：

```text
G1 GetOrCreate()          lock manager.mu
G1 newRunHub()
G1 time.AfterFunc(1ns, evictIfIdle)
                          └──────→ G2 callback 启动
G2                        lock hub.mu
G2                        idleTimer = nil ; closed = true ; cancel()
G2                        removeIfSame() → 等 manager.mu
G1 AfterFunc 返回
G1 idleTimer = timer       ← 与 G2 的 idleTimer=nil 无同步保护
G1 m.hubs[runID] = hub ; unlock manager.mu
G2 removeIfSame()          ← 把刚插入注册表的 Hub 删掉
```

后果不只是 data race，还有：**GetOrCreate 会短暂（甚至持续）发布一个已 closed / 已 cancel 的
Hub**，`runUpstream()` 可能拿到已取消的 context。

### 2.2 为什么不靠"把 IdleTTL 下限调大"修

那不是修复，只是把复现概率压低。同步关系的建立与 IdleTTL 取值无关，任何正 duration 都是合法
配置，所以正确解必须让**所有 Hub 可变生命周期状态都由 `hub.mu` 保护**，同时保持
`manager.mu` 与 `hub.mu` 不嵌套。

### 2.3 修复：新不变量「Unpublished RunHub is inert」

```text
尚未放入 HubManager registry 的 RunHub，不得启动 timer / goroutine / callback。
```

**`newRunHub`（AC-4.1.1-1）**：只分配结构 + 建 context，删掉 `hub.armIdleTimerLocked()`。
构造阶段 `timer == nil`、`goroutine == 0`、`callback == 0`。

**新增 `armInitialIdleTimer()`（AC-4.1.1-1/4/5）**：publish 之后才 arm，内部取 `hub.mu`，
发现 `closed` 或 `subscribers != 0` 就直接 return。

**`GetOrCreate` 创建分支（AC-4.1.1-3）**：

```text
m.mu: lookup / insert（只做 map 操作）
m.mu unlock
go hub.runUpstream()        ← Hub 已 publish
hub.armInitialIdleTimer()   ← 仍然不在 manager.mu 内
return hub
```

先起 upstream 再 arm timer：不是 correctness 必需（两种顺序都安全），但让 eviction 路径永远
不会和"它正准备取消的那次 SUBSCRIBE"抢。

**`armIdleTimerLocked` / `stopIdleTimerLocked`（AC-4.1.1-2）**：`Locked` 后缀恢复成真正的契约
——调用者必须已持 `hub.mu`。项目内全部 5 个调用点（`Subscribe`、`removeSubscriber`、
`failUpstream`、`shutdown`、`armInitialIdleTimer`）逐一核对，均已在 `hub.mu` 下。

### 2.4 并发结果（只有两种合法顺序，都安全）

```text
情况 A：armInitialIdleTimer 先拿 hub.mu → arm timer → Subscribe 拿 hub.mu → stop timer →
        加入 subscriber

情况 B：Subscribe 先拿 hub.mu → 加入 subscriber → armInitialIdleTimer → 见 subscribers > 0 →
        不 arm
```

不再依赖 `IdleTTL 足够大` 来"碰巧躲开 race"。

---

## 3. NEW P2 — canonical gap repair 显式校验日志连续性

### 3.1 问题

`repairDurableGap` 原先假定 MySQL 返回的一定是 `101 102 103`，不校验 `101 103` 这种形状：

```go
if ev.Sequence <= last { continue }
if ev.Sequence >  through { break }
writeFrame(...)                      // ← 中间的洞被直接跨过去
```

生产代码里这个假设当前成立（`SELECT next_event_sequence FOR UPDATE` → `UPDATE +1` →
`INSERT` 全在同一事务，migration 0023 已把 allocator 定义为 gap-free），**所以它不是当前可由
正常 writer 制造的 P1**。但 `repairDurableGap` 承担的是"发现不可信 live ordering → 回权威日志
→ fail closed"，那它本身就该验证权威日志真的连续 —— 否则"日志是权威"是假设而不是保证。

### 3.2 修复

在 `writeFrame` 前加连续性断言（AC-4.1.1-7/8）：

```go
if ev.Sequence > through { break }

if ev.Sequence != last+1 {
    // 日志本身有洞：洞之前已证明连续的前缀保留，洞及其之后的帧一律不发
    g.countLiveGapRepair(telemetry.LiveGapFailed)
    return last, false, false
}

if !writeFrame(...) { return last, false, false }
last = ev.Sequence
```

**已经安全输出的连续前缀不回滚**（SSE frame 无法撤回）：

```text
DB: 101 103
→ 允许发送 101
→ 发现 expected 102 / observed 103
→ 不得发送 103，立即结束连接
→ 客户端带实际收到的最高连续 cursor（101）重连
```

### 3.3 AC-4.1-5 措辞修正（复审 §19）

Batch 4.1 报告原文写的是"补齐失败 → 什么都不写 → 直接结束连接"。该描述只对**第一笔 DB read
就失败**成立。已按其 §19 修正 Batch 4.1 报告 §2.2：

```text
Gap repair 失败时：
绝不发送第一条未经连续性证明的 durable frame，也绝不发送其后的任何 frame。
已经成功验证并发送的连续前缀允许保留，
客户端随后从其实际收到的最高连续 cursor 重连。
```

---

## 4. 测试

### 4.1 P1（`hub_test.go`）

```text
TestNewRunHubIsInertBeforePublication          AC-4.1.1-1
TestGetOrCreateArmsInitialIdleTimer            AC-4.1.1-4
TestSubscriberStopsArmedInitialIdleTimer       AC-4.1.1-5（交错 A）
TestTinyInitialIdleTTLIsRaceSafe               AC-4.1.1-6
```

> **名字已于 Batch 4.1.2 修正**：本批原名为 `TestConcurrentSubscriberCancelsInitialIdleTimer`，
> 其注释声称"subscriber 严格早于 arm"，但 `<-created` 返回时 `armInitialIdleTimer()` 已经执行完，
> 代码建立的是"timer 已 arm → Subscribe → stop timer"（交错 A）。AC-4.1.1-5 的另一半
> （交错 B：subscriber 先到 → 不 arm）由 Batch 4.1.2 的
> `TestArmInitialIdleTimerSkipsAlreadySubscribedHub` 确定性覆盖，详见 §10。

* **Inert**：两半。长 TTL（1h）下"constructor 里 arm 了 timer"是可直接观测的非 nil；短 TTL
  （1ms）下等 50 个 IdleTTL 窗口后 `closed` 仍必须是 false —— 后半段就是**旧代码会失败的
  那一半**（旧代码里 callback 会把 `closed` 置真）。
* **Arms**：`GetOrCreate` 后 Hub 无人认领，断言"立刻已 armed"（证明 timer 没被搬丢）→
  `IdleTTL` 后 `HubCount == 0`（AC-4.1.1-4）。
* **Stops-armed-timer**：40 轮，每轮在 Hub **刚可见于注册表**时立刻 Subscribe（该时刻早于
  arm，但测试自身要等 `GetOrCreate` 返回，因此真实时序是"arm 已发生 → Subscribe 停表"），
  然后挂住 2 × IdleTTL，断言 Hub 仍在、`Subscriber.DropReason() == ""`。
* **Tiny TTL**：`IdleTTL = 1ns`，60 轮并发 GetOrCreate / Subscribe / Close，制造跨代际
  （1ns 会让 Hub 一 publish 就被回收，下一轮重建）与 arm/stop/arm 交错。功能断言刻意很弱
  （不 panic、注册表能排空、Close 正常返回）——**真正的裁决交给 race detector**。

### 4.2 P2（`sse_hub_test.go`）

```text
TestGatewayFailsClosedOnCanonicalLogSequenceHole  AC-4.1.1-7/8
TestGatewayRepairsGapFromContiguousLog            反向对照
```

* **Hole**：`from=100`、`through=103`，日志 `101 103`。断言只交付 `[101]`、103 一次都没有、
  `live_gap_repair_total{result="failed"}`、
  `studio_sse_replay_events_total == 1`（证明 repair 止步于洞，而不是越过它继续读）。
* **对照**：同样形状（cursor 100、live 103 到达）但日志 gap-free `101 102 103`，必须正常交付
  `[101 102 103]` 且 `result="repaired"`。**没有这个对照，"repair 拒绝一切"也能满足洞测试**，
  那会把 4.1 的 live-gap 修复本身破坏掉。

删除或放宽：无。Batch 4 / 4.1 既有测试一字未改。

---

## 5. Falsification

新增 `backend-go/scripts/falsify_review10_patch411.sh`（复审 §24 的 Mutation L / M）：

```text
L  把初始 idle timer 放回 newRunHub constructor（不带 hub.mu、publish 之前）
   → TestNewRunHubIsInertBeforePublication FAIL

M  删除 repairDurableGap 中的 ev.Sequence == last+1 检查
   → TestGatewayFailsClosedOnCanonicalLogSequenceHole FAIL
```

Mutation L 的功能半边只能证明"constructor 的 timer 可被观测"，真正阻止
`callback ↔ idleTimer assignment` 回归的是 CI 的 `-race` 门 + `TestTinyInitialIdleTTLIsRaceSafe`。
本机 `CGO_ENABLED=0`、无 `gcc`（实测 `CGO_ENABLED=1 go test -race` 报
`cgo: C compiler "gcc" not found`），该门仍由 CI 兜住。

实测结果：

```text
=== L. §24: the initial idle timer is armed inside the constructor again ===
  [ok]   an unpublished hub is inert (mutated): observed FAIL (expected FAIL)
  [ok]   an unpublished hub is inert (restored): observed PASS (expected PASS)
=== M. §24: the repair stops verifying that the log is contiguous ===
  [ok]   a hole in the canonical log fails closed (mutated): observed FAIL (expected FAIL)
  [ok]   a hole in the canonical log fails closed (restored): observed PASS (expected PASS)
=== control: the gap repair still repairs a gap-free log ===
  [ok]   a gap-free log is still repaired end to end: observed PASS (expected PASS)

falsifications ok=5 bad=0
```

旧驱动**回归重跑，锚点全部仍然匹配**（改代码后必须复核，失配会终止驱动）：

```text
scripts/falsify_review10.sh            ok=14 bad=0   (Mutation A–G)
scripts/falsify_review10_patch41.sh    ok= 8 bad=0   (Mutation H–K)
```

---

## 6. 本机验证

```text
gofmt -l internal tests cmd                        empty
go build ./...                                     PASS
go vet ./...                                       PASS
go test ./... -count=1                             PASS（全量单测，无 FAIL）
go test ./internal/transport/... -count=1          PASS（http / sse）
go test ./internal/transport/sse -list            63 tests（4.1 基线 57，本批 +6）
go test ./tests/integration/... -count=1           PASS  126.96s（STUDIO_TEST_DB/REDIS=1；204 tests，其中 SSE 21）
falsify_review10_patch411.sh                       5/5
falsify_review10.sh                                14/14
falsify_review10_patch41.sh                        8/8

frontend npx tsc --noEmit                          PASS
frontend npx vitest run                            158 tests / 18 files PASS
frontend npx vite build                            PASS
```

本批未改 frontend production code，前端三门按复审 §25 照跑（不放宽原门禁）。

`-race`：本机不可执行（无 gcc），由 CI 门覆盖 `./internal/transport/sse/...`。

---

## 7. 新增长期约束

Batch 4.1.1 完成后，以下两条写入长期约束（复审 §23）：

```text
1. Unpublished RunHub is inert:
   constructor 不启动 goroutine / timer / callback。

2. armIdleTimerLocked / stopIdleTimerLocked（以及 idleTimer 字段本身）
   只允许在持有 hub.mu 时访问。
```

这两条比"`newRunHub` 现在为什么没有 lock"更稳定，也不容易被未来重构重新打破 —— 后者是历史
解释，前者是可校验不变量。

---

## 8. 验收对照

```text
AC-4.1.1-1  newRunHub constructor 不启动 timer/goroutine/callback                PASS
AC-4.1.1-2  所有 idleTimer 读写都发生在 hub.mu 下（5 个调用点逐一核对）            PASS
AC-4.1.1-3  manager.mu 与 hub.mu 继续保持不嵌套                                  PASS
AC-4.1.1-4  无 subscriber 的新 Hub 仍会在 IdleTTL 后回收                          PASS
AC-4.1.1-5  并发 subscriber 能可靠 stop initial idle timer                        PASS
AC-4.1.1-6  合法的极短 IdleTTL（1ns）在 -race 下无 data race                      CI PASS
AC-4.1.1-7  canonical gap repair 只有 ev.Sequence == last+1 才允许发送            PASS
AC-4.1.1-8  canonical log 出现 hole 时，不发送 hole 后的 durable frame            PASS
AC-4.1.1-9  Batch 4 / 4.1 原全部测试继续 PASS                                     PASS
AC-4.1.1-10 backend unit / integration / race + frontend CI 全部 PASS             PASS
```

### CI 证据（`95e0a60`，backend run 35074005255）

```text
check        actionlint / gofmt check / go vet / go build                success
             go test (unit; integration gated by env)                    success
             go test -race (execution plane + SSE hub)                   success   ← AC-4.1.1-6
integration  apply migrations / 二次运行必须是干净 no-op                   success
             database-backed package tests (real MySQL/Redis)            success
             integration tests                                           success

frontend     run 35069505412（f360fac）tsc / vitest / vite build          success
             本批无 frontend 路径改动，workflow 未为 95e0a60 触发；
             同一工作区本地实测 tsc / 158 tests / vite build 全绿
```

race 覆盖 `./internal/transport/sse/...`，也就是本批改动的包；本机无法跑 `-race`，该门由 CI 兜住。
`TestTinyInitialIdleTTLIsRaceSafe`（`IdleTTL = 1ns`、60 轮跨代际）就在这组里跑，这是阻止
`callback ↔ idleTimer assignment` 回归的真正门。

---

## 9. Freeze 判定

```text
SSE Hub Architecture             PASS
Single-Run Upstream Fanout       PASS
Subscriber Protocol Isolation    PASS
Replay/Live Catch-up             PASS
Durable Live Ordering            PASS
Slow Subscriber Isolation        PASS
Bounded Replay Cache             PASS
Hub Generation Safety            PASS
Hub Lock Non-Nesting             PASS
Initial Idle Timer Race Safety   PASS
```

```text
Batch 4 + 4.1 + 4.1.1
FROZEN（Batch 4.1 报告的 FROZEN 判定自本批起恢复有效）
```

**下一步：Batch 5 — Worker Dispatcher。**

本轮严格按复审 §27 收口，未继续扩大 SSE 修改范围。

---

# 10. Batch 4.1.2 — Initial Arm Interleaving Test Closure（test-only）

复审报告《potal 第十轮 Batch 4.1.1 最新代码复审暨 Batch 4.1.2 测试加固执行报告》结论为
P0 0 / P1 0 / P2 1：生产实现正确，缺的是 **AC-4.1.1-5 另一半的确定性测试证据**。本批只补测试
与反证，**不重新修改 Hub 生产实现**。

## 10.1 复审判定（已复核确认）

```text
Initial Idle Timer runtime fix      PASS
manager.mu / hub.mu non-nesting     PASS
canonical-log continuity            PASS
SSE Hub race CI                     PASS
AC-4.1.1-5 deterministic test       INCOMPLETE  → 本批关闭
```

## 10.2 改动范围（未触碰任何生产文件）

```text
backend-go/internal/transport/sse/hub_test.go                        新增 1 测试 + 改名 1 测试 + 修 1 既有 flake（§10.7）
backend-go/scripts/falsify_review10_patch411.sh                      新增 Mutation N
docs/potal 第十轮 Batch 4.1.1 整改变更报告（…）.md                    本文件 §4.1 纠名 + 本节
```

`hub.go` / `sse.go` / `hub_cache.go` / `hub_subscription.go` / frontend / DB / migration /
OpenAPI / Provider / Execution Kernel **均未修改**（AC-4.1.2-1）。

## 10.3 原测试改名 + 注释纠偏

```text
TestConcurrentSubscriberCancelsInitialIdleTimer
  → TestSubscriberStopsArmedInitialIdleTimer
```

原因（复审 §5/§11）：`<-created` 只有在 `GetOrCreate` **完整返回后**才收到值，而返回顺序是

```text
registry insert → unlock manager.mu → go runUpstream → armInitialIdleTimer() → return hub
```

因此 `<-created` 返回时 arm 已经完成，该测试真实建立的是

```text
arm timer → GetOrCreate return → Subscribe → stop already-armed timer     （交错 A）
```

而不是注释声称的"Subscribe 严格早于 arm"（交错 B）。测试本身仍有价值（锁正确前提下的 stop
可靠性，以及集成层的"新发布窗口内到达的 subscriber 不会丢掉 Hub"），因此**保留、改名、改写
注释**，不再声称它覆盖交错 B。原 Batch 4 / 4.1 / 4.1.1 测试**无删除、无放宽**（AC-4.1.2-9）。

## 10.4 新增确定性测试

```text
TestArmInitialIdleTimerSkipsAlreadySubscribedHub   AC-4.1.2-2/3
```

交错 B 的窗口只有几条指令宽，靠 goroutine 调度撞出来是抽签，所以该测试**直接构造状态**
（同 package：`newRunHub` 与 `manager.hubs` 可达），按 `GetOrCreate` 的方式发布但由调用方 arm：

```text
newRunHub(mgr, runID)
  → mgr.mu 下写入 mgr.hubs[runID]        （= GetOrCreate 的 registry insert，只少 arm 那一步）
  → hub.Subscribe(StreamProtocolRangeDelta)
  → hub.armInitialIdleTimer()            （subscriber 已存在的 arm）
  → 断言 hub.idleTimer == nil            （根因断言）
  → sleep 2×IdleTTL：Hub 必须仍在、DropReason() == ""
  → sub.Close()：必须重新 arm 并回收      （证明跳过 arm 没把 lifecycle 绕掉）
```

**为什么断言 `idleTimer == nil` 而不是 wall-clock retention window**（复审 §14）：已 arm 但尚未
触发的 timer **就是**缺陷本身；读字段与调度、与 `IdleTTL` 取值都无关（AC-4.1.2-3）。而"detach
后重新获得完整 IdleTTL"这类 wall-clock 断言会引入 flake。

`IdleTTL = 250ms` 的选取：足够长，使"被 arm 了"表现为非 nil 字段而不是一次与断言竞争的回收；
又足够短，使 detach 后的回收仍落在 `waitFor` 的 3s 窗口内。

## 10.5 Mutation N

`scripts/falsify_review10_patch411.sh` 新增：

```text
N. armInitialIdleTimer 删掉 `if len(h.subscribers) != 0 { return }`
   → TestArmInitialIdleTimerSkipsAlreadySubscribedHub   MUST FAIL
   → 恢复源码                                          MUST PASS
```

至此 4.1.1 的三条生命周期防线各有可失败反证：

```text
L  constructor 必须 inert
N  subscriber-before-arm 不得 arm timer
M  canonical log hole 必须 fail closed
```

Mutation N **没有 race gate 的第二半**：删掉 guard 不产生未同步访问（字段读写仍在 `hub.mu` 下），
`go test -race` 会继续绿；只有确定性交错测试看得见它。这正是 AC-4.1.2-2..5 存在的理由，也说明
"-race PASS" 不能替代业务不变量测试（复审 §7）。

## 10.6 本地实测证据

```text
go test ./... -count=1                                        all ok（含 sse 4.747s）
go test -run 五个定时器相关测试 -v                             5/5 PASS
    TestArmInitialIdleTimerSkipsAlreadySubscribedHub  0.75s   ← 新增
    TestSubscriberStopsArmedInitialIdleTimer          2.46s   ← 改名后
    TestNewRunHubIsInertBeforePublication             0.05s
    TestGetOrCreateArmsInitialIdleTimer               0.10s
    TestTinyInitialIdleTTLIsRaceSafe                  0.00s
falsify_review10.sh               falsifications ok=14 bad=0
falsify_review10_patch41.sh       falsifications ok=8  bad=0
falsify_review10_patch411.sh      falsifications ok=7  bad=0
    L mutated FAIL / restored PASS
    M mutated FAIL / restored PASS
    N mutated FAIL / restored PASS          ← 新增
    control gap-free log still repaired PASS
    无 FALSIFICATION 残留、无 *.orig 残留
migrate 1st / 2nd                applied（二次运行干净 no-op）
db-backed package tests          execution / delivery / platform 全 ok
tests/integration                ok 128.788s
frontend                         tsc 0 error / vitest 18 files 158 tests PASS / vite build ok
GOMAXPROCS=2 压测 sse 包 ×16      0 失败（修 §10.7 的既有 flake 之后；修前 12 次 1 失败）
GOMAXPROCS=2 压测 go test ./... ×3 0 失败
```

### CI 证据

```text
backend run 35078966146（head 6a90295）  check success / integration success   ← 本批最终绿
backend run 35077631425（head 00d7ef4）  check FAILURE / integration success   ← §10.7 的既有 flake
```

`check` job 覆盖 actionlint / gofmt / `go vet` / `go build` / `go test ./... -count=1`，
其中**本机跑不了的 race 门**（`go test -race`，含 `./internal/transport/sse/...`）也在这个 job 里，
即 `TestArmInitialIdleTimerSkipsAlreadySubscribedHub` 与两个既有定时器测试都真实过了 race detector。
`integration` job 在 mysql5.7 + redis7 上跑 migrate×2 + db-backed 包测试 + `tests/integration`。

前端 workflow 只在 `frontend/**` 变更时触发，本批未改前端，故走本机实测（§10.6）。

`-race` 本机仍无法执行（`CGO_ENABLED=0`、无 gcc），该门由 CI 兜住，覆盖
`./internal/transport/sse/...`。

## 10.7 附带修正：既有 flake 导致的 CI 红

首次推送（`00d7ef4`）的 CI run 35077631425 中，`check` job 在 **step 9 `go test (unit;
integration gated by env)`** 判红，`integration` job 全绿。本机同一条命令全绿，因此必须定位。

在 `GOMAXPROCS=2`（≈ GitHub runner 的 2 vCPU）下压测 sse 包：

```text
12 次运行 → 1 次 FAIL
--- FAIL: TestHubManagerSharesOneUpstreamPerRun
    hub_test.go:332: UpstreamCount = 0, want 1 (upstreams must track RUNS, not connections)
```

`git diff 8168e5c HEAD -- hub_test.go` 中 `TestHubManagerSharesOneUpstreamPerRun` 出现 **0 次**
→ 该测试体与本批改动无关，**这是既有 flake**（上一次 CI run 35074005255 通过只是没有抽到
失败的那一次）。

根因是**测试自身的采样竞态，生产代码正确**：`fakeUpstream.Subscribe` 在返回之前就自增
`subscribes`（`hub_test.go:37`），而 `runUpstream` 是在它之后**再次取 `hub.mu`** 才置
`h.upstreamUp = true`（`hub.go:864-866`）。于是：

```text
waitFor(up.SubscribeCount() == 1)      ← 已满足，退出等待
  ↓  紧接着裸采样一次
mgr.UpstreamCount() 读到 upstreamUp == false  →  FAIL
```

修法：在 §10.2 允许修改的 `hub_test.go` 内，于该断言**之前**插入一条有界等待
`waitFor(t, ..., func() bool { return mgr.UpstreamCount() == 1 })`，**原断言行一字未改**。
这不是"放宽"——upstream 永不变 active 时 `waitFor` 仍会失败，断言本身保持原样，
AC-4.1.2-9「不删除、不放宽」满足。

验证：`GOMAXPROCS=2` 压测该包 **16 次 0 失败**（修前 12 次 1 失败）。

## 10.8 验收对照

```text
AC-4.1.2-1  生产代码 hub.go / sse.go 无行为修改                                PASS
AC-4.1.2-2  确定性测试覆盖「subscriber 已存在 → arm 不得启动 timer」            PASS
AC-4.1.2-3  该测试不依赖 goroutine scheduler 或 1ns timing                     PASS
AC-4.1.2-4  删除 subscriber guard 后新测试必 FAIL（Mutation N）                PASS
AC-4.1.2-5  恢复 subscriber guard 后新测试 PASS                               PASS
AC-4.1.2-6  inert-constructor / tiny-TTL-race / canonical-hole 测试继续 PASS   PASS
AC-4.1.2-7  Mutation L / M 继续有效，Mutation N 有效                           PASS
AC-4.1.2-8  go test / race / integration 全绿（race 由 CI 兜）                 PASS（CI 35078966146）
AC-4.1.2-9  Batch 4 / 4.1 / 4.1.1 原测试未删除、未放宽                          PASS（§10.7 只加等待、原断言未动）
```

## 10.9 Freeze 判定

复审第 17 节已判定运行时正确性无新 P0/P1；本批补齐证据后：

```text
Batch 4 + 4.1 + 4.1.1 + 4.1.2
FROZEN —— SSE Hub 永久冻结，不再扩大 Batch 4 生产功能范围
```

**下一步：Batch 5 — Worker Dispatcher。**
