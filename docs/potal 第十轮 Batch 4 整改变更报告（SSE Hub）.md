# potal 第十轮 Batch 4 整改变更报告

## SSE Hub — 单 Run 单 upstream + Subscriber fan-out

开发基线：`shilin414/potal` @ `dev`，BASELINE HEAD `d84140483366dca9a650e86bf17db71fd482b8e7`

执行依据：`potal Batch 4 — SSE Hub 完整开发执行报告`

---

## 0. 结论摘要

本批次**只改 SSE Gateway 的内部扇出架构**，对外协议零变化：

```text
改造前   1 Run / N Viewer → N 个 Redis subscription
                          → N 次 JSON decode
                          → N 套 replay/live 交错处理

改造后   1 Run / N Viewer → 1 个 RunHub → 1 个 Redis subscription
                          → 1 次 decode → N 个本地 Subscriber
```

| 验收项 | 结果 |
| --- | --- |
| `GET /api/v2/runs/{id}/stream` 帧格式 / `id:` 规则 / `after` 与 Last-Event-ID 优先级 | byte-for-byte 未变 |
| `?stream_protocol=1/2` 协商与 `X-Studio-Stream-Protocol` | 未变（仍只产出 1/2） |
| `Gateway.Redis` 字段 | **已删除**（结构上禁止回退到一连接一 subscription） |
| 前端 | **零修改**（无 protocol migration，可混跑） |
| DB migration | **无**（继续复用 `ListEventPage`） |
| 单元测试（`internal/transport/sse`） | 46 / 46 PASS |
| 集成测试（`tests/integration`，MySQL 5.7 + Redis 7 真实环境） | 202 PASS，连续 3 次全量绿 |
| 反证（7 个 mutation） | 14 / 14 ok，0 bad |
| `-race ./internal/transport/sse/...` | 本机无 cgo（无 gcc），**已加入 CI race 门** |

---

## 1. 变更文件范围

新增实现：

```text
backend-go/internal/transport/sse/hub.go             999 行
backend-go/internal/transport/sse/hub_cache.go       213 行
backend-go/internal/transport/sse/hub_subscription.go 191 行
```

新增测试：

```text
backend-go/internal/transport/sse/hub_test.go           1044 行
backend-go/internal/transport/sse/hub_cache_test.go      262 行
backend-go/internal/transport/sse/hub_metrics_test.go    212 行
backend-go/internal/transport/sse/sse_hub_test.go        639 行
backend-go/internal/app/bootstrap_test.go
backend-go/tests/integration/review10_sse_hub_test.go    340 行
backend-go/scripts/falsify_review10.sh
```

修改：

```text
backend-go/internal/transport/sse/sse.go        Gateway 改为 Hub subscriber（保留协议/游标/合成终态）
backend-go/internal/app/app.go                  App.SSEHub + Build 装配 + Close 顺序
backend-go/internal/app/bootstrap.go            Gateway{Hub: ...}
backend-go/internal/platform/config/config.go   SSEConfig + 5 个环境变量
backend-go/internal/platform/telemetry/telemetry.go  10 个 Hub 指标 + Replay 语义收窄
backend-go/internal/platform/config/config_test.go
backend-go/internal/platform/telemetry/metrics_test.go
backend-go/tests/integration/{review9_sse_cursor, sse_gateway, review9_patch33_sse}_test.go
.github/workflows/backend.yml                   race 门加入 transport/sse
```

---

## 2. B4-1 单 Run 共享 Redis upstream

`HubManager.GetOrCreate` 在**同一把锁内**完成 lookup + insert，因此并发首次连接不可能造出多个 Hub（§5.1）。
Redis 订阅由 Hub 自己异步启动，`ready` channel 在 SUBSCRIBE 确认（或失败）后关闭。

`Gateway` 不再持有 `*redisx.Client`。这不是风格选择：只要 Gateway 还能自己 Subscribe，未来某条 fallback 分支就会把"一连接一 subscription"带回来。**类型层面已无法表达这种退化。**

