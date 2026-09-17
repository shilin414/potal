# 冻结子系统细节（Batch 4 系列 SSE Hub / Batch 5 Worker Dispatcher）

> 从 `MEMORY.md` 拆出：这些是**已 FROZEN 的子系统**的实现约束，只在改动对应包时才需要。
> 跨切面、随时要遵守的不变量仍在 `MEMORY.md`。动手前另需读 `docs/potal 第十轮 Batch 4*.md` / `Batch 5*.md`。
> 理由、证据、验收对照都在那些报告里，这里只留「违反就会出事」的结论。

## Worker Dispatcher（Batch 5 / 5.1，FINAL FROZEN）
- **新包 `internal/workerdispatch`**（不动 execution、不动 catalog）：`RegisterProvider` 严格校验——空 key / 空 routes / 空 runtime_type / `none` 拒绝 / handler nil / `Slots.Provider != Spec.Key` / 重复注册 / `FailureSink` nil / **typed-nil HealthProbe**；routes 做 defensive copy。unknown provider → `ResolveProvider` 报错，`cmd/worker` **exit 2 且先于任何 consumer group / XREADGROUP / claim**。
- **typed-nil 硬化（5.1 P2-2）**：Go 把 nil `*T` 装进接口后 `== nil` 为 false → 校验必须用 `isNilLike`（reflect）。否则 `var executor *SomeExecutor` 会拖到第一笔业务请求才 panic，`HealthFunc(nil)` 会杀死 worker metrics goroutine（普通 goroutine panic 杀整个进程）。错误：`ErrNilFailureSink` / `ErrNilHealthProbe`。
- **route identity = canonical `Run.Provider + Run.RuntimeType`**（CreateRun 冻结列）。snapshot 里的 `provider_key`/`runtime_type` **只**做「present AND different → fail closed」（`runtime_route_snapshot_mismatch`），缺失不失败；**绝不用 snapshot 做主路由、绝不 fallback 到 agent**（missing route = `runtime_handler_unavailable` 的 ownership-fenced terminal fail，防 lease/reaper 死循环）。两条 snapshot guard 各有独立反证（5.1 P2-3/P2-4，provider guard 由 `TestSnapshotProviderMismatchFailsClosed` + Mutation H 钉住）。
- **`ProviderSlots` 是 provider-wide**：同 provider 全部 runtime routes 共用一个 semaphore（`app.go` 的 `ailySlots` 与 `ProviderSlots` 是**同一对象**，wiring 集成测试有断言）。
- Dispatcher 只做「选 Handler」+「对自身三类路由问题 terminal fail」；handler 错误**原样透传**（不 Retry / 不 Finalize / 不改写）；**无 `defer recover()`**（Worker 已有 panic recovery，双层会改 retry 语义）；`FailureSink` 最小权限（生产 `runs.WorkerOwned()`），`ErrLostOwnership` 原样传播。
- `cmd/worker`：execution 分支零具体 Executor 引用；provider monitor 泛化 `plan.Provider/Slots/Health`，**仅 `plan != nil` 才启动**（delivery worker 不再采样 Aily）。启动日志 `worker provider registered provider=... runtime_types=[...] concurrency=...`。
- 指标 `studio_worker_dispatch_total{provider,runtime_type,result}`，result 封闭枚举 `routed/provider_mismatch/snapshot_mismatch/route_missing`，无高基数 label。
- **CI integration gate 必须包含 `./internal/app/...`**（5.1 P2-1）：wiring 测试需要 `STUDIO_TEST_DB/REDIS`，unit job 恒 SKIP，包不进 integration job 就永远不在 CI 真跑。**在 CI 里调 `app.Build` 的测试必须自带最小 env**（`t.Setenv` 一次性 `TOKEN_ENCRYPTION_KEY` + `APP_ENV=development`），否则 `crypto.NewAESGCM("")` 直接炸。
- **已知生命周期顺序特征（记录，不重构）**：Dispatcher 路由检查发生在 `run.started` 与 ProviderSlots admission **之后**（Worker 既有顺序）；容量满时 unsupported runtime 会先走 `provider_inflight_limit → requeue`，但错误 Executor/Provider API **永不会被调用**。要提前终止需单独设计 Preflight。
- 反证 `falsify_review10_batch5.sh`（A–H，**17/17**）；race gate 已追加 `./internal/workerdispatch/...`。

