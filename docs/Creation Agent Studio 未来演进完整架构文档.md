# Creation Agent Studio 未来演进完整架构文档

**项目名称：** Creation Agent Studio  
**文档类型：** 目标架构 / To-Be Architecture  
**目标定位：** 企业 AI Application Runtime Portal  
**前端形态：** PC + Mobile 双端交互布局，共享业务与 API  
**认证体系：** 飞书 SSO 为默认用户入口，本地 Admin 登录作为管理入口  
**目标数据库：** TiDB 8.0.0 / MySQL 8  
**核心 Runtime：** 飞书 Aily 自定义智能体、Aily 工作流、Codex、GraphFlow、固定页面、HTTP 应用、企业内部服务及其他 Agent Provider  
**总体架构：** Modular Monolith + Independent Execution Plane + Unified Runtime Protocol + Unified Run Model + Unified Workspace Shell

---

# 1. 文档目标

本文描述 Creation Agent Studio 后续完整目标架构。

项目不推倒重写，而是在现有：

```text
Django
React
Application
Agent
Conversation
Workflow
AgentAdapter
AgentThread
App Runner
JobEvent
```

基础上逐步演进。

目标是让平台长期支持：

```text
飞书 Aily 自定义智能体
飞书 Aily Workflow
Codex
GraphFlow
企业内部 Agent
固定业务页面
HTTP API 应用
报表应用
视频生成
未来其他 Agent 产品
```

并满足：

```text
PC / 手机统一体验

飞书免登录

多智能体切换

应用快捷入口

会话恢复

附件

产物

流式输出

并发控制

统一执行追踪

TiDB / MySQL

水平扩容

企业级审计
```

---

# 2. 最终产品定位

Creation Agent Studio 不应定位为：

```text
Aily 套壳
```

也不应只是：

```text
Agent Chat UI
```

而应定位为：

# 企业 AI Application Runtime Portal

平台自己管理：

```text
Identity
Application Catalog
Workspace
Conversation
Runtime
Provider
Workflow
Execution
Artifact
Governance
UI Runtime
Audit
Observability
```

而：

```text
Aily
Codex
GraphFlow
HTTP Service
Media Service
其他 Agent 平台
```

全部属于：

# Runtime Provider

---

# 3. 用户体验定位

Creation Agent Studio 的用户体验核心不是“进入一个应用中心”。

而是：

# 用户首先进入一个像 ChatGPT / 豆包一样的主工作台。

主页面天然呈现为一个：

```text
Chat-like Workspace
```

但它并不只属于某个智能体。

它是整个系统的：

# Main Workspace / Home Workspace

---

# 4. 主页面默认状态

用户第一次进入时：

```text
Main Workspace
status = idle
```

此时：

```text
不创建 Conversation
不创建 Run
不创建 Aily Session
```

只展示：

```text
常用智能体
常用应用
最近使用
收藏
快捷入口
主输入框
```

例如：

```text
┌─────────────────────────────────────────────────────┐
│                                                     │
│                  今天想做什么？                     │
│                                                     │
│   常用智能体                                         │
│   [销售助手] [采购助手] [制度助手] [数据助手]      │
│                                                     │
│   常用应用                                           │
│   [修改OA密码] [条码流向] [销售报表] [费用查询]    │
│                                                     │
│                                                     │
│        [ 输入内容……                     ]           │
│                                                     │
└─────────────────────────────────────────────────────┘
```

只有用户真正：

```text
发送第一句话
```

或者：

```text
打开某个智能体
```

才 Lazy Create：

```text
Conversation
Aily Session
Run
```

---

# 5. 主页面不是主 Agent 页面

必须明确：

```text
Home Workspace
≠
Main Agent
```

主页面是：

```text
Workspace Shell
```

而当前智能体只是：

```text
active_application
```

用户可以随时：

```text
主助手
↓
销售助手
↓
采购助手
↓
制度助手
```

页面整体不离开。

只改变：

```text
active_application_id
RuntimeBinding
Conversation
```

---

# 6. Application 是整个系统统一入口

不管用户打开的是：

```text
Aily智能体
Aily Workflow
固定页面
HTTP应用
报表
视频生成
```

在产品层都统一定义为：

# Application

用户无需理解：

```text
Agent
Workflow
Provider
HTTP
Renderer
```

这些技术概念。

---

# 7. 四层核心模型

未来必须明确分离：

```text
Application
Runtime
Provider
Renderer
```

---

# 8. Application

Application 表示：

> 用户看到的一个业务应用。

例如：

```text
销售助手
采购助手
修改OA密码
条码流向查询
制度查询
合同分析
视频生成
```

负责：

```text
名称
图标
描述
分类
权限
快捷入口
收藏
搜索
使用统计
```

---

# 9. Runtime

Runtime 表示：

> 这个 Application 如何执行。

例如：

```text
agent
workflow
http
local_task
media
none
```

---

# 10. Provider

Provider 表示：

> 谁真正提供执行能力。

例如：

```text
feishu_aily
codex
graphflow
custom_http
internal
openai
dify
coze
```

---

# 11. Renderer

Renderer 表示：

