# Creation Agent Studio — 项目长期记忆

> 只放「跨会话仍成立、违反会复发事故」的**跨切面**代码规则。
> - **本机环境 / 命令 / CI 映射 / flake / 操作红线** → 同目录 `PITFALLS.md`（动手前必读）
> - **冻结子系统（SSE Hub / Worker Dispatcher）的实现约束** → 同目录 `FROZEN.md`
> - 起 dev 环境与验证套路 → skill `cas-dev-verify`；机制与证据 → `docs/`

## 当前状态
- 仓库 `shilin414/potal`，分支 `dev`。执行内核（Ownership/Claim/Reaper/Finalize/ProviderSlot/Lease/Heartbeat/Gate）与第九轮 **FROZEN**，不得顺手改。
- 第十轮 Batch 4 / 4.1 / 4.1.1 / 4.1.2 → **SSE Hub 永久 FROZEN**（4.1.2 为 test-only）；Batch 5 + 5.1 → **Worker Dispatcher FINAL FROZEN**。
- **第十一轮 Aily 附件链路已修复并推送**（`9d00f1a`）：Worker 侧附件上传桥接 + studio/provider id 分离 + P0-6 多图 artifact。**未动 `waiting_external` 机制**。
- **第十二轮 P0-R + P1 已完成**（复审报告 `potal 最新代码复审暨下一轮修改执行报告.md`）：目录架构闭环
  —— 头像指纹缓存 + keyset 分页（`4c8a4e2`/`80d772a`）+ **bootstrap/resolve/mention 三端点 +
  删除整目录镜像**（本轮）。**下一步仍是 Batch 6 — 优先 Aily Workflow Runtime**。
- migration 基线 = **24**（0021 run_requests / 0022 provider_submissions / 0023 next_event_sequence / 0024 容量索引）。Batch 4/5 系列与第十/十一/十二轮**均无 migration**。
- P1 待办（仍未做）：**xlsx/csv 需 potal 本地解析成结构化文本再进 `user_message.content`**，不能伪装成 `type=file`（Aily 直接文件仅支持 png/jpg/pdf）。
- P2 待办（复审报告点名，本轮未做）：`/applications/page` 的 0025 索引 **EXPLAIN 实测**（`kind=all`/`kind<>'chat'` 未必吃得到索引）、
  legacy `/agents/` 的分页（若长期保留）、用户自身头像是否走代理层（先看 DevTools 是否命中 memory/disk cache）、
  DTO 进一步拆 Summary/Detail/AuthoringDetail、CI 静态检查禁止前端出现 `/v2/applications`。

## 改冻结子系统前先读
| 子系统 | 文档 |
|---|---|
| SSE Hub | `docs/potal 第十轮 Batch 4 / 4.1 / 4.1.1 整改变更报告*.md` + `FROZEN.md` |
| Worker Dispatcher | `docs/potal 第十轮 Batch 5 Worker Dispatcher 整改变更报告.md` + `FROZEN.md` |
| 历轮 | `docs/` 按主题命名（含 TiDB→MySQL 5.7 切换报告） |

## 核心硬性约定（违反会复发 P0/事故）
- **第十一轮 Aily 附件链路**（提交 `9d00f1a`）：**Studio ID 与 Provider ID 是两个不可互换的名字空间**
  —— `runtime_attachments.id` 只存在于 potal 内部，`runs.input.studio_attachment_ids` 存前者，
  Aily `agent_attachment_id` 只能由 `POST /agents/:id/attachments` 产出、存在
  `runtime_attachments.external_attachment_id`，**只有它允许进 `user_message.agent_attachment_ids`**
  （系统级 invariant，测试 `TestChatNeverReceivesAStudioAttachmentID` 双模式钉住）。
- **附件上传必须排在 `beginSubmit` 之前**：`beginSubmit` = 「上游可能已产生 chat」的边界，上传是
  Provider IO 且**与 `/chats` 是两个外部动作**，**绝不写 `provider_submissions`**。上传失败 →
  `run.failed(aily_attachment_upload_failed)`，**绝不 `waiting_external`**（那里的「转圈」不是根因）。
  上传可有限重试（5xx/timeout/429，最坏多个孤立附件），chat 提交仍严格 at-most-once。
