# potal 第十轮 Batch 5 — Worker Dispatcher 整改变更报告

## 1. 开发基线与结论

```text
Repository     shilin414/potal
Branch         dev
起始 HEAD       7efe0aba1c7b5dcaebf4f85e3cacdcecc8b5fb69
Batch          第十轮 Batch 5
主题           Worker Dispatcher
数据库基线      MySQL 5.7（migration baseline 保持 24，本批无 migration）
```

结论：**Batch 5 — Worker Dispatcher 完成，进入 FROZEN。**
`feishu_aily:agent → AilyExecutor` 通过 dispatcher 注册，`cmd/worker` 不再直接绑定任何具体 Executor；unknown provider 启动即失败；unknown runtime / provider mismatch / snapshot mismatch 三类情况全部 ownership-fenced fail closed。

## 2. 变更清单

### 新增

| 文件 | 内容 |
| --- | --- |
| `backend-go/internal/workerdispatch/dispatcher.go` | Registry / ProviderSpec / Plan / providerScopedDispatcher / HealthProbe(HealthFunc) / FailureSink；错误码 `worker_provider_mismatch`、`runtime_route_snapshot_mismatch`、`runtime_handler_unavailable`；派发指标 result 封闭枚举 |
| `backend-go/internal/workerdispatch/dispatcher_test.go` | 执行文档 §34 全部 14 个测试 + §35 并发测试 `TestDispatcherConcurrentExecuteRaceSafe`（100 goroutine × 10 Execute） |
| `backend-go/scripts/falsify_review10_batch5.sh` | Mutation A–G 反证驱动 |
| `backend-go/internal/app/worker_dispatch_test.go` | App wiring 集成测试（STUDIO_TEST_DB/REDIS gated）：plan 存在、agent route、slots provider 一致且与兼容别名共享同一对象、workflow route 不存在、unknown provider 拒绝 |

### 修改

| 文件 | 内容 |
| --- | --- |
| `backend-go/internal/app/app.go` | App 新增 `WorkerDispatch *workerdispatch.Registry`；Build 中创建 provider-wide `ailySlots` 并注册 `feishu_aily:agent → ailyExecutor`（HealthFunc 聚合三个 limiter）；注册失败 → Build 失败；`AilyExecutor` / `ProviderSlots` 字段保留为兼容别名（§21 兼容式演进），且 `ProviderSlots` 与 plan.Slots 是同一对象 |
| `backend-go/cmd/worker/main.go` | execution 分支先 `ResolveProvider(*provider)`（成功才启动消费 goroutine，失败 `exit 2`）；`Worker` 使用 `plan.Provider / plan.Handler / plan.Slots`；启动日志 `worker provider registered provider=... runtime_types=[...] concurrency=...`；provider health monitor 泛化为 `plan.Provider / plan.Slots / plan.Health`，且仅 execution worker（plan != nil）启动；`feishu_delivery` 分支保持独立，不再采样 Aily health |
| `backend-go/internal/platform/telemetry/telemetry.go` | 新增 `studio_worker_dispatch_total{provider, runtime_type, result}`，result 为封闭枚举 `routed / provider_mismatch / snapshot_mismatch / route_missing`，无 run/user/conversation 等高基数 label |
| `backend-go/internal/platform/telemetry/metrics_test.go` | 新增 `TestWorkerDispatchMetricLabelShape`：钉死 label 集合与 4 个封闭 result 值 |
| `.github/workflows/backend.yml` | race gate 追加 `./internal/workerdispatch/...`（原 4 个 package 全部保留） |

### 未改动（冻结确认）

`internal/execution/worker.go`、`ownership.go`、`slots.go`、`finalize.go`、`submission.go`、`internal/transport/sse/**`、OpenAPI、frontend —— 零行为修改（`git diff` 验证 sse/execution 冻结层无改动）。

## 3. 关键设计决策（长期不变量，已写入 MEMORY）

