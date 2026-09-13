# Creation Agent Studio Go 后端目标架构

## 1. 架构决策

Creation Agent Studio 后端正式从：

```text
Django + DRF + Django ORM + asyncio Worker
```

调整为：

```text
Go Modular Monolith
+
Independent Stream Gateway
+
Independent Execution Worker
+
TiDB
+
Redis
+
OpenAPI Contract First
```

不做 Django → Go 的逐文件翻译。

重新实现后端执行内核，但保留已经验证成功的：

```text
Application
RuntimeBinding
Provider
Conversation
AgentThread
Message
Run
RunEvent
RunArtifact
RuntimeAttachment
RunLease
Workspace API Contract
Aily Runtime 语义
```

---

# 2. 为什么不以 Gin 为核心

Gin 可以使用，但不是本项目的最优选择。

本项目主要压力来自：

```text
SSE 长连接
Aily SSE
Redis
TiDB
附件上传
Artifact
Provider HTTP I/O
```

不是 Router 每秒多处理几十万次简单 HTTP 请求。

因此采用：

```text
net/http
+
chi
```

理由：

```text
完全兼容标准 net/http

SSE / Streaming 更自然

标准 middleware 生态更完整

context.Context 原生贯穿

HTTP Client / Server 语义统一

不会把 Domain/Application 层绑定到框架 Context

以后替换 Router 成本极低
```

不采用 Fiber/fasthttp 作为默认基础设施。

它们在纯 HTTP benchmark 中可能更快，但本项目真正瓶颈是外部 Runtime 和 I/O，换取非标准 HTTP 生态并不值得。

Gin 也不作为 Domain 基础。

HTTP Framework 只允许存在于：

```text
transport/http
```

层。

---

# 3. 推荐正式技术栈

```text
Language
Go 1.27.x

HTTP
net/http
chi/v5

API Contract
OpenAPI 3.1
oapi-codegen

Database
TiDB 8.0.0

Database Access
database/sql
go-sql-driver/mysql
sqlc

Migration
golang-migrate

Redis
go-redis/v9

Queue
Redis Streams

Realtime
Redis Pub/Sub + SSE

Distributed Rate Limit
Redis Lua / Token Bucket

Logging
log/slog

Tracing / Metrics
OpenTelemetry
Prometheus

Validation
go-playground/validator

Password Hash
Argon2id

Object Storage
S3 Compatible abstraction
LocalFS 仅开发环境

Frontend
继续 React + TypeScript + Vite + Ant Design
```

---

# 4. 不使用 GORM

本项目如果只追求开发速度可以使用 GORM。

但目标已经明确：

> 性能和长期架构优先。

因此正式方案使用：

```text
database/sql
+
sqlc
```

而不是：

```text
GORM
```

原因：

```text
SQL完全可见

不会出现隐式N+1

事务边界明确

TiDB执行计划容易分析

复杂CAS/Lease/Outbox SQL更自然

编译期生成类型安全代码

减少Reflection与ORM抽象成本

数据库问题更容易定位
```

不要再创建：

```text
GenericRepository[T]
```

这种没有业务意义的通用仓储抽象。

只在真正需要隔离基础设施或 Provider 的边界定义接口。

---

# 5. Contract First

现有系统已经出现过一个典型问题：

```text
列表接口手拼 payload
单条接口另一处手拼 payload
↓
新增字段漏改一个地方
↓
前端拿到 undefined
```

Go 重构必须顺手彻底解决。

新增：

```text
api/openapi.yaml
```

作为 API 唯一契约。

由它生成：

```text
Go Request / Response Types
Go HTTP Server Interface
TypeScript API Types
```

前端与后端不再分别维护：

```text
“我觉得这个接口应该长这样”
```

而是：

```text
OpenAPI
   │
   ├── Go
   └── TypeScript
```

---

# 6. Go 项目目录

推荐：

