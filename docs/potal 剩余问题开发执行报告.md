# potal 剩余问题开发执行报告

## 开发原则

本轮禁止为了“进一步优化”而重新修改已经成立的核心模型。

优先顺序：

```text
P0
CI deterministic / integration closure

↓

P1
Provider Admission 极端故障证明

↓

P2
Invariant L snapshot

↓

P2/P3
Production chaos / soak
```

---

# P0-1 修复最新 TiDB Integration CI

## 当前状态

GitHub Actions：

```text
Run 34814480676
```

完整 TiDB integration Job 未通过。

必须以：

```text
最新 code SHA
+
同一次 workflow
+
全部 required jobs
```

全绿为最终验收。

## 执行要求

先定位失败测试，不要：

```text
增加 sleep
扩大 timeout
忽略 flaky test
continue-on-error
```

直接把问题掩盖掉。

重点检查本轮新增并发测试与共享数据库测试之间是否存在：

```text
残留 runs

provider_execution_slots

provider_admission_locks

outbox

delivery_execution

固定 provider key

RecoverExpiredLeases(limit=N)

测试执行顺序依赖
```

优先查看：

```text
backend-go/tests/integration/provider_slots_test.go

backend-go/tests/integration/clock_authority_test.go

backend-go/tests/integration/*

backend-go/internal/execution/recovery.go
```

## 测试隔离建议

每个测试生成唯一：

```text
provider key
application
run
schedule
execution
```

例如：

```text
test-provider-{uuid}
```

避免所有测试都竞争：

```text
feishu_aily
```

同一个 provider admission lock。

如果确实需要测试 global provider limit，再专门写共享 provider 的 test suite。

## 验收

必须得到：

```text
check          PASS
integration    PASS
mysql57        PASS
```

并且：

```text
go test -race
PASS
```

不能只重跑失败 Job 一次就判定完成。

建议至少连续：

```text
3 次完整 workflow
```

全部通过。

---

# P1-1 增加 Provider Slot / Run Lease 极端边界证明

现有结构已经合理，下一步不是再改架构，而是证明边界条件。

新增测试：

```text
TestProviderAdmissionRejectsStaleOwnershipBeforeReaper

TestProviderSlotCannotBeRenewedByPreviousEpoch

TestProviderSlotCannotBeReleasedByPreviousEpoch

TestRedisFlushDoesNotIncreaseProviderCapacity

TestWorkerRestartDoesNotIncreaseProviderCapacity

TestReaperDeletesSlotsForRecoveredOwnership

TestFinalizeAndRetryDeleteSlotAtomically
```

其中最重要的是：

```text
Redis FLUSH
```

测试。

测试过程：

```text
max_inflight = 2

Run A → active
Run B → active

确认 TiDB active slots = 2

FLUSH Redis

创建 Run C

Run C 必须仍被 Provider Admission 拒绝
```

验收：

```text
remote handler entered count
永远 <= 2
```

这直接证明：

```text
Redis 不再是 Provider capacity truth
```

---

# P1-2 增加 Long Pause / Lease Expiry 故障测试

建议模拟：

```text
Worker 获取 ownership
Provider 已执行

Worker heartbeat 长时间暂停
```

然后：

```text
lease expire

Reaper 恢复 ownership

新 Worker claim
```

验证：

```text
旧 epoch 无法：
    heartbeat
    begin attempt
    renew slot
    release 新 slot
    finalize
```

对应测试：

```text
TestStaleEpochCannotMutateAfterLeaseRecovery
```

需要把所有 fenced operation 都跑一遍。

这实际上是一次：

> Ownership fencing matrix test

非常适合作为今后的 regression test。

---

# P2-1 Invariant L 改为 Snapshot 模型

当前保持 P2 是合理的，但未来建议做掉。

不要继续给：

```text
created_at
finished_at
updated_at
```

堆时间判断。

建议在 occurrence 创建时保存：

```text
delivery_required
delivery_target_type
delivery_target_id
delivery_policy_version
```

