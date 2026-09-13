# Creation Agent Studio — Go 后端重构 · 跨会话交接提示词

你现在负责 **Creation Agent Studio 后端 Go 重构的后续开发**。上一会话已完成 G0–G10
（OpenAPI 契约 → Go 平台 → 新 Schema → Identity → Catalog → Execution Core → SSE →
Aily Agent → 前端切 Go → **删除 Django（2026-09-12 用户确认后执行**，历史参考归档在
`docs/archive/django-reference/`**)**），当前系统**已经以 Go 后端为唯一后端在真实环境运行
并通过真实 Aily 端到端验证**。

工作优先级不变：

```text
架构正确性 > 运行可靠性 > 性能 > 可维护性 > 短期开发工作量
不使用 TiDB 8.0 独有语法，保持 MySQL 5.7 兼容
不要为了快而牺牲目标架构
```

---

## 一、必读资料（按顺序）

```text
docs/Creation Agent Studio Go 后端目标架构.md        # 目标架构（红线来源）
docs/Creation Agent Studio Go 重构执行计划.md        # G0-G15 阶段计划（当前进度 G9 完成）
docs/开发进度清单.md                                 # 全部已验证行为 + 四轮会话记录（必读第六章）
docs/Creation Agent Studio 未来演进完整架构文档.md    # 产品与 Domain 终态
backend-go/README.md                                 # Go 后端使用说明
C:\Users\吴志彬\Documents\飞书开放平台文档\飞书aily\自定义智能体\   # Aily 官方文档（接口唯一依据）
```

Aily API 行为优先级：官方文档 > 真实联调结论（开发进度清单里有）> 一切记忆。不要凭记忆猜接口。

---

## 二、当前状态快照（2026-09-12 深夜）

### 运行形态
```text
前端 vite :3030  ──/api 代理──►  Go API :8080（studio-api, cmd/api）
                                 Go Worker（studio-worker, cmd/worker，消费 feishu_aily 队列）
TiDB 8.0.0（经 TiProxy :6000）→ 新库 xiaoan3_go（Go 专用，干净 Schema，20 张表）
Redis（:6380 DB 2）→ 会话/UAT缓存/GCRA限流/Redis Streams/Run事件PubSub
Django（backend/）已删除（G10，2026-09-12 用户确认）——历史参考在 docs/archive/django-reference/
```

### 已完成并验证（详见进度清单第六章，全部有实测记录）
- **G0**：`backend-go/api/openapi.yaml`（OpenAPI 3.0.3，唯一 HTTP Contract）→ oapi-codegen 生成
  Go types + chi ServerInterface（`internal/gen/api`，勿手改，`make gen-api` 再生成）
- **G2**：`db/migrations/`（golang-migrate，MySQL 5.7 兼容）+ sqlc 查询层（`db/queries/` →
  `internal/gen/db`，`make gen-db` 再生成）
- **G3**：飞书 OAuth（6 scope）、**HttpOnly Opaque Session**（Redis 只存 SHA-256(token)）、
  CSRF double-submit、Argon2id 管理员 + **Django PBKDF2 兼容**（迁移用户可用原密码）、
  refresh token **AES-256-GCM** 落库
- **G4**：RuntimeAdapter/RuntimeRegistry（业务层零 `if provider == "aily"`）、智能体市场全套
- **G5**：CreateRun=事务内(Message+Run+Outbox) → Outbox Relay → Redis Streams → **CAS Claim** →
  RunLease 心跳 → Reaper 恢复 → **XAUTOCLAIM 认领死角**；**GCRA 分布式限流**（Redis Lua）
- **G6**：SSE 网关（**先 SUBSCRIBE → 回放 → 按 sequence 去重实时帧**；15s keepalive；
  终态自动关闭）
- **G7**：Aily client/auth/mapper/adapter/executor（嵌套 text 容错、markdown↔artifact 跨 item
  配对、未知 SSE 事件不死 run、Final Reconciliation 唯一终态权威、Lazy Session 按
  provider/auth_mode/auth_subject 钉死、UAT 强制）
- **G9**：前端已切 Go——**JWT localStorage 已删除**，cookie session + CSRF header；
  Vite 代理 → :8080；tsc 0 错误、vitest 91/91、浏览器 E2E 全过
- **数据迁移**：用户/飞书身份（refresh token 重加密）/应用/绑定/类目/头像 已从 xiaoan3 迁入
  xiaoan3_go（工具 `cmd/seeddata`，可 --force 重放；数字 id 保留）

