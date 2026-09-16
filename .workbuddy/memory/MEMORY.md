# Creation Agent Studio — 项目长期记忆

> 只留「跨会话仍成立、违反会复发事故」的规则；为什么见 `docs/`。

## 当前状态
- 仓库 `shilin414/potal`，分支 `dev`。第九轮（含 3.1/3.2/3.3/3.3.1）与**第十轮 Batch 4 + 4.1（SSE Hub Correctness Closure）已冻结**；报告 `docs/potal 第十轮 Batch 4*` / `Batch 4.1*`。**下一步：Batch 5 — Worker Dispatcher**。
- 执行内核（Ownership/Claim/Reaper/Finalize/ProviderSlot/Lease/Heartbeat/Gate）**冻结**，后续批次不得顺手改。
- migration 基线 = **24**（0021 run_requests / 0022 provider_submissions / 0023 next_event_sequence / 0024 容量索引）。Batch 4 / 4.1 **无 migration**。

## SSE Gateway / Hub 不变量（Batch 4 + 4.1，已冻结）
- **单 Run 单 upstream**：`GetOrCreate` 同锁内 lookup+insert；`Gateway` **不持有 `*redisx.Client`**（禁回退到一连接一订阅）。`ready` 在「确认或失败」后关闭；请求侧 `WaitReady` **之后必须再问 `Serving()`**；SUBSCRIBE 失败**先 `failUpstream()` 再 `closeReady()`**。
- **两把锁禁止嵌套**：`GetOrCreate` 是 loop —— `manager.mu` 内只 lookup/insert，**释放后**才调 `hub.Serving()`，不可服务则 `removeIfSame` 进下一轮。`newRunHub` arm idle timer 不取 `hub.mu`（尚未进注册表）。
- **cache 指标必须代际围栏**：`reportCache` 收 hub 并在锁内 `m.hubs[runID] != hub` 校验，否则旧代际写回幽灵指标（`hubs_active=0` 却 `cache_bytes>0`），已结束的 run 永不自愈。
- **register-before-replay 是承重的**：`SUBSCRIBE 确认 → 注册 Subscriber → cache/DB replay → drain 队列`；replay 期间 live 帧只进队列、绝不写 socket。
- **durable live 顺序**：`lastDelivered` = **已连续写出的最高 durable sequence**（不是"最高见过"）。live `> last+1` = FORWARD GAP → **拒绝该帧**，先 `repairDurableGap` 补 `[last+1..seq]` 再继续（`through` 已 publish ⇒ 事务已 COMMIT ⇒ 更低 sequence 已提交，日志必有）。补齐失败 **fail closed**（一帧不写、结束连接、客户端带连续游标重连）；补齐遇 terminal 仍硬边界。补齐帧计入 `studio_sse_replay_events_total`，另有 `studio_sse_live_gap_repair_total{result=repaired|failed}`。
- **durable cache 必须连续**：gap 一律**丢弃旧段**（fail-closed）；只收 `sequence>0`；count AND bytes 双限；replay 恒做一次 DB tail reconciliation；`RememberDurable` 预热但**不 fan-out**。
- **cache 字节上界是硬上界**：`ApproxBytes > CacheMaxBytes` 的帧**不缓存**（不截断、不豁免；terminal 可能带整个答案）。故 `evictLocked` 允许清空 ring，空 ring = miss，`Bytes() <= maxBytes` 恒成立。`lastObservedSeq`（曾喂到）与 `lastSeq`（真正 retained）**必须分开**。
- **协议属于 Subscriber**：Hub 无 protocol 字段；只过滤 `content.delta`（sequence 0 是传输标记，其他 transient 控制帧仍须到 legacy）。**扇出恒非阻塞**：`Offer` 先字节预留再 `select/default`，超限只关该 subscriber；**字节预算在消费者取出时释放**。
- **生命周期**：terminal 是硬边界（`dispatch` 先置 `terminal` 再扇出）；最后 subscriber 离开后 arm `IdleTTL`(30s)，回收必须 `removeIfSame` 指针比较；upstream 失败**不做 Hub 内重连**（unhealthy → 关 subscriber → 移出注册表）；Hub runtime context **不继承启动 context**；`App.Close()` = **Hub → Redis → DB**。
- **指标禁止 run/user/conversation label**；`reason`/`stage`/`result` 是封闭枚举，客户端正常断开不计入 dropped。

