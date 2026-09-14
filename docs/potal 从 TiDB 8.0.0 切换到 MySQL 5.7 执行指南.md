# potal 从 TiDB 8.0.0 切换到 MySQL 5.7 执行指南

**项目：** `shilin414/potal`  
**基线分支：** `dev`  
**目标数据库：** MySQL 5.7  
**原数据库：** TiDB 8.0.0 / TiProxy  
**后端：** Go + `database/sql` + `go-sql-driver/mysql` + sqlc + golang-migrate  
**文档日期：** 2026-09-14

---

# 1. 改造结论

本项目从 TiDB 8.0.0 切换到 MySQL 5.7，**不需要重写数据库访问层，不需要更换驱动，不需要重写现有主要 SQL。**

当前项目已经具备以下基础：

- Go 数据库驱动已经是 `github.com/go-sql-driver/mysql v1.9.3`，不存在 TiDB SDK 依赖。
- `sqlc.yaml` 已明确使用：

```yaml
engine: "mysql"
```

因此生成层本身就是 MySQL 方言。

- 主 migration `0001_core_schema.up.sql` 已明确按照：

```text
MySQL 5.7 compatible
no TiDB-only syntax
no functional indexes
no DEFAULT on TEXT/JSON columns
```

设计。

- 表结构使用的主要能力：
  - InnoDB
  - utf8mb4
  - `DATETIME(3)`
  - JSON
  - `BINARY(16)`
  - `AUTO_INCREMENT`
  - `CURRENT_TIMESTAMP(3)`
  - 普通/唯一/组合索引

均可用于 MySQL 5.7。

- 并发控制 SQL 使用的：
  - `SELECT ... FOR UPDATE`
  - `UPDATE ... WHERE ...`
  - CAS
  - `ON DUPLICATE KEY UPDATE`
  - `DATE_ADD`
  - `TIMESTAMPDIFF`
  - `CURRENT_TIMESTAMP(3)`

均属于 MySQL 5.7 可用能力。

- Provider Admission 已经针对 MySQL 的：
  - `1213 Deadlock`
  - `1205 Lock wait timeout`

实现事务级重试。

最重要的是：

**当前 CI 已经存在真正的 `mysql:5.7` 容器测试。最新代码能够在 MySQL 5.7 上成功执行 migration 和 integration tests。** 

所以本次改造建议定义为：

> 将“MySQL 5.7 兼容”提升为“MySQL 5.7 唯一正式数据库基线”，删除 TiDB/TiProxy 专属代码、配置、测试命名和 CI，同时把目前 TiDB Job 承担的完整数据库测试覆盖迁移到 MySQL 5.7 Job。

---

# 2. 最终目标架构

当前：

```text
Go Backend
   │
   │ MySQL Protocol
   ▼
TiProxy :6000
   │
   ▼
TiDB 8.0.0
```

改造后：

```text
              ┌───────────────┐
              │   studio-api  │
              └───────┬───────┘
                      │
              ┌───────▼───────┐
              │ studio-stream │
              └───────┬───────┘
                      │
              ┌───────▼───────┐
              │ studio-worker │
              └───────┬───────┘
                      │
              MySQL Protocol
                      │
                      ▼
               MySQL 5.7 :3306
                      │
                      ▼
                xiaoan3_go

Redis 继续独立承担：
- Session
- GCRA Rate Limit
- Redis Streams
- Pub/Sub
```

TiProxy 不再参与数据库链路。

---

# 3. 改造范围

本次建议修改以下文件。

| 文件 | 修改级别 | 处理 |
|---|---|---|
| `backend-go/.env.example` | 必须 | TiDB/TiProxy → MySQL 5.7，端口改 3306 |
| `backend-go/internal/platform/config/config.go` | 必须 | DB 默认端口 4000 → 3306 |
| `backend-go/internal/platform/database/database.go` | 必须 | 清理 TiDB/TiProxy 描述 |
| `backend-go/internal/app/migrate.go` | 建议必须 | 删除 TiDB 专属 migration DSN 探测 |
| `.github/workflows/backend.yml` | 必须 | MySQL 5.7 成为主 integration DB |
| `backend-go/tests/integration/tidb_cas_test.go` | 必须 | 改成数据库/MySQL integration 命名 |
| `backend-go/tests/integration/tidb_schedule_test.go` | 必须 | 同上 |
| `backend-go/README.md` | 必须 | 架构描述改为 MySQL 5.7 |
| `backend-go/internal/execution/slots.go` | 建议 | 清除 TiDB 专属 9007 及相关注释 |
| `backend-go/db/queries/execution.sql` | 建议 | 清除 TiDB 专属说明 |
| `backend-go/db/migrations/0011*.sql` | 建议 | 注释改为 MySQL/InnoDB |
| `backend-go/db/migrations/0013*.sql` | 建议 | 清理 TiDB optimistic transaction 描述 |
| `docs/**` | 建议 | 清理当前架构中的 TiDB 描述 |
| `internal/gen/db/**` | 禁止手改 | sqlc 生成文件 |

