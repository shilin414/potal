# potal 第四轮复审整改变更报告

**基线提交**：`716ee58`（复审报告 §二十六 的发布判定基准）
**目标**：关闭复审报告的两个 P1，并完成 §二十四「下一批」的全部 P2 收口项
**结论**：两个 P1 已关闭；P2 六项 + §十七（`started_at` 语义）全部落地；本地测试门全绿

---

## 一、总览

| 项 | 来源 | 内容 | 状态 |
|---|---|---|---|
| P1-1 | §5–§11 | Aily Gate 2 后移到**真实 Provider Submit 之前**；Streaming 同步 Open；attempt 只由真实提交消耗 | 已关闭 |
| P1-2 | §12–§16 | 前端正确消费 `run.cancelled`（不再映射成 `done`），顺带消费 `run.deferred` / `run.started` | 已关闭 |
| P2-1 | §18 | 非终态 Run 谓词统一为 `status NOT IN ('cancelled','succeeded','failed')` | 已完成 |
| P2-2 | §19 | Schedule Service 改用 DB 时钟 | 已完成 |
| P2-3 | §20 | RateLimiter：`ctx` 取消不当 Redis 故障；`Redis=nil` 不再绕过限流 | 已完成 |
| P2-4 | §21 | Staff 诊断：`ErrExecutionProviderMissing`→409；私有 Application staff 分支去掉 `!app.IsPublic` | 已完成 |
| P2-5 | §22 | Snapshot 注释修正（不写 capabilities） | 已完成 |
| P2-6 | §23 | 新增迁移 `0017_reconcile_binding_provider_id` | 已完成 |
| P2-7 | §17 | `started_at` = 真正允许执行的时间（claim 不再盖章） | 已完成 |

---

## 二、P1-1：Gate 2 后移到真实 Provider Submit 之前

### 2.1 问题

第三轮把 Gate 2 放在 `Execute()` 公共入口、`BeginProviderAttempt` 与 `thread()` 之前。距真实 Provider HTTP Submit 仍隔着：

- `thread()` 的多次 DB IO
- 内容/附件本地校验
- Streaming 路径的 goroutine 调度（旧实现在 goroutine 内发 POST）

而且旧测试只统计 helper 的 `submits++`，**没有证明 Provider 未被触达**（报告 §6）。

### 2.2 改动

**`internal/integrations/aily/client.go`**

- 新增 `OpenStreamChat(...)`：在**调用方 goroutine 内**构造请求、`http.Client.Do`、校验非 200（读 body 后 classify），成功返回 `resp.Body`。
- `StreamChat` 退化为 `OpenStreamChat` + `defer body.Close()` + `pumpSSE`。
- 新增 `pumpSSE(ctx, body io.Reader, fn)`：只做 SSE 帧解析，EOF 视为正常结束。

→ 消除了「Gate2 → 等 goroutine 被调度 → Provider Submit」这段窗口。

**`internal/integrations/aily/adapter.go`**

- 新增 `ProviderAPI` 窄接口（StartChat / OpenStreamChat / GetChatResult / UploadAttachment / GetArtifact / CheckVisibility），`var _ ProviderAPI = (*Client)(nil)`。
- `AgentAdapter.client *Client` → `api ProviderAPI`；新增 `NewAgentAdapterWithAPI(api, auth)`，原构造函数转调 `newAgentAdapter`。
- 拆出**无 IO 的本地校验** `ValidateSubmit(in)`（`ValidateContent` + `ValidateAttachments`）。
- `Submit = ValidateSubmit + SubmitPrepared`；`Stream = ValidateSubmit + StreamPrepared`（`StreamPrepared` 同步 `OpenStreamChat`，goroutine 只消费帧）。

→ 这个 seam 让「Provider 是否被调用」变成可观测、可断言的事实。

**`internal/integrations/aily/executor.go`**

- 从 `Execute()` 公共入口**移除** Gate 2 与 `BeginProviderAttempt`，改由两条真实提交路径在进入 Provider 前调用。
- 新增：

```go
type preSubmitStop struct{ err error }

func (e *Executor) beginSubmit(ctx, claimed) (proceed bool, err error) {
    if execution.PreSubmitGate(ctx, e.Owned, claimed, e.Gate, e.Log) {
        return false, nil // gate 已 kill/pause：run 不再由此路径推进
    }
    if err := e.Owned.BeginProviderAttempt(ctx, claimed); err != nil {
        if errors.Is(err, execution.ErrProviderAttemptsExhausted) {
            return false, e.failRun(ctx, claimed, "aily_attempts_exhausted",
                "provider retry budget exhausted before submit")
        }
        return false, err
    }
    return true, nil
}
```

- `executeStreaming` / `executeBackground`：`build submit` → `ValidateSubmit` → `beginSubmit` → `StreamPrepared` / `SubmitPrepared`。
- `classifyError` 开头 `errors.As(err, &stopped)` 直接返回内层错误，不二次归类；新增 `ErrCapability` → `aily_capability_error`。

### 2.3 语义结果

