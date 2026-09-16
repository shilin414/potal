# Creation Agent Studio — 项目长期记忆

> 只保留「跨会话仍然成立、且违反会复发事故」的内容。历轮细节见 `docs/`。

## 当前状态
- 仓库 `shilin414/potal`，分支 `dev`。第九轮批次一–三 + 3.1/3.2/3.3/3.3.1 **正式冻结**；**第十轮 Batch 4 — SSE Hub 已完成**，报告见 `docs/potal 第十轮 Batch 4 整改变更报告（SSE Hub）.md`。**下一步：Batch 5 — Worker Dispatcher**（其后：conversation lifecycle / message keyset 分页 / 前端长对话）。
- 执行内核（Ownership/Claim/Reaper/Finalize/ProviderSlot/Lease/Heartbeat/Gate）**冻结**，后续批次不得顺手改。
- migration 基线 = **24**（0021 run_requests / 0022 provider_submissions / 0023 next_event_sequence / 0024 provider capacity 索引）。Batch 4 **无 migration**。

## Batch 4（SSE Hub）新不变量
- **单 Run 单 upstream**：`HubManager.GetOrCreate` 在同一把锁内 lookup+insert；`Gateway` **不再持有 `*redisx.Client`**（类型层面禁止回退到一连接一订阅）。Hub 自己异步 Subscribe，`ready` channel 在「确认或失败」后关闭；请求侧 `WaitReady` 之后必须再问 `Serving()` —— `WaitReady` 的语义是「注册已尘埃落定」，不是「成功」。SUBSCRIBE 失败路径必须 **先 `failUpstream()` 再 `closeReady()`**（否则被 ready 唤醒的请求会给一个正在拆除的 Hub 注册 subscriber）。
- **register-before-replay 是承重的**：`SUBSCRIBE 确认 → Subscriber 注册 → cache/DB replay → drain 队列`。replay 期间 live 帧（含 transient delta）只进队列、绝不写 socket，否则前端 renderedOffset 会跳到远端 offset，中间字节随后被当 old range 丢弃 → 文本中间缺失。
- **live 帧不得推进去重游标**：`lastDelivered` 只由 replay 推进。推进它会让「序列低于已投递值的 live durable 帧」被丢弃；丢事件严格劣于重复，而重复不可能发生（序列单调分配 + post-commit 只发布一次）。3.3 的 `TestSSECursorSemanticsIgnoreStreamProtocol` 就是靠 `syncStream` 用 seq 9000 探针帧在钉这条语义。
- **durable cache 必须连续**：gap 一律**丢弃旧段**（fail-closed），绝不"接着追加"。Redis pub/sub 是 at-most-once；错误命中会静默丢掉 gap 内全部事件且游标继续前进——错误未命中只多一次 DB 读。cache 只收 `sequence>0`，count AND bytes 双限；replay 恒做一次 DB tail reconciliation（补"已 commit 未 publish"）；DB 读到的 durable 事件 `RememberDurable` 预热但**不 fan-out**。
- **协议属于 Subscriber**：Hub 无 protocol 字段；只过滤 `content.delta`（不是"所有 sequence=0"——sequence 0 是传输标记，未来 transient 控制帧必须仍到 legacy 客户端）。
- **扇出恒非阻塞**：`Offer` 先字节预留再 `select/default`；超限只关该 subscriber（`slow_consumer`）。**字节预算在消费者取出时释放**，不能只在 Close 清（否则长连接被误判 slow）。
- **terminal 是硬边界**：`dispatch` 先置 `terminal` 再扇出，其后任何事件不投递，随即释放 upstream。
- **Hub runtime context 不继承启动 context**（`NewHubManager` 自建 `Background()`）：`app.Build` 的 ctx 30s 过期，继承它会让进程启动 30 秒后所有 SSE 流被 cancel。`App.Close()` 顺序 = **Hub → Redis → DB**。
- **idle 复用 + 代际安全**：最后一个 subscriber 离开后 arm `IdleTTL`（30s）；回收必须 `removeIfSame(runID, hub)` 指针比较，否则旧定时器会删掉新 Hub 并杀死它的 Redis 订阅。
- **upstream 失败不做 Hub 内重连**：标 unhealthy → 关闭本地 subscriber（`upstream_closed`）→ 移出注册表 → 客户端重连带 durable 游标恢复。双层恢复系统必然长出竞态。
- **指标禁止 run/user/conversation label**；`protocol` 仍只有 `1|2`；`reason`/`stage` 是封闭枚举，**客户端正常断开不计入 dropped**。`studio_sse_replay_events_total` 已收窄为"从 MySQL replay 取的 durable 事件"。