- **附件写入必须 fenced + set-once**：`MarkAttachmentUploadedFenced` 谓词含 `id AND run_id AND
  external_attachment_id=''`；`ErrAttachmentNotClaimed` ≠ `ErrLostOwnership`（前者可继续、后者必须停）。
- **`JSON_ARRAY_APPEND` 写不存在的路径是静默 no-op**（MySQL 5.7/TiDB 实测原样返回，非 NULL）
  → 往 JSON 列追加前必须先 `JSON_SET` 补 `JSON_ARRAY()`；否则写入无声失败。
- **集成测试造 fixture 要走真实 query，不要用裸 SQL 手工 UPDATE**：本轮就是因为改走真实
  `AppendUserAttachmentToRunInput` 才暴露上面那条静默 no-op（手工 UPDATE 会把它一起屏蔽）。
- **幂等**：`client_request_id` 解析早于授权/限流/附件校验；身份表 `run_requests`，`request_hash = SHA-256(归一化 payload)`；同 key 不同 hash → 409；resolver infra error → 5xx。
- **Provider 提交状态机**：`sending|accepted|rejected|unknown`；**只有 `rejected` 可重发**；`sending`/`unknown` → park `waiting_external`，**绝不 blind retry**；5xx/timeout = 未知，4xx = 明确拒绝。**提交边界 = POST 成功且拿到 external id**；200 无 id → `ErrServer`（Aily → unknown → waiting_external），绝不 failRun；park 后必须 return。
- **accepted identity 持久化是 canonical correctness**：`MarkSubmissionAccepted` 失败必须**停链**（不 poll/reconcile/finalize/重发），只做本地 DB 短重试（50/100/200ms）；sentinel `ErrProviderAcceptancePersistence`。
- **每次 submission 写都是 canonical write**：`MarkSubmissionStateOwned` 事务内 `verifyActiveOwnershipTx` + SQL CAS；四条 query 互斥；「记录结果」与「武装重发」是两条语句。
- **Provider 容量**：有效容量 = `DISTINCT(live slots UNION non-settled sending/unknown/accepted)`，**必须 UNION 不能相加**；排除 `rejected` 与已 settled run；admission **必须 exclude self**。remote leg 必须 active-run 驱动：`runs(active) STRAIGHT_JOIN provider_submissions`，状态过滤写**显式 `IN`**；**`STRAIGHT_JOIN` 是承重的**；不 FORCE INDEX。
- **SSE 协议**：durable 写 `id:<seq>`，transient(seq 0) 绝不写 id；优先级 `query after > Last-Event-ID > 0`；replay 遇 terminal 立即 break；WriteHeader 后必须 Flush；`content.chunk` 只写增量 text+offset；`stream_protocol` 与 `after` 正交、每条连接都发，返回**协商值**（`>=2 → 2`，缺失/乱码/非正/溢出 → 1），**绝不回显**。**发布合同 Backend first / Frontend second。**
- **前端按 UTF-8 字节 offset 对账**：transient delta 与 durable chunk 共用同一坐标、**都带 absolute end offset**；reducer 三分支（drop / 补 suffix / append+跳计数器）；中文 3 字节、emoji 4 字节，**绝不用 `string.length`**；offset missing → legacy append。
- **终态语义**：`interrupted` status ≠ `run.interrupted` event；`IsTerminal()` 只认 `{cancelled,succeeded,failed}`；SQL `status NOT IN (四个)`；migration 新增 `MAX(sequence)` 必须 `COALESCE(MAX(...),0)+1`。
- **时钟/事务**：Clock Authority 无兜底；续约类 UPDATE 必须单调写（`GREATEST(CURRENT_TIMESTAMP(3), DATE_ADD(...,1000 MICROSECOND))`）；MySQL UPDATE 返回 changed rows；merged heartbeat `HeartbeatOwnedWithSlot` BOTH OR NEITHER，Renew 恒 XX-only；`SET timestamp=<sec>` + `MaxOpenConns(1)` 可钉时钟；metrics/Redis fan-out post-commit；attempt 唯一消耗点 `BeginProviderAttemptOwned`；消息落库同事务 `TouchConversationUpdated`。
- **准入/Gate**：唯一门 `catalog.AuthorizeExecution`；普通用户错误一律 404；先判 `err == nil` 再 `executionDenied(err)`；Gate 双检查点 level-triggered；kill = cancel，pause/infra/未知 = Defer fail-closed；Provider 门禁按 `provider_key` fail-closed；Streaming 必须同步 Open。
- **并发**：一 conversation 一个非终态 Run（409）；锁序 `users → conversations`；hard-delete cascade 必须显式带 `run_requests`/`provider_submissions`；`waiting_external` 有界（`ExpireParkedExternalRuns` 用 DB 时钟 − grace）。
- **sequence O(1)**：`runs.next_event_sequence` 锁内 `SELECT FOR UPDATE` → `UPDATE x+1`；**绝不用 `COUNT(*)+1`**；读取一律 LIMIT。
- **前端 store**：异步写用 functional setState；`activeRunId` compare-and-clear；拉取失败 `null` = 未知、不清空；`run.cancelled` 独立终态不得映射 done；`run.deferred` 靠 `run.started` 清除。
- **antd 表单取值（P0 事故）**：拼 payload 一律 `form.getFieldsValue(true)`；**绝不用 `validateFields()` / `getFieldsValue()` 的返回值**（只含已注册 Form.Item 的路径，其他字段被**静默丢弃**）。回归测试 `frontend/src/components/Schedules/__tests__/scheduleEditorPayload.test.tsx`。

