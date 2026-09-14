# Creation Agent Studio — Provider Admission & Release Gate Closure 修复变更报告

- 修复依据：
  - `docs/Creation Agent Studio 未来演进完整架构文档.md`
  - `docs/potal 最新架构终审暨 Production Hardening 开发执行报告.md`
- 范围：**Provider Inflight Fencing / Attempt 语义 / Retry 时序 / 加权优先级 Admission / Delivery 幂等语义 / CI Release Gate / P2 工程项**
- 明确排除（本轮不处理，按要求）：Git 历史、仓库公开状态、历史敏感信息、密钥轮换
- 结论：**报告中 P0-1 / P0-2 / P0-3（本地可验证部分）/ P1-1 / P1-2 / P1-3 / P2-1~P2-4 全部关闭；后端 gofmt / build / vet / unit / integration（真实 TiDB + Redis）全绿。**

---

## 1. 总体结论

执行内核本身（Ownership / Claim / Reaper / Finalize / Artifact / Session fence）保持不动，本轮把**准入（Admission）层**从"能跑"提升到"可证明"：

```text
Run（attempt = provider execution count）
  ↓
Claim（不消耗 attempt）
  ↓
ProviderSlot = {run}:{epoch}:{token}   ← attempt-scoped，可被栅栏
  ↓
Rate Limit → BeginProviderAttemptOwned（此刻才 attempt++）
  ↓
Provider Submit / Stream
  ↓
Finalize（同事务）+ Release 自己的 Slot
  ↓
Outbox(run.dispatch, priority_class) → queue:<provider>:<class>
  ↓
Worker Weighted Fair Scheduling 7 : 1 : 2
```

三条本轮建立的不变量：

1. **provider slot 属于 attempt，而不属于 run** —— stale worker 永远无法 release/renew 新 owner 的 slot。
2. **attempt = provider execution count** —— claim 与 admission requeue 都不消耗重试预算。
3. **Run.available_at ≡ Outbox.available_at = retryAt** —— 唤醒时刻与可 claim 时刻完全一致。

---

## 2. 问题修复对照表

### 2.1 P0

| 问题 | 修复结果 | 关键落点 |
|---|---|---|
| **P0-1 Provider Inflight Semaphore 缺少 Ownership Fencing**（ZSET member = runID；stale worker 的 Release/Renew 能删/续新 owner 的 slot；Renew 还能凭空创建 slot；heartbeat 先续 slot 再判断 run ownership） | ✅ 关闭。`ProviderSlot{RunID, LeaseEpoch, LeaseToken, Member}`，`Member = {run_id}:{lease_epoch}:{lease_token}`；`Acquire(ctx, ExecutionOwnership)` 单 Lua 原子（清过期 → 同 member 幂等刷新 → ZCARD≥max 拒绝 → ZADD）；`Renew` 走 `ZSCORE` + `ZADD` 的 **XX-only** 脚本，member 不存在返回 `ErrProviderSlotLost` 且**绝不重建**；`Release` 只 ZREM 精确 member；heartbeat 顺序改为 **先 Run Lease，lost 则 cancel + 释放自己的 slot + stop，未 lost 才 Renew slot** | `internal/execution/inflight.go`、`internal/execution/worker.go` |
| **P0-2 Provider Admission 错误消耗 Attempt**（CASClaimRun 自带 `attempt = attempt + 1`；Aily 用 attempt 判断是否继续重试 → 光是被 inflight 拒绝 3 次就把 Run 判死） | ✅ 关闭。`CASClaimRun` 删除 `attempt = attempt + 1`；新增 `BeginProviderAttemptFenced` + `Service.BeginProviderAttemptOwned`（事务内 `verifyActiveOwnershipTx` → `attempt >= max_attempts` 返回 `ErrProviderAttemptsExhausted` → `attempt++`）；Aily 执行路径改为 **Auth → Rate Limit → BeginProviderAttempt → Stream/Submit**；admission requeue（`provider_inflight_limit` / `provider_inflight_unavailable`）不消耗 attempt | `db/queries/execution.sql`、`internal/execution/service.go`、`internal/execution/ownership.go`、`internal/integrations/aily/executor.go` |
| **P0-3 GitHub Backend CI 无法实例化 Jobs**（`integration` / `mysql57` 两个 job 同级重复 `env:`；TiDB health-cmd 在容器内跑 curl/mysqladmin；无 workflow lint 门禁） | ✅ 修复。合并为单一 `env:` 块；TiDB service 去掉 health-cmd，改为 **host-side readiness 循环**（`mysql -h127.0.0.1 -P4000 SELECT 1`，90 次 × 2s，随后建库）；MySQL 5.7 同样以 host-side wait 为准（容器内 `mysqladmin ping` 保留，官方镜像自带）；`check` job 增加 **actionlint** 门禁 | `.github/workflows/backend.yml` |