| 场景 | HTTP 调用 | attempt |
|---|---|---|
| 应用被禁用（Gate kill） | 0 | 0 |
| Provider 停用（Gate pause） | 0 | 0（requeue 保优先级） |
| 本地内容/附件校验失败 | 0 | 0 |
| 正常提交 | 1 | 1 |

### 2.4 边界说明（报告 §10 已认可）

Gate 2 之后仍剩「attempt CAS + 一次网络调用」这一极小 TOCTOU 窗口。按报告结论，**不引入 Execution Generation / version token**（成本远高于收益），而是把所有可等待步骤移到 Gate 2 之前——本次整改即这一定义的生产级实现。

---

## 三、P1-2：前端正确消费 `run.cancelled`

### 3.1 根因

reducer 无 `run.cancelled` 分支，`finalizeRun()` 只判 `failed/interrupted` → Hard Kill（`error_code=execution_disabled`）被映射成 `done`，用户看到一条空白的「成功回答」。

### 3.2 改动

**`frontend/src/stores/useRunChatStore.ts`**

- `ChatMessage.status` 增加 `'cancelled'`。
- 新增 `cancelledNotice(errorCode)`：`execution_disabled` → 「应用或运行配置已停用，本次执行已取消。」，否则「执行已取消」。
- `applyEvent` 新增：
  - `run.cancelled` → `status='cancelled'`、`error=cancelledNotice(...)`、清空 `activeRunId`
  - `run.started` → 清除 `retryNotice`
  - `run.deferred` → 仍在 `streaming` 时按 `reason` 写 `retryNotice`（`provider_disabled` / `run_gate_unavailable` / 默认）
- `finalizeRun` 导出并改为三分支：`cancelled` / `failed|interrupted` / 其它（done）。

**`frontend/src/components/Chat/RunChatPanel.tsx` + `.css`**

- 新增「已取消」标签（warning 色，不是错误红），Tooltip 显示取消原因；错误文案在 cancelled 时套用 warning 样式。

---

## 四、P2 收口

### 4.1 非终态 Run 谓词（§18）

`db/queries/conversation.sql::CountActiveRunsByConversation` 与 `db/queries/execution.sql::CountOutstandingRunsByUser` 由 `status IN ('queued','running')` 改为：

```sql
status NOT IN ('cancelled', 'succeeded', 'failed')
```

domain 有 9 个状态，真正终态只有 3 个；`waiting_input / waiting_external / cancelling / interrupted` 必须计入在途，否则 Conversation 串行约束与 per-user 上限会被绕过。已 `sqlc generate` 重新生成。

### 4.2 Schedule Service DB 时钟（§19）

`internal/automation/schedule/service.go` 新增 `dbNow(ctx)` / `dbNowTx(ctx, q)`（DB 不可用时降级本机时钟）；`validate` 拆出 `validateAt(ctx, in, owner, now)`，使 `Create` / `Update` / `SetEnabled` 的校验与 `NextRunAfter` **锚定同一 DB 时钟读数**。

### 4.3 RateLimiter 两个边界（§20）

- `internal/execution/ratelimit.go`：`AllowKey` 在 Redis 返回 error 时**先判 `ctx.Err()`**，`context.Canceled/DeadlineExceeded` 直接返回错误，不再被误当成 Redis outage 走本地降级。
- `internal/transport/http/run_handlers.go`：`admitUserRun` 去掉 `s.Redis == nil` 直接放行的分支；无 Redis 时仍走 `AllowKey` 的本地 per-key 降级（fail-closed，不是 fail-open）。

### 4.4 Staff 执行诊断（§21）

- `writeExecutionDenied`：`catalog.ErrExecutionProviderMissing` → **409**（原落到 404）。
- `internal/catalog/authz.go`：staff 分支去掉 `if !app.IsPublic { return ErrExecutionForbidden }`——staff 只绕过 visibility，私有应用也应拿到 `ErrNoBinding` 精确诊断。

### 4.5 Snapshot 注释（§22）

`bindingFromExecutionAuthRow` 注释更正：它恢复 timeout/config/capabilities；`Snapshot()` 只冻结 timeout/config 与运行期身份字段，**capabilities 留在 Binding 上供 `EffectiveCapabilities()` 使用**。

### 4.6 provider_id 数据一致性（§23）

新增 `db/migrations/0017_reconcile_binding_provider_id.{up,down}.sql`：

```sql
UPDATE runtime_bindings b
JOIN providers p ON p.provider_key = b.provider_key
SET b.provider_id = p.id
WHERE b.provider_id IS NULL OR b.provider_id <> p.id;
```

0016 只修 NULL；0017 顺带修「指向错误 provider」的历史行。执行安全不受影响（授权与 Kill Switch 均按 `provider_key` 判定），属纯数据卫生。`down` 为 `SELECT 1;`（不可逆）。

### 4.7 `started_at` 语义（§17）

报告推荐方案二：`started_at` = 真正允许执行的时间。

