# potal 第八轮复审整改变更报告

**范围**：merged heartbeat 改为 BOTH OR NEITHER（P1）· Provider Slot 确认丢失后 Worker 立即 self-fence（P1）· 心跳决策区分「确认安全丢失」与「瞬时基础设施故障」· 新增确定性反证测试矩阵 · `heartbeat_at` 语义注释澄清（P3，文档）
**复审基线**：dev `e002c7c`（功能树 = `a7889db`，HEAD 仅多一条 memory 记录）
**本机验证**：gofmt / go vet / go build / go test 全绿；集成测试（MySQL 5.7 + Redis 7，`STUDIO_TEST_DB=1 STUDIO_TEST_REDIS=1`）57.2s 全绿；两次反证均确认新测试会在旧行为下失败。`-race` 本机无 gcc，由 CI 兜。

**未改动**（第八轮报告 §十九 明确要求）：迁移 0018 / 0019 / 0020、Gate1 / Gate2 / PreSubmit / BeginProviderAttempt、Retry / Defer、Schedule admission、SSE terminal replay、前端终态解析。migration version 仍为 20，本轮没有 0021。

---

## 一、P1 — merged heartbeat 允许「Run lease 存活、Provider Slot 消失」

### 问题（第八轮报告 §四~§九）

`ProviderSlots` 的契约是 `Run Ownership alive ⇔ Provider Slot alive`，而 active slot 数量被定义为 **provider 真实并发的严格安全上界**（`max_inflight`）。但旧实现在**错误分支上**主动放弃了这个不变式：

```go
sres, serr := q.TouchProviderSlot(...)
slotOK := false
switch {
case serr != nil:
        s.Log.Warn(...)            // 只记日志
default:
        if sn, nerr := sres.RowsAffected(); nerr == nil && sn == 1 { slotOK = true }
}
if err := tx.Commit(); err != nil { ... }
return true, slotOK, nil           // ← Run lease 单边提交
```

两条独立路径都会产出同一个非法状态：

| 路径 | 结果 |
| --- | --- |
| `TouchProviderSlot` 返回 SQL error（锁等待超时 / 连接瞬时错误） | `leaseOK=true, slotOK=false, err=nil`，lease 续约已提交 |
| `TouchProviderSlot` 成功往返但 `RowsAffected=0` | 同上，且 `RowsAffected()` 自身出错时也落到同一分支 |

于是数据库看到：

```text
run lease     = alive      → Worker 继续执行、没有任何东西 cancel 它
provider 调用 = alive
slot          = gone       → capacity accounting 已经释放了这一份
```

下一次 admission 数到 `depth = max-1 < max`，于是 admit 新 Run B，真实并发变成 `max+1`：

```text
真实执行 = A + 其余9 + B = 11
数据库   = active slots 10 = max_inflight
```

**`max_inflight` 不再是上界**。这不是 metrics 问题，而是 Provider Capacity safety contract 被破坏。

第七轮的同毫秒 `RowsAffected` 修复并没有解决这一条——它解决的是「slot 明明存在却假 0」。修完之后 `0` 才重新成为**强语义**：`0 ⇔ slot 真的不在了`。本轮正是站在这个语义之上把 `0` 变成可 action 的事实。

### 修复

`backend-go/internal/execution/service.go`

进入 provider execution 阶段之后（`slot != nil`），merged heartbeat 一律 **BOTH OR NEITHER**，整个函数只走四种出口：

| 出口 | 语义 | 落库 |
| --- | --- | --- |
| `nil, leaseOK=true, slotOK=true` | 两边都续成功 | 一个事务一次 COMMIT |
| `nil, false, false` | lease fence 拒绝（`n != 1`）→ 所有权丢失 | 全部 ROLLBACK |
| `ErrProviderSlotLost` | 往返成功但 `0 changed rows` → **已证明** slot 不在 | lease 续约一并 ROLLBACK |
| `err != nil`（基础设施） | DB **未能确认** slot，不等于丢了 | lease 续约一并 ROLLBACK |