1. execution.Worker 永远不 switch provider/runtime；cmd/worker 永远不直接绑定具体 Executor。
2. Worker execution route identity = canonical `Run.Provider + Run.RuntimeType`（CreateRun 冻结列）。
3. RuntimeSnapshot 只做 frozen config；snapshot 中 `provider_key`/`runtime_type` 出现且与 canonical 列不一致 → `runtime_route_snapshot_mismatch` fail closed（缺失则不失败，兼容旧数据）。
4. Unknown provider = startup fail（exit 2，先于任何 consumer group / XREADGROUP / claim / provider call）；禁止 default/fallback provider。
5. Unknown runtime route = ownership-fenced terminal fail `runtime_handler_unavailable`（message 含 provider=… runtime_type=…）；禁止 fallback 到 agent，避免 lease/reaper 无限循环。
6. ProviderSlots 属于 provider：同 provider 所有 runtime routes 共用 provider-wide capacity；注册时校验 `Slots.Provider == Spec.Key`，错误 wiring 启动失败。
7. Catalog RuntimeRegistry ≠ Worker Dispatcher（两层都按 provider+runtime_type 寻址，但职责分别为 capability adapter 与 claimed Run → Handler）。
8. Dispatcher 不拥有 retry / lease / heartbeat / submission / final reconciliation / panic recovery（无 `defer recover()`，Worker 已有 panic recovery，双层会改变 retry 语义）。
9. handler 错误原样透传（不 Retry、不 Finalize、不统一改 failed）；仅 dispatcher 自身的三类路由问题才由它 terminal fail。
10. `feishu_delivery` 不属于 Run Worker Dispatcher（scheduled result delivery，独立分支）。
11. `FailureSink` 最小权限：只依赖 ownership-fenced `Fail`（生产传 `runs.WorkerOwned()`），测试用 fake sink 免启动 MySQL；`ErrLostOwnership` 原样传播。
12. RegisterProvider 严格校验（空 key / 空 routes / 空 runtime_type / `none` runtime / nil handler / slots provider 不匹配 / 重复注册）+ defensive copy（调用方后续改 map 不影响路由）。

## 4. 测试与验证证据（AC 对照）

