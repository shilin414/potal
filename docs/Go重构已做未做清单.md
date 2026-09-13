# Creation Agent Studio · Go 重构已做/未做清单

> 快照时间：2026-09-13（第六轮会话：应用平台 + 权限收紧）
> 详细实测记录见 `docs/开发进度清单.md` 第六章；架构红线见
> `docs/Creation Agent Studio Go 后端目标架构.md`；阶段门槛见
> `docs/Creation Agent Studio Go 重构执行计划.md`（G0–G15）。

---

## 〇、第六轮新增 ✅（2026-09-13：应用平台优先 + 智能体权限收紧）

### 固定应用 / 应用中心（§3.2/§70/§71，本轮主线）

| 项 | 内容 | 状态 |
|---|---|---|
| 迁移 0005 | `applications.enabled` 列（应用中心开关）+ 种子 4 个固定应用（条码信息查询/条码查询页、OA账号解锁/表单、修改OA密码/表单、物料信息查询/页面，全部 公开+启用，分类 `apps/应用`） | ✅ |
| 可见性新语义 | `visible()`：staff 全可见；`enabled=0` 对普通用户全隐藏；`is_public=0`（仅自己可见）只有管理员看得到 | ✅ |
| enabled 开关 | PATCH `/api/v2/applications/{id}` 接受 `enabled`；列表/详情 payload 增 `enabled` 字段（契约先行，openapi + oapi-codegen + sqlc 均已再生） | ✅ |
| 修复隐藏缺陷 | `scope=manage` 的 SQL 预过滤此前会把其他用户的公开应用滤掉（无固定应用时不可见）；`ListApplicationsByVisibility` 改为 JOIN 分类表 + manage 侧保留 is_public 行，`visible()` 仍是权威判定 | ✅ |
| 应用中心重构 | `AppsPage` 弃用 legacy `/apps/` 接口（原 404），改用 v2 目录（kind=all + scope=manage）：搜索、分类侧栏（`AppCategoriesSidebar`，与智能体市场同构）、卡片直开 `/app/:slug`；管理员卡片内 启用/公开 开关，停用卡降级显示 | ✅ |
| PageRenderer 重写 | 弃用 legacy `useAppStore`/`ApplicationRuntime` 链路（原固定应用渲染必然空），改 v2 渲染器注册表 `FIXED_RENDERERS`（renderer_key → 页面组件）；四个种子应用先接 `FixedAppPlaceholder` 占位页，业务页就绪后逐键替换 | ✅ |
| 丝滑返回（§80） | workspaceStore 增 `previousApplicationId`（openApplication 换应用时记录来源）；固定应用「返回工作台」回来源工作区（chat → `/chat/:slug` 恢复会话/草稿/滚动），无来源才回首页 | ✅ |
| 浏览器 E2E | `gui-test-screenshots/e2e_apps.py`：18 项断言（首页常用应用、占位页、Shell 不刷新、@mention 进应用、§80 返回、应用中心管理员开关、停用/恢复、普通用户视角）+ 3 张截图 | ✅ |

### 智能体权限收紧

- **仅管理员可新建**：`POST /api/v2/applications` 非 staff → 403「只有管理员可以添加智能体」；市场「新建智能体」按钮仅 `is_staff` 渲染（auth store 增 `is_staff`，`syncSessionUser` 同步）
- **公开语义**：`is_public=true` 所有用户可见可用（能否真正调用仍由 Worker 用本人 UAT 做 Aily 可见性校验，运行时既有逻辑）；`is_public=false`（仅自己可见）只有管理员可见——普通用户连自己名下的旧私有应用也不再见（编辑弹窗已有说明文案）
- **数据**：飞书用户「吴志彬」已提为 `is_staff=1`（平台管理员，否则 UI 无法自测管理功能）；编辑弹窗新建默认仍是 仅自己可见（安全默认）
- 测试：`internal/catalog/repo_test.go` 新增 10 例可见性语义单测；Go 全包 + tsc + vitest 92/92 + 真实 Aily 聊天 E2E 基线（会话 49）全过，零回归


## 〇、第七轮新增 ✅（2026-09-13：定时任务 Schedule Core + 飞书投递）

> 依据《未来演进完整架构文档》当前阶段核心（Schedule → Delivery），本环节不引入
> Workflow、不恢复 Legacy Job；Scheduler 不调 Aily、不发飞书，后台执行一律 owner UAT。

### 数据层