> 用户看到的交互界面。

例如：

```text
chat
form
page
task
workflow
dashboard
external_page
```

最终：

```text
Application
├── Product Definition
├── Runtime Binding
└── Renderer
```

---

# 12. ApplicationRuntimeBinding

建议新增：

```text
ApplicationRuntimeBinding
```

核心字段：

```text
id

application_id

runtime_type

provider_key

external_resource_id

endpoint_key

input_schema
output_schema

capabilities

config

secret_ref

identity_mode

execution_mode

session_policy

artifact_policy

timeout_seconds

enabled

created_at
updated_at
```

---

# 13. Aily Agent Application 示例

```text
Application
name = 销售助手

renderer_key = chat
```

对应：

```text
ApplicationRuntimeBinding

runtime_type = agent

provider_key = feishu_aily

external_resource_id = agent_xxxxx

identity_mode = user

execution_mode = interactive

session_policy = lazy

artifact_policy = external_refresh
```

---

# 14. RuntimeAdapter

现有 AgentAdapter 可以继续保留。

上层新增：

```text
RuntimeAdapter
```

统一不同 Runtime 类型。

建议能力：

```text
submit()

get_status()

stream_events()

cancel()

resume()

upload_attachment()

resolve_artifact()

check_visibility()
```

---

# 15. Capability Driven

不是每个 Provider 都支持所有操作。

统一声明：

```text
streaming

async_execution

conversation

attachment

artifact

cancel

resume

human_input

visibility

file_upload
```

例如当前 Aily 自定义智能体：

```text
streaming       = true
async_execution = true
conversation    = true
attachment      = true
artifact        = true
visibility      = true

cancel          = false
resume          = false
```

前端必须根据 Capability 决定功能是否显示。

---

# 16. RuntimeRegistry

建立：

```text
RuntimeRegistry
```

例如：

```text
feishu_aily_agent

feishu_aily_workflow

codex

graphflow

custom_http

local_task

page
```

业务代码不要到处出现：

```python
if provider == "aily":
```

统一由 Registry 查 Adapter。

---

# 17. Workspace 是前端核心

未来前端最重要的组件不是：

```text
ChatPage
```

而是：

# WorkspaceHost

总体：

```text
<AppRoot>

    <ResponsiveShell>

        DesktopAppShell
        或
        MobileAppShell

            ↓

        <WorkspaceHost />

    </ResponsiveShell>

</AppRoot>
```

---

# 18. WorkspaceHost

WorkspaceHost 根据当前 Application 决定渲染：

```text
HomeWorkspace

ChatRenderer

FormRenderer

PageRenderer

WorkflowRenderer

TaskRenderer

DashboardRenderer

ExternalPageRenderer
```

因此：

```text
Chat
```

只是其中一种 Workspace。

---

# 19. PC 端整体布局

PC 端推荐：

```text
┌───────────────────────────────────────────────────────┐
│ Logo / 搜索 / 当前应用 / 用户                         │
├───────────────┬───────────────────────────────────────┤
│               │                                       │
│ 侧边栏        │          WorkspaceHost                │
│               │                                       │
│ 首页          │  Home / Chat / Page / Workflow       │
│ 智能体        │                                       │
│ 应用          │                                       │
│ 最近会话      │                                       │
│ 收藏          │                                       │
│               │                                       │
└───────────────┴───────────────────────────────────────┘
```

Sidebar 可以常驻。

---

# 20. 手机端必须单独设计

手机端不应该：

```text
把PC页面压窄
```

而应该使用：

# MobileAppShell

共享业务逻辑，但交互布局单独优化。

---

# 21. 手机端总体布局

推荐：

```text
┌─────────────────────────┐
│ 当前智能体 ▼         ☰  │
├─────────────────────────┤
│                         │
│                         │
│     WorkspaceHost       │
│                         │
│                         │
├─────────────────────────┤
│ ＋   输入内容……     发送 │
└─────────────────────────┘
```

---

# 22. 手机端 Drawer

PC Sidebar 在手机端变成：

```text
Drawer
```

内容：

```text
新对话

首页

最近会话

智能体

应用中心

收藏

个人信息
```

不常驻占屏幕宽度。

---

# 23. 手机端智能体切换

PC 可以：

```text
销售助手 ▼
```

使用 Dropdown。

手机应该使用：

```text
Bottom Sheet
```

例如：

```text
选择智能体

✓ 主助手

  销售助手

  采购助手

  制度助手

查看全部 >
```

更符合移动端习惯。

---

# 24. PC 与 Mobile 共用 WorkspaceHost

架构：

```text
Shared Business Layer
        │
        ▼
WorkspaceHost
        │
 ┌──────┴──────┐
 │             │
Desktop       Mobile
Shell         Shell
```

因此不要创建：

```text
PC React 项目
+
Mobile React 项目
```

---

# 25. Renderer 双端策略

例如：

```text
ChatRenderer
├── DesktopChatLayout
└── MobileChatLayout
```

固定页面：

```text
PageRenderer
├── DesktopPageLayout
└── MobilePageLayout
```

Dashboard：

