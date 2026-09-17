# Creation Agent Studio — 项目长期记忆

> 只放「跨会话仍成立、违反会复发事故」的**跨切面**代码规则。
> - **本机环境 / 命令 / CI 映射 / flake / 操作红线** → 同目录 `PITFALLS.md`（动手前必读）
> - **冻结子系统（SSE Hub / Worker Dispatcher）的实现约束** → 同目录 `FROZEN.md`
> - 起 dev 环境与验证套路 → skill `cas-dev-verify`；机制与证据 → `docs/`

## 当前状态
- 仓库 `shilin414/potal`，分支 `dev`。执行内核（Ownership/Claim/Reaper/Finalize/ProviderSlot/Lease/Heartbeat/Gate）与第九轮 **FROZEN**，不得顺手改。
- 第十轮 Batch 4 / 4.1 / 4.1.1 / 4.1.2 → **SSE Hub 永久 FROZEN**（4.1.2 为 test-only）；Batch 5 + 5.1 → **Worker Dispatcher FINAL FROZEN**。
- **下一步：Batch 6 — 优先 Aily Workflow Runtime**（验证同 provider、不同 `runtime_type` 走不同 Executor）。
- migration 基线 = **24**（0021 run_requests / 0022 provider_submissions / 0023 next_event_sequence / 0024 容量索引）。Batch 4/5 系列**无 migration**。

## 改冻结子系统前先读
| 子系统 | 文档 |
|---|---|
| SSE Hub | `docs/potal 第十轮 Batch 4 / 4.1 / 4.1.1 整改变更报告*.md` + `FROZEN.md` |
| Worker Dispatcher | `docs/potal 第十轮 Batch 5 Worker Dispatcher 整改变更报告.md` + `FROZEN.md` |
| 历轮 | `docs/` 按主题命名（含 TiDB→MySQL 5.7 切换报告） |

