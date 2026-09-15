# Creation Agent Studio — 项目长期记忆

## 当前状态（截至 2026-09-15 第六轮复审整改后）

第六轮 90/B+ **BLOCKED**（唯一阻断项 = 0018 改写历史事件事实）→ P0 迁移 0020 + P1 SSE cancelled fallback + P2 duration 已全部修复，本机全绿，预期解除阻断回到 96/A。基线 dev `78d2793`（第五轮功能提交 `0bdcf30`）。历次变更报告都在 `docs/` 下（按主题命名）。

**执行内核（Ownership/Claim/Reaper/Finalize/ProviderSlot）已定型，不要改。**

### 第六轮新增约定（2026-09-15）
- **`runs.status='interrupted'` 与 `run_events.event_type='run.interrupted'` 是两件事，永远不要合并处理**：
  status=interrupted 是终态别名（≈failed，见 IsSettled/IsTerminal）；而 **event `run.interrupted` 是 overloaded 的**——
  旧 `releaseInterrupted` 先写事件再判 attempt，所以 `{"reason":...}`（无 status）= retry 标记（Run 还会回来、可能成功），
  只有旧 `Finish(StatusInterrupted)` 写的 `{"status":...}` 才是真终态。**判别信号：有 reason 且无 status → retry-origin**
- 0018 **禁止修改**（已执行）；它的 over-conversion 由 **0020** 前向修复（reason-only → `run.retrying`；终态 run 缺 canonical terminal event 则补一条）。0020 扫描集合**不含 interrupted**（避免与 0018 竞争重复）。0020 down = `SELECT 1;`
- 新增 migration 时 `MAX(sequence)` 一定要 `COALESCE(MAX(...),0)+1`（MAX 在空集返回 NULL）
- **SSE synthetic terminal 必须按状态映射**：`syntheticTerminalEventName`（succeeded→completed / cancelled→cancelled / failed|interrupted→failed），未知或非 settled 返回 `ok=false` 且**不合成**——绝不允许默认 `run.completed`（会把 cancelled 变成成功）
- **`studio_run_duration` 两端都必须取 DB 时钟**：`dbClockDuration(sql.NullTime, sql.NullTime)`；finalize 在 CAS **之后重读行**拿 finished_at（不信调用方传入的 `run.StartedAt` 快照），负值丢弃。不要再用 `time.Since`
- finalize 写 terminal event 用 `AppendRunEventAtSequence` + `nextEventSequenceTx`（持 run 行锁下 COUNT，不再重复读）

### ⚠️ 报告类「全库 COUNT 期望 0」校验的必读前车之鉴
第六轮报告 §25 给了两条全库扫描 SQL（期望 0），第一轮跑出非零却不是迁移问题，而是**集成 fixture 残留**：
`seedConversation` / `seedUser` 都不注册清理；用 `seedInterruptedRun` + `seedUser` 组合会把 FK 父行先删掉留下孤儿 Run；
表驱动子测试每个都会泄一行。**跑这类校验前必须先确认/补齐 fixture 清理**，否则既可能假红也可能掩盖真回归。
清理时用 `provider LIKE 'itest%'` + `username LIKE 'itest%'` 圈定，别误删真实数据。新增 seed helper 一律自带
`t.Cleanup`（按 FK 顺序：events→leases→slots→outbox→run→conversation→user）。

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
- **授权分类双保险（三轮 P0 教训）**：调用点必须先显式判 `err == nil` 成功路径，再 `executionDenied(err)` 分类——不允许只依赖分类器；`executionDenied(nil)` 恒 false（测试 `TestExecutionDeniedNilMeansSuccess` 锁定）；正向 Happy-Path 必须有真实链路测试（不许只测失败路径/mock resolver）
- **Gate 语义（三轮定型）**：双检查点 level-triggered——Gate 1（worker claim 后）+ Gate 2（`execution.PreSubmitGate`，aily executor 在 ChatsL.Acquire 后、BeginProviderAttempt 前调用）；kill=cancel、pause/infra/未知 action=**Defer fail-closed**（`DeferOwnedRunAfter` 不改 priority、occurrence running→queued 同事务、outbox 用原优先级 class、新事件 `run.deferred`）；未知 GateAction 绝不允许继续 Handler（指标 `gate_unknown`）；run.started 在 Gate allow 之后才发；Execution Generation（严格 edge-triggered）明确不引入

