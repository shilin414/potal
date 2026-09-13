# Creation Agent Studio 未来演进完整架构文档

**架构版本：** Application First / Go Runtime Architecture  
**当前阶段核心：** Application + Aily Agent + Fixed Application + Schedule + Feishu Delivery  
**暂缓范围：** Workflow / Codex / GraphFlow  
**后端：** Go  
**前端：** React + TypeScript + Vite  
**主数据库：** TiDB 8.0.0  
**最低数据库兼容目标：** MySQL 5.7  
**实时基础设施：** Redis  
**对象存储：** Storage Abstraction，生产环境 S3 Compatible  
**默认企业身份：** Feishu OAuth  
**Aily 调用身份：** User Access Token（UAT）  
**核心产品定位：** 企业 AI Application Runtime Workspace

---

# 1. 项目定位

Creation Agent Studio 不再定位为：

```text
Aily 套壳
```

也不只是：

```text
智能体聊天页面
```

最终定位为：

# 企业 AI 应用统一工作台

用户从飞书进入系统之后，可以在同一个 Workspace 中：

```text
和不同 Aily 智能体对话

使用固定业务应用

使用表单应用

使用报表 / Dashboard 应用

创建定时任务

定时向某个智能体发送消息

将智能体结果自动发送给指定飞书用户或群组

查看历史会话

查看历史任务

查看 AI 生成产物
```

底层：

```text
Application
Runtime
Provider
Run
Schedule
Delivery
Artifact
```

这些技术概念不直接暴露给普通用户。

---

# 2. 当前开发范围

当前阶段优先完成：

```text
Application Platform

Feishu Aily Custom Agent

Fixed Application

Form Application

Dashboard Application

Identity

Conversation

Run

SSE

Attachment

Artifact

Schedule

Feishu Message Delivery

Desktop

Mobile
```

暂时不实现：

```text
Workflow

Codex

GraphFlow

Multi-Agent Workflow

复杂Agent编排

Agent Team

Evaluation Platform
```

但通过：

```text
RuntimeAdapter
DeliveryAdapter
```

保留未来扩展能力。

---

# 3. 总体设计原则

必须长期坚持：

```text
Application ≠ Agent

Application ≠ Runtime

Runtime ≠ Provider

Conversation ≠ Run

Schedule ≠ Run

Run ≠ Delivery

Message ≠ Artifact

Studio Session ≠ Feishu UAT

PC Layout ≠ Mobile Layout
```

同时：

```text
所有执行最终统一进入 Run

所有外部 Runtime 通过 Adapter

所有外部消息发送通过 Delivery Adapter

TiDB 是 Source of Truth

Redis 只负责实时、队列、缓存、协调和限流
```

---

# 4. 总体系统架构

```text
                              用户
                               │
                               ▼
                           React SPA
                               │
                  ┌────────────┴────────────┐
                  │                         │
          DesktopAppShell             MobileAppShell
                  │                         │
                  └────────────┬────────────┘
                               ▼
                         WorkspaceHost
                               │
              ┌────────────────┼────────────────┐
              │                │                │
              ▼                ▼                ▼
        System Workspace   Chat Renderer   Application Renderer
              │                                 │
       ┌──────┴──────┐                    ┌─────┼─────┐
       ▼             ▼                    ▼     ▼     ▼
 HomeWorkspace  ScheduleCenter          Page  Form  Dashboard


                               │
                               ▼
                         Go Backend
                               │
        ┌──────────────────────┼───────────────────────┐
        │                      │                       │
        ▼                      ▼                       ▼
   studio-api            studio-stream          studio-scheduler
        │                      │                       │
        └───────────────┬──────┴───────────────┬──────┘
                        │                      │
                        ▼                      ▼
                      TiDB                   Redis
                        │                      │
                        │              Redis Streams
                        │                      │
                        │                      ▼
                        │                studio-worker
                        │                      │
                        │            ┌─────────┴─────────┐
                        │            ▼                   ▼
                        │      Feishu Aily        Feishu IM API
                        │
                        ▼
                 Object Storage
```

---

# 5. 后端技术选型

正式后端采用：

```text
Go
```

HTTP：

```text
net/http
+
chi
```

数据库：

```text
database/sql
+
go-sql-driver/mysql
+
sqlc
```

Migration：

```text
golang-migrate
```

Redis：

```text
go-redis/v9
```

API Contract：

```text
OpenAPI 3.1
+
oapi-codegen
```

日志：

```text
log/slog
```

Observability：

```text
OpenTelemetry
+
Prometheus
```

---

# 6. 为什么不以 Gin / GORM 为核心

本项目的主要压力不是简单 HTTP Router QPS。

主要压力来源是：

```text
SSE长连接

Aily SSE

大量外部HTTP I/O

Redis

TiDB

Rate Limit

Queue

附件

Artifact

Scheduler

Feishu消息发送
```

因此核心架构使用：

```text
标准 net/http
```

保持：

```text
标准Context

标准HTTP Client

标准Streaming

标准Middleware

标准Tracing
```

数据库也不使用 ORM 隐藏 SQL。

采用：

```text
sqlc
```

