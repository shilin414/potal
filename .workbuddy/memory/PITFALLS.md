# 本机环境与工具踩坑（Creation Agent Studio）

> 从 `MEMORY.md` 拆出：这里是「本机怎么跑、哪些命令会翻车」的操作知识，
> 不是代码不变量。开工前或遇到怪现象时读一遍。代码规则见 `MEMORY.md`。

## 反证驱动（falsification）
- 驱动目录 `backend-go/scripts/`：`falsify_review9*.sh`、`falsify_review10.sh`(A–G)、
  `falsify_review10_patch41.sh`(H–K)、`falsify_review10_patch411.sh`(L/M/N)。
- **⚠️ 绝不可与其它 `go test` 并发**：脚本改真实源码，任何在 mutation 在位期间构建的测试二进制
  都在编译坏代码。反证一次只跑一个。
- **改代码后必须同步复核旧脚本锚点**：锚点失配会直接终止驱动（`assert ... missing`）。
- 跑完双查：`grep -rn FALSIFICATION backend-go --include='*.go' --include='*.sql'` +
  `find . -name '*.orig'`（两个都必须为空；脚本自己也会查并在非空时判 fail）。
- 反证脚本结构：`snapshot` → python 变异（带 `assert`）→ `verdict` 取 FAIL/PASS → `revert`；
  `trap cleanup EXIT` 兜中断。文本变异用 python，**不要用 bash 嵌套 heredoc**（本机会被截断）。
- 只有「能被删坏」的断言才有价值：加测试时必须同时确认它 **mutated FAIL / restored PASS**
  （Batch 4.1.2 的 Mutation N 就是这么补上的）。

## CI 红而本地全绿 —— 定位套路
1. **先把 `GOMAXPROCS` 限到 2**（GitHub runner 只有 2 vCPU）再复现。本机核多，
   调度类竞态/采样竞态会被掩盖，不限就永远复现不出来。
   `for i in $(seq 1 12); do GOMAXPROCS=2 go test <pkg> -count=1 2>&1 | grep -E '^(--- FAIL|    \w+_test)'; done`
2. **先证明是不是自己引入的**：`git diff <上一次绿的 sha> HEAD -- <文件>` 看那个测试在不在
   diff 里。不在 → 既有 flake，上一次 CI 只是没抽到，别乱改生产代码。
3. **看是"生产 bug"还是"测试采样竞态"**：若某标志位是在被测函数**返回之后**才发布的，
   测试里"waitFor 一个早发布的事实 → 紧接着裸采样晚发布的事实"就是测试自己的竞态。
   修法是补一条有界 `waitFor`，**原断言行不要动**（这样才不算"放宽"）。
4. 已知一处（`TestHubManagerSharesOneUpstreamPerRun`）已于 2026-09-16 修好（`6a90295`）。
5. 本机无 token/`gh`，Actions 日志走匿名 API 会 403；**拿不到日志就只能靠本地压测反推**。

## 本机 git
- `core.autocrlf=true` 与仓库 LF 索引相冲：git 重写文件后工作区变 CRLF，`gofmt -l` 误报。
  本仓库已局部 `core.autocrlf=false`；再见到就用 python 把内容归一回 `\n`。
  **⚠️ Edit/Write 工具本机写文件也会落 CRLF**（2026-09-16 两次实测：改 `Header.tsx` 删 3 行 →
  diff 全文件 112 行；改 `MobileAppShell.tsx` 删 1 行 → 246 行。**`.md` 文件不受影响**，
  只有 `.tsx`/代码文件命中）。识别特征：`git diff --stat` 的增删行数远大于实际改动。
  一行修法（本机 python）：
  ```bash
  "C:/Users/吴志彬/.workbuddy/binaries/python/versions/3.13.12/python.exe" -c "
  from pathlib import Path; p=Path('<file>'); b=p.read_bytes(); print('crlf',b.count(b'\r\n')); p.write_bytes(b.replace(b'\r\n',b'\n'))"
  ```
  归一后 diff 立刻收窄到真实改动（112→5 行、246→4 行）。
  **提交前一定扫一眼 `git diff --stat` 的行数是否与实际改动相符。**