```text
DashboardRenderer
├── DesktopDashboard
└── MobileDashboard
```

共享：

```text
API

Hooks

Conversation Logic

Run Logic

Artifact Logic
```

只拆：

```text
布局
交互方式
```

---

# 26. 手机端表格降级

PC：

```text
客户 | 销售额 | 区域 | 同比 | 负责人
```

手机不应该硬塞。

应转成：

```text
客户A

销售额：100万

区域：华东

同比：+12%

负责人：张三
```

即：

```text
Desktop = Table

Mobile = Card List
```

---

# 27. 移动端输入框

手机 Chat 输入框必须：

```text
常驻底部
```

并处理：

```text
iOS Safari

Android

飞书 WebView

软键盘

Safe Area

Viewport Height

滚动位置
```

重点避免：

```text
输入框被键盘遮挡

页面跳动

SSE输出时自动滚动异常
```

---

# 28. Workspace 状态恢复

为实现丝滑切换，需要单独：

```text
workspaceStore
```

只保存 UI 状态：

```text
active_application_id

active_conversation_id

previous_workspace

draft

scroll_position

sidebar_state

recent_applications
```

而：

```text
Conversation
Message
Run
Artifact
```

仍然以后端数据库为准。

---

# 29. 切换智能体不能丢状态

例如：

```text
销售助手
```

用户看到第 20 条消息。

切到：

```text
采购助手
```

再回来。

应该恢复：

```text
Conversation

Scroll Position

Draft

Artifact State
```

用户感知：

> 只是切换了一个工作区。

---

# 30. 不同 Agent 必须有不同 Conversation

例如：

```text
销售助手
    ↓
Conversation A
    ↓
Aily Session A

采购助手
    ↓
Conversation B
    ↓
Aily Session B
```

不能多个 Agent 共享一个 Aily Session。

---

# 31. 智能体快速切换

Chat 页面顶部建议：

```text
销售助手 ▼
```

点击快速切换：

```text
最近使用

✓ 销售助手

  采购助手

  制度助手

全部智能体 >
```

用户不需要先：

```text
返回应用中心
↓
找到智能体
↓
重新打开
```

---

# 32. 固定应用在同一个 Shell 中打开

例如：

```text
修改OA密码
```

点击后：

```text
AppShell
  ↓
WorkspaceHost
  ↓
PageRenderer
```

而不是：

```text
window.location
```

整页跳转。

用户完成以后：

```text
返回主页面
```

恢复之前 Workspace 状态。

---

# 33. SPA 路由建议

例如：

```text
/
→ Main Workspace

/chat/:applicationSlug
→ Chat Application

/app/:applicationSlug
→ Fixed Application

/workflow/:applicationSlug
→ Workflow Application
```

最外层：

```text
AppShell
```

始终不卸载。

---

# 34. Workspace Navigation Stack

尤其手机端不能只依赖浏览器 Back。

建议维护：

```text
WorkspaceNavigationStack
```

例如：

```text
Home

↓
销售助手

↓
OA密码

↓ Back

销售助手

↓ Back

Home
```

避免手机飞书 WebView Back 直接退出应用。

---

# 35. Home Workspace 快捷入口

主页面推荐展示：

```text
常用

最近使用

收藏

推荐
```

这些资源全部是：

```text
Application
```

不再区分：

```text
智能体快捷方式

应用快捷方式

工作流快捷方式
```

---

# 36. @ Application 路由

主输入框可以支持：

```text
@销售助手

@修改OA密码

@条码流向查询
```

输入：

```text
@销售助手 帮我分析一下这个客户
```

系统：

```text
解析 Application
↓
切换 active_application
↓
保留原始指令
↓
发送给销售助手
```

---

# 37. @ 固定页面

例如：

```text
@修改OA密码
```

系统：

```text
Chat Workspace
↓
Page Workspace
```

直接打开固定 Application。

---

# 38. 未知 @

例如：

```text
@测试一下
```

如果找不到 Application：

```text
继续由默认主 Agent 处理
```

不能直接报错。

---

# 39. 登录总体设计

普通用户默认：

# Feishu SSO Only

管理员：

# Local Admin Fallback Login

普通用户不应该看到传统登录页。

---

# 40. 默认访问入口

用户访问：

```text
/
```

首先检查：

```text
Creation Studio Session
```

如果存在：

```text
直接进入 Workspace
```

如果不存在：

```text
自动进入 Feishu OAuth
```

而不是显示：

```text
用户名
密码
登录按钮
```

---

# 41. 普通用户登录流程

完整流程：

```text
用户打开飞书应用
      ↓
Creation Agent Studio
      ↓
检查Studio Session
      │
      ├── 有
      │     ↓
      │  Main Workspace
      │
      └── 无
            ↓
       Feishu OAuth
            ↓
       获得飞书身份
            ↓
       User Mapping
            ↓
       创建/更新本地User
            ↓
       签发Studio Session
            ↓
       Main Workspace
```

---

# 42. 不展示登录选择页

不要：

```text
欢迎登录

[飞书登录]

[管理员登录]
```

普通用户直接：

```text
Feishu OAuth
```

管理员入口只有知道特殊 URL 才能访问。