## 核心硬性约定（违反会复发 P0/事故）
- **幂等**：`client_request_id` 解析早于授权/限流/附件校验；身份表 `run_requests`，`request_hash=SHA-256(归一化 payload)`；同 key 不同 hash → 409；resolver infra error → 5xx。
- **Provider 提交状态机**：`sending|accepted|rejected|unknown`；只有 `rejected` 可重发（key 不含 attempt）；`sending`/`unknown` → park `waiting_external`，**绝不 blind retry**；5xx/timeout=未知，4xx=明确拒绝。**提交边界 = POST 成功且拿到 external id**；200 无 id → `ErrServer`（Aily → unknown → waiting_external），绝不 failRun；park 后必须 return。
- **accepted identity 持久化是 canonical correctness**：`MarkSubmissionAccepted` 失败必须停止执行链（不 poll/reconcile/finalize/重发），只做本地 DB 短重试（50/100/200ms）；sentinel `ErrProviderAcceptancePersistence`。
- **每次 submission 写都是 canonical write**：`MarkSubmissionStateOwned` 事务内 `verifyActiveOwnershipTx` + SQL CAS；四条 query 互斥；「记录结果」与「武装重发」是两条语句。
- **Provider 容量**：有效容量 = `DISTINCT(live slots UNION non-settled sending/unknown/accepted)`，**必须 UNION 不能相加**；排除 `rejected` 与已 settled run；admission **必须 exclude self**。remote leg **必须 active-run 驱动**：`runs(active) STRAIGHT_JOIN provider_submissions`，状态过滤写**显式 `IN`**；**`STRAIGHT_JOIN` 是承重的**；不 FORCE INDEX；`awaitExternal` park 照旧删本地 slot。
- **SSE 协议（冻结）**：durable 写 `id:<seq>`，transient(seq 0) 绝不写 id；优先级 `query after > Last-Event-ID > 0`；replay 遇 terminal 立即 break；WriteHeader 后必须 Flush；`content.chunk` 只写增量 text+offset；`stream_protocol` 与 `after` 正交、每条连接都发，返回**协商值**（`>=2 → 2`，缺失/乱码/非正/溢出 → 1），**绝不回显**。**发布合同 Backend first / Frontend second。**
- **前端按 UTF-8 字节 offset 对账**：transient delta 与 durable chunk 共用同一坐标；**两者都带 absolute end offset**；reducer 三分支（drop/补 suffix/append+跳计数器）；中文 3 字节、emoji 4 字节，**绝不用 `string.length`**；offset missing → legacy append。
- **终态语义**：`interrupted` status ≠ `run.interrupted` event；`IsTerminal()` 只认 `{cancelled,succeeded,failed}`；SQL `status NOT IN (四个)`；migration 新增 `MAX(sequence)` 必须 `COALESCE(MAX(...),0)+1`。
- **时钟/事务**：Clock Authority 无兜底；续约类 UPDATE 必须单调写（`GREATEST(CURRENT_TIMESTAMP(3), DATE_ADD(...,1000 MICROSECOND))`）；MySQL UPDATE 返回 changed rows 不是 matched；merged heartbeat `HeartbeatOwnedWithSlot` BOTH OR NEITHER，Renew 恒 XX-only；`SET timestamp=<sec>`+MaxOpenConns(1) 可钉时钟；metrics/Redis fan-out post-commit；attempt 唯一消耗点 `BeginProviderAttemptOwned`；消息落库同事务 `TouchConversationUpdated`。
- **准入/Gate**：唯一门 `catalog.AuthorizeExecution`；普通用户错误 404；先判 `err==nil` 再 `executionDenied(err)`；Gate 双检查点 level-triggered；kill=cancel，pause/infra/未知=Defer fail-closed；Provider 门禁按 `provider_key` fail-closed；Streaming 必须同步 Open。
- **并发**：一 conversation 一个非终态 Run（409）；锁序 `users→conversations`；hard-delete cascade 必须显式带 `run_requests`/`provider_submissions`；`waiting_external` 有界（`ExpireParkedExternalRuns` 用 DB 时钟−grace）。
- **sequence O(1)**：`runs.next_event_sequence` 锁内 SELECT FOR UPDATE→UPDATE x+1；绝不用 `COUNT(*)+1`；读取一律 LIMIT。
- **前端**：store 异步写 functional setState；`activeRunId` compare-and-clear；拉取失败 null=未知不清空；`run.cancelled` 独立终态不得映射 done；`run.deferred` 靠 `run.started` 清除。
- **antd 表单取值（P0 事故）**：拼 payload 一律 `form.getFieldsValue(true)`；**绝不用 `validateFields()`/`getFieldsValue()` 的返回值**（只含已注册 Form.Item 的路径，其他字段被**静默丢弃**）。回归测试 `frontend/src/components/Schedules/__tests__/scheduleEditorPayload.test.tsx`。

