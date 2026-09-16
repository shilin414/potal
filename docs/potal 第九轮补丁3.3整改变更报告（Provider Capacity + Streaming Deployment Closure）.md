# potal 第九轮补丁 3.3 整改变更报告（Provider Capacity + Streaming Deployment Closure）

> 对应执行报告：《potal 第九轮补丁3.3修改执行报告（Provider Capacity + Streaming Deployment Closure）》（2026-09）
> 基线：`e43cc0e`（第九轮补丁 3.2：Streaming Range Protocol / Provider External Identity Closure / Accepted Identity Persistence Closure / Idempotency Replay-Before-Error / OpenAPI SSE Contract Cleanup），backend / frontend CI 均成功。
> 本批次只关闭 **3.3-A Provider Effective Capacity** 与 **3.3-B Streaming Protocol Deployment Compatibility** 两个问题，未重新调整任何 3.2 已关闭的协议语义。

---

## 一、执行基线核对

3.2 报告定义的核心不变量在当前代码中已实际成立，本批次只做**确认**、不做改动：

| 不变量 | 核对结果 |
|---|---|
| Provider 200 但取不到 external id → unknown | ✅ 未触碰 |
| accepted identity 无法持久化 → 停止执行链 | ✅ 未触碰（本批次反而**依赖**它：不要求 Worker 特判） |
| transient delta / durable chunk 共用 UTF-8 absolute end offset | ✅ 未触碰 |
| `client_request_id` post-miss 拒绝前先做 bounded replay | ✅ 未触碰 |

**明确未改动**（执行报告 §三十五）：`client_request_id` hash / reservation、`ResolveRunRequestWithWait`、Provider submission state transitions、`MarkSubmissionAccepted` fencing、Gate 1/2、Lease、Heartbeat、Retry / Defer、Finalize transaction、Event sequence allocator、`content.chunk` storage format、前端 `applyIncrementalRange`、Aily executor submission 语义。执行内核（Ownership / Claim / Reaper / Finalize / ProviderSlot / Lease / Heartbeat / Gate）保持冻结。

---

## 二、3.3-1：Migration 0024（只加索引）

新增 `backend-go/db/migrations/0024_provider_effective_capacity.{up,down}.sql`，未修改 0022 原 migration。

```sql
ALTER TABLE provider_submissions
ADD KEY idx_provider_submissions_capacity (provider, state, run_id);
```

**为什么需要**：3.3-A 让 `provider_submissions` 进入准入事实。该表原本只有
`PRIMARY KEY(run_id, submission_no)` 与 `UNIQUE(provider, idempotency_key)`，
`WHERE provider = ? AND state IN (...)` 既用不上主键（前导列是 run_id）也用不上唯一键
（前导列是 idempotency_key），退化为全表扫描。准入在**每一次 claim** 上执行，而该表只增不减
（submission 是历史，run 结算后不会被删），于是"系统越忙扫描越慢"。`(provider, state, run_id)`
同时解决过滤与覆盖（run_id 就在二级索引里，join runs 不必回表）。

**为什么不是新增 `provider_uncertain_slots` 表**（执行报告 §三）：`provider_submissions`
本来就是"HTTP 外部副作用不能被本地 lease 撤销"的 durable ledger，再加一张 uncertain-slot
表会产生两个 source of truth，并需要长期维护
`submission=unknown 但 slot 不存在` / `submission=accepted 但 slot 残留` /
`run terminal 但 slot 未清` 三类对账。没有必要。

索引是纯增量、对 0022 时代代码安全可在线执行。

---

## 三、3.3-2：Effective Capacity 查询（`db/queries/execution.sql`）

保留 `CountActiveProviderSlots` 且**不改其语义**（现明确为 controlled depth），另新增三条：

| Query | 用途 |
|---|---|
| `CountProviderEffectiveInflight` | admission 真正使用的容量事实 |
| `CountProviderEffectiveInflightExcludingRun` | admission 判定时**排除自身** |
| `CountProviderUncontrolledInflight` | 运维诊断：无 slot 但可能仍在执行的 Provider 工作 |

有效容量的定义是 **DISTINCT union**，而不是两数相加：

