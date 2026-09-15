# Creation Agent Studio — 项目长期记忆

## 当前状态
- 仓库 `shilin414/potal`，分支 `dev`。第九轮专项整改的**批次一–三**已完成（P0-1/P0-2 闭环 + P1-1/P1-3/P1-4 落地），本机全绿，提交 `b81ee8f`。CI 全绿：backend run **34983530376**（`check` 含 `-race` + `integration` 含二次迁移 no-op）、frontend run **34983530457**。报告见 `docs/potal 第九轮专项整改变更报告（…）.md`。
- 第八轮起执行内核（Ownership/Claim/Reaper/Finalize/ProviderSlot）**定型冻结**，不再微调 Gate/Lease/ProviderSlot/Retry·Defer/SSE terminal。
- migration version 基线 = **23**（0021 run_requests / 0022 provider_submissions / 0023 next_event_sequence）。0018/0019/0020 禁止修改。
- **仍开放（批次四–七）**：SSE Hub 多连接、Worker Dispatcher（poller→dispatcher + claim 后即 ACK）、Conversation lifecycle（generation / 异步 purge）、message keyset 分页 + sidebar 冗余字段、前端长对话（active turn 隔离 / 虚拟列表 / rAF 批处理 / smart auto-scroll）。

### 第九轮新增硬性约定
- **两个 P0 的边界语义**：`POST /v2/runs` 带 `client_request_id` 时**幂等解析必须早于授权/限流/附件校验**（首次请求已消费过这些检查，否则重放会被误报 400/429）；幂等身份用 `run_requests`（lazy conversation 下 `runs` 上放不下），`request_hash = SHA-256(归一化 payload)`。
- **Provider 提交状态机**：`sending|accepted|rejected|unknown`；**只有 `rejected` 允许重发**（同 payload 复用同一 submission_no/key，key 不含 attempt）。`sending`/`unknown` 一律 `ErrProviderSubmitUnknown` → **park 到 `waiting_external`**，绝不 blind retry。5xx 与 timeout 算"未知"（500 可能是已受理后才抛），4xx/401/403/429 算"明确拒绝"。
- **`IdempotencyAware` 是可选接口 + fail-closed**：未实现者按最弱类处理；报告 §6 的"可按 request key 查询"类**故意不声明**（本库无该 client，声明只会加不可测分支）。
- **`waiting_external` 必须有界**：它是非 settled，会占 conversation（否则永久 409）与配额，`ExpireParkedExternalRuns` 用 **DB 时钟 − grace** 收敛为 `failed/provider_submit_unknown`。读时钟失败跳过本轮，不回落本机时钟。
- **sequence 分配 O(1)**：`runs.next_event_sequence`，锁内 `SELECT FOR UPDATE` → `UPDATE x+1` → `INSERT`；**绝不再用 `COUNT(*)+1`**。事件读取一律带 LIMIT（200/1000），`has_more` 由"页满"推导而非 COUNT。
- **SSE 帧**：durable 写 `id: <seq>`，**transient(seq 0) 绝不写 id**（否则浏览器 Last-Event-ID 归零→全量重放）；优先级 `query after > Last-Event-ID > 0`；网关逐页 replay；`WriteHeader` 后必须 `Flush`（否则无事件的流不给响应头）。
- **`content.chunk` 只写增量** `text`+`offset`，不再写累计 `snapshot`（曾使事件数据量随回答长度平方增长）。前端 reducer 保留 snapshot 分支兼容旧事件。

## 工具与踩坑补充
- **前端**：`frontend/` 用 `npx tsc --noEmit` / `npx vitest run` / `npx vite build`（CI 跑这三个，不跑 eslint）；`npx` 在 PATH 可见。仓库既有 6 个 eslint error 属历史遗留，与 CI 无关。
- **反证脚本化**：`backend-go/scripts/falsify_review9.sh`、`frontend/scripts/falsify_review9.sh` —— 逐个还原修复→确认 FAIL→还原→确认 PASS。**反证会暴露"断言正确但从未被执行"的假测试**（本轮抓到 2 例），必须做。

## 硬性约定（违反会复发 P0/事故）

