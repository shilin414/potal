# potal 第十轮 Batch 4.1 整改变更报告（SSE Hub Correctness Closure）

## 1. 基线与范围

```text
Repository   shilin414/potal
Branch       dev
复审基线     f5fc151029334cd72bbd6b1caf824ff98df426c6
本批提交     c5ac5f2  fix(sse): repair durable live gaps from the canonical event log
             970041e  fix(sse): make replay cache byte bounds strict
             f3cd4bf  fix(sse): close the hub generation and lock-discipline gaps
             521fc4b  test(review10.1): add the SSE Hub closure falsifications
```

按复审报告执行 `Batch 4.1 — SSE Hub Correctness Closure`：两项 P1、两项 P2，全部落在 SSE
Gateway / Hub 内部，**不动**执行内核、Provider 容量、幂等、event allocator、`stream_protocol`
1/2、UTF-8 range 对账、DB schema、OpenAPI。

```text
go build ./...                     PASS
gofmt -l ./internal ./tests        empty
go vet ./...                       PASS
go test ./... -count=1             PASS（全量单测）
go test ./internal/transport/sse   57 tests PASS
go test ./tests/integration TestSSE 20 tests PASS
frontend npx tsc --noEmit          PASS
```

本机 `CGO_ENABLED=0`、无 `gcc`，`-race` 仍只能由 CI 兜（同第九轮记录）。

---

## 2. P1-1 — Durable live event 乱序导致永久丢事件

### 2.1 问题

durable event 的发布发生在 **COMMIT 之后**：

```text
Writer A  allocate seq=101 → COMMIT → （goroutine 被调度出去）
Writer B  allocate seq=102 → COMMIT → Publish 102
Writer A  恢复 → Publish 101
```

Redis pub/sub 保序的是 **publish 顺序**，不是 sequence 顺序，所以 `102 → 101` 完全合法。

而旧 gateway 的做法是"来什么写什么，`sequence <= lastDelivered` 就跳过"：

```go
if ev.Sequence != 0 && ev.Sequence <= lastDelivered { continue }
writeFrame(...)
// deliberately NOT advance lastDelivered
```

这**不是排版问题，是永久内容丢失**：客户端持久化的重连游标是"它真正渲染过的最高
sequence"。它先渲染了 102、随后 101 被当重复丢掉，下次重连就带 `after=102`，101 从此
再也回不来。若这一对是 `101 content.chunk / 102 run.completed`，客户端收到 terminal 后
直接停止读取，101 连到达的机会都没有。

`sequence 单调分配` 只能保证 DB 顺序，不能保证 publish 顺序；`Gateway 不推进
lastDelivered` 也解决不了"terminal 抢先"。

### 2.2 修复

`lastDelivered` 语义收紧为 **"这条连接已经连续写出的最高 durable sequence"**（AC-4.1-6），
live loop 四分支：

```text
sequence == 0                → transient，不碰游标
sequence <= lastDelivered    → 重复 / 迟到发布，丢弃
sequence == lastDelivered+1  → 正常发送，游标前进
sequence >  lastDelivered+1  → FORWARD GAP：拒绝该帧，从 MySQL 补 [last+1 .. sequence]
```

新增 `Gateway.repairDurableGap(ctx, run, hub, from, through, writeFrame)`：

* **为什么 DB 一定有**：`through` 已经被 publish，说明它的事务已 COMMIT，说明它的 sequence
  已经分配；而分配是 run 行上的串行点，所以所有更小的 sequence 在它分配之前就已经提交并释放
  了锁。缺口内的帧此刻必然可读 —— 这不是猜测，是读权威日志。
* **补齐按序写**：逐页 `ListEventPage(after=last)`，只写到 `through`（多出来的留给触发帧自己
  走下一轮，绝不重复发送），并同步 `RememberDurable` 预热 cache（不 fan-out，与 replay 一致）。
* **terminal 仍是硬边界**：补齐过程中遇到 terminal → 写 101 再写 102 → 关闭，绝不 `102 CLOSE`
  （AC-4.1-4）。
* **fail closed（AC-4.1-5）**：读失败 / 日志给不出 `through` → 结束连接，客户端用「最后连续游标」
  重连，走正常 replay 补齐。**重复帧只是观感问题，丢一帧是内容丢失**，所以任何情况下都不把缺口
  透传给客户端。