## SSE Hub（Batch 4 / 4.1 / 4.1.1 / 4.1.2，永久 FROZEN）
- **两把锁禁止嵌套**：`GetOrCreate` 释放 `manager.mu` 后才调 `hub.mu` 的方法；不可服务则 `removeIfSame`（指针比较）进下一轮。单 Run 单 upstream；`ready` 在「确认或失败」后关闭，`WaitReady` 之后**必须再问 `Serving()`**；SUBSCRIBE 失败先 `failUpstream()` 再 `closeReady()`。
- **Unpublished RunHub is inert**：`newRunHub` 不得启动 timer/goroutine/callback；初始 idle timer 只能由 publish 之后的 `armInitialIdleTimer()` 启动，且它**见 `closed` 或 `subscribers != 0` 就 return、绝不 arm**。`armIdleTimerLocked`/`stopIdleTimerLocked`/`idleTimer` **只在持 `hub.mu` 时访问**，不得靠「IdleTTL 足够大」躲 race。
- **register-before-replay 是承重的**：`SUBSCRIBE 确认 → 注册 Subscriber → cache/DB replay → drain 队列`；replay 期间 live 帧只进队列、绝不写 socket。
- **durable live 顺序**：`lastDelivered` = 已**连续写出**的最高 durable seq（不是「最高见过」）。live `> last+1` 即 FORWARD GAP → 先 `repairDurableGap`，它**必须验证日志自身连续**（`<= last` continue、`> through` break、**`!= last+1` → 有洞，立刻 `LiveGapFailed` 停，洞之后一帧不发**）。已发出的连续前缀**不回滚**，客户端从实际收到的最高连续 cursor 重连 = fail closed。
- **cache**：必须连续，gap 丢弃旧段；只收 `seq>0`；count AND bytes 双限；replay 恒做一次 DB tail reconciliation；`RememberDurable` 预热但**不 fan-out**；**`ApproxBytes > CacheMaxBytes` 的帧不缓存**（硬上界）；`lastObservedSeq`（曾喂到）与 `lastSeq`（真正 retained）**必须分开**；`reportCache` 必须做 `m.hubs[runID] != hub` **代际校验**，否则旧代际写回幽灵指标且永不自愈。
- **协议属于 Subscriber**：Hub 无 protocol 字段；只过滤 `content.delta`（seq 0 是传输标记，其他 transient 帧仍须到 legacy）。**扇出恒非阻塞**：`Offer` 先字节预留再 `select/default`，超限只关该 subscriber，字节预算在消费者取出时释放。terminal 是硬边界（`dispatch` 先置 `terminal` 再扇出）。
- **其余**：`App.Close()` = Hub → Redis → DB；upstream 失败**不做 Hub 内重连**；Hub runtime context **不继承启动 context**；指标禁止 run/user/conversation label，`reason`/`stage`/`result` 是封闭枚举。
- **反证三条防线**：`falsify_review10_patch411.sh` 的 **L**（constructor inert）/ **M**（canonical log hole fail closed）/ **N**（subscriber-before-arm 不得 arm timer）。**Mutation N 没有 race gate 第二半**——删 guard 不产生 data race，`-race` 照样绿，只有 `TestArmInitialIdleTimerSkipsAlreadySubscribedHub`（确定性构造「已 publish 未 arm + subscriber 已 attach」，断言 `idleTimer == nil`）看得见。原 `TestConcurrentSubscriberCancelsInitialIdleTimer` 已改名 `TestSubscriberStopsArmedInitialIdleTimer`（只覆盖「timer 已 arm → Subscribe 停表」）。
- **旧 `falsify_review9_patch33.sh` Mutation 4 已迁移**到 `Subscriber.accepts`（`hub_subscription.go`）+ `TestHubSubscriberProtocolIsolation`，8/8——SSE 生产代码零改动。**锚点会随重构失效，改 sse 后必须复核脚本锚点。**
