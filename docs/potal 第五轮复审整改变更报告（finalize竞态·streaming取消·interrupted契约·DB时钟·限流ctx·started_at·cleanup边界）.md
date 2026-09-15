# potal 第五轮复审整改变更报告

> 复审基线：`dev HEAD = 362dff69d83e75ea54420365369546b7f91bfc1f`（93 / A-）
> 本报告对应修复：`P1-1 / P1-2 / P2-1 … P2-5`（报告第十三 ~ 二十节）
> 全部条目已实现并回归通过，详下。

---

## 一、总览

| 项 | 主题 | 结论 |
|---|---|---|
| P1-1 | 前端 `finalizeRun()` 陈旧快照覆盖新 Run | 已修（functional setState + compare-and-clear + 产物三态） |
| P1-2 | streaming producer 裸 channel 发送 → goroutine 泄漏 | 已修（`emitStreamEvent` + ctx select） |
| P2-1 | `interrupted` 遗留终态契约 + 迁移 0018 | 已修（`IsSettled` + 0018 归一） |
| P2-2 | Schedule DB Clock 去掉应用机时钟兜底 | 已修（`ErrDBClockUnavailable`，无 fallback） |
| P2-3 | RateLimiter ctx error 向上传播 | 已修（AllowKey 首检 ctx + HTTP 层传播） |
| P2-4 | `started_at` 内存 Run 快照未同步 | 已修（`MarkRunStartedOwned` 返回 DB 时间） |
| P2-5 | cleanup 无界 `context.Background()` + `provider_id` orphan | 已修（`NewCleanupContext` + 迁移 0019） |

---

## 二、P1-1 前端 finalize 竞态

**文件**：`frontend/src/stores/useRunChatStore.ts`

问题：`finalizeRun` 在两次 `await`（getRun → fetchArtifacts）之前就取了 `state`/`conv` 快照，
await 之后把整份**陈旧 conversation** 写回 store —— 期间新建的 Run B 的消息会被抹掉，
`activeRunId` 也会被清空；产物拉取失败还会把 SSE 已收到的 artifacts 清成空。

改法：

1. 两次 await 之后用 **functional `setState`** 基于最新 state 计算；
2. `activeRunId` 用 **compare-and-clear**（只在仍等于 runId 时清空）；
3. artifacts 用 **`ChatArtifact[] | null` 三态**：`[]` = 服务端确实没有，`null` = 拉取失败（不清空已有）。

**测试**（`__tests__/useRunChatStore.test.ts`，共 24 个）：

- `finalizeRun preserves a newer active run`（Promise barrier 卡住产物请求 → 注入 Run B → 放行，断言 A=done、B 消息存活、`activeRunId === 'run-B'`）
- `finalizeRun preserves event artifacts when artifact refresh fails`
- `finalizeRun clears artifacts when the server reports none`

---

## 三、P1-2 streaming 取消安全

**文件**：`backend-go/internal/integrations/aily/adapter.go`

三处裸 `out <- catalog.StreamEvent{...}` 全部改为：

```go
func emitStreamEvent(ctx context.Context, out chan<- catalog.StreamEvent, ev catalog.StreamEvent) error {
    select {
    case out <- ev:
        return nil
    case <-ctx.Done():
        return ctx.Err()
    }
}
```

同时把 `context.DeadlineExceeded` 加入"传输层错误不记错误指标"的判据。

**测试**：`internal/integrations/aily/stream_cancel_test.go`（新增）

- 造假流是**无尽头**的（`endlessSSE`），消费者退出后 producer 若不响应 ctx 就永远阻塞；
- `TestStreamPreparedCancelUnblocksFullOutputBuffer`：等 `len(out) >= 64`（缓冲满）再 cancel，断言 producer 退出且 body 被 Close；
- `TestStreamPreparedStopUnblocksConsumerExit`、`TestStreamPreparedDeliversAllEventsWhenConsumed`。

> 已回退验证：恢复裸发送后 `CancelUnblocksFullOutputBuffer` 报
> `producer goroutine leaked: cancel() did not unblock a full output buffer`。

---

## 四、P2-1 `interrupted` 遗留终态契约

契约：`interrupted` = **收口前的终态别名**，等价于 `failed`。新代码永不写入，历史行由迁移归一。

**代码**