### 第五轮新增约定（2026-09-15，93/A- → 目标 96/A）
- **`interrupted` = 收口前终态别名**（等价 failed，新代码永不写入）：`IsSettled()` = canonical 终态 ∪ {interrupted}；`IsTerminal()` 只认 `{cancelled,succeeded,failed}`；SQL 谓词一律 `NOT IN ('cancelled','succeeded','failed','interrupted')`；`FinalizeOwnedRun` 通过 `IsTerminal` 拒绝写入 legacy 别名。迁移 0018 归一历史行（含为无事件行合成 `run.failed`，保 invariant D）
- **前端 store 异步写一律 functional setState**：任何 `await` 之后不得使用 await 之前取的 state/conv 快照；`activeRunId` 用 compare-and-clear；远端拉取失败的字段用 `null` 表示「未知不清空」，只有成功的空数组才清空
- **channel 发送必须可取消**：producer 一律 `select { case out <- ev: case <-ctx.Done(): }`；测此类修复时假流必须**无尽头**且先等缓冲满，否则 drain 会制造假阴性
- **Clock Authority 无兜底**：`dbNow/dbNowTx` 返回 error 就中止/跳 tick，绝不回落本机时钟
- **限流 ctx 先判**：`AllowKey` 所有分支之前先 `ctx.Err()`；HTTP 层 `err != nil` 直接返回错误（不是 429），客户端已断开时什么都不写
- **`MarkRunStartedOwned` 返回 DB 时间**，worker 必须 `claimed.Run.StartedAt = &t`（否则 `studio_run_duration` 对全量 run 静默为 0）
- **detached cleanup 用 `execution.NewCleanupContext(parent)`**（5s 有界、继承 values、丢弃父 deadline）——不要用裸 `context.Background()`，也不要用 `WithoutCancel`（会继承已过期的执行 deadline）
- 迁移 **0019** = provider_id 完整 reconciliation（LEFT JOIN，key 解析不到置 NULL）；**0017 已执行过，禁止修改**

### 第四轮新增约定（2026-09-15，P1 已关闭）
- **Gate 2 只能在真实 Provider Submit 之前**：`beginSubmit`（Gate→attempt CAS）是唯一入口，本地准备（thread DB、ValidateContent/Attachments）必须在 Gate 之前且不消耗 attempt；`preSubmitStop` 包装避免被 classify 成 Provider 故障
- **Streaming 必须同步 Open**：`OpenStreamChat` 在同 goroutine 发 POST，goroutine 只做 `pumpSSE` 帧解析（禁止在 goroutine 内发 POST，会重开 Gate→Submit 窗口）
- `aily.ProviderAPI` 是「Provider 是否被触达」的测试 seam，新测试用它统计真实调用，不要只数 helper 调用次数
- **started_at = gate allow 的时间**，不是 claim 时间：`CASClaimRun` 不写，由 `MarkRunStartedOwned` 在 Gate1 allow 后、`run.started` 前写，`COALESCE` 保留首次；`finalize.go` 必须 `StartedAt != nil` 再 `IsZero()`（指针解引用会 panic）
- **前端 `run.cancelled` 是独立终态**（`execution_disabled` = 硬取消，非失败），reducer 与 `finalizeRun` 都不得映射成 done；`run.deferred` 是非终态等待提示，靠 `run.started` 清除
- 非终态谓词统一 `status NOT IN ('cancelled','succeeded','failed')`；迁移 `0017` 修 provider_id 指向错误的历史 binding（0016 只修 NULL）

### 数据库与 CI
- **MySQL 5.7 基线**（TiDB 已退出，别加 TiDB 假设）：`192.168.211.26:20336`/`xiaoan`/`test_user`；DSN UTC（`loc=UTC`+`time_zone='+00:00'`）不能动；错误码 1213/1205 保留
- CI 两 job：`check`（gofmt/vet/build/unit/race）+ `integration`（mysql5.7+redis7 唯一闸门，含 execution/delivery 包级 DB 测试）；迁移入口 `go run ./cmd/migrate`；失败诊断读 check-run annotations
- 测试开关 `STUDIO_TEST_DB`；配置一律 `config.Load()`（.env.local 有密码）