```text
backend-go/
├── cmd/
│   ├── api/
│   │   └── main.go
│   ├── stream/
│   │   └── main.go
│   └── worker/
│       └── main.go
│
├── api/
│   └── openapi.yaml
│
├── db/
│   ├── migrations/
│   └── queries/
│
├── internal/
│   ├── platform/
│   │   ├── config/
│   │   ├── database/
│   │   ├── redis/
│   │   ├── logging/
│   │   ├── telemetry/
│   │   ├── crypto/
│   │   ├── storage/
│   │   └── httpclient/
│   │
│   ├── identity/
│   │
│   ├── tenancy/
│   │
│   ├── catalog/
│   │   ├── application/
│   │   ├── provider/
│   │   └── runtime/
│   │
│   ├── conversation/
│   │
│   ├── execution/
│   │   ├── run/
│   │   ├── event/
│   │   ├── lease/
│   │   ├── artifact/
│   │   ├── attachment/
│   │   └── outbox/
│   │
│   ├── workflow/
│   │
│   ├── governance/
│   │
│   └── integrations/
│       ├── aily/
│       ├── codex/
│       ├── graphflow/
│       └── http/
│
├── internal/gen/
│   ├── db/
│   └── api/
│
├── tests/
│   ├── integration/
│   └── contract/
│
├── sqlc.yaml
├── go.mod
└── Makefile
```

---

# 7. 三个运行进程

不再只有：

```text
Web
Worker
```

而是明确拆成：

```text
API Server
Stream Gateway
Execution Worker
```

## API Server

负责：

```text
OAuth
Session
Application
Provider
Conversation
创建Run
附件暂存
Artifact Resolve入口
管理API
```

全部是短请求。

---

## Stream Gateway

专门负责：

```text
GET /api/v2/runs/{id}/stream
```

职责：

```text
TiDB历史回放
+
Redis实时订阅
+
SSE keepalive
+
断线恢复
```

SSE 与普通 API 独立扩容。

这样即使将来有：

```text
几千个SSE连接
```

也不会占用普通 API 的连接资源。

---

## Execution Worker

负责：

```text
Run Dispatch

Aily Streaming

Aily Polling

Attachment Provider Upload

Final Reconciliation

Artifact Discovery

Lease Heartbeat

Retry

Circuit Breaker
```

---

# 8. API Server 与 Stream Gateway 为什么分开

REST：

```text
请求持续时间
几十毫秒～几秒
```

SSE：

```text
几十秒
几分钟
甚至更长
```

二者 Server Timeout、连接数、负载模型完全不同。

因此实际部署：

```text
Nginx
│
├── /api/v2/runs/*/stream
│          ↓
│    Stream Gateway
│
└── 其他 /api
           ↓
       API Server
```

比全部塞进一个 Web Server 更合理。

---

# 9. 数据库仍然是 Source of Truth

TiDB 保存：

```text
User
FeishuIdentity

Application
Provider
RuntimeBinding

Conversation
Message
AgentThread

Run
RunEvent
RunArtifact
RuntimeAttachment
RunLease

Outbox

Audit
Quota
```

Redis 不承担这些数据的最终真实性。

---

# 10. Redis 新职责

Redis 统一承担：

```text
Session

UAT Access Token Cache

Provider Rate Limit

Run Dispatch Queue

Realtime Pub/Sub

Short-lived Cache
```

统一 Key Prefix：

```text
xiaoan3:
```

例如：

```text
xiaoan3:session:...

xiaoan3:queue:aily

xiaoan3:run:{id}:live

xiaoan3:provider:aily:uat:{user}

xiaoan3:rate:aily:chat
```

不要依赖多个 Redis Logical DB 形成业务隔离。

长期应以 Key Namespace 隔离，为未来 Redis Cluster 留余地。

---

# 11. Run 分发架构升级

之前：

```text
Worker
↓
不断查询TiDB queued Run
↓
CAS Claim
```

调整为：

```text
API
↓
TiDB Transaction
├── INSERT Run
├── INSERT Message
└── INSERT Outbox
↓
COMMIT
```

然后：

```text
Outbox Relay
↓
Redis Stream
↓
Provider Worker
↓
CAS + Lease
↓
执行
```

---

# 12. 为什么使用 Outbox

不能：

```text
INSERT Run
↓
COMMIT
↓
Redis XADD
```

然后假设一定成功。

否则：

```text
Run已经入库
Redis突然断了
↓
永远没人执行
```

所以：

```text
Run
+
Outbox
```

必须同事务。

Outbox Relay 可以不断重试发布。

---

# 13. Redis Streams 只是 Dispatch

Redis Stream：

```text
xiaoan3:queue:feishu_aily
xiaoan3:queue:codex
xiaoan3:queue:http
```

只是：

```text
高性能任务唤醒和分发
```

并不是 Run 状态 Source of Truth。

