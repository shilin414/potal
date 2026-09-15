# potal 第七轮复审整改变更报告

**范围**：SSE terminal replay 硬边界（P1）· duration metric 移出正确性事务（P2-1）· 前端同 chunk 终态停止（P2-2）
**复审基线**：dev `fc255f5`（第六轮功能提交 `ecbc83f`）
**本机验证**：gofmt / go vet / go build / go test 全绿；集成测试（MySQL 5.7 + Redis 7，`STUDIO_TEST_DB=1 STUDIO_TEST_REDIS=1`）全绿；前端 `tsc --noEmit` + `vitest`（131/131）+ `vite build` 全绿。

**未改动**：迁移 0018 / 0019 / 0020、event sequence 模型、Gate1/Gate2、Lease fencing、Schedule admission、重试与 Defer 语义。第七轮报告明确要求本轮不再碰这些。

---

## 一、P1 — SSE replay 已看到 terminal 却可能继续保持连接

### 问题（第七轮报告 §六~§十一）

HTTP 层把 `GetRunForUser()` 读到的 `run.Status` **快照**交给 Gateway。该快照是打开流之前读的，因此完全正常的终态竞争即可出现：

```text
T1 handler GetRun()            → status = running
T2 Stream(w, r, run{下 running})
T3 Gateway Subscribe Redis
T4 Worker 原子提交             → runs.status = succeeded + run.completed
T5 Gateway ListEventsAfter()   → replayedTerminal = true（客户端已收到终态）
T6 if execution.IsSettled(run.Status) → 仍是 running，不 return
T7 进入 live Redis loop
```

进入 live loop 后，那条 terminal Redis 消息已经在 T3 之后缓冲，但 replay 已把它算进 `lastReplayed`，于是被

```go
if frame.Sequence != 0 && frame.Sequence <= lastReplayed { continue }
```

dedupe 掉 —— **没有任何路径再触发 return**。服务端连接只能靠 keepalive 一直挂着，直到客户端自己断开（浏览器因为 `sawTerminal` 会断开，所以多数情况被掩盖；`curl` / 第三方 SSE client / 未来非 Web client 会明显不同）。

### 修复

`backend-go/internal/transport/sse/sse.go`

```go
replayedTerminal := false
for _, ev := range events {
        if !writeFrame(ev.Sequence, ev.EventType, ev.Payload, false) {
                return
        }
        lastReplayed = ev.Sequence
        if execution.IsTerminalEventName(ev.EventType) {
                replayedTerminal = true
                break            // ← terminal 是硬边界
        }
}
if replayedTerminal {
        return                   // ← 不再咨询 run.Status 快照
}
if execution.IsSettled(run.Status) { /* legacy synthetic fallback */ }
```

两点语义变化：

1. **`replayedTerminal` 取代 `run.Status` 快照成为终态事实**。replay 自己看见的 terminal 就是权威，快照只在「replay 里没有 terminal」时用于 legacy 补帧。
2. **replay 在第一个 terminal 上 `break`**。terminal = Run 生命周期结束，它之后的行不应再发给客户端（脏历史 `run.failed … run.completed` 也不该让结果被 HTTP chunk framing 影响）。0020 已经修掉已知的历史 retry→success 形状，所以这里可以安全把 terminal 当成真正硬边界。

顺序固定为：`Subscribe → Replay →(看见 terminal? 立即 close) →(快照 settled? synthetic + close) → live loop`。

包文档同步更新（订阅先于 replay，replay 止于第一个 terminal）。

---

## 二、P2-1 — duration metric 的辅助读仍在终态事务内，且失败会 rollback

### 问题（第七轮报告 §十三~§十五）

第六轮把 `studio_run_duration` 改成 DB 时钟是对的，但读的位置留下了口子：

```text
CASFinishRunFenced
→ GetRunForUpdate()      ← 纯粹为了 metric
→ if err != nil { return err }   ← 观测用途的 SELECT 能让终态事务 rollback
→ assistant message / occurrence / delivery / lease / slot cleanup
→ COMMIT
```

Provider 已经成功后，仅仅因为「读取 duration 时间戳」失败，事务就回滚 → **Run 退回 running、terminal event 消失、lease 保留**。这与文件自身的设计原则（`metrics are post-commit side effects, never correctness`）不一致。

### 修复