```sql
SELECT COUNT(*) AS n FROM (
    SELECT s.run_id FROM provider_execution_slots s
    WHERE s.provider = ? AND s.expires_at > CURRENT_TIMESTAMP(3)
    UNION
    SELECT ps.run_id FROM provider_submissions ps JOIN runs r ON r.id = ps.run_id
    WHERE ps.provider = ? AND ps.state IN ('sending','unknown','accepted')
      AND r.status NOT IN ('cancelled','succeeded','failed','interrupted')
) capacity_runs;
```

三个关键判定：

1. **`UNION` 不能改成 `UNION ALL`** —— 正常运行中的 Run 同时持有 slot 与
   sending/accepted submission，那是**一次** Provider 执行；相加会 double count。
2. **`accepted` 必须计入** —— Worker 在 accept 后崩溃、slot 过期，但 Provider 侧 chat
   仍在跑；只统计 `sending/unknown` 会让 (max+1) 个 Run 挤进已经在跑 max 个的 Provider。
3. **`rejected` 必须排除** —— 语义是"Provider 明确拒绝、外部动作不存在"；计入会在每次
   400/401/403/429 泄漏一个容量位（缓慢的永久容量收缩）。
4. **必须带 `runs.status` 过滤** —— `provider_submissions` 是历史账本，run 成功后
   `state='accepted'` 不会被改写；不查 run 状态则每个已完成的 run 都会永久占位。

`UNION` 的时间窗口是有界的：`ExpireParkedExternalRuns` 会在 unresolved grace（默认约 10min）
后把 `waiting_external` 收敛为 `failed/provider_submit_unknown`，随之释放预留。

`sqlc generate` 已执行，生成代码稳定（二次生成无新增差异）。

---

## 四、3.3-3：`ProviderSlots` 改造（`internal/execution/slots.go`）

### Acquire 的容量判定改为 exclude-self

```text
provider admission lock
→ verify ownership
→ cleanup expired controlled slots
→ same ownership slot?
     └─ TouchProviderSlot → CountEffectiveInflightExcludingRun → depth = otherDepth + 1
        （不设 max 检查：刷新一个已存在的 slot 不消耗容量）
→ CountEffectiveInflightExcludingRun
→ max check
→ create slot
```

**为什么必须排除自身**（执行报告 §十/§十一）：`max_inflight = 1` 时，Run A 的
submission 为 `sending`、slot 已过期、reaper 已把它重新入队；新 Worker claim A 后
`Acquire(A)`，若用全局计数则 `effective = 1 → 1 >= max → 拒绝 A`。A 从此永远无法重新进入
Executor，也就永远消费不到"把它 park 成 waiting_external"的那条队列消息 —— **self-deadlock**，
任何 lease / timeout 都无法打破。

排除自身后，一个 Run 恒占 **恰好 1** 个容量：要么由它自己的 remote reservation 占，要么由它
即将拿到的 slot 占，绝不重复也绝不归零。

`Acquire` 返回值中的 depth 语义相应改为"其它 Run 的有效容量 + 本次"，仍作为拒绝指标。

### Depth 三面

| 方法 | 含义 |
|---|---|
| `Depth()` | **effective**：admission 认为已占用的容量（live slots ∪ unresolved submissions） |
| `ControlledDepth()` | **controlled**：有 Worker 持有的活 slot（原 `CountActiveProviderSlots` 语义） |
| `UncontrolledDepth()` | **uncontrolled**：有 sending/unknown/accepted submission 但没有活 slot |

三者均保持 nil-receiver / limiter-disabled 透明（返回 0 而非 panic），因为 worker 每 5s 抓一次指标。

### 文档口径修正（执行报告 §十九）

旧注释声称"active slot 数量是真实 Provider 执行的 **STRICT** 上界"。这在 3.3 之后既不再
正确也不再是本系统提供的东西。新口径：

> Effective provider capacity is the DISTINCT union of (1) live ownership-scoped provider slots
> and (2) non-settled runs whose provider submission is sending / unknown / accepted. Within the
> configured unresolved-execution grace, this is the conservative upper bound used by admission.

