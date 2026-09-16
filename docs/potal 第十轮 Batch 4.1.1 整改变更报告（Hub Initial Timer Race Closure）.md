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
TestConcurrentSubscriberCancelsInitialIdleTimer AC-4.1.1-5
TestTinyInitialIdleTTLIsRaceSafe               AC-4.1.1-6
```

* **Inert**：两半。长 TTL（1h）下"constructor 里 arm 了 timer"是可直接观测的非 nil；短 TTL
  （1ms）下等 50 个 IdleTTL 窗口后 `closed` 仍必须是 false —— 后半段就是**旧代码会失败的
  那一半**（旧代码里 callback 会把 `closed` 置真）。
* **Arms**：`GetOrCreate` 后 Hub 无人认领，断言"立刻已 armed"（证明 timer 没被搬丢）→
  `IdleTTL` 后 `HubCount == 0`（AC-4.1.1-4）。
* **Concurrent**：40 轮，每轮在 Hub **刚可见于注册表**（严格早于 arm）时立刻 Subscribe，然后
  挂住 2 × IdleTTL，断言 Hub 仍在、`Subscriber.DropReason() == ""`。
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
