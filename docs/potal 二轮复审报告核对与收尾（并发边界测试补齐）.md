# potal Admission 二轮复审报告 · 核对与收尾变更报告

## 一、背景与结论

输入：《potal Admission 收口二轮代码复审报告》（复审基线 `dev b2112ad`，核心提交 `6f59e66`，评级 91 / A-，P1×3、P2×4）。

核对基线：`dev 362dff6`（已包含二轮整改 `7559c40`、三轮 `e2805ae`、四轮 `18b031b`）。

**核对结论：该报告的 7 项整改建议（P1-1、P1-2、P1-3、§七、§八、§九、§十）在本轮之前已全部落地，本轮只补齐了§十二中尚未存在的 2 个并发边界测试。**

逐项核对证据：

| 报告条目 | 等级 | 状态 | 代码位置 |
|---|---|---|---|
| P1-1 Scheduler resolver 区分「策略拒绝」与「基础设施故障」 | P1 | 已实现 | `internal/app/app.go:305` `executionDenied()`；`authorizeForOwner()` 身份查询仅 `identity.ErrNotFound` 降级，其余上抛 |
| P1-2 已 queued 的 Run 也要服从 kill switch | P1 | 已实现 | `internal/execution/gate.go`（Gate 1 claim 后 + Gate 2 `PreSubmitGate` 提交前）；app/binding 停用=cancel，provider 停用=defer 30s |
| P1-3 run-now pending 队列上限 | P1 | 已实现 | `scheduler.MaxPendingManual` + `CountPendingOccurrences`，在 schedules 行锁内判定，超限 `ErrPendingCapReached` |
| §七 Schedule CRUD 并发 PATCH lost update | P1/P2 | 已实现 | `schedule/service.go:476`(Update)、`:638`(SetEnabled) 全部在 `GetScheduleRowForUpdate` 事务内「锁内重读→merge→validate→UPDATE」 |
| §八 非终态 Run 谓词统一 | P2 | 已实现 | `db/queries/conversation.sql:117` `status NOT IN ('cancelled','succeeded','failed')`；`CountOutstandingRunsByUser` 同谓词 |
| §九 RateLimiter ctx / nil-Redis 边角 | P2 | 已实现 | 9.2：`ratelimit.go` Redis 出错先判 `ctx.Err()` 直接返回，不进 fallback；9.1：`run_handlers.go:248` 已去掉 `s.Redis == nil` 短路 |
| §十 Schedule Service 使用 DB Clock | P2 | 已实现 | `schedule/service.go:88` `dbNow()` / `dbNowTx()`，`Create/Update/SetEnabled` 全链路 DB 时钟 |
| §十二 建议新增 5 个测试 | — | 4 个已有，1 个被更强的四轮测试覆盖 | 见下 |
| §十二 另建议 2 个并发边界测试 | — | **本轮新增** | `tests/integration/review2_fixes_test.go` |

## 二、§十二 测试核对

报告点名的 5 个测试：

| 建议测试 | 现状 |
|---|---|
| `TestSchedulerAuthInfraErrorRetriesSlot` | 已有 `review2_fixes_test.go:62` |
| `TestQueuedRunHonorsProviderKillSwitch` | 已有，且拆成 Kill / Pause 两个（`:174`、`:237`） |
| `TestConcurrentSchedulePatchDoesNotLoseFields` | 已有 `review2_fixes_test.go:304` |
| `TestRunNowPendingQueueHasBound` | 已有 `review2_fixes_test.go:349` |
| `TestConversationBusyIncludesWaitingStates` | 由四轮测试更强覆盖：`TestNonTerminalStatusBlocksSecondTurn` 遍历 6 个非终态状态（queued/running/waiting_input/waiting_external/cancelling/interrupted）同时断言「二次提交被拒」+「Clear 被拒」；`TestTerminalStatusFreesTheConversation` 为反向对照 |

## 三、本轮实际变更

只改了一个文件：`backend-go/tests/integration/review2_fixes_test.go`（新增 2 个测试，无生产代码改动）。

### 3.1 TestUserMaxOutstandingConcurrentBoundary

验证 `CreateRunAdmitted` 的 per-user outstanding 上限是**并发下的硬边界**，而不只是顺序语义。

- 12 个 goroutine 同时发起，cap=3；
- 每个 racer 用 `CreateConversation: true` 建自己的会话 —— 这样唯一被争用的资源就是上限本身，否则 `ErrConversationBusy` 会掩盖越界，测试会「因为错的原因而通过」；
- 断言：成功数 == 3、返回 `ErrUserOutstandingExceeded` 的数 == 9、无其它错误；
- 最后以数据库为准：`runs WHERE user_id=? AND status NOT IN ('cancelled','succeeded','failed')` 计数 == 3。

### 3.2 TestScheduleMaxConcurrentBoundary

同一不变量在 per-user 定时任务配额上的版本：`MaxSchedules=3`、12 个并发 `Create`，断言成功 3 / `ValidationError` 9 / 未软删行数 3。

两个测试都用 `t.Cleanup` 收尾（run 置 cancelled、schedule 软删），避免在共享开发库里留下会被 due scan 捡起的 fixture。

## 四、验证

```
gofmt   clean（review2_fixes_test.go）
go vet  ./...                                  无输出
go test ./...  STUDIO_TEST_DB=1  -count=1      14 个包全部 ok
                                               tests/integration 32.2s
```

新增两个测试单独跑：

```
--- PASS: TestUserMaxOutstandingConcurrentBoundary (0.15s)
--- PASS: TestScheduleMaxConcurrentBoundary       (0.10s)
```

## 五、说明

- 本机 `gofmt -l` 会报告 `internal/execution/recovery.go` 等 5 个文件，经 `gofmt -d` 确认为 Windows CRLF 行尾导致的整文件重写，**不是真实格式问题**；CI（Linux、LF 检出）不受影响，未做改动。
- 本机无 gcc，`-race` 无法运行；CI 的 `check` job 在 Ubuntu 上跑 race。