即系统保证的是"**对所有本地非终态、且可能仍存在的 Provider 执行，`max_inflight` 是严格保守上界**"，
而不是"无论 Provider 在后台做什么，`max_inflight` 永久绝对成立"。一个无法 lookup 的 Provider，
在 10 分钟后系统为了可用性主动结束 uncertainty window，此时**我们事实上无法证明它停止了** ——
这个事实必须写在文档里，而不是靠旧措辞掩盖。

### 不变的行为（刻意不修改）

- `AwaitExternalOwned` 继续在 park 时删除本地 ProviderSlot（§十五）：容量由 `unknown +
  non-settled` 保留，不需要 lease-bound slot。
- accepted persistence 写失败时**不**要求 `execution.Worker` 特判 `aily.ErrProviderAcceptancePersistence`（§十六）：
  数据库里仍是 `sending + running`，容量自然不会消失；`execution` 不应依赖具体 Provider adapter package。

---

## 五、3.3-4：容量测试与并发反证（`tests/integration/review9_patch33_test.go`）

| Case | 场景 | 断言 |
|---|---|---|
| 1 | slot + sending submission | effective=1（**不是 2**）；第二个 Run 被拒 |
| 2 | unknown + waiting_external + 无 slot | effective=1 / controlled=0 / uncontrolled=1；B 被拒 |
| 3 | accepted + slot 过期（Worker crash） | effective=1 / controlled=0 / uncontrolled=1；B 被拒 |
| 4 | 同一 unresolved Run recovery | `Acquire(A)` **被允许**；`BeginProviderSubmission` 返回 `ErrProviderSubmitUnknown`（绝不二次 submit）；最终 park 且容量仍为 1 |
| 5 | accepted + succeeded（走真实 `FinalizeOwnedRun`） | effective=0（finalize 同事务删 slot + run 结算） |
| 6 | rejected + 无 slot + run 非终态 | effective=0；容量真的可用 |
| 7 | acceptance persistence failure（复用 3.2 真实 InnoDB 1205 配方） | StartChat=1 / poll=0；Worker 释放 slot 后 controlled=0 / uncontrolled=1 / effective=1；B 被拒 |
| 8 | `AwaitExternalOwned` 与并发 admission race | 采样器在整个 park 事务期间 min(effective) ≥ 1，**不存在 capacity=0 窗口**；并发 4 次 Acquire 全部被拒 |

Case 8 的机制说明：park 的 `slot 删除 + status waiting_external + submission unknown` 属于**一个**
canonical transition，在 MySQL REPEATABLE READ 下外部读者要么看到事务前快照（slot 存在），要么看到
提交后状态（unknown submission + waiting_external run），中间态不可见 —— 这正是"先删 slot 再改 status"
式实现会漏出窗口的地方。

---

## 六、3.3-5：SSE stream protocol capability negotiation

### 网关（`internal/transport/sse/sse.go`）

```go
const (
    StreamProtocolLegacy     = 1
    StreamProtocolRangeDelta = 2
)
func StreamProtocol(r *http.Request) int   // 缺失/不可解析/越界 → legacy
const StreamProtocolHeader = "X-Studio-Stream-Protocol"
```

live Redis frame 上只抑制一种帧：

```go
if frame.Sequence == 0 &&
   frame.EventType == execution.EventContentDelta &&
   protocol < StreamProtocolRangeDelta {
    continue
}
```

**只 suppress `content.delta`**，不做"所有 sequence=0 一刀切"：sequence 0 是传输标记而非特性，
未来可能有不需要 byte-range 对账的 transient 控制帧，一刀切会把它静默丢掉。

响应头回写 `X-Studio-Stream-Protocol: <协商值>`，仅用于浏览器 Network 面板 / 日志 / 灰度排查，
客户端逻辑不得依赖它。

降级方向是刻意的：缺失参数 = 旧前端，不可解析 = 客户端 bug，两者都退到**更小的能力集**。
反过来（400 或默认送 delta）会破坏一个无法得知服务端已升级的客户端，或把 append-only 客户端
渲染不安全的帧交给它 —— 两个失败模式不对称，所以默认值不是风格选择。

### 为什么不在 Gateway 再做一遍 range reconciliation（§二十五）

