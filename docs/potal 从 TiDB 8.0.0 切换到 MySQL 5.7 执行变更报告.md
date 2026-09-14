# potal 从 TiDB 8.0.0 切换到 MySQL 5.7 执行变更报告

**项目：** `shilin414/potal`
**基线分支：** `dev`
**执行依据：** `docs/potal 从 TiDB 8.0.0 切换到 MySQL 5.7 执行指南.md`
**文档日期：** 2026-09-14
**执行范围：** 代码改造（2 个 commit）+ 目标库 migration + TiDB→MySQL 数据迁移与校验 + 真实 MySQL/Redis 集成闸门 + CI 验证
**推送状态：** 已推送至 `shilin414/potal` 的 `dev`，远端 HEAD `c53f934`

---

# 1. 执行结论

切换已按指南完成，**Go 侧无运行时 TiDB/TiProxy 假设残留**，Schema 与全量业务数据已在 MySQL 5.7 上落库并通过逐表内容校验，集成测试闸门在真实 MySQL 5.7 + Redis 上全绿。

一句话概括本轮实际做的事情，与指南 §41 的判断一致：

> 把已经具备的 MySQL 5.7 兼容能力变成唯一生产基线，并真正**执行**了数据库切换（不只是改代码）。

与指南描述的“理想状态”不同的一点是：**本次目标库上已经有生产数据需要保留**，因此走了指南 §23–§26 的“有数据迁移”路径（Schema 以 migrations 为权威，只搬业务数据），而不是 §22 的“全新库初始化”路径。

| 阶段 | 结果 |
|---|---|
| 代码改造（Commit 1） | ✅ 完成 |
| 测试/CI 重构（Commit 2） | ✅ 完成 |
| 目标库 migration 0001→0013 | ✅ `version=13, dirty=0` |
| TiDB → MySQL 数据迁移 | ✅ 27 张表全部搬运 |
| 数据校验（行数 + 字节级 + JSON 语义 + BINARY(16) + 引用完整性） | ✅ 全部通过 |
| Go 单元测试 | ✅ 全绿 |
| 真实 MySQL 5.7 + Redis 集成测试 | ✅ 68/68 PASS，0 FAIL，0 SKIP |
| DB 时钟门槛（P0-3） | ✅ `session time_zone = +00:00` |
| GitHub CI | ✅ **run 34852157912 全绿**（`check` + `integration` 两个 job 全部 success） |
| Smoke Test（§34） | ✅ 四进程跑通完整链路：真实 Aily 执行 + 真实飞书投递成功（见 §14） |

### 1.1 提交与 CI

```text
c53f934 chore(memory): record the MySQL 5.7 switch outcome
0f43800 docs(database): record the TiDB 8.0.0 -> MySQL 5.7 switch
28632f6 ci(database): replace the TiDB integration gate with MySQL 5.7
64fbf80 refactor(database): make MySQL 5.7 the primary database backend
```

推送到 `origin/dev` 后 CI 首次运行即全绿，新 CI 结构（两个 job）得到真实验证：

```text
check        actionlint / gofmt / vet / build / unit / race            ✅
integration  MySQL 5.7 + Redis 容器                                    ✅
             ...wait for mysql 5.7                                    ✅
             ...apply migrations                                      ✅
             ...apply migrations (second run must be a clean no-op)    ✅
             ...database-backed package tests (real MySQL/Redis)       ✅
             ...integration tests                                     ✅
```

**注意这次不是「本来就绿」**：推送前 `dev` 的最新提交 `d659489` 在 GitHub 上是 **failure**（红），`c53f934` 是修好后的第一次 success——也就是说本轮同时把 CI 从红转绿。

> 本机 git 引用写入缺陷再次出现：`git push` 实际成功（`git ls-remote` 确认远端已是 `c53f934`），
> 但本地 `refs/remotes/origin/dev` 停留在旧 sha、`git status` 误报 `[ahead 9]`。
> 已按既有办法写 loose ref + 双写 `packed-refs` 修复，现为 `## dev...origin/dev`（0 0）。
> **结论：本机 push 后一律用 `git ls-remote` 核验，不要相信 git 打印的成功信息。**

---

# 2. 目标环境（实际连接参数）

| 项 | 值 |
|---|---|
| 版本 | **MySQL 5.7.32-log** |
| 主机 / 端口 | `192.168.211.26:20336` |
| 数据库 | `xiaoan` |
| 账号 | `test_user`（`GRANT ALL PRIVILEGES ON xiaoan.*`） |
| 库级字符集 / 排序规则 | `utf8mb4` / **`utf8mb4_bin`**（见 §7.2） |
| 表 | 28 张，全部 `InnoDB` + `utf8mb4_bin` |
| `max_connections` | 1000 |
| 源库（TiDB） | `192.168.212.38:6000` / `xiaoan3_go` / TiDB 8.0.0 |