### 运行/验证要点
- delivery 是独立 worker（`--provider=feishu_delivery`），只起 aily worker 投递永远 pending；worker 按 provider 分片
- Redis Streams 跨环境继承旧 `queue:*`，上线前必须清或换前缀/DB
- 集成测试会写目标库；改 `db/queries/*.sql` 必须跑 sqlc generate（`~/go/bin/sqlc`）；`go run` 可能被杀软拦截，用 build+执行
- 冒烟无泄漏判据：running=0/leases=0/slots=0/outbox(pending)=0/delivery(pending)=0/occurrence(pending)=0

## 二轮复审整改（2026-09-15）

变更报告：`docs/potal 二轮复审整改变更报告（Scheduler错误分类·WorkerGate·run-now上限·Schedule行锁）.md`。复审基线 dev `b2112ad`，91/A- → 目标 A。

- **P1-1 错误分类**：`app.executionDenied(err)` 只认 7 个策略哨兵（含 wrap）；`EnabledBindingFor/EnabledBinding` 策略拒绝→(nil,nil)，infra error→上抛（scheduler 回滚下个 tick 重试同槽）。`authorizeForOwner` 身份查询故障上抛（只有 `identity.ErrNotFound` 降级 non-staff）。`schedule.ErrApplicationNotSchedulable` 哨兵区分 400 与 500。
- **P1-2 Worker Gate（产品决策）**：App/Binding 停用=硬 Kill（cancel，error_code=execution_disabled）；Provider 停用=Pause（requeue 30s `GatePauseRequeueDelay`，不耗 attempt）；已提交 Provider 不伪强杀。位置：`execute()` 在 ProviderSlot Acquire 之前。`GetRunGateState` 查询只读 app/binding/provider 三个可撤销事实，**不读 runtime_snapshot**；缺 app/binding 行 fail-closed=kill。`app.ExecutionGate` 适配器注入 `cmd/worker` 的 Worker.Gate。指标 `gate_killed/gate_paused`。
- **P1-3 run-now 上限**：`Scheduler.MaxPendingManual`（env `SCHEDULE_MAX_PENDING_MANUAL` 默认 1）在 schedules 行锁内 `CountPendingOccurrences` 后判定，超限 `ErrPendingCapReached`→HTTP 429。pending occurrence 不受 RUN_USER_MAX_OUTSTANDING 约束，这是它存在的意义。
- **§七 Schedule CRUD 行锁**：`Update`/`SetEnabled` 全程在 `GetScheduleRowForUpdate` 事务内（锁内重读→merge PATCH→validate→算 next_run_at→UPDATE）；软删行=ErrNoRows=ErrNotFound。锁序仍 schedules→deliveries。
- 新测试：`internal/app/authz_test.go`（3）+ `tests/integration/review2_fixes_test.go`（5，infra 重试/kill/pause/PATCH 竞态/pending cap）；测试结束要软删自己的 fixture schedule（flaky resolver 会让到期槽每 tick 报错）。
- **教训**：集成测试 fixture 的 applications.slug 是 UNIQUE，重跑套件会撞——seed 数据带 UnixNano 后缀（gateFixture 模式）。
- 本机无 gcc，`-race` 跑不了（CI Ubuntu 上跑）；本地用单测+集成覆盖。

## 三轮复审整改（2026-09-15 完成，最新）

报告：`docs/potal 第三轮复审整改变更报告（executionDenied-P0·PreSubmitGate·Defer保留优先级·run.started时点·fail-closed）.md`。修复二轮引入的 `executionDenied(nil)` P0（nil=成功被判拒绝→Schedule 面全瘫）+ Gate 2/Defer/run.started/fail-closed。核心新文件：`internal/execution/defer.go`、`internal/execution/gate.go`；新查询 `DeferRunFenced`/`DeferScheduledOccurrence`；测试 `tests/integration/review3_fixes_test.go`（11 个，含 Barrier 竞态与真实链路正向测试）；构造器 `app.NewBindingResolver`/`app.NewSchedulableChecker` 供测试与接线用。

### 下轮待办（复审遗留）
SSE Hub 多路复用、Worker 阻塞 dispatcher、RunEvent 去 COUNT、client_request_id 幂等键、Sidebar keyset 分页、Conversation soft-delete、非终态 Run predicate 统一（waiting_input 等，启用 waiting/resume 前必须 P1）、Schedule Service 全 DB Clock、RateLimiter ctx/nil-Redis 边角。