那会变成 Executor 一套 + Frontend 一套 + Gateway 一套，三份 range 算法；而紧接着的
**批次四 SSE Hub** 还会再重构 Gateway。capability negotiation 更简单，且形态可以被 Hub 原样继承：

```text
v2 客户端    → transient + durable
legacy 客户端 → durable only
```

legacy 客户端仍然收到全部 durable `content.chunk`（约 500ms / 2KB 内落库并送出），
最坏只是"流式刷新稍粗"，不会丢最终回答。durable 的 `id:` 语义、`after` / `Last-Event-ID`
优先级、terminal 硬边界**全部未改**。

### 前端（`frontend/src/services/runStream.ts`）

```text
/v2/runs/<id>/stream?after=<cursor>&stream_protocol=2
```

`stream_protocol` 与 `after` 用 `URLSearchParams` 组装，且**每条连接都发**（含首次）：
渲染能力不是游标位置，折叠进 `Last-Event-ID` 或只在重连时发送，会让中途升级的连接用错线格式。
导出的 `STREAM_PROTOCOL_RANGE_DELTA` 与后端常量一一对应。

### OpenAPI

`GET /api/v2/runs/{runId}/stream` 新增 query parameter `stream_protocol`
（`enum: [1,2]`, `default: 1`），SSE 顶层契约补上
`content.delta is sent only to stream_protocol >= 2 clients`，并声明响应头
`X-Studio-Stream-Protocol`。`make gen-api` 已执行，同时修正了一处既有漂移：
`RunEvent.event_type` 的 `run.cancelled` 早已在 spec 里，但提交的 `api_gen.go` 未重新生成。

---

## 七、3.3-7：可观测性

新增指标（`internal/platform/telemetry`）：

```text
studio_provider_capacity_depth{provider,kind="effective"}
studio_provider_capacity_depth{provider,kind="controlled"}
studio_provider_capacity_depth{provider,kind="uncontrolled"}
studio_provider_capacity_reject_total{provider,reason="effective_inflight_limit"}
studio_sse_stream_protocol_total{protocol}          # §三十二 第 4 步的 v1/v2 占比
```

`ProviderInflight` 保留为 controlled depth 的历史别名（不再改语义），worker 采集循环同时写入
effective / controlled / uncontrolled 三面；`uncontrolled > 0` 时打 WARN。

```text
正常：effective ≈ controlled，uncontrolled ≈ 0
故障：effective > controlled，uncontrolled > 0（保守计数，本身不是故障）
需报警：uncontrolled 长时间持续上升 → Provider unknown outcome / Worker crash /
        reconcile backlog / acceptance persistence failure 正在累积
```

---

## 八、3.3-8：反证（执行报告 §三十八，4 个 mutation）

新脚本 `backend-go/scripts/falsify_review9_patch33.sh`，`trap cleanup EXIT`，跑后双查
`grep -rn FALSIFICATION` + `find . -name '*.orig'`。

| Mutation | 目标测试 | 预期 | 实测 |
|---|---|---|---|
| 1. 容量判定还原成 `CountActiveProviderSlots`（只看本地 slot） | `TestProviderCapacityHoldsAfterUnknownParkReleasesSlot` | FAIL | FAIL ✓（还原后 PASS ✓） |
| 2. 容量判定改为**含自身**（`CountProviderEffectiveInflight`） | `TestProviderCapacityLetsTheSameUnresolvedRunRecover` | FAIL | FAIL ✓（还原后 PASS ✓） |
| 3. 从容量状态里删掉 `accepted` | `TestProviderCapacityHoldsAfterAcceptedWorkerCrash` | FAIL | FAIL ✓（还原后 PASS ✓） |
| 4. Gateway 恢复向 legacy 客户端发送 transient delta | `TestSSELegacyClientReceivesNoTransientDelta` | FAIL | FAIL ✓（还原后 PASS ✓） |

结果：`falsifications ok=8 bad=0`，无 `FALSIFICATION` 残留、无 `*.orig` 残留。

---

## 九、验证记录