或者单独建立：

```text
occurrence_delivery_expectations
```

Invariant L：

```text
WHERE delivery_required = true
AND successful delivery 不存在
```

这样：

```text
Application 后来改配置
Schedule 后来改 delivery
用户后来改 destination
```

都不会改变历史 occurrence 应该具备什么 Delivery 的事实。

验收：

```text
Occurrence created
→ 修改 Schedule delivery config
→ Invariant 对历史 occurrence 结果不变
```

---

# P2-2 建立 Execution Fencing Matrix

建议补一张自动化测试矩阵：

| 操作 | 当前 owner | stale owner | expired lease |
|---|---:|---:|---:|
| Heartbeat | PASS | REJECT | REJECT |
| Begin Attempt | PASS | REJECT | REJECT |
| Retry | PASS | REJECT | REJECT |
| Finalize | PASS | REJECT | REJECT |
| Slot Acquire | PASS | REJECT | REJECT |
| Slot Renew | PASS | REJECT | REJECT |
| Slot Release | PASS | 只能删自己 | 不能影响新 owner |

把这组测试固定下来以后，未来 Execution 层再改代码时非常有价值。

---

# P2-3 Production Chaos Test

CI 全绿以后再做，不作为当前开发阻塞。

建议环境：

```text
3 worker
TiDB
Redis
Mock Provider
```

持续产生：

```text
interactive
retry
scheduled
```

同时随机：

```text
kill -9 worker

restart Redis

FLUSH Redis test DB

provider 429

provider timeout

SSE interruption

DB transient error

worker pause

reaper overlap
```

检查 invariant：

```text
active slots <= max_inflight

attempt == provider-start count

scheduled never starves

stale epoch cannot finalize

terminal run has no active slot

retry_at >= expected DB time

no orphan provider slot
```

建议：

```text
30~60 min soak
```

而不是只跑几十秒。

---

# P3 生产指标

建议最终确认已有或补齐：

```text
studio_provider_inflight

studio_provider_admission_total{
    result=
        admitted
        capacity_rejected
        lost_ownership
        provider_slot_lost
}

studio_run_reaper_total

studio_run_ownership_lost_total

studio_rate_limit_degraded

studio_priority_dispatch_total{class=}

studio_delivery_retry_total

studio_invariant_violation_total{type=}
```

并重点告警：

```text
orphan_provider_slot > 0

provider_slot_lost > 0

rate_limit_degraded 持续

reaper 突增

capacity_rejected 长时间高位
```

---

# 最终开发顺序

建议开发人员严格按：

```text
1. 找出并修复 Run 34814480676 的 integration failure
2. 同一 SHA 连续 3 次完整 CI GREEN
3. 补 ownership fencing matrix
4. 补 Redis-loss / worker-restart capacity test
5. 补 long-pause / stale epoch chaos test
6. 实现 Invariant L snapshot
7. 再做生产 soak / chaos
```

## 不建议再做

当前暂时不要：

```text
重写 Scheduler

重新设计 Provider Slot

重新拆 Run / Lease

把 capacity 搬回 Redis

引入新的分布式锁组件

为了 Exactly-Once 重构 Delivery

继续增加复杂 borrowing 规则
```

这些都会增加系统复杂度，而当前核心模型已经足够稳定。

---

# 完成标准

下一版本我认为可以进入正式 Production Ready 的最低门槛：

```text
[ ] 最新代码 HEAD 全部 required CI GREEN
[ ] 连续至少 3 次 workflow GREEN
[ ] TiDB integration GREEN
[ ] MySQL 5.7 GREEN
[ ] race GREEN
[ ] Redis state loss 不放大 Provider capacity
[ ] stale ownership fencing matrix 全部通过
[ ] worker crash/reaper 恢复测试通过
[ ] 无 orphan provider slots
```

Invariant L snapshot 可以作为：

```text
P2 后续工程质量项
```

不强制卡第一版生产上线。