## 核心硬性约定（违反会复发 P0/事故）
- **幂等**：`client_request_id` 解析早于授权/限流/附件校验；身份表 `run_requests`，`request_hash = SHA-256(归一化 payload)`；同 key 不同 hash → 409；resolver infra error → 5xx。
- **Provider 提交状态机**：`sending|accepted|rejected|unknown`；**只有 `rejected` 可重发**；`sending`/`unknown` → park `waiting_external`，**绝不 blind retry**；5xx/timeout = 未知，4xx = 明确拒绝。**提交边界 = POST 成功且拿到 external id**；200 无 id → `ErrServer`（Aily → unknown → waiting_external），绝不 failRun；park 后必须 return。
- **accepted identity 持久化是 canonical correctness**：`MarkSubmissionAccepted` 失败必须**停链**（不 poll/reconcile/finalize/重发），只做本地 DB 短重试（50/100/200ms）；sentinel `ErrProviderAcceptancePersistence`。
- **每次 submission 写都是 canonical write**：`MarkSubmissionStateOwned` 事务内 `verifyActiveOwnershipTx` + SQL CAS；四条 query 互斥；「记录结果」与「武装重发」是两条语句。
- **Provider 容量**：有效容量 = `DISTINCT(live slots UNION non-settled sending/unknown/accepted)`，**必须 UNION 不能相加**；排除 `rejected` 与已 settled run；admission **必须 exclude self**。remote leg 必须 active-run 驱动：`runs(active) STRAIGHT_JOIN provider_submissions`，状态过滤写**显式 `IN`**；**`STRAIGHT_JOIN` 是承重的**；不 FORCE INDEX。
- **SSE 协议**：durable 写 `id:<seq>`，transient(seq 0) 绝不写 id；优先级 `query after > Last-Event-ID > 0`；replay 遇 terminal 立即 break；WriteHeader 后必须 Flush；`content.chunk` 只写增量 text+offset；`stream_protocol` 与 `after` 正交、每条连接都发，返回**协商值**（`>=2 → 2`，缺失/乱码/非正/溢出 → 1），**绝不回显**。**发布合同 Backend first / Frontend second。**
- **前端按 UTF-8 字节 offset 对账**：transient delta 与 durable chunk 共用同一坐标、**都带 absolute end offset**；reducer 三分支（drop / 补 suffix / append+跳计数器）；中文 3 字节、emoji 4 字节，**绝不用 `string.length`**；offset missing → legacy append。
- **终态语义**：`interrupted` status ≠ `run.interrupted` event；`IsTerminal()` 只认 `{cancelled,succeeded,failed}`；SQL `status NOT IN (四个)`；migration 新增 `MAX(sequence)` 必须 `COALESCE(MAX(...),0)+1`。
- **时钟/事务**：Clock Authority 无兜底；续约类 UPDATE 必须单调写（`GREATEST(CURRENT_TIMESTAMP(3), DATE_ADD(...,1000 MICROSECOND))`）；MySQL UPDATE 返回 changed rows；merged heartbeat `HeartbeatOwnedWithSlot` BOTH OR NEITHER，Renew 恒 XX-only；`SET timestamp=<sec>` + `MaxOpenConns(1)` 可钉时钟；metrics/Redis fan-out post-commit；attempt 唯一消耗点 `BeginProviderAttemptOwned`；消息落库同事务 `TouchConversationUpdated`。
- **准入/Gate**：唯一门 `catalog.AuthorizeExecution`；普通用户错误一律 404；先判 `err == nil` 再 `executionDenied(err)`；Gate 双检查点 level-triggered；kill = cancel，pause/infra/未知 = Defer fail-closed；Provider 门禁按 `provider_key` fail-closed；Streaming 必须同步 Open。
- **并发**：一 conversation 一个非终态 Run（409）；锁序 `users → conversations`；hard-delete cascade 必须显式带 `run_requests`/`provider_submissions`；`waiting_external` 有界（`ExpireParkedExternalRuns` 用 DB 时钟 − grace）。
- **sequence O(1)**：`runs.next_event_sequence` 锁内 `SELECT FOR UPDATE` → `UPDATE x+1`；**绝不用 `COUNT(*)+1`**；读取一律 LIMIT。
- **前端 store**：异步写用 functional setState；`activeRunId` compare-and-clear；拉取失败 `null` = 未知、不清空；`run.cancelled` 独立终态不得映射 done；`run.deferred` 靠 `run.started` 清除。
- **antd 表单取值（P0 事故）**：拼 payload 一律 `form.getFieldsValue(true)`；**绝不用 `validateFields()` / `getFieldsValue()` 的返回值**（只含已注册 Form.Item 的路径，其他字段被**静默丢弃**）。回归测试 `frontend/src/components/Schedules/__tests__/scheduleEditorPayload.test.tsx`。

## 本机操作红线（完整版 + 命令见 `PITFALLS.md`）
- **同一文件绝不可在一条消息里发两个 Edit**：并行写同文件 = 后写覆盖前写、静默丢失，且**仍能编译通过**。串行改 + grep 复核那一行。
- **反证驱动绝不可与其它 `go test` 并发**（脚本改真实源码）。
- **不要为对照基线 `git checkout <sha>`**（本机会被 SIGTERM 打断）；用 `git checkout <sha> -- <路径>`。
- **同一工作区可能有并发会话**：开工前 `git status` + 看关键文件 mtime；提交前若混着别人的改动**先问用户**，别 `git add -A`。

## 历轮索引
十轮 **4**（单 upstream / 有界 cache / 协议隔离 / register-before-replay / 慢客户端隔离 / terminal 硬边界）→ **4.1**（live gap 修复 / cache 字节硬上界 / 指标代际围栏 / 锁序）→ **4.1.1**（constructor inert / 初始 idle timer 竞态 / canonical 连续性 fail-closed）→ **4.1.2**（test-only）→ **5 / 5.1**（Worker Dispatcher，FROZEN）。
九轮及以前：五轮 93/A- → 六轮 98/A+ → 八轮 P1 关闭 → 九轮（幂等 0021 / 提交状态机 0022 / O(1) sequence 0023 / Streaming Range / 有效容量 + 协议协商，FROZEN）。
