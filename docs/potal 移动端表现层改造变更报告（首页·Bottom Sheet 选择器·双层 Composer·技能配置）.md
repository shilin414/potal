# potal 移动端表现层改造变更报告（首页 · Bottom Sheet 选择器 · 双层 Composer · 技能配置）

> 项目：`shilin414/potal`（`dev` 分支）
> 依据：《potal 移动端页面设计报告》《potal 移动端改版详细开发执行文档》（2026-09-17 版，基线 `6a92cf3`）
> 范围：**P0–P2（移动端表现层）** + 技能配置的最小后端管道
> 原则：**不重写 WorkspaceHost / 路由 / 会话模型 / SSE 协议 / 桌面端**；移动端只换表现与交互。

---

## 0. 与设计文档的一处重要偏离（用户决策，已确认）

设计文档 §18 与执行文档 §12–§17 建议把技能做成 **provider 动态能力**
（`GET /api/v2/applications/{id}/skills` + `POST /runs` 的 `skill_refs` + 改 `RunRequestHash`）。

**本轮按用户的既有模型实施，未采纳该方案。** 用户明确：

> 技能应该在智能体配置的时候加一个技能配置项，因为技能是归属智能体的；这边的技能其实就是填充指定的提示词，
> 比如技能「查询收入数据」就是在用户输入前面加 `/查询收入查询`，具体智能体怎么识别，那是 Aily 自定义智能体那边要做的事。

因此本轮的实际语义是：

| | 设计文档原方案 | 本轮实施 |
|---|---|---|
| 技能归属 | Provider / Runtime 能力 | **Application（智能体）自身配置** |
| 传输方式 | `skill_refs` 数组，独立字段 | **前置进 `content` 文本** |
| 幂等影响 | 需把 `skill_refs` 加进 `RunRequestHash` | **零改动**：前缀已进 `content`，现 hash 天然覆盖 |
| 需要新表/迁移 | 建议不建表，写入 `Run.Input` | **不写 Run.Input**；落在 `applications.default_config.skills`，**无 migration** |
| Provider 适配器 | 需 `ValidateSkillRefs` / 不支持则 400 | **不需要**，provider 只看到普通消息 |
| 执行文档 P3（Run v2 契约） | 必做 | **本轮明确不做**（用户选择「只做移动端表现层 P0–P2」） |

副作用（正面）：`RunRequestHash`、`POST /api/v2/runs`、`CreateRunInput`、Adapter 契约、SSE 协议
**全部未改动**，所以第九轮冻结的幂等/提交状态机行为不受影响（`go test ./...` 全绿可证）。

---

## 1. 交付物

### 1.1 前端 · 新增

| 文件 | 职责 |
|---|---|
| `src/lib/mobileCatalog.ts` | 纯函数：`splitMobileCatalog` / `buildRecentItems` / `buildMobileCategories` / `buildMobileCategoryTabs` / `filterMobileCatalog` |
| `src/lib/agentSkills.ts` | 纯函数：技能选择解析、前置文本合成、徽标文案、目录序不变式 |
| `src/lib/applicationRoute.ts` | **唯一的** Application → URL 映射 |
| `src/components/Mobile/MobileCatalogSheet.tsx` | 智能体 / 应用共用 Bottom Sheet（最近 3 + 分类 + 搜索 + ✓/主 + skeleton） |
| `src/components/Mobile/MobileHomeSurface.tsx` | 移动端首页（头像 + Hero + 智能体/应用双入口） |
| `src/components/Mobile/MobileAgentSwitcher.tsx` | 移动端顶栏智能体入口（改走 Sheet） |
| `src/components/Mobile/MobileComposer.tsx` | 双层 Composer |
| `src/components/Mobile/MobileSkillSheet.tsx` | 技能多选面板 |
| `src/components/Mobile/MobileAttachmentSheet.tsx` | `+` 菜单（只有「图片 / 文件」） |
| `src/components/Mobile/MobileSheets.css`·`MobileComposer.css`·`MobileSkillSheet.css`·`MobileAttachmentSheet.css`·`MobileHomeSurface.css`·`MobileAgentSwitcher.css` | 样式 |
| `src/components/Mobile/index.ts` | barrel |

### 1.2 前端 · 修改

