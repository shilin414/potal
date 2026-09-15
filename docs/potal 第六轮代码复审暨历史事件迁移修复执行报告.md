# potal 第六轮代码复审暨历史事件迁移修复执行报告

## 一、当前复审基线

当前：

```text
dev HEAD
78d279348e803a6862920d66585ff50ab8d8ab57
```

第五轮功能提交：

```text
0bdcf3035dd3a7a115829fbd351fab4b6e1e1396
fix(review5): close the fifth-round review findings
```

当前 HEAD 后面只是 memory/chore 记录；功能树已经经过 CI。

Backend 已确认通过：

```text
actionlint
gofmt
go vet
go build
go test
go test -race
MySQL 5.7
migration
migration second-run no-op
DB-backed tests
integration tests
```

Frontend 已确认通过：

```text
typecheck
unit tests
production build
```

---

# 二、本轮总判定

第五轮绝大部分整改可以正式关闭。

## 已关闭

```text
✅ finalizeRun stale state race
✅ activeRunId compare-and-clear
✅ artifact 三态
✅ streaming producer cancellation
✅ RateLimiter ctx propagation
✅ strict DB Clock
✅ started_at DB timestamp → memory snapshot
✅ bounded cleanup context
✅ provider_id 0019 full reconciliation
✅ status=interrupted 作为 legacy settled alias
```

其中前端 `finalizeRun()` 已经改成异步结束后再基于最新 Zustand state functional merge，而不是使用旧 Conversation 快照；新的 Run 不会再被旧 Run finalize 覆盖。 

Streaming producer 也已经统一通过可取消的：

```go
select {
case out <- ev:
case <-ctx.Done():
}
```

发送，上一轮指出的 goroutine 卡死路径已关闭。

Schedule DB Clock、limiter ctx、started_at 以及 cleanup 同样都已经真正落代码。  

但是：

# 当前仍有 1 个 P0 + 1 个 P1

因此：

# 正式生产发布：BLOCKED

不是 Execution/Kill-Switch 主链出问题，而是 **0018 历史事件数据迁移的语义判断错误**。

---

# 三、P0：0018 把“历史重试事件”错误迁成 terminal failure

这是本轮最重要的问题。

## 3.1 当前 0018 做了什么

现在：

`db/migrations/0018_normalize_legacy_interrupted_runs.up.sql`

包含：

```sql
UPDATE run_events
SET event_type = 'run.failed'
WHERE event_type = 'run.interrupted';
```

也就是说：

> 所有历史 `run.interrupted` 都被视为 terminal failure。



这与第五轮当前的新契约其实已经存在逻辑冲突。

当前单元测试明确规定：

```go
IsTerminalEventName(EventRunInterrupted) == false
```

并写明：

> legacy `run.interrupted` 必须可回放，但不能关闭 live stream，因为以前 retry path 会产生它。



这个测试判断是正确的。

错误的是 migration 0018。

---

# 四、历史源码已经证明 run.interrupted 并不等于失败

我继续往旧 commit 回查了实际实现。

旧版：

```go
func (s *Service) releaseInterrupted(...) error {
    _ = s.AppendEvent(
        ctx,
        run.ID,
        EventRunInterrupted,
        map[string]any{"reason": reason},
    )

    if run.Attempt >= run.MaxAttempts {
        // fail
    } else {
        // requeue
    }
}
```

关键点在顺序：

```text
先写 run.interrupted
↓
再判断：
  attempt 满了 → failed
  attempt 没满 → requeue
```

也就是说历史行为实际上是：

```text
run.interrupted
```

同时承载了两种情况：

```text
A. 本次 Worker 中断，但 Run 还会重试
B. 本次 Worker 中断且重试额度耗尽
```

旧源码直接证明了这一点。

所以：

# `runs.status='interrupted'` 和 `run_events.event_type='run.interrupted'` 不能混为一谈。

---

# 五、正确的契约应该分两层

## Run Status

```text
runs.status = interrupted
```

可以定义为：

```text
legacy terminal alias
≈ failed
```

这一点第五轮处理正确。

所以：

```go
IsSettled(StatusInterrupted) == true
IsTerminal(StatusInterrupted) == false
```

是合理的兼容模型。

---

## Run Event

但是：

```text
run.interrupted
```

历史上是 overloaded event：

```text
reason-only run.interrupted
    → worker interruption / retry marker

status=interrupted run.interrupted
    → legacy direct terminal event
```

不能：

```text
run.interrupted
→ 全部 run.failed
```

---

# 六、为什么这个问题会真的破坏前端

例如历史 Run：