| AC | 结果 | 证据 |
| --- | --- | --- |
| AC-5-1 冻结层无行为修改 | PASS | `git diff` sse/execution 冻结层为空 |
| AC-5-2 独立 workerdispatch package | PASS | `internal/workerdispatch/` 新包 |
| AC-5-3 route key = Provider+RuntimeType | PASS | dispatcher.go §12 固定顺序；Mutation A/B 反证可见 |
| AC-5-4 feishu_aily:agent → 现有 AilyExecutor | PASS | Build 注册 + wiring 集成测试；Case 1 启动验收 |
| AC-5-5/5-6 多 runtime / 多 provider 路由 | PASS | `TestRuntimeTypeSelectsDifferentHandlers`、`TestProviderIsPartOfRouteIdentity` |
| AC-5-7 unknown provider 启动失败不消费 | PASS | Case 2：`--provider=abc` → ERROR + exit status 2，无任何消费启动 |
| AC-5-8 unknown runtime 不 fallback | PASS | `TestMissingRuntimeFailsClosed` + Mutation C/D 反证 |
| AC-5-9 provider mismatch fail closed | PASS | `TestProviderMismatchFailsClosed` |
| AC-5-10 snapshot 冲突 fail closed | PASS | `TestCanonicalColumnsWinOverSnapshot` + Mutation B/E 反证 |
| AC-5-11/5-12 slots wiring 校验 + provider-wide | PASS | `TestProviderSlotMustBelongToProvider` + Mutation G；wiring 测试断言 plan.Slots == App.ProviderSlots（同一对象） |
| AC-5-13 cmd/worker 不再引用 `Handler: a.AilyExecutor` | PASS | main.go execution 分支全部使用 plan |
| AC-5-14 provider metrics 不写死 feishu_aily | PASS | monitor 使用 `plan.Provider` |
| AC-5-15 feishu_delivery 行为不变 | PASS | Case 3：delivery worker 正常消费，无 dispatcher 参与、无 Aily health 采样 |
| AC-5-16 无 migration，baseline 24 | PASS | `go run ./cmd/migrate` ×2，第二次干净 no-op |
| AC-5-17 无 OpenAPI / frontend / SSE protocol change | PASS | 未触碰；frontend 基线复验绿（见下） |
| AC-5-18 dispatch metrics 无高基数 label | PASS | `TestWorkerDispatchMetricLabelShape` |
| AC-5-19 Mutation A–G mutated FAIL / restored PASS | PASS | 15/15 ok（含 control），无 FALSIFICATION 残留、无 .orig 残留 |
| AC-5-20 go test ./... PASS | PASS | 全量 `go test ./... -count=1` EXIT=0 |
| AC-5-21 race 包含 workerdispatch 并 PASS | PASS（CI 兜底） | 本机 CGO_ENABLED=0 无 gcc 跑不了 -race（已知环境约束）；race gate 已追加 `./internal/workerdispatch/...`，`TestDispatcherConcurrentExecuteRaceSafe` 交由 CI race job 验证 |
| AC-5-22 MySQL5.7 + Redis7 integration PASS | PASS | migrate ×2 → DB-backed 包测试（execution/delivery/platform/app）全 ok → `tests/integration` ok 144.9s。**5.1 修正（P2-1）**：当时 CI 的 integration job 并未包含 `./internal/app/...`，本表的 app 包证据仅来自本地；CI 集成门自 Batch 5.1 起才真正运行 internal/app（见 §7） |
| AC-5-23 Batch 4/4.1/4.1.1/4.1.2 全部 PASS | PASS | 全量 go test + `tests/integration` 全绿 |
| AC-5-24 旧 falsification 全部 PASS | 7/8 + 1 既有偏差 | 见 §5 已知偏差 |

### 启动验收（执行文档 §42）

- **Case 1** `--provider=feishu_aily`：`worker provider registered provider=feishu_aily runtime_types=["agent"] concurrency=10` → `worker consuming`，存量 run 正常进入 AilyExecutor 状态机（日志中的 `run_config_invalid` / `schedule occurrence state conflict` 均为 dev 共享库存量脏数据在既有业务校验下的正常失败，与本批无关）。
- **Case 2** `--provider=abc`：`ERROR worker provider is not registered provider="abc"`，`exit status 2`，无 consumer group / XREADGROUP / claim。
- **Case 3** `--provider=feishu_delivery`：`delivery worker consuming queue=feishu_delivery`，无 dispatcher 日志、无 Aily execution health 采样。

### Frontend gates（本批零改动，基线复验）

```text
npx tsc --noEmit   EXIT=0
npx vitest run     18 files / 158 tests passed
npx vite build     EXIT=0
```

## 5. 已知偏差（非本批引入）

- `scripts/falsify_review9_patch33.sh` 的 **Mutation 4**（legacy transient content.delta，§二十四）锚点失配：脚本锚点指向第九轮 3.3 时代的 `internal/transport/sse/sse.go` 文本（`frame.Sequence == 0 && … protocol < StreamProtocolRangeDelta`），该文件在 Batch 4.x SSE Hub 重构后已不再包含该文本。`git diff` 证明 Batch 5 未触碰 sse 包（失配在起始 HEAD 7efe0ab 上即存在）。该脚本前三个 mutation 仍 mutated FAIL / restored PASS 正常。**本批按"不顺手修"原则未处理**，已在 Batch 5.1 完成迁移（见 §7 P2-4）。

## 7. Batch 5.1 — Freeze Gate Closure（最终复审整改）