| 文件 | 改动 |
|---|---|
| `components/Workspace/HomeWorkspace.tsx` | 按 `useIsMobile()` 分支：移动端 `emptyState` 换成 `MobileHomeSurface`，**桌面 `HomeShortcuts` 原样保留** |
| `shell/MobileAppShell.tsx` | `<ApplicationSwitcher compact />` → `<MobileAgentSwitcher />` |
| `components/Chat/RunChatPanel.tsx` | 移动端分支渲染 `MobileComposer` + 两个 Sheet；发送时合成技能前缀；隐藏 file input 提为两壳共用 |
| `stores/useWorkspaceStore.ts` | 新增每应用 `selectedSkillIds` + `setSelectedSkillIds`；`startNewConversation` 清空 |
| `services/runApi.ts` | 新增 `AgentSkill` / `AgentSkillInput`；`V2Application.skills`；`AgentApplicationPayload.skills` |
| `components/Agents/AgentEditorModal.tsx` | 智能体配置新增「技能配置」Form.List |
| `pages/Agents/AgentsPage.css` | 技能行栅格 |

### 1.3 后端 · 新增 / 修改

| 文件 | 改动 |
|---|---|
| `internal/catalog/skills.go` | **新增**：`Skill` 类型、`NormalizeSkills`（纯函数校验）、`ParseSkills`、`MergeSkills` |
| `internal/catalog/{domain,repo,service}.go` | `Application.Skills`；`appFromDetail` 投影；`CreateInput.Skills`；`Update(..., skills *[]Skill)`；`applySkillsTx` |
| `internal/transport/http/application_handlers.go` | 列表项与详情回包新增 `skills`（恒为数组）；Create/Update 接受 `skills`；`ErrInvalidSkill` → 400 字段错误 |
| `db/queries/catalog.sql` | 新增 `GetApplicationDefaultConfigForUpdate` + `UpdateApplicationDefaultConfig` |
| `internal/gen/db/*`、`internal/gen/api/api_gen.go` | `sqlc generate` / `oapi-codegen` 重生成 |
| `api/openapi.yaml` | 新增 `ApplicationSkill` schema；四处 payload 增加 `skills` |

### 1.4 测试

| 文件 | 用例数 |
|---|---|
| `frontend/src/lib/__tests__/mobileCatalog.test.ts` | 20 |
| `frontend/src/lib/__tests__/agentSkills.test.ts` | 21（含 @mention 顺序回归） |
| `frontend/src/components/Mobile/__tests__/mobileSurfaces.test.tsx` | 24（jsdom 组件交互） |
| `frontend/src/stores/__tests__/useWorkspaceStore.test.ts` | 新增 8（技能配置生命周期） |
| `backend-go/internal/catalog/skills_test.go` | 12 |

---

## 2. 关键设计决策（含理由）

### 2.1 技能落在 `default_config`，且必须 read-modify-write

`applications.default_config`（JSON）被所有 application SELECT 取出来，但 **Go 后端历史上从未写过它**
（旧 Django 侧写入过 `guided_entry_prompt_key`）。

- **直接覆盖会抹掉 legacy 键** → 新增 `GetApplicationDefaultConfigForUpdate`（`FOR UPDATE`）
  + `UpdateApplicationDefaultConfig`，在**调用方事务内** read-modify-write；
- **`FOR UPDATE` 必须在事务里才有意义**，所以合并逻辑放进 `applySkillsTx(ctx, tx, ...)`；
- 清空技能时**删除 `skills` 键**、且当整个文档为空时写 **NULL**（而非 `{}`），
  让「清空过的智能体」与「从未配置过的智能体」存储形态一致；
- 客户端提交的 `id` **严格校验、绝不静默规范化**（与 `slug` 的 `ErrBadSlug` 约定一致）；
  缺 `id` 时才由名称派生（非 ASCII 名称回退到名称哈希，保证稳定）。

### 2.2 技能前缀必须在 `@mention` 路由【之后】拼接

`parseMention` 只识别**行首** `@`。若先拼技能前缀，`@销售助手 分析这个` 会被挤出首部：

```text
正确：route(raw) → { action:'switch', content:'分析这个' } → 拼前缀 → '/查询收入查询\n分析这个'
错误：拼前缀 → '@…' 不再在首部 → routeMention 退化为 passthrough → 指令发给【错误的智能体】
```

已用测试把**危害本身**钉住（`lib/__tests__/agentSkills.test.ts` 的
「prepending BEFORE routing would have broken it」用例断言 naive 顺序确实退化成 `passthrough`）。

