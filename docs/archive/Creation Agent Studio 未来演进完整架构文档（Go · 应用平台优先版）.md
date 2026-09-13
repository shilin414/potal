# Creation Agent Studio 未来演进完整架构文档

**文档版本：** Application First / Go Architecture  
**项目名称：** Creation Agent Studio  
**核心定位：** 企业 AI 应用统一入口与 AI Application Runtime Platform  
**当前实施重点：** Application / Aily Agent / Fixed Application / Workspace / PC & Mobile  
**后端：** Go  
**主要数据库：** TiDB 8.0.0  
**最低数据库兼容基线：** MySQL 5.7  
**前端：** React + TypeScript + Vite  
**默认身份体系：** 飞书 OAuth / User Identity  
**当前 Runtime Provider：** Feishu Aily Custom Agent  

---

# 1. 文档目标

Creation Agent Studio 的当前目标不是建设一个“大而全的 Agent 平台”。

第一阶段应首先把：

```text
Application
+
Workspace
+
Aily Agent
+
Fixed Application
+
Identity
+
Execution
+
Artifact
+
PC / Mobile
```

建设完整。

重点解决：

```text
用户怎么进入系统

用户看到什么

如何找到应用

如何快速切换智能体

如何进入固定业务应用

如何从任何应用返回主工作台

如何保持上下文

如何通过本人飞书身份调用Aily

如何稳定运行大量Aily会话

如何管理附件和产物

手机端如何获得原生化体验
```

而不是现在就建设所有可能的 Runtime Provider。

---

# 2. 当前明确不做的范围

本阶段暂不实现：

```text
Aily Workflow

Codex Runtime

GraphFlow Runtime

跨Application Workflow编排

复杂Agent Team

多Agent自动协作

本地Agent执行引擎

Evaluation Platform

复杂Automation Engine
```

这些能力未来可以接入，但：

> 当前架构只需要预留干净的扩展边界，不需要为了未来能力提前实现复杂基础设施。

---

# 3. 当前真正需要支持的 Application

第一阶段只重点支持三类。

## 3.1 AI Chat Application

例如：

```text
创作助手

销售助手

问数小安

采购助手

制度助手
```

当前主要 Runtime：

```text
Feishu Aily Custom Agent
```

表现形式：

```text
ChatRenderer
```

---

## 3.2 Fixed Application

例如：

```text
修改OA密码

条码流向查询

库存查询

员工信息查询

报表查询
```

表现：

```text
PageRenderer
FormRenderer
DashboardRenderer
```

可以完全不经过 AI。

---

## 3.3 HTTP Business Application

某些 Application：

```text
前端页面
+
企业内部HTTP API
```

直接完成任务。

例如：

```text
修改密码

查询条码

提交业务表单
```

仍然属于：

```text
Application
```

而不是另外建立一套产品体系。

---

# 4. 最终产品定位

Creation Agent Studio 定义为：

# Enterprise AI Application Workspace

即：

> 企业员工进入一个统一工作台，在同一个界面里访问 AI 智能体和企业业务应用。

用户不需要理解：

```text
Aily

Runtime

Provider

Run

Adapter

Artifact Resolver
```

用户真正看到的只有：

```text
首页

智能体

应用

会话

收藏

最近使用
```

---

# 5. 最重要的领域边界

必须始终保持：

```text
Application ≠ Agent

Application ≠ Runtime

Runtime ≠ Provider

Conversation ≠ Run

Message ≠ Artifact

Platform ≠ Aily

Studio Session ≠ Aily User Access Token

PC Layout ≠ Mobile Layout
```

但：

```text
PC
+
Mobile
```

共享：

```text
同一个Application

同一个Conversation

同一个Run

同一个API

同一个业务模型
```

---

# 6. 总体架构

```text
                        用户
                         │
                         ▼
                    React SPA
                         │
                 ┌───────┴───────┐
                 │               │
          DesktopAppShell   MobileAppShell
                 │               │
                 └───────┬───────┘
                         ▼
                   WorkspaceHost
                         │
       ┌─────────────────┼─────────────────┐
       ▼                 ▼                 ▼
 HomeWorkspace      ChatRenderer      Page/Form/
                                    DashboardRenderer

                         │
                         ▼
                       API
                         │
                         ▼
                    Go API Server
                         │
          ┌──────────────┼───────────────┐
          ▼              ▼               ▼
        TiDB           Redis       Object Storage
          │              │
          │              ▼
          │         Redis Streams
          │              │
          │              ▼
          │        Execution Worker
          │              │
          │              ▼
          │          Feishu Aily
          │
          └──────────────► Run State

另：

Browser
   │
   │ SSE
   ▼
Stream Gateway
   │
   ├── TiDB Event Replay
   └── Redis Live Events
```

---

# 7. 后端最终技术栈

后端不继续使用 Django。

目标：

```text
Go
```

HTTP 基础：

```text
net/http
+
chi
```

不把 Gin、Fiber 等框架作为整个架构核心。

HTTP Router 只负责：

```text
Routing
Middleware
Request
Response
```

Domain 不依赖任何 Web Framework。

---

# 8. 数据访问技术

采用：

```text
database/sql

go-sql-driver/mysql

sqlc
```

不以 GORM 作为核心数据访问层。

原因：

```text
SQL完全可控

事务边界清晰

TiDB执行计划容易分析

避免N+1隐式问题

CAS/Lease/Outbox更容易实现

性能更可预测

编译期类型安全
```

---

# 9. 数据库兼容目标

主要运行：