`backend-go/.env.local`（git-ignored）已指向上述目标库。

---

# 3. Commit 1 — MySQL Runtime Baseline（`64fbf80`）

`refactor(database): make MySQL 5.7 the primary database backend`

## 3.1 运行配置

| 文件 | 改动 |
|---|---|
| `internal/platform/config/config.go` | `DB_PORT` 默认 **4000 → 3306**；`DB_NAME` 默认 `xiaoan3_go → xiaoan` |
| `.env.example` | 改为 MySQL 5.7 段（3306、连接池、`utf8mb4_bin` bootstrap、迁移账号/运行账号分离），不再经过 TiProxy |

DSN 未改动：`charset=utf8mb4&parseTime=true&loc=UTC&time_zone='+00:00'` 原样保留。**UTC 设计绝对不能删**——Run Lease / Provider Slot / Schedule / Retry 全部把数据库时钟当权威时钟。

## 3.2 删除 TiDB 专属兼容代码

| 文件 | 改动 |
|---|---|
| `internal/app/migrate.go` | 删除 `tidbCompatibleDSN()`、`appendDSNParam()`、`tidb_skip_isolation_level_check`、`strings` import。migration 变成纯 MySQL 路径，不再 `SELECT VERSION()` 探测服务端 |
| `internal/execution/slots.go` | 从可重试准入冲突中删除 **9007**（TiDB optimistic write conflict）。**1213 / 1205 保留**——事务边界重试在 InnoDB 上依然必需 |

## 3.3 注释与文档

- `database.go`、`app.go`、`execution/*`、`identity/oauth.go`、`integrations/aily/*`、`transport/sse/*`、`catalog`、`delivery`、`automation/schedule`、`platform/ids`、`platform/telemetry`、`cmd/worker`：`TiDB` → `MySQL` / `database`；`TiDB transaction` → `database transaction`
- `db/migrations/0001,0005,0006,0011,0013`、`db/queries/conversation.sql`、`db/queries/execution.sql`：注释改写为 MySQL/InnoDB。其中 0013 的准入串行化论证**不再依赖任何引擎专属错误码**，改为「locking read 本身不足，必须产生真实写冲突，失败者以新快照重试」
- `README.md`（根）+ `backend-go/README.md`：架构图、bootstrap、测试命令全部改为 MySQL 5.7；`STUDIO_TEST_TIDB` → `STUDIO_TEST_DB`

**未改动（按指南 §38）**：驱动、`sqlc.yaml`、`database/sql`、Repository 层、Redis、UUIDv7/BINARY(16)、JSONText、Outbox、Lease、CAS、Schedule、Delivery、OpenAPI、前端。没有引入 GORM。

---

# 4. Commit 2 — Test / CI（`28632f6`）

`ci(database): replace the TiDB integration gate with MySQL 5.7`

## 4.1 测试

| 改动 | 说明 |
|---|---|
| `STUDIO_TEST_TIDB` → `STUDIO_TEST_DB` | 该变量语义一直是「启用真实数据库测试」，与是否 TiDB 无关；改名后未来升级 MySQL 8 不用再动 |
| `tidb_cas_test.go` → `database_cas_test.go` | |
| `tidb_schedule_test.go` → `database_schedule_test.go` | |
| `migrate_test.go` 重写 | 现在断言 3 件事：① MigrateUp 在 MySQL 5.7 上**不需要任何引擎专属 DSN 兼容**；② 二次执行是干净的 no-op；③ `schema_migrations` 最终 `dirty=0` 且 `version >= 13` |
| 测试注释 | TiDB 措辞改为 MySQL/InnoDB；准入串行化的测试注释不再引用 9007 |

## 4.2 CI（本次唯一的实质结构调整）

指南 §15 已经指出：**不能简单删掉 TiDB job 只留 mysql57 job**，因为原 TiDB job 额外承担了 `internal/execution` + `internal/delivery` 的真实 DB/Redis 包级测试。执行方案：

```text
check（不变）
 ├─ actionlint → gofmt → vet → build → unit → race

integration（唯一数据库闸门）
 ├─ MySQL 5.7 + Redis 7（services）
 ├─ ALTER DATABASE xiaoan … COLLATE utf8mb4_bin
 ├─ apply migrations
 ├─ apply migrations（第二次：断言幂等）
 ├─ 数据库支撑的包级测试（execution + delivery + platform）
 └─ tests/integration
```

`integration-tidb` job 已删除，其覆盖**全部**并入 `integration`。