### 2.3 技能顺序取目录序，不取点击序

拼出的 `content` **就是幂等输入**，所以「同一次选择」必须产出**逐字节相同**的文本。
`resolveSelectedSkills` / `toggleSkillSelection` / `orderSkillsForSheet` 都强制按目录序重排。

> 开发过程记录：组件测试第一版把顺序断言写成「点击序」，跑出 FAIL —— **是实现对、测试错**，
> 已按目录序修正测试。这条不变式值得单独强调，因为它只在"同一请求重放"时才暴露。

### 2.4 移动端只换 `emptyState`，不新建路由/组件树

`HomeWorkspace` 用 `useIsMobile()` 只切换 `emptyState` 的内容，`WorkspaceHost`、Run v2、
`@mention` 路由、deep link（`/?conversation=N`）全部共用 → 两壳不可能在"快捷入口做什么"上漂移。

### 2.5 `EMPTY_WORKSPACE_STATE.selectedSkillIds` 用 `Object.freeze([])`

该常量是**所有未知应用共享的单例**，且被 `ChatRenderer` 当 Zustand selector 的返回值使用
（必须引用稳定，否则无限重渲染）。因此它的嵌套数组必须冻结 —— 否则一次意外的 `push`
会静默改掉**所有**应用的默认值。已加测试断言 `Object.isFrozen` 且 `push` 抛错。

### 2.6 两个共享容器的 padding 归零用**复合选择器**

`chatSurface.css` 的 `.chat-empty` / `.chat-input-area` 自带 padding **和各自的 safe-area inset**，
移动端组件又声明了一套。用两段选择器抬高特异度：

```css
.chat-container .chat-input-area--mobile { padding: 0; }
.chat-container .chat-empty--mobile      { padding: 0; }
```

单类选择器会与 media query 里的规则**平手**，胜负取决于打包器注入顺序（本项目已记录该坑）。

### 2.7 移动端 `Enter` = 换行，发送用按钮

桌面保持 `Enter` 发送；移动端软键盘下 `Enter` 极易误触，且本项目实际运行在**飞书 WebView** 中，
与飞书/微信一致用显式发送按钮。已在代码注释中记录该分歧。

### 2.8 遵守「拼 payload 用 `getFieldsValue(true)`」

`AgentEditorModal.handleSave` 改为 `await form.validateFields()` **仅作校验门**，
payload 取 `form.getFieldsValue(true)` —— 对齐本项目已发生过 P0 事故的约定。

---

## 3. 验证证据

| 项 | 命令 | 结果 |
|---|---|---|
| 前端类型 | `tsc --noEmit` | **无输出（净）** |
| 前端测试 | `vitest run` | **231 passed / 21 files** |
| 前端构建 | `vite build` | 成功（3387 modules，15.45s） |
| 后端构建 | `go build ./...` + `go vet ./...` | 净 |
| 后端测试 | `go test ./... -count=1` | **全绿**，含 `internal/execution`（幂等）、`internal/transport/sse`（冻结的 SSE Hub）、`internal/workerdispatch`（冻结的 Dispatcher） |
| 生成物一致性 | `sqlc generate` / `oapi-codegen` 后 `git status --short` | 仅列出预期文件 |

### 3.1 必须一并说明的既有问题（**非本轮引入**）

**`npx eslint . --ext ts,tsx` 在本仓库 HEAD 上本来就是红的。** 基线（`git show HEAD:<path>` 逐文件对照确认）：

```text
error  ArtifactMarkdown.tsx:60        react-hooks/rules-of-hooks（useCallback 条件调用）
error  useJob.ts:3                    unused useAuthStore
error  pages/Agents/AgentsPage.tsx:17 unused Avatar
error  pages/Apps/AppsPage.tsx:37     unused setFixedCategory
error  pages/Schedules/SchedulesPage.tsx:5  unused useMemo
error  pages/Share/SharePage.tsx:9    unused useMemo
error  stores/useRunChatStore.ts:138,156    多余的 eslint-disable
warn   Chat/RunChatPanel.tsx:213 x2   react-hooks/exhaustive-deps（messages 用 ?? [] 初始化）
```