外部证据（集成）：`TestSSEHubTwoStreamsShareOneUpstream` 两个 HTTP 连接 → `HubCount=1`、`UpstreamCount=1`、`SubscriberCount=2`。
单元证据：`TestHubManagerSharesOneUpstreamPerRun`（100 viewer + 40 并发首次连接 → Subscribe 恰好 1 次）。

---

## 3. B4-2 有界 durable replay cache（gap fail-closed）

`DurableRing` 三条不变量：

1. **只缓存 `sequence > 0`**。transient `content.delta` 没有 durable 位置，永远不进 cache（`Remember` 直接拒绝）。
2. **event count AND bytes 双限**（默认 2048 / 8 MiB）。只有 count 不构成内存上界：单个 `content.chunk` 可以很大。
3. **连续性段（contiguous segment）**。Redis pub/sub 是 at-most-once，Hub 可能只看到 `100,101,105,106`。此时**丢弃旧段、从 105 重新开始**，对 `after=101` 一律 cache miss → DB fallback。

第 3 条是本批次最关键的 fail-closed：**错误命中会静默丢掉 gap 内所有事件并让客户端游标越过它们；错误未命中只多付一次 DB 读。**

Replay 算法（§12）：cache 覆盖则先播 cache，然后**无论命中与否**都从 cache high-water / 客户端游标做一次分页 DB 读（DB tail reconciliation，补出"已 commit 但 Redis publish 未到"的事件）。DB 读到的 durable 事件同时 `RememberDurable` 预热 cache，但**绝不 fan-out**（同一事件已在 upstream 路上，重复投递正是 durable 游标要防的重复）。bounded ring 保证预热不会把整段历史钉在内存里（`TestDurableRingStaysBoundedOverLongHistory`：100k 事件后停在 256）。

---

## 4. B4-3 register-before-replay 与 catch-up barrier

顺序被固定为：

```text
SUBSCRIBE 确认（WaitReady）
   ↓
Hub.Subscribe(protocol)      ← Subscriber 进入队列
   ↓
cache replay → DB replay
   ↓
drain buffered live
```

两个都必须成立：

- **先 SUBSCRIBE 再 replay**（沿用改造前语义）：replay 的第一次 DB 读发生在订阅确认之后，所以任何已 commit 的事件要么在队列里、要么在这次读里。
- **先注册 subscriber 再 replay**（新增）：replay 未完成前，live 帧（含 transient delta）只进 Subscriber 队列，**绝不写 socket**。否则可能发生"future transient 先到 → 前端 renderedOffset 跳到 1000 + → 随后 replay 的 100~1000 被判为 old range 丢弃 → 中间文本永久缺失"。

`TestGatewayRegistersSubscriberBeforeReplay` / `TestGatewayBuffersTransientDeltaUntilCatchUpCompletes` 用"replay 首页读取中"这个精确窗口发布 live 帧来钉死这两条；反证 D 证明把它们调换后测试会失败。

---

## 5. B4-4 协议属于 Subscriber（AC-2/AC-3）

`Subscriber.accepts` 是唯一的 capability gate，`Hub` 没有协议字段：

```go
if ev.Sequence == 0 && ev.EventType == execution.EventContentDelta &&
   s.protocol < StreamProtocolRangeDelta { return false }
```

**只过滤 `content.delta`**，不是"过滤所有 sequence=0"——sequence 0 是传输标记而非 feature，未来可能出现无需 byte-range 对账的 transient 控制帧，一刀切会把它静默丢掉。durable chunk 路径完全不受影响，legacy 客户端的最终文本因此始终完整。

集成证据：`TestSSEHubMixedProtocolsOnOneRun`——同一个 Hub、同一次 Redis 订阅，v1 只拿到 chunk、v2 拿到 delta+chunk，两边 durable 文本一致。

---

## 6. B4-5 慢客户端隔离与 terminal 硬边界

