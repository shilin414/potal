# Creation Agent Studio — 项目长期记忆

## 当前状态（2026-09-15 第七轮复审整改后）

第七轮 97/A（P0=0，P1=1，P2=2）→ 报告 §二十四 批次一/二 **全部完成**：P1 SSE terminal replay 立即关闭、P2-1 duration metric post-commit、P2-2 前端同 chunk 终态停止，本机全绿（后端单测+集成 55.9s、前端 tsc/vitest 131/vite build）。报告预期三件套后该块达 98/A+。
基线 dev `fc255f5`（第六轮功能提交 `ecbc83f`）。变更报告按主题命名放 `docs/`。

**执行内核（Ownership/Claim/Reaper/Finalize/ProviderSlot）已定型。**
第七轮报告明确：三件套完成后，**停止**对 Gate1/Gate2/Provider submit boundary/Lease fencing/Retry·Defer/Schedule admission/legacy interrupted migration 继续微调；下轮转向新架构风险（client_request_id 幂等、SSE Hub、长历史 event 分页、Worker dispatcher、Conversation 生命周期、前端长对话性能）。

**第三批（未做）**：`onTransportEnd` 契约清理（实际不可达，报告建议暂时删除死接口）、`runs.next_event_sequence` 分配器。

---

## 硬性约定（违反会复发 P0/事故）

### 终态与事件语义
- `runs.status='interrupted'` ≠ `run_events.event_type='run.interrupted'`，**永不合并处理**。status 是终态别名（≈failed）；**event 是 overloaded 的**：`{reason}` 无 status = retry 标记，`{status}` 才是真终态。
- 谓词：`IsSettled()` = canonical ∪ {interrupted}；`IsTerminal()` 只认 `{cancelled,succeeded,failed}`；SQL 统一 `status NOT IN ('cancelled','succeeded','failed','interrupted')`。
- **迁移 0018/0019/0020 禁止修改**（已执行）。0020 前向修复 0018 的 over-conversion，扫描集合不含 interrupted。新增 migration 的 `MAX(sequence)` 必须 `COALESCE(MAX(...),0)+1`。
- **terminal event = 硬边界**：SSE replay 遇到立即 `break` 并 `return`（**不看 `run.Status` 快照**——快照是开流前读的，正常终态竞争即可 stale）；前端同一 chunk 内 terminal 后停止 dispatch。synthetic terminal 必须按状态映射（succeeded→completed / cancelled→cancelled / failed|interrupted→failed），未知或非 settled **不合成**，绝不默认 `run.completed`。

### 事务与时钟
- **metrics / Redis fan-out / delivery hook 一律 post-commit，绝不能在正确性事务内做观测读**：观测读失败不得回滚终态。`studio_run_duration` 两端都取 DB 时钟（`dbClockDuration`，NULL/非单调丢弃），在 commit 后重读行（`GetRunTimestamps`），用 `NewCleanupContext`（detached + 有界）+ 只告警。
- **Clock Authority 无兜底**：`dbNow/dbNowTx` 出错就中止/跳 tick，绝不回落本机时钟；Scheduler/TriggerNow 一律 `DBNow`。
- `MarkRunStartedOwned` 返回 **DB 时间**，worker 必须 `claimed.Run.StartedAt = &t`；started_at 在 Gate1 allow 后才写（`CASClaimRun` 不写）；`finalize.go` 先判 `StartedAt != nil` 再 `IsZero()`。
- **detached 写一律 `execution.NewCleanupContext(parent)`**（继承 values、丢弃父 deadline）；裸 `context.Background()` 与 `WithoutCancel` 都不对。
- attempt 语义：claim 不 +attempt，唯一消耗点 `BeginProviderAttemptOwned`；消息落库必须同事务 `TouchConversationUpdated`。