### 测试基线（全部可复跑，全绿）
```bash
cd backend-go
go test ./... -count=1                                    # 单测 7 包
STUDIO_TEST_TIDB=1 STUDIO_TEST_REDIS=1 go test ./tests/integration/ -count=1
# CAS 100并发恰好1赢家 / 重复投递恰好执行1次 / Lease-Reaper 状态机 / SSE 回放+实时+终态 /
# Redis pubsub / 会话时区契约
cd ../frontend && npx tsc --noEmit && npx vitest run      # 0 错误 / 91 用例
C:/software/miniconda3/envs/py311/python.exe backend-go/tests/e2e_go_chat.py
# 浏览器 E2E：需先起 Go API + worker + vite --port 3030 --strictPort（真实 Aily 调用）
```

---

## 三、环境与凭据

- **凭据全部在 `backend-go/.env.local`**（gitignored；由 backend/.env 派生 + 新生成的
  TOKEN_ENCRYPTION_KEY）。不要把任何 secret 写进源码/测试/日志。
- TiDB：192.168.212.38:6000，库 **xiaoan3_go**（xiaoanuser 无 CREATE DATABASE 权限，
  DBA 已 GRANT ALL ON xiaoan3_go.*，库级 CREATE 够用）；**xiaoan3 是 Django 现库，绝不动**。
- Redis：192.168.211.239:6380 DB 2，所有 key 前缀 `xiaoan3:`；PubSub channel
  `xiaoan3:run:{id}:events`（与参考实现一致）。
- Go 工具链：go1.27.1，`go env -w GOPROXY=https://goproxy.cn,direct` 已设置（proxy.golang.org 不可达）。
- 工具已装：oapi-codegen v2.5.0、sqlc v1.30.0（`go env GOPATH`/bin 下）。
- **DSN 必须带 `time_zone=%27%2B00%3A00%27`**（会话 UTC），有契约测试 TestSessionTimezoneUTC 钉死。

### 启动 / 运维
```bash
cd backend-go
go run ./cmd/api        # :8080（-migrate 顺带跑迁移）；必须从 backend-go 目录起（读 .env.local）
go run ./cmd/worker     # 执行面；--provider=feishu_aily 可指定
cd frontend && npx vite --port 3030 --strictPort
# E2E 会话签发：go run ./cmd/testsession -user 吴志彬   → {token, csrf, user}
# 数据迁移重放：go run ./cmd/seeddata -force（SEED_SOURCE_DSN=... SEED_MEDIA_ROOT=../backend/media）
```
本机坑：vite 只监听 localhost（用 localhost 别用 127.0.0.1）；先 `unset http_proxy https_proxy`；
多个同名进程/端口占用时先 `tasklist | findstr studio`、`netstat -ano | findstr :8080`。

---

## 四、架构红线（不得破坏）

```text
TiDB 是唯一 Source of Truth；Redis 只做分发/实时/缓存/会话
不产生重复执行：Worker 必须 CAS Claim（affected_rows==1）才算数；队列投递是 at-least-once
每 Token 不落库；实时走 Redis PubSub，持久走 Worker 收敛（Final Reconciliation 唯一终态权威）
Run 外部映射不变：binding.external_resource_id=agent_id；AgentThread.remote_id=session_id（lazy）；
  Run.external_run_id=agent_chat_id；attachment/artifact 外部 id 各自对应
调用 Aily 自定义智能体必须用户 UAT（目标 agent 拒绝应用身份，实测 10009）；
  Studio Session 与 Provider 凭据彻底分离；refresh token 只能 AES-256-GCM 落库
Domain 不 import chi/redis driver；Provider 差异只进 adapter（业务层禁止 if provider==）
列表/详情 payload 以 openapi.yaml 为准（列表 24 字段形状 vs authoring 18 字段形状是历史契约）
SQL：显式列、MySQL 5.7 兼容（无窗口函数/无 JOIN ON 子查询/TEXT JSON 无 DEFAULT）、EXPLAIN 验证
```

## 五、新会话容易踩的坑（前车之鉴，全部真实踩过）

1. **sqlc + Go 1.27 json/v2**：`json.RawMessage`（=jsontext.Value）扫 SQL NULL 报
   "unsupported Scan"。已用 `dbtypes.JSONText`（Scanner/Valuer）+ sqlc **column 级** override
   21 处解决（MySQL json 的 db_type override 被 sqlc 特例绕过，必须 column 级）。
2. **中间件包装器必须转发 Flush/Unwrap**：Metrics 的 statusWriter 曾让 `w.(http.Flusher)`
   失败 → SSE 全坏且被 E2E 的回放机制掩盖。流式路径必须有直连冒烟断言（`curl -N`）。