Worker 拿到消息后仍然必须：

```text
CAS Claim Run
```

因此：

```text
Redis重复消息
```

也不会产生重复执行。

---

# 14. CAS + Lease 保留

Worker：

```text
收到Queue消息
↓
UPDATE Run
WHERE status=queued
↓
affected_rows=1
↓
创建/刷新Lease
```

才真正开始。

Redis 负责：

```text
快
```

TiDB CAS/Lease 负责：

```text
对
```

---

# 15. Worker 故障恢复

Worker 成功 Claim 后：

```text
Redis消息可以ACK
```

运行中靠：

```text
RunLease
```

保证恢复。

如果 Worker 崩溃：

```text
Lease过期
↓
Reaper发现
↓
Run → interrupted / queued
↓
创建新的Outbox
↓
重新投递Redis Stream
```

不依赖 Redis Pending Message 永久保存执行状态。

---

# 16. Provider Worker Pool

Worker 不按照：

```text
一个用户一个线程
```

运行。

而是：

```text
goroutine
+
Provider Concurrency Semaphore
+
Distributed Rate Limiter
```

例如：

```text
Aily

Start Rate = 官方10/s

Max Inflight = 配置项
```

这两个概念完全分开。

---

# 17. Aily Distributed Rate Limit

原固定 1 秒 Redis Counter 改成：

```text
Redis Lua Token Bucket / GCRA
```

避免固定窗口边界：

```text
第0.99秒10个请求
+
第1.01秒10个请求
=
20个请求瞬时打出去
```

Provider Rate Limit 必须在多个 Worker 实例之间共享。

---

# 18. Aily HTTP Client

一个 Worker Process 内使用共享：

```text
http.Client
```

以及经过调优的：

```text
http.Transport
```

启用：

```text
Connection Pool
Keep Alive
Idle Connection Reuse
TLS Reuse
```

不允许：

```text
每个Run new http.Client
```

Go 的执行模型直接消除了现有 Python 中：

```text
event loop closed
跨loop httpx client
sync_to_async
async_to_sync
```

这些复杂度。

---

# 19. RuntimeAdapter

Go 中定义：

```text
RuntimeAdapter
```

大致能力：

```text
Submit

Status

Stream

UploadAttachment

ResolveArtifact

CheckVisibility

Capabilities
```

Provider：

```text
AilyAgentAdapter
AilyWorkflowAdapter
CodexAdapter
GraphFlowAdapter
HTTPAdapter
```

全部实现同一 Runtime Boundary。

---

# 20. Aily 模块

推荐：

```text
internal/integrations/aily/
├── client.go
├── auth.go
├── agent_adapter.go
├── workflow_adapter.go
├── dispatcher.go
├── event_mapper.go
├── artifact.go
├── attachment.go
├── ratelimit.go
└── errors.go
```

保留已经真实验证成功的语义：

```text
UAT调用

Lazy Session

Streaming

Async Poll

Final Reconciliation

Attachment Identity

Artifact ID长期引用

24h URL刷新

Visibility
```

---

# 21. Aily 数据映射保持不变

```text
ApplicationRuntimeBinding.external_resource_id
        =
Aily agent_id


AgentThread.remote_id
        =
Aily session_id


Run.external_run_id
        =
Aily agent_chat_id


RuntimeAttachment.external_attachment_id
        =
Aily agent_attachment_id


RunArtifact.external_artifact_id
        =
Aily agent_artifact_id
```

这是已经验证正确的 Domain Model，不需要因为换 Go 改变。

---

# 22. Attachment 架构进一步升级

当前方案：

```text
Browser
↓
Studio API
↓
直接上传Aily
↓
拿agent_attachment_id
↓
创建Run
```

建议调整为：

```text
Browser
↓
Studio Storage
↓
RuntimeAttachment(local)
↓
Create Run
↓
Worker
↓
以正确UAT上传Aily
↓
agent_attachment_id
↓
Start Chat
```

好处：

```text
API不被40MB Provider Upload长时间占用

上传失败可以重试

Provider切换更容易

Attachment真正Provider Neutral

用户身份可以在Worker统一处理
```

开发环境：

```text
LocalFS
```

生产：

```text
S3 Compatible / MinIO / OSS
```

---

# 23. Artifact

保持：

```text
agent_artifact_id
=
长期引用
```

而：

```text
24小时签名URL
=
Cache
```