> P0-3 说明：本地无法执行 GitHub Runner，因此结论是"**workflow 结构合法且不再有重复 key / 不可用 health-cmd**"，是否真正 GREEN 必须在 CI 上实跑确认（见 §7 未验证项）。

### 2.2 P1

| 问题 | 修复结果 | 关键落点 |
|---|---|---|
| **P1-1 Priority 只影响 DB fallback，正常 Redis 主路径仍是 FIFO** | ✅ 关闭。Outbox payload 增加 `priority_class`（interactive / retry / scheduled）；Relay 按 `provider + priority_class` 路由到 `queue:<provider>:<class>` 三条 Stream（无 class 的旧行与 delivery 事件仍走原单流）；Worker 改为 **Weighted Fair Scheduling**：每轮 7:1:2 信用额度、按信用顺序消费、信用耗尽即刷新、**全部有信用的队列皆空时可借用**（空队列额度自动让给其他队列）；权重可配置（`RUN_PRIORITY_WEIGHTS`，默认 `7,1,2`） | `internal/execution/service.go`、`internal/execution/retry.go`、`internal/execution/recovery.go`、`internal/execution/outbox.go`、`internal/execution/worker.go`、`internal/execution/priority.go` |
| **P1-2 Retry available_at 与 Outbox 发布时间不同步**（Run 写入 `+1 SECOND` 硬编码，Outbox 立即发布 → worker 拿到消息时 CAS 必然失配 → 只能等 20s 兜底扫描） | ✅ 关闭。`RetryOwnedRunAt(..., retryAt)`；单事务内 `Run.available_at = retryAt` **且** `Outbox.available_at = retryAt`（新增 `CreateOutboxEventAt`）；SQL 中 `INTERVAL 1 SECOND` 硬编码删除；退避使用 `RUN_REQUEUE_DELAY`（`Service.RequeueDelay`，默认 1s、配置默认 5s）；Reaper 恢复路径同样使用共享 retryAt（并移除 `SetRunImmediatelyAvailable` 这一"立刻置为可用"的反向补偿） | `db/queries/execution.sql`、`internal/execution/retry.go`、`internal/execution/recovery.go`、`internal/platform/config/config.go`、`internal/app/app.go` |
| **P1-3 Feishu Delivery 是 At-Least-Once 外部副作用，但 Sender 接口无幂等键** | ✅ 关闭（语义显式化）。`Sender.Send(ctx, DeliveryRequest)`；`DeliveryRequest{ExecutionID, SenderUserID, Target, IdempotencyKey}`，`IdempotencyKey = DeliveryExecution.ID`（跨重试稳定）；Feishu IM v1 无原生幂等参数 → 在接口/适配器注释中**明确声明 At Least Once**，并新增 `studio_schedule_delivery_send_total{channel, idempotency}` 让重复发送窗口可观测，而不是把"恰好一次"当成事实 | `internal/delivery/adapter.go`、`internal/delivery/worker.go`、`internal/platform/telemetry/telemetry.go` |

### 2.3 P2

| 问题 | 修复结果 | 关键落点 |
|---|---|---|
| **P2-1 Invariant L 会误报历史 occurrence**（今天新增的投递目标会被要求出现在昨天的 occurrence 上） | ✅ 关闭。`CountMissingDeliveryExecutions` 增加时间近似条件 `d.created_at <= COALESCE(o.finished_at, o.updated_at)` | `db/queries/execution.sql` |
| **P2-2 Provider MaxInflight 有两个配置源** | ✅ 关闭并明确优先级：**provider catalog（`providers.max_inflight`）为权威**；`AILY_MAX_INFLIGHT` 仅作 bootstrap/默认（catalog 行缺失或为 0 时生效），wiring 处打印实际来源 | `internal/app/app.go`、`internal/platform/config/config.go` |
| **P2-3 `BindProviderSessionOwned` 吞掉 RowsAffected 错误** | ✅ 关闭。`n, err := res.RowsAffected(); if err != nil { return err }` | `internal/execution/artifacts.go` |
| **P2-4 `terminalEventName` default 过宽（未知状态默认按"成功"发事件）** | ✅ 关闭。签名改为 `(string, error)`，未知状态返回 `ErrInvalidTerminalStatus`；Finalize 内只计算一次并复用（事件写入与 post-commit 发布一致） | `internal/execution/finalize.go` |