### 准入、授权与 Gate
- 执行准入唯一门：`catalog.AuthorizeExecution`（authz.go）；普通用户错误一律 404；停用 app 对任何人不可执行。
- **授权分类双保险**：调用点先显式判 `err == nil` 成功路径再 `executionDenied(err)`；`executionDenied(nil)` 恒 false；正向 Happy-Path 必须有真实链路测试。
- Gate 双检查点 level-triggered：Gate1（worker claim 后）+ Gate2（aily executor 在 ChatsL.Acquire 后、BeginProviderAttempt 前，`beginSubmit` 是唯一入口）。kill=cancel；pause/infra/未知 action=**Defer fail-closed**（用 `preSubmitStop` 包装避免被 classify 成 Provider 故障）；run.started 在 Gate allow 后；Execution Generation 不引入。
- Provider 门禁按 `provider_key` fail-closed（`provider_id` 常为 NULL）；binding 创建/更新写 `b.ProviderID`；授权与 binding 解析必须在 Admission Lock 之后（`admitOne` 锁内重读）。
- `bindingFromExecutionAuthRow` 与 `bindingFromRow` 输出形状必须一致；`Binding.Snapshot()` 无条件写 timeout/config/capabilities。
- Streaming 必须同步 Open（同 goroutine 发 POST），禁止在 goroutine 内发 POST。

### 并发与锁
- 一 conversation 同时只允许一个非终态 Run（`CreateRunInTx` 行锁 + Count → 409，串行非排队）。
- Schedule admission 统一到 schedules 行锁（`GetScheduleRowForUpdate` 返回整行，锁内重读→判定→创建同事务，helper 全 *InTx）；`MaxPendingManual` 超限 → 429。
- 锁序恒 `users → conversations`；`Server.RunAdmission` 是长生命周期 RateLimiter，禁止每请求 new；outstanding 上限在 `CreateRunAdmitted`。
- Conversation 硬删守卫：活跃 Run→409；有 scheduled Run/delivery→拒绝（软删未做）。
- 队列三条 Stream `queue:<provider>:interactive|retry|scheduled` 7:1:2；delivery=At Least Once，`IdempotencyKey=DeliveryExecution.ID`。

### 前端
- store 异步写一律 functional setState（`await` 后不得用旧快照）；`activeRunId` compare-and-clear；拉取失败用 `null` 表示"未知不清空"。
- `run.cancelled` 是**独立终态**（`execution_disabled` = 硬取消），reducer 与 finalizeRun 都不得映射成 done；`run.deferred` 非终态，靠 `run.started` 清除。
- channel 发送必须可取消（`select { out <- ev / ctx.Done() }`）；测这类修复的假流必须无尽头。

---

## 数据库与 CI
- **MySQL 5.7 基线**（TiDB 已退出）：`192.168.211.26:20336`/`xiaoan`/`test_user`；DSN UTC（`loc=UTC`+`time_zone='+00:00'`）不能动；错误码 1213/1205 保留。当前 migration version = 20。
- CI 两 job：`check`（actionlint/gofmt/vet/build/unit/**race**）+ `integration`（mysql5.7+redis7，含二次迁移 no-op）。**本机无 gcc，`-race` 只能由 CI 兜**。
- 改 `db/queries/*.sql` 必须跑 `~/go/bin/sqlc.exe generate`（v1.30.0），确认无附带 churn。

## 运行/验证要点
- 集成测试开关 `STUDIO_TEST_DB=1` / `STUDIO_TEST_REDIS=1`；配置一律 `config.Load()`（`.env.local` 有密码）。delivery 是独立 worker（`--provider=feishu_delivery`）。
- 冒烟无泄漏判据：running=0/leases=0/slots=0/outbox(pending)=0/delivery(pending)=0/occurrence(pending)=0。
- **报告类「全库 COUNT 期望 0」校验会被 fixture 残留假红**：新 seed 一律自带 `t.Cleanup`，FK 顺序 events→leases→slots→outbox→run→conversation；圈定清理用 `provider LIKE 'itest%'`。
- 新增测试必须做**反证**（临时还原修复 → 确认测试 FAIL → 还原 → 全绿），否则测试无价值。
- 本机 Git Bash 常丢 coreutils：命令前 `export PATH="/usr/bin:/bin:/c/software/Git/cmd:$PATH"`。本机 git 的 `refs/remotes/<name>/<branch>` 写入有缺陷，push 后必须手工写 loose ref + 双写 packed-refs 并用 `git ls-remote` 三方核对。

## 历轮整改索引（细节见 docs/ 下同名报告）
- 五轮：finalize 竞态 / streaming 取消 / interrupted 契约 / DB 时钟 / 限流 ctx / started_at / cleanup 边界 → 93/A-
- 六轮：0018→0020 历史事件迁移修复 / SSE cancelled synthetic / DB-clock duration → 96/A（解除 BLOCKED）
- 七轮：SSE replay terminal 硬边界（P1）/ duration metric post-commit（P2-1）/ 前端同 chunk 终态停止（P2-2）→ 预期 98/A+