```go
if err != nil {
        // transient：DB 无法确认，不是证明它没了 → 两边一起回滚
        s.Log.Warn("provider slot renew failed in merged heartbeat — rolling the lease renewal back", ...)
        return false, false, err
}
sn, err := sres.RowsAffected()
if err != nil { return false, false, err }
if sn != 1 {
        // 往返成功 + 0 changed rows = confirmed capacity loss
        return false, false, ErrProviderSlotLost
}
if err := tx.Commit(); err != nil { return false, false, err }
return true, true, nil
```

三处刻意的边界：

1. **slot 丢失绝不 recreate**（报告 §十二沿用）。slot 消失时系统不知道是否已有新 admission 用掉了这份被释放的 capacity；recreate 要么超限，要么必须重新参与完整 provider admission serialization，把 heartbeat 路径拖回 `provider lock → run lock → lease lock` 锁序体系。`Renew = XX-only` 保持不变。
2. **瞬时错误不立即 cancel**（报告 §十四）。锁等待超时只说明 DB 暂时无法确认，此时上一次已确认的 TTL 仍然有效；立刻 cancel 会把一次网络抖动升级为所有权丢失。整个 merged heartbeat 回滚后由下一 tick retry。
3. **`slot.Provider == ""` 从「commit + slotOK=false」改为整个回滚 + error**。原实现会在一个不可用的描述符上单边提交 lease 续约；现在同样是 BOTH OR NEITHER。

---

## 二、P1 — Worker 收到「确认的 slot 丢失」后必须立即 self-fence

### 问题（报告 §六、§十三、§十五）

旧心跳循环里 `slotOK=false` 只是记录 telemetry 然后 `continue` 执行：

```go
w.recordHeartbeatSuccess(...)
if !slotOK {
        w.recordProviderAdmission("provider_slot_lost")
        w.recordAdmission(telemetry.AdmissionProviderSlotLost)
}
```

它**不会 cancel Run**。于是 Run lease 续长、Provider 调用继续，只有 capacity 记账消失——正是第一节描述的那个非法状态。

### 修复

`backend-go/internal/execution/worker.go`

决策函数扩展 slot 结果，并把「已证明丢失」与「无法确认」分开：

```go
func shouldCancelAfterHeartbeat(leaseOK bool, slotOK bool, err error, leaseDeadlineElapsed bool) bool {
        if errors.Is(err, ErrProviderSlotLost) {
                return true                     // DB 已证明 reservation 丢失 → 立即停
        }
        if err != nil {
                return leaseDeadlineElapsed     // 无法确认 → 只在越过已确认 TTL 后 fail-closed
        }
        return !leaseOK || !slotOK              // 成功往返报缺任一半 → 确认丢失
}
```

心跳循环相应地把 slot-lost telemetry 移入 cancel 分支，并给日志区分三种 reason：

```text
provider capacity reservation lost              ← ErrProviderSlotLost / 成功往返报 slot 缺失
heartbeat unavailable past local lease deadline ← 普通基础设施错误且本地 deadline 已过
heartbeat fence rejected ownership              ← leaseOK=false
```

### 自我修正：标签不能张冠李戴

上面这个 reason 映射引出本轮唯一一处**自己写出来的缺陷**，已在提交前修掉：`HeartbeatOwnedWithSlot` 在 **lease fence 被拒** 时也返回 `slotOK=false`（那次 slot 根本没被碰过），所以用「`err == nil && !slotOK`」判断 capacity 丢失会把**所有权丢失误报成容量丢失**，把运维指向错误的子系统。

因此判定谓词带上 lease 维度，并把 reason 映射抽成可测函数：

```go
func confirmedProviderSlotLoss(leaseOK, slotOK bool, err error) bool {
        if errors.Is(err, ErrProviderSlotLost) { return true }
        return err == nil && leaseOK && !slotOK   // ← leaseOK 守卫
}

func heartbeatCancelReason(leaseOK, slotOK bool, err error) string {
        switch {
        case confirmedProviderSlotLoss(leaseOK, slotOK, err):
                return "provider capacity reservation lost"
        case err != nil:
                return "heartbeat unavailable past local lease deadline"
        default:
                return "heartbeat fence rejected ownership"
        }
}
```

