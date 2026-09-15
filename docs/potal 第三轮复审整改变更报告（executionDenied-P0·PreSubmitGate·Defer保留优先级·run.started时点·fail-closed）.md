# potal 第三轮复审整改变更报告

> 基线：dev `12d98f873af2739cba16947e737063a99ffc8990`（二轮整改后）
> 依据：《potal 第三轮代码复审暨整改执行报告》（84 / B+，1 P0 / 2 P1 / 2 P2）
> 日期：2026-09-15

---

## 一、整改总览

| 项 | 等级 | 内容 | 状态 |
|---|---|---|---|
| P0-A | P0 | `executionDenied(nil)` 条件反转 → nil 判成功 | ✅ 修复 |
| P1-B | P1 | Pre-Submit Gate（Gate 2）贴到 Provider Submit 前 | ✅ 完成 |
| P1-C | P1 | Provider Pause 改用 Defer（保持原优先级 + Occurrence 同步） | ✅ 完成 |
| P2-D | P2 | `run.started` 移到 Gate allow 之后 | ✅ 完成 |
| P2-E | P2 | 未知 GateAction fail-closed | ✅ 完成 |

预期评级：92 / A-（仅 P0）→ **94~95 / A（全量完成后，待复审确认）**。

---

## 二、P0-A：`executionDenied(nil)` 把授权成功判成拒绝

### 根因
`internal/app/app.go` 中分类器以 `err == nil ||` 开头，导致 `executionDenied(nil) == true`：
每一次**成功授权**都被当作策略拒绝。影响面：
- Schedule Create / Update（`schedulableChecker`）→ 合法应用报 `ErrApplicationNotSchedulable`
- Scheduler 自动触发（`bindingResolver.EnabledBindingFor`）→ binding=nil → occurrence=failed → 时间槽永久丢失
- Run Now → `ErrNotSchedulable`

上一轮 CI 全绿的原因：单元测试把 `nil` 放进了 policy 断言列表，**把错误逻辑固定成了"正确行为"**；集成测试用 `flakyResolver` 直接 stub 掉了 `authorizeForOwner → executionDenied` 链路。

### 修改（`internal/app/app.go`）
1. `executionDenied(err)`：`err == nil` 显式返回 `false`，只认 7 个策略哨兵（含 wrap）。
2. 三个调用点全部改为**先显式判断成功路径**，即使将来分类器再被改坏，成功路径也不受影响：
   - `schedulableChecker.SchedulableApplication`：`err == nil → return nil` → `executionDenied → ErrApplicationNotSchedulable` → 其余上抛
   - `bindingResolver.EnabledBinding` / `EnabledBindingFor`：`err == nil → bindingViewOf(exe)` → `executionDenied → (nil, nil)` → 其余上抛
3. 新增导出构造器 `NewBindingResolver` / `NewSchedulableChecker`，供外部接线与测试使用真实链路。

### 测试修复
- `internal/app/authz_test.go`：policy 断言列表**删除 `nil`**；新增 `TestExecutionDeniedNilMeansSuccess`。
- 新增真实链路正向集成测试（`tests/integration/review3_fixes_test.go`）：
  - `TestEnabledBindingForValidOwnerReturnsBinding`：真实 user + public enabled app + enabled binding + active provider → `(binding != nil, err == nil)`
  - `TestSchedulableCheckerAllowsValidApplication`：合法应用 → nil；disabled 应用（对照组）→ `ErrApplicationNotSchedulable`
  - `TestRealResolverAllowsScheduledFire`：真实 resolver 链 + 真实 due schedule → ProcessDue 后 **1 queued occurrence / 1 queued run / 0 failed**
  - `TestRealResolverAllowsTriggerNow`：真实链路 run-now → 1 queued occurrence 带 run

---

## 三、P1-B：Pre-Submit Gate（第二道门）

### 问题
Worker Gate 位于 Claim 之后、ProviderSlot 之前，但 Aily Handler 内部在真正 Submit 前还有 `Auth.Build → ChatsL.Acquire`（limiter 可能等待）。管理员在等待窗口内停用应用/Provider 时，Run 仍会被提交——此时 Provider 尚未收到请求，按产品规则应被 Kill/Pause。