执行前首先扫描：

```bash
rg -n --hidden -S \
"TiDB|tidb|TiProxy|tiproxy|STUDIO_TEST_TIDB|DB_PORT=6000|DB_PORT.*4000|:6000|:4000" \
backend-go .github README.md docs
```

最终目标是：生产数据库路径里不再存在 TiDB/TiProxy 假设。

---

# 4. 第一阶段：修改数据库配置

## 4.1 `.env.example`

当前：

```env
# Database: TiDB 8.0.0 via TiProxy.
DB_HOST=192.168.212.38
DB_PORT=6000
DB_NAME=xiaoan3_go
DB_USER=xiaoanuser
```

当前仓库确实如此配置。

修改为：

```env
# ── Database: MySQL 5.7 ──
DB_HOST=192.168.xxx.xxx
DB_PORT=3306
DB_NAME=xiaoan3_go
DB_USER=xiaoanuser
DB_PASSWORD=replace-me

DB_MAX_OPEN_CONNS=40
DB_MAX_IDLE_CONNS=10
DB_CONN_MAX_LIFETIME=30m
DB_CONN_MAX_IDLE_TIME=5m
DB_QUERY_TIMEOUT=15s
```

不要继续经过 TiProxy。

---

# 5. 修改默认数据库端口

文件：

```text
backend-go/internal/platform/config/config.go
```

当前代码：

```go
Database: DatabaseConfig{
    Host: getEnv("DB_HOST", "127.0.0.1"),
    Port: getEnvInt("DB_PORT", 4000),
```



修改：

```go
Database: DatabaseConfig{
    Host: getEnv("DB_HOST", "127.0.0.1"),
    Port: getEnvInt("DB_PORT", 3306),
```

这是一个必须修改项。

否则一旦某环境漏配 `DB_PORT`，应用仍会默认连接 TiDB 标准端口 `4000`。

---

# 6. DSN 不需要重写

当前：

```go
return fmt.Sprintf(
    "%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=true&loc=UTC&multiStatements=true&timeout=10s&readTimeout=60s&writeTimeout=60s",
    d.User, d.Password, d.Host, d.Port, d.Name,
) + sysVars
```

并带：

```text
time_zone='+00:00'
```



这套 DSN 可以继续用于 MySQL 5.7。

建议保留：

```text
charset=utf8mb4
parseTime=true
loc=UTC
time_zone='+00:00'
timeout=10s
readTimeout=60s
writeTimeout=60s
```

尤其不能随意删除 UTC 设计。

当前 Run Lease、Provider Slot、Schedule、Retry 等大量逻辑都将数据库时间作为权威时钟：

```sql
CURRENT_TIMESTAMP(3)
DATE_ADD(CURRENT_TIMESTAMP(3), ...)
```

切换数据库后如果 MySQL session 时区错误，可能直接造成：

- Lease 提前失效
- Lease 延迟失效
- Retry 时间错误
- Schedule 错误触发
- Provider slot 误释放

因此建议继续：

```sql
SET time_zone = '+00:00';
```

由 DSN 自动完成。

---

# 7. database.go 清理

文件：

```text
backend-go/internal/platform/database/database.go
```

目前代码实际上已经是标准：

```go
sql.Open("mysql", cfg.DSN())
```



因此逻辑不用改变。

只修改：

```go
// Package database opens and tunes the MySQL connection pool.
```

以及：

```go
// Open connects to MySQL 5.7 with conservative pool defaults.
```

删除：

```text
TiDB
TiProxy
```

描述。

---

# 8. 删除 TiDB 专属 migration 兼容代码

这是本次代码清理比较重要的一处。

文件：

```text
backend-go/internal/app/migrate.go
```

当前专门存在：

```go
tidbCompatibleDSN()
```

它会：

```sql
SELECT VERSION()
```

如果发现 TiDB：

```text
tidb_skip_isolation_level_check=1
```

目的是绕开 TiDB 不支持 `SERIALIZABLE` 的问题。

MySQL 5.7 不需要这一套。

目标代码可以简化为：