保证：

```text
SQL明确

事务明确

性能明确

索引明确

CAS明确

TiDB兼容行为明确
```

---

# 7. Go 项目结构

推荐：

```text
backend-go/

├── cmd/
│   ├── api/
│   ├── stream/
│   ├── scheduler/
│   └── worker/
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
│   │   ├── lease/
│   │   ├── artifact/
│   │   ├── attachment/
│   │   └── outbox/
│   │
│   ├── automation/
│   │   ├── schedule/
│   │   ├── occurrence/
│   │   ├── scheduler/
│   │   └── delivery/
│   │
│   ├── governance/
│   │
│   └── integrations/
│       ├── aily/
│       └── feishu/
│
└── internal/gen/
    ├── api/
    └── db/
```

---

# 8. 四个运行角色

同一个 Go Repository，四种运行角色。

## studio-api

负责：

```text
Authentication

Application

Catalog

Conversation

Run创建

Schedule CRUD

Attachment入口

Artifact Resolver

Admin API
```

---

## studio-stream

专门负责：

```text
SSE
```

例如：

```text
GET /api/v2/runs/{id}/stream
```

职责：

```text
历史RunEvent回放

Redis实时事件

Keepalive

Reconnect

Slow Client
```

---

## studio-scheduler

负责：

```text
扫描到期Schedule

创建ScheduleOccurrence

创建Run

更新next_run_at
```

它：

```text
不调用Aily

不发送飞书消息
```

---

## studio-worker

负责：

```text
Run Worker

Aily Worker

Attachment Worker

Artifact Worker

Delivery Worker
```

第一阶段可以：

```text
一个Binary
多个Consumer Pool
```

以后根据规模拆实例。

---

# 9. 数据库兼容策略

Primary：

```text
TiDB 8.0
```

Minimum Compatibility：

```text
MySQL 5.7
```

Secondary：

```text
MySQL 8+
```

所有核心 SQL：

# 必须属于 MySQL 5.7 Compatible SQL Subset。

禁止核心业务依赖：

```text
CTE

Window Function

JSON_TABLE

Functional Index

SKIP LOCKED

TiDB Hint

TiFlash

Placement Rule

TiDB TTL

Stale Read

MySQL 8-only Collation
```

---

# 10. 数据库迁移原则

这是新架构。

不迁移旧 Django Schema。

不保留：

```text
django_*

AgentExecution

AgentTurn

AppRunnerJob

Legacy Workflow
```

直接建立最终干净 Schema。

---

# 11. ID 策略

核心业务 ID 推荐：

```text
UUIDv7
```

应用层生成。

数据库：

```text
BINARY(16)
```

API：

```text
标准UUID String
```

不依赖：

```text
AUTO_INCREMENT
```

作为跨系统业务身份。

---

# 12. OpenAPI Contract First

正式建立：

```text
backend-go/api/openapi.yaml
```

成为：

# HTTP API 唯一事实来源。

生成：

```text
Go Request / Response

Go Handler Interface

TypeScript Types

TypeScript Client
```

禁止以后再出现：

```text
接口A手拼dict

接口B手拼另一套dict

前端再自己猜类型
```

---

# 13. Application 模型

整个产品核心是：

```text
Application
```

用户看到：

```text
销售助手

创作助手

修改OA密码

条码查询

销售报表
```

全部是 Application。

核心：

```text
Application
├── id
├── slug
├── name
├── description
├── icon
├── avatar_storage_key
├── kind
├── renderer_key
├── category_id
├── is_public
├── is_default_agent
├── created_by
├── enabled
├── created_at
└── updated_at
```

---

# 14. Application Kind

当前支持：

```text
chat

page

form

dashboard
```

暂时不加入：

```text
workflow
```

当前阶段。

---

# 15. Runtime 与 Provider

Chat Application：

```text
Application
↓
ApplicationRuntimeBinding
↓
RuntimeAdapter
↓
Provider
```

例如：

```text
销售助手

runtime_type = agent

provider_key = feishu_aily

external_resource_id = agent_xxxxx
```

---

# 16. ApplicationRuntimeBinding

核心字段：

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

第一阶段：

```text
provider = feishu_aily

runtime_type = agent
```

---

# 17. RuntimeAdapter

统一接口概念：

```text
Capabilities

Submit

Status

Stream

UploadAttachment

ResolveArtifact

CheckVisibility
```

第一阶段只正式实现：

```text
AilyAgentAdapter
```

暂时不实现：

```text
CodexAdapter

GraphFlowAdapter

AilyWorkflowAdapter
```

---

# 18. Provider 不污染业务层

禁止：

```go
if provider == "feishu_aily" {
}
```

散落在：

```text
Application

Conversation

Run

Schedule
```

里面。

Provider 差异只允许存在：

```text
integrations/
RuntimeAdapter
```

---

# 19. Main Workspace

普通用户登录以后：

```text
Main Workspace
```

主页面看起来像：

```text
ChatGPT / 豆包
```

但：

```text
HomeWorkspace
≠
某个固定Agent
```

---

# 20. 首页空闲状态

没有发起对话：