`backend-go/internal/execution/finalize.go`

- 事务内**不再有任何为 metrics 服务的读取**（`GetRunForUpdate` 的这次 fatal read 删除；`row` 继续由 `verifyFinalizeOwnershipTx` 提供，用于 assistant message 与 occurrence 收敛）。
- commit 之后：

```go
s.publishLive(...)
if s.Metrics != nil {
        s.Metrics.RunTotal.WithLabelValues(run.Provider, in.Status).Inc()
        s.observeRunDuration(ctx, own.RunID, run.Provider, in.Status)
}
```

```go
const RunDurationObservationTimeout = 2 * time.Second

func (s *Service) observeRunDuration(ctx context.Context, runID ids.ID, provider, status string) {
        // 独立的 detached + 有界上下文：继承 values（日志/链路关联），丢弃父取消与父 deadline
        obsCtx, cancel := NewCleanupContext(ctx)
        defer cancel()
        obsCtx, cancelTimeout := context.WithTimeout(obsCtx, RunDurationObservationTimeout)
        defer cancelTimeout()

        startedAt, finishedAt, err := s.runTimestamps(obsCtx, runID)
        if err != nil {
                log.Warn("run duration observation skipped", ...)   // 只告警，绝不返回错误
                return
        }
        seconds, ok := dbClockDuration(startedAt, finishedAt)
        if !ok { return }                     // NULL / 非单调 → 丢样本，不写垃圾值
        s.Metrics.RunDuration.WithLabelValues(provider, status).Observe(seconds)
}
```

- 新增查询 `GetRunTimestamps`（`backend-go/db/queries/execution.sql`，已跑 `sqlc generate`，只新增 `execution.sql.go` / `querier.go` 两处，无附带 churn）：

```sql
-- name: GetRunTimestamps :one
SELECT started_at, finished_at FROM runs WHERE id = ?;
```

- 新增测试 seam `Service.RunDurationTimestamps`（nil = 走 SQL querier）。理由与 `aily.ProviderAPI` 相同：要证明的性质是「metric 读失败绝不可能回滚终态事务」，唯一办法是在真库上**故意让它失败**。

上下文用 `NewCleanupContext`（第五轮定型的 detached primitive）而不是裸 `context.Background()`：终态观测不应该因为调用方 ctx 被取消而静默消失，但也不能无界阻塞 worker。

---

## 三、P2-2 — 前端同一网络 chunk 内 terminal 后仍继续 dispatch

### 问题（第七轮报告 §十七~§十九）

```ts
frames.forEach((frame) => consumeFrame(frame, dispatch));
```

`dispatch()` 命中终态会置 `sawTerminal = true`，但 `forEach` 不会因此停下。HTTP / ReadableStream / nginx / TCP 都可能把多帧合并进**一次 read**，于是同一个 chunk 里 terminal 之后的帧仍会进入 reducer（`run.failed` + `run.completed` 同 chunk → UI 先把失败渲染出来又被改写成成功）。第六轮的测试特意一帧一次 pull，所以没有覆盖 coalesced chunk。

### 修复

`frontend/src/services/runStream.ts`

```ts
for (const frame of frames) {
  if (closed || sawTerminal) break;
  consumeFrame(frame, dispatch);
}
if (complete && buffer.trim() && !closed && !sawTerminal) {
  consumeFrame(buffer, dispatch);
}
```

尾部残余 buffer 的处理同样加守卫，否则 EOF 路径会绕过边界。

---

## 四、新增测试（第七轮报告 §十二 / §十六 / §二十 / §二十五 全清单）