| 项 | 内容 | 状态 |
|---|---|---|
| 迁移 0006 | `runs` 增 `trigger_type/trigger_id/priority/available_at`（TiDB 约束：ALTER 的 AFTER 不可引用同批新列，故列/索引分两个 migration 批次） | ✅ |
| 迁移 0007 | `runs` claim 索引 `(status, available_at, priority, created_at)` + trigger 索引 | ✅ |
| 迁移 0008 | `schedules` / `schedule_occurrences`（UNIQUE(schedule_id, scheduled_at) 幂等屏障）/ `schedule_deliveries` / `delivery_executions`（UNIQUE(occurrence_id, schedule_delivery_id) 投递幂等） | ✅ |
| 契约决策 | once/daily/weekly/monthly 结构化触发（无裸 Cron）；IANA 时区；monthly 29-31 短月顺延月末；misfire fire_once/skip（默认 fire_once）；overlap queue/skip（默认 queue，v1 无 parallel）；deadline=scheduled_at+execution_window_seconds；run-now 建真实 occurrence 且不动 next_run_at；删除硬删配置保留历史；投递 v1 仅飞书 owner_user + summary 模式，重试上限 5 次指数退避 + 429 长冷却 | ✅ |

### 后端（四角色拓扑成型：api / stream / worker / scheduler）

| 项 | 内容 | 状态 |
|---|---|---|
| schedule 域 | `internal/automation/schedule`：CRUD + next-run 计算（自研，避 cron 依赖）+ 权威 next-runs 预览 + 可调度应用校验（chat + enabled + 有绑定） | ✅ |
| scheduler | `internal/automation/scheduler` + `cmd/scheduler`：每秒扫描 due（纯 SELECT，MySQL 5.7 兼容无 SKIP LOCKED）→ 单事务 原子提交 occurrence+conversation+message+run+outbox+next_run_at（崩溃无孤儿）→ overlap/misfire/execution_window 策略判定（skipped/failed occurrence 原子落库） | ✅ |
| run 扩展 | `CreateRunInput` 增 TriggerType/TriggerID/Priority/AvailableAt + `CreateRunInTx` 事务变体；execution.Service 增 `OnRunSucceeded` CAS-赢家钩子（panic 隔离） | ✅ |
| delivery | `internal/delivery`：Dispatcher（成功终态后 fan-out，UNIQUE 吸收重复）+ FeishuSender（owner UAT 发 IM，复用 FeishuClient/AuthResolver）+ 独立 GCRA 限流 `rate/feishu/im` + Worker 池（XReadGroup + due-scan 双兜底 + sending 租约回收 + XAutoClaim 同款恢复） | ✅ |
| API | OpenAPI 3.0.3 增 10 个端点（CRUD/enable/disable/run-now/occurrences/preview），owner 鉴权防伪造，keyset 分页，字段级 400 | ✅ |
| 指标 | trigger/queue delay、misfire、overlap_skipped、delivery duration/failures | ✅ |
| 测试 | TiDB 集成：双 Scheduler 同槽只产 1 occurrence+1 run、overlap skip 不产生 run、run-now 不动 next_run_at、同 occurrence 同 target 只 fan-out 一次；纯单测：next-run（时区/周/月末顺延/闰日/once）+ 投递文本截断 + 目标类型路由 | ✅ |

### 前端（定时任务中心）

- 顶级入口 `/schedules`（fullWidthConsole）+ 桌面 Header「⏰ 定时任务」+ 移动 Drawer；路由/导航/Shell 均不卸载
- `SchedulesPage`：状态筛选（全部/运行中/已暂停/失败）+ 搜索 + 桌面表格/移动卡片（768px 断点切换）+ 空态引导 CTA / 错误重试 / 行级 mutation 锁
- `ScheduleEditorModal`：三区块结构化表单（基本信息/执行时间/策略与会话 + 飞书投递区块），智能体选择器过滤不可调度应用，`POST /schedules/preview` 权威预览未来执行时间（`aria-live`）
- `ScheduleDetailDrawer` 执行历史（状态 Tag 时间线 + run id）、`ScheduleStatusTag` 状态语义（文字+图标）
- 测试：`scheduleFormat.test.ts`（payload↔表单互转/字段清理/中文摘要）+ `scheduleApi.test.ts`（请求契约钉死）；tsc + vitest 全绿（112/112）

### 遗留 / 已知边界（下轮候选）

