# Creation Agent Studio · Go 重构已做/未做清单

> 快照时间：2026-09-13 凌晨（第五轮会话收尾）
> 详细实测记录见 `docs/开发进度清单.md` 第六章；架构红线见
> `docs/Creation Agent Studio Go 后端目标架构.md`；阶段门槛见
> `docs/Creation Agent Studio Go 重构执行计划.md`（G0–G15）。

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