| 测试 | 位置 | 断言 |
| --- | --- | --- |
| `TestSSEStaleRunningSnapshotClosesAfterTerminalReplay` | `tests/integration/sse_gateway_test.go` | claim → `GetRun` 取 stale `running` 快照 → `FinalizeOwnedRun(succeeded)` → 用 **stale 快照** 调 `Stream`；收到 `run.completed` 后 **2s 内必须 body EOF**，不得靠 keepalive 续命 |
| `TestSSEReplayStopsAtFirstTerminalEvent` | 同上 | 脏历史 `content.delta / run.failed / content.delta` → 只 replay 2 帧，止于 `run.failed` |
| `TestDurationMetricReadFailureDoesNotRollbackFinalize` | `tests/integration/review7_fixes_test.go` | 注入 duration 读失败 → `FinalizeOwnedRun` 返回 nil；status=succeeded、`run.completed` 恰 1 条、无 lease、**provider slot = 0**、assistant message = 1；唯一代价是**没有 duration 样本**（0 个采集点） |
| `TestDurationMetricUsesPersistedDBTimestamps` | 同上 | observer 走**独立连接**：观察到的 `runs.status` 必须已是 `succeeded`（证明在 commit 之后读），且恰好产生 1 个 duration 样本 |
| `stops dispatching events after a terminal frame in the same network chunk` | `frontend/src/services/__tests__/runStream.test.ts` | 单 chunk 内 `run.failed` + `run.completed` → `events === ['run.failed']` |
| `emits both frames when a non-terminal frame precedes a terminal one in the same chunk` | 同上 | 单 chunk 内 `run.retrying` + `run.completed` → 两帧都到（retrying 非终态） |

新增 fixture 清理：`deleteConversationFixture`；`deleteRunFixture` 补上 `provider_execution_slots`。第七轮报告沿用第六轮的教训 —— 「全库 COUNT 期望 0」类校验会被 fixture 残留假红，所以新 seed 一律自带 `t.Cleanup`（FK 顺序：events → leases → slots → outbox → run → conversation）。

### 反证（falsification）—— 确认测试真的能抓住回归

新增测试如果对旧代码也通过，就没有价值。逐个把修复临时还原、跑测试，确认失败：

| 临时还原 | 结果 |
| --- | --- |
| `sse.go` 去掉 `break` 与 `if replayedTerminal { return }` | `TestSSEStaleRunningSnapshotClosesAfterTerminalReplay` **FAIL**（2.18s，停在第 2s EOF deadline：服务端确实一直挂着）；`TestSSEReplayStopsAtFirstTerminalEvent` **FAIL**（`got 3 frames … [content.delta run.failed content.delta]`） |
| `finalize.go` 把观测移回事务内且 fatal | 两个 metric 测试 **FAIL**（`finalize returned injected duration read failure`；observer 读到的是未提交状态 → `duration bounds are NULL after finalize`） |
| `runStream.ts` 去掉 `closed \|\| sawTerminal` 守卫 | 同 chunk 测试 **FAIL**（`["run.failed", "run.completed"]`，多出 `run.completed`） |

还原后全部重新变绿。

---

## 五、本机验证结果

```text
gofmt -l internal tests cmd        → 空
go vet ./...                       → ok
go build ./...                     → ok
go test ./...                      → 全部 ok
sqlc generate                      → 仅 GetRunTimestamps 相关新增，无其他 diff

STUDIO_TEST_DB=1 STUDIO_TEST_REDIS=1
  go test ./tests/integration/     → ok (55.9s，全量套件)

frontend: tsc --noEmit             → ok
          vitest run               → 16 files / 131 tests passed
          vite build               → ok
```

未变更迁移，故 MySQL 侧 `migration version = 20`、二次执行 clean no-op 不受影响（CI 覆盖）。
本机无 gcc，`go test -race` 仍只在 CI Ubuntu 上跑。

---

## 六、遗留（第七轮报告 §二十一 / §二十三 / §二十四 第三批）

按报告「第三批：以后」执行，本轮**未做**：

1. **P3 `onTransportEnd` 契约清理**。当前结构下 `while (!closed && !sawTerminal)` 的退出条件与循环内 `return` 重叠，导致 `handlers.onTransportEnd?.()` 实际不可达；网络错误时行为是每 2s 无限重连。报告建议二选一：删除死接口，或引入 `max retries + exponential backoff` 后调用它。**报告倾向暂时删除死接口**，等未来 SSE Hub / 统一重连策略时再设计。
2. **Event sequence allocator**（`runs.next_event_sequence`）—— 现仍是 `COUNT(*) + 1`，有 run 行锁保证并发正确，报告定为 P3 / 后续性能架构。
3. 下一轮应转向的新架构风险：`client_request_id` 幂等、SSE 多连接 / Hub、长历史 event 存储与分页、Worker dispatcher、Conversation 生命周期、前端长对话性能。

报告同时建议：第七轮三件套完成后，**停止**对 Gate1 / Gate2 / Provider submit boundary / Lease fencing / Retry·Defer / Schedule admission / legacy interrupted migration 继续微调。
