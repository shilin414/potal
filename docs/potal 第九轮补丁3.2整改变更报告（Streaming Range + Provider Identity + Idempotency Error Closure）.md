# potal 第九轮补丁 3.2 整改变更报告（Streaming Range + Provider Identity + Idempotency Error Closure）

> 对应复审报告：《potal 第九轮补丁3.1最新代码复审暨3.2修改执行报告》（2026-09）
> 基线：`347c7ce`（3.1 补丁 + 记忆提交）。本批次按该报告 §十五 的 Batch 3.2-A/B/C/D 四个子批次执行，不触碰已冻结的 Lease / Heartbeat / ProviderSlot / Gate / Retry·Defer / terminal transaction。

---

## 一、Batch 3.2-A：Streaming Range Protocol（transient delta 携带 byte offset）

**修复问题**（复审 §二）：SSE 网关 subscribe → replay → drain 的架构天然产生「chunk 先到、buffered delta 后到」的线序，原 delta 分支无条件 append 导致 `ABCABC` 重复。

### 后端（`internal/integrations/aily/executor.go`）
- `deltaCoalescer` 新增 `totalReceived`（绝对 UTF-8 字节计数器）；`add(text)` 改为返回 `(endOffset, shouldFlush)`。
- `executeStreaming` 的 `EventContentDelta` 分支：先 clone payload 再写入 `offset = endOffset`，transient delta 与 durable chunk 使用**同一套 UTF-8 字节坐标系**（Go `len(string)` 即字节数）。

### 前端（`frontend/src/stores/useRunChatStore.ts`）
- `applyIncrementalChunk` 提升为 `applyIncrementalRange`，`content.delta`（带 offset 时）与 `content.chunk` 共用同一 byte-range 三分支对账：`end<=rendered→drop` / `start<=rendered<end→补 suffix` / `gap→append+跳计数器`。
- **legacy 兼容**：不带 offset 的旧 delta（pre-3.2 后端）保留原 append 行为，前后端不要求同步切换。

### 测试
- 后端单测 `TestDeltaCoalescerSharesOneByteCoordinateSystem`：A/中/🚀 → transient offset 1/4/8，durable final chunk offset 8，证明同一坐标系（复审 §四后端测试）。
- 前端新增 5 个用例（复审 §四 Test 1–4 + legacy delta 兼容）：reverse overlap、partial overlap、中文/emoji、delta→chunk→late delta、无 offset legacy。

---

## 二、Batch 3.2-B：Provider External Identity Closure

**修复问题**（复审 §五/§六）：① background `StartChat` 返回 200 但无 `agent_chat_id` 时直接进入 poll("")；② `MarkSubmissionAccepted` 写库失败被 Warn 吞掉后继续执行，破坏 accepted identity 账本不变量。

### Client 层（`internal/integrations/aily/client.go`）
- `StartChat` 严格校验协议：malformed JSON / 空 `agent_chat_id` → `&APIError{Kind: ErrServer, HTTPStatus: 200}`。Aily = IdempotencyNone → unknown → `waiting_external`；未来 native 幂等 Provider 自动走 same-key 安全重试，与提交状态机完全一致。

### Executor 层（`internal/integrations/aily/executor.go`）
- **defense-in-depth**：`SubmitPrepared` 成功但 `ExternalRunID == ""` → 构造 `ErrServer` APIError 走 `onSubmitFailure`（park），绝不进入 poll。
- **拆分 `bindThreadAndRun`**（消除错误语义模糊）：
  - `persistProviderAcceptance`（canonical correctness）：只做 `MarkSubmissionAccepted`，失败时本地 DB 短重试 50/100/200ms（共 4 次尝试，每次校验 ownership），仍失败 → 返回 sentinel，**立即停止执行链**（不 poll / 不 reconcile / 不 finalize / 不二次 submit）。sentinel `ErrProviderAcceptancePersistence` 在 `classifyError` 中**原样上抛**：run 保持 running，lease 过期 → Reaper 接管 → 新 owner 看到 `sending` submission → park `waiting_external`。
  - `bindProviderSessionBestEffort`（conversation optimization）：失败只 Warn（`ErrLostOwnership` 仍上抛以保持 fencing 语义）。
- streaming 与 background 两条路径统一使用上述语义。

### 测试（`tests/integration/review9_patch32_test.go`）
- `TestBackgroundSubmitSuccessWithoutChatIDParks`：StartChat=1、poll=0、状态 `waiting_external`、submission `unknown`、无 retrying/failed 事件。
- `TestBackgroundAcceptedPersistenceFailureStopsExecution`：真实 InnoDB 1205 注入（holder 事务锁 runs 行 + `innodb_lock_wait_timeout=1` 的 tuned session）→ StartChat=1、poll=0、状态仍 running、submission 仍 `sending`；随后 expire lease → recover → reclaim → 新 owner park 为 `waiting_external` 且不接触 Provider。
- `TestStreamingAcceptedPersistenceFailureStopsTheStream`：同一注入 → 流消费立即停止、OpenStreamChat=1、poll=0、状态 running、不 finalize、不重试。

---

## 三、Batch 3.2-C：Idempotency Replay-Before-Error

