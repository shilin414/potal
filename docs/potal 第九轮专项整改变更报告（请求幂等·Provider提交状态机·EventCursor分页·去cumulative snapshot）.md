# potal 第九轮专项整改变更报告（请求幂等·Provider 提交状态机·Event Cursor 分页·去 cumulative snapshot）

**范围**：第九轮专项审查报告的**批次一、二、三**（报告 §二十三 的前三批）。
**基线**：`4cbfc4367fee5a6b33bd087ba4985716ec42ccf7`（第八轮整改后的 dev）
**不在本轮范围**：批次四（SSE Hub）、批次五（Worker Dispatcher）、批次六（Conversation lifecycle / message 分页）、批次七（前端虚拟列表）。报告 §二十三 的依赖顺序要求 Event Cursor 先于 SSE Hub、Message 分页先于前端虚拟列表，因此这三批必须停在批次三。

---

## 一、本轮关闭的结论

| ID | 等级 | 结论 |
|---|---|---|
| P0-1 | P0 | `POST /v2/runs` 现在具备端到端 `client_request_id` 幂等：重放不写任何新行，同 id 不同 payload → 409，重放优先于准入限制 |
| P0-2 | P0 | Provider 提交具备持久化状态机：**结果不确定时绝不重发**，`accepted` 的提交在 re-claim 时被复用而不是重新提交；无法确认时 run 进入 `waiting_external`（非终态），由有界 grace 兜底收敛 |
| P1-1 | P1 | SSE 具备 durable cursor：`id: <sequence>` + `?after` / `Last-Event-ID` + 前端本地去重 |
| P1-3 | P1 | `run_events` sequence 改为 O(1) 分配器；事件读取一律 keyset 分页（默认 200 / 上限 1000） |
| P1-4 | P1 | `content.chunk` 只写增量 text + offset，不再写累计 snapshot |

报告另列的 P1-2（SSE Hub）、P1-5（Worker poller）、P1-6（Conversation lifecycle）、P1-7（前端长对话）仍开放，属批次四–七。

---

## 二、P0-1：`client_request_id` 端到端幂等

### 迁移 0021 `run_requests`

```sql
PRIMARY KEY (user_id, client_request_id), UNIQUE KEY uniq_run_requests_run (run_id)
request_hash BINARY(32)  -- SHA-256(application_id, conversation_id|0, content, 去重后的 attachment set)
```

**为什么不放在 `runs` 上**：首次请求可能是 lazy conversation，`conversation_id` 为空且 conversation 在**同一个事务里**才被创建，因此"请求身份"必须在任何 run 行存在之前就能被占用。

### 请求顺序（契约，非实现细节）

```
1 认证 → 2 JSON/基础校验 → 3 client_request_id + request_hash
→ 4 解析已存在的 reservation        ← 早于授权与准入
→ 5 执行授权 → 6 QPS/outstanding → 7 conversation/附件校验
→ 8 一个事务：reserve + conversation + message + run + attachment claim + outbox
```

第 4 步必须在 5–7 之前：**首次请求已经消费过这些检查**（它的附件已被自己的 run claim，它的 run 计入 per-user outstanding）。若先跑 5–7，一次**已经成功**的请求的合法重放会被报成 `400 attachment already used` 或 `429 too many outstanding runs`。

`CreateRunIdempotent` 在任何失败后**再次解析**：并发重复的败者会先在 user 行锁 / conversation 锁上输给自己的原请求，从而看到"超出配额"，此时已提交的 reservation 证明该请求已被服务，于是返回原 run 而不是那个失败。

### 响应

* 首创 `201`；重放 `200` + `idempotency_replayed: true`（两个字段 `omitempty`，普通创建的 201 body 逐字节不变）
* 同 id 不同 payload → `409 idempotency_key_reused`

### 前端