```go
func MigrateUp(ctx context.Context, dsn, migrationsDir string) error {
    db, err := sql.Open("mysql", dsn)
    if err != nil {
        return err
    }
    defer db.Close()

    if err := db.PingContext(ctx); err != nil {
        return fmt.Errorf(
            "cannot reach mysql database (does it exist? see README bootstrap): %w",
            err,
        )
    }

    driver, err := mysqldrv.WithInstance(
        db,
        &mysqldrv.Config{},
    )
    if err != nil {
        return err
    }

    m, err := migrate.NewWithDatabaseInstance(
        "file://"+migrationsDir,
        "mysql",
        driver,
    )
    if err != nil {
        return err
    }

    if err := m.Up(); err != nil &&
        !errors.Is(err, migrate.ErrNoChange) {
        return err
    }

    slog.Info("migrations applied")
    return nil
}
```

同时删除：

```go
"strings"
```

以及：

```go
tidbCompatibleDSN()
appendDSNParam()
```

这样数据库 migration 逻辑就成为纯 MySQL 实现。

---

# 9. Go Driver 不修改

`go.mod` 当前已经有：

```go
github.com/go-sql-driver/mysql v1.9.3
github.com/golang-migrate/migrate/v4 v4.19.1
```



因此：

**不要换驱动。**

不需要：

```text
gorm
mysql ORM
TiDB SDK
```

也不需要重新设计 Repository。

现有：

```text
database/sql
   ↓
go-sql-driver/mysql
   ↓
sqlc
```

非常适合继续使用。

---

# 10. sqlc 不修改

`sqlc.yaml` 已经是：

```yaml
engine: "mysql"
schema: "db/migrations/*.up.sql"
queries: "db/queries"
```



因此保持。

只有 SQL 本身发生实质修改时才执行：

```bash
cd backend-go
make gen
```

如果此次只是：

- 修改注释
- 修改 DB port
- 删除 TiDB migration compatibility
- 修改 CI

则没有必要因为切数据库而重新生成 `internal/gen/db`。

禁止直接修改：

```text
backend-go/internal/gen/db/*.go
```

---

# 11. Migration SQL 原则上无需改写

这是本次审查的重点结论。

`0001_core_schema.up.sql` 文件开头已经明确：

```text
MySQL 5.7 compatible
no TiDB-only syntax
no functional indexes
no DEFAULT on TEXT/JSON columns
```



当前实际大量使用：

```sql
ENGINE=InnoDB
DEFAULT CHARSET=utf8mb4
COLLATE=utf8mb4_bin
```

例如：

```sql
CREATE TABLE runs (
    id BINARY(16) NOT NULL,
    ...
    input JSON NULL,
    ...
    queued_at DATETIME(3)
        NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    ...
    PRIMARY KEY (id)
) ENGINE=InnoDB
  DEFAULT CHARSET=utf8mb4
  COLLATE=utf8mb4_bin;
```

这些都可以直接运行在 MySQL 5.7。

Schedule 表同样使用标准 MySQL 5.7 类型和索引。

所以：

**禁止为了“迁移 MySQL”而重新设计现有表。**

此次应属于数据库平台切换，而不是 Schema 重构。

---

# 12. Provider Admission 并发模型保留

当前这一块是 potal 数据库代码中最敏感的部分。

例如：

```sql
UPDATE provider_admission_locks
SET admissions = admissions + 1
WHERE provider = ?;
```

通过真实写冲突串行化：

```text
delete expired
→ count active
→ insert slot
```



在 MySQL 5.7/InnoDB 下，这种方案反而比 TiDB 更符合标准行锁模型。

同时现有代码已经处理：

```go
case
    1213, // MySQL deadlock victim
    1205: // MySQL lock wait timeout
```



因此该设计：

**保留。**

---

# 13. 是否删除 TiDB Error 9007

当前还有：

```go
case 9007, // TiDB optimistic write conflict
     1213,
     1205:
```

如果本项目明确决定：

> 从此不再支持 TiDB。

那么最终可以改成：

```go
switch mysqlErr.Number {
case 1213, // deadlock
    1205: // lock wait timeout
    return true
default:
    return false
}
```

建议修改文件：

```text
backend-go/internal/execution/slots.go
```

同时将所有：

```text
TiDB transaction
TiDB optimistic conflict
TiDB correctness plane
```

改成：

```text
MySQL/InnoDB transaction
database transaction
durable database state
```

注意：

**不能因为删除 TiDB 兼容而删除整个 transaction retry。**

MySQL 5.7 一样存在：

```text
1213 Deadlock found when trying to get lock
1205 Lock wait timeout exceeded
```

事务重试仍然有必要。

---

# 14. Integration Test 必须重命名

当前有：

```text
tests/integration/tidb_cas_test.go
tests/integration/tidb_schedule_test.go
```

并通过：

```env
STUDIO_TEST_TIDB=1
```

启用数据库 Integration Test。

但是目前 MySQL 5.7 CI 本身居然也是设置：

```yaml
STUDIO_TEST_TIDB: '1'
```

然后连接 MySQL 5.7。

这证明这个变量现在实际上已经变成：