**修复问题**（复审 §九/§十）：bounded replay 只挂在 QPS 429 一条路径；且 `serveReplayAfterRefusal` 把 `ErrIdempotencyKeyReused` 与 DB error 吞掉伪装成 429。

### 改动（`internal/transport/http/run_handlers.go`）
- `serveReplayAfterRefusal` 升级为统一的 **`tryServeIdempotentReplay`**，完整处理四种结局：
  - `found` → 200 原始 run（`idempotency_replayed=true`）
  - `ErrIdempotencyKeyReused` → **409** `idempotency_key_reused`（conflict metric）
  - 其他 resolver error → **500**（基础设施故障不再伪装限流）
  - bounded wait 后仍未出现 → false，调用方写原业务错误
- 挂载到 initial miss 之后的全部竞争出口：**AuthorizeExecution 失败 / admitUserRun 失败 / conversation 校验失败 / attachment 校验失败**（Basic JSON/必填字段校验不做——此时请求身份尚未形成）。

### 测试
- 单测（`run_handlers_test.go`，无 DB）：conflict → 409（12.1）、resolver error → 500（12.2）、not found → 静默、空 id → 不查询。
- 集成测试（真实事务，`review9_patch32_test.go`）：
  - `TestReplayAfterAttachmentClaimRaceServesTheOriginalRun`（12.3）：winner 的事务已提交 attachment claim，reservation 以 uncommitted 重现 → 初始 resolve miss → attachment 校验真实拒绝 → bounded wait 在 winner commit 后返回原 run。
  - `TestReplayAfterAuthorizationRaceServesTheOriginalRun`（12.4）：app 在 winner 提交途中被 disable → 授权真实拒绝 → wait resolve 返回原 run。

---

## 四、Batch 3.2-D：OpenAPI Contract Cleanup（P2）

`backend-go/api/openapi.yaml`：
- 顶部 SSE 总说明改为 durable cursor 语义：`query after > Last-Event-ID > 0`；transient（sequence 0）不写 `id:` 不推进 cursor；terminal 集合为 `run.completed / run.failed / run.cancelled`；`run.interrupted` 标注为 legacy 历史兼容。
- `RunEvent.event_type` enum 补上缺失的 `run.cancelled`（此前枚举与实际协议不一致）。

---

## 五、验证与反证

### 常规验证
- Backend：`gofmt -l`（干净）/ `go vet ./...` / `go build ./...` / `go test ./internal/... ./tests/...` 全绿。
- Frontend：`npx tsc --noEmit` / `npx vitest run`（17 文件 154 用例）/ `npx vite build` 全绿。
- 集成（STUDIO_TEST_DB=1 / STUDIO_TEST_REDIS=1）：3.2 新增 5 个集成测试全绿。

### 反证（报告 §十七 要求的 3 个 mutation）
| Mutation | 预期 | 实测 |
|---|---|---|
| 1. 前端去掉 transient delta offset（delta 恢复无条件 append） | reverse-overlap 测试 FAIL | FAIL ✓（legacy delta 用例同时保持 PASS ✓） |
| 2. 恢复 background 空 chat-id 直接 poll | provider-boundary 测试 FAIL | FAIL ✓ |
| 3. 恢复 accepted-persistence log-and-continue | acceptance durability 测试 FAIL | FAIL ✓ |

- 新脚本：`backend-go/scripts/falsify_review9_patch32.sh`（4/4 ok）、`frontend/scripts/falsify_review9_patch32.sh`（3/3 ok），均带 `trap cleanup EXIT`。
- 旧 3.1 反证脚本回归：`falsify_review9_patch.sh`、`falsify_review9.sh`（mutation 7 锚点已随 3.2-A 代码结构更新）、`frontend/scripts/falsify_review9.sh`（mutation 7 锚点 `applyIncrementalChunk→applyIncrementalRange` 更新）全部通过。
- 跑后双查：`grep -rn FALSIFICATION`（仅脚本自身）+ `find -name '*.orig'`（空）。

---

## 六、影响面与不变量

- **未触碰**：Lease / Heartbeat / ProviderSlot / Gate / Retry·Defer / terminal transaction / migration（基线仍为 23）。
- 新不变量：
  1. **提交边界 = POST 成功且拿到 external id**：HTTP 200 但无 id / body 不可解析 ≡ 未确认（unknown → waiting_external），background 与 streaming 完全一致。
  2. **accepted identity 持久化是 canonical correctness**：写失败必须停链，由 lease/reaper 收敛，绝不 failRun / retry / re-submit。
  3. **transient delta 与 durable chunk 共享同一 UTF-8 字节 end offset**：网关的 subscribe→replay→drain 线序不再产生重复文本，到达顺序无关。
  4. **client_request_id 的并发 loser 在任何 post-miss 拒绝出口都先做 bounded replay**：winner commit → 200；同 key 不同 payload → 恒 409；resolver 故障 → 5xx；真正新请求 → 原错误。

## 七、后续

3.2 关闭后，第九轮批次一–三可正式冻结。下一步按原计划进入**批次四：SSE Hub**（共享 fan-out），把本批次固化的 transient/durable overlap 语义作为 Hub 的输入契约。