**非阻塞扇出**（§17）：`Subscriber.Offer` 先做字节预留，再用 `select/default` 做事件数上界；任一超限只关闭**该** subscriber（reason=`slow_consumer`），Hub、upstream、其他 subscriber 完全不受影响。前端凭 durable 游标 2 秒后重连恢复；transient delta 即使丢失也无害，durable chunk 会补齐文本。

**字节预算必须即时释放**（§18）：消费者每取出一个事件就 `Release`。只在 Close 时清会让 `queuedBytes` 随连接寿命单调增长，最终把健康的长连接误判成 slow——`TestHubReleasesQueueBytesOnConsume` 专门覆盖（200 轮消费，预算仅够几个帧，不得被 drop）。

**Terminal 是硬边界**（§19）：`dispatch` 先置 `terminal` 再扇出，之后任何事件（哪怕 Redis 仍投递）一律不投递；`RunHub` 随即释放 upstream。`TestHubTerminalIsAHardBoundary` 断言 `run.completed(100)` 之后发布的 `run.failed(101)` 不得到达，且 upstream 已关闭。

---

## 7. 生命周期：runtime context / Close 顺序 / idle 复用与代际安全

- **Hub 不继承启动 context**（§6 / AC-11）。`studio-stream` 的 `app.Build` 用 30s context；若 Hub 以它为父，进程启动 30 秒后**所有 SSE 流被 cancel**——这种缺陷在快测里看不见，在生产上是"所有用户同时中招"。`NewHubManager` 因此自建 `context.WithCancel(context.Background())`，参数只用于记录"不得传播"。`TestHubRuntimeContextSurvivesStartupContextExpiry`（+ 反证 E）钉死。
- **`App.Close()` 顺序**：`SSEHub.Close()` → `Redis.Close()` → `DB.Close()`（AC-12）。
- **idle 复用与回收**（§21/§22）：最后一个 subscriber 离开后 arm `IdleTTL`（默认 30s）定时器，期间重连复用同一 Hub 与同一 upstream；超时无 subscriber 才回收。
- **代际安全**（§23 / AC-10）：回收必须走 `removeIfSame(runID, hub)` 指针比较。否则"旧 Hub 定时器在新 Hub 已接管后触发"会删掉**新** Hub，连带杀死它的 Redis 订阅——reconnect 风暴会不断复现该竞态。
- **upstream 失败不做内部重连**（§24）：标记 unhealthy → 关闭本地 subscriber（reason=`upstream_closed`）→ 移出注册表 → 前端重连 → DB durable replay + 新 upstream。理由是客户端已有一套完整恢复能力，Hub 内再实现一套会形成**双层恢复系统**。

---

## 8. 配置与指标

`config.SSEConfig`（默认即报告 §10.1 的数值）：

```text
SSE_HUB_CACHE_EVENTS=2048          SSE_HUB_CACHE_BYTES=8388608
SSE_SUBSCRIBER_QUEUE_EVENTS=1024   SSE_SUBSCRIBER_QUEUE_BYTES=4194304
SSE_HUB_IDLE_TTL=30s
```

这些是**内存上界**，因此新增 `getEnvPositiveInt/Int64/Duration`：**能解析但 ≤ 0 的值与乱码同样回退默认**，绝不解释为"不限"（`SSE_HUB_CACHE_EVENTS=0` 是配置错误，不是"关闭缓存"）。`sse.HubOptions.normalized()` 是第二道网；两者一致由 `TestHubDefaultsMatchConfigDefaults` 跨包钉死（config 不能 import sse，所以是两份数值，必须防漂移）。

新增指标：

```text
studio_sse_hubs_active / _hub_subscribers_active{protocol} / _hub_upstreams_active
studio_sse_hub_created_total / _hub_cache_replay_total / _hub_cache_miss_total
studio_sse_hub_subscriber_dropped_total{reason}   slow_consumer|hub_closed|upstream_closed
studio_sse_hub_upstream_failure_total{stage}      subscribe|receive|channel_closed|decode
studio_sse_hub_cache_events / _hub_cache_bytes
```

约束与取舍：

