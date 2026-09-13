你现在负责 **Creation Agent Studio 后端 Go 重构与后续开发**。

这不是一个普通的“把 Python 换成 Go”任务。

本次目标是：

# 在已经验证成功的产品行为基础上，重新实现一个高性能、长期可扩展的 Go Runtime Platform Backend。

工作优先级：

```text
架构正确性
>
运行可靠性
>
性能
>
可维护性
>
短期开发工作量
```

不使用TIDB8.0.0独有的语法，要适配mysql5.7。
不要为了少写代码、复用旧 Django 或快速看到页面运行，而牺牲目标架构。

---

# 一、必须首先阅读的资料

开始任何修改前，完整阅读：

```text
docs/Creation Agent Studio 未来演进完整架构文档.md
docs/开发进度清单.md
docs/Creation Agent Studio Go 后端目标架构.md
docs/Creation Agent Studio Go 重构执行计划.md
```

其中：

```text
未来演进完整架构文档
Go 后端目标架构
```

描述产品和 Domain 最终目标。

```text
开发进度清单.md
```

描述当前已经验证成功的行为、真实 Aily 联调结论和已知问题。

在必要的时候阅读本地飞书官方 Aily 文档：

```text
C:\Users\吴志彬\Documents\飞书开放平台文档\飞书aily\自定义智能体
```

任何 Aily API 行为必须优先依据官方文档和当前真实联调结果。

不要凭模型记忆猜测接口。

---

# 二、现有 Django 后端的定位

当前：

```text
backend/
```

是已经实现过大量能力的 Django 参考实现。

它可以用于：

```text
理解Domain

确认API行为

确认Aily特殊兼容

确认真实联调Bug

确认前端当前依赖
```

但是它：

# 不是未来后端架构的一部分。

不要：

```text
让Go调用Django

长期维持Go+Django双后端

在Django旁边增加少量Go服务

把Django ORM继续作为核心数据层

逐文件把Python翻译成Go
```

目标是：

```text
Go Backend最终完全取代Django Backend
```

---

# 三、不考虑旧数据库迁移

这是新项目。

不需要保留 Django 数据模型兼容。

不需要：

```text
旧Migration兼容

旧表结构兼容

AgentTurn迁移

AgentExecution迁移

Job迁移

Django Session迁移

Django Admin迁移
```

直接按目标架构建立新的干净数据库 Schema。

在真正重建目标数据库之前必须先确保当前开发数据有需要时能够备份。

不要在没有确认的情况下自动删除当前数据库。

---

# 四、目标后端技术栈

使用：

```text
Go 1.27.x

net/http

chi/v5

OpenAPI 3.1

oapi-codegen

database/sql

sqlc

go-sql-driver/mysql

golang-migrate

go-redis/v9

OpenTelemetry

Prometheus

log/slog
```

数据库：

```text
TiDB 8.0.0
```

通过：

```text
TiProxy : 6000
```

连接。

不要使用 GORM 作为核心数据层。

不要选择 Fiber/fasthttp 作为项目基础 HTTP Runtime。

不要让 Domain 依赖 chi。

---

# 五、为什么使用 net/http + chi

HTTP Framework 只负责：

```text
Routing
Middleware
Request/Response
```

核心：

```text
Runtime
Execution
Conversation
Application
Provider
```

必须是普通 Go package。

不要把：

```text
chi.Context
gin.Context
fiber.Ctx
```

传入 Domain / Application Service。

内部统一使用：

```go
context.Context
```

---

# 六、数据库配置

目标环境：

```text
DB_HOST=192.168.212.38
DB_PORT=6000
DB_NAME=xiaoan3
DB_USER=xiaoanuser
DB_PASSWORD=<从本地环境变量读取>
```

真实密码只能从：

```text
.env.local
Environment
Secret Store
```

取得。

禁止：

```text
写进源码
写进Prompt生成文件
写进测试
提交Git
打印日志
```