### 修改
1. **`internal/execution/gate.go`（新增）**：`PreSubmitGate(ctx, owned, claimed, gate, log) bool` —— Gate 2 的共享判定：
   - kill → `Finalize(StatusCancelled, "execution_disabled")`（不耗 attempt）
   - pause → `DeferAfter("provider_disabled", 30s)`（保持原优先级）
   - gate 查询失败 → `DeferAfter("run_gate_unavailable", 1s)`（fail safe）
   - **未知 action → `DeferAfter("run_gate_unknown", 1s)`，fail closed（P2-E 同源）**
   - 返回 true = 已拦截，调用方禁止 Submit
2. **`internal/integrations/aily/executor.go`**：`Executor` 增加 `Gate execution.RunGate` 字段；在 `ChatsL.Acquire` **之后**、`BeginProviderAttempt` **之前**调用 `PreSubmitGate`——距离 Provider Submit 最近的检查点。
3. **`internal/app/app.go`**：Build 中给 `ailyExecutor` 注入 `Gate: NewExecutionGate(catalogSvc)`（与 Worker Gate 1 共用同一判定实现）。

双门时序：

```
Claim → Gate 1 → ProviderSlot → Handler → Auth → ChatsL.Acquire
      → ★ Gate 2 → BeginProviderAttempt → Provider Submit
```

### 测试（Barrier 竞态）
- `TestApplicationDisabledBetweenWorkerGateAndSubmitIsKilled`：Gate 1 allow → handler 阻塞在 barrier → 停用应用 → 放行 → Gate 2 kill。断言：cancelled / error_code=execution_disabled / **submit=0 / attempt=0**
- `TestProviderDisabledBetweenWorkerGateAndSubmitIsDeferred`：同结构，停用 Provider → Gate 2 defer。断言：queued / available_at≈30s 后 / **submit=0 / attempt=0 / priority 不变**
- 两个测试都经过**真实的 `execution.PreSubmitGate`** 代码路径（非 mock 逻辑）。

---

## 四、P1-C：Provider Pause 改用 Defer（保持原优先级）

### 问题
GatePause 复用 `RetryOwnedRunAfter`，其 SQL 强制 `priority = 'retry'`，outbox 也固定发 `PriorityClassRetry`——`interactive_user` / `scheduled_high` 碰到一次 Provider 停用就全部降级成 retry，恢复后公平调度语义丢失。且 `RetryOwnedRunAfter` 不同步 occurrence（runs=queued 而 occurrences=running 的不一致状态）。

### 修改
1. **`db/queries/execution.sql`** 新增两条：
   - `DeferRunFenced`：`running → queued`，`available_at = DB时钟 + delay`，**不改 priority**，fenced by lease_epoch
   - `DeferScheduledOccurrence`：`schedule_occurrences running → queued`（by run_id，同事务）
2. **`internal/execution/defer.go`（新增）**：`DeferOwnedRunAfter`，与 retry.go 同构的单事务：
   verify ownership → DeferRunFenced → Occurrence 同步 → `run.deferred` 事件（非终态，SSE 不断流）→ outbox `run.dispatch`（**priority_class = 原优先级** `PriorityClassOf(run.Priority)`，delay 与 available_at 同源 DB 时钟）→ 清 provider slots → 清 lease → COMMIT → publishLive
   另有 `WorkerOwnedService.DeferAfter` fence 包装。
3. **`internal/execution/worker.go`**：Gate 1 的 pause 分支与 gate-unavailable 分支改用 `deferAfter`（原优先级 + 不耗 attempt）；**provider 容量类 requeue（inflight limit 等）保持原有 retry 语义不变**（最小行为变更）。
4. 新事件 `EventRunDeferred = "run.deferred"`：前端 TERMINAL 集合不含它 → 流保持存活，与 `run.retrying` 同等安全性；语义上区分"延迟等待"与"失败重试"。

### 测试
- `TestProviderPausePreservesRunPriority`：claim 后 defer → status=queued、**priority 原样**、available_at 未来、lease=0、slots=0、outbox payload `priority_class=interactive`（非 retry）、恰 1 条 `run.deferred`
- `TestProviderPauseRequeuesOccurrence`：scheduled run claim（occurrence→running）后 defer → **occurrence queued=1 / running=0**、run queued、priority 不变

