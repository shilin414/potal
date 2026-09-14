# Creation Agent Studio — Execution Correctness Closure 修复变更报告

- 修复依据：
  - `docs/Creation Agent Studio 未来演进完整架构文档.md`
  - `docs/potal 二次架构评测报告（Execution Correctness Hardening 复核）.md`
  - `docs/Creation Agent Studio Execution Correctness Closure 修复执行开发计划.md`
- 目标：把 Execution Plane 的 **Ownership / Lease / Retry / Terminal State** 一致性问题全部关闭，使 Run 执行内核达到正式生产运行的基础正确性标准。
- 结论：**P0 / P1 / P2 中本轮计划内的全部问题已关闭，后端与前端全量回归通过。**

---

## 1. 总体结论

本轮修复后，执行内核已经具备以下不可绕过的正确性结构：

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

具体保证：

1. **running ⇔ active lease**：claim、reaper、retry、finalize 全部单事务，不再存在 `running + no lease` 的孤儿窗口。
2. **Ownership 不可丢失**：`ExecutionOwnership` 与 `Run` 彻底分离，`GetRun()` 刷新永远无法覆盖或丢失 `lease_token`。
3. **Retry 语义唯一**：重试只发非终态 `run.retrying`；终态只可能是 `run.completed` / `run.failed` / `run.cancelled`。
4. **Terminal 原子性**：Run 终态 CAS、terminal RunEvent、assistant Message、occurrence 收敛、lease 清理在**同一个 TiDB 事务**内完成。
5. **Worker 写面收敛**：Provider Executor 只能拿到 `WorkerOwnedService`，没有任何绕过 fence 的写接口。
6. **Scheduler 多实例安全**：admission 在 `schedules` 行锁内完成，FIFO、一次只 admit 一个。
7. **Admin 认证收口**：本地密码登录 staff-only，限流 + 审计；旧 `/api/auth/login/` 标记 deprecated 且同样收口。
8. **可观测性闭环**：新增 invariant checker、Prometheus 指标、CI 门禁。

---

## 2. 问题修复对照表

### 2.1 P0：Execution Correctness

| 问题 | 修复结果 | 关键落点 |
|---|---|---|
| **P0-1 Reaper recovery 非原子**（先删 lease 再改 Run，crash 后产生 `running + no lease`） | ✅ 关闭。`RecoverExpiredLeases` 对每个 run 执行单事务：锁 expired lease → 锁 run → 校验 epoch → requeue/fail → 写事件 → 写 outbox → 删 lease，整体 commit/rollback | `internal/execution/recovery.go` |
| **P0-2 `ReleaseInterrupted` 存在 unfenced side effect**（stale worker 可写事件 / 建 outbox / 删新 owner 的 lease） | ✅ 关闭。旧的 `ReleaseInterrupted` / `ReleaseInterruptedFenced` 路径删除，改为语义明确的 `RetryOwnedRun` / `FailOwnedRun`，全部写操作都在事务内且受 fence 约束 | `internal/execution/retry.go`、`internal/execution/finalize.go` |
| **P0-3 `run.interrupted` 同时表示“重试”和“终态”**（前端提前关闭 SSE、清空 activeRunId） | ✅ 关闭。重试只发非终态 `run.retrying`；终态集合收敛为 `run.completed` / `run.failed` / `run.cancelled`。前端收到 `run.retrying` 保持 streaming 并保留 `activeRunId`，历史 `run.interrupted` replay 兼容渲染为失败 | `internal/execution/domain.go`、`internal/transport/sse/sse.go`、`frontend/src/services/runStream.ts`、`frontend/src/stores/useRunChatStore.ts` |
| **P0-4 Aily `retryOrFail()` 绕过 fenced release** | ✅ 关闭。Aily Executor 全面改走 `WorkerOwnedService`，retry 路径调用 `RetryOwnedRun`，stale worker 直接得到 `ErrLostOwnership` | `internal/integrations/aily/executor.go` |
| **P0-5 Background execution refresh 丢失 Ownership**（`GetRun()` 覆盖 `lease_token`） | ✅ 关闭。`ExecutionOwnership` 与 `Run` 拆分，`ClaimedRun.RefreshRun()` 只刷新业务数据，Ownership 在 attempt 生命周期内不可变 | `internal/execution/ownership.go` |
| **P0-6 Worker canonical writes 可绕过 Ownership** | ✅ 关闭。新增 `WorkerOwnedService` 作为 Worker 唯一写面；`AppendEvent` / `UpdateExternalRunID` / `Retry` / `Fail` / `Finalize` / `PersistArtifact` / `BindProviderSession` 全部 Owned 化 | `internal/execution/ownership.go`、`internal/execution/service.go` |