（`shouldCancelAfterHeartbeat` 的 `!leaseOK || !slotOK` 不受影响——两种确认丢失都必须立刻 cancel，只是**文案与 telemetry** 必须分家。）

**确认丢失后不立即 requeue**（报告 §十六）。cancel 分支只做 local cancel + 移除 inflight 记录 + 幂等 Release（slot 行已不在时是 no-op），**不续长也不释放 Run lease**——Provider 请求可能已经真正提交出去，立即 requeue 会让新 Worker 重复提交；保留现有 lease 到原 expiry，由既有 reaper / reconciliation 收敛。

`shouldCancelAfterHeartbeat` 的 slot 参数使 `!slotOK && err == nil` 必然进 cancel 分支，因此旧代码末尾那段「只记 telemetry 的 `if !slotOK`」成了不可达分支，已删除。

---

## 三、依赖与顺序（为什么三件事必须一起合入）

单看任一件都不成立，这是一个闭包：

```text
第七轮 same-ms RowsAffected 修复
        ↓ 让 0 具备「真不存在」的强语义
service.go BOTH OR NEITHER
        ↓ 让「slot 丢了」不再以 lease 单边续长为代价暴露
worker.go shouldCancelAfterHeartbeat
        ↓ 让这个已被证明的事实真的停止本 Worker 执行
测试矩阵（Case A/B/C）
        ↓ 把三者同时钉住，且任一条回归都会红
```

---

## 四、测试

### 升级 / 新增（本机与 CI 均跑真实 MySQL 5.7）

| 测试 | 位置 | 钉住的语义 |
| --- | --- | --- |
| `TestMergedHeartbeatSlotLossRollsBackLeaseRenewal` | `tests/integration/review8_fixes_test.go` | slot 被删除 → `ErrProviderSlotLost` + lease 时间戳**逐字节未变**；lease 仍存活到原 expiry；未 recreate slot |
| `TestMergedHeartbeatTransientSlotErrorRollsBackRunLease` | 同上 | 真实 InnoDB 行锁 + `innodb_lock_wait_timeout=1` → `TouchProviderSlot` 1205 → 返回错误且 lease 未动；并断言这**不是** `ErrProviderSlotLost` |
| `TestMergedHeartbeatCommitsBothRenewals` | 同上 | 反向保护：happy path 仍一次提交两边，lease 与 slot 都真的被刷新（防止「靠不续约来通过」） |
| `TestHeartbeatDecisionCancelsConfirmedProviderSlotLoss` | `internal/execution/worker_test.go` | `ErrProviderSlotLost` 在 deadline 未到时也 cancel；同形状的普通错误仍遵守已确认 TTL |
| `TestConfirmedProviderSlotLossNeedsProof` | 同上 | 只有成功往返才能证明丢失；基础设施错误不构成 capacity loss；**被拒的 lease fence 不算 capacity loss** |
| `TestHeartbeatCancelReasonNamesTheActualLoss` | 同上 | 三种 cancel 文案各归其位（尤其所有权丢失不得写成容量丢失） |
| `TestMergedHeartbeatRenewsLeaseAndSlot` | `tests/integration/provider_slots_test.go` | 旧断言（`leaseOK=true`）已被新契约取代：`ErrProviderSlotLost` + `false/false` |
| `TestLiveRenewalReportsOneChangedRow` | `tests/integration/review7_fixes_test.go` | **保留不动**（报告 §十八）：`0` 的强语义是本轮修复的前提，不能被回退 |

### 复用而非复制

`newFrozenClockService`（第七轮，`SET timestamp` 会话钉死）与 `newSessionTunedService`（本轮，`SET SESSION innodb_lock_wait_timeout`）需要同一套「单连接 + 会话变量」管道，因此把它抽成 `newSessionTunedService(t, sessionStmt, args...)`，第七轮的 helper 现在只是它的一行调用。会话变量是连接级的，所以两者都必须 `MaxOpenConns(1)`。