```text
不创建Conversation

不创建Run

不创建Aily Session
```

展示：

```text
常用智能体

常用应用

最近使用

收藏

推荐

主输入框
```

---

# 21. WorkspaceHost

前端：

```text
WorkspaceHost
```

分为：

```text
System Workspace

Application Workspace
```

结构：

```text
WorkspaceHost

├── System Workspace
│   ├── HomeWorkspace
│   └── ScheduleCenter
│
└── Application Workspace
    ├── ChatRenderer
    ├── PageRenderer
    ├── FormRenderer
    └── DashboardRenderer
```

---

# 22. Desktop 一级导航

PC 顶部：

```text
Logo

首页

智能体

应用

定时任务

搜索

用户
```

即：

```text
Top Navigation
├── 首页
├── 智能体
├── 应用
└── 定时任务
```

---

# 23. Desktop Sidebar

PC 左侧 Sidebar 不再重复一级导航。

它负责：

# Context Navigation。

Chat 时：

```text
新建会话

最近会话

历史会话
```

Schedule 时：

```text
全部任务

运行中

已暂停

失败任务
```

---

# 24. Desktop 最终布局

```text
┌───────────────────────────────────────────────────────┐
│ Logo  首页  智能体  应用  定时任务     搜索    用户 │
├──────────────┬────────────────────────────────────────┤
│              │                                        │
│ Context      │                                        │
│ Sidebar      │            WorkspaceHost               │
│              │                                        │
│              │                                        │
└──────────────┴────────────────────────────────────────┘
```

---

# 25. Mobile 一级导航

Mobile：

```text
☰
```

打开 Drawer：

```text
首页

智能体

应用

定时任务

最近会话

收藏

设置
```

即：

```text
Desktop一级导航
=
TopBar

Mobile一级导航
=
Drawer
```

两端读取同一份 Navigation Model。

---

# 26. Mobile 智能体切换

PC：

```text
Dropdown
```

Mobile：

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

# 27. Mobile 输入框

Chat Composer：

```text
固定底部
```

必须兼容：

```text
Feishu Mobile WebView

iOS Keyboard

Android Keyboard

Safe Area

Viewport Resize

Auto Scroll
```

---

# 28. Workspace 状态恢复

workspaceStore 只保存 UI：

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
Conversation

Message

Run

Artifact
```

以后端为准。

---

# 29. Conversation

一个 Conversation：

```text
只属于一个用户

只属于一个Application
```

模型：

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

当前不做：

```text
同一个Conversation自动切多个Agent
```

---

# 30. AgentThread

保存 Provider Remote Session：

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
remote_id = session_id
```

---

# 31. Lazy Session

用户打开智能体页面：

```text
不创建Aily Session
```

第一次发消息：

```text
Run
↓
Aily Chat
↓
Aily返回session_id
↓
AgentThread.remote_id
```

---

# 32. Run

所有真实 AI 执行统一：

```text
Run
```

当前：

```text
Aily Agent调用
=
Run
```

未来其他 Runtime 也仍然进入 Run。

核心：

```text
Run
├── id
├── user_id
├── application_id
├── conversation_id
├── runtime_binding_id
├── provider
├── runtime_type
├── external_run_id
├── trigger_type
├── trigger_id
├── status
├── provider_status
├── provider_finish_reason
├── input
├── output
├── runtime_snapshot
├── priority
├── available_at
├── queued_at
├── started_at
├── finished_at
├── error_code
└── error_message
```

---

# 33. Run Trigger

Run 需要明确记录来源：

```text
manual

scheduled

api

retry
```

例如：

```text
trigger_type = scheduled

trigger_id = occurrence_id
```

这样可以追踪：

```text
这个Run为什么产生？
```

---

# 34. Run 状态

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

Aily 当前：

```text
cancel = false
```

前端不要伪造取消能力。

---

# 35. RunEvent

统一：

```text
run.started

content.delta

content.snapshot

content.completed

artifact.discovered

run.completed

run.failed

run.interrupted
```

---

# 36. 实时事件策略

不要：

```text
每一个Token
↓
INSERT TiDB
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

或累计一定字符
```

再写 TiDB。

---

# 37. SSE

浏览器：

```text
GET /api/v2/runs/{id}/stream
```

Stream Gateway：

```text
TiDB Replay
↓
Redis Subscribe
↓
Live Event
```

Browser Disconnect：

```text
不等于Run取消
```

---

# 38. Final Reconciliation

无论：

```text
Aily SSE正常结束

Aily SSE超时

连接断开
```

最终都：

```text
GET Chat Result
```

确认：

```text
Final Text

Status

Finish Reason

Artifacts
```

然后：

```text
Message

Run

RunArtifact
```

最终落库。

---

# 39. Run 分发

采用：

# Transactional Outbox + Redis Streams + CAS + Lease。

创建：

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

---

# 40. 为什么需要 Outbox

禁止简单：

```text
INSERT Run
COMMIT

然后 Redis XADD
```

否则 Redis 恰好失败：

```text
Run存在
但永远没人执行
```

所以：

```text
Run
+
Outbox
```

必须同事务。

---