三处顺带补强的缺口（原 CI 从未验证）：

1. **库级 collation 显式设为 `utf8mb4_bin`**：不再依赖容器默认排序规则。
2. **migration 幂等性断言**：第二次 `apply migrations` 必须干净通过，否则报 `migrate re-run failed (must be idempotent)`。
3. **`./internal/platform/...` 纳入包级测试**：`TestSessionTimezoneUTC`（断言 `session time_zone = +00:00`，即 Lease/Retry/Schedule 正确性的前提）此前在**两个 job 里都被静默跳过**——unit job 没设 `STUDIO_TEST_DB`，原 TiDB job 没跑这个包。这是指南 P0-3 在 CI 里的一个真实空缺。

---

# 5. 目标库 Migration

```bash
cd backend-go
go build -o ./studio-migrate.exe ./cmd/migrate && ./studio-migrate.exe
```

| 检查项 | 结果 |
|---|---|
| 首次执行 | `migrations applied` |
| 二次执行 | 干净通过（`ErrNoChange` 被吸收，无 duplicate table / column） |
| `schema_migrations` | **`version=13, dirty=0`** |
| 表数量 | 28 |
| 引擎 / 排序规则 | 全部 `InnoDB` / `utf8mb4_bin` |
| 库级排序规则 | `utf8mb4_bin` |

migration 全程未出现 `dirty` / `syntax error` / `duplicate`。

---

# 6. 数据迁移（TiDB → MySQL）

## 6.1 迁移原则

严格按指南 §23：

```text
Schema 权威来源 = backend-go/db/migrations（已在目标库执行）
Data  权威来源 = TiDB xiaoan3_go
```

- **不导出** TiDB 的 `CREATE TABLE`，**不覆盖** 目标 `schema_migrations`
- `schema_migrations` 整表跳过（目标自持状态）
- 迁移粒度：逐表流式读取 + 批量 INSERT，值以**原始字节**搬运（`parseTime=false`），因此 BINARY(16) UUID 与 JSON 文本都不经过任何编码往返
- 源端开启单个 REPEATABLE-READ 事务，27 张表读同一快照
- 迁移前先做 **schema 列集合比对**，任何漂移直接中止（实际：27 张表列完全一致）
- 迁移后做**逐表行数对账**，不一致即非零退出

## 6.2 遇到的真实问题：目标库已有 migration seed 行

目标库刚跑完 migration，4 张表已有 seed 行（`applications` 4、`application_categories` 1、`providers` 1、`provider_admission_locks` 1）。天真地直接 INSERT 会主键冲突。

更关键的是：**目标库的 seed 行与源库是同一批逻辑行，但自增 id 不同**——seed 的固定应用是 id `1,2,3,4`，而源库是 `30019,30021,30023,30025`。而 `applications.id` 是**直接暴露给前端 API 的 number**，必须以源库为准。

处理方式：以**自然键**（`slug` / `name` / `provider_key`）判定「目标行是不是源库已有的逻辑行」，确认后才允许删除目标 seed 行、再由源库整体写入（含源库 id）。守卫逻辑：

> 只有当一个目标行的**任意一个唯一键**都在源库存在时，才允许删除。
> 只要有一行在源库里找不到任何对应，立即中止并列出该行主键，绝不静默丢弃。

这个守卫在真实场景里立刻生效并拦住了第一版实现（第一版按主键比对，误报 `applications` id 2/4 为“目标独有”）。

## 6.3 迁移结果

27 张表全部搬运，**逐表行数对账 100% 一致**：

| 表 | 行数 | 表 | 行数 |
|---|---|---|---|
| agent_threads | 85 | provider_admission_locks | 140 |
| application_categories | 4 | provider_execution_slots | 0 |
| application_favorites | 0 | providers | 1 |
| applications | 7 | quota_policies | 0 |
| audit_logs | 0 | run_artifacts | 90 |
| conversation_shares | 10 | run_commands | 0 |
| conversations | 627 | run_events | 7198 |
| delivery_executions | 141 | run_leases | 40 |
| feishu_identities | 1 | runs | 4975 |
| messages | 653 | runtime_attachments | 0 |
| occurrence_delivery_expectations | 37 | runtime_bindings | 2 |
| outbox_events | 5379 | schedule_deliveries | 193 |
| schedules | 702 | schedule_occurrences | 943 |
| users | 83 | | |

---

# 7. 数据校验（指南 §25–§27）

## 7.1 逐表内容校验（不止是行数）

对每张表计算两个与顺序无关的校验和并跨库比对：

```sql
SELECT COUNT(*), SUM(CRC32(tuple)), BIT_XOR(CRC32(tuple)) FROM <table>
```