```text
TiDB 8.0.0
```

最低兼容：

```text
MySQL 5.7
```

同时自然兼容：

```text
MySQL 8+
```

核心原则：

# 所有核心 SQL 使用 MySQL 5.7 Compatible Subset。

---

# 10. 数据库禁止依赖

核心路径禁止依赖：

```text
MySQL 8 CTE

Window Function

JSON_TABLE

Functional Index

MySQL 8专属Collation

SKIP LOCKED

TiDB Hint

TiFlash专属能力

Placement Rules

TiDB TTL

Stale Read

PostgreSQL语法

RETURNING

Partial Index
```

以后：

```text
TiDB
↓
MySQL 5.7
```

应主要通过：

```text
修改DSN
+
运行Migration
+
数据库验证
```

完成。

不修改 Domain Architecture。

---

# 11. 字符集与命名

统一：

```text
utf8mb4
```

推荐默认：

```text
utf8mb4_bin
```

需要大小写不敏感的业务字段，由应用层：

```text
normalize
```

处理。

表名全部：

```text
lower_snake_case
```

禁止依赖数据库大小写行为。

---

# 12. ID 策略

核心资源使用：

```text
UUIDv7
```

由 Go 应用生成。

例如：

```text
Application

Conversation

Run

Message

Artifact

Attachment
```

数据库推荐：

```text
BINARY(16)
```

API：

```text
UUID String
```

禁止依赖 MySQL 8 的：

```text
UUID_TO_BIN()
```

转换由 Go 完成。

---

# 13. OpenAPI Contract First

新增：

```text
backend-go/api/openapi.yaml
```

它成为：

# API唯一契约。

由 OpenAPI 生成：

```text
Go Request Types

Go Response Types

HTTP Handler Interface

TypeScript API Types

TypeScript API Client
```

避免：

```text
列表接口一个结构

详情接口另一个结构

前端手写interface

新增字段漏改
```

---

# 14. Go 项目结构

推荐：

```text
backend-go/
│
├── cmd/
│   ├── api/
│   │   └── main.go
│   │
│   ├── stream/
│   │   └── main.go
│   │
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
│   │
│   ├── platform/
│   │   ├── config/
│   │   ├── database/
│   │   ├── redis/
│   │   ├── storage/
│   │   ├── logging/
│   │   ├── telemetry/
│   │   ├── crypto/
│   │   └── httpclient/
│   │
│   ├── identity/
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
│   │   ├── artifact/
│   │   ├── attachment/
│   │   ├── lease/
│   │   └── outbox/
│   │
│   ├── governance/
│   │
│   └── integrations/
│       └── aily/
│
├── internal/gen/
│   ├── api/
│   └── db/
│
├── tests/
│   ├── integration/
│   └── contract/
│
├── sqlc.yaml
└── go.mod
```

---

# 15. 三个运行进程

虽然是 Modular Monolith：

```text
一套代码
一个Repository
```

但运行时分三种角色。

---

## 15.1 studio-api

处理：

```text
OAuth

Session

Application API

Catalog

Conversation

Run创建

Application管理

Avatar

Attachment上传入口

Artifact Resolve

Admin API
```

特点：

```text
短请求
```

---

## 15.2 studio-stream

专门处理：

```text
GET /api/v2/runs/{id}/stream
```

职责：

```text
SSE

TiDB历史事件回放

Redis实时事件

Keepalive

Reconnect

Slow Client处理
```

SSE 长连接与普通 API 分离。

---

## 15.3 studio-worker

负责：

```text
Run Dispatch

Aily调用

Aily Streaming

Aily Polling

Attachment上传Aily

Final Reconciliation

Artifact Discovery

Lease

Retry
```

---

# 16. 为什么 API 与 SSE 分开

API 请求通常：

```text
几十毫秒
~
数秒
```

SSE：

```text
几十秒
~
几分钟
```

所以：

```text
REST
```

和：

```text
SSE
```

应该：

```text
独立Connection Pool

独立Timeout

独立实例扩容
```

Nginx：

```text
/api/v2/runs/*/stream
        ↓
studio-stream


其他 /api
        ↓
studio-api
```

---

# 17. Main Workspace

用户成功登录后进入：

# Main Workspace

它看起来像：

```text
ChatGPT
豆包
```

的首页。

但它并不是：

```text
某一个Agent页面
```

它是：

```text
Workspace Shell
```

---

# 18. 首页空闲状态

用户刚进入系统：

```text
HomeWorkspace
```

此时：

```text
不创建Conversation

不创建Run

不创建Aily Session
```

只加载：

```text
默认智能体

常用智能体

常用应用

最近使用

收藏

推荐
```

以及底部：

```text
主输入框
```

---

# 19. 首页产品形态

PC：

```text
┌──────────────────────────────────────────────┐
│                                              │
│             今天想做什么？                   │
│                                              │
│ 常用智能体                                   │
│ [销售助手] [问数小安] [采购助手]            │
│                                              │
│ 常用应用                                     │
│ [OA密码] [条码查询] [报表查询]              │
│                                              │
│ 最近使用                                     │
│ ...                                          │
│                                              │
│        [ 输入内容……              ]           │
└──────────────────────────────────────────────┘
```

---

# 20. WorkspaceHost

前端真正核心：

```text
WorkspaceHost
```

根据当前 Application：

```text
HomeWorkspace

ChatRenderer

PageRenderer

FormRenderer

DashboardRenderer
```

Renderer 只负责：

```text
表现
```

Runtime 负责：

```text
执行
```

---

# 21. Application

