# Creation Agent Studio — 项目长期记忆

## 当前状态
- 仓库 `shilin414/potal`，分支 `dev`。第九轮批次一–三 + 补丁 3.1 + **补丁 3.2 已完成**（Streaming Range / Provider Identity Closure / Idempotency replay-before-error / OpenAPI 清理，报告见 `docs/potal 第九轮补丁3.2整改变更报告（…）.md`），批次一–三可冻结。批次四–七（SSE Hub / Worker Dispatcher / Conversation lifecycle / message keyset 分页 / 前端长对话）开放，**下一步：批次四 SSE Hub**。
- 执行内核（Ownership/Claim/Reaper/Finalize/ProviderSlot/Lease/Heartbeat/Gate）**冻结**，3.2 未触碰。
- migration 基线 = **23**（0021 run_requests / 0022 provider_submissions / 0023 next_event_sequence）。
- **3.2 新不变量**：①transient delta 与 durable chunk 共享同一 UTF-8 字节 absolute end offset（coalescer `totalReceived`；前端 `applyIncrementalRange` 双事件共用，无 offset legacy delta 走 append）；②提交边界 = POST 成功**且拿到 external id**（StartChat 200 无 id/坏 body → ErrServer → park，background 与 streaming 一致）；③`persistProviderAcceptance` 失败 = sentinel `ErrProviderAcceptancePersistence`，classifyError 原样上抛（不 poll/finalize/重发），本地重试 50/100/200ms，session bind 恒 best-effort；④`tryServeIdempotentReplay` 统一挂 authorize/admitUserRun/conversation/attachment 四个 post-miss 出口（found→200 / KeyReused→409 / infra err→500 / miss→原错误）。

## 核心硬性约定（违反会复发 P0/事故）
- **幂等**：`client_request_id` 解析早于授权/限流/附件校验；身份表 `run_requests`，`request_hash=SHA-256(归一化 payload)`。同 key 不同 hash → 409；resolver infra error → 5xx，绝不伪装 429。
- **Provider 提交状态机**：`sending|accepted|rejected|unknown`；只有 `rejected` 允许重发（同 submission_no/key，key 不含 attempt）。`sending`/`unknown` → `ErrProviderSubmitUnknown` → park `waiting_external`，绝不 blind retry。5xx/timeout=未知，4xx=明确拒绝。**提交边界 = POST 成功，不是拿到 chat id**：已提交但无 external id 一律 unknown+waiting_external，绝不 failRun；park 后必须 return。StartChat 200 但无 chat id / JSON 坏 → `ErrServer` APIError（Aily=IdempotencyNone → unknown → waiting_external）。
- **accepted identity 持久化是 canonical correctness**：`MarkSubmissionAccepted` 失败必须停止执行链（不 poll/reconcile/finalize/重发），只做本地 DB 短重试（50/100/200ms）；session bind 是 best-effort。sentinel：`ErrProviderAcceptancePersistence`，classifyError 直接上抛。
- **每次 submission 写都是 canonical write**：`MarkSubmissionStateOwned` 事务内 `verifyActiveOwnershipTx` + SQL CAS；四条 query 互斥（结果只从 sending 来；重发只从 rejected/unknown+native；accepted 只从 sending/unknown 且单调）。「记录结果」与「武装重发」是两条语句。native 能力由 executor 从 `catalog.SubmitIdempotencyOf(adapter)` 现场推导传进 `BeginProviderSubmission*`。
- **前端按 UTF-8 字节 offset 对账**：transient delta 与 durable chunk 共用同一套 byte coordinate system，**两者都带 absolute end offset**；reducer 三分支：end<=rendered→drop / start<=rendered<end→补 suffix / gap→append+跳计数器。中文 3 字节、emoji 4 字节，**绝不用 `string.length`**。offset missing → legacy append 兼容旧事件。gateway 反向顺序（chunk 先于 buffered delta）天然存在，必须用 offset 去重。
- **SSE**：durable 写 `id:<seq>`，transient(seq 0) 绝不写 id；优先级 `query after > Last-Event-ID > 0`；replay 遇 terminal 立即 break+return（不看 status 快照）；WriteHeader 后必须 Flush。`content.chunk` 只写增量 text+offset，不写累计 snapshot。
- **终态语义**：`interrupted` status ≠ `run.interrupted` event（`{reason}`无status=retry标记，`{status}`才是终态）。`IsTerminal()` 只认 `{cancelled,succeeded,failed}`；SQL `status NOT IN (四个)`。migration 新增 `MAX(sequence)` 必须 `COALESCE(MAX(...),0)+1`。
- **时钟/事务**：Clock Authority 无兜底（dbNow 失败即中止）。续约类 UPDATE 必须单调写（`GREATEST(CURRENT_TIMESTAMP(3), DATE_ADD(...,1000 MICROSECOND))`）；MySQL UPDATE 返回 changed rows 不是 matched。merged heartbeat `HeartbeatOwnedWithSlot` BOTH OR NEITHER，Renew 恒 XX-only。`SET timestamp=<sec>`+MaxOpenConns(1) 可钉时钟；同配方可注入真实 1205。metrics/Redis fan-out post-commit。attempt 唯一消耗点 `BeginProviderAttemptOwned`；消息落库同事务 `TouchConversationUpdated`。
- **准入/Gate**：唯一门 `catalog.AuthorizeExecution`；普通用户错误 404；先判 `err==nil` 再 `executionDenied(err)`。Gate 双检查点 level-triggered（Gate1 claim 后 + Gate2 `beginSubmit`）；kill=cancel，pause/infra/未知=Defer fail-closed。Provider 门禁按 `provider_key` fail-closed。Streaming 必须同步 Open。
- **并发**：一 conversation 一个非终态 Run（409）；锁序 `users→conversations`；hard-delete cascade 必须显式带 `run_requests`/`provider_submissions`（无 FK 级联）。`waiting_external` 有界：`ExpireParkedExternalRuns` 用 DB 时钟−grace 收敛。
- **sequence O(1)**：`runs.next_event_sequence` 锁内 SELECT FOR UPDATE→UPDATE x+1；绝不用 `COUNT(*)+1`；事件读取一律 LIMIT。
- **前端**：store 异步写 functional setState；`activeRunId` compare-and-clear；拉取失败 null=未知不清空；`run.cancelled` 独立终态（execution_disabled=硬取消）不得映射 done；`run.deferred` 靠 `run.started` 清除；channel 发送可取消。

