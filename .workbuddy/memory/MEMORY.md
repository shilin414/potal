# Creation Agent Studio — 项目长期记忆

> 只留「跨会话仍成立、违反会复发事故」的代码规则；理由与细节见 `docs/`。
> **本机环境、命令、CI 映射、flake 见同目录 `PITFALLS.md`（动手前建议读一遍）。**

## 当前状态
- 仓库 `shilin414/potal`，分支 `dev`。执行内核（Ownership/Claim/Reaper/Finalize/ProviderSlot/Lease/Heartbeat/Gate）与第九轮 **冻结**，不得顺手改。
- 第十轮 Batch 4 / 4.1 / 4.1.1 / **4.1.2** 全部收口，**SSE Hub 永久 FROZEN**（4.1.2 为 test-only）。**Batch 5 — Worker Dispatcher + Batch 5.1 Freeze Gate Closure 已完成并 FINAL FROZEN**（报告 `docs/potal 第十轮 Batch 5 Worker Dispatcher 整改变更报告.md` §7）。**下一步：Batch 6 — 优先 Aily Workflow Runtime**（验证同 provider 不同 runtime_type 不同 Executor）。
- migration 基线 = **24**（0021 run_requests / 0022 provider_submissions / 0023 next_event_sequence / 0024 容量索引）。Batch 4/5 系列**无 migration**。

## Batch 5/5.1 — Worker Dispatcher（FINAL FROZEN —— 动手前先读 `docs/potal 第十轮 Batch 5*.md`）
- **新包 `internal/workerdispatch`**（不动 execution、不动 catalog）：`RegisterProvider` 严格校验（空 key / 空 routes / 空 runtime_type / `none` 拒绝 / handler nil / `Slots.Provider != Spec.Key` / 重复注册 / **FailureSink nil** / **typed-nil HealthProbe**）+ routes defensive copy；unknown provider `ResolveProvider` 报错，cmd/worker **exit 2 且先于任何 consumer group/XREADGROUP/claim**。
- **5.1 typed-nil 硬化（P2-2）**：Go 的 nil `*T` 装进接口后 `== nil` 为 false——注册校验必须用 `isNilLike`（reflect），否则 `var executor *SomeExecutor` 会拖到第一笔业务请求 panic、`HealthFunc(nil)` 会杀死 worker metrics goroutine（普通 goroutine panic 杀整个进程）。错误：`ErrNilFailureSink` / `ErrNilHealthProbe`。
- **route identity = canonical `Run.Provider + Run.RuntimeType`**（CreateRun 冻结列）；snapshot 的 `provider_key`/`runtime_type` 只做「present AND different → fail closed」（`runtime_route_snapshot_mismatch`），缺失不失败；**绝不**用 snapshot 做主路由、**绝不** fallback 到 agent（missing route = `runtime_handler_unavailable` ownership-fenced terminal fail，防 lease/reaper 死循环）。**两条 snapshot guard 各有独立反证**（5.1 P2-3/P2-4：provider guard 由 `TestSnapshotProviderMismatchFailsClosed` + Mutation H 钉住）。
- **ProviderSlots 是 provider-wide**：同 provider 全部 runtime routes 共用一个 semaphore（app.go `ailySlots` 与 `ProviderSlots` 兼容别名是**同一对象**，wiring 集成测试有断言）。
- Dispatcher 只选 Handler + 对自身三类路由问题 terminal fail；handler 错误**原样透传**（不 Retry/Finalize/改写）；**无 `defer recover()`**（Worker 已有 panic recovery，双层会改 retry 语义）；`FailureSink` 最小权限（生产 `runs.WorkerOwned()`），`ErrLostOwnership` 原样传播。
- `cmd/worker`：execution 分支零具体 Executor 引用；provider monitor 泛化 `plan.Provider/Slots/Health`，**仅 plan != nil 才启动**（delivery worker 不再采样 Aily）；启动日志 `worker provider registered provider=... runtime_types=[...] concurrency=...`。
- 指标 `studio_worker_dispatch_total{provider,runtime_type,result}`，result 封闭枚举 `routed/provider_mismatch/snapshot_mismatch/route_missing`，无 run/user 等高基数 label。
- **CI integration gate 必须包含 `./internal/app/...`**（5.1 P2-1 教训：wiring 测试要 STUDIO_TEST_DB/REDIS，unit job 恒 SKIP，integration job 不列包就永远不会在 CI 真跑）。
- **已知生命周期顺序特征（记录不重构）**：Dispatcher 路由检查在 `run.started`/ProviderSlots admission 之后（Worker 既有顺序）；容量满时 unsupported runtime 先走 `provider_inflight_limit → requeue`，但错误 Executor/Provider API 永不会被调用。要提前终止需单独设计 Preflight。
- 反证 `falsify_review10_batch5.sh`（A-H，**17/17**）；race gate 已追加 `./internal/workerdispatch/...`。**旧 `falsify_review9_patch33.sh` Mutation 4 已迁移到 `Subscriber.accepts`（hub_subscription.go）+ `TestHubSubscriberProtocolIsolation`，8/8**——SSE 生产代码零改动；锚点会随重构失效，改 sse 后必须复核脚本锚点。

