# Creation Agent Studio — Admission Fairness & Distributed Lease Hardening 修复变更报告

- 修复依据：
  - `docs/Creation Agent Studio 未来演进完整架构文档.md`
  - `docs/potal 最新代码复审报告暨剩余问题开发执行计划.md`
- 范围：**Phase 1 加权调度空 class 饥饿 / Phase 2 Provider Inflight 持久化真值 / Phase 3 Clock Authority / Phase 4 CI 覆盖闭合 / Phase 5 故障与并发测试 / P2 工程项**
- 明确排除（本轮不处理，按要求）：Git 历史、仓库公开状态、历史敏感信息、密钥轮换
- 结论：**报告 Phase 1–5 全部关闭；P2 中 §32（CI readiness annotation）、§34（权重校验）、§35（术语）关闭，§33（Invariant L 快照化）按要求保持 P2 未做；后端 gofmt / build / vet / unit / integration（真实 TiDB 8.0.11 + Redis）全绿。**

---

## 1. 总体结论

执行内核（Ownership / Claim / Lease / Reaper / Finalize / Artifact / Session fence）保持不动，本轮把**准入与时间**两条地基换成"可证明"的形态：

```text
Run（attempt = provider execution count）
  ↓
Claim（CAS + lease_epoch + token，不消耗 attempt）
  ↓
classScheduler（interactive 7 : retry 1 : scheduled 2，空 class 立即放弃本轮额度）
  ↓
ProviderSlots（TiDB 事务，ownership-scoped，DB 时钟租约）← Redis 不再持有容量真值
  ↓
Rate Limit（Redis GCRA，内部 TIME 为唯一时钟）
  ↓
BeginProviderAttemptOwned（此刻才 attempt++）
  ↓
Provider Submit / Stream
  ↓
Heartbeat = run_leases + provider_execution_slots 同一个 TiDB 事务
  ↓
Finalize / Retry / Reaper：同一个事务里同时清 lease + provider slot
```

本轮建立/强化的不变量：

1. **active provider execution ⇔ active `provider_execution_slots` row**，且活跃 slot 数 **严格**为 `max_inflight` 上界 —— Redis 重启 / FLUSH / 故障切换 / worker 重启都不能放大真实并发。
2. **Run Ownership alive ⇔ Provider Slot alive** —— 二者在同一 TiDB 事务内续期，不再各自漂移。
3. **所有会导致"等待/过期"的时间只有一个权威**：Run Lease、Provider Slot、Retry/Admission 可用时刻由 **DB 时钟**决定；共享 GCRA 窗口由 **Redis TIME** 决定；应用时钟可任意偏移。
4. **空闲优先级 class 不得钉死本轮** —— 空的 credited class 立即放弃剩余额度，scheduled/retry 不会因为另一个 class 队列为空而被饿死。

---

## 2. 问题修复对照表

### 2.1 Phase 1（P0）：加权调度空 class 饥饿

| 问题 | 修复结果 | 关键落点 |
|---|---|---|
| **空 credited class 永久钉住本轮信用** —— `credits` 只在"全部为 0"时刷新；retry 队列为空时永远不消费自己的 1 份额度，`credits` 钉死在 `[0,1,0]`，`allExhausted()` 永假 → scheduled 在连续 interactive 流量下**永久饥饿**（`priority_test.go` 的 7:1:2 用例因三队列均饱和而无法暴露） | ✅ 关闭。`classScheduler` 改为"只有 credit > 0 的 class 参与本轮"，并新增 `markEmpty(idx)`：**探到空队列的 credited class 立即把本轮剩余 credit 清零**；`order()` 仅在全部 credit 归零时 refill。轮次自然收敛：三类都繁忙时严格 7:1:2；只有一类繁忙时该类独占容量；空闲 class 不消耗、不阻塞 | `internal/execution/priority.go`、`internal/execution/worker.go`（`readWeighted` 空探测即 `markEmpty`） |
| 原"借用（borrowing）"设计对空 class 无界放行且掩盖了饥饿 | ✅ 移除借用语义：额度要么被消耗、要么被放弃，规则从 5 条收敛为 4 条（见 §3.1） | `internal/execution/priority.go` |
| 测试未覆盖"某 class 为空"的调度分支 | ✅ 补齐 15 个用例（含 4 个直接回归本轮缺陷），见 §5.1 | `internal/execution/priority_test.go` |