```text
run.started

run.interrupted
payload = {
  "reason": "worker lease expired"
}

↓ requeue

run.started

run.completed
```

这个 Run 最终成功。

执行 0018 后：

```text
run.started

run.failed       ← 被 migration 错改

run.started

run.completed
```

而当前 frontend 的 SSE client：

```ts
const TERMINAL_EVENTS = new Set([
  'run.completed',
  'run.failed',
  'run.cancelled',
]);
```

一旦收到：

```text
run.failed
```

就把：

```text
sawTerminal = true
```

停止继续订阅。

如果网络 chunk 恰好在：

```text
run.failed
```

之后断开，那么后面的：

```text
run.completed
```

根本不会再读。

于是一个历史成功 Run 可能最终显示：

# 执行失败

而且这个结果取决于网络分帧，非常隐蔽。

---

# 七、更严重的情况：迁移时仍处于 queued 的历史 Run

例如 migration 执行前：

```text
runs.status = queued

events:
run.started
run.interrupted(reason)
```

这是过去一次失败后正在等下一轮 retry。

0018 会得到：

```text
runs.status = queued

events:
run.started
run.failed
```

Run 明明还是 live work，但事件日志已经出现 terminal failure。

Frontend 重连：

```text
看到 run.failed
↓
关闭 stream
↓
后面真正 retry 成功
↓
用户收不到
```

这是当前我把它定为：

# P0 release blocker

的核心原因。

---

# 八、为什么现有第五轮测试没抓到

现在 `review5_interrupted_test.go` 的 migration fixture 主要是：

```text
直接把 runs.status 强制设为 interrupted
```

然后测试：

```text
interrupted → failed
```

这个测试覆盖的是：

# legacy status compatibility

但没有模拟旧版真实：

```text
running
↓
run.interrupted(reason)
↓
requeue
↓
succeeded
```

生命周期。

甚至目前 migration 测试插入的是类似：

```sql
event_type = 'run.interrupted'
payload = '{}'
```

但历史源码里真正 retry-origin 事件是：

```json
{
  "reason": "..."
}
```

而旧版真正通过 `Finish(StatusInterrupted)` 写出的 terminal interrupted event，payload 则包含：

```json
{
  "status": "interrupted",
  "provider_status": "...",
  "finish_reason": "..."
}
```

旧 Finish 代码对此也有直接证据。

所以我们其实有一个非常好用的历史判别信号：

```text
有 reason
没有 status
→ retry-origin interrupted
```

---

# 九、P0 修复原则

### 不修改 0018

0018 已经进入 Git 历史，也已经被开发/CI 数据库执行过。

不要回改：

```text
0018_normalize_legacy_interrupted_runs
```

应该新增：

# `0020_repair_legacy_interrupted_event_semantics`

这样三类环境都安全：

```text
全新库：
0018 → 0019 → 0020
最终正确

已经执行 0018 的开发库：
0020 修复历史误转换

未来生产升级：
按顺序执行
最终正确
```

保持 MySQL 5.7 兼容。

---

# 十、0020 推荐完整 SQL

文件：

```text
backend-go/db/migrations/
0020_repair_legacy_interrupted_event_semantics.up.sql
```

建议：

```sql
-- Repair the overloaded pre-closure run.interrupted event semantics.
--
-- Historical ReleaseInterrupted wrote:
--
--   run.interrupted {"reason": "..."}
--
-- BEFORE deciding whether the run would be requeued or failed.
--
-- Therefore a reason-only interrupted event is a retry/interruption marker,
-- NOT a terminal failure.
--
-- Migration 0018 converted every run.interrupted to run.failed. Restore
-- those retry-origin markers to the canonical non-terminal event.

UPDATE run_events
SET event_type = 'run.retrying'
WHERE event_type IN ('run.failed', 'run.interrupted')
  AND JSON_EXTRACT(payload, '$.reason') IS NOT NULL
  AND JSON_EXTRACT(payload, '$.status') IS NULL;


-- Some legacy runs exhausted retries after emitting the reason-only
-- interrupted marker. After converting that marker to run.retrying they
-- may have no canonical terminal event.
--
-- Restore invariant:
--
-- terminal run <=> at least one terminal run event
--
-- This also safely heals older cancelled/succeeded/failed rows that
-- predate atomic terminal-event persistence.

INSERT INTO run_events (
    run_id,
    sequence,
    event_type,
    payload
)
SELECT
    r.id,
    COALESCE(
        (
            SELECT MAX(e.sequence)
            FROM run_events e
            WHERE e.run_id = r.id
        ),
        0
    ) + 1,
    CASE r.status
        WHEN 'succeeded' THEN 'run.completed'
        WHEN 'cancelled' THEN 'run.cancelled'
        ELSE 'run.failed'
    END,
    JSON_OBJECT(
        'status', r.status,
        'error_code', COALESCE(r.error_code, ''),
        'error_message', COALESCE(r.error_message, ''),
        'migrated_from', 'terminal_without_canonical_event'
    )
FROM runs r
WHERE r.status IN (
        'succeeded',
        'failed',
        'cancelled'
    )
  AND NOT EXISTS (
      SELECT 1
      FROM run_events e2
      WHERE e2.run_id = r.id
        AND e2.event_type IN (
            'run.completed',
            'run.failed',
            'run.cancelled'
        )
  );
```