Application 是整个产品统一对象。

例如：

```text
销售助手

修改OA密码

条码查询

BI报表
```

建议核心字段：

```text
id

slug

name

description

icon

avatar_storage_key

kind

renderer_key

category_id

is_public

is_default_agent

created_by

enabled

created_at

updated_at
```

---

# 22. Application Kind

当前只需要：

```text
chat

page

form

dashboard
```

暂时不要加入：

```text
workflow
```

作为本轮实现。

以后需要时再增加。

---

# 23. Provider

第一阶段 Provider 只重点实现：

```text
feishu_aily
```

Provider 表保存：

```text
key

name

capabilities

rate_limit_profile

enabled

health_status

config
```

不存：

```text
Secret明文
```

---

# 24. ApplicationRuntimeBinding

Chat Application 通过：

```text
ApplicationRuntimeBinding
```

绑定 Aily。

字段：

```text
id

application_id

runtime_type

provider_key

external_resource_id

identity_mode

execution_mode

session_policy

artifact_policy

capabilities

input_schema

output_schema

config

enabled
```

Aily 示例：

```text
application = 销售助手

runtime_type = agent

provider_key = feishu_aily

external_resource_id = agent_xxxxx

identity_mode = user

execution_mode = interactive

session_policy = lazy

artifact_policy = external_refresh
```

---

# 25. Application 不知道 Aily

业务层不能：

```text
if application.provider == aily
```

Application 只知道：

```text
RuntimeBinding
```

然后：

```text
RuntimeRegistry
↓
RuntimeAdapter
```

调用。

---

# 26. RuntimeAdapter

当前虽然只做 Aily，也必须保留 Runtime Boundary。

例如：

```go
type RuntimeAdapter interface {
    Capabilities() Capabilities

    Submit(
        ctx context.Context,
        req SubmitRequest,
    ) (RunHandle, error)

    Status(
        ctx context.Context,
        handle RunHandle,
    ) (RuntimeStatus, error)

    Stream(
        ctx context.Context,
        handle RunHandle,
    ) (<-chan RuntimeEvent, error)

    UploadAttachment(
        ctx context.Context,
        req AttachmentRequest,
    ) (ProviderAttachment, error)

    ResolveArtifact(
        ctx context.Context,
        ref ArtifactRef,
    ) (ResolvedArtifact, error)

    CheckVisibility(
        ctx context.Context,
        req VisibilityRequest,
    ) (VisibilityResult, error)
}
```

未来增加新 Provider：

```text
只增加Adapter
```

而不是改 Application / Conversation / Run。

---

# 27. 本阶段唯一正式 RuntimeAdapter

当前实现：

```text
AilyAgentAdapter
```

暂时不实现：

```text
AilyWorkflowAdapter

CodexAdapter

GraphFlowAdapter
```

即：

```text
RuntimeRegistry

feishu_aily:agent
        ↓
AilyAgentAdapter
```

---

# 28. 智能体市场

用户进入：

```text
/agents
```

看到所有自己有权限管理/使用的 Chat Applications。

用户可以：

```text
新增Aily智能体引用

修改名称

修改头像

修改Agent ID

修改描述

修改分类

设置公开/私有

设为主智能体

删除

校验可见性
```

---

# 29. “创建Aily智能体”的真实含义

Studio 目前不会：

```text
调用Aily API创建智能体本体
```

而是：

```text
用户在飞书Aily平台已有Agent

↓取得agent_id

Studio创建Application

+

RuntimeBinding
```

因此产品文案可以叫：

```text
添加智能体
```

或：

```text
接入智能体
```

比：

```text
在Aily创建智能体
```

更加准确。

---

# 30. 主智能体

Application 可以：

```text
is_default_agent = true
```

用于：

```text
Home Workspace默认发送目标
```

要求：

```text
全局/租户内只有一个默认Chat Application
```

不依赖 MySQL Partial Unique Index。

由事务实现：

```text
UPDATE applications
SET is_default_agent = 0

UPDATE application
SET is_default_agent = 1
```

---

# 31. 智能体快速切换

Chat Workspace 顶部：

```text
销售助手 ▼
```

PC：

```text
Dropdown
```

Mobile：

```text
Bottom Sheet
```

切换：

```text
active_application
```

而不是：

```text
刷新整个网页
```

---

# 32. Conversation

Conversation 表示：

> 当前用户在某个 Chat Application 下的一段长期会话。

建议：

```text
Conversation
├── id
├── user_id
├── application_id
├── title
├── status
├── created_at
└── updated_at
```

原则：

```text
一个Conversation只属于一个Application
```

当前不要做：

```text
一个Conversation跨多个Agent
```

---

# 33. AgentThread

用于保存 Provider Session：

```text
AgentThread
├── id
├── conversation_id
├── provider
├── remote_id
├── auth_mode
├── auth_subject_key
├── status
└── timestamps
```

Aily：

```text
remote_id
=
session_id
```

---

# 34. Lazy Session

用户只是打开销售助手：

```text
不创建Aily Session
```

第一次发消息：

```text
Create Run
↓
Worker调用Aily Chat
↓
不传session_id
↓
Aily返回session_id
↓
写入AgentThread
```

第二轮：

```text
继续使用session_id
```

---

# 35. Message

Message 保存：

```text
用户真正看到的最终消息
```

例如：

```text
id

conversation_id

run_id

role

content

metadata

created_at
```

RunEvent 不是 Message。

---

# 36. Run

所有 Aily Agent 调用统一使用：

```text
Run
```

字段建议：