## 前端数据层：**单行解析 + 分页 + bootstrap**，禁止整目录下载（第十二轮 2026-09-17 收口）
`useApplicationCatalogStore`（把整张目录镜像到浏览器）**已删除**；`fetchV2Applications` /
`fetchManageableAgents` 也从前端移除。现在的三个数据层与**唯一允许的四个入口**：
- 列表 → `GET /v2/applications/page`（keyset，`useApplicationPage`；q/category 传后端）；
- 起屏事实 → `GET /v2/workspace/bootstrap`（`useWorkspaceBootstrapStore`：默认主智能体 +
  收藏/常用/最近/推荐/常用应用 + 智能体与应用分类导航；**响应体与目录规模无关**）；
- 单行 → `GET /v2/applications/resolve?slug=|id=`（`useApplicationEntityStore`，同一套
  `catalog.VisibleTo`，不可见与不存在都 404）；
- `@` 路由 → `GET /v2/applications/resolve-mention?q=`（`lib/composerRouting`，
  `onRouteSend` 可以返回 Promise）。
- **绝不再让前端碰 `GET /v2/applications`（全量数组）**：小库看不出、大库是事故。
  后端保留该端点只为非 studio 客户端，并用 `studio_legacy_application_list_requests_total` 计量。
- 变更后的本地 patch 三处一起：**page item + entity cache + bootstrap**（AgentsPage 是范例）；
  只有「前一个默认智能体被服务端降级」这种看不见的副作用才需要重新拉一次 bootstrap（几十行）。
- 组件若要只渲染不解析，参数类型用 `ApplicationSummary`（`V2Application` 是它的结构化子类型），
  **不要把 summary 塞进 entity store**（那里只放完整 item）。

## 移动端列表/选择器：分类与「最近使用」绝不从当前页推导（P0-R1，2026-09-17）
`MobileCatalogSheet` 有**两种数据模式**，测试必须分清（旧的 256 个用例全走 local 模式，
所以生产路径的语义错误一个都没抓到）：
- `applications` 传了 → legacy 本地池（**只用于单测**）：`buildMobileCategoryTabs(pool)` +
  `buildRecentItems(pool, recentIds, type)` + `MOBILE_SHEET_MAX_ROWS` 渲染预算；
- `applications` 未传 → **生产** server-paged：分类 tabs = `bootstrap.{agent,app}_categories`
  （服务端算好，含 `__uncategorized__`→「其他」哨兵），最近使用 =
  `mergeRecentItems(本地 recentIds 经实体缓存解析, bootstrap.recent/recent_fixed_apps)`，
  **不受当前分类/搜索影响**；`activeApplication` 只在「`category==='all'` 且无搜索」时注入
  （注入到分类或搜索结果里 = 让 IT 分类出现销售智能体）。
- 空态要分两类：目录真空 → 「暂无可用智能体/请联系管理员配置」；**分类或搜索无结果 → 保留 tab 导航**
  （否则用户被困在那个分类里出不来）。