全部都是：

```text
MySQL 5.7
```

支持的语法。

---

# 十一、0020 down.sql

这是历史语义修复，不应该尝试反推。

文件：

```text
0020_repair_legacy_interrupted_event_semantics.down.sql
```

写：

```sql
-- Irreversible historical event normalization.
SELECT 1;
```

即可。

---

# 十二、直接 terminal interrupted 不得被误改

旧版真正：

```go
Finish(StatusInterrupted)
```

产生的 payload 带：

```json
{
  "status": "interrupted"
}
```

0020 条件：

```sql
JSON_EXTRACT(payload, '$.status') IS NULL
```

因此不会把这种真正 terminal legacy event 转成：

```text
run.retrying
```

它经过 0018 后继续：

```text
run.failed
```

这是正确的。

---

# 十三、旧版 retry exhausted 场景也能正确恢复

旧流程：

```text
run.interrupted(reason)

attempt >= max_attempts
↓
runs.status = failed
```

旧代码并不会另外补：

```text
run.failed
```

terminal event。

0020：

第一步：

```text
run.interrupted / 被0018改成的run.failed(reason)
↓
run.retrying
```

第二步发现：

```text
runs.status = failed
但没有 canonical terminal event
```

于是追加：

```text
run.failed
```

最终事件：

```text
run.retrying
run.failed
```

语义完全恢复。

---

# 十四、P0 必须增加的测试

文件建议：

```text
backend-go/tests/integration/
review6_interrupted_event_migration_test.go
```

至少增加 5 个。

### 1. 最重要：历史 retry 后成功

```text
TestMigration0020RestoresHistoricalRetryBeforeSuccess
```

Fixture：

```text
runs.status = succeeded

sequence 1
run.started

sequence 2
run.interrupted
{"reason":"worker lease expired"}

sequence 3
run.started

sequence 4
run.completed
```

先执行：

```text
0018
```

确认：

```text
seq2 = run.failed
```

然后执行：

```text
0020
```

最终必须：

```text
seq2 = run.retrying
seq4 = run.completed

terminal events count = 1
terminal event = run.completed
```

---

### 2. 仍然 queued 的 retry

```text
TestMigration0020KeepsActiveRetryNonTerminal
```

Fixture：

```text
runs.status = queued

event:
run.interrupted {
    "reason":"worker lease expired"
}
```

0018 + 0020 后：

```text
status = queued
event = run.retrying
terminal event count = 0
```

这是最能防止本轮 P0 回归的测试之一。

---

### 3. retry exhausted

```text
TestMigration0020SynthesizesTerminalAfterExhaustedRetry
```

Fixture：

```text
runs.status = failed
error_code = interrupted

event:
run.interrupted {
    "reason":"worker lease expired"
}
```

最终：

```text
run.retrying
run.failed
```

并且：

```text
run.failed count = 1
```

---

### 4. direct terminal interrupted

```text
TestMigration0020PreservesDirectTerminalInterrupted
```

Fixture：

```text
runs.status = interrupted

event:
run.interrupted {
    "status":"interrupted",
    "provider_status":"",
    "finish_reason":""
}
```

0018 + 0020 后：

```text
run.status = failed
event = run.failed
```

不能变成：

```text
run.retrying
```

---

### 5. 幂等

```text
TestMigration0020IsIdempotent
```

0020 执行两次：

```text
event count 不增长
sequence 不变化
没有重复 terminal
```

---

# 十五、同时修改现有 0018 测试 fixture

不要再使用：

```json
{}
```

来代表 legacy terminal `run.interrupted`。

明确分成：

### retry marker

```json
{
  "reason": "worker lease expired"
}
```

### direct terminal

```json
{
  "status": "interrupted",
  "error_code": "interrupted"
}
```

这样测试才与历史代码真正一致。

---

