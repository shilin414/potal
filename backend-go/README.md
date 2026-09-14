# Creation Agent Studio — Go Backend (backend-go)

按《Creation Agent Studio Go 后端目标架构》与《Go 重构执行计划》重建的运行时平台后端。
不是 Django 的逐文件翻译，而是一个以 OpenAPI 契约为唯一 HTTP Contract 的
Go Control Plane + Streaming Plane + Execution Plane。

> 状态：**G0–G10 已完成**；Go API + Worker + Vite 已通过真实 MySQL 5.7/Redis、真实用户 UAT 与
> 浏览器 E2E 验收，Django 已退出运行架构（历史参考在 docs/archive/django-reference/）。
> 当前生产部署拓扑仍属于 G15。

## 架构总览

```text
Nginx
 ├─ REST            → studio-api   (cmd/api,    :8080)   短请求控制面
 ├─ /runs/*/stream  → studio-stream(cmd/stream, :8081)   SSE 长连接面
 └─ React SPA

studio-worker (cmd/worker) —— 执行面（可按 provider 水平扩容）
   Outbox Relay: MySQL → Redis Streams
   Worker:       XREADGROUP → MySQL CAS Claim → RunLease → Provider Handler
```

- **MySQL 5.7（Source of Truth）**：users / feishu_identities / providers /
  applications / runtime_bindings / conversations / agent_threads / messages /
  runs / run_events / run_commands / run_leases / runtime_attachments /
  run_artifacts / outbox_events / quota_policies / audit_logs
- **Redis（非唯一真相）**：Opaque Session（存 token hash）、UAT TTL 缓存、
  Provider 限流（GCRA）、Redis Streams 分发、Run 事件 Pub/Sub
  （channel `xiaoan3:run:{id}:events`，与参考实现一致）。

## 目录

```text
cmd/api          studio-api 入口（-migrate 顺带跑迁移）
cmd/stream       studio-stream 入口（SSE 独立扩容）
cmd/worker       studio-worker 入口（--provider=feishu_aily）
api/openapi.yaml 唯一 HTTP Contract（OpenAPI 3.0.3，冻结已验证 v2 行为）
db/migrations    golang-migrate 迁移（MySQL 5.7 兼容 DDL）
db/queries       sqlc 查询（显式列，无 SELECT *）
internal/platform    config/logging/database/redisx/telemetry/crypto/storage/httpclient/ids
internal/identity    用户、飞书身份、Opaque Session、OAuth 编排
internal/catalog     Application/Runtime/Provider/Renderer + RuntimeRegistry/Adapter
internal/execution   Run 生命周期、Outbox、Redis Streams Worker、CAS/Lease/Reaper、GCRA
internal/integrations/aily   Aily 客户端/鉴权/限流/事件映射/执行器/适配器
internal/transport   chi 路由 + handler（错误封套与 SSE 帧格式与参考实现一致）
internal/gen         oapi-codegen / sqlc 生成代码（勿手改）
```

## Bootstrap（一次性）

数据库基线是 **MySQL 5.7**（TiDB 8.0.0 / TiProxy 已退出运行架构，不再参与数据库链路）。
Go 后端使用**独立库 `xiaoan`**，与 Django 旧库 `xiaoan3` 物理隔离，绝不触碰历史数据
（Django 已在 G10 退出运行架构，历史参考归档在 `docs/archive/django-reference/`）。

库级 collation 必须是 `utf8mb4_bin`，与全部 migration 保持一致——改成
`utf8mb4_general_ci` / `utf8mb4_unicode_ci` 会改变字符串唯一索引与大小写比较语义。

```sql
-- 用有建库权限的账号执行一次：
CREATE DATABASE xiaoan CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;

-- 迁移账号（仅 migration Job 持有 DDL 权限）：
GRANT ALL PRIVILEGES ON xiaoan.* TO 'potal_migrate'@'%';

-- 运行账号（studio-api/stream/worker/scheduler 只用 DML）：
GRANT SELECT, INSERT, UPDATE, DELETE ON xiaoan.* TO 'potal_app'@'%';
```

Schema 权威来源是 `db/migrations`——**不要**把任何 dump 的 `SHOW CREATE TABLE`
输出当作目标 Schema 权威；数据权威来源才是原 TiDB 库。

然后：

```bash
cd backend-go
cp .env.example .env.local     # 填入真实 DB/Redis/Feishu secrets
go run ./cmd/api -migrate      # 应用迁移并启动 API
go run ./cmd/stream            # 另一个终端：SSE 平面
go run ./cmd/worker            # 另一个终端：执行面
```

## 常用命令

```bash
make build        # go build ./...
make vet          # go vet ./...
make test         # 全部单测（不需要数据库）
make test-race    # -race
make gen          # 重新生成 OpenAPI/SQL 代码
STUDIO_TEST_DB=1 STUDIO_TEST_REDIS=1 go test ./tests/integration/ -count=1
# 浏览器 E2E 另需 Go API + Worker + Vite 三进程：
C:/software/miniconda3/envs/py311/python.exe tests/e2e_go_chat.py
```

## 保留的真实行为（不得回归）

- **错误封套四形态**：`{"detail"}` / `{"error"}` / `{"field":[..]}` / `["msg"]`
- **SSE 帧**：`event: run.event` + `data:{run_id,sequence,event_type,payload[，created_at]}`；
  15s `: keepalive`；终态（completed/failed/interrupted）自动关闭；
  重连全量回放由前端按 sequence 去重
- **Aily 兼容**：SSE text 嵌套对象容错；markdown `artifacts/<name>` 与
  artifact entry 跨 content item 配对；Final Reconciliation 是唯一终态权威
  （流结束/超时/断开一律 GET chat result 收敛）；UAT 强制（目标 agent 拒绝应用身份）
- **身份**：Studio Session（HttpOnly Opaque Cookie，Redis 存 SHA-256 hash）
  与 Provider UAT 完全分离；refresh token AES-256-GCM 落库；
  AgentThread 按 (provider, auth_mode, auth_subject) 钉死，禁止跨用户复用 session
- **执行正确性**：Run 创建 = 事务内 (Message + Run + Outbox)；Redis 重复消息
  不会重复执行（CAS claim 才算数）；Worker 崩溃由 Reaper 恢复（interrupted →
  重排或 failed，attempt/max_attempts 控制）；Outbox 至少一次投递

## 关键架构决策（与文档的差异说明）

1. **OpenAPI 3.0.3（而非 3.1）**：oapi-codegen v2.5 官方建议 3.0.x（3.1 支持不完整，
   `type:[string,"null"]` 无法生成）。契约内容等价，工具链完全兼容。
2. **ID 策略**：Run 域资源（runs/run_commands/run_artifacts/runtime_attachments/
   agent_threads/outbox 聚合）用 UUIDv7（应用生成，BINARY(16) 存储，SQL 不用
   MySQL8 的 UUID_TO_BIN）；users/applications/conversations/messages 等在
   API 里已作为 number 暴露的资源保持 BIGINT 自增——前端契约不变优先。
3. **G6 之前不入库 content.snapshot 的实时 delta**：实时路径走 Redis Pub/Sub，
   持久路径由 Worker 收敛（终态 reconcile 落 final text + artifact）；per-token
   落库被禁止。