## 前端列表/选择器：渲染预算仍必须有上界（2026-09-17 事故，遗留约束）
进入某个页面/选择器以后看到的仍是「分页/上限」结果，所以下面这些仍然成立：
- **首屏动画延迟绝不可与下标线性相关**：`animationDelay: index * 50ms` 配
  `animation: fadeIn … both` 时，`both` 会在**整个延迟期间保持 opacity:0**，
  1800 行 → 最后一张要等 ~90 秒，用户看到的就是「空列表」。必须**封顶**（`cardDelay`）。
- **列表必须设渲染上限 + 加载更多**：市场页 `PAGE_SIZE=24`，移动端 sheet
  `MOBILE_SHEET_MAX_ROWS=60`，切换器跳跃菜单 `SWITCHER_MAX_ROWS=50`（并**钉住当前项**，别把它截掉）。
- **上限只能是「渲染预算」，绝不能加到过滤之前**（legacy 本地池模式）：`filtered` 必须在**完整池**上算，
  只有 `rendered` 才切片 —— 否则搜索会静默搜不到第一页之外的项（管理员看不见自己的智能体）。
  **server-paged 模式下搜索/分类由后端做**，`filtered` 就是服务端返回的那一页，不存在这个坑。
- **同一批数据出现在两个 Menu 分组时，key 必须按分组加前缀**（`recent-1` / `all-1`）：
  共用 `String(app.id)` 会触发 React `Duplicated key` 且 antd 的 active-key 追踪会把两行当一行。
- **派生结果放 store，不要在每个消费组件的渲染体里重算**：整目录镜像没了以后，剩下的
  派生点（`ApplicationSummary` 分组、分类导航）都在 `useWorkspaceBootstrapStore` 里由**服务端**
  算好一次；组件里再 `list.filter(...)` 就是每次渲染重分配。改派生字段时**所有写它的方法都要同步维护**
  （`patch` / `remove` / `toggleFavorite` 都算 —— bootstrap store 里这三处是并列的）。
- `applications.default_config` 是「归属智能体的配置」的落点，但**写它必须 FOR UPDATE
  读-改-写**（`MergeSkills`）：该 JSON 列还被 legacy 的 `guided_entry_prompt_key` 共用，
  直接覆盖会把它一起抹掉。

## 本机操作红线（完整版 + 命令见 `PITFALLS.md`）
- **同一文件绝不可在一条消息里发两个 Edit**：并行写同文件 = 后写覆盖前写、静默丢失，且**仍能编译通过**。串行改 + grep 复核那一行。
- **反证驱动绝不可与其它 `go test` 并发**（脚本改真实源码）。
- **不要为对照基线 `git checkout <sha>`**（本机会被 SIGTERM 打断）；用 `git checkout <sha> -- <路径>`。
- **同一工作区可能有并发会话**：开工前 `git status` + 看关键文件 mtime；提交前若混着别人的改动**先问用户**，别 `git add -A`。
- **🔴 不要用 `git rm`；不要把 git 写操作和长任务串在一条命令里；不要用双引号包 `python -c "…"` 写含反引号的内容**
  —— 2026-09-17 一天撞了两次「删工作区 176 文件 + 删 `.git/refs` + 对象库回退一代」，
  起因分别是 `git rm … && npx tsc` 撞 120s 超时、以及 bash 把正文里的反引号当命令执行。
  恢复流程（`git archive` 补文件 / 重建 refs / `git fetch --tags` 取回对象库）见 `PITFALLS.md`。

## 历轮索引
十轮 **4**（单 upstream / 有界 cache / 协议隔离 / register-before-replay / 慢客户端隔离 / terminal 硬边界）→ **4.1**（live gap 修复 / cache 字节硬上界 / 指标代际围栏 / 锁序）→ **4.1.1**（constructor inert / 初始 idle timer 竞态 / canonical 连续性 fail-closed）→ **4.1.2**（test-only）→ **5 / 5.1**（Worker Dispatcher，FROZEN）。
九轮及以前：五轮 93/A- → 六轮 98/A+ → 八轮 P1 关闭 → 九轮（幂等 0021 / 提交状态机 0022 / O(1) sequence 0023 / Streaming Range / 有效容量 + 协议协商，FROZEN）。