---

# 七、Redis

目标：

```text
REDIS_HOST=192.168.211.239
REDIS_PORT=6380
REDIS_DB=2
REDIS_PASSWORD=<环境变量>
REDIS_KEY_PREFIX=xiaoan3
```

新 Go 后端尽量统一使用一个 Redis DB。

所有 Key 必须有：

```text
xiaoan3:
```

前缀。

设计不得依赖多 Logical DB 隔离，以便未来迁移 Redis Cluster。

---

# 八、飞书配置

飞书 App ID 从环境配置读取。

飞书 App Secret 必须从 Secret / 环境变量读取。

禁止把 App Secret：

```text
写源码

提交Git

返回前端

输出日志
```

---

# 九、普通用户认证

普通用户：

```text
/
↓
检查Studio Session
↓
未登录
↓
自动Feishu OAuth
↓
User Mapping
↓
Studio Session
↓
Workspace
```

不要显示普通用户名密码登录页。

---

# 十、管理员认证

管理员：

```text
/login/admin
```

允许 Local Admin 登录。

Local Admin Password 使用：

```text
Argon2id
```

---

# 十一、Studio Auth 重构

新的 Go Backend 不延续：

```text
JWT localStorage
+
Cookie
```

双重身份方案。

正式改成：

# HttpOnly Opaque Session Cookie

Cookie：

```text
HttpOnly
Secure
SameSite=Lax
```

Session 可存在 Redis。

Session Token 必须使用安全随机数生成。

Redis 中尽量保存 Token Hash，而不是原值。

所有状态修改 API 必须加入 CSRF 防护。

---

# 十二、飞书 UAT

Studio Session 与 Aily Provider Credential 完全分离。

调用 Aily 自定义智能体：

# 必须使用当前用户的 user_access_token。

不要为了方便改成：

```text
tenant_access_token
```

也不要使用：

```text
TAT + user_id
```

模拟用户身份。

当前真实联调已经确认目标 Aily Agent 不接受应用身份。

---

# 十三、Token Storage

Refresh Token：

```text
AES-256-GCM加密后存TiDB
```

Access Token：

```text
Redis TTL Cache
```

禁止在：

```text
Run
Message
RuntimeBinding
Log
```

中保存明文 Token。

---

# 十四、OpenAPI 是唯一 HTTP Contract

建立：

```text
backend-go/api/openapi.yaml
```

作为唯一 API Schema。

从 OpenAPI 生成：

```text
Go server types

TypeScript types/client
```

禁止再次出现：

```text
列表手拼一套payload

详情手拼另一套payload
```

这种 Schema 漂移。

---

# 十五、尽量保持已有 /api/v2 行为

现有 React 前端已经验证大量：

```text
/api/v2
```

行为。

除非目标架构有明确理由，不要无意义修改 URL 和 Payload。

保留：

```text
POST /api/v2/runs

GET /api/v2/runs/{id}

GET /api/v2/runs/{id}/events

GET /api/v2/runs/{id}/stream

POST /api/v2/runs/{id}/commands

GET /api/v2/runs/{id}/artifacts

GET /api/v2/artifacts/{id}/open

GET /api/v2/applications

POST /api/v2/applications

PATCH /api/v2/applications/{id}

DELETE /api/v2/applications/{id}

Application Favorite

Avatar

Default Agent

Runtime Catalog

Runtime Validate
```

现有前端 E2E 是新 Go Backend 的验收依据。

---

# 十六、项目目录

新建：

```text
backend-go/
```

建议：

```text
cmd/
├── api
├── stream
└── worker

api/
└── openapi.yaml

db/
├── migrations
└── queries

internal/
├── platform
├── identity
├── tenancy
├── catalog
├── conversation
├── execution
├── workflow
├── governance
└── integrations
```

不要把所有代码堆：

```text
controllers/
services/
models/
```

三个巨型目录。

