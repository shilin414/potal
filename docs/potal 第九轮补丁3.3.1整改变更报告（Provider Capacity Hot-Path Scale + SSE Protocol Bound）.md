# potal 第九轮补丁 3.3.1 整改变更报告

## Provider Capacity Hot-Path Scale Closure + SSE Protocol Negotiation Bound

复审依据：《potal 第九轮补丁3.3最新代码复审暨3.3.1修改执行报告》（§四–§四十三）。

本补丁的两个编号：

| 编号 | 等级 | 问题 | 本报告章节 |
|---|---|---|---|
| 3.3.1-A | P1-HIGH | 有效容量的 remote leg 仍由历史 `provider_submissions` 驱动 | §2 |
| 3.3.1-B | P1 | `stream_protocol` 接受任意正整数并直接成为 Prometheus label | §3 |

**结论：两个问题均已关闭。未修改任何已冻结的 Execution Correctness 语义；未新增
migration（migration 版本保持 24）。**

---

## 1. 变更文件范围

```text
backend-go/db/queries/execution.sql                                  三条容量查询
backend-go/internal/gen/db/*                                         sqlc generate
backend-go/internal/transport/sse/sse.go                             StreamProtocol 协商
backend-go/internal/transport/sse/sse_protocol_test.go               协议协商 + 基数测试
backend-go/internal/platform/telemetry/metrics_test.go               protocol label 形状
backend-go/tests/integration/review9_patch331_test.go                新增（规模 + EXPLAIN + 状态清单）
backend-go/api/openapi.yaml                                          stream_protocol 描述
backend-go/internal/gen/api/*                                        oapi-codegen
backend-go/scripts/falsify_review9_patch331.sh                        新增（3 个 mutation）
backend-go/README.md                                                 3.3.1 规模与协议合同
docs/potal 第九轮补丁3.3.1整改变更报告（…）.md                        本报告
```

Frontend：**未修改**。当前前端已正确发送 `stream_protocol=2`。

---

## 2. 3.3.1-A：容量准入查询由 active run 驱动

### 2.1 问题

3.3 已经把「有效容量」的语义做对了（`DISTINCT(live slots UNION non-settled
sending/unknown/accepted submissions)`、排除 `rejected`、exclude-self），本轮
**不动语义**，只改 SQL 的遍历方向。

migration 0024 加上的 `(provider, state, run_id)` 索引消掉了全表扫描，但没有让
**候选区间**有界：

```sql
SELECT ps.run_id
FROM provider_submissions ps
JOIN runs r ON r.id = ps.run_id
WHERE ps.provider = ?
  AND ps.state IN ('sending', 'unknown', 'accepted')
  AND r.status NOT IN ('cancelled','succeeded','failed','interrupted')
```

`provider_submissions` 是历史表——run settled 之后 submission 不会被删除，所以
`provider = P AND state = 'accepted'` 覆盖了 **P 建表以来的全部执行**。而这是
`ProviderSlots.Acquire()` 里的准入查询，**每一次 claim 都执行**（不是报表、不是
低频 metrics）。`Depth()` / `UncontrolledDepth()` 还会周期性查同类数据。

风险不是「某天慢一点」，而是把 DB query scale 放大成 Provider admission
串行化：候选范围随历史单调增长 → claim 延迟上升 → `provider_admission_locks`
持锁时间变长 → 同 Provider 的并发 Acquire 等锁加重。

### 2.2 修改

遍历方向反转，并**固定** join 方向：

```sql
SELECT COUNT(*) AS n
FROM (
    SELECT s.run_id
    FROM provider_execution_slots s
    WHERE s.provider = ?
      AND s.expires_at > CURRENT_TIMESTAMP(3)

    UNION

    SELECT r.id AS run_id
    FROM runs r
    STRAIGHT_JOIN provider_submissions ps
      ON ps.run_id = r.id
     AND ps.provider = r.provider
    WHERE r.provider = ?
      AND r.status IN (
          'queued', 'running', 'waiting_input', 'waiting_external', 'cancelling'
      )
      AND ps.state IN ('sending', 'unknown', 'accepted')
) capacity_runs
```