> “启用真实数据库测试”

而不是：

> “使用 TiDB”。

必须清理。

建议改成：

```env
STUDIO_TEST_DB=1
```

或者更明确：

```env
STUDIO_TEST_MYSQL=1
```

推荐前者：

```text
STUDIO_TEST_DB
```

未来如果升级 MySQL 8，不用再改名字。

修改：

```go
if os.Getenv("STUDIO_TEST_DB") != "1" {
    t.Skip("set STUDIO_TEST_DB=1 to run database integration tests")
}
```

并：

```go
db, err := database.Open(...)
if err != nil {
    t.Fatalf("database: %v", err)
}
```

不要继续：

```go
t.Fatalf("tidb: %v", err)
```

文件建议重命名：

```text
tidb_cas_test.go
→ database_cas_test.go

tidb_schedule_test.go
→ database_schedule_test.go
```

---

# 15. CI 是此次改造最重要的地方

当前 CI 已经有：

```yaml
mysql57:
  services:
    mysql:
      image: mysql:5.7
```

并执行：

```text
wait for mysql 5.7
apply migrations
integration tests
```

最新提交上：

```text
mysql57 = success
```

包括：

```text
MySQL 5.7 container     SUCCESS
apply migrations        SUCCESS
integration tests       SUCCESS
```



这意味着当前数据库兼容性基础已经通过真实环境验证。

但是当前还有一个问题：

现有 TiDB `integration` Job 执行：

```text
execution + delivery package tests
tests/integration
Redis
TiDB
```

而现有 `mysql57` Job 主要执行：

```text
migration
tests/integration
```

**覆盖范围并不完全相同。**

因此不能简单：

> 删除 TiDB integration Job，只留下现在的 mysql57 Job。

正确做法是：

## 将 TiDB Integration Job 的完整测试能力搬到 MySQL 5.7 Job。

目标 CI：

```text
check
 ├─ gofmt
 ├─ go vet
 ├─ go build
 ├─ unit test
 └─ race

integration-mysql57
 ├─ MySQL 5.7
 ├─ Redis 7
 ├─ migration
 ├─ internal/execution tests
 ├─ internal/delivery tests
 └─ tests/integration
```

然后删除：

```text
integration-tidb
```

---

# 16. 推荐新的 CI 环境

核心形态：

```yaml
integration:
  runs-on: ubuntu-latest

  env:
    STUDIO_TEST_DB: '1'
    STUDIO_TEST_REDIS: '1'

    DB_HOST: 127.0.0.1
    DB_PORT: 3306
    DB_NAME: xiaoan3_go
    DB_USER: root
    DB_PASSWORD: ''

    REDIS_HOST: 127.0.0.1
    REDIS_PORT: 6379
    REDIS_DB: '2'

  services:
    mysql:
      image: mysql:5.7
      env:
        MYSQL_ALLOW_EMPTY_PASSWORD: 'yes'
        MYSQL_DATABASE: xiaoan3_go
      ports:
        - 3306:3306

    redis:
      image: redis:7-alpine
      ports:
        - 6379:6379
```

然后依次：

```bash
go run ./cmd/migrate
```

```bash
go test \
  ./internal/execution/... \
  ./internal/delivery/... \
  -count=1 -v
```

```bash
go test ./tests/integration/... -count=1 -v
```

MySQL 5.7 Job 全绿才允许合并。

---

# 17. MySQL 5.7 初始化

建议仍沿用现有数据库名：

```text
xiaoan3_go
```

## 17.1 建库

管理员执行：

```sql
CREATE DATABASE xiaoan3_go
  CHARACTER SET utf8mb4
  COLLATE utf8mb4_bin;
```

选择 `utf8mb4_bin` 是为了和当前全部 migration 保持一致。

不要擅自改：

```text
utf8mb4_general_ci
utf8mb4_unicode_ci
```

否则字符串唯一索引和大小写比较语义可能变化。

---

# 18. 推荐数据库账户方案

生产建议分成两个账号。

## Migration 账号

```sql
CREATE USER 'potal_migrate'@'%'
IDENTIFIED BY 'CHANGE_ME_STRONG_PASSWORD';

GRANT ALL PRIVILEGES
ON xiaoan3_go.*
TO 'potal_migrate'@'%';
```

用于：

```bash
go run ./cmd/migrate
```

## Runtime 账号

```sql
CREATE USER 'potal_app'@'%'
IDENTIFIED BY 'CHANGE_ME_STRONG_PASSWORD';

GRANT
    SELECT,
    INSERT,
    UPDATE,
    DELETE
ON xiaoan3_go.*
TO 'potal_app'@'%';
```

然后：

```sql
FLUSH PRIVILEGES;
```

推荐不要让长期运行的：