* `createRun` 增加 `client_request_id`；`newClientRequestId()` 在非安全上下文（明文 http）下用 `getRandomValues` 构造 v4 形状 id，不依赖 `crypto.randomUUID`
* `useRunChatStore.sendMessage` 以**发送动作**为单位持有身份：失败的发送记住 id，同一 payload 的手动重试复用；内容或附件集合变化则换新 id；发送成功后释放（下一次点击是新动作）
* 重放响应按 run id 去重插入，避免同一轮对话在 UI 里出现两次

---

## 三、P0-2：Provider 提交幂等状态机

### 迁移 0022 `provider_submissions`

```
PK (run_id, submission_no)，UNIQUE (provider, idempotency_key)
state ∈ sending | accepted | rejected | unknown
idempotency_key = potal:run:<run_uuid>:submit:<n>    ← 不含 attempt
```

key 里**不含 attempt** 是刻意的：一次外部业务动作的多个 HTTP 重试必须给 provider 同一个 key，否则支持幂等键的 provider 会把它们当成不同请求——那正是本表要关闭的"第二个 provider chat"。

### `BeginProviderSubmissionOwned` 的判定表

| 上一次提交状态 | 结论 |
|---|---|
| 无记录 | 创建 `sending`，**允许发送** |
| `sending` / `unknown` | `ErrProviderSubmitUnknown` —— **禁止发送**（无论 payload 是否变化） |
| `accepted` | 返回已接受的 submission（含 external id）——**禁止发送**，改为从结果 API 收敛 |
| `rejected` | 允许发送：同 payload 复用同一 submission_no/key，不同 payload 才分配新的 submission_no |

attempt 预算检查在任何 submission 行被触达**之前**执行，因此预算耗尽的 run 不会留下发送意图。

### 提交失败的分类（纯函数，表驱动）

| 失败 | 含义 | 处理 |
|---|---|---|
| 429 / 4xx 业务错误 / 401、403 | provider **明确拒绝**，外部无副作用 | 记录 `rejected`，走原有重试/失败策略 |
| 5xx | **不是答复**：500 可能在 provider 已受理之后才抛出 | 未知 |
| timeout / 传输错误 | 响应从未到达 | 未知 |
| 未知 + provider 无幂等能力 | —— | 记录 `unknown` → **park** |
| 未知 + provider 声明 `IdempotencyNative` | provider 可折叠重发 | 走原有策略 |

`IdempotencyAware` 是**可选**接口（type assertion 探测），`SubmitIdempotencyOf` 对未实现者 **fail-closed** 返回最弱类：新接入的 provider 默认继承安全策略而不是危险策略。

Aily 声明 `IdempotencyNone`：`POST /agents/:id/chats` 没有幂等键，且只能按 `agent_chat_id` 反查——而那正是未知场景下缺失的值。报告 §6 的第三类（"可按 request key 查询"）**没有实现**：本代码库唯一的 client 按 chat id 反查，声明一个无法执行的类别只会在最安全敏感的分支里加一条不可测路径。

### run 被复用而不是重复提交

`beginSubmit` 在调用 provider 之前先看 `claimed.Run.ExternalRunID`，再看 submission 记录。**两者都非空时一律不发送**，改为 `reconcile` / `pollUntilTerminal`，且 chat id 优先取 **submission 记录**（run 快照可能是 claim 时读的、早于 accepted 写入）。

### `waiting_external` 是有界的

`waiting_external` 是非 settled 状态，会占住 conversation（否则永久 409）与 per-user 配额，所以必须有收敛路径，否则 park 就是僵尸。

`ExpireParkedExternalRuns`（worker scanLoop 每 tick 调一次）：cutoff 取 **DB 时钟 − grace**（默认 10 分钟，`Service.WaitingExternalGrace` 可注入），一个事务内 CAS `waiting_external → failed/provider_submit_unknown` + 终态事件 + occurrence 收敛。读取 DB 时钟失败**跳过本轮**，绝不回落本机时钟。

---

## 四、批次二：Event Cursor / Keyset 分页 / O(1) sequence

