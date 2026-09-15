# potal 第九轮「补丁批次 3.1」变更报告

> 依据复审报告《potal 第九轮整改代码复审报告（以 GitHub 当前代码为准）》逐项整改。
> 基线 `d4eb77b`（第九轮代码提交 `b81ee8f`）。
>
> 复审判定：批次一～三「主体整改通过，存在 3 个 blocker」。本批次把这 3 个 blocker 与
> 4 项工程一致性问题全部关闭，批次四（SSE Hub）可以开始。

---

## 一、结论对照

| 复审项 | 复审判定 | 本批次后 |
|---|---|---|
| P1-HIGH：transient delta + durable chunk 文本重复 | ✕ 端到端未通过 | ✓ 关闭 |
| P1-HIGH：streaming 首帧 `agent_chat_id` 前断流被错误 fail | ✕ P0-2 不能关闭 | ✓ 关闭 |
| P1-HIGH：`MarkSubmissionStateOwned` 无数据库 fencing | ✕ 破坏 fencing invariant | ✓ 关闭 |
| P1：`client_request_id` 并发撞 QPS 误报 429 | △ 基本完成 | ✓ 关闭 |
| P1：两个新表未接入 Conversation hard-delete cascade | 待修 | ✓ 关闭 |
| P2：`IdempotencyNative` 与状态机互相矛盾 | 待修 | ✓ 关闭 |
| P2：REST Event 与 SSE Event 不是同一个 schema | 待修 | ✓ 关闭 |
| P1-DEPLOY：0023 不是 mixed-version-safe migration | 待记录 | ✓ 已写入迁移注释与 README |

---

## 二、P1-HIGH ①：durable chunk 与 transient delta 重叠（复审 §三）

**问题**：后端同一段答案会走两条路 —— transient `content.delta`（Redis，不落库）与
durable `content.chunk`（落库 + 重放）。删除 cumulative `snapshot` 之后，前端
`content.chunk` 分支退化成「无脑 append」，于是 `delta "你" + delta "好" + chunk "你好"`
渲染成 `"你好你好"`。durable cursor 救不了：transient 帧 sequence 恒为 0，本来就不进
cursor，所以 cursor 只能去 durable-vs-durable 的重，去不了 transient-vs-durable 的重。

**修法**（`frontend/src/stores/useRunChatStore.ts`）：

- 新增 `utf8ByteLength` / `utf8SliceFromBytes`：按 **UTF-8 字节**计算，迭代 code point，
  因此中文 3 字节、emoji（代理对）4 字节；`string.length` 计的是 UTF-16 code unit，
  与后端 offset 不是一套单位。
- `ChatMessage` 增加 `streamBytes`（已渲染的 UTF-8 字节数）。历史消息/重播种的气泡没有
  该字段时由 `renderedBytes()` 从 content 推导。
- 新增 `applyIncrementalChunk()`，把 chunk 的 `offset`（**字节 end offset**）与已渲染
  字节数做三分支对账：

  ```
  end <= rendered            整块已通过 transient 显示过 → 丢弃
  start <= rendered < end    部分显示过 → 只 append 缺失的 suffix（不切断字符）
  rendered < start           真实 gap → 保留这块字节，计数器跳到 end
  ```

  gap 分支保留新字节而不是丢弃：丢掉就是真的少一段文本，而终态事件
  （`run.completed` 的 text / `finalizeRun` 的 `run.output.text`）是权威文本，会自愈。
- `run.completed` 与 `finalizeRun` 覆盖文本时同步重算 `streamBytes`。

**测试**：6 个新用例（delta+delta+chunk、纯重放、断线重放、部分重叠 `AB → C → CD → ABCD`、
中文/emoji UTF-8 offset、历史 snapshot 覆盖）。

---

## 三、P1-HIGH ②：streaming 首帧前断流被错误声明失败（复审 §四）

**问题**：`OpenStreamChat` 成功即意味着请求**已跨过提交边界**，但 `agent_chat_id` 要等
第一帧 SSE 才到。这个窗口里断流，`externalRunID == ""`，`reconcile("")` 走
`failRun("aily_no_chat_id")` —— 把一个 Provider 可能仍在执行的 Run 声明为终态失败。

**修法**：

1. `adapter.go` `StreamPrepared`：`aily.stream.started` **只在 `agent_chat_id != ""`
   时才 emit**。没有 id 的帧继续等待后续帧（该帧自己的事件照常投递），否则后面才到达的
   chat id 会永远丢失。