```text
id

user_id

application_id

conversation_id

runtime_binding_id

provider

runtime_type

external_run_id

status

provider_status

provider_finish_reason

input

output

runtime_snapshot

priority

available_at

queued_at

started_at

finished_at

error_code

error_message
```

---

# 37. Run 状态

统一：

```text
queued

running

waiting_external

cancelling

cancelled

succeeded

failed

interrupted
```

当前 Aily：

```text
cancel = false
```

所以正常 UI：

```text
不显示真正的取消按钮
```

---

# 38. Run 创建事务

创建一条聊天请求：

```text
BEGIN

INSERT Conversation
    （仅首轮需要）

INSERT User Message

INSERT Run

INSERT Outbox Event

COMMIT
```

保证：

```text
Run和Queue事件不会出现一边成功一边失败
```

---

# 39. Outbox

新增：

```text
outbox_events
```

字段：

```text
id

aggregate_type

aggregate_id

event_type

payload

status

available_at

attempts

created_at

published_at
```

Outbox Relay：

```text
TiDB
↓
Redis Streams
```

---

# 40. Redis Streams

正常 Run 分发不持续轮询：

```text
SELECT queued
```

而使用：

```text
Redis Streams
```

例如：

```text
xiaoan3:queue:feishu_aily
```

Worker：

```text
XREADGROUP
↓
CAS Claim
↓
执行
```

---

# 41. Redis不是Source of Truth

Redis Stream：

```text
只是Dispatch
```

真正状态仍然在：

```text
TiDB
```

即使 Redis 消息：

```text
重复
```

CAS 也必须保证：

```text
一个Run只有一个Worker真正执行
```

---

# 42. RunLease

运行时建立：

```text
RunLease
```

字段：

```text
run_id

worker_id

lease_token

acquired_at

heartbeat_at

expires_at
```

Worker 周期：

```text
heartbeat
```

---

# 43. Worker Crash Recovery

Worker 崩溃：

```text
Lease过期
↓
Reaper检测
↓
Run → interrupted
↓
根据Retry Policy
↓
queued
↓
写新Outbox
↓
重新执行
```

保证：

```text
不丢Run
```

---

# 44. Aily 身份

Aily 自定义智能体调用必须：

# 使用当前用户 UAT。

默认：

```text
identity_mode = user
```

不能：

```text
TAT + user_id
```

伪装成用户。

---

# 45. Studio Session 与 Aily Token

必须完全分离。

```text
Studio Session
```

负责：

```text
用户登录Studio
```

而：

```text
Aily UAT
```

负责：

```text
用户调用Aily
```

不能使用同一个 Token。

---

# 46. Studio Authentication

普通员工：

```text
/
↓
检查Session
↓
没有
↓
Feishu OAuth
↓
User Mapping
↓
Studio Session
↓
Workspace
```

默认不存在普通账号密码登录页面。

---

# 47. Session 方案

Go 后端正式使用：

# HttpOnly Opaque Session Cookie

例如：

```text
studio_session=<random token>
```

Cookie：

```text
HttpOnly

Secure

SameSite=Lax
```

Redis：

```text
hash(session_token)
↓
Session Payload
```

---

# 48. CSRF

使用 Cookie Session 后必须加入：

```text
Origin检查

Referer检查

CSRF Token
```

所有：

```text
POST
PATCH
PUT
DELETE
```

进行保护。

---

# 49. 管理员登录

管理员：

```text
/login/admin
```

使用：

```text
Local Admin Account
```

密码：

```text
Argon2id
```

普通用户界面不展示：

```text
管理员登录
```

入口。

---

# 50. Feishu Identity

建议：

```text
users
```

与：

```text
feishu_identities
```

分开。

User：

```text
id

username

display_name

display_id

avatar_url

auth_source

is_admin

status
```

FeishuIdentity：

```text
user_id

open_id

union_id

feishu_user_id

tenant_key

encrypted_refresh_token

refresh_token_expires_at
```

---

# 51. UAT Cache

Refresh Token：

```text
AES-256-GCM
↓
TiDB
```

Access Token：

```text
Redis TTL Cache
```

Key：

```text
xiaoan3:provider:aily:uat:{user_id}
```

---

# 52. Aily Client

Worker 内共享：

```text
http.Client
```

和：

```text
http.Transport
```

开启：

```text
KeepAlive

Connection Reuse

MaxIdleConns

MaxIdleConnsPerHost

TLSReuse
```

禁止：

```text
每一个Run创建一个新Client
```

---

# 53. Aily Rate Limit

官方已知 Chat Start：

```text
10 requests / second
```

Provider 配置区分：

```text
start_rate_limit

max_inflight

artifact_rate_limit

poll_rate_limit
```

不得把：

```text
Rate
```

和：

```text
Concurrency
```

混为一谈。

---

# 54. 分布式限流

使用：

```text
Redis Lua Token Bucket
```

或：

```text
GCRA
```

实现。

不要简单使用：

```text
固定1秒窗口Counter
```

避免边界 Burst。

---

# 55. Aily Streaming

Interactive Chat：

```text
stream=true
```

调用链：

```text
Aily
 │
 │ SSE
 ▼
Go Worker
 │
 ├── Redis Live Events
 │
 └── Coalesced RunEvent
```

---

# 56. SSE Gateway

浏览器：

```text
GET /api/v2/runs/{run_id}/stream
```

Stream Gateway：

```text
① 从TiDB读取历史RunEvent

② 发送Replay

③ 订阅Redis

④ 推送实时Event

⑤ Run终态自动关闭
```