```text
studio-api
studio-stream
studio-worker
scheduler
```

持有：

```text
DROP
ALTER
CREATE
```

权限。

部署时：

```text
Migration Job
DB_USER=potal_migrate

正式服务
DB_USER=potal_app
```

当前 `cmd/migrate` 本来就是独立 migration-only 入口，非常适合这样部署。

---

# 19. MySQL 基础配置

建议确认以下变量：

```sql
SELECT VERSION();

SHOW VARIABLES LIKE 'character_set_server';
SHOW VARIABLES LIKE 'collation_server';
SHOW VARIABLES LIKE 'time_zone';
SHOW VARIABLES LIKE 'sql_mode';
SHOW VARIABLES LIKE 'transaction_isolation';
SHOW VARIABLES LIKE 'max_connections';
```

目标至少满足：

```text
version             = 5.7.x
character_set       = utf8mb4
DB schema collation = utf8mb4_bin
```

建议继续启用严格 SQL Mode，不要为了兼容历史 SQL 关闭：

```text
STRICT_TRANS_TABLES
ONLY_FULL_GROUP_BY
NO_ZERO_IN_DATE
NO_ZERO_DATE
ERROR_FOR_DIVISION_BY_ZERO
NO_ENGINE_SUBSTITUTION
```

不要通过关闭严格模式掩盖代码问题。

---

# 20. Connection Pool 必须重新核算

当前每个进程默认：

```text
DB_MAX_OPEN_CONNS=40
DB_MAX_IDLE_CONNS=10
```



TiDB 是分布式数据库，而 MySQL 5.7 是单实例或传统主从体系。

因此这一项不能照搬后就不管。

最大理论连接数大约为：

```text
所有 Go 数据库客户端进程数量
× DB_MAX_OPEN_CONNS
```

例如：

```text
2 API
2 Stream
5 Worker
1 Scheduler

共 10 个进程
```

如果全部：

```text
40 max open
```

理论上：

```text
10 × 40 = 400 connections
```

所以需要根据实际副本数量配置 MySQL：

```text
max_connections
```

以及应用池大小。

建议第一版不要盲目扩大连接池。

---

# 21. MySQL 锁行为是上线重点

从 TiDB 切换 MySQL 后，最大的运行时区别不是 SQL 语法，而是：

> InnoDB 的真实行锁、Next-Key Lock、Gap Lock 和 Deadlock 行为。

potal 当前大量依赖：

```sql
SELECT ... FOR UPDATE
UPDATE CAS
provider admission lock
run lease
provider slot
schedule occurrence
outbox
```

因此生产验证重点应放在：

```text
并发 Claim
Provider max_inflight
Lease Heartbeat
Reaper
Finalize
Retry
Schedule admission
Outbox
```

而不是普通 CRUD。

尤其关注日志：

```text
Error 1213
Error 1205
```

当前 Provider Admission 已经可以自动重试这两类错误。

但上线初期仍建议监控发生频率。

---

# 22. 全新数据库初始化流程

如果目前 TiDB 数据不需要保留，这是最推荐的切换方式。

## Step 1

创建：

```text
xiaoan3_go
```

## Step 2

设置：

```env
DB_HOST=<mysql-ip>
DB_PORT=3306
DB_NAME=xiaoan3_go
DB_USER=potal_migrate
DB_PASSWORD=***
```

## Step 3

执行：

```bash
cd backend-go

go run ./cmd/migrate
```

## Step 4

确认：

```sql
SELECT * FROM schema_migrations;
```

应：

```text
dirty = 0
```

并达到当前最新 migration。

当前仓库 migrations 已到：

```text
0013_provider_admission_lock_write
```

## Step 5

切 runtime 账号：

```env
DB_USER=potal_app
DB_PASSWORD=***
```

## Step 6

启动：

```text
studio-api
studio-stream
studio-worker
scheduler
```

---

# 23. 如果 TiDB 已有正式数据

如果当前 `xiaoan3_go` 已经有业务数据，不要直接：

```text
mysqldump TiDB schema + data
→ mysql < dump.sql
```

尤其不要把 TiDB `SHOW CREATE TABLE` 输出当成目标 Schema 的权威来源。

推荐：

```text
代码 migrations
      ↓
建立 MySQL Schema
      ↓
只迁移业务数据
```

即：

## Schema 权威来源

```text
backend-go/db/migrations
```

## Data 权威来源

```text
当前 TiDB
```

---

# 24. 有数据情况下的推荐迁移流程

停写窗口方式最稳妥：

```text
1. MySQL 建空库
2. 运行当前 migrations
3. 关闭 potal 写入
4. 停 worker / scheduler
5. TiDB 做最终逻辑导出
6. 数据导入 MySQL
7. 数据校验
8. DB_HOST / DB_PORT 切 MySQL
9. 启动服务
10. Smoke Test
11. 开放流量
```