- **`refs/remotes/<name>/<branch>`（两层）写入有缺陷**（PortableGit 2.55 / 系统 2.53 都复现）：
  `git fetch/push` 返回 0、reflog 也写了，但 `.git/refs/remotes/origin/` 目录不存在 → 引用丢失，
  `git status -sb` 误报 `[ahead N]` / `[gone]`。**push 本身是成功的**。
  修复：`mkdir -p .git/refs/remotes/origin`（**承重步骤，漏了必失败**）→
  `printf '%s\n' "$(git ls-remote origin refs/heads/dev | cut -f1)" > .git/refs/remotes/origin/dev`
  → `sed -i` 双写 `packed-refs` → `for-each-ref` + `ls-remote` 核对。
- **看到 ahead/gone 不要急着重推**：先 `git ls-remote origin refs/heads/<branch>` 与本地对比。
- **⚠️ 不要为对照基线 `git checkout <sha>`**：本机实测会被 SIGTERM 打断，留下「工作区文件被删 +
  `.git/index.lock` 残留 + 分支没切过去」。恢复：备份未提交改动到仓库外 → `rm .git/index.lock` →
  `git reset --hard HEAD` → 从备份恢复。对照基线用 `git checkout <sha> -- <路径>`。
- 无 `gh`；查 CI 用匿名 GitHub API。
- **写 loose ref 用 `Set-Content -Encoding ascii -NoNewline` 时 git 会报
  `ignoring ref with broken name refs/remotes/origin/dev?`**（`?` 是 BOM）→ 必须显式
  `-Encoding ascii`（或 `utf8NoBOM`）；PowerShell 默认的 `utf8` 会带 BOM。
  修复后核对：`git for-each-ref refs/remotes/` 无 warning + `git status -sb` 无 `[ahead]`。
- **2 层 ref 缺陷第 13 次命中（2026-09-17）**：`push` 打印 `b63cdd2..9d00f1a  dev -> dev`，
  本地 ref 仍 `b63cdd2`。顺序照做即可（push → mkdir → loose ref → packed-refs 双写）。
  本条**每次 push 后都要复查**，不要相信 git 打印的成功信息。

## Git Bash / shell
- **2026-09-17：Bash 工具整会话不可用**——`ls/cat/head/cp/dirname` 全部
  `command not found`（PortableGit 的 `bash.exe` 起不来 coreutils），
  加 PATH 也无效。**直接改用 PowerShell 工具**；脚本/测试文件用 **Write 工具**写，
  **别用 heredoc**（`cat > f <<EOF` 本会话必失败）。