### 2.2 P1：Canonical State 完整性

| 问题 | 修复结果 | 关键落点 |
|---|---|---|
| **P1-1 Artifact persistence fencing**（stale worker 可写 artifact / event；event 使用预生成 ID） | ✅ 关闭。`PersistArtifactOwned` 单事务完成：verify ownership → upsert artifact → 读回真实 row → 用**真实 artifact id** 发 `artifact.discovered`。stale worker 既写不了 artifact，也发不了 event | `internal/execution/artifacts.go` |
| **P1-2 AgentThread session fencing**（stale worker 可覆盖 conversation 的 provider session） | ✅ 关闭。`BindProviderSessionOwned` 在事务内先校验 run ownership，再做 set-once / idempotent / conflict 语义；owner 可 rebind 新 session，stale worker 一律 `ErrLostOwnership` | `internal/execution/artifacts.go` |
| **P1-3 Terminal Run + RunEvent 非原子**（Run 已 succeeded 但 terminal event 丢失） | ✅ 关闭。`FinalizeOwnedRun` 单事务完成 terminal CAS + terminal RunEvent + assistant Message + occurrence terminal + lease 删除；Redis publish 在 commit 后 | `internal/execution/finalize.go` |
| **P1-4 Assistant Message 与 Run Finalize 一致性**（Run 成功但回答未落库） | ✅ 关闭。assistant Message 与 finalize 同事务写入，不再有“重新打开会话答案消失”的窗口 | `internal/execution/finalize.go` |
| **P1-5 Scheduler multi-instance admission 竞态**（两个 Scheduler 同时 admit 同一 Schedule 的两个 pending） | ✅ 关闭。admission 事务内先 `SELECT ... FOR UPDATE` 锁 schedules 行，再按 `ORDER BY enqueued_at, id` FIFO admit，且一次只 admit 一个 | `internal/automation/scheduler/scheduler.go`、`db/queries/automation.sql` |
| **P1-6 AdminLogin 未强制 staff-only**（非管理员 + 本地密码可登录） | ✅ 关闭。`VerifyLocalAdmin` 要求 `is_staff || is_superuser`；旧 `/api/auth/login/` 保留为 deprecated + staff-only + 限流 + 审计 | `internal/identity/repo.go`、`internal/transport/http/identity_handlers.go` |

### 2.3 P2：Production Hardening

| 问题 | 修复结果 | 关键落点 |
|---|---|---|
| **P2-1 Invariant checker** | ✅ 完成。新增 `cmd/invariant-checker`，周期性扫描 A–G 七类 invariant：running 无 lease、queued 带 lease、terminal 带 lease、terminal 缺 terminal event、lease epoch 不一致、schedule overlap 违例、outbox backlog 超龄。只 detect / metric / log / alert，不自动修复 | `internal/execution/invariant/checker.go`、`cmd/invariant-checker/main.go` |
| **P2-2 Degraded rate limiter metric** | ✅ 完成。新增 `studio_execution_invariant_violation_total{type}` 与 `studio_provider_limiter_degraded` | `internal/platform/telemetry/telemetry.go` |
| **P2-3 Redis outage limiter test** | ✅ 完成。`TestLimiterRedisOutageLocalFallback` 用不可达 Redis 验证本地 GCRA 兜底不 fail-open 且 `Degraded() == true` | `internal/execution/ratelimit_test.go` |
| **P2-4 CI** | ✅ 完成。新增 backend / frontend GitHub Actions；backend 包含 gofmt、vet、build、unit、真实 TiDB + Redis 的 integration；frontend 包含 tsc、vitest、vite build | `.github/workflows/backend.yml`、`.github/workflows/frontend.yml` |