二进制 UUID 字段大量使用：

```text
BINARY(16)
```

所以使用 mysqldump 时必须特别注意：

```bash
--hex-blob
```

例如：

```bash
mysqldump \
  -h <tidb-host> \
  -P 6000 \
  -u <user> \
  -p \
  --single-transaction \
  --quick \
  --hex-blob \
  --no-create-info \
  xiaoan3_go \
  > potal_data.sql
```

但正式执行前必须处理：

```text
schema_migrations
migration seed rows
```

不要把源库的 `schema_migrations` 直接覆盖目标 migration 状态。

数据量很大时建议改用：

```text
Dumpling / CSV / ETL
```

做逻辑数据迁移，而不是物理备份。

TiDB BR 备份不能作为 MySQL 5.7 的直接恢复文件。

---

# 25. 数据迁移后的校验

至少检查：

## 表数量

```sql
SELECT COUNT(*)
FROM information_schema.tables
WHERE table_schema = 'xiaoan3_go';
```

## 各业务表记录量

源和目标分别执行：

```sql
SELECT COUNT(*) FROM users;
SELECT COUNT(*) FROM providers;
SELECT COUNT(*) FROM applications;
SELECT COUNT(*) FROM conversations;
SELECT COUNT(*) FROM messages;
SELECT COUNT(*) FROM runs;
SELECT COUNT(*) FROM run_events;
SELECT COUNT(*) FROM run_leases;
SELECT COUNT(*) FROM provider_execution_slots;
SELECT COUNT(*) FROM schedules;
SELECT COUNT(*) FROM schedule_occurrences;
SELECT COUNT(*) FROM delivery_executions;
```

必须逐表比对。

---

# 26. BINARY(16) 特别检查

例如：

```sql
SELECT
    COUNT(*) total,
    SUM(LENGTH(id) <> 16) invalid_id
FROM runs;
```

目标：

```text
invalid_id = 0
```

对：

```text
runs.id
run_commands.id
run_artifacts.id
runtime_attachments.id
agent_threads.id
```

等 BINARY UUID 表都建议检查。

---

# 27. 数据库时间检查

执行：

```sql
SELECT
    CURRENT_TIMESTAMP(3),
    UTC_TIMESTAMP(3),
    @@session.time_zone,
    @@global.time_zone;
```

应用连接后的：

```text
@@session.time_zone
```

应该为：

```text
+00:00
```

这是 Lease / Retry / Schedule 正确性的上线门槛。

---

# 28. Migration 验证

执行：

```bash
go run ./cmd/migrate
```

第一次：

```text
migrations applied
```

第二次再次执行应该：

```text
NoChange
```

而不能：

```text
dirty
duplicate table
duplicate column
syntax error
```

---

# 29. 必须执行的 Go 测试

代码修改完成以后：

```bash
cd backend-go
```

执行：

```bash
gofmt -w .
```

```bash
go vet ./...
```

```bash
go build ./...
```

```bash
go test ./... -count=1
```

然后以真实 MySQL 5.7 + Redis：

```bash
STUDIO_TEST_DB=1 \
STUDIO_TEST_REDIS=1 \
go test ./internal/execution/... ./internal/delivery/... -count=1 -v
```

以及：

```bash
STUDIO_TEST_DB=1 \
STUDIO_TEST_REDIS=1 \
go test ./tests/integration/... -count=1 -v
```

重点保证：

```text
CAS race single winner
Lease expiry
Reaper
Provider admission
Provider max inflight
Previous epoch fencing
Finalize
Retry
Outbox
Schedule
Delivery
DB clock
```

全部通过。

---

# 30. GitHub CI 上线门槛

最终 GitHub Actions 建议只保留两个数据库相关逻辑层次：

```text
check
integration-mysql57
```

要求：

```text
check                 GREEN
mysql57 migration     GREEN
mysql57 DB tests      GREEN
mysql57 + Redis       GREEN
```

当前最新代码事实上已经：

```text
mysql57
  wait for mysql       SUCCESS
  apply migrations     SUCCESS
  integration tests    SUCCESS
```



因此此次切换的基础风险已经明显低于一般 TiDB → MySQL 迁移。

不过在删除 TiDB Job 之前，必须先把它目前承担的：

```text
execution + delivery real DB/Redis tests
```

迁到 MySQL 5.7 Job。

---

# 31. README 修改

当前 README 明确写：

```text
TiDB（Source of Truth）
Outbox Relay: TiDB → Redis Streams
Worker: XREADGROUP → TiDB CAS Claim
```



全部修改为：

```text
MySQL 5.7（Source of Truth）
Outbox Relay: MySQL → Redis Streams
Worker: XREADGROUP → MySQL CAS Claim
```