---

# 43. 管理员登录入口

推荐：

```text
/login/admin
```

展示：

```text
管理员登录

用户名

密码

登录
```

管理员：

```text
auth_source = local_admin
```

普通用户：

```text
auth_source = feishu
```

---

# 44. 为什么不直接使用 /admin

Django 本身通常使用：

```text
/admin/
```

作为 Django Admin。

所以推荐：

```text
/login/admin
```

作为管理员身份登录页。

管理后台再使用：

```text
/admin/*
```

如果未来自己开发管理端。

或者：

```text
/manage/*
```

更加清晰。

---

# 45. Django Admin

如果确实需要保留 Django Admin：

建议改：

```text
/django-admin/
```

避免和业务管理后台：

```text
/admin/
```

冲突。

---

# 46. Unified User

飞书用户和管理员最终统一关联：

```text
User
```

例如：

```text
User
├── id
├── username
├── display_name
├── avatar
├── email
│
├── feishu_user_id
├── feishu_open_id
├── feishu_union_id
│
├── auth_source
├── is_staff
├── is_superuser
├── status
└── timestamps
```

---

# 47. Auth Source

例如：

```text
feishu

local_admin
```

以后也可以扩：

```text
ldap

oidc
```

但当前不要过度设计。

---

# 48. Studio Session 与 Feishu Token 分离

飞书 OAuth Token 不能直接当系统 Session。

正确：

```text
Feishu OAuth
      ↓
Verify User
      ↓
Map Local User
      ↓
Studio Session
```

---

# 49. Provider Credential 与 Login Session 分离

特别是 Aily：

```text
user_access_token
```

属于：

```text
Provider Credential
```

不是：

```text
Studio Login Session
```

因此：

```text
登录认证
```

和：

```text
调用 Aily
```

两个领域必须分开。

---

# 50. ProviderAuthContext

运行 Aily 时生成：

```text
ProviderAuthContext
```

例如：

```text
provider = feishu_aily

identity_mode = user

subject_user_id = xxx

credential_ref = xxx

tenant_id = xxx
```

数据库不能存明文 UAT。

---

# 51. Aily 五类资源

Aily 自定义智能体实际包含：

```text
Agent

Session

Chat

Attachment

Artifact

Visibility
```

Aily 文档明确区分这些资源。

---

# 52. Aily 数据映射

建议最终固定：

```text
Creation Agent Studio          Feishu Aily
───────────────────────────────────────────

RuntimeBinding
.external_resource_id
                        ↔       agent_id


AgentThread.remote_id
                        ↔       session_id


Run.external_run_id
                        ↔       agent_chat_id


RuntimeAttachment
.external_attachment_id
                        ↔       agent_attachment_id


RunArtifact
.external_artifact_id
                        ↔       agent_artifact_id
```

---

# 53. Conversation

Conversation 表示：

```text
平台中的长期用户会话
```

而 Aily Session 是：

```text
Provider Remote Session
```

关系：

```text
Conversation
   ↓
AgentThread
   ↓
Aily session_id
```

---

# 54. Lazy Aily Session

第一次用户真正发送消息：

```text
Conversation
↓
还没有Aily session
↓
Start Chat
↓
Aily返回session_id
↓
保存到AgentThread
```

之后多轮：

```text
继续传session_id
```

Aily Chat API 支持通过 session_id 延续多轮会话。

---

# 55. Run

所有真实执行统一：

```text
Run
```

无论：

```text
Aily Agent
Aily Workflow
Codex
GraphFlow
HTTP
Media
```

---

# 56. Aily Chat 与 Run

调用：

```text
POST /agents/{agent_id}/chats
```

对应：

```text
一个 Run
```

Aily 返回：

```text
agent_chat_id
session_id
```

因此：

```text
Run.external_run_id
=
agent_chat_id
```



---

# 57. Run 数据模型

建议：

```text
Run
├── id
├── organization_id
├── user_id
├── application_id
├── conversation_id
├── workflow_run_id
├── runtime_binding_id
│
├── provider
├── runtime_type
├── external_run_id
│
├── status
├── provider_status
├── provider_finish_reason
│
├── input
├── output
│
├── runtime_snapshot
│
├── queued_at
├── started_at
├── finished_at
│
├── error_code
├── error_message
└── timestamps
```

---

# 58. Run 状态

统一：

```text
queued

running

waiting_input

waiting_external

cancelling

cancelled

succeeded

failed

interrupted
```

Provider 原始状态只放：

```text
provider_status
```

---

# 59. RunEvent

统一事件：

```text
RunEvent
```

字段：

```text
id
run_id
sequence
event_type
payload
created_at
```

唯一：

```text
(run_id, sequence)
```

---

# 60. 统一事件协议

建议：

```text
run.started

content.started

content.delta

content.completed

tool.started

tool.completed

artifact.discovered

artifact.created

input.required

run.completed

run.failed

run.interrupted
```

---

# 61. SSE

Aily Streaming：

```text
stream=true
```

当前文档明确流式连接最多约 5 分钟。

因此：

```text
SSE
≠
Run生命周期
```

---