---

## 3. 核心架构调整

### 3.1 Ownership 与 Run 彻底分离

新增不可变的 `ExecutionOwnership`：

```go
type ExecutionOwnership struct {
    RunID      ids.ID
    WorkerID   string
    LeaseEpoch uint64
    LeaseToken ids.ID
}
```

配套 `ClaimedRun`：

```go
type ClaimedRun struct {
    Run       *Run
    Ownership ExecutionOwnership
}
```

规则：

- `ClaimRun()` 产生 `ClaimedRun`。
- `GetRun()` / `RefreshRun()` 只能替换业务数据 `Run`。
- `Ownership` 在本次 attempt 生命周期内**不可刷新、不可从数据库重新获取、不可被新 Run 覆盖**。
- Worker 只能拿到 `WorkerOwnedService`，所有写方法都强制携带 `ExecutionOwnership`。

这从类型层面消灭了评测报告第十章指出的 Background 路径丢 token 问题。

### 3.2 Retry 状态机重构

`RetryOwnedRun` 单事务完成：

```text
verify ownership
↓
CAS running → queued
↓
append run.retrying (non-terminal)
↓
INSERT outbox run.dispatch
↓
DELETE own lease
↓
COMMIT
```

终态事件唯一化：

```text
run.completed
run.failed
run.cancelled
```

`run.retrying` 永远不关闭 SSE。前端 reducer 收到 `run.retrying` 时：

- 保持 `activeRunId`
- 保持 `status = streaming`
- 显示“正在重试…”
- 不清空会话、不 finalize

历史 `run.interrupted` 事件仍可 replay，前端兼容渲染为失败，避免旧数据无法展示。

### 3.3 Reaper 原子恢复

`recoverExpiredLeaseTx` 单事务流程：

```text
BEGIN
SELECT expired lease FOR UPDATE
SELECT run FOR UPDATE
校验 run.status = running
校验 run.lease_epoch = lease.lease_epoch
retryable  → requeue + run.retrying + outbox dispatch
exhausted  → failed + run.failed
DELETE lease by token
COMMIT
```

关键点：

- 不再先删 lease 再改 Run，`running + no lease` 的 crash 窗口被彻底关闭。
- heartbeat 先到则事务直接 no-op，owner 继续持有。
- epoch 不匹配的 stale lease 只清理 bookkeeping，不碰 run。

### 3.4 Finalize 单事务

`FinalizeOwnedRun` 单事务完成：

```text
1. verify ownership (FOR UPDATE + epoch)
2. terminal RunEvent
3. terminal CAS (running → succeeded/failed/cancelled)
4. assistant Message（如有最终回答）
5. ScheduleOccurrence terminal（scheduled run）
6. DELETE own lease
COMMIT
```

Redis pub/sub、metrics、delivery hook 全部在 commit 后执行，属于 UX / 延迟优化，不再影响正确性。

> 说明：terminal RunEvent 在 CAS 之前写入，因为 fence predicate 要求 `status='running'`；此时 run 行已被本事务 `FOR UPDATE` 锁定且 epoch 已验证，CAS 不可能丢失，事务整体仍然原子。

### 3.5 Aily Executor 全路径 Owned 化

`internal/integrations/aily/executor.go` 重写后：

