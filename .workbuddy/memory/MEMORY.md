# Creation Agent Studio — 项目长期记忆

> 只放「跨会话仍成立、违反会复发事故」的**跨切面**代码规则。
> - 本机环境 / 命令 / CI 映射 / flake / 操作红线 → `PITFALLS.md`（动手前必读）
> - 冻结子系统（SSE Hub / Worker Dispatcher）→ `FROZEN.md`
> - 起 dev 环境与验证套路 → skill `cas-dev-verify`；机制与证据 → `docs/`

## 当前状态（2026-09-18）
- 仓库 `shilin414/potal`，分支 `dev`。执行内核与第九轮 **FROZEN**；SSE Hub（十轮 Batch 4.x）与 Worker Dispatcher（Batch 5/5.1）**FROZEN**。
- 十二轮目录架构闭环（`4c8a4e2`→`7e50567`）：keyset 分页 + 头像指纹缓存 + bootstrap/resolve/mention。
- **十三轮（二次复审）已落地**：访问边界 + Session 隔离 + Bootstrap SQL 化 + Client Cache 一致性 + DTO 拆分。
- migration 基线 **24**（0025 索引属十二轮）。十~十三轮无迁移。
- 待办：xlsx/csv 本地解析进 `user_message.content`；**0025 索引 EXPLAIN 实测（需真实 DB，未完成）**；legacy `/agents/` 真分页（已加渲染预算，真迁移是 Legacy Agent → Application）；自身头像是否走代理层。

## 冻结子系统文档
| 子系统 | 文档 |
|---|---|
| SSE Hub | `docs/potal 第十轮 Batch 4 / 4.1 / 4.1.1 整改变更报告*.md` + `FROZEN.md` |
| Worker Dispatcher | `docs/potal 第十轮 Batch 5 Worker Dispatcher 整改变更报告.md` + `FROZEN.md` |

## 核心硬性约定（违反会复发 P0/事故）
- **Aily 附件**：Studio ID 与 Provider ID 是**两个不可互换名字空间**。`runtime_attachments.id` 只存 potal 内部 → `runs.input.studio_attachment_ids`；Aily `agent_attachment_id` 只能由 `POST /agents/:id/attachments` 产出（存 `external_attachment_id`），**只有它能进 `user_message.agent_attachment_ids`**（`TestChatNeverReceivesAStudioAttachmentID` 钉住）。
- **上传必须排在 `beginSubmit` 之前**（该边界后上游可能已产生 chat）；上传是 Provider IO，**绝不写 `provider_submissions`**；上传失败 → `run.failed(aily_attachment_upload_failed)`，**绝不 `waiting_external`**；chat 提交严格 at-most-once。
- **附件写入 fenced + set-once**：谓词含 `id AND run_id AND external_attachment_id=''`；`ErrAttachmentNotClaimed` ≠ `ErrLostOwnership`。
- **`JSON_ARRAY_APPEND` 写不存在的路径是静默 no-op** → 追加前先 `JSON_SET` 补 `JSON_ARRAY()`；集成测试 fixture 走真实 query，不要裸 SQL 手工 UPDATE。
- **幂等**：`client_request_id` 解析早于授权/限流/附件校验；`request_hash = SHA-256(归一化 payload)`；同 key 不同 hash → 409；resolver infra error → 5xx。
- **Provider 提交状态机**：`sending|accepted|rejected|unknown`；**只有 `rejected` 可重发**；5xx/timeout = 未知 → park `waiting_external`。**提交边界 = POST 成功且拿到 external id**；200 无 id → unknown，绝不 failRun；park 后必须 return。`MarkSubmissionAccepted` 失败必须停链（短重试 50/100/200ms）。每次 submission 写都是 canonical write（`verifyActiveOwnershipTx` + SQL CAS）。
- **Provider 容量**：有效容量 = `DISTINCT(live slots UNION non-settled sending/unknown/accepted)`，**必须 UNION 不能相加**；admission exclude self；remote leg 用 `runs(active) STRAIGHT_JOIN provider_submissions`，状态过滤写显式 `IN`；不 FORCE INDEX。
- **SSE 协议**：durable 写 `id:<seq>`，transient(seq 0) 绝不写 id；优先级 `query after > Last-Event-ID > 0`；replay 遇 terminal 立即 break；WriteHeader 后必须 Flush；`content.chunk` 只写增量 text+offset；`stream_protocol` 每条连接都发且返回**协商值**（`>=2 → 2`，否则 1），**绝不回显**。发布顺序 **Backend first / Frontend second**。
- **前端按 UTF-8 字节 offset 对账**：transient delta 与 durable chunk 共用同一坐标、**都带 absolute end offset**；三分支（drop / 补 suffix / append+跳计数器）；中文 3 字节、emoji 4 字节，**绝不用 `string.length`**。
- **终态语义**：`interrupted` status ≠ `run.interrupted` event；`IsTerminal()` 只认 `{cancelled,succeeded,failed}`；migration 新增 `MAX(sequence)` 必须 `COALESCE(MAX(...),0)+1`。
- **时钟/事务**：续约 UPDATE 必须单调写（`GREATEST(CURRENT_TIMESTAMP(3), DATE_ADD(...,1000 MICROSECOND))`）；merged heartbeat BOTH OR NEITHER；metrics/Redis fan-out post-commit；attempt 唯一消耗点 `BeginProviderAttemptOwned`；消息落库同事务 `TouchConversationUpdated`。
- **准入/Gate**：唯一门 `catalog.AuthorizeExecution`；普通用户错误一律 404；先判 `err == nil` 再 `executionDenied(err)`；kill = cancel，pause/infra/未知 = Defer fail-closed；Provider 门禁按 `provider_key`；Streaming 必须同步 Open。
- **并发**：一 conversation 一个非终态 Run（409）；锁序 `users → conversations`；hard-delete cascade 必须显式带 `run_requests`/`provider_submissions`。
- **sequence O(1)**：`runs.next_event_sequence` 锁内 `SELECT FOR UPDATE` → `UPDATE x+1`；**绝不用 `COUNT(*)+1`**。
- **前端 store**：异步写用 functional setState；`activeRunId` compare-and-clear；拉取失败 `null` = 未知、不清空；`run.cancelled` 不映射 done。
- **antd 表单取值（P0 事故）**：拼 payload 一律 `form.getFieldsValue(true)`；**绝不用 `validateFields()` / `getFieldsValue()` 返回值**。回归测试 `frontend/src/components/Schedules/__tests__/scheduleEditorPayload.test.tsx`。