三条查询（`CountProviderEffectiveInflight` /
`CountProviderEffectiveInflightExcludingRun` / `CountProviderUncontrolledInflight`）
全部改成同一形状。exclude-self 在**两条腿上都保留**（`s.run_id <> ?` 与
`ps.run_id <> ?`），3.3 修掉的 self-deadlock 未被恢复。

### 2.3 三个刻意的决定

**① `STRAIGHT_JOIN` 而不是只把 `FROM` 顺序写对。**
优化器可以重排 inner join；若它认为 `('sending','unknown','accepted')` 更选择性，
就会重新从 ledger 驱动。性能不变量必须写进语句，而不是寄希望于优化器的默认选择。
MySQL 5.7 与 TiDB 都支持 `STRAIGHT_JOIN` 强制 `FROM` 顺序。

**② 状态过滤写成显式 `IN (...)` 而不是 `NOT IN`。**
`status` 是 `idx_runs_claim(status, provider, queued_at)` 的前导列，`NOT IN` 用不上
该索引区间，会把计划推回扫描。代价是这个清单不再「自动包含新状态」，因此由
`TestProviderCapacityStatusListIsExactComplementOfSettled` 把它钉死为
`IsSettled` 的**精确补集**（双向比较：漏一个状态 = 过度准入，多一个 settled 状态
= 永久泄漏容量），并同时断言该清单仍是 `IN`、仍带 `STRAIGHT_JOIN`、且不再
`FROM provider_submissions`。

**③ 不 `FORCE INDEX`、不新增 0025。**
复审报告要求先用 EXPLAIN 证明，再决定是否加 hint/索引。实测（§2.4）MySQL 5.7
已稳定选择 `idx_runs_claim` 与 `PRIMARY`，因此 migration 版本保持 **24**，
`idx_provider_submissions_capacity` 一并保留（诊断与 optimizer 备选路径仍可用，
只是不再作为准入的驱动路径）。

### 2.4 EXPLAIN 证据（MySQL 5.7，192.168.211.26:20336）

验收用的数据形状与复审报告 §三十九 一致：同一 provider 下
**5 000 settled runs（accepted + unknown 各半）** + **2 个活跃 run**
（accepted+running 持 slot；unknown+waiting_external 已 park）。

EXPLAIN 直接对**驱动实际发送的 SQL 文本**执行——文本从
`internal/gen/db/execution.sql.go` 的常量里读出来，不是手抄的近似查询。

**修改前（历史驱动）**：

```text
id=1 PRIMARY  <derived2>              ALL      rows=344
id=2 DERIVED  s (slots)               ref      key=uniq_provider_slot_ownership      rows=1
id=3 UNION    ps (provider_submissions) ref    key=uniq_provider_submit_key  key_len=258  rows=2251  filtered=30
id=3 UNION    r  (runs)               eq_ref   key=PRIMARY                   key_len=16   rows=1     filtered=50.67
              <union2,3>              ALL      Extra=Using temporary
```

**修改后（active-run 驱动）**：

```text
id=1 PRIMARY  <derived2>              ALL      rows=4
id=2 DERIVED  s (slots)               ref      key=uniq_provider_slot_ownership      rows=1
id=3 UNION    r  (runs)               range    key=idx_runs_claim  key_len=340  rows=7  Extra=Using where; Using index
id=3 UNION    ps (provider_submissions) ref    key=PRIMARY         key_len=16   rows=1  filtered=15  Extra=Using where
              <union2,3>              ALL      Extra=Using temporary
```

读法（**锁结构，不锁毫秒**，复审报告 §三十八）：

| 观察点 | 修改前 | 修改后 |
|---|---|---|
| 驱动表 | `provider_submissions`（前导列 `provider`） | `runs`（前导列 `status`） |
| ledger 访问方式 | `uniq_provider_submit_key`，**只用 provider 前缀** | `PRIMARY`，按 `run_id` 定位 |
| ledger 估算扫描行 | **2251** | **1 / 外层行** |
| runs 访问方式 | `PRIMARY`（被驱动，eq_ref） | `idx_runs_claim`（range，**Using index** 覆盖索引） |
| runs 估算行 | 1（每个 settled run 一次） | **7** |
| 派生表估算 | 344 | 4 |