2. `executor.go` `executeStreaming`：新增 `parkIfUnconfirmed(reason) (bool, error)`。
   只要「已提交 && 没有 external id」，无论 transport_error、正常结束、EOF 还是首帧无 id，
   一律：submission → `unknown` → Run → `waiting_external`（**不重试、不 fail**）。
   返回布尔值是关键：park 之后 Run 已结算，必须直接 return，不能再掉进 reconcile。

**测试**：`TestStreamEofBeforeChatIdParksInsteadOfFailing`、`TestFirstFrameWithoutChatIdParks`、
`TestChatIdOnALaterFrameStillResumes`（最后一条锁住「等待 id ≠ 错过 id」）。

---

## 四、P1-HIGH ③：`MarkSubmissionStateOwned` 没有数据库 fencing（复审 §五）

**问题**：原实现只校验 `own.Valid()` —— 那只证明 token 格式合法，**没有去数据库确认这份
lease 现在仍属于当前 worker**。租约过期后被接管的 stale worker 仍可改写
`provider_submissions`；SQL 也没有状态 CAS，理论上可以逆向覆盖。

**修法**：

- `MarkSubmissionStateOwned` 改为与 `MarkSubmissionAcceptedOwned` 同形状：
  `BEGIN → verifyActiveOwnershipTx(epoch + token) → CAS UPDATE → COMMIT`。
- SQL 加 CAS（三张新/改 query 语义互斥、不复用）：

  | query | FROM 状态 | 语义 |
  |---|---|---|
  | `MarkProviderSubmissionState` | `sending` | 记录本次结果（unknown/rejected），**唯一合法前驱是 in-flight** |
  | `ReopenProviderSubmission` | `rejected` | 明确拒绝后才重新武装一次重发 |
  | `ReopenUnknownProviderSubmission` | `unknown` | 仅 native 幂等 Provider 允许的重发 |
  | `MarkProviderSubmissionAccepted` | `sending`/`unknown` | accepted 单调，且 0 行时区分「幂等重入」与「被别人先行结算」 |

  「记录结果」与「武装重发」拆成两条独立语句，是为了让「只有明确拒绝才能重发」这件事
  在代码里看得见，而不是藏在一个通用 `UPDATE state=?` 里。

**测试**：`TestMarkSubmissionStateRequiresLiveOwnershipAndLegalTransition`
（A 被接管后写 ledger → `ErrLostOwnership` 且 DB 不变；B 可写；B 试图把 `unknown` 改回
`rejected` → 被拒）。

---

## 五、P1：并发 replay 撞 QPS 误报 429（复审 §六）

**问题**：A 已通过准入、事务未提交；B（同 `client_request_id`）提前 resolve 看不到，走进
QPS limiter，被 429 拒绝 —— 而它其实是一个**已经成功**的请求的运输层重试。

**修法**：

- `execution.Service.ResolveRunRequestWithWait`：在**有界预算**（默认 400ms / 20ms 轮询）
  内等待身份出现。预算只覆盖「一次 commit」，不覆盖慢请求；A 若回滚，B 依旧拿到 429
  （那才是正确答案）。
- HTTP 层 `serveReplayAfterRefusal`：仅在准入被拒时调用，命中则回 200 + 原 Run
  （`idempotency_replayed: true`），未命中则什么都不写，交回调用方写 429。
  resolver 以闭包注入，所以这条路径可在无数据库下单测。

**测试**：`TestResolveRunRequestWithWaitBridgesAnUncommittedReservation`
（真实未提交事务：单次读必须看不见 → 等待后必须看得见）；
`run_handlers_test.go` 3 条（命中回 200、新请求保持沉默、无 id 不做无谓查询）。

---

## 六、P1：两个新表未接入 hard-delete cascade（复审 §七）

`run_requests` / `provider_submissions` 都没有从 `runs` 出发的 FK 级联。
新增 `DeleteRunRequestsByRun` / `DeleteProviderSubmissionsByRun`，在
`DeleteConversationCascade` 删除 run 的循环里显式清理（保持现有显式级联风格）。

不清理的后果是永久失效的幂等预留：之后客户端拿同一个 `client_request_id` 请求，
`GetRunRequest` 命中、`GetRun(run_id)` 已不存在 → 500。