- `CASClaimRun` **不再**写 `started_at`。
- 新增 `MarkRunStartedFenced`：`SET started_at = COALESCE(started_at, CURRENT_TIMESTAMP(3)) WHERE id=? AND status='running' AND lease_epoch=?`。
- 新增 `Service.MarkRunStartedOwned(ctx, own)`（fenced，校验所有权）。
- `worker.go` 在 **Gate 1 allow 之后、`run.started` 事件之前**调用；`ErrLostOwnership` 则停止，其它错误仅 warn（可观测性损失，不是正确性损失）。
- `finalize.go` 补 nil 保护：`run.StartedAt != nil && !run.StartedAt.IsZero()`——`StartedAt` 是 `*time.Time`，原写法在 NULL 时会 panic。

COALESCE 保证 Provider 停用导致的 defer/re-claim 保留**首次**开始时间，`RunDuration` 量的是执行墙钟而非最后一次 requeue。

---

## 五、新增测试

**`tests/integration/review4_gate_submit_test.go`（新建）** — 基于 `ProviderAPI` seam 的 `submitRecorder`，统计真实 `StartChat` / `OpenStreamChat` 调用：

| 用例 | 断言 |
|---|---|
| `TestDisabledApplicationNeverReachesStartChat`（background + interactive） | HTTP=0、`cancelled`、`error_code=execution_disabled`、attempt=0 |
| `TestInactiveProviderNeverReachesStartChat` | HTTP=0、`queued`、优先级保持 `DefaultPriority`、attempt=0、`available_at` 在未来 |
| `TestAilyBackgroundGateRunsAfterThreadPreparation` | `agent_threads=1`（thread 解析在 Gate 之前）且 StartChat=0 |
| `TestAilyStreamingGateRunsImmediatelyBeforeOpenStream` | Provider 被调用时（`onSubmit` 钩子内）已 attempt=1、threads=1 |
| `TestAilySubmitHappyPathWithGateAllowed` | 对照组：StartChat=1 且 succeeded |
| `TestLocalPreparationFailureDoesNotConsumeAttempt` | 内容超长 → HTTP=0、failed、`aily_capability_error`、attempt=0 |
| `TestNonTerminalStatusBlocksSecondTurn` | 6 个非终态子状态均阻止第二轮 |
| `TestTerminalStatusFreesTheConversation` | 终态释放会话 |
| `TestNonTerminalRunCountsAgainstUserOutstandingCap` | 非终态计入 per-user 上限 |
| `TestStaffDiagnosesPrivateApplicationMissingBinding` | 私有应用 staff 也能拿到精确诊断 |

**`tests/integration/review4_started_at_test.go`（新建）**

- `TestKilledRunHasNoStartedAt`：Gate kill 的 run → `started_at IS NULL`
- `TestAllowedRunStampsStartedAt`：Gate allow → 有值；再次盖章 → 保持首次值（COALESCE）

**`internal/transport/http/execution_denied_test.go`（新建）**

- `TestWriteExecutionDeniedProviderMissingIsConflict` → 409
- `TestWriteExecutionDeniedLeaksNothingToRegularCallers` → 404

**前端 `src/stores/__tests__/useRunChatStore.test.ts`**（21 tests）

- `run.cancelled` → `cancelled` 且 `activeRunId` 清空、`execution_disabled` 显示停用原因
- `run.deferred` → 仍 `streaming` 且显示等待原因（两种 reason）
- `run.started` → 清除等待提示
- `finalizeRun` → `cancelled` 保持 cancelled（P1-2 回归）、failed/succeeded 各自映射

---

## 六、验证结果（本地）

| 门 | 结果 |
|---|---|
| `gofmt -l`（本次改动文件） | 空（仓库内 `recovery/retry/slots/transactions.go`、`provider_slots_test.go` 为既有 CRLF 噪声，未改动，不处理） |
| `go vet ./...` | 通过 |
| `go build ./...` | 通过 |
| `go test ./... -count=1` | 13 个包全 ok |
| `STUDIO_TEST_DB=1 STUDIO_TEST_REDIS=1 go test ./tests/integration/... -count=1` | ok（62s） |
| MySQL 5.7 迁移 | `TestMigrateUpAgainstRealDatabase` 通过；`schema_migrations version = 17`；0017 的 NULL 与错指两种不一致均被纠正（临时脚本验证后已删除） |
| 前端 `vitest run` | 15 files / 122 tests 全绿 |
| 前端 `tsc --noEmit` | 无错 |
| `go test -race execution/delivery/automation` | **本机无法执行**：`-race` 需要 cgo，本机无 gcc。由 CI 的 Ubuntu job 覆盖（既有约定） |

---

## 七、遗留（交给下一轮，均非上线阻断）

- SSE Hub 多路复用、Worker 阻塞 dispatcher、RunEvent 去 COUNT
- `client_request_id` 幂等键、Sidebar keyset 分页
- Conversation soft-delete（当前硬删有守卫）
- RateLimiter 其余边角
- 复审建议：Admission / Kill-Switch 主链不再小修，审查重点切到其它子系统