# 62. SSE 架构

推荐：

```text
Browser
 │
 │ SSE
 ▼
Studio SSE Gateway
 ▲
 │
Redis
 ▲
 │
Aily Worker
 │
 │ SSE
 ▼
Feishu Aily
```

---

# 63. SSE 断开

如果：

```text
Browser断开
```

Run 继续。

如果：

```text
Aily SSE超时
```

也不能马上标记 Failed。

需要：

```text
GET Chat Result
```

重新确认最终状态。

---

# 64. Final Reconciliation

即使流式正常结束，也建议最终：

```text
GET Chat Result
```

获取：

```text
最终Text

Status

Finish Reason

Artifact ID
```

Aily 获取结果接口会返回文字与产物等最终信息。

---

# 65. Aily Cancel

当前 Aily 自定义智能体文档未提供：

```text
Cancel Chat
```

接口。

因此：

```text
AilyAgentAdapter.cancel = false
```

不能做假取消。

---

# 66. Attachment

用户输入文件统一为：

```text
RuntimeAttachment
```

---

# 67. Aily Attachment

Aily 支持：

```text
image

file

feishu_doc

bitable
```

当前文档同时约束文件大小、图片大小，以及单轮最多 8 个附件 ID。

---

# 68. Attachment Identity

Aily Attachment 属于上传者。

因此：

```text
上传附件
```

与：

```text
发送Chat
```

必须使用一致身份上下文。

---

# 69. RuntimeAttachment

建议：

```text
RuntimeAttachment
├── id
├── provider
├── external_attachment_id
├── attachment_type
├── name
├── source_type
├── source_url
├── auth_mode
├── auth_subject_key
├── created_by
└── created_at
```

---

# 70. 大文件上传

不要：

```text
Browser
↓
Django全量内存
↓
Aily
```

建议：

```text
Streaming Upload
```

或者：

```text
Temporary Storage
↓
Upload Worker
↓
Aily
```

---

# 71. Artifact

Aily Agent 可能产生：

```text
图片

文件

飞书文档

其他资源
```

最终通过：

```text
agent_artifact_id
```

定位。

---

# 72. RunArtifact

建议：

```text
RunArtifact
├── id
├── run_id
├── provider
├── external_artifact_id
├── provider_artifact_type
├── name
├── normalized_type
├── storage_type
├── cached_external_url
├── cached_url_fetched_at
├── cached_url_expires_at
├── storage_key
├── resolution_status
├── metadata
├── created_at
└── updated_at
```

---

# 73. Aily Artifact URL 不能永久保存

Aily Artifact API 返回：

```text
artifact_id

name

url
```

其中 URL 只有：

# 24 小时有效。

所以永久保存的是：

```text
agent_artifact_id
```

不是 URL。

---

# 74. Artifact 打开逻辑

```text
用户点击产物
↓
/artifacts/{id}/open
↓
校验权限
↓
检查URL缓存
↓
有效 → 302
↓
过期
↓
重新调用Aily Artifact API
↓
新24小时URL
↓
302
```

---

# 75. Artifact Policy

支持：

```text
external_refresh

mirror_on_access

mirror_on_complete
```

初期默认：

```text
external_refresh
```

满足当前：

> 本系统不需要展示或存储全部产物，但用户必须能找到它。

---

# 76. Message 与 Artifact 分离

最终：

```text
Message
=
文本内容

RunArtifact
=
生成资源
```

前端可以一起展示，但数据库不要混在一个字段里。

---

# 77. Aily User / Tenant Identity

Aily Chat 支持：

```text
user_access_token

tenant_access_token
```



因此 RuntimeBinding 必须配置：

```text
identity_mode
```

---

# 78. UAT

如果要实现：

> 以当前用户本人身份调用 Aily。

就必须使用：

```text
user_access_token
```

不能：

```text
TAT + employee_no
```

模拟。

---

# 79. TAT

对于公共 Agent：

```text
企业知识助手
后台任务
公共查询
```

可以使用：

```text
tenant_access_token
```

属于应用身份。

---

# 80. Session 与身份绑定

AgentThread 增加：

```text
auth_mode

auth_subject_key
```

避免：

```text
用户A的Session
被用户B继续使用
```

---

# 81. Visibility

Aily 支持用户 Visibility Check。

当前该接口：

```text
使用 UAT
```

并且 channel_type 当前明确的是：

```text
web_sdk
```



---

# 82. 两层权限

平台权限：

```text
Application ACL
```

负责：

> 用户能不能看到这个应用。

Aily 权限：

负责：

> Provider 是否真的允许调用。

最终：

```text
Local ACL
+
Provider Permission
```

---

# 83. Provider 并发

系统本身不负责模型推理。

主要压力来自：

```text
SSE

HTTP

Redis

TiDB

Attachment

Artifact

Provider连接
```

属于：

# I/O Bound Runtime Gateway

---

# 84. Aily 发起 Chat 限制

当前 Aily：

```text
POST /agents/{agent_id}/chats
```

明确：

# 10 次 / 秒。

---

# 85. Rate 与 Concurrency 分离

Provider 必须同时配置：