### 2.2 Phase 2（P0）：Provider Inflight 持久化真值

| 问题 | 修复结果 | 关键落点 |
|---|---|---|
| **容量真值在 Redis，Redis 丢失即 `max_inflight` 失效** —— ZSET 被 flush / 重启 / 故障切换后，正在执行的 provider 调用仍在跑，新 worker 会再放一整批，真实并发可达 2×limit | ✅ 关闭。容量状态迁入 TiDB（校正平面）：新增 `provider_execution_slots`（ownership 唯一键 `provider + run_id + lease_epoch`，`(provider, expires_at)` 索引）+ `provider_admission_locks`（每 provider 串行化行）；Acquire 在单事务内 **锁串行化行 → ownership 栅栏 → 清过期 → 幂等复用 → 计数 ≤ max → 插入**；`Renew` XX-only 绝不复活失效 slot；`Release` 精确匹配 ownership | `db/migrations/0011_provider_execution_slots.up.sql`、`db/queries/execution.sql`、`internal/execution/slots.go` |
| stale worker 可能把 slot 写进容量账本（唤醒后 run 已被 reaper 重排） | ✅ `Acquire` 增加数据库侧栅栏 `CountRunningRunAtEpoch`：run 必须仍处于 `running` 且 epoch 匹配，否则返回 `ErrLostOwnership` 且**不占容量**；worker 对该错误只停止、不 requeue（恢复权归 reaper/新 owner） | `internal/execution/slots.go`、`internal/execution/worker.go` |
| Run Lease 与 Provider Slot 是两次独立续期（"Run Ownership alive ⇔ Provider Slot alive"只能靠时序约定） | ✅ 合并心跳：`Service.HeartbeatOwnedWithSlot` 在**同一 TiDB 事务**内 `HeartbeatLeaseFenced` + `TouchProviderSlot`；lease 丢失则不触碰 slot；slot 丢失只上报 `provider_slot_lost`，绝不影响 lease（避免"slot 抖动把健康 run 判死"） | `internal/execution/service.go`、`internal/execution/worker.go` |
| run 离开 running 后仍持有 slot（依赖 worker 的 defer release，崩溃/被栅栏时不可靠） | ✅ Finalize / Retry / Reaper 三个所有权转移事务**各自**删除该 run 的 slot（`DeleteProviderSlotsUpToEpoch`，同事务提交）；worker scanLoop 定期清扫过期 slot；defer release 保留为幂等兜底 | `internal/execution/finalize.go`、`internal/execution/retry.go`、`internal/execution/recovery.go`、`internal/execution/worker.go` |
| 无孤儿 slot 检测 | ✅ invariant checker 新增 **Invariant M（orphan_provider_slot）**：活跃 slot 必须属于 `running` 且 epoch 匹配的 run | `internal/execution/invariant/checker.go` |
| Redis slot 实现（ZSET + 2 段 Lua）成为死代码与第二套账本 | ✅ 删除 `inflight.go` / `inflight_test.go` 与两段 Lua；`app.go`、`cmd/worker` 改注入 `ProviderSlots`（`studio_provider_inflight` 指标改由 TiDB depth 驱动），容量路径对 Redis 零依赖 | `internal/execution/inflight.go`（删除）、`internal/app/app.go`、`cmd/worker/main.go` |
| heartbeat loop 读 `ctl.slot` 未持锁（与 `attachProviderSlot` 写并发） | ✅ 快照结构 `inflightSnapshot{ctl, slot}`：**slot 指针在有锁快照时读取**，消除数据竞争（为 CI 的 `-race` 门禁铺路） | `internal/execution/worker.go` |

