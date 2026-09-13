# potal 架构评测问题修复报告

**修复基准：**《potal 完整架构实施评测报告（dev 分支）》
**架构准则：**《Creation Agent Studio 未来演进完整架构文档》
**修复范围：** Execution Correctness Hardening（P0 全部）+ Scheduler Correctness + Product Convergence（P1 主体）
**验证环境：** 真实 TiDB（远程）+ Redis + 前端 tsc/vitest

---

## 一、修复总览

| # | 优先级 | 问题 | 状态 |
|---|---|---|---|
| P0-1 | P0 | Outbox 的 BINARY(16) 原始字节直接转 string，Redis 主唤醒通道实际失效 | ✅ 已修复 |
| P0-2 | P0 | CAS Claim 成功但 Lease 创建失败 → Run 永久 running 卡死 | ✅ 已修复（单事务） |
| P0-3 | P0 | Lease 缺少真正 fencing，stale worker 可写坏 canonical state | ✅ 已修复（lease_epoch + 全链路 fence + lease-lost 取消） |
| P1 | P1 | fire_once 退化为逐槽 catch_up | ✅ 已修复 |
| P1 | P1 | catch_up 无上限，可能 43200 Run 风暴 | ✅ 已修复（MaxCatchUpSlots=10） |
| P1 | P1 | Run Now 在 overlap=queue 时突破不并行语义 | ✅ 已修复（Occurrence Admission） |
| P1 | P1 | 普通用户进入通用账密登录页（偏离 Feishu SSO Only） | ✅ 已修复 |
| P1 | P1 | content.delta 逐条写 TiDB（写放大） | ✅ 已修复（transient + content.chunk 聚合） |
| P1 | P1 | RuntimeSnapshot 裸 type assertion 可能 panic | ✅ 已修复（typed validation → run_config_invalid） |
| P1 | P1 | Redis 限频器故障完全 fail-open | ✅ 已修复（本地 GCRA 降级） |
| P2 | P1 | Aily 附件整文件进内存（[]byte → io.Reader 流式） | ⏳ 未做（Iteration C 范围，见遗留） |
| P2 | P1 | Legacy workflow execution 向 RuntimeAdapter 收敛 | ⏳ 未做（迁移期技术债，报告允许） |

---

## 二、P0 修复明细

### P0-1 Outbox UUID 边界（outbox.go）

`Relay.RunOnce` 中 `string(row.AggregateID)` 把 BINARY(16) 原始字节当成 Go string 写入 Redis Stream，Worker 端 `ids.Parse()` 必然失败并 ACK 丢弃——主唤醒通道长期静默失效，全靠 TiDB fallback scan 兜底。

**修复：** `"run_id": mustID(row.AggregateID).String()`，边界统一为 canonical UUID。
全库排查其余 `string(rawBytes)` 用法：剩余 3 处均为 Go→DB 边界的字节透传（绑定 BINARY(16) 列），符合 ID Boundary 规则，保留。

### P0-2 Claim + Lease 原子化（service.go `ClaimAndLease`）

原逻辑 `CASClaim → AcquireLease` 两步非原子，Lease INSERT 失败时 Run 已 running、消息已 ACK → 无 lease、无消息、reaper 扫不到 → 永久卡死。

**修复：** 新增 `ClaimAndLease`，单个 TiDB 事务内完成：
```
UPDATE runs SET status='running', attempt=attempt+1, lease_epoch=lease_epoch+1
WHERE id=? AND status='queued'      -- CAS
SELECT lease_epoch FROM runs WHERE id=? -- 同事务读取 epoch
INSERT INTO run_leases(...)             -- 失败则整体回滚，Run 保持 queued
COMMIT
```
Worker 的 Redis Stream 路径与 fallback scan 路径统一走该入口。

### P0-3 Lease Fencing（迁移 0009 + service.go + worker.go + executor.go）

**Schema：** 迁移 `0009_runs_lease_epoch` 为 `runs` 增加 `lease_epoch BIGINT UNSIGNED NOT NULL DEFAULT 0`（保持 MySQL 5.7 兼容）。每次 claim 自增；claim 时捕获的 epoch/token 为**唯一写权限凭证**，绝不从 DB 刷新（防止 stale worker 采纳新 owner 的 epoch）。

**受 fence 保护的写入（affected=0 → ErrLostOwnership → worker 立即停止写入）：**
- `AppendEventFenced`：事件序号分配的 FOR UPDATE 行锁加 `status='running' AND lease_epoch=?` 谓词
- `CASFinishRunFenced`：终态 CAS 只允许当前 epoch 完成 running run
- `UpdateRunExternalIDFenced`：external_run_id set-once + fence
- `RequeueRunFenced` / `FailRunFenced`：中断重排队/失败
- `DeleteLeaseFenced`：只允许删除自己的 lease 行（stale worker 无法删新 owner 的 lease）
- `HeartbeatLeaseFenced`：按 lease_token（而非 worker_id）续租
- `CheckOwnership`：非 CAS 写入（artifact upsert、assistant message）前的所有权门

**Lease lost → 取消本地执行：** Worker `inflight map[ids.ID]bool` 升级为 `executionControl{cancel, token, epoch}`；heartbeat 续租失败即 `cancel()` 本地执行上下文——Provider 远程调用无法取消，但旧 worker 失去「写 Studio canonical state」的全部权利，这正是 fencing 语义。

**Executor（aily）改造：** 所有 AppendEvent/Finish/UpdateExternalRunID/ReleaseInterrupted 走 fenced 变体；`preserveOwnership` 在 run 刷新（reconcile/poll 路径）时保留 claim-time fence；`classifyError` 对 `ErrLostOwnership` 直接穿透（不再进入 retry/panic 体系）。