```text
start_rate_limit

max_inflight
```

例如：

```text
Aily

start_rate_limit = 10/s

max_inflight = 100
```

其中：

```text
10/s
```

是文档限制。

```text
100
```

只是系统保护值，需要压测。

---

# 86. AilyDispatcher

负责：

```text
Queue

Rate Limit

Concurrency

Retry

Backoff

Routing
```

流程：

```text
Run
↓
AilyDispatcher
↓
RateLimiter
↓
Concurrency Slot
↓
AilyWorker
```

---

# 87. 高并发排队

如果：

```text
100个用户同时提交
```

不直接：

```text
100次打Aily
```

而是：

```text
100 Run
↓
queued
↓
10/s逐步释放
```

---

# 88. Polling

异步运行通过：

```text
GET Chat Result
```

轮询。

该接口属于特殊频控，因此必须统一管理，不允许每个前端自己轮询。

---

# 89. Polling Backoff

建议：

```text
1s
↓
2s
↓
3s
↓
5s
```

统一通过 Provider RateLimiter。

---

# 90. Async Worker

Aily Worker 应使用：

```text
asyncio

Async HTTP Client

Connection Pool
```

而不是：

```text
一个Streaming请求
=
一个OS Thread
```

---

# 91. Execution Worker 划分

推荐：

```text
aily-worker

codex-worker

graphflow-worker

http-worker

media-worker
```

独立扩容。

---

# 92. RunLease

统一 Worker Claim：

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

---

# 93. TiDB 与 SKIP LOCKED

当前项目：

```text
select_for_update(skip_locked=True)
```

不能作为 TiDB 8.0.0 的 Worker Claim 基础。

未来使用：

# CAS + Lease

---

# 94. CAS Claim

流程：

```text
SELECT queued candidate

↓

UPDATE run
SET status='running'
WHERE id=?
AND status='queued'

↓

affected_rows == 1
成功
```

否则继续领取下一条。

---

# 95. Heartbeat

Worker 周期更新：

```text
heartbeat_at

lease_expires_at
```

过期：

```text
running
↓
interrupted
↓
queued / failed
```

---

# 96. Runtime Snapshot

每个 Run 保存：

```text
runtime_snapshot
```

记录：

```text
provider

agent_id / workflow_id

identity_mode

session_id

execution_mode

config
```

但：

```text
不保存Token明文
```

---

# 97. Aily Workflow

Aily Workflow 不属于：

```text
AilyAgentAdapter
```

而应该：

```text
AilyWorkflowAdapter
```

共享：

```text
AilyClient

AilyAuth

AilyRateLimiter
```

但执行协议独立。

---

# 98. 本地 Workflow

本系统 Workflow：

```text
Application A
↓
Application B
↓
Application C
```

是平台自己的编排。

Aily Workflow 则是：

```text
External Runtime
```

两者不要混合。

---

# 99. Fixed Page

例如：

```text
修改OA密码

条码查询

审批查询
```

可配置：

```text
runtime_type = none
renderer = page
```

或者：

```text
runtime_type = http
renderer = page
```

---

# 100. Renderer Registry

未来：

```text
RendererRegistry
├── chat
├── form
├── page
├── task
├── workflow
├── dashboard
└── external_page
```

WorkspaceHost 根据：

```text
renderer_key
```

选择组件。

---

# 101. Secret Management

禁止：

```text
Application.config
```

直接存：

```text
API Key

Aily Secret

Access Token
```

只保存：

```text
secret_ref
```

真实 Secret：

```text
Vault

KMS

Environment Secret

Secret Store
```

---

# 102. 数据库目标

未来：

```text
TiDB 8.0.0

MySQL 8
```

配置：

```text
DB_TYPE=tidb

DB_TYPE=mysql
```

---

# 103. Django DB Backend

MySQL：

```text
django.db.backends.mysql
```

TiDB：

```text
django_tidb
```

驱动统一倾向：

```text
mysqlclient
```

---

# 104. JSONField

可以继续大量使用：

```text
models.JSONField
```

但核心查询字段必须是真实列：

```text
provider

runtime_type

status

enabled

organization_id

application_id

created_at
```

---

# 105. JSON 适用内容

适合：

```text
Provider Config

Runtime Snapshot

Event Payload

Input Schema

Output Schema

Metadata
```

不适合：

```text
Run.status

Provider

Runtime Type
```

---

# 106. DB 兼容红线

避免核心依赖：

```text
PostgreSQL ArrayField

JSONB特有查询

GIN

GiST

SKIP LOCKED

Stored Procedure

Trigger

DB Event

XA

数据库专属RawSQL
```

---

# 107. 字符集

统一：

```text
utf8mb4
```

---

# 108. API V2

建议：

```text
POST /api/v2/runs

GET /api/v2/runs/{id}

GET /api/v2/runs/{id}/events

GET /api/v2/runs/{id}/stream

POST /api/v2/runs/{id}/commands

GET /api/v2/runs/{id}/artifacts

GET /api/v2/artifacts/{id}/open
```

---

# 109. Conversation 数据关系