### 终态与事件语义
- `runs.status='interrupted'` ≠ `run_events.event_type='run.interrupted'`，**永不合并**。status 是终态别名（≈failed）；**event 是 overloaded 的**：`{reason}` 无 status = retry 标记，`{status}` 才是真终态。
- 谓词：`IsSettled()` = canonical ∪ {interrupted}；`IsTerminal()` 只认 `{cancelled,succeeded,failed}`；SQL 统一 `status NOT IN ('cancelled','succeeded','failed','interrupted')`。
- 新增 migration 的 `MAX(sequence)` 必须 `COALESCE(MAX(...),0)+1`。
- **terminal event = 硬边界**：SSE replay 遇到立即 `break`+`return`（**不看 `run.Status` 快照**，快照开流前读的，终态竞争即 stale）；前端同一 chunk 内 terminal 后停止 dispatch。synthetic terminal 按状态映射（succeeded→completed / cancelled→cancelled / failed|interrupted→failed），未知或非 settled **不合成**，绝不默认 `run.completed`。

### 事务与时钟
- **续约类 UPDATE 必须单调写**（第七轮 CI 教训）：MySQL `UPDATE` 返回 **changed rows**，非 matched rows。续约与建行落在同一毫秒 → 0 changed rows → 被误读成 `ErrProviderSlotLost`。修法 `heartbeat_at = GREATEST(CURRENT_TIMESTAMP(3), DATE_ADD(heartbeat_at, INTERVAL 1000 MICROSECOND))`（sqlc 不认 `1 MILLISECOND`）。**不要**用 `ClientFoundRows=true`（破坏重复检测语义）。`heartbeat_at` 仅观测，fencing 只看 `expires_at`。
- **merged heartbeat 是 BOTH OR NEITHER**（第八轮 P1，`HeartbeatOwnedWithSlot`）：进入 provider execution 后只有 `(true,true,nil)` / `(false,false,nil)` / `ErrProviderSlotLost` / 普通 error 四个出口，**任何 slot 失败都连同 lease 一起 ROLLBACK**。Renew 恒 **XX-only，绝不 recreate slot**。确认丢失后只 local cancel + 幂等 Release，**不 requeue**。
- **多返回值 flag 判定必须带齐维度**：谓词写 `confirmedProviderSlotLoss(leaseOK, slotOK, err)`，文案抽成可测函数 + 表驱动测试。
- **`SET timestamp = <second>` 可钉死 session 时钟**，配 `MaxOpenConns(1)` 把「恰好同一毫秒」变确定性条件。同配方可注入**真实 InnoDB 1205**：`SET SESSION innodb_lock_wait_timeout = 1` + 另一连接 `SELECT … FOR UPDATE` 持锁。两种注入共用 `newSessionTunedService(t, sessionStmt, args...)`。
- **metrics / Redis fan-out / delivery hook 一律 post-commit**，观测读失败不得回滚终态。`studio_run_duration` 两端取 DB 时钟（`dbClockDuration`），commit 后重读（`GetRunTimestamps`），用 `NewCleanupContext` + 只告警。
- **Clock Authority 无兜底**：`dbNow/dbNowTx` 出错即中止/跳 tick，绝不回落本机时钟。
- `MarkRunStartedOwned` 返回 **DB 时间**；started_at 在 Gate1 allow 后才写（`CASClaimRun` 不写）；`finalize.go` 先判 `StartedAt != nil`。
- **detached 写一律 `execution.NewCleanupContext(parent)`**（继承 values、丢 deadline）。
- attempt 语义：claim 不 +attempt，唯一消耗点 `BeginProviderAttemptOwned`；消息落库必须同事务 `TouchConversationUpdated`。

### 准入、授权与 Gate
- 执行准入唯一门 `catalog.AuthorizeExecution`；普通用户错误一律 404；停用 app 对任何人不可执行。
- **授权分类双保险**：调用点先判 `err == nil` 成功路径再 `executionDenied(err)`；`executionDenied(nil)` 恒 false。
- Gate 双检查点 level-triggered：Gate1（worker claim 后）+ Gate2（aily executor 在 ChatsL.Acquire 后、BeginProviderAttempt 前，`beginSubmit` 唯一入口）。kill=cancel；pause/infra/未知 action=**Defer fail-closed**（`preSubmitStop` 包装）。run.started 在 Gate allow 后。
- Provider 门禁按 `provider_key` fail-closed（`provider_id` 常为 NULL）；授权与 binding 解析必须在 Admission Lock 之后。
- `bindingFromExecutionAuthRow` 与 `bindingFromRow` 输出形状必须一致；`Binding.Snapshot()` 无条件写 timeout/config/capabilities。
- Streaming 必须同步 Open（同 goroutine 发 POST）。

