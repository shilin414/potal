# Progress

## 2026-09-14

### 第一轮（已提交部分）
- Read skill guidance and audit report structure.
- Identified the actionable P0/P1 scope and excluded security-history work.
- Began code-level audit of backend-go execution paths.
- Implemented the P0/P1 code and CI changes.
- Added `backend-go/tests/integration/production_hardening_test.go`.
- Regenerated sqlc code and formatted all Go files.
- Verified: `go build ./...`, `go vet ./...`, `go test ./... -count=1`, and
  `STUDIO_TEST_TIDB=1 STUDIO_TEST_REDIS=1 go test ./tests/integration/... -count=1` all pass.

### 第二轮：按「potal 剩余问题开发执行报告」收口

#### P0-1 CI 失败：最终根因（第一次判断被 CI 证据推翻）

失败用例：`TestWorkerHoldsRunBackWhenProviderSlotIsFull`（25s 超时）与
`TestConcurrentProviderAdmissionOnFreshProvider`（`admitted=8 rejected=0, want 1/7`）。

第一次修复（认为并发首次物化 admission lock 行触发 9007）**不足以修复**：推送后 CI 仍红。
由于 GitHub job 日志需鉴权，改为把失败证据写进 check annotation（测试用 t.Logf 复述 worker 日志 +
槽位/租约/事件事实，workflow 用 `grep -B` 提取失败用例自身的日志行），取到决定性证据：

```text
concurrent admission: admitted=8 rejected=0, want 1/7
provider admission admitted 2 concurrent executions with max_inflight=1:
  calls=2 act=2 max=2 slots=[01a09f2d@e1+59915ms,01a09f2d@e1+59922ms]
```

- 真正根因：**准入决策从未真正串行化**。TiDB 以 optimistic 事务模式执行该显式事务
  （`SELECT @@tidb_txn_mode` 实测为 `optimistic`），此时 `SELECT ... FOR UPDATE` 不阻塞并发决策，
  于是每个竞争者都 count 到 0 再各自 insert 自己的槽位（不同主键、无写冲突）→ 超发。
  本地之所以一直绿，是因为本地 DB 极快，胜者的整笔事务在其它竞争者 count 之前就提交了——
  属于“时序侥幸”，不是锁在起作用。
- 修复：把串行化改成**对共享锁行的真实写入**（迁移 0013 给 `provider_admission_locks`
  增加 `admissions` 计数器，`LockProviderAdmission` 由 `SELECT ... FOR UPDATE` 改为
  `UPDATE ... SET admissions = admissions + 1`）。悲观模式下写锁阻塞后来者；乐观模式下
  提交时产生 9007 写冲突，由既有“整笔决策重试”逻辑重放（重放后重新 count → 正常拒绝）。
  同时把**拒绝路径改为回滚而不提交**，使并发的拒绝之间不再互相冲突、不会耗尽重试预算。

#### 本地可判别验证（关键）

- 把竞争者从 8 提到 64（保证任何机器上决策窗口都会交错）后，**临时移除串行化写入即可在本地复现**：
  `capacity rejection depth=3` / `depth=17`（即 max_inflight=1 下并发放行 3 / 17 个）。
- 恢复写入后：64 竞争者下 64 次决策恰好 1 个通过、63 个正常拒绝（`-count=3` 全绿）；
  并新增断言 `admissions == admitted`，钉住“决策必须做一次提交成功的写入”这一机制
  （只做 locking read 的实现会让计数保持 0）。
- 另用 DSN 临时强制 `tidb_txn_mode=optimistic` 复跑准入相关用例 3 轮，全绿。

#### P1/P2/P3（第一轮已完成，未变）

P1-1/P1-2 边界证明测试（`tests/integration/provider_slots_test.go`）：
- 新增 `TestProviderSlotCannotBeRenewedByPreviousEpoch`（旧 epoch 续租被拒、且不触碰新 owner 的 expires_at）。
- 新增 `TestProviderSlotCannotBeReleasedByPreviousEpoch`（旧 epoch 释放只能删自己）。
- 新增 `TestReaperDeletesSlotsForRecoveredOwnership`（recovery 事务内删除该 run 全部旧 epoch 槽位）。
- 新增 `TestFinalizeAndRetryDeleteSlotAtomically`（合并原 finalize/retry 两个用例，断言槽+lease 同事务消失）。
- 新增 `TestRedisFlushDoesNotIncreaseProviderCapacity`：max=2 → A/B 占满 → FLUSH 整个 Redis DB →
  Run C 仍被 Provider Admission 拒绝；handler 并发高水位恒 <= 2；完成后 Redis 队列从未重建（全流程只靠 TiDB）。
- 新增 `TestWorkerRestartDoesNotIncreaseProviderCapacity`：第二个执行平面（新 DB handle + 新 slot store + 新 worker id）
  不能放大容量，前一个平面结束后立即可用。
- 新增 `TestExecutionFencingMatrix`（`tests/integration/fencing_matrix_test.go`）：current / expired / stale
  × heartbeat / begin_attempt / retry / finalize / slot_acquire / slot_renew / slot_release 共 21 个子用例。

P2-1 Invariant L snapshot：核对完成（迁移 0012 + occurrence_delivery_expectations + scheduler 各创建路径同事务捕获 +
dispatcher 读取快照 + invariant L 查询改为 expectations）。

P3 生产指标（`internal/platform/telemetry/telemetry.go`）：
- 新增 `studio_provider_admission_total{provider,result}`（admitted / capacity_rejected / lost_ownership / provider_slot_lost）。
- 新增 `studio_run_reaper_total`、`studio_run_ownership_lost_total`、`studio_priority_dispatch_total{class}`、
  `studio_delivery_retry_total`，并在 worker / reaper / heartbeat / delivery 处接线。
- 新增 `internal/platform/telemetry/metrics_test.go` 固定指标名与 label 契约。
- 既有等价指标保留：`studio_execution_invariant_violation_total{type}`、`studio_provider_limiter_degraded`。

验证（本地）：
- `gofmt -l .` 干净；`go build ./...`、`go vet ./...` 通过。
- `go test ./... -count=1` 全绿。
- `STUDIO_TEST_TIDB=1 STUDIO_TEST_REDIS=1 go test ./tests/integration/... -count=3` 连续 3 轮全绿（610s，含 64 并发准入用例）。
- 同环境下 `-shuffle=on` 全绿，未发现测试执行顺序依赖。
- `go test -race` 本机无法运行（Windows 无 gcc / CGO_ENABLED=0），由 CI `check` job（ubuntu）覆盖。

迁移：
- 0011 provider execution slots / admission locks
- 0012 occurrence delivery expectations（Invariant L snapshot）
- 0013 provider admission lock write（准入串行化写，CI 超发根因修复）

未完成（明确不在本轮范围）：
- P2-3 生产 chaos / 30~60min soak：报告标注「CI 全绿以后再做，不作为当前开发阻塞」，本轮不做。