# 41. Redis Streams

例如：

```text
xiaoan3:queue:run:feishu_aily
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

Redis：

```text
负责快
```

TiDB：

```text
负责正确
```

---

# 42. RunLease

模型：

```text
RunLease
├── run_id
├── worker_id
├── lease_token
├── acquired_at
├── heartbeat_at
└── expires_at
```

Worker Crash：

```text
Lease Expire
↓
Reaper
↓
Run interrupted
↓
根据策略Requeue
```

---

# 43. Identity

普通用户：

# Feishu SSO Only。

流程：

```text
打开Studio
↓
检查Studio Session
↓
没有
↓
Feishu OAuth
↓
Local User Mapping
↓
Studio Session
↓
Workspace
```

---

# 44. Studio Session

新的 Go Backend 使用：

# HttpOnly Opaque Session Cookie。

不再把：

```text
JWT
```

长期放在：

```text
localStorage
```

Cookie：

```text
HttpOnly

Secure

SameSite=Lax
```

Session：

```text
Redis
```

保存 Token Hash 对应状态。

---

# 45. CSRF

Cookie Session 下：

```text
POST

PUT

PATCH

DELETE
```

必须通过：

```text
Origin / Referer

CSRF Token
```

校验。

---

# 46. 管理员入口

管理员：

```text
/login/admin
```

Local Admin Password：

```text
Argon2id
```

普通用户界面不出现管理员登录入口。

---

# 47. Studio Session 和 Aily UAT

必须完全分离：

```text
Studio Session
=
登录Studio
```

而：

```text
Aily UAT
=
代表该飞书用户调用Aily
```

不能：

```text
Studio Token = Provider Token
```

---

# 48. Aily 必须使用用户身份

Aily 自定义智能体默认：

```text
identity_mode = user
```

必须使用：

```text
user_access_token
```

不能因为后台执行：

```text
改成TAT
```

定时任务同样如此。

---

# 49. FeishuIdentity

建议：

```text
User
+
FeishuIdentity
```

分离。

FeishuIdentity 保存：

```text
open_id

union_id

feishu_user_id

tenant_key

encrypted_refresh_token

refresh_expire_at
```

---

# 50. Token Storage

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

例如：

```text
xiaoan3:provider:aily:uat:{user_id}
```

---

# 51. Aily Client

一个 Worker 进程共享：

```text
http.Client

http.Transport
```

配置连接池。

禁止：

```text
每Run创建一个HTTP Client
```

---

# 52. Aily Rate Limit

必须区分：

```text
chat.start

chat.poll

attachment.upload

artifact.resolve

visibility.check
```

每一个 Operation：

```text
独立Rate Policy
```

不能只有：

```text
Aily = 10/s
```

这种粗粒度。

---

# 53. 分布式接口限频

所有 Worker 共享：

```text
Redis Distributed Rate Limiter
```

推荐：

```text
Redis Lua
+
Token Bucket / GCRA
```

不能每个 Worker 本地：

```text
10 QPS
```

否则 5 个 Worker：

```text
50 QPS
```

会直接超限。

---

# 54. Rate 与 Concurrency

必须分开：

```text
start_rate_limit
```

控制：

```text
每秒开始多少请求
```

而：

```text
max_inflight
```

控制：

```text
同时多少任务正在运行
```

两者独立。

---

# 55. 所有 Run 共享 Provider Limit

不管来源：

```text
用户手动聊天

定时任务

API调用

Retry
```

全部进入：

# 同一个 Provider Admission Control。

不能：

```text
聊天10/s
+
定时任务10/s
```

分别算。

---

# 56. Run Priority

为了防止大量 08:30 定时任务挤死用户实时聊天：

```text
Interactive Run
```

优先于：

```text
Scheduled Run
```

建议：

```text
interactive_user

manual_application

scheduled_high

scheduled_normal

retry
```

但需要：

```text
Priority Aging / Fair Queue
```

防止低优先级永远饿死。

---

# 57. Attachment

新的标准流程：

```text
Browser
↓
Studio Storage
↓
RuntimeAttachment
↓
Run
↓
Worker
↓
User UAT
↓
Aily Upload
↓
Aily Chat
```

API Server 不长时间同步等待 Aily 上传。

---

# 58. Storage

统一：

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

开发：

```text
LocalFS
```

生产：

```text
S3 Compatible
```

Application Avatar、Attachment 等都走同一套 Storage。

---

# 59. Artifact

长期保存：

```text
agent_artifact_id
```

不能把：

```text
Aily 24h URL
```

作为永久资源地址。

用户打开：

```text
/api/v2/artifacts/{id}/open
↓
Permission
↓
检查URL Cache
↓
必要时重新Resolve
↓
302
```

---

# 60. Schedule 定位

Schedule 是独立一级模块。

它不是：

```text
Workflow
```

也不是：

```text
Agent的附属配置
```

产品一级入口：

```text
首页

智能体

应用

