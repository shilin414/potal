# Creation Agent Studio Go 重构执行计划

## 总原则

这次不是：

```text
Python代码翻译Go
```

也不是：

```text
先凑一个Go版本跑起来再慢慢改
```

而是：

```text
以当前已验证行为作为验收基线
+
按最终目标架构重新实现后端
```

允许投入更多工作换取：

```text
更清晰的架构
更高并发能力
更低资源占用
更强可观测性
更少运行时隐式行为
```

---

# Phase G0：冻结行为契约

在修改前端调用之前完成。

## 工作

把现有前端真正依赖的 API 整理到：

```text
api/openapi.yaml
```

至少包括：

```text
Identity

Session

Applications

Favorites

Runtime Catalog

Runtime Validate

Avatar

Default Agent

Runs

Run Events

Run Stream

Run Commands

Attachments

Artifacts

Artifact Open
```

把当前浏览器 E2E：

```text
e2e_chat_identity.py
e2e_agent_market.py
```

保留下来作为 Go Backend Acceptance Test。

## 目标

以后：

```text
Django返回什么
```

不重要。

重要的是：

```text
OpenAPI定义什么
+
现有前端期待什么
```

## 完成门槛

```text
OpenAPI完整
TypeScript类型能够生成
已有前端编译通过
核心接口有Contract Tests
```

---

# Phase G1：Go Platform Foundation

创建：

```text
backend-go/
```

完成：

```text
Go module

Config

slog

TiDB Pool

Redis Pool

Graceful Shutdown

Health

Metrics

Tracing

Migration Runner

sqlc
```

建立三个入口：

```text
cmd/api

cmd/stream

cmd/worker
```

## 完成门槛

```text
Go API启动

TiDB Ping通过

Redis Ping通过

/health/live 200

/health/ready 200

/metrics 可读取

Graceful Shutdown实测
```

---

# Phase G2：重建数据库 Schema

因为不考虑旧数据迁移：

不要翻译 Django Migration。

直接根据最终 Domain 创建新的 SQL Migration。

首先完成：

```text
users
feishu_identities

providers
applications
runtime_bindings
application_favorites

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
```

然后生成：

```text
sqlc
```

查询代码。

## 完成门槛

在真正 TiDB 上（不使用TIDB8.0.0独有的语法，要适配mysql5.7。）：

```text
Migration up成功

Migration down / forward-fix策略明确

CRUD测试

Transaction测试

CAS测试

Lease测试

JSON测试

Unique约束测试

Index EXPLAIN测试
```

---

# Phase G3：Identity / Security

重新实现：

```text
Feishu OAuth

User Mapping

Feishu Identity

Refresh Token Encryption

UAT Cache

Studio Session

Admin Login

Current Session User
```

正式取消：

```text
JWT localStorage
```

改：

```text
HttpOnly Opaque Session
```

增加：

```text
CSRF

Secure Cookie

Session Revoke

Argon2id Admin Password
```

## 保留行为

普通用户：

```text
/
↓
没有Session
↓
自动飞书OAuth
↓
Workspace
```

管理员：

```text
/login/admin
```

## 完成门槛

真实飞书用户：

```text
OAuth成功

Session刷新页面仍有效

Logout立即失效

管理员登录成功

UAT能够刷新

Aily身份仍是User Identity
```

---

# Phase G4：Catalog / Application Runtime

实现：

```text
Provider

Application

RuntimeBinding

RuntimeRegistry

RuntimeAdapter

Capabilities
```

同时实现现有智能体市场 API。

将原 Django Admin 能力迁入：

```text
Admin API
```

而不是再建立 Go 版“Django Admin”。

## 完成门槛

现有 React `/agents`：

```text
无需业务降级

能创建Aily引用

编辑Agent ID

修改头像

设默认

收藏

Runtime Validate

删除
```

全部通过。

---

# Phase G5：Execution Core

这是整个 Go 后端核心。

实现：

```text
Run

RunEvent

RunCommand

RunLease

Outbox
```

Run 创建：

```text
Transaction
├── User Message
├── Run
└── Outbox
```

实现：

```text
Outbox Relay

Redis Stream Dispatch

CAS Claim

Lease

Heartbeat

Reaper

Retry
```

## 完成门槛

测试：

```text
100个Worker竞争同一个Run
只能1个成功

重复Queue消息
不会重复执行

Redis短暂断开
Run不会丢

Worker执行中kill
Lease到期后可恢复

Outbox积压恢复后可重新发布
```

---

# Phase G6：Realtime / Stream Gateway

建立独立：

```text
cmd/stream
```

实现：

```text
TiDB Event Replay

Redis Pub/Sub

SSE

Keepalive

Backpressure

Slow Client Protection

Reconnect
```

引入：

```text
content.snapshot
```

同时保留：

```text
content.delta
```

## 完成门槛

测试：

```text
浏览器断网

恢复

不重复最终内容

不丢最终内容

终态自动关闭

1000+ SSE连接压力测试
```

实际容量继续向上压，直到发现服务器资源瓶颈。

---

# Phase G7：Aily Agent Native Go Implementation

完全重新实现：