# 十六、P1：SSE synthetic fallback 把 cancelled 合成为 run.completed

这是另一个独立问题。

当前：

`internal/transport/sse/sse.go`

对于：

```text
Run 已 settled
但历史数据没有 terminal event
```

会 synthetic 一个 terminal frame。

代码目前：

```go
eventType := execution.EventRunCompleted

if run.Status == execution.StatusFailed ||
    run.Status == execution.StatusInterrupted {
    eventType = execution.EventRunFailed
}
```

没有：

```go
StatusCancelled
```

分支。

所以：

```text
runs.status = cancelled
无 terminal event
```

重连 SSE 得到：

```text
run.completed
```

这又会把我们前几轮已经修好的：

```text
cancelled ≠ success
```

语义重新破坏。

---

# 十七、P1 SSE 修复

建议增加小 helper：

```go
func syntheticTerminalEventName(
    status string,
) (string, bool) {
    switch status {
    case execution.StatusSucceeded:
        return execution.EventRunCompleted, true

    case execution.StatusCancelled:
        return execution.EventRunCancelled, true

    case execution.StatusFailed,
        execution.StatusInterrupted:
        return execution.EventRunFailed, true

    default:
        return "", false
    }
}
```

Gateway：

```go
if execution.IsSettled(run.Status) {
    if !replayedTerminal {
        eventType, ok :=
            syntheticTerminalEventName(run.Status)

        if !ok {
            return
        }

        synthetic := map[string]any{
            "status": run.Status,
        }

        ...

        writeFrame(
            0,
            eventType,
            synthetic,
            true,
        )
    }

    return
}
```

---

# 十八、P1 SSE 测试

当前：

```text
backend-go/internal/transport/sse
```

只有实现文件，没有专门测试文件。

新增：

```text
sse_terminal_test.go
```

至少：

```text
succeeded
→ run.completed

failed
→ run.failed

interrupted
→ run.failed

cancelled
→ run.cancelled

queued
→ no synthetic terminal mapping
```

再推荐一个完整 Gateway 回归：

```text
TestCancelledRunWithoutTerminalEventReplaysCancelled
```

使用真实 test DB：

```text
run.status = cancelled
terminal event 删除
↓
GET /stream
```

断言返回 frame：

```json
{
  "event_type": "run.cancelled"
}
```

绝不能：

```json
{
  "event_type": "run.completed"
}
```

---

# 十九、建议增加一个真正的前端历史回放测试

Frontend：

```text
frontend/src/services/
runStream.test.ts
```

模拟两次网络 read：

第一次：

```text
run.retrying
```

第二次：

```text
run.completed
```

断言：

```text
run.retrying
不会设置 sawTerminal

run.completed
才结束
```

再做一条：

第一段：

```text
run.failed
```

第二段：

```text
run.completed
```

断言：

```text
failed 后不会继续第二次读取
```

这个测试可以直接证明为什么 migration 不能把历史 retry marker 改成 failed。

---

# 二十、P2：RunDuration 仍然混用了 DB Clock 与 App Clock

第五轮已经正确做到：

```text
started_at
由 MySQL CURRENT_TIMESTAMP 产生
```

并且读回：

```text
claimed.Run.StartedAt
```

这一项整改正确。

但是 finalize metric 当前：

```go
timeSinceSeconds(*run.StartedAt)
```

而：

```go
timeSinceSeconds
```

内部还是：

```go
time.Since(start)
```

也就是：

```text
start = DB clock
end   = Worker host clock
```

 

在机器时钟存在 skew 时：

```text
duration 偏大
duration 偏小
甚至负值
```

业务正确性不受影响，只影响 observability。

所以定：

# P2

---

# 二十一、P2 RunDuration 推荐方案

最干净的是在 finalize transaction 中获取：

```text
finished_at
```

或者：

```text
DB NOW
```

不要重新使用 Worker clock。

可以新增：

```sql
-- name: CurrentDBTime :one
SELECT CURRENT_TIMESTAMP(3);
```

其实项目已有 DB clock query，可以复用。

Finalize：

```go
finishedAt, err := q.CurrentDBTime(ctx)
```

最好与 terminal CAS 在同事务。

commit 后：

```go
if run.StartedAt != nil {
    duration :=
        finishedAt.Sub(*run.StartedAt).Seconds()

    if duration >= 0 {
        metric.Observe(duration)
    }
}
```

更进一步可直接读 terminal row 的：

```text
finished_at
```

作为最终权威。

此项不阻塞上线。

---

# 二十二、P3：Event sequence 仍使用 COUNT(*) + 1