```text
Conversation
│
├── Message
│
├── AgentThread
│
└── Run
     │
     ├── RunEvent
     ├── RunCommand
     ├── RunArtifact
     ├── RuntimeAttachment
     └── RunLease
```

---

# 110. Workflow 数据关系

```text
WorkflowRun
│
├── StepRun
│    └── Run
│
├── StepRun
│    └── Run
│
└── StepRun
     └── Run
```

---

# 111. 现有执行模型迁移

当前：

```text
AgentTurn

AgentExecution

Job

WorkflowStepRun
```

不要立即删除。

先：

```text
增加 Run 关联
```

新代码优先 Run。

最终逐步淘汰旧 Execution Root。

---

# 112. Aily Integration 模块

建议：

```text
integrations/aily/
├── client.py
├── auth.py
├── rate_limit.py
├── dispatcher.py
├── agent_adapter.py
├── workflow_adapter.py
├── event_mapper.py
├── attachment_mapper.py
├── artifact_mapper.py
└── exceptions.py
```

---

# 113. 后端目标模块

建议最终：

```text
identity

tenancy

catalog
├── applications
├── agents
├── skills
├── providers
└── runtime_bindings

workspace

conversation

workflow

execution
├── run
├── event
├── command
├── artifact
├── attachment
└── lease

integrations
├── aily
├── codex
├── graphflow
└── http

governance
├── secret
├── quota
├── policy
└── audit

automation
```

---

# 114. 推荐部署架构

```text
                          Nginx
                            │
              ┌─────────────┴─────────────┐
              ▼                           ▼
         React Static                Django ASGI
                                          │
                      ┌───────────────────┼──────────────┐
                      ▼                   ▼              ▼
                    TiDB                Redis       Object Storage
                      │                   │
                      └────────┬──────────┘
                               ▼
                       Execution Plane
             ┌─────────────────┼────────────────┐
             ▼                 ▼                ▼
        Aily Worker       Codex Worker    GraphFlow Worker
             │
             ▼
         Feishu Aily
```

---

# 115. PC / Mobile 部署

PC 与手机：

```text
同一个 Web Build
```

通过 Responsive Shell 选择：

```text
DesktopAppShell

MobileAppShell
```

无需两个域名，也无需两个前端项目。

---

# 116. 飞书内嵌体验

系统主要运行于：

```text
飞书 PC
飞书 Mobile
```

因此必须重点验证：

```text
Feishu Desktop WebView

Feishu Mobile WebView

OAuth跳转

History

Safe Area

File Upload

Download

SSE

软键盘

返回行为
```

---

# 117. Observability

后台至少监控：

```text
在线用户

PC / Mobile使用比例

SSE连接数

Queued Run

Running Run

成功率

失败率

平均Run耗时

P95耗时

Aily Start Rate

Aily Inflight

429

Timeout

Polling QPS

Artifact Resolve

Redis Latency

TiDB Latency

Worker Heartbeat

Lease Expired
```

---

# 118. 用户行为指标

Workspace 还可以统计：

```text
Application Usage

Agent Usage

快捷入口点击率

最近应用

收藏应用

PC/Mobile来源

Conversation数量

Run数量
```

用于自动生成：

```text
常用应用
```

---

# 119. Phase 1：数据库兼容

优先：

```text
PostgreSQL
↓
TiDB / MySQL
```

完成：

```text
DB_TYPE

mysqlclient

django-tidb

移除SKIP LOCKED

CAS + Lease
```

---

# 120. Phase 2：Runtime 抽象

完成：

```text
RuntimeAdapter

RuntimeRegistry

Provider

ApplicationRuntimeBinding
```

---

# 121. Phase 3：Unified Run

完成：

```text
Run

RunEvent

RunCommand

RunArtifact

RuntimeAttachment

RunLease
```

---

# 122. Phase 4：Aily Agent

实现：

```text
Session

Chat

Streaming

Final Reconciliation

Attachment

Artifact

UAT / TAT

Rate Limit

Error Mapping
```

---

# 123. Phase 5：Workspace Shell

把现有前端改造成：

```text
AppShell
↓
WorkspaceHost
```

支持：

```text
Home Workspace

Chat

Fixed Page

Workflow

Dashboard
```

---

# 124. Phase 6：PC / Mobile 双布局

增加：

```text
DesktopAppShell

MobileAppShell
```

以及：

```text
Desktop / Mobile Renderer Layout
```

---

# 125. Phase 7：登录体系

完成：

```text
Default Feishu OAuth

Studio Session

User Mapping

/login/admin

Local Admin Auth
```

---

# 126. Phase 8：主页快捷入口

实现：

```text
常用应用

最近使用

收藏

智能体切换

Application Search
```

---

# 127. Phase 9：@ 路由

实现：

```text
@智能体

@固定应用

@工作流
```

并携带：

```text
原始指令
```

---

# 128. Phase 10：Aily Workflow

增加：

```text
AilyWorkflowAdapter
```

---

# 129. Phase 11：治理

后续：

```text
Quota

Audit

Cost

Circuit Breaker

Evaluation

Automation
```

---

# 130. 最终普通用户入口

完整体验：