关键差别不是「快了 N 倍」，而是**候选范围的驱动来源换了**：修改后的 2251 →
7/1 与历史量无关，历史继续增长时驱动侧不变。

> 注意：EXPLAIN **必须在真实数据形状下跑**。在空 provider 上所有候选索引都估 1 行，
> 优化器按 tie-break 会选 `idx_runs_external`（前导列 `provider`）与
> `uniq_provider_submit_key`（同样前导列只有 `provider`）——那时 EXPLAIN 说明不了
> 生产的计划。测试因此**先造 fixture 再 EXPLAIN**，这一步是承重的。

### 2.5 TiDB 验证状态

**未执行，列为部署验收项。** 本机可达的数据库只有 MySQL 5.7
（192.168.211.26:20336，即为 `.env.local` 配置）；`20336` 之外的 `20337` 端口有
服务在听，但没有可用的已授权凭据（`root` 与 `test_user` 均 Access denied），
环境中没有可直接连接的 TiDB 8.0.0 实例。

方案本身对 TiDB 兼容：`STRAIGHT_JOIN` 是 TiDB 官方支持的 join 顺序强制语法，
显式 `IN` 列表与 `(status, provider)` 前缀也与 TiDB 的索引选择一致。部署环境按
复审报告 §二十一/§三十九 对同一份 SQL 执行 EXPLAIN，确认
`runs → provider_submissions` 顺序即可。

---

## 3. 3.3.1-B：`stream_protocol` 是协商值，不是回显值

### 3.1 问题

修改前：

```go
v, err := strconv.Atoi(strings.TrimSpace(raw))
if err != nil || v < StreamProtocolLegacy {
    return StreamProtocolLegacy
}
return v          // ← 原样回显
```

于是 `3 → 3`、`100 → 100`、`123456 → 123456`，而 `Stream()` 紧接着把这个值
**当成 Prometheus label**：

```go
protocol := StreamProtocol(r)
g.Metrics.SSEStreamProtocolTotal.WithLabelValues(strconv.Itoa(protocol)).Inc()
```

`SSEStreamProtocolTotal` 是 `CounterVec{protocol}`，所以任何有权限打开自己 Run
SSE 的客户端都能用 `?stream_protocol=1001/1002/1003/…` **无限制造 time series**。
同时 `implementation / tests / OpenAPI / 变更报告` 四者对「越界」的表述互相矛盾
（OpenAPI 写 `enum: [1,2]`，runtime 却接受任意正整数）。

### 3.2 修改

```go
func StreamProtocol(r *http.Request) int {
	raw := strings.TrimSpace(r.URL.Query().Get("stream_protocol"))
	if raw == "" {
		return StreamProtocolLegacy
	}
	requested, err := strconv.Atoi(raw)
	if err != nil || requested < StreamProtocolRangeDelta {
		return StreamProtocolLegacy
	}
	return StreamProtocolRangeDelta
}
```

契约收敛为：

```text
缺失 / 空 / 乱码 / 0 / 负数 / Atoi 溢出   → 1 (legacy)
1                                        → 1
2                                        → 2
3 / 4 / 100 / 1001 / 999999 / 2147483647 → 2   （未来版本向下协商，不 400）
```

选择「降到 2 而不是 1」保住了原代码的 forward-compatibility 意图：客户端声明
能力、服务端回答**双方共同能力**，而不是声称自己在用服务端根本没有定义的 v3。

三个输出面因此只剩 1 / 2：

```text
1. StreamProtocol() 的返回值
2. 响应头 X-Studio-Stream-Protocol（表达「本次响应按哪个协议渲染」，不是「客户端想要几」）
3. studio_sse_stream_protocol_total 的 protocol label
```

### 3.3 契约一致性