`leaseStamp`（`CAST(heartbeat_at/expires_at AS CHAR)` 逐字符串比较）刻意同时断言两列：`heartbeat_at` 被单调写入，即使一次续约恰好落在同一毫秒也会至少 +1ms，因此「未变」是一个非常强的断言，而不是可能被同毫秒巧合掩盖的弱断言。

### 反证（必须做，已做）

| 反证 | 临时还原的旧行为 | 结果 |
| --- | --- | --- |
| 1 | `service.go` 恢复到「slot 失败也 commit lease，返回 `true,false,nil`」 | `TestMergedHeartbeatRenewsLeaseAndSlot` / `...SlotLossRollsBackLeaseRenewal` / `...TransientSlotErrorRollsBackRunLease` **全部 FAIL** |
| 2 | `shouldCancelAfterHeartbeat` 恢复为忽略 slot 的旧实现 | `TestHeartbeatDecisionCancelsConfirmedProviderSlotLoss` **FAIL** |

两次还原后用备份文件逐字节恢复（md5 复核一致，`FALSIFY` 标记计数为 0），随后全量套件重新全绿。

### fixture 卫生

三个新测试均注册 `deleteRunFixture`（不把 Run 标成终态——那会让第六轮那个 DATABASE-WIDE 迁移 sweep 误红）。实测：清理基线 `runs=0 leases=0 slots=0 outbox=0` → 跑完整套件后 `runs=1 leases=1 slots=0 outbox=0`，唯一残留来自本轮未改动的既有 `TestMergedHeartbeatRenewsLeaseAndSlot`（沿用该文件其他用例的 `claimForSlots` 习惯），新用例残留为 0。

---

## 五、P3 — `heartbeat_at` 注释澄清（纯文档）

报告 §二十 指出旧注释里「`heartbeat_at` 最多比 DB 时钟超前 1ms」在数学上不绝对成立：在一个被钉死的 DB 毫秒里连续续约，会 `+1ms → +2ms → …` 累积。结论是**不需要改代码**，因为：

```text
heartbeat_at  仅观测，不参与 fencing
expires_at    才是判断字段，且始终 = CURRENT_TIMESTAMP + lease（从不累加）
```

因此只改了注释（`db/queries/execution.sql` 的 `HeartbeatLease` 与 `TouchProviderSlot`），明确写出「可略超前 DB now、不参与 fencing、任何调用方都不得开始依赖它」，并顺带记录本轮 P1 使 `0` 成为可 action 的事实。已跑 `sqlc generate`（v1.30.0）同步生成代码，diff 仅注释 22 行 ×2 文件，无 query 签名或参数变化。

---

## 六、本机验证结果

```text
gofmt -l .                → 无输出
go vet ./...              → 通过
go build ./...            → 通过
go test ./...             → 全绿（含 internal/execution 2.27s）
STUDIO_TEST_DB=1 STUDIO_TEST_REDIS=1 go test ./tests/integration/  → ok 57.2s
```

CI 侧 `-race`（本机无 gcc，只能由 CI 兜）与前端均无改动。

---

## 七、状态与后续

第八轮报告给出的剩余问题清单只有这一条 P1，现已关闭。报告 §二十三 同时明确：到这一节点应**真正停止**沿执行主链继续抠边角（Gate / Lease / ProviderSlot / Retry / Defer / Schedule / legacy migration / SSE terminal correctness），下一轮转向新架构风险：

```text
client_request_id 幂等
SSE Hub / 多连接
Worker dispatcher
Conversation lifecycle
长历史 event 分页
长对话前端性能
```

仍未做、且报告建议继续保持原状的 P3：

- `onTransportEnd` 契约清理 —— 实际不可达，等 SSE Hub / reconnect policy 一起设计；
- `runs.next_event_sequence` 分配器 —— 当前 `COUNT(*)+1` 有 run 行锁保证正确性，属性能 backlog；
- 既有 `TestMergedHeartbeatRenewsLeaseAndSlot` 的 fixture 残留（本轮范围外，与该文件其他用例一致）。