## 核心硬性约定（违反会复发 P0/事故）
- **幂等**：`client_request_id` 解析早于授权/限流/附件校验；身份表 `run_requests`，`request_hash=SHA-256(归一化 payload)`。同 key 不同 hash → 409；resolver infra error → 5xx，绝不伪装 429。
- **Provider 提交状态机**：`sending|accepted|rejected|unknown`；只有 `rejected` 允许重发（同 submission_no/key，key 不含 attempt）。`sending`/`unknown` → `ErrProviderSubmitUnknown` → park `waiting_external`，绝不 blind retry。5xx/timeout=未知，4xx=明确拒绝。**提交边界 = POST 成功且拿到 external id**：200 但无 id / 坏 body → `ErrServer`（Aily=IdempotencyNone → unknown → waiting_external），绝不 failRun；park 后必须 return。
- **accepted identity 持久化是 canonical correctness**：`MarkSubmissionAccepted` 失败必须停止执行链（不 poll/reconcile/finalize/重发），只做本地 DB 短重试（50/100/200ms）；session bind 恒 best-effort。sentinel `ErrProviderAcceptancePersistence`，classifyError 直接上抛。
- **每次 submission 写都是 canonical write**：`MarkSubmissionStateOwned` 事务内 `verifyActiveOwnershipTx` + SQL CAS；四条 query 互斥（结果只从 sending 来；重发只从 rejected/unknown+native；accepted 只从 sending/unknown 且单调）。「记录结果」与「武装重发」是两条语句。native 能力由 executor 从 `catalog.SubmitIdempotencyOf(adapter)` 现场推导。
- **Provider 容量（3.3-A/3.3.1-A）**：有效容量 = `DISTINCT(live slots UNION non-settled sending/unknown/accepted submissions)`，**必须 UNION 不能相加**（正常执行 slot+submission 是同一执行）；排除 `rejected`（否则 4xx 泄漏容量）与已 settled run。admission 判定**必须 exclude self**（否则 self-deadlock）。remote leg **必须 active-run 驱动**：`runs(active) STRAIGHT_JOIN provider_submissions`，状态过滤写**显式 `IN`**（`status` 是 `idx_runs_claim` 前导列，`NOT IN` 用不了索引区间）；**`STRAIGHT_JOIN` 是承重的**（FROM 顺序不足，优化器可重排）；不 FORCE INDEX。容量事实源是 `provider_submissions`，**不新增第二张 uncertain-slot 表**；`awaitExternal` park 时照旧删本地 slot。
- **SSE 协议（已冻结）**：durable 写 `id:<seq>`，transient(seq 0) 绝不写 id；优先级 `query after > Last-Event-ID > 0`；replay 遇 terminal 立即 break（不看 status 快照）；WriteHeader 后必须 Flush；`content.chunk` 只写增量 text+offset。`stream_protocol` 与 `after` 正交、每条连接都发；`StreamProtocol` 返回**协商值**（`requested>=2 → 2`，缺失/乱码/非正/Atoi 溢出 → 1），**绝不回显**（回显 = 无界 Prometheus label）。降级方向恒为"更小能力集"。响应头 `X-Studio-Stream-Protocol` 仅调试用。**发布合同：Backend first / Frontend second**。
- **前端按 UTF-8 字节 offset 对账**：transient delta 与 durable chunk 共用同一 byte coordinate system，**两者都带 absolute end offset**；reducer 三分支（drop / 补 suffix / append+跳计数器）；中文 3 字节、emoji 4 字节，**绝不用 `string.length`**；offset missing → legacy append。gateway 天然可能 chunk 先于 delta，必须用 offset 去重。
- **终态语义**：`interrupted` status ≠ `run.interrupted` event（`{reason}`=retry 标记，`{status}`=终态）。`IsTerminal()` 只认 `{cancelled,succeeded,failed}`；SQL `status NOT IN (四个)`。migration 新增 `MAX(sequence)` 必须 `COALESCE(MAX(...),0)+1`。
- **时钟/事务**：Clock Authority 无兜底（dbNow 失败即中止）。续约类 UPDATE 必须单调写（`GREATEST(CURRENT_TIMESTAMP(3), DATE_ADD(...,1000 MICROSECOND))`）；MySQL UPDATE 返回 changed rows 不是 matched。merged heartbeat `HeartbeatOwnedWithSlot` BOTH OR NEITHER，Renew 恒 XX-only。`SET timestamp=<sec>`+MaxOpenConns(1) 可钉时钟，同配方可注入真实 1205。metrics/Redis fan-out post-commit。attempt 唯一消耗点 `BeginProviderAttemptOwned`；消息落库同事务 `TouchConversationUpdated`。
- **准入/Gate**：唯一门 `catalog.AuthorizeExecution`；普通用户错误 404；先判 `err==nil` 再 `executionDenied(err)`。Gate 双检查点 level-triggered（Gate1 claim 后 + Gate2 `beginSubmit`）；kill=cancel，pause/infra/未知=Defer fail-closed。Provider 门禁按 `provider_key` fail-closed。Streaming 必须同步 Open。
- **并发**：一 conversation 一个非终态 Run（409）；锁序 `users→conversations`；hard-delete cascade 必须显式带 `run_requests`/`provider_submissions`（无 FK 级联）。`waiting_external` 有界：`ExpireParkedExternalRuns` 用 DB 时钟−grace。
- **sequence O(1)**：`runs.next_event_sequence` 锁内 SELECT FOR UPDATE→UPDATE x+1；绝不用 `COUNT(*)+1`；事件读取一律 LIMIT。
- **前端**：store 异步写 functional setState；`activeRunId` compare-and-clear；拉取失败 null=未知不清空；`run.cancelled` 独立终态（execution_disabled=硬取消）不得映射 done；`run.deferred` 靠 `run.started` 清除；channel 发送可取消。
- **antd 表单取值（P0，2026-09-16 事故）**：拼 payload 一律 `form.getFieldsValue(true)`；**绝不用 `validateFields()`/`getFieldsValue()` 的返回值**——它们只含已注册 Form.Item 的路径，`setFieldsValue` 写入但无 Form.Item 的字段会被**静默丢弃**。回归测试 `frontend/src/components/Schedules/__tests__/scheduleEditorPayload.test.tsx`（jsdom）。