## PowerShell 工具专项
- **`$LASTEXITCODE` 在管道或赋值后会被清掉**：`cmd 2>&1 | Out-File ...` 之后再读
  `$LASTEXITCODE` 拿到的是**空**（本会话为此空跑 3 次）。可靠写法：
  ```powershell
  $out = & some.cmd args 2>&1 | Out-String
  Set-Content -Path out.txt -Value "EXIT=$LASTEXITCODE`n$out" -Encoding utf8
  ```
  或把命令与 `"EXITCODE=$LASTEXITCODE"` 一起写进**同一条** `Set-Content`。
- 看到 `...+ ... is not recognized`／`NativeCommandError` 时，先确认命令本身是否存在
  （`go`/`sqlc`/`git`），PowerShell 会把 stderr 也包装成错误记录，**exit code 才是判据**。
- 本机 **`sqlc` 未安装**（`~/go/bin/sqlc.exe` 也不存在）→ 改 `db/queries/*.sql` 后
  `internal/gen/db/*.go` 只能**手工按生成风格同步**；有 sqlc 时再 `sqlc generate` 核对漂移。

## 数据库 / Redis
- MySQL 5.7 `192.168.211.26:20336/xiaoan`、Redis `192.168.211.239:6380`，
  凭据在 `backend-go/.env.local`；DSN 保持 UTC 不动。
- **⚠️ 这是共享库，跑集成测试前先查有没有别人卡住的长事务**：
  ```sql
  SELECT id, time, state, LEFT(info,120) FROM information_schema.processlist
  WHERE command='Query' AND time > 60;
  ```
  2026-09-17 实测：一个**别的会话遗留的** `tmp_clean_itest.py` 长事务里
  `DELETE FROM outbox_events ... IN (SELECT ... 'itest-%')` **卡了 1703s**，持着 `runs` 行锁，
  导致我的集成测试全部报 `Error 1205 Lock wait timeout exceeded`，
  **看起来完全像自己的代码 bug**（还会让人误改冻结的 reaper）。
  处置：确认是遗留清理语句后 `KILL <id>`（**只杀 `info LIKE '%itest-%'` 且 `time>60` 的**）。
- 开关：`STUDIO_TEST_DB=1` / `STUDIO_TEST_REDIS=1`（不设则集成用例静默 skip）。
- 集成门禁顺序（本机 ≈2.5 分钟）：
  `go run ./cmd/migrate` ×2（**第二次必须干净 no-op**）→
  `go test ./internal/execution/... ./internal/delivery/... ./internal/platform/...` →
  `go test ./tests/integration/...`。
- 全库 COUNT=0 会被 fixture 假红；清理圈定 `provider LIKE 'itest%'`。
- 改 `db/queries/*.sql` 后跑 `~/go/bin/sqlc.exe generate`（本机未装，见上）。
- **本机集成 flake**：`TestRetryRunAndOutboxShareRetryAt` /
  `TestReaperRequeueIsImmediatelyClaimableAndInSync` 要求两次独立写的 `available_at` 差 ≤50ms，
  实测 60–110ms（A/B 交替已证基线同样失败）；`TestTickAndRunNowConcurrent` 全量跑因库污染失败、
  隔离跑通过。**故集成测试不要与前端 build 等重负载并发跑。**

## CI 映射
- 前端：`npx tsc --noEmit` / `npx vitest run` / `npx vite build`（不跑 eslint）。
- 后端：actionlint / gofmt check / `go vet` / `go build` / `go test ./... -count=1`，
  另加 **race** 门 `go test -race ./internal/execution/... ./internal/delivery/...
  ./internal/automation/... ./internal/transport/sse/... -count=1`，
  以及 integration job（mysql5.7 + redis7 service containers）。
- **本机 `CGO_ENABLED=0` 且无 gcc，`-race` 跑不了**（`CGO_ENABLED=1` 实测 `gcc not found`），
  并发正确性只能由 CI 的 race job 兜住 → 本地提交前至少保证功能断言全绿。

## Redis pub/sub 与 SSE 测试
- pub/sub **不为未来订阅者缓冲**：集成测试里"证明网关已经订阅"必须**循环重发直到收到**。
- 区分两类 SSE 测试："测 transient 帧本身"必须声明 `stream_protocol=2`；
  "测 legacy 兼容"不要声明。
- Gateway 装配：`sse.Gateway` 已无 `Redis` 字段；
  用 `sse.NewHubManager(ctx, rdb, nil, opts)` + `t.Cleanup(hub.Close)`。
- **手造 durable SSE 帧是契约负债**：必须 `appendLiveEvent`（真实落库）或
  `seedDurableEvent`（插行 + 推进 `next_event_sequence`）；liveness 探针用 **transient 帧**。

## 本地起环境
5 个进程：api / stream / worker(`--provider=feishu_aily`) /
**worker(`--provider=feishu_delivery`)** / vite。
漏掉 delivery 时飞书收不到（`delivery_executions` 停在 `pending`）。
`(cmd &)` 自 detach 活不下来，常驻必须 `run_in_background`。

## 数据层踩坑
- **sqlc 隐式改名连带破调用点与反证锚点**：`ps.run_id <> ?` 被改写成 `r.id <> ?` 会让参数名从
  `RunID_2` 变 `ID`；要保名就写 `ps.run_id <> ?`。
- **`CASFinishDelivery` 双占位符**：`sent_at = IF(?='succeeded',…)` 的第二个 `?` 生成成
  `Column5`，调用方必须把 status 传两次。
- **EXPLAIN 断言只在真实数据形状下有意义**：空 provider 上所有候选索引都估 1 行；须先造
  「5k settled + 2 活跃」。`table` 列有别名时返回别名；索引前导列查
  `information_schema.STATISTICS`，**按 shape 断言不按 key 名**。

## 操作红线（会静默出事，代价已付过）
- **同一文件绝不可在一条消息里发两个 Edit**：工具各自基于同一旧快照写回，**后写覆盖前写 = 静默丢失**，
  且**仍然编译通过**。本轮被吃掉过 `app.go` 的 `SSEHub` 装配（丢失后 SSE 退化成「只有 durable replay、
  没有 live 事件」，日志不报错、指标全 0）。纪律：串行改 + 收尾 `grep` 复核关键装配行；
  `git diff --stat` 与预期不符时优先怀疑这个。
- **反证驱动绝不可与其它 `go test` 并发**（脚本改真实源码）。
- **不要为对照基线 `git checkout <sha>`**（会被 SIGTERM 打断、留半成品工作区）；用 `git checkout <sha> -- <路径>`。
- **同一工作区可能有并发会话**：开工前 `git status` + 看关键文件 mtime；提交前若混着别人的改动**先问用户**，
  别 `git add -A`（会把别人的在制品卷进你的提交）。
- 顺手：**改动落地后必须同步更新旧反证脚本的锚点**，锚点失配会让驱动在断言前终止（假绿）。

## 启动 dev 环境时的现场检查（2026-09-16 实测）
- 上一轮会话的进程常常**还活着**：先 `netstat -ano | grep LISTENING | grep -E ":(8080|8081|3030)\b"`，
  再用 `tasklist /FO CSV | grep -iE "api.exe|stream.exe|worker.exe"` 认进程。
- **Git Bash 里 `taskkill /PID x /F` 会被 MSYS 路径转换吃掉**（报「键入 TASKDILL /?」）→ 必须
  `MSYS_NO_PATHCONV=1 taskkill /PID <pid> /F`（或改用 PowerShell 工具）。
- 起之前**确认没漏 delivery worker**：只起 aily worker 时「对话正常但飞书群收不到」，
  `delivery_executions` 停在 `pending`（见上「本地起环境」）。
- **⚠️ 端口要在「启动那一秒」查，查早一分钟都可能撞车**（2026-09-17 实测）：
  17:39 查 `8080/8081/3030` 全空 → 17:39:58 编译完 → 17:40:07 `api.exe` 报
  `listen tcp :8080: bind: Only one usage of each socket address ...` 2 秒即退出 ——
  **期间另一个会话（或用户本人）把整套环境起起来了**。
  **判据**：`api.exe` 在 1–2 秒内退出且日志里只有一条 `studio-api listening` + 一条
  `http server ... bind: Only one usage of each socket address` → **不是崩溃、不是被杀，
  是端口已被别人占**。先 `netstat -ano | grep LISTENING | grep :8080` 看 PID，
  用 `tasklist` 认一下是不是自己的再决定动不动手，**别重复起一套**。
- **delivery worker 一起来就会 drain 历史 pending**（`dueScanLoop` 自动补发）：
  会有 `delivery failed permanently code=send_failed` 之类的日志，属历史任务收敛，不是本次启动的回归。
- dev 环境已起：api/stream 各自 `/healthz` 返回 **401**（= 活着且鉴权生效，不是故障），vite `:3030` 返回 200。
  业务链自证：`POST /api/auth/login/`（demo / Creator@2026）拿 `studio_session` → `GET /api/auth/me/` → `GET /api/v2/runtimes`。
  cookie 只在 shell 变量里传递，**不要落盘**。

## 本机 git：**绝对禁止 `git stash`**（2026-09-17 实测丢对象库）

**症状**：`git stash push -- <文件>` 会报 `<sha> is not a valid object`，随后
`.git/refs/heads` 与 `.git/refs/remotes` **整个目录消失**、`.git/objects/pack/*.pack` **被删只剩 `.idx`**
（`git count-objects -v` → `in-pack: 0 packs: 0`），`git log` 报"分支没有任何提交"。
根因同「本机 git 引用写入缺陷」：**git 事务失败后的回滚清理会删掉"因此变空的目录"**，
删到 `refs/heads` 丢分支，删到 `objects/pack` 丢**全部历史**。

**纪律**：
- 不做 `git stash`。要"临时移开某些改动看基线"时，**不要动工作区的已跟踪文件**——
  改用 `git show <sha>:<path>` 取内容、或把改动文件**复制到仓库外**再临时还原，
  或干脆只靠"当前整树 `tsc`/`vitest` 全绿 + 论证依赖方向"来确认可构建性。
- 一切会触发 git 事务回滚的写操作都要警惕；`git add`/`commit` 正常可用（本轮多次成功）。

**恢复流程（已实测成功）**：
1. 先确认工作区文件完好（`node` 读文件 grep 关键标识）——**内容才是资产，历史可从远程再取**。
2. 把失效的孤儿 `.idx` / `multi-pack-index` 移出 `.git/objects/pack/`（缺 `.pack` 会让 git 报错）。
3. `git fetch origin --tags` 取回对象库（会被本机 SIGTERM 打断 → 用 `run_in_background`）。
   完成后 `git cat-file -t <远程tip>` 应返回 `commit`。
4. 手工重建引用：`mkdir -p` + 写 loose ref + 双写 `packed-refs`，**一律 LF**（见下条）。
5. `git fsck --no-progress` 应无输出（只有 `dangling …` 是正常的）；
   残留的 `invalid reflog entry <sha>`（指向已消失的提交）可把该行从 `.git/logs/**` 过滤掉。
6. 索引里引用的 blob 可能一起丢 → `git commit` 报 `invalid object … for <path>`，
   **重新 `git add` 该文件**即可（基于工作区重建 blob，内容无损）。

**`packed-refs` 必须是 LF**：CRLF 会让 git 把引用名解析成 `…dev\r`，报
`warning: ignoring ref with broken name refs/remotes/origin/dev?`（`\r` 显示成 `?`），
于是 `for-each-ref` 一条都不列、`git log` 也可能异常。写完用 `node -e "…includes(13)"` 断言无 `\r`。
（另一种同症状的成因是**写 loose ref 时带了 BOM** —— 用 `Set-Content -Encoding ascii` 可避免。）

## 共享 dev 库（`192.168.211.26:20336/xiaoan`）的写操作纪律（2026-09-17 实测，代价 28 分钟）

- 服务器默认 **`innodb_lock_wait_timeout = 5`**（且 `innodb_rollback_on_timeout=0`），
  长事务/宽扫描必然撞 `1205`。
- **共享库上禁止长事务**：一次 `DELETE ... WHERE run_id IN (SELECT id FROM runs WHERE ...)`
  的跨 991 个 run 宽子查询卡了 28 分钟、持 `runs` 行锁，
  **把并发会话的集成测试全部打成 `1205 Lock wait timeout`**，对方当成自己的代码 bug 查了半天。
  → 正确配方：**id 先取到客户端（Python/Go 侧）→ 显式 `IN (...)` 分块 → `autocommit=True` 短事务
  → `1205` 退避重试**。改完 5 秒跑完全部 1800+ 行的级联。
- 批量删数据前**先采样确认数据停止增长**：并发会话在跑集成测试时，`itest-*` 会边删边涨
  （实测 1818 → 1844 → 1852），"清空"这个目标不成立。40 秒内 3 次采样一致再动手。
- 删前用**指纹守卫**：把要保留的真实行（计数/关键字段）前后对比，不一致就回滚并中止。
- 级联面**必须用 `information_schema.columns` 枚举**，不能凭记忆：
  `runs` 挂 9 张表、`conversations` 挂 4 张；`outbox_events` 是**多态**
  （`aggregate` + `aggregate_id`，聚合值实测只有 `run` / `delivery`），没有 `run_id` 列。
- 这张库**没有任何外键约束**（`information_schema.referential_constraints` 为空）：
  不会自动级联，顺序写错只会留孤儿 → 必须"子表排空到 0 再动父表"。
- `applications` 曾被历次 authz/gate 集成测试灌入 **1818 条 `itest-*` 残留**（2026-09-17 已清理，
  清理后仅剩 7 条真实应用）。**根治办法是让集成测试自带 `t.Cleanup`**，否则会持续复发。