- `internal/execution/domain.go`：新增 `LegacyTerminalStatuses`、`IsSettled()`（canonical 终态 ∪ `interrupted`）；`IsTerminal()` 保持只认 `{cancelled, succeeded, failed}`。
- `finalize.go`：`terminalEventName` 不再接受 `interrupted`；`FinalizeOwnedRun` 通过 `IsTerminal` 直接拒绝写入（`ErrInvalidTerminalStatus`）。
- `sse.go`（2 处）、`aily/executor.go`：`IsTerminal` → `IsSettled`（已 settlement 的 legacy 行必须关闭流）。
- SQL 谓词统一为 `status NOT IN ('cancelled','succeeded','failed','interrupted')`：`CountActiveRunsByConversation`、`CASFinishRun`、`CountOutstandingRunsByUser`。

**迁移 0018**（新增，不改 0017）

1. occurrences：`interrupted` run 的槽位 → `failed`；
2. `run_events`：`run.interrupted` → `run.failed`；
3. **新增**：为"被中断但没有任何事件行"的 run 合成 `run.failed`（invariant D：终态 run 必须有终态事件；否则回放与 invariant checker 都会报警）；
4. `runs`：`interrupted` → `failed`，保留原 `error_code`（缺失则写 `legacy_interrupted`），`finished_at` 兜底。

**测试**

- 单测 `internal/execution/interrupted_contract_test.go`：`IsSettled` / `IsTerminal` 边界、`terminalEventName` 拒绝 legacy、`run.interrupted` 不关闭活跃流。
- 集成 `tests/integration/review5_interrupted_test.go`：不阻塞会话、不占用 outstanding、不可被重新终态化、**新代码写不进 interrupted**、迁移归一 + 合成终态事件 + 重复执行 no-op。
- **契约翻转**：`review4_gate_submit_test.go` 的 `TestNonTerminalStatusBlocksSecondTurn` 原把 `interrupted` 当作活跃状态；已移出该列表并新增 `TestLegacyInterruptedFreesTheConversation`。

---

## 五、P2-2 Schedule DB Clock

**文件**：`internal/automation/schedule/service.go`、`internal/automation/scheduler/scheduler.go`

`dbNow` / `dbNowTx` 改为返回 `(time.Time, error)`，**删除 `nowFunc` 兜底**；`Create` / `Update` / `SetEnabled` 拿不到 DB 时间即中止；`Scheduler.ProcessDue` 跳过本 tick，`TriggerNow` 向上传播 `ErrDBClockUnavailable`。

**测试**：`tests/integration/review5_clock_test.go`（新增 4 个）——用 `driver.Connector` 包装只让 `CURRENT_TIMESTAMP(3)` 那一条语句失败，断言 Create 中止、Update/SetEnabled 回滚、Scheduler 跳过 tick。

---

## 六、P2-3 限流 ctx 传播

**文件**：`internal/execution/ratelimit.go`、`internal/transport/http/run_handlers.go`

- `AllowKey` 在**任何分支之前**先判 `ctx.Err()`（原先只有 Redis 错误分支判，导致无 Redis 部署会放行已取消请求）；
- HTTP 层 `admitUserRun`：`err != nil` 直接返回错误，只有 `ok == false` 才写 429；`CreateRun` 在 `ctx.Err() != nil` 时不写任何响应（客户端已断开）。

**测试**：

- 单测 `TestAllowKeyCancelledContextReturnsCtxError`（`no-redis` / `with-redis` 两个子用例，并断言已取消调用**不消耗 token**）；
- 单测 `internal/transport/http/admission_ctx_test.go`（传播取消 ctx / 放行活跃 ctx / 正常 429）；
- `TestUserAdmissionRedisDownStillBounded` 覆盖"Redis 为 nil 仍本地限流"。

> 已回退验证：去掉 AllowKey 的首检后，`no-redis` 子用例报
> `AllowKey err = <nil>, want context.Canceled`。

---

## 七、P2-4 `started_at` 内存同步

**文件**：`internal/execution/service.go`、`worker.go`、`db/queries/execution.sql`

`started_at` 在 Gate allow 后由 DB 写入，但 worker 的 `claimed.Run` 是 claim 时刻的快照（那时还是 NULL），
`FinalizeOwnedRun` 只在 `run.StartedAt != nil` 时观测 `studio_run_duration` —— 第四轮之后该指标对**所有** run 静默失效。

