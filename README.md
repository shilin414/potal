# Creation Agent Studio · 创作智能体工作台

Creation Agent Studio 是一个以 Application 为入口、以统一 Run 为执行协议的 AI 应用工作台。当前主链路由 React 前端、Go 控制面/流式面/执行面、TiDB、Redis 和飞书 Aily 自定义智能体组成。

## 当前状态

- **Go 后端**：[backend-go/](backend-go/)；G0–G10 已完成，Django 已退出运行架构。
- **前端**：[frontend/](frontend/)；React 18 + TypeScript + Vite + Ant Design + Zustand。
- **HTTP 契约**：[backend-go/api/openapi.yaml](backend-go/api/openapi.yaml) 是唯一 API Contract。
- **数据库**：TiDB 是唯一 Source of Truth；SQL 保持 MySQL 5.7 兼容。
- **Redis**：仅用于 Opaque Session、UAT 缓存、GCRA 限流、Redis Streams 分发和 Run Pub/Sub。
- **认证**：HttpOnly Opaque Session Cookie + CSRF double-submit；前端不保存 JWT。
- **Provider**：当前已完成飞书 Aily Agent；Aily Workflow、Codex、GraphFlow、HTTP Adapter 属后续 G12。

权威设计与进度文档：

- [Go 后端目标架构](docs/Creation%20Agent%20Studio%20Go%20后端目标架构.md)
- [Go 重构执行计划](docs/Creation%20Agent%20Studio%20Go%20重构执行计划.md)
- [Go 重构交接提示词](docs/Go重构交接提示词.md)
- [开发进度清单](docs/开发进度清单.md)
- [Django 历史参考归档](docs/archive/django-reference/README.md)

## 运行架构

```text
React/Vite :3030
      │ /api
      ▼
studio-api :8080 ─────────────── TiDB
      │                           Source of Truth
      ├── REST + 当前开发环境 SSE
      │
      └── Outbox ── Redis Streams ── studio-worker ── Feishu Aily
                     Redis Pub/Sub

生产目标：REST → studio-api，SSE → studio-stream :8081（G15 部署拓扑）。
```

Run 的正确性边界：

1. API 在一个 TiDB 事务内写入 Message、Run 和 Outbox。
2. Outbox Relay 以 at-least-once 语义发布到 Redis Streams。
3. Worker 只有 CAS Claim 成功时才能执行；RunLease、Heartbeat、Reaper 负责恢复。
4. Aily 实时 delta 走 Redis Pub/Sub；Final Reconciliation 的 GET chat result 是终态唯一权威。
5. 最终 Message、Run、Artifact 持久化到 TiDB；Redis 从不作为业务真相。

## 项目结构

```text
creation_agent_studio/
├── backend-go/
│   ├── cmd/api/                 # REST 控制面；开发环境也挂载 SSE
│   ├── cmd/stream/              # 可独立扩容的 SSE Gateway
│   ├── cmd/worker/              # Outbox Relay + Provider Worker
│   ├── cmd/testsession/         # 本地 E2E 会话签发工具
│   ├── api/openapi.yaml         # 唯一 HTTP Contract
│   ├── db/migrations/           # golang-migrate；MySQL 5.7 兼容
│   ├── db/queries/              # sqlc 显式 SQL
│   ├── internal/                # identity/catalog/execution/integrations/platform/transport
│   └── tests/                   # TiDB/Redis 集成测试 + 真实 Aily 浏览器 E2E
├── frontend/                    # React + TypeScript + Vite
└── docs/
    └── archive/django-reference # 只读历史行为参考，不参与运行
```

## 本地启动

### 前置要求

- Go 1.27.x
- Node.js 与 npm
- 可访问已配置的 TiDB、Redis 和飞书/Aily 环境
- 真实凭据仅放在 gitignored 的 `backend-go/.env.local`
- Go 模块代理不可达时使用 `GOPROXY=https://goproxy.cn,direct`

### 启动三个进程

```bash
# 终端 1：API（从 backend-go 目录启动，确保读取 .env.local）
cd backend-go
go run ./cmd/api

# 终端 2：执行面
cd backend-go
go run ./cmd/worker

# 终端 3：前端
cd frontend
npm install
npx vite --port 3030 --strictPort
```

访问 `http://localhost:3030`。本机 Vite 可能只监听 `localhost/::1`，不要把浏览器地址替换成 `127.0.0.1`。

如需独立 SSE 进程：

```bash
cd backend-go
go run ./cmd/stream
```

## 常用开发命令

```bash
# Go 生成物（修改 OpenAPI 或 SQL 后）
cd backend-go
make gen-api
make gen-db

# Go 静态与单元测试
go build ./...
go vet ./...
go test ./... -count=1

# 真实 TiDB + Redis 集成测试
STUDIO_TEST_TIDB=1 STUDIO_TEST_REDIS=1 \
  go test ./tests/integration/ -count=1

# 前端
cd ../frontend
npx tsc --noEmit
npx vitest run
```

## 真实 Aily 浏览器 E2E

E2E 需要 API、Worker、Vite 三个进程，并使用已经迁入 `xiaoan3_go` 的真实用户、Application、RuntimeBinding 和 UAT 身份数据。

```bash
python -m pip install -r backend-go/tests/requirements-e2e.txt
C:/software/miniconda3/envs/py311/python.exe backend-go/tests/e2e_go_chat.py
```

该脚本通过 `cmd/testsession` 签发 Go Opaque Session，不依赖 Django、`manage.py`、JWT 或 8000 端口。

## 不得回归的架构红线

- Domain 不依赖 chi、Redis driver 或 TiDB driver。
- Provider 差异只进入 RuntimeAdapter；业务层禁止散落 `if provider == ...`。
- Redis 只是分发、实时、缓存与会话，不是 Source of Truth。
- Worker 必须 CAS Claim；Redis 重复消息不得造成重复执行。
- SSE 断开不等于 Run 失败；终态必须由 Final Reconciliation 收敛。
- Aily 自定义智能体使用用户 UAT；Studio Session 与 Provider 凭据彻底分离。
- Refresh Token 只能 AES-256-GCM 加密落库；secret 不进源码、测试或日志。
- SQL 使用显式列并保持 MySQL 5.7 兼容。
- `runtime_bindings.external_resource_id` 才是 Agent ID 配置来源，不得写死。

## 已知的计划内缺口

- Workspace 模板工作区及旧 GraphFlow 链路将在 G12 迁入统一 Application → RuntimeBinding → Run 模型。
- 企业控制台、组织、API Key、Quota、Audit、Provider Health 属 G13；相关 legacy 页面目前可能返回 404。
- 完整容器、Nginx、资源限制与生产发布拓扑属 G15。

不要恢复 Django 作为临时备用后端。历史实现只保存在 `docs/archive/django-reference/` 中，用于查证已验证行为。