### Backend
- `gofmt -l .` 干净；`go vet ./...` 干净；`go build ./...` 通过。
- `go test ./... -count=1` 全绿（单测无 DB 依赖）。
- `STUDIO_TEST_DB=1 STUDIO_TEST_REDIS=1 go test ./tests/integration/ -count=1` → **ok 107s 全绿**。
- `go test -race` 本机不可用（`-race requires cgo`，本机无 gcc），按项目惯例由 GitHub Actions 覆盖。

### Migration 0024（真实库 MySQL 5.7 192.168.211.26:20336/xiaoan）
```text
after up       : [provider state run_id]
second up      : no-op ( version=24 dirty=false )
after down(-1) : []                     # 索引确实被 down 掉
after re-up    : [provider state run_id]
```
- `sqlc generate` / `oapi-codegen` 二次生成无新增差异（生成代码稳定）。

### Frontend
- `npx tsc --noEmit` 干净；`npx vitest run` → **17 文件 156 用例全绿**；`npx vite build` 成功。

### 新增 / 修改测试
- 新增 `tests/integration/review9_patch33_test.go`（8 个 Case）。
- 新增 `tests/integration/review9_patch33_sse_test.go`（legacy / v2 / cursor（2 子用例）/ terminal）。
- 新增 `internal/transport/sse/sse_protocol_test.go`（协商降级 + cursor 与 protocol 互不干扰）。
- 新增 `internal/platform/telemetry/metrics_test.go::TestProviderCapacityMetricsAreExported`。
- 新增 `internal/execution/slots_test.go` 两个透明度用例。
- **适配**：`TestSSETransientFrameCarriesNoResumeId` 改为声明 `stream_protocol=2` —— 它断言的是
  transient 帧的 `id:` 行，而 legacy 客户端按新契约根本不再收到 transient 帧；两侧语义分别由
  该用例与 `TestSSELegacyClientReceivesNoTransientDelta` 各自钉住。
- 前端 `runStream.test.ts` 新增 2 个用例（首次连接即声明、重连时与 cursor 并存）；
  既有 delta/chunk reverse overlap 与 offset-less legacy delta fallback 用例**全部保留**。

---

## 十、发布顺序（写进 `backend-go/README.md`）

```text
1. 迁移 0024（纯加索引，可在线）
2. 全量部署 Backend 3.3（api / stream / worker / scheduler 全部切完）
     旧 frontend 不带 stream_protocol=2 → 只推 durable chunk，天然安全
3. 部署 Frontend 3.3（带 stream_protocol=2）→ 恢复低延迟 transient delta
4. 观测 provider controlled / uncontrolled / effective depth 与 SSE v1/v2 占比
```

**禁止 frontend-first**：旧 Backend 不认识 `stream_protocol`，会继续发送不带 offset 的
transient delta；新前端虽能 fallback append，但"durable chunk 先到、buffered delta 后到"
的线序仍可能造成重复渲染。

---

## 十一、3.3 关键不变量（冻结）

```text
Capacity
  Provider effective inflight
    = DISTINCT( live controlled slots UNION non-settled sending/unknown/accepted submissions )
  rejected 不占容量；settled run 不占 remote capacity

Self recovery
  Acquire(current run) 必须把当前 run 从既有容量计数中排除，否则 unresolved run self-deadlock

Unknown
  waiting_external 可以删除 ProviderSlot，但 unknown submission 继续保留 effective capacity

Accepted crash
  accepted submission + 无活 slot + run 非终态 → 仍占 effective capacity

Streaming
  stream_protocol >= 2 → delta + chunk
  legacy               → durable chunk only
```

---

## 十二、后续

第九轮批次 1～3 + 3.1 + 3.2 + 3.3 至此可以宣布
**Execution / Submission / Event Protocol correctness frozen**：不再围绕幂等、Provider submit、
Event Cursor、Streaming Range、Provider capacity correctness 做零散修补。

下一阶段进入规模化 / 性能架构：

```text
批次四：SSE Hub       —— 必须继承 stream_protocol capability / durable cursor /
                        transient sequence=0 / byte offset / terminal hard boundary；
                        protocol capability 属于 SUBSCRIBER，不是 RUN，
                        不要因为 Hub 共享订阅就把它放到 RunHub 全局
批次五：Worker Dispatcher
批次六：Conversation lifecycle + message keyset 分页
批次七：长对话前端
```