- `cmd/worker --provider=feishu_delivery` 为独立投递消费进程（与 runs worker 同 binary 不同 pool）；部署脚本需补 scheduler + delivery 两进程
- 投递 v1 未做 external_message_id 回填（SendIMMessage 不返回 message_id）与飞书 429 的 Retry-After 精确解析（按 code 99991400 启发式识别 + 2x 冷却）
- overlap=queue 的「排队」实现为被动等待：上一轮未 terminal 时本轮 slot 每秒重扫、不创建 run，上一轮结束即补跑（scheduled_at 保持原槽位）；严格的 pending-occurrence 持久化排队未做
- execution_window=0 语义 = 无窗口限制（deadline 检查跳过）；misfire 宽限 5 分钟为常量未配置化
- occurrence `running` 状态经 run claim 回写（best-effort），不影响终态收敛正确性
- 「运行中」列表筛选 = enabled=1（Schedule 的「运行中/已暂停」即启用/停用语义，与卡片状态 Tag 一致）
- 执行历史 keyset 分页仅后端就绪，前端「加载更多」按钮待补（当前首页 50 条）
- `reuse` 会话策略后端已支持（首跑惰性绑定 conversation_id），UI 已暴露开关但未提供指定历史会话入口
- `content.snapshot` 250-500ms coalesce 仍未做（G11 前置小债）
- Enterprise 自动化 Tab 仍指遗留接口（与 Schedule 无关，待迁移决策）

---

## 一、已完成 ✅

### 执行计划阶段（G0–G10 全部完成）

| 阶段 | 内容 | 状态 |
|---|---|---|
| G0 | OpenAPI 契约冻结（`backend-go/api/openapi.yaml` 唯一 HTTP Contract → oapi-codegen/sqlc 生成） | ✅ |
| G1 | Go 平台基础（config/slog/TiDB pool/redisx/Prometheus/OTel/优雅停机；cmd/api、cmd/stream、cmd/worker + testsession、seeddata） | ✅ |
| G2 | 新 Schema（TiDB 新库 `xiaoan3_go` 20 张表，golang-migrate + sqlc，MySQL 5.7 兼容，显式列 + EXPLAIN 验证） | ✅ |
| G3 | Identity（飞书 OAuth 6 scope、HttpOnly Opaque Session、CSRF double-submit、Argon2id + Django PBKDF2 兼容、refresh token AES-256-GCM 落库） | ✅ |
| G4 | Catalog（RuntimeAdapter/RuntimeRegistry、智能体市场全套：创建/改绑/头像/默认/收藏/校验/删除） | ✅ |
| G5 | Execution Core（CreateRun=事务内 Message+Run+Outbox、Outbox Relay、Redis Streams、CAS Claim、Lease/Heartbeat/Reaper、XAUTOCLAIM、GCRA 分布式限流） | ✅ |
| G6 | SSE 网关（先 SUBSCRIBE→回放→按 sequence 去重实时帧、15s keepalive、终态自动关闭；statusWriter Flush 转发修复） | ✅ |
| G7 | Aily Agent 集成（嵌套 text 容错、跨 item artifact 配对、Final Reconciliation 唯一终态权威、Lazy Session 钉死、UAT 强制、**streaming→polling chat_id 传递修复**） | ✅ |
| G8 | Storage/Attachment（LocalFS/S3 抽象、Worker 侧按用户 UAT 上传、头像已迁入 Go Storage） | ✅ |
| G9 | 前端切 Go（删 JWT/localStorage、cookie session + CSRF、Vite→:8080、conversations 新接口、cmd/testsession 会话签发） | ✅ |
| G10 | **删除 Django**（脱敏归档 `docs/archive/django-reference/`，`backend/` 已删，活跃文档/Nginx 上游/前端文案同步；删除后全套复验通过） | ✅ |

### 数据与测试基线

- 数据迁移完成：users/feishu_identities（refresh token 重加密）/applications/bindings/categories/avatars → `xiaoan3_go`（数字 id 保留；工具 `cmd/seeddata` 保留为一次性历史工具）
- 测试基线全绿（可复跑）：
  - `go test ./... -count=1`（7 个包）
  - `STUDIO_TEST_TIDB=1 STUDIO_TEST_REDIS=1 go test ./tests/integration/ -count=1`（CAS 竞争恰好 1 赢家、重复投递恰好 1 次、Lease-Reaper、SSE 回放+实时+终态、会话时区 UTC 契约）
  - `npx tsc --noEmit` 0 错误；`npx vitest run` 13 文件/91 用例
  - 真实 Aily 浏览器 E2E：`backend-go/tests/e2e_go_chat.py`（需 Go API + Worker + Vite 三进程）