---

## 3. 关键设计说明

### 3.1 ProviderSlot：attempt-scoped 才是可栅栏的

```go
type ProviderSlot struct {
    RunID      ids.ID
    LeaseEpoch uint64
    LeaseToken ids.ID
    Member     string // "{run_id}:{lease_epoch}:{lease_token}"
}
```

- **Acquire**（单 Lua，原子）：

```lua
ZREMRANGEBYSCORE key -inf now      -- 清过期（崩溃 worker 不永久占额度）
if ZSCORE key member then ZADD key expiry member; return {1, ZCARD} end
if ZCARD key >= max then return {0, ZCARD} end
ZADD key expiry member; return {1, ZCARD+1}
```

- **Renew**（XX-only）：`if ZSCORE() then ZADD ; return 1 end ; return 0` → `0` 即 `ErrProviderSlotLost`，**不重建**。
- **Release**：`ZREM key <精确 member>`。
- 结果：`Worker A(epoch=1)` 的 defer release/renew 无论如何都碰不到 `Worker B(epoch=2)` 的 slot；`max_inflight` 因此在真正的并发路径上成立。

### 3.2 Heartbeat 顺序（必须）

```text
HeartbeatOwned(run lease)
  ├─ lost/err → cancel() → 从 inflight 表移除 → Release 自己的 slot → metrics → stop
  └─ ok       → slot != nil ? Renew(精确 slot) : 不动
```

先续 slot 再判 ownership 会让"已经被栅栏掉的 attempt"继续影响容量账本——这是本轮明确修掉的顺序缺陷。

### 3.3 attempt 语义

```text
attempt  = provider execution count
claim    ≠ attempt
admission requeue（inflight 限制 / limiter 不可用）≠ attempt
panic retry / 429 retry / reaper 重排 ≠ attempt（预算不变）
BeginProviderAttemptOwned = 唯一 attempt++
```

因此 `max_attempts=3` 的 Run 在 provider 满载时可以被无限次 requeue 且始终拥有完整预算；Aily 的 `attempt < max_attempts → Retry else Fail` 判断只在"真的调用过 provider"之后生效。

重排时延区分：provider 失败退避用 `RUN_REQUEUE_DELAY`；admission 竞争用固定 `AdmissionRequeueDelay = 1s`（容量问题不是 provider 故障，暂停 1s 避免对饱和 provider 热循环，同时空出额度后可立即被取走）。

### 3.4 加权优先级流

```text
queue:feishu_aily:interactive   weight 7
queue:feishu_aily:retry         weight 1
queue:feishu_aily:scheduled     weight 2
```

- 生产端：Outbox relay 按 `provider + priority_class` 选 stream；delivery 事件与历史无 class 行保持原单流 `queue:feishu_delivery` / `queue:<provider>`。
- 消费端：`classScheduler`（纯策略、可单测）→ credit 顺序消费 → 全员耗尽即刷新 → 有信用的队列皆空时借用（借用不消耗信用，因此空队列不会浪费 worker 容量）。
- 效果：交互流量下 scheduled 每轮仍拿到 2/10；interactive 空闲时其 7 份额度自动让给其他队列。

> 升级注意：relay 对新产生的 `run.dispatch` 会写入 class stream；**升级时刻**遗留在旧单流中的消息 worker 不再消费，由 fallback scan（`RUN_REAPER_INTERVAL`，默认 20s）兜底认领。无需数据迁移。

---

## 4. 数据库与生成代码