### 并发与锁
- 一 conversation 同时只允许一个非终态 Run（`CreateRunInTx` 行锁 + Count → 409）。
- Schedule admission 统一到 schedules 行锁（`GetScheduleRowForUpdate` 锁内重读→判定→创建同事务）；`MaxPendingManual` 超限 → 429。
- 锁序恒 `users → conversations`；`Server.RunAdmission` 长生命周期，禁止每请求 new。
- Conversation 硬删守卫：活跃 Run→409；有 scheduled Run/delivery→拒绝。
- 队列三条 Stream `queue:<provider>:interactive|retry|scheduled` 7:1:2；delivery=At Least Once。

### 前端
- store 异步写一律 functional setState；`activeRunId` compare-and-clear；拉取失败用 `null` 表示「未知不清空」。
- `run.cancelled` 是**独立终态**（`execution_disabled` = 硬取消），不得映射成 done；`run.deferred` 非终态，靠 `run.started` 清除。
- channel 发送必须可取消（`select { out <- ev / ctx.Done() }`）。

## 数据库与 CI
- **MySQL 5.7**：`192.168.211.26:20336`/`xiaoan`/`test_user`；DSN UTC（`loc=UTC`+`time_zone='+00:00'`）不能动；错误码 1213/1205 保留。
- CI 两 job：`check`（actionlint/gofmt/vet/build/unit/**race**）+ `integration`（mysql5.7+redis7，含二次迁移 no-op）。**本机无 gcc，`-race` 只能由 CI 兜**。
- 改 `db/queries/*.sql` 必须跑 `~/go/bin/sqlc.exe generate`（v1.30.0）。只改 SQL 注释也会有 diff（注释进 Go doc），属预期。
- 本机**无 `gh`**：查 CI 用匿名 GitHub API（`api.github.com/repos/shilin414/potal/actions/runs?branch=dev`）。**MSYS `/tmp` 与原生 python/node 不互通** → 用 `curl … | python -c "json.load(sys.stdin)"` 管道。

## 运行/验证要点
- 集成测试开关 `STUDIO_TEST_DB=1` / `STUDIO_TEST_REDIS=1`；配置一律 `config.Load()`（`.env.local` 有密码）。delivery 是独立 worker（`--provider=feishu_delivery`）。
- 冒烟无泄漏判据：running=0/leases=0/slots=0/outbox(pending)=0/delivery(pending)=0/occurrence(pending)=0。
- **「全库 COUNT 期望 0」会被 fixture 残留假红**：新 seed 自带 `t.Cleanup`，FK 顺序 events→leases→slots→outbox→run→conversation；圈定清理用 `provider LIKE 'itest%'`。需要时插临时探针 `tests/integration/zz_leakprobe_test.go` 量真实残留，**跑完立即删**。
- 新增测试必须做**反证**（临时还原修复 → 确认 FAIL → 还原 → 全绿）。断言时间戳「未变」用 `CAST(col AS CHAR)` 逐字符串比较。
- 本机 Git Bash 常丢 coreutils：命令前 `export PATH="/usr/bin:/bin:/c/software/Git/cmd:$PATH"`。本机 git 的 `refs/remotes/<name>/<branch>` 写入有缺陷，push 后必须手工写 loose ref + 双写 packed-refs 并用 `git ls-remote` 三方核对。

## 历轮整改索引（细节见 docs/ 下同名报告）
- 五轮：finalize 竞态 / streaming 取消 / interrupted 契约 / DB 时钟 / 限流 ctx / started_at / cleanup 边界 → 93/A-
- 六轮：0018→0020 历史事件迁移修复 / SSE cancelled synthetic / DB-clock duration → 96/A
- 七轮：SSE replay terminal 硬边界 / duration metric post-commit / 前端同 chunk 终态停止 → 98/A+
- 八轮：merged heartbeat BOTH OR NEITHER / Provider Slot 确认丢失后 self-fence → P1 关闭
- 九轮（专项）：批次一–三完成 —— 请求幂等（0021 `run_requests`）/ Provider 提交状态机（0022 `provider_submissions` + `waiting_external`）/ Event Cursor·Keyset 分页·O(1) sequence（0023）/ 去 cumulative snapshot；批次四–七 仍开放。见 `docs/potal 第九轮专项整改变更报告（…）.md`。