- Background refresh 使用 `ClaimedRun.RefreshRun()`，不再覆盖 ownership。
- `run.poll` 等事件走 `AppendOwnedEvent`。
- retry 走 `RetryOwnedRun`。
- fail / finalize 走 `FailOwnedRun` / `FinalizeOwnedRun`。
- artifact 走 `PersistArtifactOwned`。
- provider session 绑定走 `BindProviderSessionOwned`。

stale worker 在任何写路径上都会得到 `ErrLostOwnership` 并立即停止。

---

## 4. 数据库迁移

新增：

```text
backend-go/db/migrations/0010_run_lease_epoch_link.up.sql
backend-go/db/migrations/0010_run_lease_epoch_link.down.sql
```

内容要点：

- `run_leases` 增加 `lease_epoch`，与 `runs.lease_epoch` 对齐。
- Claim 时写入 `runs.lease_epoch == run_leases.lease_epoch`。
- Reaper / invariant checker 可直接验证 expired lease 是否属于当前 run epoch。

已通过 `go run ./cmd/api -migrate` 应用到本地 dev TiDB 并通过集成测试。

---

## 5. 测试矩阵（T1–T13）

新增 `tests/integration/correctness_closure_test.go`，并扩展既有 fencing / CAS / ratelimit 测试。

| 测试 | 验证目标 | 状态 |
|---|---|---|
| `TestReaperRecoveryAtomicRetry` | T1：reaper 单事务 retry，无 `running + no lease` 窗口 | ✅ |
| `TestReaperRecoveryAtomicFail` | T1：attempt 耗尽时单事务 fail + terminal event | ✅ |
| `TestStaleWorkerRetryFenced` | T2：stale worker 无法 requeue / 建 outbox / 删新 owner lease | ✅ |
| `TestRetryEventNonTerminal` | T3：`run.retrying` 非终态，SSE 不关闭 | ✅ |
| `TestBackgroundRefreshPreservesOwnership` | T4：Background refresh 后 lease token 不丢，finalize 后 lease 删除 | ✅ |
| `TestBackgroundRefreshPreservesOwnership`（stale 分支） | T5：stale background worker 无法写 `run.poll` / finalize | ✅ |
| `TestArtifactFencing` | T6：stale worker 无法写 artifact / artifact event | ✅ |
| `TestArtifactRediscoveryStableID` | T7：同一 external artifact 重复发现得到同一 local id | ✅ |
| `TestProviderSessionFence` | T8：stale worker 无法覆盖 provider session，owner rebind 语义正确 | ✅ |
| `TestFinalizeTransactionDurability` | T9/T10：terminal CAS + terminal event + assistant message 原子，terminal 必有事件 | ✅ |
| `TestDualSchedulerPendingAdmissionNoParallel` | T11：双 Scheduler 并发 admission，active occurrence ≤ 1 | ✅ |
| `TestAdminLoginRejectsNonStaff` | T12：非 staff + 本地密码登录返回 401 | ✅ |
| `TestLimiterRedisOutageLocalFallback` | T13：Redis 不可达时本地 GCRA 兜底，不 fail-open，`Degraded()=true` | ✅ |

既有回归同步更新并通过：

- `TestOutboxRelayCanonicalUUID`
- `TestClaimRunAtomicity`
- `TestLeaseFencingStaleWorker`
- `TestHeartbeatFencedByToken`
- `TestCASRaceSingleWinner`
- `TestDuplicateQueueMessagesNoDoubleExecution`
- `TestLeaseExpiryAndReaper`

前端新增单测：

- `run.retrying keeps the run active and streaming`
- `run.retrying followed by run.completed converges normally`
- `legacy run.interrupted (historical replay) renders as failure`

---

## 6. 全量回归结果

### 6.1 后端

```text
gofmt        ✅ clean
go build     ✅
go vet       ✅
go test      ✅ 10/10 packages
integration  ✅ STUDIO_TEST_TIDB=1 STUDIO_TEST_REDIS=1 全部通过
```