---

## 五、P2-D：`run.started` 移到 Gate allow 之后

### 修改（`internal/execution/worker.go`）
Gate 检查从 `execute()` 移到 `claimAndExecute` 中 **`run.started` 追加之前**（`gateBlocked` 方法）：
- Kill：queued → cancelled，**无假 started**
- Pause：queued → deferred → queued，**不再每 30s 产生 started/retrying 事件对**
- 正常路径：Gate allow → run.started → ProviderSlot → Handler（顺序不变）

附带收益：被 gate 拦截的 Run 不再注册 inflight/heartbeat。

### 测试
- `TestGateKillDoesNotEmitRunStarted`：kill 后 `run.started`=0、`run.cancelled`=1、handler=0
- `TestGatePauseDoesNotEmitRepeatedRunStarted`：pause 后 `run.started`=0、`run.retrying`=0、**`run.deferred`=1**（30s 窗口内无热循环）、handler=0

---

## 六、P2-E：未知 GateAction fail-closed

### 修改
- Worker Gate 1（`gateBlocked`）与 Gate 2（`PreSubmitGate`）的 switch 均增加 `default` 分支：未知 action → **defer + log + 指标**，绝不继续 Handler。
- 新指标：`telemetry.AdmissionGateUnknown = "gate_unknown"`（ProviderAdmission 计数器新增标签值；持续非零即 gate 实现缺陷信号）。

### 测试
- `TestUnknownGateActionFailsClosed`：stub gate 返回 `"bogus"` → handler=0、`run.started`=0、run 持续 fail-closed defer（attempt=0）

---

## 七、验证结果

| 步骤 | 命令/方式 | 结果 |
|---|---|---|
| sqlc generate | `~/go/bin/sqlc generate`（新增 DeferRunFenced / DeferScheduledOccurrence） | ✅ |
| 编译 | `go build ./...` | ✅ |
| vet | `go vet`（execution / app / aily / tests） | ✅ |
| gofmt | 变更文件全部干净（recovery/retry/slots/transactions 为已知本地 go 版本历史差异，未触碰） | ✅ |
| 全量单测 | `go test ./...` | ✅ 全绿 |
| 第三轮新测试 | 11/11 通过（6 DB + 5 worker） | ✅ |
| 全量集成套件 | `STUDIO_TEST_DB=1 STUDIO_TEST_REDIS=1 go test ./tests/integration/ -count=1` | ✅ 全绿（含二轮 5 项回归） |

**遗留说明**
- Execution Generation（严格 edge-triggered Kill）按报告建议**本轮不引入**，双检查点 level-triggered 已覆盖上线要求。
- 报告 §11 提到的"provider capacity 也可区分 defer/admission-retry"为未来项：本轮容量类 requeue 保持 retry 语义。
- `run.deferred` 为新事件类型：前端未知事件按忽略处理（非终态不断流），如需展示"延迟执行"提示可后续在前端补充。

---

## 八、涉及文件

| 文件 | 变更 |
|---|---|
| `internal/app/app.go` | P0 修复（executionDenied + 显式成功路径 + 2 个导出构造器）、ailyExecutor 注入 Gate |
| `internal/app/authz_test.go` | policy 列表删 nil、新增 NilMeansSuccess |
| `internal/execution/defer.go` | **新增**：DeferOwnedRunAfter / DeferAfter / EventRunDeferred |
| `internal/execution/gate.go` | **新增**：PreSubmitGate（Gate 2 共享判定 + fail closed） |
| `internal/execution/worker.go` | gateBlocked（移门前置 + fail closed + defer 化）、run.started 后移 |
| `internal/integrations/aily/executor.go` | Gate 字段 + PreSubmitGate 调用点 |
| `internal/platform/telemetry/telemetry.go` | AdmissionGateUnknown 指标 |
| `db/queries/execution.sql` | DeferRunFenced / DeferScheduledOccurrence |
| `internal/gen/db/*` | sqlc 重新生成 |
| `tests/integration/review3_fixes_test.go` | **新增**：11 个回归测试 |