- 无 DDL 变更（不需要新迁移）。
- `db/queries/execution.sql` 变更：
  - `CASClaimRun`：删除 `attempt = attempt + 1`
  - 新增 `BeginProviderAttemptFenced`
  - `RequeueRunFenced`：`available_at` 由硬编码 `+1 SECOND` 改为参数
  - 删除 `SetRunImmediatelyAvailable`
  - 新增 `CreateOutboxEventAt`
  - `CountMissingDeliveryExecutions`：加 `created_at` 时间近似条件
- `sqlc generate` 已重跑（`internal/gen/db/execution.sql.go`、`querier.go`）。

---

## 5. 测试矩阵与结果

### 5.1 新增测试

| 测试 | 验证目标 | 位置 |
|---|---|---|
| `TestProviderSlotStaleReleaseCannotDeleteNewOwnerSlot` | stale release 不能删新 owner 的 slot | `internal/execution/inflight_test.go` |
| `TestProviderSlotStaleRenewCannotExtendNewOwnerSlot` | stale renew 不能续新 owner 的 slot | 同上 |
| `TestProviderSlotRenewMissingDoesNotRecreate` | Renew 不会重建已消失的 slot | 同上 |
| `TestRejectedAttemptNeverHoldsSlotEvenIfRenewed` | 被拒 attempt 即便被错误 Renew 也拿不到 slot | 同上 |
| `TestProviderSlotCrashExpiresSlot` | 崩溃 worker 的 slot 随租约过期 | 同上 |
| `TestProviderInflightGlobalMaxAcrossWorkers` | 跨 worker 全局 max 成立 | 同上 |
| `TestProviderSlotAcquireIsIdempotentForSameAttempt` | 同 attempt 重入不重复占额 | 同上 |
| `TestWeightedFairShareIsSevenOneTwo` | 满负载下 7:1:2 真实份额，每轮首条必为 interactive | `internal/execution/priority_test.go` |
| `TestScheduledNeverStarvesUnderContinuousInteractive` | 持续交互流量下 scheduled/retry 不被饿死 | 同上 |
| `TestIdleClassQuotaIsBorrowed` | 空队列额度被借用 | 同上 |
| `TestBorrowReleasesWhenInteractiveReturns` | 借用不产生饿死债，交互恢复即优先 | 同上 |
| `TestClassSchedulerRefillsAndBorrows` | 刷新/借用边界 | 同上 |
| `TestPriorityClassOfMapping` / `TestDefaultPriorityWeightsShape` | 优先级→class 映射与权重形状 | 同上 |
| `TestProviderAdmissionDoesNotConsumeAttempt` | claim + admission requeue 不消耗 attempt | `tests/integration/provider_admission_test.go` |
| `TestThreeAdmissionRequeuesStillLeavesAttemptZero` | 报告场景：连续 3 次 inflight 拒绝后 attempt 仍为 0 | 同上 |
| `TestProviderAttemptConsumesAttemptAndIsFenced` | 只有 owner 能消耗 attempt，且逐次 +1 | 同上 |
| `TestProviderAttemptExhaustionIsExplicit` | 预算耗尽显式返回 `ErrProviderAttemptsExhausted` | 同上 |
| `TestReaperBeforeProviderAttemptDoesNotExhaustRetries` | 未触达 provider 的 3 次租约过期不致 Run failed | 同上 |
| `TestRetryRunAndOutboxShareRetryAt` | Run.available_at ≡ Outbox.available_at，且 relay 不会提前发布 | 同上 |
| `TestRelayRoutesRunDispatchByPriorityClass` | class 路由命中对应 stream，且不泄漏进旧 FIFO 流 | 同上 |
| `TestCreateRunOutboxCarriesPriorityClass` | 创建 Run 的 outbox 自带 priority_class | 同上 |
| `TestDeliveryRequestIdempotencyKey` | 投递请求携带稳定幂等键 | `internal/delivery/adapter_test.go` |

### 5.2 既有测试同步修正（语义变更导致）

- `TestCASRaceSingleWinner`：claim 后 `attempt == 0`
- `TestLeaseExpiryAndReaper` / `TestReaperRecoveryAtomicFail`：改为"claim → **BeginProviderAttempt** → 租约过期"来耗尽预算（与新语义一致）
- `TestDuplicateQueueMessagesNoDoubleExecution`：fixture 发布到 interactive class stream
- `TestProviderAttemptConsumesAttemptAndIsFenced`：stale 判定改用 epoch 失配（epoch 即写栅栏，token 与 epoch 同源铸造）
- `TestGCRAAgainstRealRedis`：改用 `config.Load()` 取 Redis 配置（原实现只读 `REDIS_HOST/REDIS_PASSWORD` 环境变量，漏掉 `.env.local` 里的密码 → 必然 NOAUTH 失败）