```text
AilyClient

AilyAuth

AilyAgentAdapter

AilyDispatcher

AilyEventMapper

AilyRateLimiter

AilyArtifact

AilyAttachment
```

不要逐行翻译 Python。

必须重新跑已经验证过的真实场景：

```text
Visibility

UAT

Single Turn

Multi Turn

Streaming

Nested text SSE

Attachment

Artifact

Markdown Artifact Reference

Final Reconciliation

24h Artifact URL Refresh
```

## 特别要求

保留现有真实环境发现的兼容性：

```text
Aily text可能是嵌套对象

markdown artifact路径与artifact entry可能跨content item

Aily SSE字段可能出现文档之外的结构
```

Mapper 必须宽容。

## 完成门槛

真实飞书用户完整 E2E 全部通过。

---

# Phase G8：Attachment / Storage 升级

引入统一：

```text
Storage
```

接口。

实现：

```text
LocalFS
S3Compatible
```

Avatar、RuntimeAttachment、ProjectAsset 后续全部走它。

附件改成：

```text
Browser
↓
Studio Storage
↓
Run
↓
Worker
↓
Aily
```

而不是 API Server 同步等待 Provider Upload。

## 完成门槛

```text
40MB附件上传不造成API内存暴涨

重试不会重复创建错误附件

用户A附件不能被用户B执行

Worker上传Aily仍使用原用户UAT
```

---

# Phase G9：Frontend Go Cutover

前端继续保留：

```text
AppShell

WorkspaceHost

HomeWorkspace

RunChatPanel

workspaceStore

MobileAppShell

ApplicationSwitcher
```

不推倒重写。

主要修改：

```text
API Client改为OpenAPI生成

删除JWT localStorage

改Cookie Session

适配content.snapshot

删除Django兼容代码
```

Vite：

```text
/api → Go API

/api/v2/runs/*/stream → Go Stream Gateway
```

## 完成门槛

现有：

```text
tsc
vitest
Playwright
```

全通过。

---

# Phase G10：删除 Django

只有到这一阶段才删。

删除：

```text
backend/
Python venv
requirements
Django migrations
Django Admin
DRF
Channels
asyncio worker
pytest Django tests
```

不要保留：

```text
“临时Django备用”
```

长期双后端只会制造两套真相。

保留必要的历史资料：

```text
docs/archive/django-reference/
```

即可。

---

# Phase G11：Mobile 完整化

继续完成现在未完成的：

```text
Bottom Sheet Agent Switcher

Mobile Keyboard Handling

Bottom Composer

Viewport Resize

Safe Area

Workspace Back Stack
```

并完成：

```text
FormRenderer

PageRenderer

DashboardRenderer
```

Mobile 专属布局。

---

# Phase G12：Aily Workflow + 其他 Provider

按最终 Runtime 模型实现：

```text
AilyWorkflowAdapter

CodexAdapter

GraphFlowAdapter

HTTPAdapter
```

不要移植原 Django GraphFlow legacy chain。

新的 Provider 必须直接进入：

```text
Application
↓
RuntimeBinding
↓
Run
```

---

# Phase G13：治理

实现：

```text
Quota

Audit

Policy

Circuit Breaker

Provider Health

Usage Accounting
```

以及后台运行监控。

---

# Phase G14：性能与故障测试

正式上线前必须进行：

```text
REST Load Test

SSE Connection Test

Redis Stream Backlog Test

Worker Crash Test

Redis Restart Test

TiDB短暂异常 Test

Provider 429 Test

Aily SSE 5min Timeout Test

Slow Client Test

Large Attachment Test
```

重点不只测：

```text
QPS
```

还要测：

```text
Memory / Connection

Queue Recovery

No Lost Run

No Duplicate Run

Event Ordering

Reconnection
```

---

# Phase G15：Production Deployment

最终进程：

```text
studio-api

studio-stream

studio-worker
```

同一个源码仓库、同一套版本。

Nginx：

```text
REST → api

SSE → stream

Static → frontend
```

Worker 可以：

```text
--provider=feishu_aily
```

水平增加实例。

生产采用：

```text
Graceful Shutdown

Readiness

Metrics

Log Rotation

OTel

Resource Limits
```

---

# 不再执行的旧任务

Go 方案落地后以下待办直接删除：

```text
AilyClient Python event loop优化

Django ASGI / Daphne部署

Django Admin增强

SQLite pytest兼容

旧GraphFlow Django Adapter迁移

旧ChatContainer后端兼容

旧Job / AgentTurn迁移
```

它们不属于新的目标架构。

---

# 建议开发优先级

最终排序：

```text
P0
OpenAPI契约
Go基础设施
新Schema
Identity
Execution Core
Aily Agent
Stream Gateway
Frontend Cutover

P1
Object Storage
Mobile完整化
Renderer
Admin Console
Observability

P2
Aily Workflow
Codex
GraphFlow
HTTP Runtime

P3
Quota
Audit
Cost
Evaluation
Automation
```

不要为了尽快“看起来能跑”而：

```text
先Gin+GORM快速抄Django
```

然后未来再重构。

这一次就直接建立最终内核。