## 目录访问：可见性 vs 可用性（十三轮 P0-3/P0-4/P0-5，务必分清）
- **`scope` = 谁能看见**（管理面：staff 也能看 disabled/private/unbound 以便修复）。
- **`mode` = 能不能真的用**（消费面）：`catalog.Usable` = `Enabled && (kind != 'chat' || 有 enabled binding)`，与 `AuthorizeExecution` **同一谓词**。
  - `page` 有 `?mode=consume`（默认 manage，保持管理页兼容）；**resolve / mention / bootstrap 分组恒为 consume**。
  - 切换器 / 移动端目录用 consume；智能体市场 / 应用中心用 manage。
- **`GET /v2/applications/{id}`（authoring）与 `.../avatar` 都要求认证 + `VisibleTo`，不通过一律 404（绝不 403）** —— id 是顺序的，可区分的 403 就是存在性探测。avatar 已从 `publicRoutes` 移除；**检查顺序：认证 → 可见性 → ETag/304**（否则浏览器缓存会继续显示已失去权限的图）。
- **默认智能体（P0-6）**：`SetDefaultAgent` 要求 Enabled；`Update(enabled=false)` 事务内同时清 `is_default_agent`；bootstrap default 由 SQL 保证 `enabled=1 AND bound`。

## 前端数据层：单行解析 + 分页 + bootstrap（禁止整目录下载）
`useApplicationCatalogStore` 已删除。唯一允许的**四个入口**（全部在 `/v2` 下 —— 十二轮曾把 bootstrap 写成 `/workspace/bootstrap` 导致 404，现由 `runApi.contract.test.ts` 钉住路径）：
- 列表 → `GET /v2/applications/page`（keyset）；起屏事实 → `GET /v2/workspace/bootstrap`；
- 单行 → `GET /v2/applications/resolve?slug=|id=`；`@` → `GET /v2/applications/resolve-mention?q=`。
- **绝不再碰 `GET /v2/applications`（全量数组）**，后端用 `studio_legacy_application_list_requests_total` 计量。
- **Bootstrap 成本恒定（P1-1）**：6 条排序查询 + 1 次 `IN(...)` 取行 + 1 次收藏查询 + 2 条分类 GROUP BY；**禁止把 5000 行拉进 Go 再切**。分组预算 `catalog.BootstrapGroupLimit`（SQL LIMIT 即唯一定义）。
- **变更后的三处写入**：page item + entity cache + bootstrap（`AppsPage.applyApplicationMutation` / AgentsPage 是范例）。成员资格无法客户端重算时 → `bootstrap.invalidate()`（dirty 标记，**不是**每次发消息都重拉）。
- **收藏只属于 chat**：fixed 应用只翻 `is_favorite`，绝不进「收藏智能体」（P1-5）。
- **`V2Application` 不含 `external_resource_id/identity_mode/execution_mode`**（P2-1，provider 资源 id 只出现在 authoring 的 `ManagedAgent`/`RuntimeAgentDetail`）。