定时任务
```

---

# 61. Schedule 的业务定义

用户可以定义：

> 什么时间，以自己的身份，向哪个智能体发送什么消息；智能体回复完成后，是否以自己的飞书身份发送给指定飞书用户或群组。

核心：

```text
Schedule
├── Trigger
├── Invocation
└── Delivery
```

---

# 62. Trigger

定义：

```text
什么时候执行
```

第一阶段支持：

```text
once

daily

weekly

monthly

cron
```

前端普通用户不要直接输入 Cron。

通过 UI 配置，后端生成 Cron / next_run_at。

---

# 63. Invocation

定义：

```text
执行哪个 Application

发送什么内容
```

Schedule 指向：

```text
application_id
```

而不是：

```text
Aily Agent ID
```

因为：

```text
Application
↓
RuntimeBinding
↓
Agent ID
```

才是正确边界。

---

# 64. Schedule Model

建议：

```text
Schedule
├── id
├── owner_user_id
├── name
├── description
├── application_id
├── input_payload
├── schedule_type
├── cron_expression
├── timezone
├── run_at
├── enabled
├── conversation_policy
├── overlap_policy
├── misfire_policy
├── execution_window_seconds
├── deadline_policy
├── next_run_at
├── last_run_at
├── created_at
└── updated_at
```

---

# 65. 用户输入消息

对 Chat Application：

```text
input_payload
```

主要：

```json
{
  "prompt": "请生成昨天的销售日报，并总结异常。"
}
```

底层不要直接把字段命名死成：

```text
message
```

以便以后固定 Application 也能使用：

```text
form_data
parameters
```

---

# 66. Schedule Owner

必须保存：

```text
owner_user_id
```

并遵守：

```text
Schedule Creator
=
Aily Caller
=
Feishu Sender
```

例如吴志彬创建：

```text
调用Aily → 吴志彬UAT

发送飞书 → 吴志彬身份
```

不能后台自动改成应用 TAT。

---

# 67. Conversation Policy

定时任务支持：

```text
new_each_run

reuse
```

默认：

```text
new_each_run
```

避免日报之类长期积累无限 Aily Session Context。

---

# 68. ScheduleOccurrence

Schedule 是定义。

每一次真正触发：

```text
ScheduleOccurrence
```

模型：

```text
id

schedule_id

scheduled_at

enqueued_at

admitted_at

run_id

status

triggered_at

finished_at
```

唯一：

```text
(schedule_id, scheduled_at)
```

防止两个 Scheduler：

```text
重复执行同一个08:30任务
```

---

# 69. Scheduler 执行

```text
Schedule
↓
next_run_at到期
↓
studio-scheduler
↓
CAS
↓
ScheduleOccurrence
↓
Create Run
↓
Outbox
```

Scheduler：

```text
不调用Aily

不发送消息
```

---

# 70. Schedule 和 Run

定时任务执行仍然：

```text
Run
```

例如：

```text
trigger_type = scheduled

trigger_id = occurrence_id
```

绝对不要重新创造：

```text
ScheduledExecution
```

然后又自己拥有一套 AI 生命周期。

---

# 71. Schedule API 限频

用户配置：

```text
08:30
```

代表：

```text
业务期望执行时间
```

不代表：

```text
08:30:00.000必须立即调用Aily
```

真正执行受：

```text
Queue

Priority

Provider Rate Limit

Concurrency

Execution Window
```

控制。

---

# 72. 大量同时间任务

例如：

```text
500个任务
都设置08:30
```

正确：

```text
08:30
↓
500 Occurrences
↓
500 Runs queued
↓
Aily Dispatcher
↓
Distributed Rate Limiter
↓
按限频平滑释放
```

不能瞬间 500 请求打 Aily。

---

# 73. Execution Window

Schedule 可以增加：

```text
execution_window_seconds
```

例如：

```text
计划08:30

允许08:30~08:40内执行
```

用户仍然看到：

```text
每天08:30
```

系统可以在 Provider 限频情况下平滑消化。

---

# 74. Deadline

必要时：

```text
deadline_policy
```

例如：

```text
最晚09:00
```

超过：

```text
skip

execute_anyway
```

由策略决定。

---

# 75. Misfire

Misfire：

```text
08:30 Scheduler本身没运行

09:00恢复
```

策略：

```text
fire_once

skip
```

默认推荐：

```text
fire_once
```

---

# 76. Overlap Policy

如果：

```text
10分钟执行一次
```

上一轮跑了 15 分钟：

```text
skip

queue

parallel
```

第一阶段推荐：

```text
queue
```

或者按 Application 类型配置。

默认不建议：

```text
parallel
```

---

# 77. Schedule Delivery

Schedule 可以配置：

```text
执行完成以后
是否自动发送飞书
```

如果不开：

```text
结果只保留在Studio
```

如果开启：

```text
进入Delivery流程
```

---

# 78. ScheduleDelivery

一个 Schedule 可以有：

```text
0..N
```

个 Delivery Target。

模型：

```text
ScheduleDelivery
├── id
├── schedule_id
├── channel
├── sender_identity_mode
├── target_type
├── target_id
├── target_name
├── content_mode
├── enabled
└── timestamps
```

第一阶段：

```text
channel = feishu

sender_identity_mode = owner_user