> **AC-4.1-5 的文字修正（Batch 4.1.1 §19）**：原文"什么都不写"只对**第一笔 DB read 就失败**成立。
> 准确的契约是：
>
> ```text
> Gap repair 失败时：
> 绝不发送第一条未经连续性证明的 durable frame，也绝不发送其后的任何 frame。
> 已经成功验证并发送的连续前缀允许保留，
> 客户端随后从其实际收到的最高连续 cursor 重连。
> ```
>
> 已写出的 SSE frame 无法撤回，要求回滚是做不到的；真正的保证是"从第一帧未被证明连续的
> durable frame 起，一帧都不再发"。

第 31 条要求也对上了：补齐走的就是 MySQL replay 路径，帧数继续计入
`studio_sse_replay_events_total`；新增

```text
studio_sse_live_gap_repair_total{result="repaired|failed"}
```

回答的是"**为什么会多一次 replay**"。`result` 是封闭二值枚举；run_id / sequence / error 只进日志，
绝不做 label。

### 2.3 证据

```text
TestGatewayRepairsOutOfOrderDurableLiveEvent   DB 有 101,102；Redis 到货顺序 102,101
                                               → 客户端恰好收到 [101 102]，各一次
TestGatewayRepairsGapBeforeTerminal            Redis 只送 run.completed(102)
                                               → 输出 [101 102] 后 EOF，不是 [102] 后 EOF
TestGatewayFailsClosedWhenGapRepairFails       补齐读失败 → 一帧都不写、连接结束、failed 计数 +1
TestSSEHubRepairsDurableRedisReorder (集成)    真实 MySQL + Redis：先写 101/102 行，
                                               再逆序 publish → 客户端 [101 102]，游标 = 102
```

---

## 3. P1-2 — `CacheMaxBytes` 不是硬上界

### 3.1 问题

`evictLocked` 保留"至少留下最新一条"：

```go
if live <= 1 { break }
```

于是**单个事件比整个预算还大也照样常驻**，并且旧测试把它写成了契约
（`"newest event always survives"`：`maxBytes=10` + `4096B` 事件 → 仍缓存 4096B）。
这不是纸面问题：terminal 事件会把 `output["text"]` 整段放进 `payload.text`，即
`run.completed > 8 MiB` 在结构上完全可能；N 个 hub 再乘上去，`SSE_HUB_CACHE_BYTES=8 MiB`
就只是一句注释。

### 3.2 修复

**超预算的 durable event 不缓存 —— 不截断、不豁免**：

```text
event.ApproxBytes > CacheMaxBytes
→ 该段 retained segment 全部清空
→ 保留 lastObservedSeq（ring 已经越过这个位置）
→ 该 cursor 及其以下一律 cache miss → MySQL replay
```

cache 是优化，MySQL 才是事实源：**宁可 cache miss，也不突破内存安全界限**（AC-4.1-1/2）。
由此 `evictLocked` 可以真的把 ring 抽空（被保留的每一条本身都在预算内，所以一定能压回界内），
`After()` 对空 ring 一律返回 miss —— 这是这笔交易里便宜的那一边。

同时把"ring 走到哪"拆成两个概念（§19）：

```text
lastObservedSeq  曾经被喂到过的最高 sequence
lastSeq          当前真正 retained segment 的最高 sequence
```

少了这个区分，"reset 段之后 lastSeq 归零"会让一条迟到的旧帧把 cache 倒着接回去。coverage
行为与 §21 完全一致：1–4 命中、5 超限、6–7 命中 → `after=4` MISS（5 只在 MySQL），
`after=5` HIT `[6 7]`。

### 3.3 证据

```text
TestDurableRingRejectsSingleOversizedEvent      maxBytes=10 / 4096B → Len=0 Bytes=0 After(0) 不覆盖
TestDurableRingOversizedEventBreaksCoverage     after=4 → covered=false
TestDurableRingResumesSegmentAfterOversizedBarrier  after=4 MISS / after=5 → [6 7]；迟到 3 不复活
TestDurableRingBytesNeverExceedConfiguredBound  混合负载下每一条 Remember 之后 Bytes() <= maxBytes
TestGatewayDeliversOversizedTerminalWithoutCachingIt  大 terminal：订阅者收到完整帧、
                                                      hub cache 0 字节、重连从日志拿到完整帧
TestSSEHubOversizedTerminalIsDeliveredButNotCached (集成) 同上，真实 hub cache 与真实字节上界
```