```text
用户点击飞书应用
       ↓
是否已有Studio Session
       │
   ┌───┴────┐
   │        │
  有        无
   │        │
   │        ▼
   │   Feishu OAuth
   │        │
   │        ▼
   │    User Mapping
   │        │
   └────────┤
            ▼
       Main Workspace
            │
      ┌─────┼──────────────┐
      ▼     ▼              ▼
   主对话  常用智能体      常用应用
      │
      ├── 切换Aily Agent
      │
      ├── @Application
      │
      └── 普通输入
```

---

# 131. 最终管理员入口

```text
/login/admin
      ↓
Admin Login Page
      ↓
Local Admin Auth
      ↓
Admin Workspace
```

普通用户默认不知道，也不需要看到该入口。

---

# 132. 最终 PC 用户体验

```text
常驻 Sidebar

中央 Workspace

智能体快速切换

最近会话

常用应用

Chat / Fixed Page 无缝切换

返回时保持状态
```

---

# 133. 最终 Mobile 用户体验

```text
顶部当前智能体

Drawer 导航

Bottom Sheet 智能体切换

底部输入框

单列 Application 页面

移动端 Card List

完整 Workspace History

软键盘适配
```

整体交互参考：

```text
ChatGPT Mobile

豆包 Mobile
```

但保持 Creation Agent Studio 自己的 Application Workspace 设计。

---

# 134. 最终 Aily 调用链

```text
用户
 │
 ▼
Main Workspace
 │
 ▼
Chat Application
 │
 ▼
RuntimeBinding
 │
 ▼
Create Run
 │
 ▼
queued
 │
 ▼
AilyDispatcher
 │
 ├── Rate Limit
 ├── Concurrency
 ├── Auth Context
 └── Session
 │
 ▼
AilyWorker
 │
 ▼
Feishu Aily
 │
 │ SSE / Async
 ▼
AilyEventMapper
 │
 ▼
RunEvent
 │
 ├── Redis
 │     ↓
 │    SSE
 │     ↓
 │   Browser
 │
 └── Final Reconciliation
        ↓
      Message
      Run
      Artifact
```

---

# 135. 最终 Artifact 链

```text
Aily
↓
agent_artifact_id
↓
RunArtifact
↓
用户后来点击
↓
Artifact Resolver
↓
重新获取24小时URL
↓
用户打开产物
```

---

# 136. 最终架构关系

```text
Organization
│
├── User
│
├── Provider
│
└── Application
     │
     ├── Renderer
     │
     └── RuntimeBinding
          │
          ▼
      Workspace
          │
          └── Conversation
               │
               ├── Message
               ├── AgentThread
               │     └── External Session
               │
               └── Run
                    │
                    ├── RunEvent
                    ├── RunCommand
                    ├── RunArtifact
                    ├── RuntimeAttachment
                    └── RunLease
```

---

# 137. 产品层最终结构

最终用户理解的产品只有几个概念：

```text
首页

智能体

应用

会话

我的
```

而技术架构中的：

```text
Runtime

Provider

Run

Artifact

Lease
```

对普通用户完全隐藏。

---

# 138. 最终关键边界

未来架构必须始终坚持：

```text
Application
≠
Agent

Application
≠
Runtime

Conversation
≠
Run

Message
≠
Artifact

Platform
≠
Provider

Studio Session
≠
Feishu Provider Token

Desktop Layout
≠
Mobile Layout

但：
Desktop 与 Mobile
共享同一套业务模型
```

---

# 139. 最优先开发顺序

建议顺序：

```text
1. TiDB / MySQL兼容

2. RuntimeBinding / RuntimeAdapter

3. Unified Run

4. Aily Agent完整接入

5. Main Workspace / AppShell

6. 多智能体切换

7. 固定Application

8. 飞书OAuth登录

9. Admin独立登录

10. MobileAppShell

11. Artifact

12. Aily Workflow

13. 治理与观测
```

其中 Workspace 和 Feishu OAuth 可以根据产品上线节奏提前并行开发。

---

# 140. 最终结论

Creation Agent Studio 最终应该给用户一种感觉：

> 打开飞书里的 Creation Agent Studio，就进入一个统一 AI 工作台。

用户无需理解：

```text
这个是Aily

那个是Workflow

这个是HTTP

那个是Fixed Page
```

用户只需要：

```text
选择一个智能体

输入问题

打开一个应用

完成一个任务

随时返回主页面
```

PC 上：

```text
像一个完整的 AI 工作台。
```

手机上：

```text
像一个真正为移动端设计的 AI 助手应用。
```

登录上：

```text
普通员工打开即飞书身份进入。
```

管理员：

```text
通过专属 Admin 入口登录。
```

技术底层则统一使用：

```text
Application
+
Workspace
+
Renderer
+
RuntimeBinding
+
RuntimeAdapter
+
Conversation
+
Run
+
Artifact
+
Provider
```

形成稳定的平台架构。

最终 Creation Agent Studio 不只是：

```text
智能体门户
```

而应该成为：

# 企业 AI 应用统一入口 + AI Runtime 编排平台 + 企业任务工作台

这也是当前项目最适合的长期演进方向。