### 2.3 Phase 3（P1）：Clock Authority Hardening

| 问题 | 修复结果 | 关键落点 |
|---|---|---|
| **Run Lease 到期用应用时钟** —— worker 时钟快则租约被"提前续长/判活"，慢则被 reaper 误判过期 | ✅ `CreateRunLease` / `HeartbeatLease` / `HeartbeatLeaseFenced` 改为 `expires_at = DATE_ADD(CURRENT_TIMESTAMP(3), INTERVAL ? MICROSECOND)`：租约的获取、续期、过期判定全部由 **DB 时钟**决定；负延迟用于测试强制过期 | `db/queries/execution.sql`、`internal/execution/service.go` |
| RetryAt 由应用时钟计算（app/DB 偏差直接造成"唤醒早于可 claim"或延迟） | ✅ `RetryOwnedRunAfter(delay)`：run.available_at 与 outbox.available_at 均取 `DATE_ADD(CURRENT_TIMESTAMP(3), INTERVAL retry_delay_micros MICROSECOND)`（同事务、同延迟）；事件 payload 的 `retry_at` 取自同事务的 `CurrentDBTime`；`RetryOwnedRunAt(绝对时刻)` 从 API 中移除 | `db/queries/execution.sql`、`internal/execution/retry.go`、`internal/execution/worker.go` |
| GCRA 以调用方时钟推进共享窗口（快时钟消耗他人令牌，慢时钟放行突发） | ✅ Lua 内改用 `redis.call('TIME')`，应用只传策略参数（emission/burst/ttl）；仅 Redis 不可用时的进程内兜底使用本地时钟，并通过 `Degraded()` 上报 | `internal/execution/ratelimit.go` |
| Delivery 重试/回收时间全部来自应用时钟 | ✅ `RequeueDelivery`（DB now + backoff micros）、`ListDueDeliveries`（DB now 判 due）、`ReclaimStuckDeliveries` / `FailStuckDeliveries`（DB now − lease micros）；删除 `delivery.Worker.nowFunc` 与 `sqlNullTime` | `db/queries/automation.sql`、`internal/delivery/worker.go` |
| Provider slot 过期（Redis ZSET score）由应用时钟决定 | ✅ 随 Phase 2 一并解决：slot 的 acquire/renew/expire 全部 SQL 内 `CURRENT_TIMESTAMP(3)` | `db/queries/execution.sql`、`internal/execution/slots.go` |

### 2.4 Phase 4：CI 覆盖闭合

| 问题 | 修复结果 | 关键落点 |
|---|---|---|
| Redis 相关测试永远不在 CI 跑（`STUDIO_TEST_REDIS` 只在本地设置），Redis 路径长期无回归保护 | ✅ `integration` job 新增步骤：`go test ./internal/execution/... ./internal/delivery/... -count=1 -v`（job 级 `STUDIO_TEST_TIDB=1` + `STUDIO_TEST_REDIS=1`）→ GCRA/Redis 断链降级、权重调度、slot 存储等真正进入 CI；失败同样输出 `::error` 摘要 | `.github/workflows/backend.yml` |
| 无竞态门禁（worker / heartbeat / executionControl / scheduler / delivery worker 都是并发代码） | ✅ `check` job 新增 `go test -race ./internal/execution/... ./internal/delivery/... ./internal/automation/... -count=1` | `.github/workflows/backend.yml` |
| TiDB readiness 超时仍打印 `::error` 后继续（annotation 误导，真正失败原因被掩盖） | ✅ 两个 job 的 readiness 改为 `ready` 标志 + 超时后 `exit 1`；MySQL 5.7 job 去掉"超时后继续跑 `SELECT 1` 靠非零退出码兜底"的写法 | `.github/workflows/backend.yml` |
| 既有 reaper 用例在共享 dev 库上不确定（`RecoverExpiredLeases(limit=10)` 可能够不到本用例的 run） | ✅ 新增 `recoverRun(t, svc, runID)`：按"目标 run 不再是 running"为收敛条件循环回收（上限 20 轮）；`correctness_closure` / `tidb_cas` / `provider_admission` / `production_hardening` / `lease_fencing` 的相关断言改用该 helper | `tests/integration/main_test.go` 等 6 个测试文件 |