采用按 Domain 切模块。

---

# 十七、运行角色

必须提供至少三个可独立启动的角色：

```text
studio-api

studio-stream

studio-worker
```

API：

```text
短请求
```

Stream：

```text
SSE长连接
```

Worker：

```text
Provider执行
```

三者可以独立水平扩展。

---

# 十八、Domain 核心模型

必须正式建立：

```text
User
FeishuIdentity

Application
Provider
ApplicationRuntimeBinding

Conversation
AgentThread
Message

Run
RunEvent
RunCommand
RunLease

RuntimeAttachment
RunArtifact

OutboxEvent
```

---

# 十九、Application / Runtime / Provider / Renderer

严格遵守：

```text
Application ≠ Agent

Application ≠ Runtime

Runtime ≠ Provider

Renderer ≠ Runtime
```

例如：

```text
销售助手
=
Application

agent
=
Runtime Type

feishu_aily
=
Provider

chat
=
Renderer
```

---

# 二十、RuntimeAdapter

设计 Provider-Neutral：

```text
RuntimeAdapter
```

至少：

```text
Submit

Status

Stream

UploadAttachment

ResolveArtifact

CheckVisibility

Capabilities
```

Aily、Codex、GraphFlow、HTTP 都通过 Adapter。

不要在 Catalog / Run Service 里写：

```go
if provider == "aily" { ... }
```

---

# 二十一、统一 Run

所有执行：

```text
Aily Agent

Aily Workflow

Codex

GraphFlow

HTTP

Media
```

统一：

```text
Run
```

不要重新建立：

```text
AilyRun

CodexJob

WorkflowJob
```

各自独立生命周期。

---

# 二十二、Run Queue 使用 Outbox + Redis Streams

创建 Run 必须：

```text
BEGIN

INSERT Message

INSERT Run

INSERT Outbox

COMMIT
```

Outbox Relay：

```text
TiDB
↓
Redis Streams
```

Worker：

```text
XREADGROUP
↓
CAS Claim
↓
Lease
↓
执行
```

Redis Stream 负责性能。

TiDB CAS/Lease 负责正确性。

---

# 二十三、禁止数据库轮询作为正常主队列

可以保留：

```text
Reaper
Fallback Scan
```

但正常任务唤醒不能一直：

```text
SELECT queued
SELECT queued
SELECT queued
```

扫描 TiDB。

---

# 二十四、CAS + Lease

必须实现：

```text
CAS Claim

Lease Heartbeat

Lease Expire

Run Requeue

Retry Policy
```

重复 Redis 消息不得导致重复执行。

---

# 二十五、Aily Rate Limit

Aily Chat 官方限制：

```text
10 requests / second
```

必须实现分布式 Provider Rate Limiter。

不要继续使用容易产生边界 Burst 的普通 fixed window。

使用：

```text
Redis Lua Token Bucket / GCRA
```

同时还要有：

```text
max_inflight
```

控制当前执行数。

两者不是同一个概念。

---

# 二十六、Aily Client

Go Worker Process 内共享：

```text
http.Client
http.Transport
```

配置：

```text
KeepAlive

MaxIdleConns

MaxIdleConnsPerHost

IdleConnTimeout

TLSHandshakeTimeout

ResponseHeaderTimeout
```

不要每个 Run 创建 HTTP Client。

---

# 二十七、Aily 真实兼容行为必须保留

当前真实联调已经发现：

```text
SSE text可能是嵌套对象

Artifact markdown路径可能和artifact entry分处不同content item

Artifact URL只有24h

Aily SSE字段并非完全被文档覆盖
```

Go Event Mapper 必须宽容。

不要因为静态 struct 不匹配就让整个 Run 崩溃。

必要时原始事件：

```text
json.RawMessage
```

先保留，再做安全解析。

---

# 二十八、Aily Session

继续采用：

```text
Lazy Session
```

映射：