`tuple = CONCAT_WS(0x1f, <所有非 JSON 列>)`，因此 BINARY(16)、DECIMAL、`DATETIME(3)`、文本全部参与。

**结果：27 张表 `SUM(crc32)` 与 `BIT_XOR(crc32)` 全部一致，0 张表不匹配。**

JSON 列无法按文本比对——MySQL 5.7 在写入时会**重排对象键**（`{"provider":…,"run_id":…}` → `{"run_id":…,"provider":…}`）并输出 `", " / ": "`，而 TiDB 保持插入顺序。因此 JSON 列改为在 Go 侧解析后**规范化重编码**（键排序、数字字面量原样保留）再比对：

**结果：14 张含 JSON 的表全部语义一致，0 行内容差异。**

> 说明：这里刻意没有只比 `JSON_LENGTH` 之类的弱校验——那种做法只能证明“形状一样”，证明不了“内容一样”。

## 7.2 §26 BINARY(16) 完整性

17 个 BINARY(16) 列（`runs.id`、`run_events.run_id`、`run_leases.lease_token`、`outbox_events.aggregate_id`、`schedule_occurrences.run_id` …）全部检查：

| 检查 | 结果 |
|---|---|
| 非 NULL 值长度 ≠ 16 的行数 | **0**（全部 17 列） |
| NULL 计数 | 与源库逐列一致（如 `schedule_occurrences.run_id` 两侧同为 299） |

## 7.3 引用完整性（schema 无外键，故显式验证）

26 条关联关系逐条检查（run_events/run_leases/run_artifacts/delivery_executions → runs，messages → conversations，occurrence/delivery 系列 → schedules/occurrences，runtime_bindings → applications/providers …）。

其中 3 条非零，**但对照源库完全一致**，即属于共享 dev 库的既有状态，**不是迁移引入**：

| 关系 | 目标库 | 源库 | 性质 |
|---|---|---|---|
| `messages.conversation_id → conversations` | 26 | 26 | `conversation_id = 0` 的测试消息 |
| `runs.provider → providers` | 4061 | 4061 | 全部是 `itest_*` 合成 provider（集成测试产生） |
| `provider_admission_locks.provider → providers` | 139 | 139 | 全部是 `itest_slots_*` 测试串行化行 |

## 7.4 §27 数据库时钟

`TestSessionTimezoneUTC`（走应用自己的 DSN）通过：

```text
session time_zone = "+00:00", NOW() == UTC_TIMESTAMP() ✓
```

目标服务器 `global time_zone = +08:00`，但应用 DSN 强制 session 为 `+00:00`，因此 Lease / Retry / Schedule 的 DB 时钟语义与切换前一致。

---

# 8. 测试闸门结果

## 8.1 单元测试

```bash
go build ./... && go vet ./... && go test ./... -count=1
```

全部通过，`gofmt` 干净。

## 8.2 真实 MySQL 5.7 + Redis 集成测试

```bash
STUDIO_TEST_DB=1 STUDIO_TEST_REDIS=1 \
  go test ./internal/execution/... ./internal/delivery/... -count=1 -v   # ✅
STUDIO_TEST_DB=1 STUDIO_TEST_REDIS=1 \
  go test ./tests/integration/... -count=1 -v                            # ✅ 68 PASS / 0 FAIL / 0 SKIP
```

重点覆盖项（指南 §29 要求的全部命中）：

```text
CAS 单赢家            TestCASRaceSingleWinner
Lease 过期            TestLeaseExpiryAndReaper / TestLeaseExpiryUsesDatabaseClock
Reaper 原子恢复        TestReaperRecoveryAtomicRetry / …AtomicFail
Provider 准入并发      TestConcurrentProviderAdmissionOnFreshProvider
Provider max_inflight TestProviderSlotsEnforceGlobalMaxAcrossWorkers
上一 epoch 隔离        TestStaleEpochCannotMutateAfterLeaseRecovery
                      TestProviderSlotCannotBeRenewedByPreviousEpoch
Finalize 事务持久      TestFinalizeTransactionDurability
Retry 时序一致        TestRetryRunAndOutboxShareRetryAt
Outbox               TestOutboxRelayCanonicalUUID / TestCreateRunOutboxCarriesPriorityClass
Schedule             TestDualSchedulerSingleOccurrence / …PendingAdmissionNoParallel
Delivery             TestDeliveryFanoutIdempotent
DB 时钟              TestLeaseExpiryUsesDatabaseClock / …RetryAvailabilityIsDatabaseClockBased
迁移路径              TestMigrateUpAgainstRealDatabase
```