---

## 三、Scheduler 语义修复

**misfire 三策略（此前 fire_once 实际退化为 catch_up）：**
- `skip`：记录 skipped occurrence，`next_run_at` 一次性快进到第一个未来槽位（`advancePast`）
- `fire_once`（默认）：对最老错过的槽位**只创建 1 个**补偿 Run（保留原始计划时间），随后快进——绝不逐槽重放
- `catch_up`（新增策略）：允许逐槽补跑，但 `MaxCatchUpSlots=10` 封顶；积压超限直接快进，防止停机 30 天 × 每分钟任务 = 43200 Run 风暴

**Run Now 统一 Occurrence Admission：**
`TriggerNow` 在 overlap=queue 且存在 active occurrence 时不再直接创建 Run（旧实现会造成 A running + B queued 被另一 worker 取走 → 并行），而是落一条 **pending** occurrence；Scheduler 扫描循环新增 `admitPending`：该 schedule 无 queued/running occurrence 时才把 pending 转成 Run（事务内二次 CAS 检查防并发 admit）。overlap=skip 维持 409 拒绝。

---

## 四、限频降级与写放大

**Rate limiter（ratelimit.go）：** Redis GCRA 调用失败时不再 fail-open（否则 N 个 worker 同时放开 → Aily 429 storm），改为降级到**进程内同参数 GCRA**（aggregate 上限 = limit × worker 数，仍远优于无限流）；`Degraded()` 暴露降级状态用于 `provider_limiter_degraded` 监控；Redis 恢复自动回到分布式模式。

**content.delta 聚合（executor.go + useRunChatStore.ts）：**
- delta 只经 Redis pub/sub 瞬态转发（`PublishTransient`，sequence=0，SSE 网关 live 转发、replay 永不出现）
- 每 2000 字符或 500ms 聚合为持久化 `content.chunk` 事件（新增事件类型），payload 携带增量 `text` + 累计 `snapshot`
- 前端对 `content.chunk`：有 `snapshot` 则**替换**（自愈，replay 重放不重复），否则追加
- 流结束 flush 尾部 buffer 后再走 Final Reconciliation

**RuntimeSnapshot typed validation：** `snapshot["external_resource_id"].(string)` 裸断言替换为 `parseAilySnapshot()`，配置缺失/类型错误 → `run_config_invalid` 终态，绝不进 panic/retry。

---

## 五、登录入口收敛（Feishu SSO Only）

- `ProtectedRoute` 未登录 → `/login`（新增路由：整页自动跳转 `/api/identity/oauth/start`，用户不看到登录页）
- 新增 `/login/admin`：唯一出现用户名/密码的入口（`POST /api/identity/admin/login`，Argon2id 本地管理员）
- `/auth/login` 降级为飞书重试 fallback（OAuth 失败时），移除用户名密码表单、企业 SSO、立即注册
- 移除 `/auth/register` 路由；`useAuthStore` 新增 `adminLogin`

---

## 六、测试矩阵（全部在真实 TiDB/Redis 上通过）

新增故障注入测试（`tests/integration/lease_fencing_test.go` + `tidb_schedule_test.go`）：

| 测试 | 对应评测矩阵 |
|---|---|
| `TestOutboxRelayCanonicalUUID` | #1 Outbox→Relay→Redis→canonical UUID 断言 |
| `TestClaimAndLeaseAtomicity` | #2 故意让 lease INSERT 失败 → Run 不进入 running |
| `TestLeaseFencingStaleWorker` | #3 chaos：A claim→暂停→过期→B 重取→A 复活；断言 A 无法 AppendEvent/Finish/Requeue/删 B 的 lease，B 全部成功 |
| `TestHeartbeatFencedByToken` | token 级续租（同 worker_id 回收也不能续） |
| `TestMisfireFireOnceSingleCompensation` | #4 停机 10 天 fire_once → 恰好 1 个补偿 Run |
| `TestMisfireCatchUpOverLimit` | #5 missed=30 > maxCatchUp=10 → 不补跑 |
| `TestRunNowQueueBehindActive` | #6 active running 时 TriggerNow → pending，不并行 |
| `TestMisfireSkipFastForward` / `TestMisfireCatchUpBoundedReplay` | skip 快进 / catch_up 有界逐槽 |

**回归结果：** 后端 `go build ./...` / `go vet ./...` / `go test ./...` 全绿；真实 TiDB/Redis 集成测试 22 个用例连续三轮全绿；前端 `tsc --noEmit` 无错、vitest 112/112 通过。既有 `TestOverlapSkip` 因 admission 语义修正改用真实 `running` 状态模拟（原用 `pending` 模拟，现会被 admission 正确转化）。

---

## 七、遗留项（按评测报告归属后续迭代）

1. **Aily 附件流式上传**（P1 #9，Iteration C）：`[]byte` → `io.Reader + io.Pipe + multipart.Writer`，涉及 Adapter 接口重构，建议独立迭代。
2. **Legacy workflow → RuntimeAdapter 收敛**（P1 #10）：迁移期技术债，评测明确允许暂缓。
3. **P2 清单**：RunEvent retention、Provider circuit breaker、DLQ、`provider_limiter_degraded` 接入 Prometheus 指标注册、Execution Invariant Checker（"running run 必须有 active lease" 报警）等。
4. 迁移 0009 已应用到开发库（`-migrate`）；生产部署时随发布执行。
