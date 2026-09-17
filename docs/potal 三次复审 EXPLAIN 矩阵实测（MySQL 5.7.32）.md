# potal 三次复审 EXPLAIN 矩阵实测（MySQL 5.7.32）

**执行文档**：`potal dev 最新代码复审暨剩余问题修改执行文档（c6db4dc）.md` P2-R1
**实测日期**：2026-09-18
**数据库**：dev 共享库 `192.168.211.26:20336/xiaoan`，`SELECT VERSION()` = **5.7.32-log**
**索引集**：migration 0025（`idx_app_catalog_public (kind, enabled, is_public, created_at, id)`、`idx_app_catalog_category (kind, enabled, category_id, created_at, id)`、`idx_runs_user_application_created (user_id, application_id, created_at)`）
**数据规模（实测时）**：applications ≈ 253 行、runtime_bindings ≈ 239 行、单用户 runs ≈ 3177 行
**复跑方式**：`cd backend-go && go run ./cmd/explain-catalog-matrix`（查询文本与 `db/queries/catalog.sql` 保持一致，查询形状或索引变化后必须重测）

---

## 一、page 查询矩阵（P2-R1 要求的 11 个场景）

| 场景 | a 表访问路径 | key | rows | Extra |
|---|---|---|---|---|
| chat / public / 首页 | range | `idx_app_catalog_public` | 187 | Using index condition; **temporary; filesort** |
| chat / consume / 首页 | range | `idx_app_catalog_public` | 187 | 同上 |
| chat / manage(staff) / 首页 | **ALL** | — | 253 | Using where; temporary; filesort |
| fixed / consume / 首页 | range | `idx_applications_kind_public` | 10 | Using index condition; filesort（**无 temporary**） |
| fixed / manage / 首页 | range | `idx_applications_kind_public` | 10 | 同上 |
| all / manage(staff) / 首页 | **ALL** | — | 253 | Using where; temporary; filesort |
| chat / consume / **cursor 第 N 页** | range | `idx_app_catalog_public` | 187 | 同首页（keyset 前缀未被 OR 谓词破坏） |
| chat / consume / category | range | `idx_app_catalog_public` | 187 | c 表 filtered 25% |
| fixed / consume / `__uncategorized__` | range | `idx_applications_kind_public` | 10 | 无 temporary |
| chat / consume / search | range | `idx_app_catalog_public` | 187 | 同首页（LIKE 在排序后过滤） |

### 结论

1. **`kind = 'chat'` 场景**：0025 的 `idx_app_catalog_public` 被 5.7 优化器稳定选中，`(kind, enabled, is_public)` 前缀全部进入 key_len（99 ≈ kind+enabled+is_public 的字节和），rows ≈ 可见 chat 池大小 —— 符合设计。
2. **`kind <> 'chat'` / `kind = all` / OR 谓词场景（本报告最担心的点）**：优化器**没有死掉**——fixed 页走 `idx_applications_kind_public`（range，rows=10），`all/manage` 走全表但那本来就是「管理面 + 无谓词」语义（staff-only，无更优索引可用）。**未发现需要立即补 `(enabled, is_public, created_at, id)` 类索引的证据**，按报告要求暂不加索引。
3. **temporary + filesort 在 chat 页普遍存在**：根因是 `(show_all OR …)` / `(consume_only=0 OR …)` 的 OR 结构使 `ORDER BY created_at, id` 无法走索引序。当前 rows examined = 整个可见 chat 池（187），可接受；**当 catalog 到 5k+ 时需重测**——若 p95 退化，首选方案是把 `consume_only` 分支拆成 UNION 两段（每段可走索引序），而不是再加二级索引。
4. 每页固定成本的 JOIN（b/newer_b/p）都是 ref/eq_ref 级，rows≈1，provider kill switch 的 JOIN 没有引入可测成本。

## 二、bootstrap 组查询矩阵（P1-R5 关联）

| 查询 | 驱动表 | runs 聚合 | Extra |
|---|---|---|---|
| default | `b` ALL(239)→a eq_ref→p eq_ref | `<derived2>` **`idx_runs_user_application_created`，Using index**，rows=3177 | temporary; filesort |
| frequent | 同上 | 同上 | 同上 |
| recommended | 同上 | 同上 + 两个 DEPENDENT SUBQUERY（均 Using index） | 同上 |
| recent fixed | `a` range(10) | derived Using index | temporary; filesort |
| agent categories | 同 default 形状 | — | temporary; filesort |

### 结论

1. **重复聚合成本可控但存在**：每次 bootstrap 对同一用户的 runs 做 4 次同形 GROUP BY，每次都索引覆盖（`Using index`，无需回表）。当前单用户 3177 行 runs 时每查询 rows≈3177，总成本 ≈ 4×3177 索引行 —— 远低于 0025 之前的「用户全历史 × 每页」问题。
2. **P1-R5 的 `user_application_usage` 汇总表暂不实施**：报告要求先在 1k/10k/100k/1m runs 规模下实测 p50/p95。当前 dev 库无法构造该规模数据（shared 库，不适合灌数），`explain-catalog-matrix` 的最后一个场景即为该聚合的形状探针；**迁移到独立可压测环境后按报告流程执行**，压力不达标再建汇总表，且汇总表更新必须只挂在「真正创建新 Run 的事务」内（幂等 replay 不得 +1）。
3. bootstrap 各组的 `b` 表全扫（239 行）是优化器在 `newer_b` 反连接形状下的选择，上限 = binding 总数；binding 数量增长到千级时需把 default/分组查询改写为先按 `a` 驱动（或加 `(application_id, enabled, id)` 复合索引辅助反连接），当前规模不动作。

## 三、后续触发条件（不要预防性优化）

- applications 表 > 5000 行，或 chat 池 > 2000 行 → 重跑本工具，观察 page chat 各场景 rows/temporary 是否线性放大。
- 单用户 runs > 100k → 压测 bootstrap p50/p95，决定 `user_application_usage`。
- runtime_bindings > 1000 → 重测 bootstrap 组查询驱动表选择。