"不缓存 ≠ 不发送 ≠ 丢失"这三件事是被**一起**断言的：只测上界的实现完全可以靠截断或干脆不发
来通过。

---

## 4. P2-1 — 旧 Hub 可以重置新代际的 cache 指标

`removeIfSame` 有指针比较，`reportCache` 没有。竞态窗口：

```text
OldHub dispatch → 释放 hub.mu → 尚未 reportCache
idle timer → OldHub removeIfSame → cacheStats[run] 删除
NewHub 接管同一 runID
OldHub 继续 → reportCache(OLD_STATS) → 幽灵缓存
```

结果是 `hubs_active=0` 却 `cache_bytes>0`；对一个已经结束的 run，不会有新 durable 事件来纠正它。
SSE 数据没问题，**从指标做出的每个判断都有问题**（canary / Grafana / 排障）。

修复：`reportCache` 接收 hub 并在同一把锁内做同样的 identity 校验（AC-4.1-7），"谁拥有这个
run"只剩一个定义。

## 5. P2-2 — `manager.mu` 与 `hub.mu` 的唯一一处嵌套

文件里写着两把锁互不嵌套，但 `GetOrCreate` 在持 `manager.mu` 时调用了 `hub.Serving()`（取
`hub.mu`）。今天没有反向路径所以不是活锁，但整套生命周期就是按"两把锁不嵌套"设计的，留一处例外
足以让将来某个 `hub.mu → manager.mu` 闭合环。

`GetOrCreate` 改为 loop：`manager.mu` 内只做查找/插入，**释放后**才问 `hub.Serving()`；不可服务
则 `removeIfSame`（指针比较）后进入下一轮，被并发调用者新建的 hub 会被下一轮的查找直接复用。
`newRunHub` 也不再为 arm idle timer 取 `hub.mu`（此时 hub 还没进注册表，没人能拿到它），否则等于
从另一个方向重新制造同一处嵌套。

证据：`TestGetOrCreateSweepsAnUnusableHubInPlace` 复现 `failUpstream` 留下的窗口（hub 已在
`hub.mu` 下标记不可用、注册表尚未清扫），断言换新代际、该 run 只有一个 hub、被替换代际的
cache 统计不残留。

---

## 6. 集成测试契约同步（本批真正的"发现"）

新契约让**手造的 durable 帧变成非法输入**：gateway 现在会把「日志拿不出对应 sequence 的 durable
帧」当成无法修复的有序性缺口，并（正确地）结束连接。而既有集成测试大量依赖这类帧：

```text
syncStream 用 sequence 9000 的 durable 帧当"订阅已建立"探针（MySQL 里没有它）
review9_patch33 用 9001 / 100 / 101 / 9002 发布 durable chunk / terminal
review10       用 9100 / 9200 发布 terminal / chunk
```

这不是测试琐事，而是**契约变更的必然结果**，所以按契约修：

* `syncStream` 的探针改成 **transient 帧**（sequence 0，`run.started`）。liveness 探针本来就该是
  这个形状：不携带位置、不进 cache、不推游标，且对 protocol 1 客户端同样可达（只有
  `content.delta` 会被能力过滤）。
* durable 帧一律经真实路径落库：新增 `appendLiveEvent`（`AppendEvent` 之后读回分配的 sequence）
  与 `runEventTail`。
* `publishTransientFrame` 对非 0 sequence 直接 `t.Fatal`，把"不能手造 durable 帧"变成结构性约束
  而不是注释。
* 需要「publish 顺序 ≠ commit 顺序」的乱序用例，用 `seedDurableEvent` 显式插入行（并同步推进
  `runs.next_event_sequence`，保持 allocator 自己的不变量），再由 `publishDurableFrame` 手工发帧。
  这是**忠实建模**而不是绕过系统：生产里发布本就在 commit 之后，`AppendEvent` 无法复现两者分离，
  这也正是补齐路径存在的理由。