### 2.5 Phase 5：故障与并发测试

| 覆盖目标 | 新增测试 | 关键落点 |
|---|---|---|
| 加权调度公平性 | 15 个单元用例（7:1:2、retry 空、scheduled 空、空 class 不钉轮、7:2 相对份额、class 复活、零权重、全零、未配置） | `internal/execution/priority_test.go` |
| Provider 容量真值 | Redis 状态丢失/worker 重启不可放大并发、全局上限、reaper 清理崩溃尝试 slot、ownership 隔离 release/renew、幂等 acquire、崩溃过期回收、stale ownership 拒绝、合并心跳、finalize 清理、`orphan_provider_slot` 检查 | `tests/integration/provider_slots_test.go` |
| **真实 Worker 准入背压** | `max_inflight=1`、两个排队 run：一个进入 handler 并阻塞（占住唯一 slot），另一个被判定拒绝并 requeue（`attempt` 保持 0、未进入 handler），释放后第二个才执行且**每个 run 恰好执行一次** | `tests/integration/provider_slots_test.go` |
| 时钟权威 | 租约到期 = DB now + lease（±容差）、负债心跳立即过期、retry/admission 可用时刻 = DB now + delay、delivery 重试与 stuck 回收的 DB 时钟语义 | `tests/integration/clock_authority_test.go` |
| 配置校验 | `parseWeights` 表驱动：`0` / 负数 / 非数字 / 元数不符 / 空串全部回退 7/1/2 | `internal/platform/config/config_test.go` |

### 2.6 P2

| 问题 | 处理 | 说明 |
|---|---|---|
| **§32 TiDB readiness annotation 误报** | ✅ 关闭 | 见 §2.4 |
| **§34 权重 0 可禁用 class** | ✅ 关闭。`parseWeights` 要求三类权重**严格 > 0**，0 视为配置事故回退 7/1/2；调度器自身对零权重 class 是"永不探测、永不服务"的安全行为（有单测钉死） | `internal/platform/config/config.go`、`internal/execution/priority.go` |
| **§35 术语精确化（attempt-scoped → ownership-scoped）** | ✅ 关闭。slot 身份/注释/测试统一为 ownership-scoped，`Member = {run_id}:{lease_epoch}:{lease_token}` | 全量 execution 变更 |
| **§33 Invariant L 快照化** | ⏸ 保持 P2 未做。报告自身指出"继续加时间条件会引入误报"，正解是 occurrence 投递快照；当前 `created_at <= COALESCE(finished_at, updated_at)` 近似保留 | `db/queries/execution.sql`（未改） |

---

## 3. 关键设计说明

### 3.1 classScheduler：额度要么被消耗，要么被放弃

```text
order():
  若全部 credit == 0 → refill(weights)
  返回 credit > 0 的 class（按权重顺序）

consume(idx):   credit[idx] > 0 ? credit[idx]-- : no-op
markEmpty(idx): credit[idx] = 0        ← 探到空队列立即放弃本轮额度
```

- 三类都繁忙：一轮严格服务 7 : 1 : 2（回归用例逐条核对份额与轮首必须是 interactive）。
- interactive 饱和 + retry 空 + scheduled 饱和：`[0,1,2]` → retry 探测为空 → `[0,0,2]` → scheduled 连续取用 → 归零 refill，scheduled 稳定拿到 2/9 ≈ 22% 容量（旧实现为 0）。
- 只有 scheduled 有流量：其余两类被 `markEmpty` 后 scheduled 独占 100% 容量。
- 零权重 class 永不参与（生产配置层也已拒绝 0）。