| 面 | 修改前 | 修改后 |
|---|---|---|
| runtime | 任意正整数 | 恒 1 / 2 |
| unit test | 固定 `3 → 3` 并称 forward compatible | `3 → 2`，新增 4/100/1001/999999/MaxInt32/溢出 |
| OpenAPI | `enum: [1,2]` + 旧 description | `enum: [1,2]` 保留，description 写明**协商**与降级规则 |
| 响应头 schema | `type: integer` | 增加 `enum: [1,2]`（值只会是 1/2） |
| 变更报告 | 「缺失/不可解析/越界 → legacy」 | 与实现一致 |

---

## 4. 测试矩阵

### 4.1 新增

| 测试 | 文件 | 锁什么 |
|---|---|---|
| `TestProviderCapacityStatusListIsExactComplementOfSettled` | `tests/integration/review9_patch331_test.go` | SQL 的 active 状态清单 == `IsSettled` 补集（双向）、仍是 `IN`、仍带 `STRAIGHT_JOIN`、不再 `FROM provider_submissions` |
| `TestProviderCapacityIgnoresLifetimeAcceptedHistory` | 同上 | 8 000 accepted+succeeded + 2 000 unknown+succeeded 历史下，`effective/controlled/uncontrolled = 2/1/1`；且 max=2 拒第 4 个 run、max=3 准入同一个 run 并返回 depth=3 |
| `TestProviderCapacityQueryIsDrivenByActiveRuns` | 同上 | EXPLAIN：`runs` 先于 `provider_submissions`；两表索引前导列分别为 `status` / `run_id` |
| `TestStreamProtocolNegotiatesFutureVersionsDown` | `internal/transport/sse/sse_protocol_test.go` | `3/4/100/1001/999999/2147483647 → 2`，溢出 → 1 |
| `TestSSEStreamProtocolMetricCardinalityIsBounded` | 同上 | 16 个（含恶意）输入走完 `Stream() → negotiate → label → Inc()` 后，registry 里该 family **≤ 2 条 series** |
| `TestSSEStreamProtocolMetricLabelShape` | `internal/platform/telemetry/metrics_test.go` | 该 metric 只有 1 个 label（`protocol`）且取值 ∈ {1,2} |

说明：基数测试放在 `sse` 包而不是 `telemetry` 包，因为不变量的一半（协商）与另一半
（写 label）只有在这里才同时可见；`telemetry` 侧只保留 label 形状这一半。两处交叉
引用已在注释里写明。

**没有**任何墙钟断言（复审报告 §三十八）：规模测试锁 `COUNT`，计划测试锁 join 顺序
与索引前导列，`rows` 只打印不断言。

### 4.2 原有测试保持

3.3 的 8 个 Capacity correctness case 全部继续通过：

```text
slot + sending → 1
unknown no slot → still 1
accepted expired slot → still 1
same unresolved run recovery → allowed
accepted terminal → release
rejected → no capacity
acceptance persistence failure → still capacity
park/admission race → no zero window
```

### 4.3 结果

```text
gofmt -l .                                  干净
go vet ./...                                通过
go build ./...                              通过
go test ./... -count=1                      通过
STUDIO_TEST_DB=1 STUDIO_TEST_REDIS=1
  go test ./tests/integration/ -count=1     全部通过（111s，含新增 3 个）
npx tsc --noEmit                            通过
npx vitest run                              18 文件 / 158 用例 全绿
npx vite build                              通过
```

本机无 gcc，`go test -race` 由 GitHub Actions 兜——**已通过**：

```text
commit        adc5621
run           35047359734   backend    success
job check        actionlint / gofmt / vet / build / go test（unit）
                 go test -race (execution plane)                        全绿
job integration  wait for mysql 5.7 / apply migrations（含第二次必须 no-op）
                 database-backed package tests (real MySQL/Redis)
                 integration tests                                      全绿
```

Frontend workflow 是路径过滤的（只吃 `frontend/**`），本补丁前端零改动，因此没有
为 adc5621 触发 frontend run；前端以本地全套回归为准（上一次 frontend run
35044964086 亦为 success）。

---

## 5. 反证（falsification）

新增 `backend-go/scripts/falsify_review9_patch331.sh`，3 个 mutation、8 项判定
**全部符合预期**：