## Session 隔离（十三轮 P0-2）
- 所有登录路径（login/adminLogin/register/completeSso/completeFeishuLogin）**必须**走 `useAuthStore.acceptAuthenticatedUser(user)`；登出/401 走 `clearAuth()`。
- `resetSessionScopedState()` 用**注册表**实现（`registerSessionReset`），**resetSessionState.ts 不 import 任何 store** —— 否则 `axios → useAuthStore → resetSessionState → useConversationStore → axios` 成环，Vitest 下会静默破坏 store 的依赖图（实测 useRunChatStore 的 mock createRun 不执行）。
- bootstrap / entity store 的 `clear()` 必须**递增 generation**，请求回来后比对：`clear()` 本身拦不住已在飞行中的 Promise 写入上一个用户的数据。
- `isSameUser` 用 String 比较 id（admin 登录给 number，飞书给 string），否则同一用户重登会清掉自己的草稿。

## 前端列表/选择器：渲染预算必须有上界
- **首屏动画延迟绝不可与下标线性相关**：`animation: fadeIn … both` 在**整个延迟期保持 opacity:0** → 必须封顶（`cardDelay`）。
- 渲染上限：市场 24、移动端 sheet 60、切换器 50（钉住当前项）、legacy 本地智能体 24。
- **上限只能是「渲染预算」，绝不能加到过滤之前**；同一批数据出现在两个 Menu 分组时 key 必须加前缀。
- **派生结果放 store**，改派生字段时所有写它的方法都要同步维护。
- `applications.default_config` 写必须 FOR UPDATE 读-改-写（`MergeSkills`），该列还被 legacy `guided_entry_prompt_key` 共用。

## 移动端列表/选择器（P0-R1）
`MobileCatalogSheet` 两种模式：传 `applications` = legacy 本地池（**只用于单测**）；未传 = 生产 server-paged（tabs 取 `bootstrap.{agent,app}_categories`，最近使用取 bootstrap，**不受当前分类/搜索影响**）。空态分两类：目录真空 vs 分类/搜索无结果（后者**保留 tab 导航**）。

## 本机操作红线（完整版见 `PITFALLS.md`）
- **同一文件绝不可在一条消息里发两个 Edit**（后写覆盖前写、静默丢失且仍能编译）。串行改 + grep 复核。
- **🔴 不要用 `git rm`；不要把 git 写操作和长任务串在一条命令里；不要用双引号包 `python -c "…"` 写含反引号的内容** —— 09-17 撞过两次「删工作区 176 文件 + 删 `.git/refs` + 对象库回退一代」。**绝不用 `git stash`**（会删对象库）。
- **不要 `git checkout <sha>`**（会被 SIGTERM 打断）；用 `git checkout <sha> -- <路径>`。判基线可用 `git worktree add <dir> HEAD --detach` + Junction 链 `node_modules`。
- **Bash 的 coreutils 常不可用**（`ls/head/cat/dirname/grep` not found）→ 用 Glob/Grep 工具或 PowerShell。
- push/fetch 后必须手补 `refs/remotes/<remote>/<branch>`（本机 git 写双层 ref 静默失败），否则 `git status` 误报 ahead/gone。

## 历轮索引
十轮 **4/4.1/4.1.1/4.1.2**（SSE Hub，FROZEN）→ **5/5.1**（Worker Dispatcher，FROZEN）→ 十一轮 Aily 附件 → 十二轮目录架构 → **十三轮二次复审（访问边界 + Session 隔离 + Bootstrap SQL 化 + 缓存一致性 + DTO 拆分）**。
九轮及以前：五轮 93/A- → 六轮 98/A+ → 八轮 P1 关闭 → 九轮（幂等 0021 / 提交状态机 0022 / O(1) sequence 0023 / Streaming Range / 有效容量 + 协议协商）。

## 代码生成器（本机已装）
`oapi-codegen@v2.5.0` 与 `sqlc@v1.30.0` 在 `$(go env GOPATH)/bin`；改 `api/openapi.yaml` / `db/queries/*.sql` 后必须 `make gen-api` / `make gen-db`（生成代码是提交的）。
**sqlc 坑**：`sqlc.arg('x')` 写成 `NOT sqlc.arg('x')` 时参数**不会**生成到 Params 结构体，要用 `sqlc.arg('x') = 0`；同一查询里 `runs.user_id`(sql.NullInt64) 与 `application_favorites.user_id`(uint64) 必须用**不同的 arg 名**，否则报 "incompatible types"。