当前：

```go
SELECT COUNT(*)
FROM run_events
WHERE run_id = ?

sequence = count + 1
```

仍存在。

因为 run row 已经：

```text
FOR UPDATE
```

所以并发正确性没问题。

但长输出、高事件 Run 会导致：

```text
每写一个 durable event
都 COUNT 一遍历史
```

复杂度逐渐增加。

这是之前已经知道的架构项：

# 不要放进这次发布修复。

以后独立做：

```text
runs.next_event_sequence
```

或：

```text
MAX(sequence)
```

优化即可。

---

# 二十三、第五轮整改最终复核表

| 项目 | 最终结果 |
|---|---|
| finalize stale snapshot | ✅ CLOSED |
| newer Run 被旧 finalize 覆盖 | ✅ CLOSED |
| artifact fetch failure 清空 SSE artifact | ✅ CLOSED |
| stream producer goroutine leak | ✅ CLOSED |
| strict Schedule DB Clock | ✅ CLOSED |
| limiter ctx propagation | ✅ CLOSED |
| started_at memory sync | ✅ CLOSED |
| cleanup bounded context | ✅ CLOSED |
| provider_id orphan 0019 | ✅ CLOSED |
| `StatusInterrupted` legacy settled | ✅ CLOSED |
| `EventRunInterrupted` migration | ❌ **P0** |
| cancelled SSE fallback | ❌ **P1** |
| DB/App mixed duration metric | ⚠️ P2 |

---

# 二十四、建议开发执行顺序

## 第一优先级：发布前必须

```text
1. 新增 migration 0020
2. 修复 interrupted reason-only event
3. terminal run 缺 event 时补 canonical terminal event
4. 修正 review5 interrupted fixtures
5. 增加历史 retry→success migration 测试
6. 修 SSE cancelled synthetic mapping
7. 增加 SSE synthetic terminal mapping tests
```

完成这 7 条：

# 可以解除生产阻断。

---

## 第二优先级：非阻断

```text
8. RunDuration 全部改 DB clock
```

---

## 下一阶段再做

```text
client_request_id 幂等
SSE Hub multiplexing
RunEvent sequence
Worker blocking dispatcher
Conversation soft-delete
Sidebar / history 性能
```

不要再继续重构：

```text
Gate1
Gate2
Defer
Provider attempt
Kill Switch
Schedule admission
Lease fencing
```

这些当前已经基本稳定。

---

# 二十五、最终 CI 要求

修完以后 backend 至少重新跑：

```text
actionlint
gofmt
go vet ./...
go build ./...
go test ./... -count=1
go test -race execution/delivery/automation

MySQL 5.7:
migrations 1..20
第二次 migrate no-op

STUDIO_TEST_DB=1
STUDIO_TEST_REDIS=1
integration tests
```

重点确认 migration version：

```text
20
```

并明确检查：

```sql
SELECT COUNT(*)
FROM run_events e
JOIN runs r ON r.id = e.run_id
WHERE e.event_type = 'run.failed'
  AND JSON_EXTRACT(e.payload, '$.reason') IS NOT NULL
  AND JSON_EXTRACT(e.payload, '$.status') IS NULL
  AND r.status NOT IN ('failed');
```

期望：

```text
0
```

再检查 terminal invariant：

```sql
SELECT COUNT(*)
FROM runs r
WHERE r.status IN (
    'succeeded',
    'failed',
    'cancelled'
)
AND NOT EXISTS (
    SELECT 1
    FROM run_events e
    WHERE e.run_id = r.id
      AND e.event_type IN (
          'run.completed',
          'run.failed',
          'run.cancelled'
      )
);
```

期望：

```text
0
```

---

# 二十六、当前评级

如果只评价第五轮新代码实现：

# **96 / A**

前端竞态、Streaming、Clock、Limiter、started_at、cleanup 的实现质量都明显已经进入比较稳定的状态。

但是从“当前版本能不能直接生产升级”评价：

# **90 / B+ — BLOCKED**

原因只有一个主要问题：

```text
0018 会错误改变历史事件事实
```

migration 的风险比普通 runtime bug 更高，因为：

```text
runtime bug
→ 修代码即可

错误 migration
→ 会永久改写数据库历史
```

所以我建议不要带着 0018 当前语义直接进入正式环境。

完成：

```text
0020 interrupted-event repair
+
SSE cancelled fallback
```

以后，我的预期评级是：

# **96 / A，可以生产**

并且 Execution / Admission / Kill-Switch 这一整条主链可以正式停止反复小修，把审查资源转移到下一阶段架构。