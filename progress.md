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

P0-1 CI 失败的根因已定位并确认修复：
- GitHub Actions run 34814480676 唯一失败 job = `integration`，唯一失败测试 =
  `TestWorkerHoldsRunBackWhenProviderSlotIsFull`（25s 超时）。
- 根因：全新 provider 上两个 run 并发做出首次 admission 决策时，双方都在显式事务里
  创建 `provider_admission_locks` 行，TiDB 乐观事务让一方拿到 9007 write conflict；
  旧代码把它当基础设施错误 → `provider_inflight_unavailable` 重排，测试等不到
  `provider_inflight_limit`。
- 修复（工作区）：`EnsureProviderAdmissionLock` 在事务外先物化 + `Acquire` 对整个
  事务做 9007/1213/1205 安全重试（`internal/execution/slots.go`）。

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
- `STUDIO_TEST_TIDB=1 STUDIO_TEST_REDIS=1 go test ./tests/integration/... -count=3` 连续 3 轮全绿（318s）。
- 同环境下 `-shuffle=on` 全绿（81s），未发现测试执行顺序依赖。
- `go test -race` 本机无法运行（Windows 无 gcc / CGO_ENABLED=0），由 CI `check` job（ubuntu）覆盖。

未完成（明确不在本轮范围）：
- P2-3 生产 chaos / 30~60min soak：报告标注「CI 全绿以后再做，不作为当前开发阻塞」，本轮不做。
- 最新 SHA 的 CI 三连绿需 push 后由 GitHub Actions 验证（本轮不提交、不推送）。