* `TestSSECursorSemanticsIgnoreStreamProtocol` 顺带变强了：游标换成"下一个事件将要取得的
  sequence"，于是"游标处不重发 / 游标之上必达"两条都由**真实事件**驱动，且"游标处"那条真的走
  了 live 去重路径。
* 前端只加架构注释（`frontend/src/services/runStream.ts`，无行为改动）：说明
  "highest sequence seen 是安全游标"**依赖服务端连续保证**，以及若该保证将来弱化，前端需要补的
  防御（`sequence > last + 1` 主动断流重连）。

---

## 7. 反证

```text
scripts/falsify_review10.sh         7 mutation / 14 checks   ok=14 bad=0
scripts/falsify_review10_patch41.sh 4 mutation /  8 checks   ok=8  bad=0
```

新增驱动：

```text
H  删掉 forward gap repair，恢复"直接写 seq102"
   → TestGatewayRepairsOutOfOrderDurableLiveEvent / TestGatewayRepairsGapBeforeTerminal FAIL
I  补齐失败后仍发送原 Redis 帧
   → TestGatewayFailsClosedWhenGapRepairFails FAIL
J  回到 4.1 之前的 cache 规则（超限事件照样留 + eviction 停在一帧）
   → TestDurableRingRejectsSingleOversizedEvent / TestDurableRingBytesNeverExceedConfiguredBound FAIL
K  删掉 reportCache 的代际校验
   → TestStaleHubCannotRestoreCacheMetricsAfterEviction /
     TestOldHubCannotOverwriteNewHubCacheMetrics FAIL
```

Mutation J 一次改两处，理由与 Batch 4 的 Mutation G 相同：字节上界是被两层同时保证的（append 前
拒绝超限事件、eviction 允许清空 ring），任何一半单独都能守住界；"最新一条永远存活"才是那一个缺陷。

Batch 4 驱动里 A / F 的锚点随被改代码位移（`GetOrCreate` 变成 loop；gap reset 变成
`resetSegmentLocked` 且拆出 observed high-water），已同步更新 —— 锚点失配会让驱动在断言前终止，
而这正是驱动坚持按源码文本断言的意义。

两个驱动跑完 `FALSIFICATION` 标记与 `*.orig` 均为空。

---

## 8. 真实陷阱（本机/环境，供后续批次复用）

1. **集成测试里手造的 durable 帧是契约负债**。它们看起来"只是测试"，实际编码了一个默认假设：
   gateway 会转发任何 durable 帧。任何收紧 durable 顺序语义的改动都会同时打断它们 —— 先修
   fixture，再谈新断言。
2. **本机 `core.autocrlf=true` 与本仓库 LF 索引相冲**。git 一旦重写文件（checkout / reset），
   工作区变成 CRLF、`gofmt -l` 立刻全文件报警（索引里仍是 LF，CI 不受影响）。本批已把该仓库
   局部设为 `core.autocrlf=false` 并把 24 个文件规范回 LF；否则本地 gofmt 校验不可信。
3. **`git checkout <old-sha>` 会被 SIGTERM 打断并留下半成品工作区**（本次实测：文件被删、
   `.git/index.lock` 残留，HEAD 未移动）。恢复顺序：`rm .git/index.lock` → `git reset --hard HEAD`，
   之后把未提交的改动从备份恢复。**对照基线不要 checkout**，用 `git checkout <sha> -- <具体路径>`
   或 worktree。
4. **两个集成用例在本机是负载敏感的 flake**：`TestRetryRunAndOutboxShareRetryAt` /
   `TestReaperRequeueIsImmediatelyClaimableAndInSync` 要求 `runs.available_at` 与
   `outbox_events.available_at` 相差 ≤ 50ms，而两者是两次独立写。本机 A/B 交替实测（各 2 次）：

   ```text
   baseline  run1 FAIL(63ms / 84ms)   head run1 PASS
   baseline  run2 FAIL(60ms, reaper)  head run2 FAIL(84ms, reaper)
   ```

   两个变体同样失败 → **与 Batch 4.1 无关的环境级 flake**，全量集跑一次里
   `TestTickAndRunNowConcurrent` 也是同样的（隔离跑 PASS）。