## 工具与踩坑
- **⚠️ 同一文件绝不可并行发两个 Edit**：工具会各自基于同一旧快照写回，**后写覆盖前写 = 静默丢失一处改动**。2026-09-16 实测：`internal/app/app.go` 的 `SSEHub: sse.NewHubManager(...)` 与 `sse.go` 的 `RunReader` 接口、`config.go` 的 `SSE: SSEConfig{...}` 三处装配被静默吃掉，**代码仍编译通过**（Hub=nil 时只是"没有 live 事件"）。改同一文件务必**串行**，改完 `grep` 复核关键行。
- **反证脚本绝不可与其它 `go test` 并发**：脚本改真实源码，中途构建的测试会编译到坏代码（实测一次全量集成因此报无关失败，顺序重跑 3 次全绿）。既有驱动 `backend-go/scripts/falsify_review9*.sh`；Batch 4 = `falsify_review10.sh`（7 mutation / 14 checks）。
- **反证必须做**（还原修复→FAIL→还原→PASS）。脚本必须 `trap cleanup EXIT` 还原 `.orig`；跑完 `grep FALSIFICATION` + `find -name '*.orig'` 双查。改代码后同步更新旧脚本锚点。
- **CI**：前端 `npx tsc --noEmit` / `npx vitest run` / `npx vite build`（不跑 eslint）。后端 gofmt/vet/build/test + **race**（execution/delivery/automation/**transport/sse**）+ integration（mysql5.7+redis7）。**本机 `CGO_ENABLED=0` 无 gcc，`-race` 只能由 CI 兜**。
- **MySQL 5.7** `192.168.211.26:20336/xiaoan`、Redis `192.168.211.239:6380`（`backend-go/.env.local`）；DSN UTC 不动。集成开关 `STUDIO_TEST_DB=1`/`STUDIO_TEST_REDIS=1`。全库 COUNT=0 会被 fixture 假红，清理圈定 `provider LIKE 'itest%'`。改 `db/queries/*.sql` 跑 `~/go/bin/sqlc.exe generate`。本机全量集成 ≈110–120s。
- **本机环境**：Git Bash 常丢 coreutils，命令前加 `export PATH="/usr/bin:/bin:/c/software/Git/cmd:$PATH"`。git `refs/remotes/<name>/<branch>` 写入有缺陷：push 后必须 `mkdir -p` 建目录 + 写 loose ref + 双写 packed-refs，并用 `git ls-remote` 核对；看到 ahead/gone 先 ls-remote 别急着重推。无 `gh`，查 CI 用匿名 GitHub API。bash 嵌套 heredoc 会被内层定界符截断（用 python 做文本变异更稳）。断言时间戳未变用 `CAST(col AS CHAR)` 逐字符串比较。
- **Redis pub/sub 不为未来订阅者缓冲**：SSE 集成测试用探针帧证明"网关已订阅"时必须**循环重发直到收到**；探针帧会占用一个真实 durable 序列（`syncStream` 用 9000），**不要把它当游标**（Batch 4 的 `contentCursor()` 因此只取 `content.chunk` 的序列）。既有 SSE 测试要区分"测 transient 帧本身"（须声明 `stream_protocol=2`）与"测 legacy 兼容"（不声明）。
- **集成测试的 Gateway 装配**：`sse.Gateway` 已无 `Redis` 字段，测试需注入 `sse.NewHubManager(ctx, rdb, nil, opts)` + `t.Cleanup(hub.Close)`；同一 server 多流共享一个 Hub（`startHubSSEServer` / `openStreamAt`）。
- **本地起环境是 5 个进程**：api / stream / worker(`--provider=feishu_aily`) / **worker(`--provider=feishu_delivery`)** / vite。漏掉 delivery 那个时定时任务照常跑、有回复，但飞书收不到（`delivery_executions` 停在 `pending`）；消费者起来后 `dueScanLoop` 自动补发。`(cmd &)` 自 detach 在普通 Bash 调用里活不下来，常驻必须 `run_in_background`。
- **`CASFinishDelivery` 双占位符**：`sent_at = IF(?='succeeded',…)` 的第二个 `?` 被 sqlc 生成成 `Column5`，调用方必须把 status 传两次，否则成功投递的 `sent_at` 恒 NULL。
- **sqlc 隐式改名会连带破调用点与反证脚本锚点**：`ps.run_id <> ?` 改写成 `r.id <> ?` 会让生成参数名从 `RunID_2` 变 `ID`。要保名就写 `ps.run_id <> ?`。改完 `sqlc generate` 后 `md5sum` 复核手工回滚过的文件。
- **EXPLAIN 断言只在真实数据形状下有意义**：空 provider 上所有候选索引都估 1 行，MySQL 按 tie-break 会选恰好要排除的路径；必须先造「5k settled + 2 活跃」。`table` 列有别名时返回别名；索引前导列查 `information_schema.STATISTICS`，**按 shape 断言不按 key 名**；`rows` 只打印。
- **本机 WorkBuddy shim 的 PATH 收窄**：`sleep`/`head`/`dirname` 会 `command not found`。`go run /tmp/xxx.go` 在 Windows 报 `GetFileAttributesEx /tmp/...`，临时程序放仓库内 `backend-go/tmp-xxx/`（跑完 `rm -rf`）。
- **⚠️ 同一工作区可能有并发会话**：2026-09-16 实遇另一会话同时改 `internal/delivery/worker.go` / `ENTERPRISE.md` / `backend-go/README.md` 并推送。开工前先 `git status` + 看关键文件 mtime；提交前若工作区混着别人的改动，**先问用户**再定范围。
- migration up/down 验证可写临时 `cmd/tmp-migdown`（在 backend-go 下 source 路径是 `file://db/migrations`）；跑完删掉。

## 历轮索引（细节见 docs/）
五轮 93/A- → 六轮 96/A → 七轮 98/A+ → 八轮 P1 关闭 → 九轮批次一–三 + 3.1（幂等 0021 / 提交状态机 0022 / cursor+O(1) sequence 0023 / 去 snapshot）+ 3.2（Streaming Range / Provider Identity / 幂等 replay）+ 3.3（Provider 有效容量 + SSE 协议协商）+ 3.3.1（容量准入 active-run 驱动 + stream_protocol 协商上界，**第九轮冻结**）→ **十轮 Batch 4（SSE Hub：单 Run 单 upstream / 有界 durable cache / Subscriber 协议隔离 / register-before-replay / 慢客户端隔离 / terminal 硬边界 / Hub 生命周期）**。