- **禁止 run_id / user_id / conversation_id label**；`protocol` 仍只有 `1|2`。`TestHubMetricLabelsStayBounded` 驱动全部 drop/failure 路径后断言 label 取值落在枚举内，并断言**客户端正常断开不计入 dropped**（否则真实 slow 信号会被正常抖动淹没）。
- `studio_sse_replay_events_total` 语义收窄为"**从 MySQL replay 路径取回**的 durable 事件"（§29），cache replay 由 `studio_sse_hub_cache_replay_total` 计数——两者相除才能看出 Hub 是否真的减少了数据库 replay。
- `_hub_upstreams_active` 未做 `GaugeVec`：Hub 数量无法用无标签 Gauge 精确表达（last-writer-wins），因此 `SSEHubActive`/`CacheEvents`/`CacheBytes` 由 Manager 汇总（`cacheStats` + `removeIfSame` 时剪枝）而非各 Hub 各写一次。

---

## 9. 测试矩阵

单元（`internal/transport/sse`，46 个用例，全绿）：

```text
DurableRing       只缓存 durable / count+bytes 双限 / gap 失效 / 重复与陈旧忽略 /
                  覆盖边界 / 长历史有界 / After 返回副本
Hub 扇出          单 Run 单 upstream / 多 Run 隔离 / protocol 隔离 / 非阻塞隔离 /
                  byte 限触发 slow / 消费即释放字节 / terminal 硬边界 /
                  transient 不入 cache / RememberDurable 不扇出 / gap fail-closed /
                  idle 复用 / idle 回收 / 旧定时器不删新 Hub / upstream 失败 /
                  SUBSCRIBE 未就绪降级 / ready 守卫有界 / build-ctx 过期存活 /
                  Close 幂等且完整 / 客户端断开不算 drop / 并发生命周期
Gateway           register-before-replay / catch-up 期 transient 缓冲 /
                  cache hit 不做历史 DB 读 / cache miss 全量 DB / cache gap fallback /
                  settled 不开 upstream / settled 复用保留 cache /
                  两条真实流共享 Hub 且协议各自生效 / 活 terminal 终止 / keepalive 连接级 /
                  upstream 失败降级为 durable-only
指标与配置        drop/failure label 有界 / 默认值跨包一致 / 负数回退默认
装配              NewServer 必须拿到 App 的同一个 Hub（否则"只 replay、无 live"且静默）
```

集成（`tests/integration`，真实 MySQL 5.7 + Redis 7，202 个顶用例，连续 3 次全绿）：

```text
Case A  TestSSEHubTwoStreamsShareOneUpstream      两流共享 1 hub / 1 upstream，同 terminal
Case B  TestSSEHubMixedProtocolsOnOneRun          v1 无 delta、v2 有 delta，durable 文本一致
Case C  TestSSEHubReconnectKeepsCursorSemantics   断线 → after=cursor 重连 → 无重复无丢失
原有    TestSSE*（3.3/3.3.1 协议、游标、终态、合成终态、replay 停于 terminal）全部保持绿
```

原有测试的装配点随 `Gateway.Redis` 删除做了等价调整（改为注入 `NewHubManager`），**断言一条未改**。`review9_patch33` 的 SSE 读取器抽到 `openStreamAt`，避免同包两份帧解析器。

`-race`：本机 `CGO_ENABLED=0` 无法执行，已把 `./internal/transport/sse/...` 加入 CI 的 race 门（与 execution / delivery / automation 同一步），并在 CI 内覆盖 subscriber add/remove、idle timer、terminal、manager close、fan-out、slow drop、cache 变更。

---

## 10. 反证（falsification）

`backend-go/scripts/falsify_review10.sh`，7 个 mutation × (破坏→必须 FAIL、还原→必须 PASS) = **14 ok / 0 bad**：