---

## 9. 验收对照

```text
AC-4.1-1  DurableRing.Bytes() <= CacheMaxBytes 恒成立（含单事件超界）        PASS
AC-4.1-2  超限 durable event 不缓存 / 不截断 / 不丢失（MySQL fallback）      PASS
AC-4.1-3  Redis 102 → 101，客户端最终 [101 102] exactly once               PASS
AC-4.1-4  terminal 乱序：101 → 102 terminal → EOF                          PASS
AC-4.1-5  补齐失败 fail closed，不透传缺口                                  PASS
AC-4.1-6  lastDelivered 只代表最高"连续"durable sequence                     PASS
AC-4.1-7  旧 Hub 无法在回收后写回 cacheStats，也无法覆盖新代际              PASS
AC-4.1-8  manager.mu 与 hub.mu 不再嵌套                                     PASS
AC-4.1-9  Batch 4 原矩阵（单 upstream / 协议隔离 / 慢客户端隔离 /
          catch-up barrier / terminal 硬边界 / idle 生命周期）继续 green     PASS
AC-4.1-10 CI：unit / integration / race / frontend                          PASS
```

未删除或放宽任何 Batch 4 既有测试；唯一被替换的语义是 §22 指定的
`"newest event always survives"`。

### CI 证据（`f360fac`，run 35069505453）

```text
check        actionlint / gofmt check / go vet / go build
             go test (unit; integration gated by env)
             go test -race (execution plane + SSE hub)          all success
integration  apply migrations / 二次运行必须是干净 no-op
             database-backed package tests (real MySQL/Redis)
             integration tests                                   all success
frontend     tsc / vitest / vite build                          success
```

race 覆盖 `./internal/transport/sse/...`，也就是本批改动的包；本机无法跑 `-race`（无 gcc），
该门由 CI 兜住。

同时 CI 的 integration 全绿也反向确认了 §8.4：本机那两个 `available_at` 用例的失败是环境负载
所致，而不是代码问题。

---

## 10. Freeze 判定

```text
SSE Hub Architecture            FROZEN
Single-Run Upstream Fanout      FROZEN
Subscriber Protocol Isolation   FROZEN
Replay/Live Catch-up            FROZEN
Durable Live Ordering           FROZEN
Slow Subscriber Isolation       FROZEN
Bounded Replay Cache            FROZEN
Hub Generation Safety           FROZEN
Hub Lifecycle                   FROZEN
```

Batch 4 + 4.1 至此收口，**下一步：Batch 5 — Worker Dispatcher**（其后 conversation lifecycle /
message keyset 分页 / 前端长对话）。

本轮明确未扩大范围：sequence allocator 本身没有问题（问题是 commit 后 publish 的 wall-clock
顺序与 DB sequence 顺序不是同一件事），故 `provider_submissions`、Provider 容量、Worker lease、
ownership epoch、请求幂等、terminal DB 事务、UTF-8 range 对账全部未动。

### 4.1.1 修正记录

> 上述 FROZEN 判定在 Batch 4.1 复审中被**暂时撤销**：复审发现 §6 的锁序整改把初始 idle timer
> 留在 `newRunHub` constructor 内且不持 `hub.mu`，而 `armIdleTimerLocked` 会调用
> `time.AfterFunc`，其 callback（`evictIfIdle`）在另一 goroutine 上清 `idleTimer` / 置
> `closed` ⇒ **真实 data race**；`SSE_HUB_IDLE_TTL` 只校验 `> 0`，`1ns` 是合法配置，callback
> 可以赢过赋值，于是 GetOrCreate 会发布一个已取消的 Hub。
>
> 该 P1 连同一条 P2（`repairDurableGap` 应显式校验权威日志自身连续）由
> `Batch 4.1.1 — Hub Initial Timer Race Closure` 关闭，见
> `docs/potal 第十轮 Batch 4.1.1 整改变更报告（Hub Initial Timer Race Closure）.md`；
> 本文档 §2.2 的 AC-4.1-5 措辞亦按该批 §19 修正为"连续前缀允许保留"。
> 4.1.1 全部门禁通过后，上表 FROZEN 判定恢复有效。
