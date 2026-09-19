# Creation Agent Studio — 项目长期记忆

> 只放「跨会话仍成立、违反会复发事故」的**跨切面**代码规则。
> 本机环境/命令/操作红线 → `PITFALLS.md`；冻结子系统 → `FROZEN.md`；起 dev 环境与验证 → skill `cas-dev-verify`。

## 当前状态（2026-09-19）
- 仓库 `shilin414/potal`，分支 `dev`。执行内核/九轮 FROZEN；SSE Hub（十轮 4.x）、Worker Dispatcher（十轮 5.x）FROZEN。
- **十五轮（Mobile Console 1.1.2）已落地**（基线 1182982 + 11 个功能 commit + 复审修复 942ccd3）：共享 `useAccessPolicyEditor`（代际守卫 + ready 不变量 + save target/generation guard）；`useSchedules` 51 probe/50 display keyset 分页 + `q` 服务端搜索（后端 COLLATE utf8mb4_unicode_ci + likePattern 转义，limit clamp 100≥51 探针成立）+ errorPhase 三相；`useScheduleDetail` 两失败域解耦 + 执行记录分页；resolve 404=unavailable/5xx=transient；router lazy（主包 1777→1563 kB）。**复审铁律：loadingMore/saving 被 bump 作废后必须无条件复位（守卫会楔死）；关闭表面时清空上一目标数据（重开不闪现）**。
- **十四轮（三次复审）已落地三批提交**：dd51bed 后端不变量（默认智能体唯一入口 + Consumable 谓词 + 顺序 ID opaque 404 + DB 错误不伪装 404）→ 0f16a20 前端会话隔离与 Bootstrap 竞态 → eb04e0a include_unbound 语义 + ESLint 约束 + EXPLAIN 实测。
- migration 基线 **26**（0026 清理非法 default，dev 库实测非法数 0）。0025 EXPLAIN 已实测（MySQL 5.7.32），结果见 `docs/potal 三次复审 EXPLAIN 矩阵实测（MySQL 5.7.32）.md`；复跑工具 `cmd/explain-catalog-matrix`。
- 待办：schedule detail 配置域重试按钮；LIKE `ESCAPE '\\'` 与 catalog 统一；openapi q maxLength 未强制；xlsx/csv 解析进 `user_message.content`；legacy `/agents/` 真分页；runs 100k/1m 规模压测（决定是否建 `user_application_usage`）。

## 冻结子系统
SSE Hub：`docs/potal 第十轮 Batch 4*.md`；Worker Dispatcher：`docs/potal 第十轮 Batch 5*.md`（均配 `FROZEN.md`）。

## 核心硬性约定（违反会复发 P0/事故）
- **Aily 附件**：Studio ID 与 Provider ID 两名字空间不可互换。`runtime_attachments.id` 只存内部 id；Aily `agent_attachment_id` 只能由 `POST /agents/:id/attachments` 产出，只有它能进 `user_message.agent_attachment_ids`（测试钉住）。上传排在 `beginSubmit` 之前；上传失败 → `run.failed(aily_attachment_upload_failed)` 绝不 `waiting_external`；写入 fenced + set-once；`JSON_ARRAY_APPEND` 前先 `JSON_SET` 补数组。
- **幂等**：`client_request_id` 解析早于授权/限流；同 key 不同 hash → 409；resolver infra error → 5xx。
- **Provider 提交状态机**：`sending|accepted|rejected|unknown`，只有 `rejected` 可重发；5xx/timeout=park `waiting_external`；提交边界=POST 成功且拿到 external id；`MarkSubmissionAccepted` 失败停链。有效容量 = UNION 不相加；admission exclude self；按 `provider_key` 门禁；不 FORCE INDEX。
- **SSE**：durable 写 `id:<seq>`，transient 绝不写 id；`stream_protocol` 回协商值绝不回显；Backend first / Frontend second。前端按 UTF-8 字节 offset 对账，绝不用 `string.length`。
- **终态/时钟**：`IsTerminal()` 只认 `{cancelled,succeeded,failed}`；续约 UPDATE 单调写；sequence O(1)（锁内 FOR UPDATE +1，绝不用 COUNT）。
- **准入**：唯一门 `catalog.AuthorizeExecution`；普通用户错误一律 404；kill=cancel，未知=Defer fail-closed。
- **前端 store**：functional setState；`activeRunId` compare-and-clear；失败 null=未知不清空。**antd 表单拼 payload 一律 `form.getFieldsValue(true)`**（回归测试 scheduleEditorPayload.test.tsx）。

