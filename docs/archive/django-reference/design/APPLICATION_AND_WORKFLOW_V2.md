# Application 与 Workflow 当前模型

## 设计原则

系统只保存 Application、Agent 和 Skill 的最新状态，不维护业务对象的历史版本。
编辑对象会立即更新它的当前配置；数据库和 API 中不提供版本创建、发布、审核、
升级或回滚流程。

## 核心对象

### Application

Application 同时保存应用身份、展示信息和当前运行配置：

- 名称、slug、描述、图标、分类和可见性；
- `kind`：`chat`、`task` 或 `custom`；
- `renderer_key`：前端运行界面注册键；
- `executor_key`：后端任务执行器注册键；
- `input_schema`、`output_schema` 和 `default_config`；
- ChatApplicationProfile、Agent/Skill 绑定和 Guided Prompt。

前端通过 ApplicationRuntime 使用 Application 当前配置。应用在工作流中出现时，
仍使用相同的 renderer，只额外注入 Project 和 WorkflowStepRun 上下文。

### Agent

Agent 保存当前系统提示词及模型、工具、Skill、知识库、护栏和工作流配置。
ApplicationAgentBinding 和 Conversation 都直接引用 Agent。

AgentDeployment 只记录某个环境当前启用该 Agent 及环境覆盖配置；由于没有历史快照，
Agent 被编辑后，各环境会读取同一份最新配置，也不提供版本回滚。

### Skill

Skill 保存当前来源和内容信息：`source_type`、`source_uri`、`artifact_key`、
`manifest` 和 `content_hash`。同步命令发现同 slug 的 Skill 时直接覆盖当前内容。

AgentSkillBinding、ApplicationSkillBinding 和 ConversationSkillBinding 都直接引用 Skill。

## 关系

```text
Application
├─ ChatApplicationProfile
├─ ApplicationAgentBinding ──> Agent
├─ ApplicationSkillBinding ──> Skill
└─ GuidedPrompt
   └─ GuidedQuestion
      └─ GuidedOption

Agent
└─ AgentSkillBinding ──> Skill

Workflow
└─ WorkflowStep ──> Application
   └─ WorkflowStepRun ──> Application

Conversation
├─ Agent
├─ Application（可选）
└─ ConversationSkillBinding ──> Skill
```

## API 标识符

所有接口直接传稳定对象 ID：

- `application_id`
- `agent_id`
- `skill_ids`
- 工作流步骤中的 `application_id`

Application 的引导问题组装接口为：

```text
POST /api/apps/{application_slug}/compose-prompt/
```

## 更新语义

- 修改 Application、Agent 或 Skill 即覆盖当前配置；
- 已存在的 Conversation 和 Workflow 直接读取对象最新配置；
- 工作流运行仍复制步骤名称、顺序和节点 config，但 Application 引用指向最新对象；
- 如将来需要审计，应记录操作日志，而不是恢复业务版本表。

## 数据库策略

本次改造是破坏性 schema 变更。旧迁移链和 SQLite 数据库不兼容，需要使用当前的
初始迁移创建新数据库。迁移会自动创建通用 Agent 和默认聊天 Application。