---

# 57. 实时Event与持久Event分离

不要：

```text
每个Token
↓
INSERT RunEvent
```

实时：

```text
content.delta
↓
Redis
↓
Browser
```

持久：

```text
content.snapshot
```

例如：

```text
每250~500ms
```

或达到字符阈值后写。

---

# 58. content.snapshot

断线恢复不依赖保存每一个 Delta。

例如：

```text
content.delta
A
B
C
D
```

数据库可以只保存：

```text
content.snapshot
ABCD
```

重连：

```text
最近snapshot
+
之后live delta
```

---

# 59. Final Reconciliation

即使 Aily SSE 正常结束：

仍然：

```text
GET Chat Result
```

进行 Final Reconciliation。

确认：

```text
Final Text

Status

Finish Reason

Artifact
```

然后：

```text
Message

Run

RunArtifact
```

最终落库。

---

# 60. Aily SSE异常

包括：

```text
5分钟超时

网络断开

Browser关闭

Aily连接中断
```

都不能直接等价：

```text
Run failed
```

Worker：

```text
GET Chat Result
```

确认真实状态。

---

# 61. Aily Event Mapper

真实联调已经证明 Aily Streaming 数据可能：

```text
和文档描述存在细微差异
```

所以：

```text
Event Mapper
```

必须容错。

先安全解析：

```text
json.RawMessage
```

再映射：

```text
content.delta

artifact.discovered

run.completed
```

未知字段：

```text
忽略但保留日志
```

不能让整个 Run 崩溃。

---

# 62. Attachment

用户文件统一为：

```text
RuntimeAttachment
```

字段：

```text
id

user_id

run_id

application_id

provider

external_attachment_id

storage_key

name

mime_type

size

auth_mode

auth_subject_key

status

created_at
```

---

# 63. Attachment新流程

不再推荐：

```text
Browser
↓
API
↓
同步上传Aily
↓
用户一直等待
```

改为：

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
根据User UAT上传Aily
↓
获得agent_attachment_id
↓
Start Chat
```

---

# 64. 为什么这样更好

这样：

```text
大文件不会长期占用API请求

Provider失败可以重试

Storage Provider-Neutral

后续切Provider不需要重新设计上传

Aily UAT统一在Worker解析
```

---

# 65. Storage

抽象：

```text
Storage
```

接口：

```text
Put

Open

Delete

SignedURL
```

实现：

开发：

```text
LocalFS
```

生产：

```text
S3 Compatible
```

例如：

```text
MinIO
企业对象存储
OSS/S3兼容存储
```

---

# 66. Avatar

Application Avatar：

```text
不要依赖backend/media目录
```

统一：

```text
Storage
```

Application 保存：

```text
avatar_storage_key
```

前端：

```text
/api/v2/applications/{id}/avatar
```

访问。

---

# 67. Artifact

RunArtifact：

```text
id

run_id

provider

external_artifact_id

provider_artifact_type

name

normalized_type

storage_type

cached_external_url

cached_url_fetched_at

cached_url_expires_at

storage_key

resolution_status

metadata
```

---

# 68. Aily Artifact

长期引用：

```text
agent_artifact_id
```

而不是：

```text
24小时URL
```

用户点击：

```text
GET /api/v2/artifacts/{id}/open
↓
权限
↓
URL缓存是否有效
↓
无效则调用Aily
↓
获取新URL
↓
302
```

---

# 69. Artifact Policy

第一阶段只实现：

```text
external_refresh
```

未来按需要再实现：

```text
mirror_on_access

mirror_on_complete
```

当前不要提前增加复杂度。

---

# 70. Page Application

固定应用：

```text
Application
kind = page
renderer_key = xxx
```

页面：

```text
AppShell
↓
WorkspaceHost
↓
PageRenderer
```

不是：

```text
window.location
↓
跳出Studio
```

---

# 71. Form Application

例如：

```text
修改OA密码
```

推荐：

```text
kind = form
renderer_key = oa-password
```

Renderer：

```text
Desktop
→ 宽表单

Mobile
→ 单列全宽表单
```

业务 API：

```text
/api/v2/apps/oa-password/...
```

或后续统一：

```text
Application Action API
```

---

# 72. Dashboard Application

例如：

```text
销售报表
```

PC：

```text
Chart
Table
Filter
```

Mobile：

```text
Summary Card

Card List

Compact Chart
```

禁止把 PC 宽表格直接缩放到手机。

---

# 73. AppShell

前端：

```text
<AppRoot>

  <ResponsiveShell>

      DesktopAppShell
      或
      MobileAppShell

        <WorkspaceHost />

  </ResponsiveShell>

</AppRoot>
```

AppShell：

```text
登录期间之外始终不卸载
```

---

# 74. DesktopAppShell

PC：

```text
Sidebar

TopBar

Workspace

Conversation Rail
```

Sidebar：

```text
首页

智能体

应用

最近会话

收藏
```

---

# 75. MobileAppShell

手机：

```text
TopBar

Drawer

Workspace

Bottom Composer
```

重点：

```text
Safe Area

软键盘

Viewport

手势

返回行为
```

---

# 76. Mobile Agent Switcher

手机不要使用桌面小 Dropdown。

使用：

```text
Bottom Sheet
```

例如：

```text
选择智能体

✓ 创作助手

  问数小安

  销售助手

查看全部 >
```

---

# 77. Mobile Input

聊天输入框：

```text
常驻底部
```

正确处理：

```text
iOS

Android

Feishu WebView