* **迁移 0023** `runs.next_event_sequence`，backfill 用 `COALESCE(MAX(sequence)+1, 1)`；分配改为 `SELECT … FOR UPDATE` → `UPDATE … + 1` → `INSERT`，全部在写者既有的 run 行锁事务内，sequence 仍 gap-free / unique（`UNIQUE(run_id, sequence)` 兜底）。计数器自增写 `x + 1` 而不是赋已读值——MySQL 报 changed rows，同值写会报 0 并被读成"行不存在"
* `ListRunEventsAfter` 加 `LIMIT`；`ListEventPage` 返回 `items / next_after / has_more`，`has_more` 由「页满」推导而非 `COUNT(*)`——否则又会把 O(history) 读带回来。上限 1000，超出被 clamp
* `GET /v2/runs/{id}/events` 改为返回分页信封；SSE 网关改为**逐页** replay，不再一次性把整段历史装进内存

### SSE durable cursor

```
durable 帧 (sequence > 0) → 写 `id: <sequence>`，推进游标
transient 帧 (sequence 0) → 不写 `id:`（0 是哨兵，不是位置）
优先级：query `after` > `Last-Event-ID` > 0
```

transient 写 `id: 0` 会让浏览器下一次 `Last-Event-ID` 变成 0，静默把重放拉回起点——对增量 chunk 就是重复文本。

顺带修掉一个真实观测问题：`WriteHeader` 后没有 `Flush`，Go 会缓冲响应头，于是**没有事件可发的新 run 会让客户端一直等不到响应头**。现在显式 flush。

前端 `openRunStream` 维护 `lastDurableSequence`：重连带 `?after=`，并本地丢弃 `sequence <= cursor` 的帧。本地去重不是冗余——chunk 变成增量后，被喂两次就是可见的重复输出。

---

## 五、批次三：去掉 cumulative snapshot

`content.chunk` 曾同时写增量 `text` 与**累计** `snapshot`，使一次 run 的事件数据量随回答长度**平方**增长：1 MB 回答 = 2KB + 4KB + … + 1MB ≈ 250 MB。现在只写 `text` + `offset`。

前端 reducer 的 `snapshot` 分支**保留**：历史事件仍带 snapshot，且对旧数据 replace 是自愈的。

部署顺序（写在代码注释里）：durable cursor + 前端去重必须先于停止写 snapshot——本轮两者同批发布，单次发布内满足。

---

## 六、验证

**本机全绿**：`go build` / `go vet` / `gofmt` / `go test ./... -count=1`（无 env，单测）与 `STUDIO_TEST_DB=1 STUDIO_TEST_REDIS=1 go test ./... -count=1`（含集成 **74.7 s**）；前端 `tsc --noEmit` / `vitest run`（**143 passed**）/ `vite build`。`sqlc generate` 幂等。

**新增测试**

后端集成（`tests/integration/review9_fixes_test.go`、`review9_aily_boundary_test.go`、`review9_sse_cursor_test.go`）：
20 并发同 id → 1 conversation/1 message/1 run/1 outbox；重放返回原 run；同 id 不同 payload → 冲突；并发重放败者不 429；reservation 随失败回滚；reservation 只读不落库；submission unknown 禁止重发且不消耗预算；accepted 可恢复且 key 不变；rejected 允许同 key 重试；park → sweep 释放 conversation；重复 sweep 不覆写；sequence gap-free / 单调 / 每 run 独立；分页走完且不重不丢、超限被 clamp；**真实 executor + 假 provider 传输**下 ①已接受提交不会再开流（OpenStreamChat=0）②未决提交不接触 provider ③提交超时 park 而非重试 ④明确拒绝仍走失败策略 ⑤400 个 delta 的 chunk 数据量与回答等长。
后端单测：hash 归一化（附件顺序/空 id/长度前缀抗歧义）、submission key 形式与稳定性、提交失败分类表、增量 chunk payload。
前端：`runStream` 游标 5 例（含 durable→transient 顺序）、`useRunChatStore.idempotency` 7 例。