```text
AgentThread.remote_id
=
Aily session_id
```

第一次真实发送消息才建立。

不同：

```text
User
Agent
Provider
Auth Subject
```

之间不能错误复用 Session。

---

# 二十九、Aily Chat

映射：

```text
Run.external_run_id
=
agent_chat_id
```

Streaming：

```text
interactive
```

Async/Poll：

```text
background
```

---

# 三十、Final Reconciliation

无论：

```text
SSE正常结束

SSE超时

连接断开
```

最终都必须：

```text
GET Chat Result
```

确认：

```text
Final Text

Status

Finish Reason

Artifact
```

再收敛 Run。

---

# 三十一、Attachment 架构

不要让新的 API Server 直接同步等待大文件上传 Aily。

新的标准流程：

```text
Browser
↓
Studio Storage
↓
RuntimeAttachment
↓
Create Run
↓
Worker
↓
User UAT
↓
Aily Upload
↓
Start Chat
```

Attachment 身份必须钉死：

```text
auth_mode

auth_subject
```

---

# 三十二、Storage

抽象：

```text
Storage
```

实现：

```text
LocalFS
S3Compatible
```

生产不依赖：

```text
backend/media/
```

Avatar、附件、ProjectAsset 等逐步全部归一。

---

# 三十三、Artifact

Aily：

```text
agent_artifact_id
```

是长期引用。

Signed URL 只是缓存。

用户访问：

```text
Studio Artifact ID
↓
权限校验
↓
缓存URL有效?
↓
否则重新Resolve
↓
302
```

---

# 三十四、Realtime

实时路径：

```text
Worker
↓
Redis Pub/Sub
↓
Stream Gateway
↓
Browser
```

持久路径：

```text
Worker
↓
Coalesced content.snapshot
↓
RunEvent
↓
TiDB
```

不要每 Token 插数据库。

---

# 三十五、SSE Replay

Browser 重连：

```text
GET stream
↓
读取TiDB最近RunEvent/content.snapshot
↓
订阅Redis live event
```

保证：

```text
流式快
+
断线可恢复
```

---

# 三十六、前端保留

下面这些前端已经验证，不要无意义重写：

```text
AppShell

DesktopAppShell

MobileAppShell

WorkspaceHost

HomeWorkspace

ApplicationSwitcher

workspaceStore

RunChatPanel

AgentAvatar

Mention Router
```

后端切换时优先保持 UI 行为。

---

# 三十七、Frontend Auth 改造

删除：

```text
JWT localStorage
```

axios/fetch 统一：

```text
credentials / cookie session
```

并加入：

```text
CSRF Header
```

---

# 三十八、Mobile 后续

继续完成：

```text
Bottom Sheet智能体切换

Soft Keyboard

Bottom Composer

Safe Area

Mobile Back Stack

Form Renderer

Page Renderer

Dashboard Renderer
```

但不创建独立 Mobile 项目。

---

# 三十九、Django Admin 替代

不要实现“Go Django Admin”。

管理能力统一进入 React 管理后台：

```text
Provider

Binding

Application

User

Quota

Audit

Runtime Health
```

由 Go Admin API 提供。

---

# 四十、测试原则

不要用 SQLite 证明 TiDB 代码正确。

必须有：

```text
Go Unit Tests

TiDB Integration Tests

Redis Integration Tests

OpenAPI Contract Tests

Aily Mock Tests

Real UAT Opt-in Tests

Playwright E2E
```

---

# 四十一、必须保留的现有真实 E2E 行为

至少验证：

```text
飞书登录

真实用户身份显示

Aily visibility

Aily流式回答

多轮Session

新建会话

输入框清空

工作区切换

草稿恢复

滚动恢复

智能体切换

@Application

智能体头像

Aily附件

Aily产物

Artifact markdown内嵌

24h Artifact URL刷新

设置主智能体

新建Aily引用
```

---

# 四十二、Observability