### 第五轮会话的用户实测 UI 修复（全部经真实 Aily / 浏览器验证）

1. **顶部「对话」导航**：进入主智能体聊天工作区（带切换器/新建按钮），不再落在无头部的首页
2. **侧栏「新建」**：对当前/主智能体开新会话；移动端抽屉同语义
3. **侧栏自动刷新**：Run 创建/终态收敛后 1.5s 防抖刷新列表（此前只在挂载时拉一次，切智能体新建的对话不出现）
4. **同路由「新建」失效**：workspace store 增加 `newConversationTick` 信号 + ChatRenderer `useLayoutEffect` 重置（此前只变 URL 不清画面）
5. **生成图片先裂开再展示/闪烁**：未解析 artifact 前渲染占位（不再 404 破图）+ `ChatImage` 稳定组件（内部加载态，重渲染不重挂）
6. **图片反复加载**（F12 中 `/open` 302 + CDN 成对重复请求）：ReactMarkdown `components` 用 `useMemo` 稳定组件身份 + 后端 `OpenArtifact` 302 加 `Cache-Control: private, max-age=1800`；实测生成全流程各恰好 1 次请求
7. **`Uncaught AbortError: BodyStreamBuffer was aborted`**：`runStream.ts` 终态关闭时 `reader.cancel()` 的 Promise 拒绝显式吞掉

---

## 二、未完成 ❌（按执行计划优先级）

### G11 Mobile 完善化（前端，下一个阶段）

- Bottom Sheet 智能体切换、软键盘处理、Bottom Composer、Viewport Resize、Safe Area、Workspace Back Stack
- FormRenderer / PageRenderer / DashboardRenderer 移动专属布局

### G12 Aily Workflow + 其他 Provider

- AilyWorkflowAdapter、CodexAdapter、GraphFlowAdapter、HTTPAdapter（直接进 Application→RuntimeBinding→Run）
- legacy `/api/agents/*`（本地创作智能体，当前为空集 shim）转为 Application 体系
- WorkspacePage 模板工作区的旧 GraphFlow 链路在此之后才能迁移

### G13 治理

- Quota/Audit（`quota_policies`/`audit_logs` 表已建，运行时未实现）、熔断、Provider Health、Usage Accounting
- React 管理后台（替代 Django Admin：Provider/Binding/User/Runtime Health）
- 企业控制台 legacy 接口（`/enterprise/*`、组织、API keys）——前端控制台页当前 404

### G14 性能与故障测试

- REST/SSE 压测、Redis Stream 积压、Worker kill、Provider 429、5min SSE 超时、慢客户端、大附件
- 重点：不丢 Run、不重复 Run、事件有序、可恢复

### G15 生产部署

- studio-api / studio-stream / studio-worker 三进程部署；Nginx REST/SSE 分流
- `frontend/nginx.conf` 上游已指向 `api:8080` / `stream:8081`，但容器镜像/部署清单/资源限制未建

### 其他已知缺口 / 待办小项

- **content.snapshot**（§25）：实时 delta 只在 Redis + 终态落库，长回复断线重连会丢中间 delta（靠 reconcile 兜底）；需 Worker 侧 250–500ms Coalesce 快照落 run_events
- **前端仍指向缺失 API 的 legacy 面**（G12/G13 范围，当前经代理 404）：`useJob`（/app-runner/jobs/、/ws/runner/）、BatchTranscribeRunner、FolderPickerModal、ImageGenieRunner、WorkflowsPage、EnterprisePage、useProjectStore（/projects/*）
- antd `validateDOMNesting` 开发警告（历史组件 `<div>` 嵌套，无功能影响）
- Go 侧 Django PBKDF2 密码兼容保留（迁移用户登录所需），账号全部重哈希 Argon2id 后可移除
- `cmd/seeddata` 已成一次性历史工具（avatar 源文件随 backend/ 删除，重放头像部分不可用）
- **Git 工作区有大量未提交改动**（backend/ 删除登记 + backend-go/ 等新文件 + 修改），是否提交/如何拆分提交由用户决定——新会话不要擅自 commit / reset / clean
- 本机进程现状：studio-api（Temp 下的新构建 exe，含 Cache-Control 修复）、studio-worker-g10、vite:3030 正在运行；新会话建议 `go run ./cmd/api` / `./cmd/worker` 重启到源码最新态