3. **DSN time_zone**：不锁会话时区，DATETIME 列会混服务器本地 DEFAULT 与 Go 写的 UTC
   （+8h 偏移污染 Lease 比较和前端时间显示）。
4. **Django 密码格式**：`pbkdf2_sha256$<iter>$<salt>$<hash>` 的 salt 是**原始字符串非 base64**。
5. **Aily run input 的 content**：JSON 反序列化得 `[]any`，不是 `[]map[string]any`
   （contentFromPayload 已双形态兼容；worker goroutine 有 recover，单 Run panic 不杀执行面）。
6. **TiDB 限制**：JOIN ON 内子查询不支持；窗口函数是 MySQL 8（保持 5.7 兼容不能用）。
7. **消费者组死角**：组建立前的消息 + 崩溃 worker 未 ACK 的消息会卡 PEL——reclaimLoop
   （XAUTOCLAIM）已在，改 Worker 时别删。
8. **测试封闭性**：集成测试用独立 provider 名（如 itest_provider），否则在线 worker 的
   回扫会把测试 Run 抢走执行。
9. **gofmt 重排会破坏 python/sed 文本替换的匹配**——做精确替换前先看当前文件内容。
10. **agent id 不写死在代码里**：`runtime_bindings.external_resource_id` 才是配置；
    市场页可改；`AILY_DEFAULT_AGENT_ID` 只是 seed 默认值。
11. **Streaming → Polling 必须显式传 `agent_chat_id`**：流式首帧虽然会把 id 写入数据库，
    当前 Worker 内存里的 Run 仍可能是陈旧对象。若第一次 reconcile 返回非终态，后续轮询不能
    再从旧 `run.ExternalRunID` 取值，否则会请求 `/chats/` 并误报 404/10002。已用显式 chat id
    参数 + 回归测试修复；官方文档没有说 404 可重试，不要用放宽 404 来掩盖 id 丢失。

---

## 六、下一步工作（按优先级）

1. **G10 删除 Django ✅ 已完成**（2026-09-12 用户确认后执行）：归档
   `docs/archive/django-reference/`（脱敏，排除 .env/venv/media/缓存/日志）→ 删除
   `backend/`（顺带清掉了三个遗留 Django 进程）→ 活跃文档/前端提示/nginx 上游同步更新
   → 删除后 Go 测试/集成/tsc/vitest/真实 Aily E2E 全部复验通过。详见进度清单第五轮记录。
   注意：`cmd/seeddata` 保留为一次性历史迁移工具；Go 侧 Django PBKDF2 密码兼容继续保留
   （迁移用户登录所需）。**不要恢复 Django 作为备用后端。**
2. **G11 Mobile 完善化**（前端）：Bottom Sheet 智能体切换、软键盘、Bottom Composer、
   Safe Area、Workspace Back Stack；FormRenderer/PageRenderer/DashboardRenderer 界面。
3. **G12 Aily Workflow + Codex/GraphFlow/HTTP RuntimeAdapter**：新 Provider 直接进
   Application→RuntimeBinding→Run；本地创作智能体（legacy /api/agents/*）在此时转为
   Application 体系（当前是空集 shim）；WorkspacePage 模板工作区的旧 GraphFlow 链路
   在此之后才能迁移。
4. **G13 治理**：Quota/Audit（表已建：quota_policies/audit_logs）、熔断、Provider Health、
   React 管理后台（替代 Django Admin——Provider/Binding/User/Runtime Health）。
5. **G14 性能与故障测试**：REST/SSE 压测、Redis Stream 积压、Worker kill、Provider 429、
   5min SSE 超时、慢客户端、大附件。重点是不丢 Run、不重复 Run、事件有序、可恢复。
6. **content.snapshot**：实时 delta 目前只在 Redis + 终态落库；长回复断线重连会丢中间
   delta（靠 reconcile 兜底）。按 §25 在 Worker 侧加 250-500ms Coalesce 快照落 run_events。
7. **企业控制台 legacy 接口**（/enterprise/*、组织、API keys）未实现——前端控制台页会 404，
   属 G13 范围。

---

## 七、会话工作方式（沿用）

每个阶段：阅读现有实现 → 确认已验证行为 → 写目标设计 → 写测试 → 实现 →
go test + 集成测试 + tsc/vitest → 浏览器 E2E → 确认真实行为 → 更新 `docs/开发进度清单.md`。

发现 Django 设计不好：不要为兼容而复制缺陷。
发现产品行为已被真实用户验证：没有明确理由必须保留。
所有 Go 代码：gofmt / go vet / go test / race test 关键并发模块。
不要在没有用户确认的情况下删除 xiaoan3 或 backend/。
真实 secret 永远只从 .env.local / 环境变量来。