Keyboard Resize

safe-area-inset-bottom
```

---

# 78. Workspace State

前端 workspaceStore 只保存 UI：

```text
activeApplicationId

activeConversationId

draft

scrollPosition

recentApplications

previousWorkspace

sidebarState
```

业务数据：

```text
Message

Conversation

Run

Artifact
```

始终以后端为 Source of Truth。

---

# 79. 切换智能体状态恢复

用户：

```text
销售助手
↓
问数小安
↓
销售助手
```

回来恢复：

```text
原Conversation

原ScrollPosition

原Draft
```

不能重新打开一个空页面。

---

# 80. 固定应用返回

例如：

```text
销售助手
↓
修改OA密码
↓
完成
↓
返回
```

应该回：

```text
销售助手原Workspace
```

而不是：

```text
Home重新加载
```

---

# 81. @Application

主输入框保留：

```text
@销售助手 帮我分析一下客户

@修改OA密码
```

Chat Application：

```text
切到目标智能体
+
携带原Prompt
```

Page/Form：

```text
直接打开对应Application
```

---

# 82. Unknown @

如果：

```text
@不存在
```

则：

```text
不要报错

继续交给默认主智能体处理
```

---

# 83. Application Catalog API

核心：

```text
GET /api/v2/applications
```

支持：

```text
scope

kind

favorite

search

include_unbound
```

返回：

```text
id

slug

name

description

avatar_url

icon

kind

renderer_key

category

is_favorite

is_default_agent

usage_count

last_used_at

capabilities

runtime summary

can_manage
```

统一由 OpenAPI 定义。

---

# 84. Application Authoring API

当前至少支持：

```text
POST /api/v2/applications

GET /api/v2/applications/{id}

PATCH /api/v2/applications/{id}

DELETE /api/v2/applications/{id}
```

以及：

```text
/avatar

/favorite

/default-agent
```

---

# 85. Runtime Catalog

```text
GET /api/v2/runtimes
```

第一阶段可能只返回：

```text
Feishu Aily Custom Agent
```

但返回结构必须 Provider Neutral。

例如：

```json
{
  "provider_key": "feishu_aily",
  "runtime_type": "agent",
  "display_label": "飞书 Aily 自定义智能体",
  "resource_id_label": "Agent ID",
  "identity_modes": ["user"],
  "execution_modes": ["interactive", "background"]
}
```

---

# 86. Runtime Validate

```text
POST /api/v2/runtimes/validate
```

Aily：

```text
检查agent_id

检查Visibility

检查当前UAT
```

无飞书身份：

```text
checked=false
```

而不是把整个 Application 创建流程崩掉。

---

# 87. REST API

第一阶段核心：

```text
Identity
────────────────────────────
GET  /api/identity/oauth/start
GET  /api/identity/oauth/exchange
GET  /api/identity/session
POST /api/identity/logout
POST /api/identity/admin/login


Application
────────────────────────────
GET    /api/v2/applications
POST   /api/v2/applications
GET    /api/v2/applications/{id}
PATCH  /api/v2/applications/{id}
DELETE /api/v2/applications/{id}

POST   /api/v2/applications/{id}/favorite
DELETE /api/v2/applications/{id}/favorite

POST   /api/v2/applications/{id}/default-agent
DELETE /api/v2/applications/{id}/default-agent

GET    /api/v2/applications/{id}/avatar
POST   /api/v2/applications/{id}/avatar
DELETE /api/v2/applications/{id}/avatar


Runtime
────────────────────────────
GET  /api/v2/runtimes
POST /api/v2/runtimes/validate


Conversation
────────────────────────────
GET /api/v2/conversations
GET /api/v2/conversations/{id}


Run
────────────────────────────
POST /api/v2/runs
GET  /api/v2/runs/{id}

GET /api/v2/runs/{id}/events
GET /api/v2/runs/{id}/stream

GET /api/v2/runs/{id}/artifacts


Attachment
────────────────────────────
POST /api/v2/applications/{id}/attachments


Artifact
────────────────────────────
GET /api/v2/artifacts/{id}/open
```

---

# 88. 推荐核心表

第一阶段只需要重点建设：

```text
users

feishu_identities

sessions

application_categories

applications

application_favorites

providers

runtime_bindings

conversations

agent_threads

messages

runs

run_events

run_leases

runtime_attachments

run_artifacts

outbox_events

audit_logs
```

不建：

```text
workflow_runs

workflow_steps

agent_execution

agent_turns

app_runner_jobs
```

---

# 89. 索引设计

至少：

```text
applications
UNIQUE(slug)

applications
(kind, enabled)

runtime_bindings
(application_id, enabled)

conversations
(user_id, application_id, updated_at)

messages
(conversation_id, created_at)

runs
(status, available_at, priority, created_at)

runs
(conversation_id, created_at)

runs
(user_id, created_at)

runs
(provider, external_run_id)

run_events
UNIQUE(run_id, sequence)

run_leases
(expires_at)

runtime_attachments
(user_id, created_at)

run_artifacts
(run_id)

outbox_events
(status, available_at, created_at)
```

所有核心 SQL 上线前：

```text
EXPLAIN
```

验证。

---

# 90. Pagination

禁止深度：

```text
OFFSET 100000
```

核心列表采用：

```text
Keyset Pagination
```

例如：

```text
created_at + id
```

用于：

```text
Conversation

Run

Audit