**测试**：`TestCascadeDeleteRemovesRoundNineChildTables`。

---

## 七、P2：`IdempotencyNative` 与状态机矛盾（复审 §八）

`classifySubmitFailure` 说「native 幂等 Provider 可以重试」，`BeginProviderSubmission`
却拒绝一切 `unknown` —— 两半互相矛盾，那条分支是死代码。

**修法**：把能力传进状态机。新增 `execution.SubmissionResend`
（`ResendForbidden` / `ResendOnUnknownSubmission`），由 `aily.Executor.submissionResendPolicy()`
从 `catalog.SubmitIdempotencyOf(e.Adapter)` **现场推导** —— 与 `classifySubmitFailure`
读的是同一个来源，fail-closed（未实现可选接口者按最弱类处理）。
`unknown + native` 时复用**同一 submission_no 与同一 idempotency key**，attempt +1。

**测试**：`TestNativeIdempotencyMayResendOnTheSameKey`（含「同一个状态在无能力时仍拒绝」
的反向断言）。

---

## 八、P2：统一 Event schema（复审 §九）

`EventRecord.RunID` 的 JSON key 由 `run` 改为 `run_id`，OpenAPI `RunEvent` 同步更新并重新
生成 `api_gen.go`。REST 分页与 SSE 帧现在是同一个 schema，可以直接喂给前端的
`applyEvent()`—— 此前 `fetchRunEventPage` 返回的数据名义上叫「统一事件」，实际上不能。

---

## 九、P1-DEPLOY：0023 升级约束（复审 §十）

写入 `db/migrations/0023_*.up.sql` 注释与 `backend-go/README.md`：

- 旧 worker 用 `COUNT(*)+1` 且不推进 `next_event_sequence` → 新 worker 分配到同一个
  sequence → `UNIQUE(run_id, sequence)` 冲突（1062），后果是该 Run 之后每个事件插入失败，
  不是静默漂移。
- 因此本次升级必须 **drain 全部旧 worker → 迁移 → 全量启动新 worker**；滚动发布/多 Pod
  灰度不适用。

---

## 十、验证

| 项目 | 结果 |
|---|---|
| `gofmt -l` / `go build ./...` / `go vet ./...` | 全清 |
| 后端单测 `go test ./...` | 全绿（15 个包） |
| 后端集成 `STUDIO_TEST_DB=1 STUDIO_TEST_REDIS=1` | 全绿（82s） |
| 前端 `tsc --noEmit` / `vitest run` / `vite build` | 全绿，149 个用例 |
| 反证 `backend-go/scripts/falsify_review9_patch.sh` | 15/15（逐个还原修复 → FAIL → 还原 → PASS） |
| 反证 `backend-go/scripts/falsify_review9.sh`（回归第九轮原 7 项） | 16/16 |
| 反证 `frontend/scripts/falsify_review9.sh` | 14/14（新增第 7 项：忽略 offset 直接 append） |

`falsify_review9_patch.sh` 为本次新增；两个既有脚本的匹配锚点随代码改动同步更新，
并给新脚本加了 `trap` 清理 —— 被中断的「临时还原」不会悄悄变成提交内容。

### 新增测试清单

后端集成：`TestStreamEofBeforeChatIdParksInsteadOfFailing`、`TestFirstFrameWithoutChatIdParks`、
`TestChatIdOnALaterFrameStillResumes`、`TestMarkSubmissionStateRequiresLiveOwnershipAndLegalTransition`、
`TestNativeIdempotencyMayResendOnTheSameKey`、`TestCascadeDeleteRemovesRoundNineChildTables`、
`TestResolveRunRequestWithWaitBridgesAnUncommittedReservation`。
后端单测：`TestServeReplayAfterRefusal{ReturnsTheOriginalRun,StaysSilentForANewRequest,RequiresAClientRequestID}`。
前端：`content.chunk offset reconciliation` 6 例。

---

## 十一、剩余（原计划批次四～七，未在本批次范围内）

```
批次四：SSE Hub 多连接
批次五：Worker Dispatcher（poller → dispatcher + claim 后即 ACK）
批次六：Conversation lifecycle（generation / 异步 purge）、message keyset 分页 + sidebar 冗余字段
批次七：前端长对话（active turn 隔离 / 虚拟列表 / rAF 批处理 / smart auto-scroll）
```