Artifact Resolver 放 API Server。

需要归档时：

```text
mirror_on_access
mirror_on_complete
```

写入 Object Storage。

---

# 24. Realtime Event 架构

Aily Chunk 到来：

```text
Aily
↓
Worker
↓
content.delta
```

不要每 Token INSERT TiDB。

分两条：

```text
实时路径
Worker
↓
Redis Pub/Sub
↓
Stream Gateway
↓
Browser
```

和：

```text
持久路径
Worker
↓
Coalesce
↓
RunEvent / content.snapshot
↓
TiDB
```

---

# 25. Content Snapshot

为了同时满足：

```text
低数据库写入
+
SSE断线恢复
```

引入：

```text
content.snapshot
```

例如：

```text
每250~500ms
或者累计达到一定字符数
```

写一条完整当前文本快照。

实时用户继续收到：

```text
content.delta
```

但重连以后：

```text
先加载最近content.snapshot
↓
再接实时delta
```

不会依赖保存每一个 Token。

---

# 26. Final Reconciliation

Aily 流结束、5 分钟超时、网络断开：

```text
都必须
GET Chat Result
```

最终：

```text
Message
Run
Artifact
```

以 Reconciliation 结果为准。

这一行为必须保留现有真实联调结论。

---

# 27. 身份认证进一步优化

普通用户仍然：

```text
飞书 OAuth Only
```

管理员：

```text
/login/admin
```

但 Studio Auth 不再同时维护：

```text
JWT localStorage
+
Cookie
```

新的正式方案：

# HttpOnly Opaque Session Cookie

例如：

```text
studio_session=<random 256bit token>
```

Redis：

```text
SHA256(session_token)
→ session payload
```

Cookie：

```text
HttpOnly
Secure
SameSite=Lax
```

前端 JavaScript 完全拿不到登录凭据。

---

# 28. 为什么移除 JWT localStorage

当前浏览器应用完全是自己的同源 SPA。

没有必要：

```text
把Bearer Token暴露给JS
```

Opaque Session：

```text
XSS攻击面更小

注销立即生效

Session吊销简单

飞书OAuth语义更自然

Artifact 302天然携带Cookie
```

这是比当前双认证机制更干净的方案。

---

# 29. CSRF

使用 Cookie Session 后，所有变更请求必须增加：

```text
CSRF Protection
```

至少实现：

```text
Origin / Referer 校验
+
CSRF Token Header
```

不能因为 SameSite 就完全忽略 CSRF。

---

# 30. 飞书 Refresh Token

飞书 Refresh Token 必须持久化时：

```text
AES-256-GCM
```

加密后写 TiDB。

Access Token：

```text
Redis TTL Cache
```

数据库不保存 UAT 明文。

---

# 31. Local Admin

本地 Admin 密码：

```text
Argon2id
```

不要复刻 Django Password Hash，既然没有历史迁移需求，就直接采用新的安全格式。

---

# 32. Application / Catalog

现有前端智能体市场行为全部保留：

```text
Application CRUD

Runtime Binding

Agent ID配置

Default Agent

Avatar

Favorite

Usage Count

Runtime Directory

Validate Runtime
```

但后端由 Go 提供。

不再需要 Django Admin。

---

# 33. 管理后台

Django Admin 必须正式退出目标架构。

真正需要管理的：

```text
Provider

RuntimeBinding

User

Quota

Audit

Application

默认Agent

运行监控
```

全部进入：

```text
React Admin / Enterprise Console
```

并调用普通 Go Admin API。

---

# 34. 数据库 Schema

既然是新项目：

```text
不迁移旧 Django Schema
```

直接重新设计干净 Schema。

核心表：

```text
users
feishu_identities

applications
application_favorites

providers
runtime_bindings

conversations
agent_threads
messages

runs
run_events
run_commands
run_leases

runtime_attachments
run_artifacts

outbox_events

quota_policies
audit_logs
```

旧：

```text
AgentExecution
AgentTurn
AppRunner Job
Django Site
Django ContentType
Django Session
```

都不进入新 Schema。

---

# 35. Index 设计

重点索引：