**反证（必须做）**：两项脚本化，逐个临时还原修复 → 确认对应测试 FAIL → 还原 → 确认 PASS。后端 **16/16**、前端 **12/12**（`backend-go/scripts/falsify_review9.sh`、`frontend/scripts/falsify_review9.sh`）。

其中两次反证**当场发现测试无效并已修正**：

1. 前端 cursor：原测试把 transient 放在最前面，而那时游标本来就是 0，等价于没测；改为 durable → transient 顺序
2. 前端重放去重：原测试的首次请求是失败的（没插入任何东西），去重分支从未被执行；改为调用方显式持有 id 的重试路径

两次都是「测试写对了断言、但没有让断言真正被执行」。这正是报告要求反证的意义。

---

## 七、变更文件

**新增**：`db/migrations/0021_run_requests.{up,down}.sql`、`0022_provider_submissions.{up,down}.sql`、`0023_run_event_sequence_allocator.{up,down}.sql`、`internal/execution/idempotency.go`、`submission.go`、`internal/execution/idempotency_test.go`、`internal/integrations/aily/submission_policy_test.go`、`tests/integration/review9_*.go`、`frontend/src/stores/__tests__/useRunChatStore.idempotency.test.ts`、两个 falsify 脚本。

**修改**：`db/queries/execution.sql`（+11 查询，event 读取加 LIMIT，去掉 `CountRunEvents` / `AppendRunEventAtSequence` → 合并为唯一写者 `AppendRunEvent`）、`internal/gen/db/*`、`internal/gen/api/api_gen.go`、`api/openapi.yaml`、`internal/execution/{service,finalize,domain,ownership,worker}.go`（`recovery.go` 未改）、`internal/transport/sse/sse.go`、`internal/transport/http/run_handlers.go`、`internal/integrations/aily/{executor,adapter}.go`、`internal/catalog/registry.go`、`internal/platform/telemetry/telemetry.go`、`internal/app/bootstrap.go`、既有集成测试改用新 API（`main_test.go` 新增 fixture helper）、`frontend/src/services/{runApi,runStream}.ts`、`frontend/src/stores/useRunChatStore.ts`。

**注意**：`GetRunByID` / `ListRunsByConversation` 的 SELECT 列表补了 `next_event_sequence`，以保持 sqlc 仍返回 `db.Run`（否则会生成逐查询 Row 类型并改变签名）。

---

## 八、新增指标

`studio_run_idempotency_replay_total` / `studio_run_idempotency_conflict_total` / `studio_provider_submission_unknown_total` / `studio_provider_submission_dedup_total` / `studio_sse_replay_events_total`。

**告警**：`studio_provider_submission_unknown_total > 0` 应作为高价值告警——它意味着发生了"外部副作用结果不确定"的情况。

---

## 九、明确的遗留与风险

1. **at-most-once 的可用性代价**：provider 无幂等能力时，"已提交 `sending` 但结果未知"一律 park。若 provider 实际未收到，该 run 会以 `provider_submit_unknown` 失败而不是重试成功。这是刻意的取舍——重复的 agent 执行可能产生真实外部动作，比一次可见失败更糟。报告 §6 给出的正是这个选择。
2. **`sending` 窗口**：宽度 = 一次 HTTP 往返 + 首帧延迟（streaming 场景首帧才带来 `agent_chat_id`）。accepted 之后不再有风险窗口。
3. **Reconciler 未实现**：`unknown` 的提交目前只能靠 grace 过期收敛。将来若 provider 提供 request-key 查询，`IdempotencyLookup` 类与独立 Reconciler 才有意义——现在声明它只会增加不可测分支。
4. **批次四–七 未动**：SSE Hub（P1-2）、Worker Dispatcher（P1-5）、Conversation lifecycle（P1-6）、前端长对话（P1-7）仍按报告原样开放。