| # | 变异 | 期望失败者 |
| --- | --- | --- |
| A | `GetOrCreate` 每次新建 Hub（= 一连接一订阅） | `TestHubManagerSharesOneUpstreamPerRun` |
| B | 关闭 transient delta 的 capability 过滤 | `TestHubSubscriberProtocolIsolation` |
| C | `Offer` 改为阻塞发送 | `TestHubFanoutIsNonBlocking` |
| D | 注册挪到 replay 之后 | `TestGatewayRegistersSubscriberBeforeReplay`、`…BuffersTransientDelta…` |
| E | Hub runtime context 继承 startup context | `TestHubRuntimeContextSurvivesStartupContextExpiry` |
| F | gap 仍宣称 cache 覆盖 | `TestDurableRingGapInvalidatesContinuity`、`TestGatewayCacheGapFallsBackToMySQL`、`TestHubCacheGapFallsBackToMySQL` |
| G | 去掉 terminal 硬边界（dispatch 与 upstream 两处） | `TestHubTerminalIsAHardBoundary` |

Mutation G 刻意改动**两行**：边界由两处实现（dispatch 拒绝扇出、upstream 循环停止读取），单独去掉任一都会被另一处掩盖；"terminal 不是边界"是同一个缺陷，因此是同一个 mutation。

驱动脚本会检查 `FALSIFICATION` 标记与 `*.orig` 残留（本次均为空），并新增了显式警告：**该脚本会改真实源码，绝不可与其它 `go test` 并发运行**（自证过程中确实观察到一次并发导致的无关失败，顺序重跑 3 次全绿）。

---

## 11. 自证过程中发现并修正的 3 个真实问题

1. **live 帧不得推进去重游标**。初版在 live 循环里加了 `lastDelivered = ev.Sequence`，它让既有集成用例 `TestSSECursorSemanticsIgnoreStreamProtocol` 失败：一旦推进，任何"序列低于已投递值的 live durable 帧"都会被丢弃。丢事件严格劣于它想避免的重复，而重复在现实中不可能发生（序列单调分配、post-commit 只发布一次）。**已回到改造前的去重语义**，并在代码里写明原因。
2. **SUBSCRIBE 失败路径的顺序**：必须先 `failUpstream()` 再 `closeReady()`。反过来的话，被 `ready` 唤醒的请求会看到一个"仍然健康"的 Hub 并注册 subscriber，随后被拆除。
3. **`WaitReady` 的布尔语义**是"注册已尘埃落定"而非"注册成功"（成功与否由 `Serving()` 回答），测试初版按前者写会误判；同时补齐了"SUBSCRIBE 一直不返回时由 ready 守卫兜底"的独立用例。

---

## 12. 明确未修改

- 执行内核：Provider Capacity SQL / `ProviderSlots` / `provider_submissions` / submission 状态机 / run lease / ownership epoch / worker retry / `waiting_external` / run request 幂等 / `client_request_id` / terminal transaction / event sequence allocator **全部未触碰**。
- 冻结协议：`stream_protocol` 协商、`after` / Last-Event-ID、`content.chunk`/`content.delta` 帧形状与 offset 语义、SSE `id:` 规则、terminal 语义。
- 前端：`frontend/src/services/runStream.ts` 与 `applyIncrementalRange` 等**零改动**。
- 数据库：**无 migration**（migration 基线仍为 24）。
- 未引入 WebSocket / Redis Streams / Kafka / NATS / 分布式 Hub / 跨实例路由 / sticky session / SSE v3。

---

## 13. 冻结与下一步

验收通过后新增冻结边界：

```text
SSE Hub Architecture            FROZEN
Single-Run Upstream Fanout      FROZEN
Subscriber Protocol Isolation   FROZEN
Replay/Live Catch-up            FROZEN
Slow Subscriber Isolation       FROZEN
Bounded Replay Cache            FROZEN
Hub Lifecycle                   FROZEN
```

下一步：**Batch 5 — Worker Dispatcher**。

遗留（非阻塞）：

- `-race ./internal/transport/sse/...` 仅由 CI 执行（本机无 cgo）。合入后需确认 CI 该步为绿。
- 生产 canary 需按 §59 观察 `studio_sse_active_connections ≫ hubs_active ≈ hub_upstreams_active`；若 `upstreams` 跟随连接数增长，说明存在回退路径。