**未出现** `1213` / `1205` / `duplicate key` / `lock wait` / `too many connections` / `invalid JSON` 等异常日志。

## 8.3 测试后环境复原

集成测试会向目标库写入测试数据（实测新增 runs +164、run_events +135、outbox +112、conversations +13、messages +13、leases +103 等）。为了让交付的 `xiaoan` 保持为**纯迁移态**，测试后用数据迁移工具的 restore 模式把目标库整体还原为源库的精确副本（还原前会先**报告**将丢弃多少行 target-only 数据，实测丢弃的行数与上述增量完全吻合），并**重新跑了一次完整内容校验 → 再次 VERIFY PASSED**。

---

# 9. 与执行指南的偏差说明

| # | 指南原定 | 实际执行 | 原因 |
|---|---|---|---|
| 1 | §17 数据库名沿用 `xiaoan3_go` | 使用 **`xiaoan`** | 目标库已由数据库侧建好，用户明确指明库名。代码默认值、`.env.example`、README、CI 同步改为 `xiaoan`，避免仓库里出现一个在新基线下并不存在的库名 |
| 2 | §17.1 库级 `utf8mb4_bin` | 从 `utf8mb4_unicode_ci` **ALTER 为 `utf8mb4_bin`** | 实测目标库建库时默认排序规则是 `utf8mb4_unicode_ci`，与全部 migration 不一致。库当时为空，改动无风险。若不改，任何未显式带 `COLLATE` 的建表都会与迁移集语义分叉 |
| 3 | §18 双账号（`potal_migrate` / `potal_app`） | 未创建，沿用 `test_user` | 创建账号需要全局权限，现有账号只有 `xiaoan.*` 的权限。**生产部署前应补齐**（见 §10） |
| 4 | §29 只跑 execution/delivery + tests/integration | 额外把 `./internal/platform/...` 纳入 CI | 否则 P0-3 的 DB 时钟断言在任何 job 里都不会执行（详见 §4.2） |
| 5 | §39 Commit 3 = Documentation Cleanup | 文档清理并入 Commit 1 | 代码注释与 README 属于同一次“运行基线切换”，拆开会留下一个「代码已切、文档还写 TiDB」的中间 commit |

---

# 10. 遗留项与建议（上线前处理）

## P1-1 `sql_mode` 缺少 `ONLY_FULL_GROUP_BY`

目标实例当前：

```text
STRICT_TRANS_TABLES,NO_ZERO_IN_DATE,NO_ZERO_DATE,ERROR_FOR_DIVISION_BY_ZERO,NO_AUTO_CREATE_USER,NO_ENGINE_SUBSTITUTION
```

严格模式（`STRICT_TRANS_TABLES`）**已在**，数据完整性不受影响；缺的是 `ONLY_FULL_GROUP_BY`。指南 §19 建议完整沿用推荐集合。`test_user` 无 `SUPER` 权限，改不了 global 值，**需要 DBA 在实例级配置**。

现状风险评估：项目 SQL 由 sqlc 生成、列显式、无含糊 `GROUP BY`，因此**不构成本次切换的阻塞项**，但建议补齐以免掩盖后续新增 SQL 的问题。

## P1-2 `max_connections` 容量核算（指南 §20）

目标实例 `max_connections = 1000`，应用默认每个进程 `DB_MAX_OPEN_CONNS = 40`：

```text
2 API + 2 Stream + 5 Worker + 1 Scheduler = 10 进程 × 40 = 400 连接
```

400 < 1000，余量充足，**无需调整连接池，也无需扩大 `max_connections`**。若后续 worker 副本数大幅增加，按 `进程数 × 40` 重新核算。

## P1-3 Redis Streams 携带跨环境遗留工作（**上线前必须处理**，§14.6 有新证据）

`REDIS_KEY_PREFIX` 不变时，新部署会继承旧环境的队列。实测：启动 `feishu_delivery` worker 后
立即消费了 98 条切换前遗留的投递消息（全部指向合成目标，已正确失败，未打扰真人）。

上线前必须三选一：① 清空 `queue:*` 及其 consumer group；② 更换 `REDIS_KEY_PREFIX`；
③ 使用独立 Redis DB。**不能默认「Redis 原样沿用即可」。**

## P2 共享 dev 库带入的测试残留

从 TiDB 搬过来的数据里包含集成测试产生的垃圾行（**已与源库核对一致，属于“忠实迁移”而非迁移失误**）：

```text
runs.provider = itest_*          4061 行
provider_admission_locks         139 行（itest_slots_*）
messages.conversation_id = 0     26 行
```

建议在正式开放流量前按 `itest_%` 前缀清理。这属于数据治理，不在切换范围内，所以本次**原样保留**（未擅自删除生产/共享库数据）。