复审（基线 HEAD `54e8d31`）结论：`P0=0、P1=0、P2=4`，生产路由主链设计成立。Batch 5.1 按《potal 第十轮 Batch 5 最新代码复审暨 Batch 5.1 Freeze Gate Closure 完整修改执行报告》执行，四项 P2 全部关闭，之后 **Batch 5 + 5.1 FINAL: FROZEN**。

### P2-1：CI integration gate 从未运行 internal/app

`TestBuildRegistersFeishuAilyAgentDispatchPlan` 依赖 `STUDIO_TEST_DB/REDIS`，unit job 里必然 SKIP，而 integration job 的包清单没有 `./internal/app/...` —— 该测试在 GitHub CI 上从未真正执行。

整改：`.github/workflows/backend.yml` 的 database-backed package tests 步骤加入 `./internal/app/...`。

**CI 红与修复（de3da86）**：首次纳入后 integration job 4 秒失败——CI 环境只提供 DB/Redis 变量（无 `.env.local`），`App.Build` 在 `crypto.NewAESGCM("")` 处报 `crypto: empty encryption secret`。修复：测试内 `t.Setenv` 提供一次性 `TOKEN_ENCRYPTION_KEY` 并显式 `APP_ENV=development`（本测试证明的是 wiring，不是密钥材料；development 钉死同时防未来 CI 端 production 设置把 `validateProduction` 拖进来）。

最终证据（run @ `de3da86`）：`check` job success（unit + **race gate 含 workerdispatch**）；`integration` job 的 "database-backed package tests (real MySQL/Redis)" step success —— 上一轮同 step 正是因该测试真跑 Build 而失败，本轮同 step 通过即证明 `TestBuildRegistersFeishuAilyAgentDispatchPlan` 在 CI 真实执行且 PASS（本地集成环境 verbose 复验同为 `--- PASS`，非 SKIP）。

### P2-2：RegisterProvider 的 typed-nil 漏洞

`handler == nil` 拦不住 Go 的 typed-nil（nil `*T` 装进接口后接口非 nil）：`var executor *SomeExecutor` 装进 Routes 会在第一笔业务请求时 panic；`HealthFunc(nil)` 装进 HealthProbe 会让 worker metrics goroutine 首个 tick panic（普通 goroutine panic 杀死整个进程）。

整改：`dispatcher.go` 新增 `isNilLike`（reflect 判 nil，覆盖 Pointer/Func/Map/Slice/Chan/Interface/UnsafePointer），`RegisterProvider` 三处硬化：
- handler：`isNilLike(handler)` → `ErrNilHandler`（普通 nil 与 typed-nil 都拒绝）；
- Registry 的 FailureSink：`isNilLike(r.failureSink)` → 新错误 `ErrNilFailureSink`（没有可用 sink 就无法 ownership-fenced terminal fail，属 boot wiring 错误）；
- Health：`spec.Health != nil && isNilLike(spec.Health)` → 新错误 `ErrNilHealthProbe`。

测试：`TestTypedNilHandlerRegistrationRejected`、`TestNilFailureSinkRegistrationRejected`、`TestTypedNilFailureSinkRegistrationRejected`、`TestTypedNilHealthProbeRejected`（拒绝后 provider 确实不可 resolve）。

### P2-3：snapshot.provider_key guard 缺独立反证

原 `TestCanonicalColumnsWinOverSnapshot` 与 Mutation E 都只被 runtime_type mismatch 触发——只删 provider guard 不会让任何测试变红。

整改：新增 `TestSnapshotProviderMismatchFailsClosed`（snapshot `provider_key=other_provider` 而 `runtime_type` 列刻意匹配，断言 handler 0 调用、sink 1 调用、`runtime_route_snapshot_mismatch`）；反证扩为 **Mutation H**（只删 provider guard，不碰 runtime guard）→ 新测试 mutated FAIL / restored PASS。