target_type = user | chat
```

---

# 79. Delivery Target

用户可以选择：

```text
飞书用户

飞书群组
```

例如：

```text
销售管理群

张三

李四
```

发送身份始终显示：

```text
当前用户本人
```

不允许普通用户：

```text
选择其他人作为发送者
```

---

# 80. DeliveryExecution

每一次 Occurrence 的实际发送建立：

```text
DeliveryExecution
```

字段：

```text
id

occurrence_id

run_id

schedule_delivery_id

sender_user_id

target_type

target_id

status

external_message_id

attempt

error_code

error_message

created_at

sent_at

updated_at
```

---

# 81. Run 和 Delivery 状态分离

例如：

```text
AI成功

飞书发送失败
```

正确：

```text
Run = succeeded

DeliveryExecution = failed
```

不能：

```text
Run = failed
```

因为 AI 已经执行成功。

---

# 82. Delivery 幂等

必须防止：

```text
同一结果
给同一个群发送两次
```

建议唯一：

```text
(occurrence_id, schedule_delivery_id)
```

发送前：

```text
pending
↓ CAS
sending
↓
succeeded
```

---

# 83. Delivery Queue

Run 完成：

```text
Run succeeded
↓
Create DeliveryExecution
↓
Delivery Outbox
↓
Redis Stream
```

例如：

```text
xiaoan3:queue:delivery:feishu
```

Delivery Worker：

```text
Claim
↓
Feishu Rate Limiter
↓
Feishu IM API
```

---

# 84. DeliveryAdapter

消息发送不直接写死到 Scheduler。

定义：

```text
DeliveryAdapter
```

当前：

```text
FeishuDeliveryAdapter
```

未来可以扩：

```text
Email

Webhook
```

但现在只实现飞书。

---

# 85. Feishu Delivery 限频

Aily API 和飞书消息 API：

# 必须是两个独立的 Rate Limit Domain。

例如：

```text
Run
↓
Aily Rate Limiter
↓
Aily


Delivery
↓
Feishu IM Rate Limiter
↓
飞书用户/群组
```

---

# 86. 每个接口独立限频

限频不能只按：

```text
provider
```

要按：

```text
provider + operation
```

例如：

```text
feishu_aily:chat_start

feishu_aily:chat_poll

feishu_aily:attachment_upload

feishu_aily:artifact_resolve

feishu_aily:visibility

feishu_im:message_send
```

---

# 87. ProviderRatePolicy

建议配置：

```text
ProviderRatePolicy
├── provider_key
├── operation
├── rate
├── burst
├── max_inflight
├── retry_policy
└── enabled
```

具体限频值：

```text
配置化
```

不能硬编码到业务逻辑。

---

# 88. 429 Handling

收到：

```text
429
```

不能立即疯狂重试。

必须：

```text
读取Retry-After / Reset

标记operation cooldown

Requeue

Backoff
```

避免：

```text
Retry Storm
```

---

# 89. Schedule Center

定时任务是一级模块：

```text
/schedules
```

子页面：

```text
/schedules/new

/schedules/:id
```

---

# 90. PC Schedule Center

PC 推荐：

```text
左侧任务列表
+
右侧任务详情
```

例如：

```text
┌────────────────────┬────────────────────────────────┐
│ 每日销售日报        │ 每日销售日报                    │
│ 每周经营周报        │                                │
│ 库存异常提醒        │ 销售助手                       │
│                    │ 每天08:30                      │
│                    │                                │
│                    │ Prompt                         │
│                    │ 请生成昨天的销售日报...         │
│                    │                                │
│                    │ 发送到                          │
│                    │ 销售管理群、张三                │
│                    │                                │
│                    │ 最近执行记录                    │
└────────────────────┴────────────────────────────────┘
```

---

# 91. Mobile Schedule Center

Mobile：

```text
定时任务                     ＋

每日销售日报
每天 08:30
销售助手
下一次：明天 08:30
发送到：销售管理群 +2
● 已启用

────────────

每周经营周报
周一 09:00
问数小安
● 已启用
```

点击进入单页详情。

---

# 92. 新建定时任务 UI

推荐分步骤：

```text
Step 1
什么时候执行

Step 2
选择智能体

Step 3
发送什么消息

Step 4
是否发送飞书回复

Step 5
选择飞书用户/群组
```

---

# 93. Schedule 状态展示

用户层：

```text
等待执行

排队中

执行中

发送中

成功

部分失败

失败

已跳过
```

详情可以显示：

```text
计划时间

实际入队

实际开始

AI完成

消息发送完成
```

如果限频等待：

```text
等待原因：Provider限频排队
```

---

# 94. Schedule API

核心：

```text
GET    /api/v2/schedules

POST   /api/v2/schedules

GET    /api/v2/schedules/{id}

PATCH  /api/v2/schedules/{id}

DELETE /api/v2/schedules/{id}

POST   /api/v2/schedules/{id}/enable

POST   /api/v2/schedules/{id}/disable

POST   /api/v2/schedules/{id}/run-now

GET    /api/v2/schedules/{id}/occurrences