实际执行命令：

```bash
cd backend-go
gofmt -l .
go build ./...
go vet ./...
go test ./... -count=1
STUDIO_TEST_TIDB=1 STUDIO_TEST_REDIS=1 go test ./tests/integration/... -count=1
```

### 6.2 前端

```text
npm test      ✅ 15 files / 115 tests passed
npm run build ✅ tsc + vite build passed
```

实际执行命令：

```bash
cd frontend
npm test -- --run
npm run build
```

### 6.3 变更规模

- 后端 + 前端 tracked diff：约 `37 files changed, 1296 insertions(+), 706 deletions(-)`
- 另有新增未跟踪文件：ownership / retry / recovery / finalize / artifacts / invariant checker / CI workflows / 0010 迁移 / correctness closure 测试套件
- 为满足 CI gofmt 门禁，对 35 个 Go 文件做了机械 gofmt（主要是注释缩进规范化），不影响语义。

---

## 7. 关键文件清单

### 7.1 新增

```text
backend-go/internal/execution/ownership.go
backend-go/internal/execution/retry.go
backend-go/internal/execution/recovery.go
backend-go/internal/execution/finalize.go
backend-go/internal/execution/artifacts.go
backend-go/internal/execution/invariant/checker.go
backend-go/cmd/invariant-checker/main.go
backend-go/db/migrations/0010_run_lease_epoch_link.up.sql
backend-go/db/migrations/0010_run_lease_epoch_link.down.sql
backend-go/tests/integration/correctness_closure_test.go
.github/workflows/backend.yml
.github/workflows/frontend.yml
```

### 7.2 主要修改

```text
backend-go/internal/execution/service.go
backend-go/internal/execution/worker.go
backend-go/internal/execution/domain.go
backend-go/internal/integrations/aily/executor.go
backend-go/internal/automation/scheduler/scheduler.go
backend-go/internal/identity/repo.go
backend-go/internal/transport/http/identity_handlers.go
backend-go/internal/transport/http/server.go
backend-go/internal/transport/sse/sse.go
backend-go/internal/platform/telemetry/telemetry.go
backend-go/db/queries/execution.sql
backend-go/db/queries/conversation.sql
backend-go/db/queries/automation.sql
backend-go/db/queries/identity.sql
backend-go/internal/gen/db/*  (sqlc 重新生成)
frontend/src/services/runStream.ts
frontend/src/stores/useRunChatStore.ts
frontend/src/stores/__tests__/useRunChatStore.test.ts
frontend/src/components/Chat/RunChatPanel.tsx
```

---

## 8. 遗留项（本轮明确不处理）

以下两项在开发计划中被定义为后续迭代，不阻塞本轮 correctness closure：

1. **Aily 附件流式上传**：当前仍是整文件读入内存，后续改为 streaming upload。
2. **Legacy Workflow RuntimeAdapter 收敛**：旧 workflow 路径还未完全 RuntimeAdapter 化。

其他可后续增强：

- 引入完整 `run_attempts` 表，记录每次 attempt 的独立生命周期。
- 将 delivery fan-out 从内存 hook 迁移到 domain outbox 事件（`scheduled_run.succeeded`）。
- `run.cancelled` 目前作为终态保留；若产品侧引入显式取消，可复用同一 finalize 事务。

---

## 9. 结论

本轮 Execution Correctness Closure 已经完成：

```text
Reaper 原子恢复         ✅
Retry 状态机唯一化       ✅
Aily Ownership 全闭环   ✅
Artifact / Session fence ✅
Finalize 事务一致        ✅
Scheduler 多实例安全     ✅
Admin 认证收口           ✅
Invariant + Metrics + CI ✅
```

Execution Plane 现在满足：

> **所有 Worker canonical writes 都必须持有不可伪造的 Ownership。**

可以进入下一阶段的 Aily 附件流式上传与 Legacy Workflow RuntimeAdapter 收敛。