### P2-4：旧第九轮反证 Mutation 4 失效迁移

capability gate 自 Batch 4 起位于 `internal/transport/sse/hub_subscription.go` 的 `Subscriber.accepts`，合同测试是 `TestHubSubscriberProtocolIsolation`。整改**只改脚本不改任何 SSE 生产代码**：`falsify_review9_patch33.sh` Mutation 4 的目标文件换成 `hub_subscription.go`（把 `s.protocol < StreamProtocolRangeDelta` 比较改成 `false`），verdict 换成 sse 包的 `TestHubSubscriberProtocolIsolation`。结果恢复 **8/8**。

### 已知生命周期顺序特征（记录，不重构）

复审确认 Dispatcher 的三类路由检查发生在 `run.started` 与 `ProviderSlots admission` 之后（Worker 既有顺序，非 Batch 5 引入）：provider 容量满时，unsupported runtime 会先走 `provider_inflight_limit → run.retrying → requeue`。即便如此，错误 Executor 与错误 Provider API 都不会被调用。本轮不打开 execution.Worker 冻结层；若未来要求 route-invalid run 在 run.started/capacity 之前终止，应单独设计 Handler/Dispatcher Preflight。

### AC-5.1 对照

| AC | 结果 | 证据 |
| --- | --- | --- |
| AC-5.1-1 路由算法不重写 | PASS | dispatcher.go 仅加 isNilLike 与两处注册校验，Execute 主链未动 |
| AC-5.1-2/5.1-3 普通 nil + typed-nil Handler 拒绝 | PASS | TestNilHandlerRegistrationRejected + TestTypedNilHandlerRegistrationRejected |
| AC-5.1-4 nil / typed-nil FailureSink 拒绝 | PASS | TestNilFailureSinkRegistrationRejected + TestTypedNilFailureSinkRegistrationRejected |
| AC-5.1-5 typed-nil HealthProbe 不进入运行期 | PASS | TestTypedNilHealthProbeRejected |
| AC-5.1-6/5.1-7/5.1-8 provider snapshot 独立测试 + 反证 | PASS | TestSnapshotProviderMismatchFailsClosed + Mutation H |
| AC-5.1-9 Mutation A-G 继续有效 | PASS | 反证 A-G 段全绿 |
| AC-5.1-10/5.1-11 Mutation H + 反证 17/17 | PASS | falsify_review10_batch5.sh ok=17 bad=0 |
| AC-5.1-12/5.1-13 旧 Mutation 4 迁移 + 8/8 | PASS | falsify_review9_patch33.sh ok=8 bad=0（零 SSE 生产改动） |
| AC-5.1-14/5.1-15 CI integration 运行 internal/app 且 PASS | PASS | backend.yml integration step；run @ `de3da86`：同 step 前后两轮对照（上轮 4s 失败证明真跑，本轮 success）+ 本地 verbose `--- PASS` |
| AC-5.1-16 go test ./... PASS | PASS | 全量绿 |
| AC-5.1-17 -race 全绿 | PASS | run @ `de3da86` check job：race gate（含 workerdispatch）success |
| AC-5.1-18 MySQL5.7 + Redis7 integration 全绿 | PASS | migrate ×2 + 包测试 + tests/integration |
| AC-5.1-19 无 migration | PASS | baseline 24 |
| AC-5.1-20 execution / SSE 冻结层无修改 | PASS | `git diff` 为空（仅脚本重定向） |

## 6. 后续（Batch 6 建议）

按执行文档 §56：优先接 **Aily Workflow Runtime**（同 provider、不同 runtime_type、不同 Executor），只需新增 Workflow Adapter/Executor 并在 `RegisterProvider` 的 Routes 增加一条 `catalog.RuntimeTypeWorkflow` 映射，不改 Worker、不改 cmd/worker 主循环、不改 Dispatcher 算法 —— 这正是 Batch 5 的验收标准（§44）。