GET    /api/v2/schedules/{id}/runs
```

---

# 95. Application API

核心：

```text
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
```

---

# 96. Runtime API

```text
GET /api/v2/runtimes

POST /api/v2/runtimes/validate
```

第一阶段目录里主要：

```text
Feishu Aily Custom Agent
```

---

# 97. Run API

```text
POST /api/v2/runs

GET /api/v2/runs/{id}

GET /api/v2/runs/{id}/events

GET /api/v2/runs/{id}/stream

GET /api/v2/runs/{id}/artifacts
```

---

# 98. Attachment / Artifact API

```text
POST /api/v2/applications/{id}/attachments

GET /api/v2/artifacts/{id}/open
```

---

# 99. 核心数据库表

第一阶段：

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

schedules

schedule_occurrences

schedule_deliveries

delivery_executions

audit_logs
```

---

# 100. 当前不建的表

暂时不建立：

```text
workflow_steps

workflow_runs

codex_sessions

graphflow_runs

agent_execution

agent_turns

legacy_jobs
```

---

# 101. 关键数据库索引

至少：

```text
applications
UNIQUE(slug)

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
(provider, external_run_id)

run_events
UNIQUE(run_id, sequence)

run_leases
(expires_at)

outbox_events
(status, available_at, created_at)

schedules
(enabled, next_run_at)

schedule_occurrences
UNIQUE(schedule_id, scheduled_at)

delivery_executions
UNIQUE(occurrence_id, schedule_delivery_id)
```

---

# 102. Pagination

核心长列表：

```text
Conversation

Run

Schedule History

Audit
```

采用：

```text
Keyset Pagination
```

避免深 Offset。

---

# 103. Redis Key Namespace

统一：

```text
xiaoan3:
```

例如：

```text
xiaoan3:session:...

xiaoan3:provider:aily:uat:...

xiaoan3:queue:run:feishu_aily

xiaoan3:queue:delivery:feishu

xiaoan3:rate:feishu_aily:chat_start

xiaoan3:rate:feishu_im:message_send

xiaoan3:run:{id}:live
```

---

# 104. Redis Logical DB

新架构尽量：

```text
统一一个Redis DB
```

通过 Key Namespace 隔离。

不要长期依赖：

```text
DB 2

DB 3
```

区分模块，以便未来兼容 Redis Cluster。

---

# 105. Observability

第一版就必须实现：

```text
Structured Logging

Trace

Metrics
```

所有日志至少关联：

```text
trace_id

request_id

user_id

run_id

schedule_id

occurrence_id
```

按实际上下文存在。

---

# 106. 核心指标

至少：

```text
HTTP Latency

HTTP Error Rate

Active SSE

Run Queue Depth

Run Success Rate

Run P95

Outbox Backlog

Redis Stream Lag

Lease Expiration

Schedule Delay

Schedule Misfire

Aily Inflight

Aily 429

Aily Timeout

Feishu Delivery Rate

Feishu Delivery Failure

Delivery 429

TiDB Latency

Redis Latency
```

---

# 107. Schedule 特有指标

特别监控：

```text
schedule_trigger_delay_seconds

schedule_queue_delay_seconds

schedule_run_duration_seconds

schedule_delivery_duration_seconds

schedule_misfire_total

schedule_overlap_skipped_total
```

以后能直接回答：

> 为什么用户设置 08:30，08:31 才收到？

---

# 108. Health

提供：

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

Aily 暂时不可用不应该导致：

```text
整个API Pod Not Ready
```

Provider Health 单独监控。

---

# 109. 管理后台

不再依赖：

```text
Django Admin
```

正式建设：

```text
React Admin / Enterprise Console
```

管理：

```text
Application

Provider

RuntimeBinding

User

Schedules

Runs

Rate Policy

Audit

Provider Health
```

---

# 110. Security

禁止日志输出：

```text
DB Password

Redis Password

App Secret

User Access Token

Tenant Access Token

Refresh Token

Authorization Header

Session Token

Signed Artifact URL敏感参数
```

---

# 111. Test Strategy

禁止使用：

```text
SQLite
```

证明数据库兼容。

测试：

```text
Unit
↓
无数据库


Integration
↓
TiDB 8


Compatibility Integration
↓
MySQL 5.7


Redis Integration

API Contract Test

Mock Aily Test

Real UAT Test

Feishu Delivery Test

Playwright E2E
```

---

# 112. CI Matrix

至少：

```text
TiDB 8

MySQL 5.7
```

同时测试：

```text
Migration

Transaction

CAS

Lease

Outbox

JSON

Schedule

Occurrence Unique

Delivery Idempotency

Run Event

Application

Identity
```

---

# 113. 性能测试重点

不重点追求：

```text
Hello World Router Benchmark
```

真正测试：

```text
1000+ SSE

大量Scheduled Run

大量同一时间Schedule

Provider限频

Redis Stream Backlog

Worker Crash

Delivery Retry

Redis Restart

TiDB短暂异常

Large Attachment

Slow Browser
```

---

# 114. 第一阶段开发顺序

## Phase 0

```text
OpenAPI Contract
```

冻结现有产品行为。

---

## Phase 1