```text
1. 容量 SQL 恢复成 provider_submissions JOIN runs（历史驱动）
     → TestProviderCapacityQueryIsDrivenByActiveRuns  FAIL → 还原 PASS
2. stream_protocol 恢复成原样回显 return requested
     → TestSSEStreamProtocolMetricCardinalityIsBounded FAIL → 还原 PASS
     → TestStreamProtocolNegotiatesFutureVersionsDown FAIL → 还原 PASS
3. 从容量查询的 IN 清单里删掉 'cancelling'
     → TestProviderCapacityStatusListIsExactComplementOfSettled FAIL → 还原 PASS
```

修改 1 的实际 FAIL 证据（同一份 5 000 历史 + 2 活跃的 fixture）：

```text
ACCESS provider_submissions (ps) type=ref key=uniq_provider_submit_key leading_column="provider" rows=2251
ACCESS runs (r)                  type=eq_ref key=PRIMARY                 leading_column="id"      rows=1
--- FAIL: TestProviderCapacityQueryIsDrivenByActiveRuns
```

**既有 3.3 反证未被替换**：`scripts/falsify_review9_patch33.sh` 的 4 个 mutation /
8 项判定复跑仍全绿。

---

## 6. 明确未修改

```text
migration 0024（编号、索引、语义一律不动；版本保持 24）
Provider Capacity 定义 / effective / controlled / uncontrolled 三面语义
ProviderSlot ownership / lease / heartbeat / Renew
exclude-self 语义（只改了 SQL 的驱动方向，排除条件两条腿都在）
provider_submissions 状态机（sending|accepted|rejected|unknown）
AwaitExternalOwned / accepted persistence 处理 / ErrProviderAcceptancePersistence
Reaper / Retry / Defer / Finalize
client_request_id 幂等（run_requests / request_hash）
Event Cursor / sequence O(1) 分配
delta / chunk 的 UTF-8 byte Range 协议
frontend applyIncrementalRange（前端零改动）
```

3.3.1 是 `query scale + protocol normalization` 补丁，不是新的 correctness 重构。

---

## 7. 冻结与下一步

3.3.1 完成后，按复审报告 §四十二 的结论执行冻结：

```text
第九轮批次 1～3 + 3.1 + 3.2 + 3.3 + 3.3.1   正式冻结
```

此后不再围绕以下主题做零散修补：

```text
Request Idempotency / Provider Submission / Provider Capacity
Lease 与 Ownership / Event Cursor / Streaming Range / Stream Protocol
```

下一步进入 **Batch 4 — SSE Hub**，直接继承已经稳定的
`subscriber stream_protocol`（现在是 `∈ {1,2}` 的协商值）、durable cursor、
transient `sequence=0`、UTF-8 offset、terminal hard boundary、
legacy durable-only fallback。之后 Batch 5 Worker Dispatcher、Batch 6
Conversation Lifecycle / message keyset 分页、Batch 7 长对话前端。

### 遗留（非阻塞）

1. **TiDB EXPLAIN 待部署环境执行**（§2.5，本机无可授权实例）。
2. `provider_submissions` 的长期留存策略（归档 / 分区）仍未定；本补丁让准入不再
   依赖它的规模，但表本身仍随历史单调增长。

---

## 8. 交付

```text
commit   adc5621   fix(review9.3.1): drive capacity admission from active runs,
                   bound the stream protocol
branch   dev （c55f24c..adc5621）
远程     origin/dev = adc5621（ls-remote 核对；本机 ref 缺陷已按既定手法修复）
CI       backend run 35047359734  success（job check + job integration 全绿，含 -race）
         前端零改动，frontend workflow 路径过滤未触发
```

同一提交内还包含工作区里**并行会话**尚未提交的两处改动（经用户确认一并提交）：
`internal/delivery/worker.go`（`CASFinishDelivery` 的重复 status 占位符导致成功投递
`sent_at` 恒为 NULL）及其对应的 `README.md` / `ENTERPRISE.md` 说明。这两处与 3.3.1
无逻辑关联，仅因 `backend-go/README.md` 是两边共同修改的文件而无法拆分为独立提交。