## 工具与踩坑
- **前端 CI**：`npx tsc --noEmit` / `npx vitest run` / `npx vite build`（不跑 eslint）。后端 CI：gofmt/vet/build/test/**race** + integration（mysql5.7+redis7）。本机无 gcc，-race 由 CI 兜。
- **反证测试必须做**（还原修复→FAIL→还原→PASS），会暴露假测试。脚本必须 `trap cleanup EXIT` 还原 `.orig`；跑完 `grep FALSIFICATION` + `find -name '*.orig'` 双查。改代码后同步更新旧脚本锚点。
- **MySQL 5.7** `192.168.211.26:20336/xiaoan`；DSN UTC 不动；错误码 1213/1205 保留。共享 dev 库测试前先清 orphan 残留。改 `db/queries/*.sql` 跑 `~/go/bin/sqlc.exe generate`。集成开关 `STUDIO_TEST_DB=1`/`STUDIO_TEST_REDIS=1`。全库 COUNT=0 会被 fixture 假红，清理圈定 `provider LIKE 'itest%'`。
- **本机环境**：Git Bash 常丢 coreutils，先 `export PATH="/usr/bin:/bin:/c/software/Git/cmd:$PATH"`。本机 git `refs/remotes/<name>/<branch>` 写入有缺陷：push 后必须 `mkdir -p .git/refs/remotes/origin` + 写 loose ref + 双写 packed-refs，并用 `git ls-remote` 核对；看到 ahead/gone 先 ls-remote 对比别急着重推。无 `gh`，查 CI 用匿名 GitHub API。
- bash 嵌套 heredoc 会被内层定界符截断，用不同定界符或 Write 工具写脚本。断言时间戳未变用 `CAST(col AS CHAR)` 逐字符串比较。

## 历轮索引（细节见 docs/）
五轮 93/A- → 六轮 96/A → 七轮 98/A+ → 八轮 P1 关闭 → 九轮批次一–三+3.1（幂等 0021 / 提交状态机 0022 / cursor+O(1) sequence 0023 / 去 snapshot）→ **3.2 进行中**。