并将：

```bash
STUDIO_TEST_TIDB=1
```

改为：

```bash
STUDIO_TEST_DB=1
```

---

# 32. 注释与命名清理

重点扫描：

```bash
rg -n -i \
"tidb|tiproxy|studio_test_tidb|optimistic transaction|9007" \
backend-go .github docs
```

对于已经不再成立的 TiDB 专属描述全部清理。

但是不要机械替换历史分析文档。

建议：

```text
docs/archive/
历史复审报告
历史事故分析
```

中涉及 TiDB 的历史记录可以保留。

真正需要修改的是：

```text
README
代码注释
运行配置
CI
当前架构文档
测试名称
```

---

# 33. 生产上线顺序

建议严格按照以下顺序。

## T-1：部署前

确认：

```text
[ ] MySQL 5.7 实例准备完成
[ ] xiaoan3_go 已创建
[ ] utf8mb4_bin
[ ] migration account 已创建
[ ] runtime account 已创建
[ ] migration 0001~0013 全部通过
[ ] MySQL CI 全绿
[ ] Redis 不变
[ ] DB timezone 验证通过
[ ] max_connections 核算完成
```

---

## T0：进入维护

如果存在正式 TiDB 数据：

```text
1. 阻断新业务写入
2. 停 scheduler
3. 停 worker
4. 停 API
5. 停 stream
```

确保 TiDB 不再产生新数据。

---

## T1：最终数据迁移

```text
TiDB
 ↓
logical export
 ↓
MySQL 5.7
```

完成：

```text
row count
UUID
关键业务数据
schema_migrations
```

校验。

---

## T2：切换配置

修改生产环境：

```env
DB_HOST=<mysql-host>
DB_PORT=3306
DB_NAME=xiaoan3_go
DB_USER=potal_app
DB_PASSWORD=***
```

完全删除：

```text
TiProxy :6000
TiDB :4000
```

依赖。

---

## T3：启动

推荐：

```text
1. API
2. Stream
3. Worker
4. Scheduler
```

然后检查日志。

---

# 34. Smoke Test

至少执行以下业务测试：

```text
[ ] 飞书登录
[ ] 用户读取
[ ] Application 列表
[ ] 创建 Conversation
[ ] 发送 Message
[ ] 创建 Run
[ ] Run 被 Worker Claim
[ ] Provider Slot 正常占用
[ ] Aily 执行成功
[ ] Run Finalize
[ ] SSE 正常收到事件
[ ] Conversation History 正常
[ ] Retry 正常
[ ] Schedule 创建
[ ] Schedule 触发
[ ] Delivery 正常
[ ] Worker 重启后 Lease/Reaper 正常
```

并观察：

```text
1213
1205
duplicate key
lock wait
connection refused
too many connections
invalid JSON
incorrect datetime
```

---

# 35. Rollback 方案

这是数据库切换最容易被忽略的一点。

在 MySQL 正式开放写入之前：

```text
MySQL → TiDB
```

回滚很简单：

```env
DB_HOST=<old-tiproxy>
DB_PORT=6000
```

重新启动旧版本应用即可。

但是：

> 一旦正式业务已经向 MySQL 写入新数据，TiDB 与 MySQL 就开始分叉。

此时不能简单切回 TiDB。

否则会丢失：

```text
MySQL 切换后产生的 Run
Message
Schedule
Conversation
Delivery
User changes
```

所以定义两个回滚阶段：

## 阶段 A：开放业务前

允许快速回滚。

## 阶段 B：MySQL 已接收正式写入后

不得直接回切。

必须：

```text
停止写入
MySQL 新增数据反向迁移
校验
再切 TiDB
```

或者接受业务数据损失。

因此真正的回滚点应该放在：

```text
正式开放流量之前。
```

---

# 36. 风险等级

## P0：必须完成

### P0-1 MySQL Integration Coverage

当前 MySQL 5.7 Job 虽然成功，但必须继承 TiDB Job 的完整 DB + Redis 测试覆盖。

否则不能删除 TiDB Integration Job。

### P0-2 Existing Data Migration

如果 TiDB 已有正式数据：

必须完成逻辑数据迁移和逐表校验。

### P0-3 DB Clock

必须确认：

```text
session time_zone = +00:00
```

---

## P1：上线前处理

### P1-1 MySQL Lock / Deadlock

关注：

```text
1213
1205
```

### P1-2 max_connections

根据实际进程/副本数量重新计算。

### P1-3 SQL Mode

不得通过关闭严格模式解决 SQL 问题。

### P1-4 Collation

目标统一：

```text
utf8mb4_bin
```

---

## P2：技术债清理

包括：