## 本机三条操作红线
- **同一文件绝不可并行发两个 Edit**：后写覆盖前写 = 静默丢失（实测被吃掉且**仍编译通过**）。串行改 + grep 复核。
- **反证驱动绝不可与其它 `go test` 并发**（脚本改真实源码）。
- **不要为对照基线 `git checkout <sha>`**：本机会被 SIGTERM 打断并留下半成品工作区。用 `git checkout <sha> -- <路径>`。
- 另外：**同一工作区可能有并发会话** —— 开工前 `git status` + 看 mtime；提交前若混着别人的改动**先问用户**。

## SSE Hub（永久冻结 —— 动手前先读 `docs/potal 第十轮 Batch 4*.md`）
- **两把锁禁止嵌套**：`GetOrCreate` 释放 `manager.mu` 后才调取 `hub.mu` 的方法；不可服务则 `removeIfSame`（指针比较）进下一轮。单 Run 单 upstream；`ready` 在「确认或失败」后关闭，`WaitReady` 之后必须再问 `Serving()`；SUBSCRIBE 失败先 `failUpstream()` 再 `closeReady()`。
- **Unpublished RunHub is inert**：`newRunHub` 不得启动 timer/goroutine/callback；初始 idle timer 只能由 publish 之后的 `armInitialIdleTimer()` 启动，且它**见 `closed` 或 `subscribers != 0` 就 return、绝不 arm**。`armIdleTimerLocked`/`stopIdleTimerLocked`/`idleTimer` **只在持 `hub.mu` 时访问**，不得靠「IdleTTL 足够大」躲 race。
- **register-before-replay 是承重的**：`SUBSCRIBE 确认 → 注册 Subscriber → cache/DB replay → drain 队列`；replay 期间 live 帧只进队列、绝不写 socket。
- **durable live 顺序**：`lastDelivered` = 已连续写出的最高 durable seq（不是"最高见过"）。live `> last+1` 即 FORWARD GAP → 先 `repairDurableGap`，且它**必须验证日志自身连续**（`<= last` continue、`> through` break、**`!= last+1` → 有洞，立刻 `LiveGapFailed` 停，洞之后的帧一帧不发**）。已发出的连续前缀**不回滚**，客户端从实际收到的最高连续 cursor 重连 = fail closed。
- **cache**：必须连续，gap 丢弃旧段；只收 `seq>0`；count AND bytes 双限；replay 恒做一次 DB tail reconciliation；`RememberDurable` 预热但**不 fan-out**；**`ApproxBytes > CacheMaxBytes` 的帧不缓存**（硬上界）；`lastObservedSeq`（曾喂到）与 `lastSeq`（真正 retained）必须分开；`reportCache` 必须做 `m.hubs[runID] != hub` **代际校验**，否则旧代际写回幽灵指标且永不自愈。
- **协议属于 Subscriber**：Hub 无 protocol 字段；只过滤 `content.delta`（seq 0 是传输标记，其他 transient 帧仍须到 legacy）。**扇出恒非阻塞**：`Offer` 先字节预留再 `select/default`，超限只关该 subscriber，字节预算在消费者取出时释放。terminal 是硬边界（`dispatch` 先置 `terminal` 再扇出）。
- **其余**：`App.Close()` = Hub → Redis → DB；upstream 失败**不做 Hub 内重连**；Hub runtime context **不继承启动 context**；指标禁止 run/user/conversation label，`reason`/`stage`/`result` 是封闭枚举。
- **反证三条防线**：`falsify_review10_patch411.sh` 的 **L**(constructor inert) / **M**(canonical log hole fail closed) / **N**(subscriber-before-arm 不得 arm timer)。**Mutation N 无 race gate 第二半**——删 guard 不产生 data race，`-race` 照样绿，只有 `TestArmInitialIdleTimerSkipsAlreadySubscribedHub`（确定性构造「已 publish 未 arm + subscriber 已 attach」，断言 `idleTimer == nil`）看得见。原 `TestConcurrentSubscriberCancelsInitialIdleTimer` 已改名 `TestSubscriberStopsArmedInitialIdleTimer`（它只能覆盖「timer 已 arm → Subscribe 停表」）。