- 新增查询 `GetRunStartedAt`（`status='running' AND lease_epoch=?`）；
- `MarkRunStartedOwned` 改为 `(time.Time, error)`：在同事务内 stamp 后读回 **DB 时间**（`COALESCE` 保留首次），非本地生成；
- worker：`claimed.Run.StartedAt = &startedAt`。

**测试**：`tests/integration/review5_started_at_test.go`（新增 5 个）

- `MarkRunStartedOwned` 返回值 === DB 行值，二次调用返回**首次**时间（COALESCE）；
- worker handler 拿到的 `ClaimedRun.StartedAt != nil`（回退验证失败信息："in-memory snapshot was never refreshed"）；
- 已启动 run → `studio_run_execution_seconds` 恰好 1 个观测；Gate1 kill（无 started_at）→ 0 个观测。

---

## 八、P2-5 cleanup 边界 + provider_id orphan

### 8.1 有界 cleanup context

**新增** `internal/execution/cleanup.go`：

```go
const CleanupTimeout = 5 * time.Second

func NewCleanupContext(parent context.Context) (context.Context, context.CancelFunc)
```

- 从 parent **取 values**（trace/log 关联不断），但**脱离 parent 的取消与 deadline**；
- 不用 `context.WithoutCancel(parent)`：worker 的 parent 是执行 ctx，`maxRuntime` 一到 deadline 就在过去，继承它会导致每次 cleanup 立刻失败 —— 正是要避免的孤儿化；
- 统一替换 6 处：`gate.go` 的 `deferClaimed`、`worker.go` 的 slot Release ×2、panic retry、admission requeue、gate defer。

**测试**：`internal/execution/cleanup_test.go`（4 个）：父取消仍可用 / 有 ~5s deadline / 丢弃已过期父 deadline / nil parent 安全。

### 8.2 迁移 0019

`db/migrations/0019_full_reconcile_binding_provider_id.{up,down}.sql`（**不改 0017**）：

```sql
UPDATE runtime_bindings b
LEFT JOIN providers p ON p.provider_key = b.provider_key
SET b.provider_id = p.id
WHERE (p.id IS NULL AND b.provider_id IS NOT NULL)
   OR (p.id IS NOT NULL AND (b.provider_id IS NULL OR b.provider_id <> p.id));
```

0017 是 INNER JOIN，修不了"provider_key 已不存在、provider_id 仍指向另一个活着的 provider"的行；0019 补齐，且 key 解析不到时把派生列置 NULL。幂等。

**测试**：`tests/integration/review5_binding_provider_id_test.go`（4 个）——先跑 0017 证明它修不了 orphan，再跑 0019 断言置 NULL；错指 / NULL 修复；二次执行 no-op + 全表 0 条不一致。

---

## 九、CI 门（本机实跑结果）

| 门 | 结果 |
|---|---|
| actionlint | PASS（`go install` 后实跑，无告警） |
| `gofmt -l .` | PASS（空） |
| `go vet ./...` | PASS |
| `go build ./...` | PASS |
| unit（`go test ./...`） | PASS（13 个包） |
| `-race` | **本机跑不了**：无 gcc（`cgo: C compiler "gcc" not found`），由 CI Ubuntu 执行 |
| MySQL 5.7 migration | PASS（`cmd/migrate` 连跑两次，第二次 no-op，版本到 19） |
| DB-backed（`STUDIO_TEST_DB=1 go test ./...`） | PASS |
| integration（`STUDIO_TEST_DB=1 STUDIO_TEST_REDIS=1`） | PASS（`tests/integration` 47.9s） |
| frontend typecheck（`tsc --noEmit`） | PASS |
| frontend unit（vitest） | PASS（15 files / 125 tests） |
| frontend production build（`tsc && vite build`） | PASS |

---

## 十、遗留与提醒

1. **共享开发库有测试残留**：invariant checker 报 228 条（`outbox_backlog_age` 12122、`scheduled_run_occurrence_mismatch` 173、`terminal_without_terminal_event` 49、`running_without_lease` 5），均为历次集成测试累积的 fixture，不属于本次改动引入（但其中 5 条 `legacy_interrupted` 无终态事件正是 0018 第 3 步要修的形状，已在迁移内补齐）。冒烟前建议换干净库。
2. `-race` 与 actionlint 之外的所有门均在本机复现；`-race` 只能在 CI 验证。
3. 本次未提交；如需推 `dev` 跑 CI，请确认后再执行。