## 目录访问：可见性 vs 可用性（务必分清）
- `scope`=谁能看见（管理面）；`mode`=能不能真的用（消费面）。
- **消费谓词 = `catalog.Consumable(ConsumptionFacts)`（十四轮取代 Usable）**：`enabled && (kind≠chat || (enabled binding && provider 存在且 active))`，与 `AuthorizeExecution` 同一谓词；page consume/bootstrap 全组/resolve/mention/默认智能体已全部在 SQL 或 Go 里含 provider active（provider_key join，fail closed）。
- **默认智能体唯一入口 `promoteDefaultIfEligibleTx`**：事务内 FOR UPDATE 读最终态，校验 enabled+chat+最新 enabled binding+provider active，失败整体回滚绝不清原 default；Create/Update/SetDefaultAgent 全走它，裸 promoteDefaultTx 已删除。
- **顺序 ID 非披露**：by-id 管理面（detail/PATCH/DELETE/avatar/default-agent/favorite）的 ErrNotFound 与 ErrNotManageable 统一 opaque 404（`writeApplicationAccessError`）；`GET /v2/applications/{id}` 只允许 `canManageCaller`（creator/staff），普通用户读走 resolve/page/bootstrap；Favorite/Unfavorite 有 VisibleTo 检查（fixed 收藏保留）。
- **DB 错误绝不伪装 404**：所有 catalog lookup 区分 ErrNotFound(404) 与基础设施错误(500)，处理器里禁止 `x, _ :=` 二次读取。
- `page` 的 `include_unbound` 只管 chat（management knob），**fixed 消费不再依赖它**（P1-R4）；avatar 检查顺序：认证→可见性→ETag/304。

## 前端数据层 / Session 隔离
- 唯一四入口：`/v2/applications/page`、`/v2/workspace/bootstrap`、`/v2/applications/resolve`、`resolve-mention`；authoring detail 走 `runApi.fetchApplicationDetail`。**所有 applications URL 只能写在 services/runApi.ts**（ESLint no-restricted-syntax 钉住，services 与 __tests__ 豁免）。
- bootstrap store：`generation`（clear 防跨用户回填）+ `invalidationRevision`（旧响应落在 invalidate 之后不得清 dirty）+ inflight 按 request identity 清理 + `load(true)` 先 drain 再重拉；收藏按 per-app `favoriteVersions` 收敛，失败只恢复 target flag + invalidate（绝不做整快照 rollback）。
- 登录路径必须走 `acceptAuthenticatedUser`；`resetSessionScopedState()` 注册表模式（resetSessionState.ts 不 import store，防循环）；同时清 `SESSION_SCOPED_STORAGE_KEYS`（workspace/organization-storage，theme 保留）；OrganizationStore 已注册 reset（axios 每请求从 localStorage 读 `organization-storage` 发 X-Organization-ID）。`isSameUser` 用 String 比较 id。
- bootstrap/entity `clear()` 必须递增 generation。变更后三处写入：page item + entity cache + bootstrap；无法客户端重算的成员资格 → `invalidate()`。
- 渲染上限：市场 24、移动 sheet 60、切换器 50、legacy 本地 24；动画延迟封顶。收藏只属于 chat 分组。

## 本机操作红线（完整版见 `PITFALLS.md`）
- 同一文件绝不可一条消息发两个 Edit（后写覆盖前写）。
- 🔴 禁 `git rm`/`git stash`；git 写操作不与长任务串联；判基线用 `git worktree add <dir> <sha> --detach`。
- Bash coreutils 常缺失（ls/head/cat/grep/mkdir/rm）→ 用 Glob/Grep 工具或 PowerShell；PowerShell 中文用户名路径会让 `go run` 的临时 exe 找不到 → bash 下 `go build -o x.exe && ./x.exe`。
- push/fetch 后必须手补 `refs/remotes/<remote>/<branch>`（loose ref + packed-refs + origin/HEAD 三件套，**PowerShell 写入会带 CRLF 导致 "broken name" 警告，必须 LF**），并用 `git ls-remote` 验证远程真实落地，勿信本地 status。

## 历轮索引
十轮 4/4.1（SSE Hub）+ 5/5.1（Worker Dispatcher）→ 十一轮 Aily 附件 → 十二轮目录架构（keyset 分页/bootstrap）→ 十三轮二次复审（访问边界/Session 隔离/Bootstrap SQL 化/DTO 拆分）→ **十四轮三次复审（不变量收口/竞态/EXPLAIN 实测）**。九轮及以前：幂等 0021 / 提交状态机 0022 / O(1) sequence 0023 / 有效容量 0024。

## 代码生成器
`oapi-codegen@v2.5.0`、`sqlc@v1.30.0` 在 `$(go env GOPATH)/bin`（本机无 make，直接调二进制）；改 openapi.yaml/db/queries 后必须重新生成（生成代码是提交的，embedded-spec 含 yaml 全文）。sqlc 坑：`NOT sqlc.arg('x')` 不生成参数，用 `= 0`；同查询不同表的同名列必须用不同 arg 名。