第一版 Go Backend 就实现：

```text
slog JSON Logs

Trace ID

Run ID

OpenTelemetry

Prometheus
```

不能把 Observability 留到项目最后才补。

---

# 四十三、性能原则

重点优化：

```text
连接复用

异步I/O

goroutine调度

数据库连接池

SQL索引

Redis Pipeline

事件批写

内容Coalesce

零不必要拷贝

附件流式处理

SSE Backpressure
```

不要做没有 Profile 依据的微优化。

使用：

```text
pprof
Benchmark
Load Test
EXPLAIN
```

确认真正瓶颈。

---

# 四十四、数据库性能原则

禁止：

```text
SELECT *

N+1 Query

无限列表

无索引分页

Offset深分页

大量JSON字段扫描

每Token INSERT
```

优先：

```text
明确列

Keyset Pagination

Batch Query

Batch Insert

覆盖索引

EXPLAIN
```

---

# 四十五、运行可靠性优先于纯Benchmark

不能为了高 Benchmark：

```text
牺牲net/http兼容

牺牲事务正确性

牺牲断线恢复

牺牲幂等

牺牲可观测性
```

Creation Agent Studio 的“性能最优”定义是：

```text
高吞吐
低延迟
低资源
可恢复
不丢Run
不重复Run
可水平扩展
```

而不是某个 Hello World Router QPS。

---

# 四十六、实施顺序

严格优先：

```text
1. OpenAPI契约冻结

2. Go Platform

3. 新TiDB Schema + sqlc

4. Identity

5. Catalog / Runtime

6. Execution Core + Outbox + Redis Streams

7. Stream Gateway

8. Aily Agent

9. Storage / Attachment / Artifact

10. Frontend切Go

11. 删除Django

12. Mobile完善

13. Aily Workflow

14. Codex / GraphFlow / HTTP

15. Governance

16. Production Load / Failure Test
```

---

# 四十七、不要做的事情

禁止：

```text
Gin Handler里写业务

使用GORM快速复制Django Model

长期Go+Django双后端

Go调用Django API

为省时间保留Django Admin

继续使用SQLite验证TiDB

继续维护旧AgentTurn/Job体系

直接迁移旧GraphFlow链路

每个Provider建立独立Run模型

每Token落TiDB

Redis作为唯一Run状态

把Aily URL当永久Artifact

把Studio Session和UAT混为一个Token

普通用户使用TAT调用Aily

真实Secret写入源码或Prompt
```

---

# 四十八、每一阶段的工作方式

每个阶段开始前：

```text
阅读现有实现

确认已有行为

写目标设计

写测试

实现

跑Unit

跑Integration

跑E2E

确认真实行为
```

发现现有 Django 设计不好时：

> 不要为了兼容旧代码复制缺陷。

发现现有产品行为已经被真实用户验证时：

> 在没有明确理由的情况下必须保留该行为。

---

# 四十九、代码质量要求

所有 Go Code：

```text
gofmt

go vet

go test ./...

race test关键并发模块

golangci-lint
```

关键并发模块必须考虑：

```text
Race

Context Cancellation

Timeout

Graceful Shutdown

Idempotency

Duplicate Delivery

Retry Storm
```

---

# 五十、最终目标

完成后 Creation Agent Studio 后端应该是：

```text
Go API Control Plane

Go SSE Streaming Plane

Go Execution Plane

TiDB Source of Truth

Redis Realtime / Queue / Coordination

Object Storage

Provider Runtime Adapters
```

前端仍然提供：

```text
统一Main Workspace

多智能体切换

固定应用

PC

Mobile

飞书SSO
```

Aily、Codex、GraphFlow 只是 Runtime Provider。

本项目最终不是：

```text
一个Aily聊天套壳
```

而是：

# 企业 AI Application Runtime Platform。

任何实现决策都围绕这个目标，而不是围绕“如何最快把旧 Django 功能搬过来”。