## 工具与踩坑
- **⚠️ 同一文件绝不可并行发两个 Edit**：各基于同一旧快照写回，后写覆盖前写 = 静默丢失（实测三处装配被吃掉且**仍编译通过**）。串行改，改完 `grep` 复核。
- **⚠️ 手造 durable SSE 帧是契约负债**：4.1 起 gateway 把「日志拿不出该 sequence 的 durable 帧」当无法修复的缺口并结束连接。集成测试必须 `appendLiveEvent`（真实落库）或 `seedDurableEvent`（显式插行 + 推进 `next_event_sequence`）；liveness 探针用 **transient 帧**。
- **反证绝不可与其它 `go test` 并发**（脚本改真实源码）。驱动 `scripts/falsify_review9*.sh`、`falsify_review10.sh`(7/14)、`falsify_review10_patch41.sh`(4/8)；**改代码后必须同步更新旧脚本锚点**（失配会终止驱动）。跑完 `grep FALSIFICATION` + `find -name '*.orig'` 双查。
- **本机 `core.autocrlf=true`（系统级）与本仓库 LF 索引相冲**：git 重写文件后工作区变 CRLF，`gofmt -l` 全文件报警（索引仍 LF、CI 不受影响）。本仓库已局部 `core.autocrlf=false`；再见到就 python 把 `\r\n` 归一回 `\n`。
- **⚠️ 不要为对照基线 `git checkout <sha>`**：会被 SIGTERM 打断并留下半成品工作区（文件被删 + `.git/index.lock`，HEAD 未移动）。恢复：`rm .git/index.lock` → `git reset --hard HEAD` → 从仓库外备份恢复未提交改动。对照基线用 `git checkout <sha> -- <路径>`。
- **本机集成 flake**：`TestRetryRunAndOutboxShareRetryAt` / `TestReaperRequeueIsImmediatelyClaimableAndInSync` 要求两次独立写的 `available_at` 差 ≤50ms，实测 60–110ms（A/B 交替已证基线同样失败）。`TestTickAndRunNowConcurrent` 全量跑因库污染失败、隔离跑通过。
- **CI**：前端 `npx tsc --noEmit` / `npx vitest run` / `npx vite build`（不跑 eslint）；后端 gofmt/vet/build/test + **race**（execution/delivery/automation/**transport/sse**）+ integration（mysql5.7+redis7）。**本机 `CGO_ENABLED=0` 无 gcc，`-race` 只能由 CI 兜**。
- **MySQL 5.7** `192.168.211.26:20336/xiaoan`、Redis `192.168.211.239:6380`（`backend-go/.env.local`）；DSN UTC 不动。开关 `STUDIO_TEST_DB=1`/`STUDIO_TEST_REDIS=1`。全库 COUNT=0 会被 fixture 假红，清理圈定 `provider LIKE 'itest%'`。改 `db/queries/*.sql` 跑 `~/go/bin/sqlc.exe generate`。全量集成本机 ≈9 分钟。
- **本机环境**：Git Bash 常丢 coreutils，命令前加 `export PATH="/usr/bin:/bin:/c/software/Git/cmd:$PATH"`。git `refs/remotes/<name>/<branch>` 写入有缺陷：push 后必须 `mkdir -p` 建目录 + 写 loose ref + 双写 packed-refs，并用 `git ls-remote` 核对；看到 ahead/gone 先 ls-remote。无 `gh`，查 CI 用匿名 GitHub API。bash 嵌套 heredoc 会被截断（文本变异用 python）。断言时间戳未变用 `CAST(col AS CHAR)` 比较。
- **Redis pub/sub 不为未来订阅者缓冲**：集成测试证明"网关已订阅"必须**循环重发直到收到**；SSE 测试要区分"测 transient 帧本身"（须声明 `stream_protocol=2`）与"测 legacy 兼容"（不声明）。
- **集成测试 Gateway 装配**：`sse.Gateway` 已无 `Redis` 字段；用 `sse.NewHubManager(ctx, rdb, nil, opts)` + `t.Cleanup(hub.Close)`（`startHubSSEServer` / `openStreamAt` / `startHubSSEServerWithOptions`）。
- **本地起环境是 5 个进程**：api / stream / worker(`--provider=feishu_aily`) / **worker(`--provider=feishu_delivery`)** / vite。漏掉 delivery 时飞书收不到（`delivery_executions` 停在 `pending`）；`(cmd &)` 自 detach 活不下来，常驻必须 `run_in_background`。
- **`CASFinishDelivery` 双占位符**：`sent_at = IF(?='succeeded',…)` 第二个 `?` 生成成 `Column5`，调用方必须把 status 传两次。
- **sqlc 隐式改名会连带破调用点与反证锚点**：`ps.run_id <> ?` 改写成 `r.id <> ?` 会让参数名从 `RunID_2` 变 `ID`；要保名就写 `ps.run_id <> ?`。
- **EXPLAIN 断言只在真实数据形状下有意义**：空 provider 上所有候选索引都估 1 行；须先造「5k settled + 2 活跃」。`table` 列有别名时返回别名；索引前导列查 `information_schema.STATISTICS`，**按 shape 断言不按 key 名**。
- **本机 WorkBuddy shim 的 PATH 收窄**：`sleep`/`head`/`dirname` 会 `command not found`；临时 Go 程序放仓库内 `backend-go/tmp-xxx/`（跑完删）。
- **⚠️ 同一工作区可能有并发会话**：2026-09-16 实遇另一会话同时改 `internal/delivery/worker.go` / `ENTERPRISE.md` / `backend-go/README.md` 并推送。开工前 `git status` + 看 mtime；提交前若混着别人的改动**先问用户**。

## 历轮索引（细节见 docs/）
十轮 **Batch 4**（SSE Hub：单 upstream / 有界 cache / 协议隔离 / register-before-replay / 慢客户端隔离 / terminal 硬边界 / 生命周期）+ **Batch 4.1**（live gap 修复 / cache 字节硬上界 / 指标代际围栏 / 锁序，**冻结**）。九轮及以前：五轮 93/A- → 六轮 96/A → 七轮 98/A+ → 八轮 P1 关闭 → 九轮（幂等 0021 / 提交状态机 0022 / cursor+O(1) sequence 0023 / 3.2 Streaming Range / 3.3 有效容量 + 协议协商 / 3.3.1 容量 active-run 驱动，**冻结**）。