## P2 账号权限收敛

生产部署时应按指南 §18 拆分为迁移账号（DDL）与运行账号（仅 DML），不要让 `studio-api/stream/worker/scheduler` 长期持有 `DROP/ALTER/CREATE`。

---

# 11. 验收清单（指南 §40）

| # | 验收项 | 状态 | 证据 |
|---|---|---|---|
| 1 | 默认 `DB_PORT` 已为 3306 | ✅ | `config.go` |
| 2 | `.env.example` 已改 MySQL 5.7 | ✅ | `.env.example` |
| 3 | TiProxy 不再使用 | ✅ | 运行时代码/配置/CI 零命中 |
| 4 | `tidbCompatibleDSN` 已删除 | ✅ | `internal/app/migrate.go` |
| 5 | `tidb_skip_isolation_level_check` 已删除 | ✅ | 全仓零命中 |
| 6 | sqlc 仍为 mysql engine | ✅ | `sqlc.yaml` 未改 |
| 7 | migration 0001~0013 可在全新 MySQL 5.7 执行 | ✅ | `version=13, dirty=0` |
| 8 | `schema_migrations` `dirty=0` | ✅ | `13 / 0` |
| 9 | MySQL 5.7 + Redis 完整 integration test 全绿 | ✅ | 68 PASS / 0 FAIL / 0 SKIP |
| 10 | CAS single winner 通过 | ✅ | `TestCASRaceSingleWinner` |
| 11 | Provider max_inflight 通过 | ✅ | `TestProviderSlotsEnforceGlobalMaxAcrossWorkers` |
| 12 | Lease / Reaper / Fencing 通过 | ✅ | Lease / Reaper / Fencing 系列 |
| 13 | Schedule / Delivery 通过 | ✅ | `TestDualScheduler*` / `TestDeliveryFanoutIdempotent` |
| 14 | `session time_zone = +00:00` | ✅ | `TestSessionTimezoneUTC` |
| 15 | `utf8mb4_bin` | ✅ | 28 张表 + 库级 |
| 16 | `max_connections` 完成容量核算 | ✅ | §10 P1-2 |
| 17 | README 当前架构已改为 MySQL | ✅ | 根 README + backend-go/README |
| 18 | 运行时代码不存在 TiDB/TiProxy 专属逻辑 | ✅ | 扫描零命中（README 中保留“TiDB 已退出运行架构”的说明性文字） |
| 19 | 正式 Smoke Test 全部通过 | ✅ 除 2 项 | 已执行，见 §14；未覆盖：飞书浏览器登录、运行中杀 worker（后者由集成测试覆盖） |

另有 3 项超出指南但已完成的验证：**逐表字节级校验和比对**、**JSON 文档语义比对**、**26 条引用完整性检查（含源库基线的对照）**。

---

# 12. Rollback

目标库目前处于**纯迁移态**（已通过内容校验证明与源库一致），且尚未开放业务写入，因此仍在指南 §35 的 **阶段 A**——可快速回滚：

```env
DB_HOST=192.168.212.38
DB_PORT=6000
DB_NAME=xiaoan3_go
```

配合切回切换前的 commit（`d659489`）即可，TiDB 侧数据未被本次操作修改过（全程只读）。

> 一旦 MySQL 开始接收正式业务写入，`xiaoan3_go` 与 `xiaoan` 即开始分叉，此时不得直接回切，必须按指南 §35 阶段 B 处理。

---

# 13. 本次执行使用的临时工具（已删除）

数据迁移与校验是用一次性 Go 工具完成的（`cmd/dbprobe` / `cmd/dbcli` / `cmd/dbcopy` / `cmd/dbverify`），**已在提交前删除**，原因：它们内含连接凭据，不应进入仓库；且不属于指南 §38 声明的改造范围。核心能力与结论都已沉淀到本报告（§6、§7 列出了完整的校验方法和 SQL），需要时可按此重建。

关键步骤复现：

```bash
# 1. 目标库 schema（权威来源）
cd backend-go && go run ./cmd/migrate

# 2. 业务数据搬运
#    源端单事务快照读 + 逐表列集合比对 + 批量 INSERT（原始字节）+ 逐表行数对账
#    跳过 schema_migrations

# 3. 内容校验
#    SELECT COUNT(*), SUM(CRC32(CONCAT_WS(0x1f, <非 JSON 列>))), BIT_XOR(CRC32(...)) FROM <t>
#    JSON 列：取回后在客户端解析并按有序键重新编码后比对
```

---

# 14. Smoke Test（指南 §34）执行结果

**结论：四个进程在 MySQL 5.7 上跑通了完整业务链路，包括一次真实的 Aily 执行和一次真实的飞书投递。**