Application Usage
```

---

# 91. Redis

统一：

```text
Redis DB 2
```

Key Prefix：

```text
xiaoan3:
```

不要继续依赖：

```text
DB 2
DB 3
```

作为架构隔离方式。

为 Redis Cluster 提前兼容。

---

# 92. Redis用途

仅：

```text
Session

UAT Cache

Rate Limit

Redis Streams

Realtime Pub/Sub

Short Cache
```

不是：

```text
业务Source of Truth
```

---

# 93. Observability

Go 第一版就实现：

```text
slog Structured Logging

OpenTelemetry

Prometheus

Trace ID

Run ID
```

---

# 94. 必须观测的指标

至少：

```text
API Request Rate

API Latency

API Error Rate

Active SSE Connections

Run Queue Depth

Run Running

Run Success Rate

Run Failure Rate

Run P50/P95/P99

Outbox Backlog

Redis Stream Lag

Lease Expired

Provider Inflight

Aily Start Rate

Aily 429

Aily Timeout

Aily Reconcile Count

TiDB Query Latency

Redis Latency
```

---

# 95. Health API

```text
/health/live

/health/ready

/metrics
```

Ready：

```text
TiDB

Redis
```

临时 Aily 不可用：

```text
不应该导致整个API实例Not Ready
```

---

# 96. Security

禁止日志输出：

```text
DB Password

Redis Password

App Secret

UAT

TAT

Refresh Token

Authorization Header

Signed Artifact URL敏感参数

Session Token
```

---

# 97. Secret

所有敏感配置：

```text
.env.local

Environment Variables

Secret Store
```

`.env.example`：

```text
只保留字段名
```

---

# 98. Test Strategy

不再用：

```text
SQLite
```

证明数据库兼容。

测试分：

```text
Unit
↓
无数据库


Repository Integration
↓
TiDB 8
+
MySQL 5.7


API Integration
↓
TiDB + Redis


Provider Contract
↓
Mock Aily


Real UAT E2E
↓
真实飞书用户


Frontend E2E
↓
Playwright
```

---

# 99. CI 数据库矩阵

必须：

```text
TiDB 8.x

MySQL 5.7
```

同时跑：

```text
Migration

CRUD

Transaction

CAS

Lease

Outbox

Run Event

JSON

Favorite

Default Agent

Identity

Artifact

Attachment
```

保证未来：

```text
TiDB → MySQL 5.7
```

不是理论兼容。

---

# 100. 性能目标

项目性能重点不是：

```text
Router Hello World QPS
```

而是：

```text
大量SSE连接

大量Aily I/O

Run调度

Redis吞吐

数据库连接

附件

Artifact

低内存

故障恢复
```

---

# 101. Go性能原则

重点：

```text
共享HTTP Client

Connection Pool

goroutine

Context Timeout

Context Cancellation

Batch SQL

Redis Pipeline

Keyset Pagination

Event Coalesce

Stream Attachment

SSE Backpressure

减少Allocation
```

优化必须通过：

```text
pprof

benchmark

load test

EXPLAIN
```

确认。

---

# 102. 当前不需要微服务

不要拆：

```text
User Service

Application Service

Conversation Service

Run Service
```

当前保持：

# Go Modular Monolith

但运行角色：

```text
API

Stream

Worker
```

独立。

这是：

```text
部署隔离
```

不是：

```text
领域微服务
```

---

# 103. Production 拓扑

```text
                         Nginx
                           │
          ┌────────────────┼────────────────┐
          │                │                │
          ▼                ▼                ▼
       React SPA         API Pool       Stream Pool
                            │                │
                            └───────┬────────┘
                                    │
                  ┌─────────────────┼────────────────┐
                  ▼                 ▼                ▼
                TiDB              Redis        Object Storage
                                    │
                                    ▼
                              Redis Streams
                                    │
                                    ▼
                               Worker Pool
                                    │
                                    ▼
                               Feishu Aily
```

---

# 104. 第一阶段实施范围

正式优先：

```text
Go Platform

OpenAPI

TiDB Schema

MySQL 5.7 Compatibility

Identity

Application Catalog

RuntimeBinding

Aily Agent Adapter

Conversation

Run

SSE

Attachment

Artifact

Main Workspace

Agent Market

Fixed Application

PC

Mobile
```

---

# 105. 第一阶段不进入开发主线

明确：

```text
Workflow
Codex
GraphFlow
```

即使旧系统存在：

```text
GraphFlow代码

Agent模型

WorkspacePage旧链路
```

也不要为了兼容它们：

```text
污染新的Go架构
```

这些旧代码只作为：

```text
历史参考
```

---

# 106. Phase 0 — Contract Freeze

首先完成：

```text
OpenAPI 3.1

现有前端API契约

现有E2E行为
```

冻结：

```text
Application API

Identity API

Run API

SSE Event

Artifact API

Attachment API
```

---

# 107. Phase 1 — Go Foundation

建立：

```text
backend-go
```

完成：

```text
Config

Logging

TiDB

Redis

sqlc

Migration

Health

Metrics

Tracing

Graceful Shutdown
```

---

# 108. Phase 2 — 新 Schema

直接建立：

```text
最终Go Schema
```

不翻译：

```text
Django Migration
```

不考虑：

```text
旧Schema兼容
```

---

# 109. Phase 3 — Identity

实现：

```text
Feishu OAuth

User Mapping

Opaque Session

CSRF

UAT Refresh

Admin Login
```

首先保证：

```text
普通员工打开飞书应用
↓
直接Workspace
```

---

# 110. Phase 4 — Application

实现：

```text
Application

Category

Favorite

Provider

RuntimeBinding

Default Agent

Avatar