```text
TiDB 注释
TiDB 测试文件名
STUDIO_TEST_TIDB
TiProxy 描述
9007
TiDB 专属 migration probe
旧架构 README
```

---

# 37. 最终代码状态要求

完成后：

```bash
rg -n -i \
"tidb|tiproxy|studio_test_tidb|tidb_skip_isolation_level_check" \
backend-go .github
```

理想结果：

```text
0 个运行时代码命中
0 个运行配置命中
0 个 CI 命中
```

历史 archive 文档除外。

同时：

```bash
rg -n "DB_PORT.*4000|DB_PORT=6000" backend-go .github
```

应无生产配置命中。

---

# 38. 本项目哪些东西明确“不需要改”

不要扩大改造范围。

以下内容无需因为 MySQL 5.7 而重构：

```text
Go Repository 层
sqlc
database/sql
go-sql-driver/mysql
Redis
Run UUIDv7/BINARY(16)
JSONText
Outbox
Run Lease
Provider Slot
CAS
Schedule
Delivery
OpenAPI
Frontend
```

尤其不要把：

```text
sqlc + database/sql
```

改成：

```text
GORM
```

这和本次数据库切换没有关系，反而会扩大风险。

---

# 39. 推荐执行批次

建议开发按三个 Commit 完成。

## Commit 1：MySQL Runtime Baseline

```text
config.go
.env.example
database.go
migrate.go
slots.go
README
代码注释
```

Commit：

```text
refactor(database): make MySQL 5.7 the primary database backend
```

---

## Commit 2：Test / CI

修改：

```text
STUDIO_TEST_TIDB
→ STUDIO_TEST_DB

tidb_cas_test.go
→ database_cas_test.go

tidb_schedule_test.go
→ database_schedule_test.go
```

重构：

```text
backend.yml
```

只以：

```text
mysql:5.7 + redis:7
```

完成完整 integration gate。

Commit：

```text
ci(database): replace TiDB integration gate with MySQL 5.7
```

---

## Commit 3：Documentation Cleanup

处理：

```text
README
当前架构文档
当前运维文档
代码注释
```

Commit：

```text
docs(database): remove TiDB runtime references
```

---

# 40. 验收标准

只有以下项目全部通过才算“完成切换”：

```text
[ ] 默认 DB_PORT 已为 3306
[ ] .env.example 已改 MySQL 5.7
[ ] TiProxy 不再使用
[ ] tidbCompatibleDSN 已删除
[ ] tidb_skip_isolation_level_check 已删除
[ ] sqlc 仍为 mysql engine
[ ] migration 0001~0013 可在全新 MySQL 5.7 执行
[ ] schema_migrations dirty=0
[ ] MySQL 5.7 + Redis 完整 integration test 全绿
[ ] CAS single winner 通过
[ ] Provider max_inflight 通过
[ ] Lease/Reaper/Fencing 通过
[ ] Schedule/Delivery 通过
[ ] session time_zone=+00:00
[ ] utf8mb4_bin
[ ] max_connections 完成容量核算
[ ] README 当前架构已经改为 MySQL
[ ] 运行时代码不存在 TiDB/TiProxy 专属逻辑
[ ] 正式 Smoke Test 全部通过
```

---

# 41. 最终判断

基于当前代码，我认为：

**potal 从 TiDB 8.0.0 切换到 MySQL 5.7 的技术风险属于“中低”，不是数据库层重构。**

原因是：

1. 驱动本来就是标准 MySQL Driver。
2. sqlc 本来就是 MySQL Engine。
3. Schema 从一开始就明确按 MySQL 5.7 兼容设计。
4. UUID 没有使用 MySQL 8 的 `UUID_TO_BIN()` 等能力。
5. JSON 没有依赖 MySQL 8 JSON_TABLE。
6. 核心并发控制已经考虑 MySQL 1213/1205。
7. GitHub CI 已经真实运行 `mysql:5.7`。
8. 最新 MySQL 5.7 migration + integration test 已经成功。

当前最大的工作实际上不是“让代码能跑 MySQL 5.7”，而是：

> **把已经具备的 MySQL 5.7 兼容能力变成唯一生产标准，并彻底删除 TiDB/TiProxy 的运行时假设。**

因此不建议做大规模 SQL/Repository 重构。

**推荐实施路线：**

```text
删除 TiDB 专属兼容
        ↓
MySQL 3306 成为默认配置
        ↓
将完整 integration gate 搬到 mysql:5.7
        ↓
全新 MySQL 跑 migration
        ↓
真实 MySQL + Redis 全套并发测试
        ↓
如有数据则进行逻辑数据迁移
        ↓
生产切换
        ↓
Smoke Test
```

这就是本项目当前代码基础下成本最低、风险最可控的 MySQL 5.7 切换方案。