## 14.1 执行前的关键发现：迁移把「可执行 backlog」一起搬过来了

第一次准备启动 worker/scheduler 时先做了只读勘察，结果**不能直接起**——库里带着 dev 环境的待执行工作：

| 对象 | 数量 | 一起进程会立刻发生什么 |
|---|---|---|
| `runs` queued（provider 可见范围） | 404（`feishu_aily`） | worker 是**按 provider 分片**的（`ClaimCandidates(ctx, provider)`），会真的调 Aily 执行 |
| `runs` running | 34（`feishu_aily`） | 过期 lease 被 reaper 恢复/重排 |
| `schedules` enabled 且已过期 | 287 | scheduler 瞬间触发，创建 occurrence → run |
| `schedule_occurrences` pending | 3 | scheduler 直接 admit 成 run |
| `delivery_executions` pending | 98 | 投递 worker 尝试真实发消息 |

飞书身份只有一条，且是**用户本人**（`user_id=30001`，refresh token 有效到 2026-09-20）——那 404 个 run 会用本人账号与额度执行。

> 这就是指南 §24「停写窗口」的实际形态：不是代码问题，而是**迁移必须连同 backlog 一起处理**。

## 14.2 排空（用户确认后执行，每步记录影响行数）

| 步骤 | 操作 | 行数 |
|---|---|---|
| 1 | 为待取消的 run 补写 `run.cancelled` 终态事件（保证不变量 D：终态 run 必有终态事件） | +438 |
| 2 | `runs`（queued+running, provider=feishu_aily）→ `cancelled` | 438 |
| 3 | 删除这些 run 的 lease（不变量 C：终态不得持有 lease） | 0（这些 run 本就没有 lease） |
| 4 | `schedule_occurrences` pending → `skipped` | 3 |
| 5 | `schedules` enabled 且已过期 → `enabled=0` | 287 |
| 6 | 收尾：把被取消的 **scheduled** run 的 occurrence 收敛为 `failed`（不变量 H） | 274 |

**故意没动**：`outbox_events`（relay 的真实准入条件是 `status='pending'`，只有 2 条，属正常待投递）、`itest_*` 的 run（对 `--provider=feishu_aily` 的 worker 不可见）、以及源库自带的历史不变量违规。

### 不变量检查器（`cmd/invariant-checker`）前后对比

用**同一份代码**对源 TiDB 和目标 MySQL 各跑一遍，这是本轮最有力的证据之一：

| 不变量 | 源 TiDB | 目标（排空前） | 目标（排空后） | 说明 |
|---|---|---|---|---|
| running_without_lease | 34 | 34 | **0** | 排空改善 |
| terminal_without_terminal_event | 241 | 241 | 241 | 保持不变（补写的事件起作用了） |
| outbox_backlog_age | 15556s | 15556s | **0** | relay 投递后消失 |
| scheduled_run_occurrence_mismatch | 130 | 130 | 173 | 见下 |

- **源库与目标库初始违规数完全相同（406 = 406）** → 这些违规是数据自带的，不是迁移引入；同时说明这 11 条不变量查询在 TiDB 与 MySQL 5.7 上结果一致，本身就是一次迁移正确性交叉验证。
- H 的 +43 **不是新制造的不一致**：源库就有 64 个「`trigger_type='scheduled'` 但没有 occurrence 行」的 run，其中 43 个非终态（H 不统计）、21 个终态（H 统计）。排空把 43 个变成终态后 H 才看见它们。**不一致总数两库相同（64 = 64）。**

## 14.3 启动（指南 §33 顺序：api → stream → worker → scheduler）

启动过程**完全安静**，这正是排空的目的：

```text
studio-api     :8080   /health/live 200   /health/ready "ok"   (真实 MySQL 5.7 + Redis 连通)
studio-stream  :8081   监听正常
studio-worker  --provider=feishu_aily      "worker consuming"
               第一轮扫描即 "reaper recovered runs count=40"   (恰好是那 40 条过期 lease)
               之后无任何 claim、无异常
studio-worker  --provider=feishu_delivery  "delivery worker consuming"
studio-scheduler                            "scheduler started"  无任何 schedule 触发
```

另有一处值得记录：**delivery 由独立的 `feishu_delivery` 队列消费者处理**（`--provider=feishu_aily | feishu_delivery`）。只起 aily worker 时投递会一直停在 `pending`。

## 14.4 定向 run：一次真实 Aily 执行