Runtime Catalog

Runtime Validate
```

让智能体市场首先完全切 Go。

---

# 111. Phase 5 — Execution Core

实现：

```text
Run

RunEvent

RunLease

Outbox

Redis Streams

Worker

Retry

Reaper
```

---

# 112. Phase 6 — Aily

重新用 Go 实现：

```text
AilyClient

Auth

AgentAdapter

Streaming

Polling

Visibility

Attachment

Artifact

Final Reconciliation
```

然后重新跑：

```text
真实UAT E2E
```

---

# 113. Phase 7 — Stream Gateway

实现：

```text
Historical Replay

Redis Live Event

SSE

Reconnect

content.snapshot

Keepalive
```

---

# 114. Phase 8 — Storage

统一：

```text
Avatar

Attachment

未来ProjectAsset
```

到：

```text
Storage abstraction
```

开发：

```text
LocalFS
```

生产：

```text
S3 Compatible
```

---

# 115. Phase 9 — Frontend Cutover

保留当前：

```text
AppShell

WorkspaceHost

RunChatPanel

workspaceStore

ApplicationSwitcher

HomeWorkspace
```

只修改：

```text
API Client → OpenAPI Generated

Auth → Cookie Session

SSE → Go Stream Gateway
```

---

# 116. Phase 10 — Fixed Applications

首先完成几个真实固定应用。

例如：

```text
修改OA密码

条码流向查询
```

用它们验证：

```text
PageRenderer

FormRenderer

Application Action

PC/Mobile
```

完整架构。

---

# 117. Phase 11 — Mobile Complete

完成：

```text
Bottom Sheet Agent Switcher

Bottom Composer

Keyboard Handling

Safe Area

Mobile Back Stack

Form Mobile Layout

Dashboard Mobile Layout
```

---

# 118. Phase 12 — Admin Console

逐步替代：

```text
Django Admin
```

完成：

```text
Application

Provider

RuntimeBinding

User

Run Monitoring

Audit
```

管理界面。

---

# 119. Phase 13 — Performance & Failure Test

必须模拟：

```text
1000+ SSE

大量并发Run

Redis Restart

Worker Kill

TiDB短暂异常

Aily 429

Aily 5分钟SSE Timeout

Slow Browser

Large Attachment

Repeated Redis Messages
```

验收：

```text
不丢Run

不重复Run

不乱序

可恢复

内存稳定

连接数稳定
```

---

# 120. Workflow / Codex / GraphFlow 后续原则

本版本只保留：

```text
RuntimeAdapter
```

这一扩展边界。

未来如果需要：

```text
Codex
GraphFlow
Aily Workflow
```

只允许：

```text
新增Integration Adapter
+
必要的Runtime能力
```

而不能：

```text
重新设计Application

重新设计Conversation

重新设计Run

重新设计Workspace
```

这就是当前预留扩展性的目的。

---

# 121. 最终普通用户体验

用户：

```text
打开飞书
↓
Creation Agent Studio
↓
自动飞书身份登录
↓
Main Workspace
```

首页：

```text
常用智能体

常用应用

最近使用

收藏

推荐

输入框
```

用户可以：

```text
直接问默认智能体

切换智能体

打开固定应用

@某个智能体

@某个应用

继续历史对话

查看AI产物
```

---

# 122. 最终 PC 体验

```text
Sidebar
+
Workspace
+
Agent Switcher
+
Conversation History
```

操作：

```text
像一个企业AI桌面工作台
```

---

# 123. 最终 Mobile 体验

```text
Top Bar

Drawer

Agent Bottom Sheet

Workspace

Bottom Input
```

操作：

```text
像一个真正针对手机设计的AI助手
```

而不是：

```text
压缩后的PC网页
```

---

# 124. 最终开发原则

以后任何实现必须遵守：

```text
Application First

Workspace First

User Experience First

Domain Boundary Clear

TiDB Source of Truth

Redis For Realtime / Queue

Provider Through Adapter

Aily User Identity First

SSE Is Transport, Not Lifecycle

Run Is Execution Truth

Artifact ID Is Durable

PC/Mobile Share Domain

MySQL 5.7 Compatible SQL

No Legacy Architecture Pollution
```

---

# 125. 最终结论

Creation Agent Studio 当前不需要同时建设：

```text
Workflow Platform

Codex Platform

GraphFlow Platform

Multi-Agent Platform
```

应该首先把：

# Application Platform

做完整。

第一阶段最终产品应该做到：

> 用户从飞书进入后，立即来到一个丝滑的 AI 工作台，可以在同一个 Workspace 中快速切换不同 Aily 智能体、进入不同固定业务应用、返回原工作区、继续历史会话，并在 PC 和手机上都获得针对设备设计的体验。

技术底座统一为：

```text
Go

OpenAPI

Application

RuntimeBinding

AilyAgentAdapter

Conversation

Run

RunEvent

RunLease

Outbox

Redis Streams

SSE Gateway

RuntimeAttachment

RunArtifact

TiDB

MySQL 5.7 Compatible SQL

Object Storage
```

当前所有开发决策都应该围绕：

# “把应用这一块做到完整、稳定、快、好用。”

而不是为了未来可能存在的 Provider 或 Workflow 提前增加复杂度。

等：

```text
Application

Aily Agent

Fixed Application

Identity

Workspace

Mobile

Execution
```

全部成熟以后，再以 RuntimeAdapter 为边界增加其他 Provider。

届时新 Provider 应该只是：

```text
新的执行能力
```

而不是一次新的平台架构改造。