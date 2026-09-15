# Creation Agent Studio — 项目长期记忆

## Admission 状态（截至 2026-09-15 二轮复审后）

复审结论：91/A-，可作上线候选。基线 dev `b2112ad`。历次变更报告都在 `docs/` 下（按主题命名）。

**执行内核（Ownership/Claim/Reaper/Finalize/ProviderSlot）已定型，不要改。**

### 硬性约定（违反会复发 P0/事故）
- `bindingFromExecutionAuthRow` 与 `bindingFromRow` 输出形状必须一致；`Binding.Snapshot()` 无条件写 timeout/config/capabilities（回归测试 `TestAuthorizeExecutionPreservesRuntimeSnapshot` 锁定）
- Provider 门禁按 `provider_key` fail-closed（`provider_id` 常为 NULL，**不要写依赖 provider_id 的授权判定**）；binding 创建/更新写 `b.ProviderID`，迁移 0016 回填
- 授权与 binding 解析必须在最终 Admission Lock 之后（`admitOne` 锁内重读）
- 执行准入唯一门：`catalog.AuthorizeExecution`（authz.go）；新执行入口必须接它；停用 app 对任何人不可执行；普通用户错误一律 404
- 一个 conversation 同时只允许一个非终态 Run：`CreateRunInTx`（conversations 行 FOR UPDATE + Count）→ 409（串行非排队）
- Schedule admission 统一到 schedules 行锁：`GetScheduleRowForUpdate` 返回整行；triggerSchedule/TriggerNow/admitOne 全部「锁内重读→判定→创建」同事务；helper 均为 *InTx
- Conversation 硬删守卫：活跃 Run→409；有 scheduled Run 或 delivery→拒绝硬删（`DeleteConversationCascade` 单事务）；Clear=删 messages+agent_thread。软删除未做
- `Server.RunAdmission` 是长生命周期 RateLimiter，**禁止每请求 new**；outstanding 上限在 `CreateRunAdmitted`（users 行锁→计数→建 Run，锁序恒 users→conversations）
- Schedule 软删（0015）不物理删 schedule_deliveries（避免锁序反转）；`GetScheduleByID` 故伴不过滤 deleted_at（delivery worker 要用）
- 消息落库必须同事务 `TouchConversationUpdated`；Scheduler/TriggerNow 时钟一律 DB 时钟（`DBNow`），不要 `nowFunc()`
- attempt 语义：claim 不+attempt，唯一消耗点 `BeginProviderAttemptOwned`；ProviderSlot attempt-scoped member={run_id}:{lease_epoch}:{lease_token}；heartbeat 先 Run Lease 后 Slot
- 队列三条 Stream `queue:<provider>:interactive|retry|scheduled` 权重 7:1:2（classScheduler）；retry 单事务 Run.available_at≡Outbox.available_at
- delivery=At Least Once，`IdempotencyKey=DeliveryExecution.ID`；provider 并发权威=catalog `max_inflight`

### 数据库与 CI
- **MySQL 5.7 基线**（TiDB 已退出，别加 TiDB 假设）：`192.168.211.26:20336`/`xiaoan`/`test_user`；DSN UTC（`loc=UTC`+`time_zone='+00:00'`）不能动；错误码 1213/1205 保留
- CI 两 job：`check`（gofmt/vet/build/unit/race）+ `integration`（mysql5.7+redis7 唯一闸门，含 execution/delivery 包级 DB 测试）；迁移入口 `go run ./cmd/migrate`；失败诊断读 check-run annotations
- 测试开关 `STUDIO_TEST_DB`；配置一律 `config.Load()`（.env.local 有密码）

### 运行/验证要点
- delivery 是独立 worker（`--provider=feishu_delivery`），只起 aily worker 投递永远 pending；worker 按 provider 分片
- Redis Streams 跨环境继承旧 `queue:*`，上线前必须清或换前缀/DB
- 集成测试会写目标库；改 `db/queries/*.sql` 必须跑 sqlc generate（`~/go/bin/sqlc`）；`go run` 可能被杀软拦截，用 build+执行
- 冒烟无泄漏判据：running=0/leases=0/slots=0/outbox(pending)=0/delivery(pending)=0/occurrence(pending)=0

## 二轮复审整改（2026-09-15 完成，最新）

变更报告：`docs/potal 二轮复审整改变更报告（Scheduler错误分类·WorkerGate·run-now上限·Schedule行锁）.md`。复审基线 dev `b2112ad`，91/A- → 目标 A。

- **P1-1 错误分类**：`app.executionDenied(err)` 只认 7 个策略哨兵（含 wrap）；`EnabledBindingFor/EnabledBinding` 策略拒绝→(nil,nil)，infra error→上抛（scheduler 回滚下个 tick 重试同槽）。`authorizeForOwner` 身份查询故障上抛（只有 `identity.ErrNotFound` 降级 non-staff）。`schedule.ErrApplicationNotSchedulable` 哨兵区分 400 与 500。
- **P1-2 Worker Gate（产品决策）**：App/Binding 停用=硬 Kill（cancel，error_code=execution_disabled）；Provider 停用=Pause（requeue 30s `GatePauseRequeueDelay`，不耗 attempt）；已提交 Provider 不伪强杀。位置：`execute()` 在 ProviderSlot Acquire 之前。`GetRunGateState` 查询只读 app/binding/provider 三个可撤销事实，**不读 runtime_snapshot**；缺 app/binding 行 fail-closed=kill。`app.ExecutionGate` 适配器注入 `cmd/worker` 的 Worker.Gate。指标 `gate_killed/gate_paused`。
- **P1-3 run-now 上限**：`Scheduler.MaxPendingManual`（env `SCHEDULE_MAX_PENDING_MANUAL` 默认 1）在 schedules 行锁内 `CountPendingOccurrences` 后判定，超限 `ErrPendingCapReached`→HTTP 429。pending occurrence 不受 RUN_USER_MAX_OUTSTANDING 约束，这是它存在的意义。
- **§七 Schedule CRUD 行锁**：`Update`/`SetEnabled` 全程在 `GetScheduleRowForUpdate` 事务内（锁内重读→merge PATCH→validate→算 next_run_at→UPDATE）；软删行=ErrNoRows=ErrNotFound。锁序仍 schedules→deliveries。
- 新测试：`internal/app/authz_test.go`（3）+ `tests/integration/review2_fixes_test.go`（5，infra 重试/kill/pause/PATCH 竞态/pending cap）；测试结束要软删自己的 fixture schedule（flaky resolver 会让到期槽每 tick 报错）。
- **教训**：集成测试 fixture 的 applications.slug 是 UNIQUE，重跑套件会撞——seed 数据带 UnixNano 后缀（gateFixture 模式）。
- 本机无 gcc，`-race` 跑不了（CI Ubuntu 上跑）；本地用单测+集成覆盖。

### 下轮待办（复审遗留）
SSE Hub 多路复用、Worker 阻塞 dispatcher、RunEvent 去 COUNT、client_request_id 幂等键、Sidebar keyset 分页、Conversation soft-delete、非终态 Run predicate 统一（waiting_input 等，启用 waiting/resume 前必须 P1）、Schedule Service 全 DB Clock、RateLimiter ctx/nil-Redis 边角。