### 3.2 ProviderSlots：TiDB 事务 + 串行化行才是"严格上界"

```text
BEGIN
  INSERT ... ON DUPLICATE KEY UPDATE provider=provider   -- 物化串行化行
  SELECT provider FROM provider_admission_locks FOR UPDATE -- 每 provider 串行
  SELECT COUNT(*) FROM runs WHERE id=? AND status='running' AND lease_epoch=?  -- ownership 栅栏
  DELETE FROM provider_execution_slots WHERE provider=? AND expires_at<=CURRENT_TIMESTAMP(3)
  -- 同一 ownership 已有 slot → TouchProviderSlot（幂等复用）
  SELECT COUNT(*) FROM provider_execution_slots WHERE provider=? AND expires_at>CURRENT_TIMESTAMP(3)
  COUNT >= max → 拒绝
  INSERT provider_execution_slots(... expires_at = DATE_ADD(CURRENT_TIMESTAMP(3), INTERVAL ? MICROSECOND))
COMMIT
```

- `Renew`：`UPDATE ... WHERE provider/run/epoch/token` 命中 0 行 → `ErrProviderSlotLost`，**不重建**。
- `Release`：`DELETE ... WHERE provider/run/epoch/token`，stale worker 的释放天然只能删自己的行；重复释放幂等。
- 与架构文档的一致性：**TiDB 是 Source of Truth**，容量是安全状态 → TiDB；**Redis 只负责限流** → GCRA 保留在 Redis（Redis TIME 为时钟）。

### 3.3 合并心跳：一个事务，两条租约

```text
BEGIN
  UPDATE run_leases  SET expires_at = DB now + lease   WHERE run_id=? AND lease_token=?
    ├─ 0 行 → 返回 (leaseOK=false)：不触碰 slot，停止本地执行
    └─ 1 行 → UPDATE provider_execution_slots SET expires_at = DB now + slotLease
                WHERE provider/run/epoch/token（0 行 → slotOK=false，lease 依然有效）
COMMIT
```

`err != nil` 只有一种含义：**没有提交任何东西**（调用方按心跳失败处理）。这避免了"slot 抖动把健康 run 判死"和"slot 先续期让被栅栏的 attempt 继续影响容量"两类反模式。

### 3.4 Clock Authority 清单

| 时间语义 | 权威时钟 | 实现 |
|---|---|---|
| Run Lease 获取/续期/过期 | DB | `DATE_ADD(CURRENT_TIMESTAMP(3), INTERVAL ? MICROSECOND)` |
| Provider Slot acquire/renew/expire | DB | 同上 |
| Retry / Admission 可用时刻（Run + Outbox 同刻） | DB | `RequeueRunFenced` + `CreateOutboxEventAt` |
| 事件 `retry_at` 展示值 | DB | 同事务 `CurrentDBTime` |
| Delivery 重试时间 / due 判定 / stuck 回收 | DB | automation.sql |
| 共享 GCRA 窗口 | Redis | Lua 内 `TIME` |
| 进程内 GCRA 兜底（Redis 不可用） | 本地 | `Degraded()=true` 上报，绝不 fail-open |

### 3.5 CI 门禁

```text
check:        actionlint → gofmt → vet → build → test → test -race（execution/delivery/automation）
integration:  TiDB 8.0.0 + Redis 7 → migrate → internal/execution+delivery（真实 TiDB/Redis）→ tests/integration
mysql57:      MySQL 5.7 → migrate → tests/integration（兼容下限）
```

---

## 4. 测试与验证证据

### 4.1 本地实跑（真实 TiDB 8.0.11 + Redis，`STUDIO_TEST_TIDB=1` `STUDIO_TEST_REDIS=1`）

```text
go run ./cmd/migrate                     → migrations applied（0011 已落库）
gofmt -l .                               → 空
go vet ./...                             → 通过
go build ./...                           → 通过
go test ./... -count=1                   → 全绿
  internal/execution                      ok
  internal/delivery                       ok
  internal/platform/config                ok（新增 parseWeights 用例）
  tests/integration                       ok（约 106s，含全部新增用例）
```