```text
applications:
UNIQUE(slug)

runtime_bindings:
(application_id, enabled)

conversations:
(user_id, application_id, updated_at)

messages:
(conversation_id, created_at)

runs:
(status, available_at, priority, created_at)
(conversation_id, created_at)
(user_id, created_at)
(provider, external_run_id)

run_events:
UNIQUE(run_id, sequence)

run_leases:
(expires_at)

run_artifacts:
(run_id)
(provider, external_artifact_id)

runtime_attachments:
(user_id, provider, created_at)

outbox_events:
(status, available_at, created_at)
```

所有真实查询都需要 EXPLAIN 验证。

---

# 36. ID

新系统不要继承 Django 自增主键设计依赖。

推荐核心业务资源采用：

```text
UUIDv7
```

API 表现为 String。

数据库尽量使用紧凑存储。

要求：

```text
应用层生成
时间有序
不依赖TiDB AUTO_INCREMENT
不依赖某一个数据库特性
```

---

# 37. Migration

正式 Schema 由：

```text
golang-migrate
```

管理。

禁止生产依赖：

```text
AutoMigrate
```

Migration 必须：

```text
可审核
可重复
可回滚或有明确Forward Fix方案
```

---

# 38. 测试数据库

彻底取消：

```text
SQLite作为TiDB逻辑的主要集成测试数据库
```

测试分层：

```text
Unit Test
→ 无数据库

Repository Integration Test
→ TiDB

API Integration Test
→ TiDB + Redis

Provider Contract Test
→ Mock Aily

Real Provider E2E
→ 明确开启后使用真实UAT

Frontend E2E
→ Playwright
```

不能再出现：

```text
SQLite全绿
TiDB才暴露兼容问题
```

---

# 39. Observability

第一版 Go 后端就接入：

```text
Structured Log
OpenTelemetry Trace
Prometheus Metrics
```

核心 Metrics：

```text
HTTP latency

Active SSE

Run queue depth

Run execution latency

Run success/failure

Lease expired

Outbox backlog

Redis Stream lag

Provider inflight

Provider start rate

Provider 429

Provider timeout

Aily reconcile count

TiDB query latency

Redis latency
```

---

# 40. Health

提供：

```text
/health/live

/health/ready

/metrics
```

Ready 至少检测：

```text
TiDB
Redis
```

Provider 不应该因为临时 Aily 故障直接导致 API Server Not Ready。

---

# 41. 部署拓扑

最终：

```text
                         Nginx
                           │
           ┌───────────────┴──────────────┐
           │                              │
           ▼                              ▼
       React SPA                      Go Backend
                                          │
                       ┌──────────────────┴────────────────┐
                       │                                   │
                       ▼                                   ▼
                 REST API Pool                      SSE Gateway Pool
                       │                                   │
                       └──────────────┬────────────────────┘
                                      │
                          ┌───────────┼───────────┐
                          ▼           ▼           ▼
                        TiDB        Redis     Object Storage
                                      │
                                      ▼
                              Execution Plane
                    ┌─────────────────┼─────────────────┐
                    ▼                 ▼                 ▼
               Aily Worker       Codex Worker      HTTP Worker
                    │
                    ▼
                Feishu Aily
```

---

# 42. 代码设计约束

必须遵守：

```text
不使用TIDB独有的语法，要适配mysql5.7

Handler只做HTTP适配

Domain不依赖chi

Domain不依赖Redis

Domain不依赖TiDB Driver

Integration不污染Domain

禁止Global Mutable State

禁止Generic Repository

禁止ORM隐藏SQL

禁止Provider if/else散落业务层

禁止Web Handler执行长任务

禁止把Redis当Source of Truth

禁止把SSE连接当Run生命周期
```

---

# 43. 当前 Django 后端的定位

Go 重写期间：

```text
Django
=
行为参考实现
```

而不是：

```text
未来架构组成部分
```

尤其保留并参考现有已经真实验证过的：

```text
Aily SSE解析

Artifact markdown修正

Final Reconcile

Attachment规则

OAuth Scope

身份显示

API响应行为
```

但不要照搬：

```text
async_to_sync

sync_to_async

Django ORM

DRF Serializer

Django Admin

Channels

Management Command结构
```

---

# 44. 最终架构结论

新的后端不是：

```text
Django换成Go
```

而是：

# 重新实现一个以 Go 为核心的高并发 Runtime Control Plane + Execution Plane。

核心是：

```text
OpenAPI Contract

Application Runtime

Run

Outbox

Redis Streams

Lease

Provider Adapter

SSE Gateway

TiDB

Object Storage

Observability
```

而不是某个 Web Framework。