## 核心硬性约定（违反会复发 P0/事故）
- **幂等**：`client_request_id` 解析早于授权/限流/附件校验；身份表 `run_requests`，`request_hash=SHA-256(归一化 payload)`；同 key 不同 hash → 409；resolver infra error → 5xx。
- **Provider 提交状态机**：`sending|accepted|rejected|unknown`；只有 `rejected` 可重发；`sending`/`unknown` → park `waiting_external`，**绝不 blind retry**；5xx/timeout=未知，4xx=明确拒绝。**提交边界 = POST 成功且拿到 external id**；200 无 id → `ErrServer`（Aily → unknown → waiting_external），绝不 failRun；park 后必须 return。
- **accepted identity 持久化是 canonical correctness**：`MarkSubmissionAccepted` 失败必须停链（不 poll/reconcile/finalize/重发），只做本地 DB 短重试（50/100/200ms）；sentinel `ErrProviderAcceptancePersistence`。
- **每次 submission 写都是 canonical write**：`MarkSubmissionStateOwned` 事务内 `verifyActiveOwnershipTx` + SQL CAS；四条 query 互斥；「记录结果」与「武装重发」是两条语句。
- **Provider 容量**：有效容量 = `DISTINCT(live slots UNION non-settled sending/unknown/accepted)`，**必须 UNION 不能相加**；排除 `rejected` 与已 settled run；admission **必须 exclude self**。remote leg 必须 active-run 驱动：`runs(active) STRAIGHT_JOIN provider_submissions`，状态过滤写**显式 `IN`**；**`STRAIGHT_JOIN` 是承重的**；不 FORCE INDEX。
- **SSE 协议**：durable 写 `id:<seq>`，transient(seq 0) 绝不写 id；优先级 `query after > Last-Event-ID > 0`；replay 遇 terminal 立即 break；WriteHeader 后必须 Flush；`content.chunk` 只写增量 text+offset；`stream_protocol` 与 `after` 正交、每条连接都发，返回**协商值**（`>=2 → 2`，缺失/乱码/非正/溢出 → 1），**绝不回显**。**发布合同 Backend first / Frontend second。**
- **前端按 UTF-8 字节 offset 对账**：transient delta 与 durable chunk 共用同一坐标、**都带 absolute end offset**；reducer 三分支（drop/补 suffix/append+跳计数器）；中文 3 字节、emoji 4 字节，**绝不用 `string.length`**；offset missing → legacy append。
- **终态语义**：`interrupted` status ≠ `run.interrupted` event；`IsTerminal()` 只认 `{cancelled,succeeded,failed}`；SQL `status NOT IN (四个)`；migration 新增 `MAX(sequence)` 必须 `COALESCE(MAX(...),0)+1`。
- **时钟/事务**：Clock Authority 无兜底；续约类 UPDATE 必须单调写（`GREATEST(CURRENT_TIMESTAMP(3), DATE_ADD(...,1000 MICROSECOND))`）；MySQL UPDATE 返回 changed rows；merged heartbeat `HeartbeatOwnedWithSlot` BOTH OR NEITHER，Renew 恒 XX-only；`SET timestamp=<sec>`+MaxOpenConns(1) 可钉时钟；metrics/Redis fan-out post-commit；attempt 唯一消耗点 `BeginProviderAttemptOwned`；消息落库同事务 `TouchConversationUpdated`。
- **准入/Gate**：唯一门 `catalog.AuthorizeExecution`；普通用户错误 404；先判 `err==nil` 再 `executionDenied(err)`；Gate 双检查点 level-triggered；kill=cancel，pause/infra/未知=Defer fail-closed；Provider 门禁按 `provider_key` fail-closed；Streaming 必须同步 Open。
- **并发**：一 conversation 一个非终态 Run（409）；锁序 `users→conversations`；hard-delete cascade 必须显式带 `run_requests`/`provider_submissions`；`waiting_external` 有界（`ExpireParkedExternalRuns` 用 DB 时钟−grace）。
- **sequence O(1)**：`runs.next_event_sequence` 锁内 SELECT FOR UPDATE→UPDATE x+1；绝不用 `COUNT(*)+1`；读取一律 LIMIT。
- **前端 store**：异步写 functional setState；`activeRunId` compare-and-clear；拉取失败 null=未知不清空；`run.cancelled` 独立终态不得映射 done；`run.deferred` 靠 `run.started` 清除。
- **antd 表单取值（P0 事故）**：拼 payload 一律 `form.getFieldsValue(true)`；**绝不用 `validateFields()`/`getFieldsValue()` 的返回值**（只含已注册 Form.Item 的路径，其他字段被**静默丢弃**）。回归测试 `frontend/src/components/Schedules/__tests__/scheduleEditorPayload.test.tsx`。

## 历轮索引（细节见 `docs/`）
十轮 **4**(单 upstream/有界 cache/协议隔离/register-before-replay/慢客户端隔离/terminal 硬边界) → **4.1**(live gap 修复/cache 字节硬上界/指标代际围栏/锁序) → **4.1.1**(constructor inert/初始 idle timer 竞态/canonical 连续性 fail-closed) → **4.1.2**(test-only：交错 B 确定性测试 + Mutation N) → **5**(Worker Dispatcher：provider+runtime_type 路由/unknown provider 启动失败/missing route fail-closed/snapshot guard/slots provider-wide，**冻结**)。九轮及以前：五轮 93/A- → 六轮 98/A+ → 八轮 P1 关闭 → 九轮（幂等 0021 / 提交状态机 0022 / O(1) sequence 0023 / Streaming Range / 有效容量 + 协议协商，**冻结**）。