专项复核：

```text
新加权调度回归（retry 空 / scheduled 空 / 空 class 不钉轮）        PASS
provider slot 11 项（含 Redis 无关性、reaper 清理、stale ownership）PASS
真实 Worker 准入背压（-count=6 连续）                              PASS
时钟权威 3 项（lease / retry+admission / delivery）                PASS
```

### 4.2 未在本机验证（依赖 CI 环境）

1. `go test -race`：本机无 gcc（`CGO_ENABLED=0`），由 CI `check` job（ubuntu-latest）实跑；为此已显式修掉 heartbeat loop 的 slot 读取竞争。
2. MySQL 5.7 兼容性：本轮新增 SQL 仅使用 `DATE_ADD/DATE_SUB(CURRENT_TIMESTAMP(3), INTERVAL ? MICROSECOND)`、`TIMESTAMPDIFF`、`INSERT ... ON DUPLICATE KEY UPDATE`、`SELECT ... FOR UPDATE`、`LAST_INSERT_ID` 等 5.7 子集能力，由 `mysql57` job 实跑确认。
3. TiDB 8.0.0 容器 + 全新库的迁移与集成套件：本机 dev 库为 TiDB 8.0.11，CI 为 8.0.0 容器。

---

## 5. 变更文件清单

**生产代码**

```text
backend-go/db/migrations/0011_provider_execution_slots.{up,down}.sql   新增
backend-go/db/queries/execution.sql                                    修改（slot 查询 / DB 时钟 / slot 清理）
backend-go/db/queries/automation.sql                                   修改（delivery DB 时钟）
backend-go/internal/execution/slots.go                                 新增（ProviderSlots）
backend-go/internal/execution/inflight.go                              删除（Redis ZSET 实现）
backend-go/internal/execution/priority.go                              修改（markEmpty，去借用）
backend-go/internal/execution/worker.go                                修改（markEmpty / slot / 合并心跳 / 竞态修复）
backend-go/internal/execution/service.go                               修改（DB 时钟租约 / HeartbeatOwnedWithSlot）
backend-go/internal/execution/retry.go                                 修改（RetryOwnedRunAfter / DB 时钟 / slot 清理）
backend-go/internal/execution/finalize.go                              修改（终态清 slot）
backend-go/internal/execution/recovery.go                              修改（reaper 清 slot）
backend-go/internal/execution/ratelimit.go                             修改（Redis TIME）
backend-go/internal/execution/invariant/checker.go                     修改（Invariant M）
backend-go/internal/delivery/worker.go                                 修改（DB 时钟；删除 nowFunc）
backend-go/internal/platform/config/config.go                          修改（权重严格 > 0）
backend-go/internal/app/app.go, backend-go/cmd/worker/main.go           修改（注入 ProviderSlots）
backend-go/internal/gen/db/*                                            sqlc 重新生成
```

**测试**

```text
backend-go/internal/execution/priority_test.go                 重写补强（15 用例）
backend-go/internal/execution/slots_test.go                    新增（ownership 身份 / 关闭态透明）
backend-go/internal/execution/inflight_test.go                 删除（Redis slot 用例）
backend-go/internal/platform/config/config_test.go             新增
backend-go/tests/integration/provider_slots_test.go            新增（11 + 1 用例）
backend-go/tests/integration/clock_authority_test.go           新增（3 用例）
backend-go/tests/integration/main_test.go                      新增 recoverRun helper
backend-go/tests/integration/{correctness_closure,tidb_cas,provider_admission,
                              production_hardening,lease_fencing}_test.go   改用 recoverRun
```

**CI / 文档**

```text
.github/workflows/backend.yml                                  修改（race 门禁 / Redis 测试入 CI / readiness 修复）
docs/potal Admission Fairness & Distributed Lease Hardening 修复变更报告.md   新增（本报告）
```
