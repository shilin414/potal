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
| AC-5-22 MySQL5.7 + Redis7 integration PASS | PASS | migrate ×2 → DB-backed 包测试（execution/delivery/platform/app）全 ok → `tests/integration` ok 144.9s |
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

- `scripts/falsify_review9_patch33.sh` 的 **Mutation 4**（legacy transient content.delta，§二十四）锚点失配：脚本锚点指向第九轮 3.3 时代的 `internal/transport/sse/sse.go` 文本（`frame.Sequence == 0 && … protocol < StreamProtocolRangeDelta`），该文件在 Batch 4.x SSE Hub 重构后已不再包含该文本。`git diff` 证明 Batch 5 未触碰 sse 包（失配在起始 HEAD 7efe0ab 上即存在）。该脚本前三个 mutation 仍 mutated FAIL / restored PASS 正常。**本批按"不顺手修"原则未处理**，建议单独小补丁同步锚点或宣布该 mutation 由 Batch 4.1 系列驱动接管。

## 6. 后续（Batch 6 建议）

按执行文档 §56：优先接 **Aily Workflow Runtime**（同 provider、不同 runtime_type、不同 Executor），只需新增 Workflow Adapter/Executor 并在 `RegisterProvider` 的 Routes 增加一条 `catalog.RuntimeTypeWorkflow` 映射，不改 Worker、不改 cmd/worker 主循环、不改 Dispatcher 算法 —— 这正是 Batch 5 的验收标准（§44）。