```text
POST /api/v2/runs {application_id:1, content:"请只回复两个字：收到"}  -> 201, status=queued
queued 14:27:46.283 -> started 14:27:46.581 -> finished 14:28:05.889      (19.3s)
output : {"text":"收到","status":"Completed"}          <- 真实 Aily 执行成功
events : 1 run.started -> 2 content.chunk -> 3 run.completed
SSE    : 实收 3 帧 (run.started / content.chunk / run.completed)
finalize 后 leases=0 slots=0，全 provider 开放 slot=0            <- 无泄漏
outbox  : run.dispatch published, retry_count=0
```

覆盖指南清单：创建 Conversation ✔ 发送 Message ✔ 创建 Run ✔ **Worker Claim** ✔ **Provider Slot 占用** ✔ **Aily 执行成功** ✔ **Run Finalize** ✔ **SSE 收到事件** ✔ Conversation History ✔

## 14.5 定向 Schedule：创建 → 触发 → 投递

```text
POST /api/v2/schedules  (once, run_at = now+40s, delivery -> 本人 open_id)   -> 201
t+41.0s  occurrence running   run running
t+61.5s  occurrence succeeded run succeeded   delivery pending
         run output {"text":"收到","status":"Completed"}    trigger_type=scheduled  trigger_id=<occurrence>
         delivery_executions: status=succeeded, attempt=1   <- 真的发出去了
```

覆盖指南清单：**Schedule 创建** ✔ **Schedule 触发** ✔ **Delivery 正常** ✔
同时观察到投递失败路径：98 条历史投递各重试 5 次（指数退避）后置为永久失败——**Retry 机制在真实环境上得到验证** ✔

## 14.6 新发现：Redis Streams 携带跨环境遗留工作（P1，上线前必须处理）

指南 §2 把 Redis 定位为「继续独立承担」，隐含假设是 Redis 可以原样沿用。实际不是：

- 启动 `feishu_delivery` worker 后，它**立即消费了 98 条来自切换前环境的投递消息**（旧环境已发布、但消费方在切换前就停了，消息留在 stream 里）。
- 这 98 条全部指向**合成目标**（`ou_test` / `oc_test` / `clock_retry` / `oc_original` / `oc_replacement`），失败原因明确：
  `delivery: owner uat: user has no feishu identity; re-login through Feishu OAuth`。
- **指向真实身份的失败 = 0**；唯一成功的那条就是本次冒烟自己发的。
- 另 23 条 `lease_expired`（"worker crashed"）是更早的历史遗留。

这意味着：**`REDIS_KEY_PREFIX` 不变时，新部署会继承旧环境的队列**。对 Aily 队列而言，被取消的 run 让 CAS claim 失败、无害（本次正是靠取消 run 兜住了）；但投递队列没有这层保护，会真的尝试发送。

**上线前建议三选一**：① 清空 `queue:*` 与对应 consumer group；② 更换 `REDIS_KEY_PREFIX`；③ 使用独立 Redis DB。绝不能默认「Redis 不用管」。

## 14.7 日志与终态

5 个进程日志在指南 §34 监控清单上**全部零命中**：

```text
1213 / 1205 / duplicate key / lock wait / connection refused
too many connections / invalid JSON / incorrect datetime / panic      全部 0
```

> 注意：直接 `grep 1205` 会假阳性命中 backoff 毫秒值（如 `2.1205 38976`），需按结构化字段判断。

终态快照（全部无泄漏）：

| 指标 | 值 |
|---|---|
| runs | 4977（迁移 4975 + 冒烟 2） |
| runs succeeded / cancelled / running | 764 / 438 / **0** |
| run_leases / provider_execution_slots | **0 / 0** |
| outbox pending | **0**（排空前 2，relay 已投递） |
| delivery_executions pending / succeeded | **0** / 1（本次那条） |
| schedules enabled / 其中已过期 | 265 / **0** |
| schedule_occurrences pending | **0** |

## 14.8 冒烟测试对库造成的净变更（可审计）

新增 2 条 run（均 succeeded）、1 个一次性 schedule（已自动关闭）、1 条成功投递；排空相关：438 run 取消、274 occurrence 收敛、3 occurrence 跳过、287 schedule 关闭、98 条遗留投递耗尽重试后置失败、2 条 outbox 投递。

**未执行的清单项**（需要交互式浏览器或另行安排）：

- `飞书登录`：真实 OAuth 重定向需要浏览器操作，本次会话是用 `cmd/testsession` 直接签发的（已验证会话本身 + 其后的全部授权路径）。
- `Worker 中途被杀后 Lease/Reaper 恢复`：本次只验证了**启动时** reaper 恢复 40 条过期 lease；「运行中杀 worker」的场景由 `TestLeaseExpiryAndReaper` / `TestReaperRecoveryAtomic*` 在真实 MySQL 5.7 上覆盖。