### 5.3 实跑结果

```text
gofmt -l .                              ✅ clean
go build ./...                          ✅
go vet ./...                            ✅
go test ./... -count=1                  ✅ all packages
STUDIO_TEST_REDIS=1  go test ./internal/execution/ -run 'TestProviderSlot|TestProviderInflight|TestRejected'   ✅ 7/7
STUDIO_TEST_TIDB=1 STUDIO_TEST_REDIS=1 go test ./tests/integration/... -count=1                                ✅ 全部通过（74s，真实 TiDB + Redis）
```

新增/修改规模：

```text
.github/workflows/backend.yml
backend-go/db/queries/execution.sql
backend-go/internal/execution/{inflight,worker,priority,retry,recovery,service,outbox,finalize,artifacts,ownership}.go
backend-go/internal/execution/{inflight_test,priority_test,ratelimit_test}.go
backend-go/internal/integrations/aily/executor.go
backend-go/internal/delivery/{adapter,worker,adapter_test}.go
backend-go/internal/platform/{config/config.go, telemetry/telemetry.go}
backend-go/internal/app/app.go
backend-go/cmd/worker/main.go
backend-go/tests/integration/{provider_admission_test,tidb_cas_test,correctness_closure_test}.go
backend-go/internal/gen/db/*        (sqlc 重新生成)
```

---

## 6. 最终验收条件对照（报告第十二部分）

| 验收条件 | 状态 | 依据 |
|---|---|---|
| ProviderSlot attempt-scoped | ✅ | §3.1 |
| stale attempt 不能 release/renew 新 slot | ✅ | 4 个 slot 测试 |
| Admission 不消耗 Provider Attempt | ✅ | 3 个 attempt 测试 |
| Interactive priority 真正作用于正常队列 | ✅ | relay class 路由 + 7:1:2 份额测试 |
| Scheduled 有公平执行保证 | ✅ | 不饿死 / 借用测试 |
| Run retryAt = Outbox retryAt | ✅ | `TestRetryRunAndOutboxShareRetryAt` |
| Provider max_inflight 压测不突破 | ⚠️ 局部 | 单元/集成层已证明；**真实压测未执行**（见 §7） |
| Backend CI GREEN | ⚠️ 待 CI 实跑 | workflow 结构已修复 + actionlint 门禁；本地无法跑 Runner |
| TiDB Integration GREEN | ✅ | 本地真实 TiDB 全绿 |
| MySQL57 Integration GREEN | ⚠️ 待 CI 实跑 | job 结构与 readiness 已修正 |
| Delivery duplicate semantics 已明确/处理 | ✅ | At-Least-Once 显式声明 + 指标 |

---

## 7. 遗留 / 未验证项（本轮如实声明）

1. **GitHub Actions 实跑结果未验证**：本地无法执行 Runner，`check / integration / mysql57` 是否 GREEN 需推送后在 CI 确认；MySQL 5.7 兼容性只能由该 job 证明。
2. **Phase 7 压测未执行**：报告要求的"1000 scheduled @ 08:30 + 持续 interactive"场景需要压测环境与真实 provider 配额，本轮只做到策略级与集成级证明（比例、借用、不饿死、max 不突破的机制验证）。
3. **升级窗口内的旧单流消息**：不做数据迁移，由 fallback scan 兜底（§3.4 说明）。
4. 报告第九部分 Phase 1 建议的独立文件 `provider_slot.go` 未单独拆出——`ProviderSlot` 与 Lua 脚本仍与 `InflightLimiter` 同处 `inflight.go`（职责内聚，行为与建议一致）。
5. 与此前迭代相同的遗留（非本轮范围）：Aily 附件流式上传、Legacy Workflow RuntimeAdapter 收敛。

另：仓库根目录存在上一轮排查遗留的临时文件 `findings.md` / `progress.md` / `task_plan.md`（未跟踪），本报告未修改它们，可按需删除。