```text
Go Platform Foundation
```

完成：

```text
Config

TiDB

Redis

Logging

Metrics

Tracing

Health

Graceful Shutdown
```

---

## Phase 2

建立：

```text
最终新Schema
```

并完成：

```text
sqlc
```

---

## Phase 3

完成：

```text
Identity

Feishu OAuth

Opaque Session

UAT Refresh

Admin Login
```

---

## Phase 4

完成：

```text
Application

Provider

RuntimeBinding

Runtime Catalog

Agent Market

Default Agent

Avatar

Favorite
```

---

## Phase 5

完成：

```text
Run

RunEvent

RunLease

Outbox

Redis Streams

Worker
```

---

## Phase 6

完成：

```text
Aily Agent Adapter

Streaming

Polling

Attachment

Artifact

Visibility

Final Reconciliation
```

并重新跑真实 UAT。

---

## Phase 7

完成：

```text
Stream Gateway

SSE Replay

content.snapshot

Reconnect
```

---

## Phase 8

完成：

```text
Storage

Attachment异步上传

Avatar Storage
```

---

## Phase 9

完成：

```text
Schedule

ScheduleOccurrence

Scheduler

Misfire

Overlap

Execution Window
```

---

## Phase 10

完成：

```text
ScheduleDelivery

DeliveryExecution

FeishuDeliveryAdapter

Delivery Queue

Delivery Rate Limit

Delivery Retry
```

---

## Phase 11

前端切换：

```text
OpenAPI Generated Client

Cookie Session

Go SSE

Schedule Center
```

---

## Phase 12

完成：

```text
Fixed Application

FormRenderer

DashboardRenderer
```

---

## Phase 13

完成：

```text
Mobile Bottom Sheet

Mobile Keyboard

Bottom Composer

Schedule Mobile UI

Workspace Back Stack
```

---

## Phase 14

完成：

```text
Admin Console

Rate Policy管理

Run监控

Schedule监控
```

---

## Phase 15

做：

```text
Load Test

Failure Test

Security Test
```

---

# 115. 当前暂缓内容

以下不进入当前主线：

```text
Workflow

Codex

GraphFlow
```

旧 Django 中相关代码：

```text
只作为历史参考
```

不迁到新 Go 架构。

---

# 116. 未来接 Provider 的原则

未来增加：

```text
Codex

GraphFlow

Aily Workflow
```

只能通过：

```text
RuntimeAdapter
```

增加执行能力。

不能要求重新设计：

```text
Application

Conversation

Run

Schedule

Workspace
```

---

# 117. Schedule 与 Workflow 的区别

必须明确：

```text
Schedule
=
什么时候执行
```

而：

```text
Workflow
=
执行什么步骤以及步骤关系
```

当前：

```text
Schedule
↓
Application
↓
Run
```

未来可能：

```text
Schedule
↓
Workflow
↓
多个Run
```

所以：

```text
当前做Schedule
```

完全不需要：

```text
当前做Workflow
```

---

# 118. 最终用户体验

普通员工：

```text
飞书
↓
Creation Agent Studio
↓
自动飞书登录
↓
Main Workspace
```

可以：

```text
直接向主智能体提问

切换不同Aily智能体

打开固定应用

使用业务表单

查看报表

创建定时任务

配置每天几点向哪个智能体发送什么内容

配置是否将AI回复自动发送给指定飞书用户或群组

查看每次执行和发送情况

继续历史会话

查看AI产物
```

---

# 119. 最终 Desktop 产品结构

```text
顶部一级导航

首页
智能体
应用
定时任务
```

左侧：

```text
根据当前模块显示Context Sidebar
```

中央：

```text
WorkspaceHost
```

---

# 120. 最终 Mobile 产品结构

Top Bar：

```text
☰
页面标题 / 当前智能体
头像
```

Drawer：

```text
首页

智能体

应用

定时任务

最近会话

收藏

设置
```

Chat：

```text
底部Composer
```

Agent Switch：

```text
Bottom Sheet
```

Schedule：

```text
移动端单列任务卡片
```

---

# 121. 最终架构核心

Creation Agent Studio 当前最终核心可以总结成：

```text
Application
+
Workspace
+
Identity
+
Conversation
+
Run
+
Aily Runtime
+
Schedule
+
Delivery
+
Artifact
```

其中：

```text
Application
=
用户要使用什么能力


Run
=
这个能力的一次真实执行


Schedule
=
什么时候自动创建Run


Delivery
=
Run完成以后结果要发给谁
```

这是整个架构最核心的四层语义。

---

# 122. 最终一句话定位

Creation Agent Studio 最终不是：

> 一个可以聊天的 Aily 门户。

而是：

# 一个以 Application 为核心、以 Run 为统一执行模型、支持用户身份 AI 调用、定时自动执行和飞书结果分发的企业 AI 工作台。

当前优先把：

```text
Application

Aily Agent

Fixed Application

Schedule

Feishu Delivery

Desktop

Mobile
```

做到完整、稳定、高性能。

Workflow、Codex、GraphFlow 等能力以后再以扩展方式加入，而不是影响当前核心平台的设计和交付。