**本轮改动没有新增任何一条**（对比方法：`git show HEAD:<path> | eslint --stdin --stdin-filename <path>`）。
其中 `ArtifactMarkdown.tsx` 的 `rules-of-hooks` 是**真实的 hooks 顺序缺陷**，
`RunChatPanel.tsx` 的 warning 会牵动自动滚动 effect —— 两者都超出表现层范围，**本轮未动**，建议单独立项。

### 3.2 工作区状态警告

`backend-go/internal/delivery/worker.go`（`M`）与 `backend-go/internal/delivery/worker_test.go`（`??`）
在本轮**开工前就已经是脏的**，属**并发会话**在修 delivery worker 的 `NOGROUP`/`BUSYGROUP` 恢复。
本轮全程用显式路径 `git add`，**未使用 `git add -A`**，因此没有卷入这两个文件。
**本轮尚未提交**，请确认提交范围后再提交。

---

## 4. 明确未做（避免误判为遗漏）

| 项 | 原因 |
|---|---|
| 执行文档 **P3**：`GET .../skills`、`POST /runs` 的 `skill_refs`、`RunRequestHash` 扩展、Adapter `ValidateSkillRefs` | 用户的技能模型不需要；改 hash 会影响第九轮冻结的幂等契约 |
| 执行文档 **P4**：起本地 dev 环境做端到端联调（MySQL + Redis + Vite、飞书 WebView 软键盘、iPhone safe-area 实测） | 用户选定范围到 P2；本轮以 `tsc` + 231 用例 + 生产构建 + `go test` 全绿为证 |
| 语音输入、云文档、连接器、浏览器、模板、多模型切换、技能编辑、PC 首页改版 | 设计文档 §19 明确排除 |
| 桌面端 `ApplicationSwitcher` / `HomeShortcuts` / PC Composer | §17「移动端重新设计，桌面端功能不回归」——一行未动 |
| 修既有 lint 红 | 见 §3.1，超出范围且涉及敏感区 |

---

## 5. 设计文档 §21 验收清单对照

| 验收项 | 状态 | 依据 |
|---|---|---|
| 首屏无需滚动看到 Hero / 双入口 / Composer | ✅ 结构保证 | Composer 是 workspace 列的兄弟节点；`.chat-empty--mobile { padding: 0 }` 释放预算 |
| 智能体选择不跳智能体市场 | ✅ | `MobileCatalogSheet` 直接 `onSelect` → `routeForApplication` |
| 应用选择不跳应用中心 | ✅ | 同上 |
| 两个选择器都有 最近 3 / 分类 / 全部列表 | ✅ | 测试：`shows at most THREE 最近使用 rows`、`renders 全部 first, then the catalog categories` |
| 对话页可直接切换智能体 | ✅ | `MobileAgentSwitcher` 复用同一 Sheet（§7.1 同一组件） |
| Composer 双层 Agent 风格 | ✅ | `.mobile-composer__textarea` + `.mobile-composer__toolbar` |
| 技能为一级入口 | ✅ | 与 `+` 并列在工具栏，非藏在 `+` 内 |
| `+` 中只存在「图片 / 文件」 | ✅ | `MobileAttachmentSheet` 仅一项 + 限制提示 |
| 不存在无功能按钮 | ✅ | 测试：无麦克风/语音按钮；技能按钮即使无技能也保留（避免布局跳动） |
| Bottom Sheet 适配 safe area | ✅ | `env(safe-area-inset-bottom)` 在 sheet 滚动尾与 composer |
| 软键盘不遮挡 Composer | ✅ 结构保证 | 不用 `position: fixed`，保持 flex 链（`dvh`）；**真机未实测**（P4 未做） |
| PC 页面与 Composer 无视觉回归 | ✅ | 桌面分支一行未改；`HomeShortcuts` / `ApplicationSwitcher` 保留 |

---

## 6. 后续建议

1. **提交拆分**（对齐执行文档 §42）：
   `feat(mobile): catalog derivation libs` → `feat(mobile): bottom sheet catalog picker` →
   `feat(mobile): agent-first home surface` → `feat(mobile): two-layer composer` →
   `feat(skills): agent-scoped skill config` → `test(...)`。
2. **P4 真机联调**（飞书 WebView 软键盘、iPhone safe area、Android Chrome 高度）建议单开一轮，
   这是本轮唯一没有真实环境证据的验收项。
3. **`ArtifactMarkdown.tsx` 的 `rules-of-hooks` 缺陷**建议尽快单独立项修复（真实缺陷，非风格问题）。
