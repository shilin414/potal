// 第九轮补丁 3.3.1-A 验证：Provider 容量的准入查询必须由 ACTIVE RUN 驱动，
// 而不是由 provider_submissions 的生命周期历史驱动（复审报告 §四–§二十一）。
//
// 3.3 已经把「有效容量」的语义做对了：max_inflight 约束的是真实的 Provider
// 执行，而不是本地 worker 恰好持有的 slot。语义没有变，本轮改的是 SQL 的
// 遍历方向：
//
//	旧：provider_submissions(历史) → JOIN runs → 剔除 settled
//	新：runs(active，有界) → STRAIGHT_JOIN provider_submissions(run_id)
//
// provider_submissions 是历史表：run settled 之后 submission 不会被删除，
// 所以 state='accepted' 的范围覆盖了建表以来的每一次 Provider 执行。准入在
// 每一次 Acquire 上执行（不是报表），因此 candidate 范围随历史单调增长会把
// 「DB 查询变慢」放大成「同 Provider 准入串行化」。
//
//	TestProviderCapacityStatusListIsExactComplementOfSettled   状态清单与枚举互补
//	TestProviderCapacityIgnoresLifetimeAcceptedHistory         大历史集下计数不变
//	TestProviderCapacityQueryIsDrivenByActiveRuns              EXPLAIN 证明 join 方向
//
// 复用了 review9_patch33_test.go 的 fixture（seedRun / claimForSlots /
// beginSubmission / capacityDepths），每个用例用自己的 provider key。
package integration

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
)

// ── 1. 状态清单：SQL 的 IN 列表必须恰好是 settled 的补集 ──────────────────

// TestProviderCapacityStatusListIsExactComplementOfSettled guards the ONE
// thing the 3.3.1 rewrite made easy to get wrong.
//
// The capacity queries used to say `r.status NOT IN (cancelled, succeeded,
// failed, interrupted)`, which was self-maintaining: a tenth status added to
// the enum would automatically be treated as active. The active-run-driven
// form needs the opposite — an explicit `IN (...)` list, because `status`
// leads idx_runs_claim(status, provider, queued_at) and a NOT IN cannot use
// that index range.
//
// The trade is deliberate, but it makes the list a maintenance hazard: a new
// status that is missing from it silently stops holding provider capacity
// (over-admission), and a settled status accidentally left in it pins
// capacity forever. So the list is pinned here against the real predicate —
// execution.IsSettled — instead of being duplicated as a comment.
//
// The test reads db/queries/execution.sql directly: the SQL text is the thing
// under test, and the generated Go constant is only a copy of it.
func TestProviderCapacityStatusListIsExactComplementOfSettled(t *testing.T) {
	// The full run status enum (九 states, the validated API contract).
	all := []string{
		execution.StatusQueued,
		execution.StatusRunning,
		execution.StatusWaitingInput,
		execution.StatusWaitingExternal,
		execution.StatusCancelling,
		execution.StatusCancelled,
		execution.StatusSucceeded,
		execution.StatusFailed,
		execution.StatusInterrupted,
	}
	var wantActive []string
	for _, s := range all {
		if !execution.IsSettled(s) {
			wantActive = append(wantActive, s)
		}
	}
	if len(wantActive) != 5 {
		t.Fatalf("active statuses = %v, want 5 of the 9-state enum", wantActive)
	}
	sort.Strings(wantActive)

	raw, err := os.ReadFile("../../db/queries/execution.sql")
	if err != nil {
		t.Fatalf("read execution.sql: %v", err)
	}
	src := string(raw)

	queries := []string{
		"CountProviderEffectiveInflight",
		"CountProviderEffectiveInflightExcludingRun",
		"CountProviderUncontrolledInflight",
	}
	for _, name := range queries {
		block := stripSQLLineComments(queryBlock(t, src, name))
		got := statusListOf(t, block, name)
		// Every capacity query must filter runs by the SAME active set.
		// Equal in both directions: a missing status over-admits, an extra
		// settled status leaks capacity.
		if len(got) != len(wantActive) {
			t.Fatalf("%s status list = %v, want exactly %v (the complement of IsSettled)",
				name, got, wantActive)
		}
		for i := range got {
			if got[i] != wantActive[i] {
				t.Fatalf("%s status list = %v, want %v (the complement of IsSettled)",
					name, got, wantActive)
			}
		}
		// The list must be an IN, not a NOT IN: idx_runs_claim leads with
		// status, and NOT IN cannot use that index range — it would put the
		// plan back onto a scan, which is the regression this patch removes.
		if strings.Contains(block, "status NOT IN") {
			t.Fatalf("%s still filters status with NOT IN; writes must use the "+
				"explicit IN list so idx_runs_claim(status, ...) stays usable", name)
		}
		// The driving-side invariant is the other half of 3.3.1-A and belongs
		// in the same guard: without STRAIGHT_JOIN the optimizer is free to
		// reorder the join and re-create the history-driven plan that
		// TestProviderCapacityQueryIsDrivenByActiveRuns detects on live
		// statistics (which a local run may not reproduce).
		if !strings.Contains(block, "STRAIGHT_JOIN") {
			t.Fatalf("%s lost its STRAIGHT_JOIN: the active-run driving side is only "+
				"guaranteed if the join order is pinned in the statement", name)
		}
		if strings.Contains(block, "FROM provider_submissions") {
			t.Fatalf("%s drives FROM provider_submissions; the ledger is history and must "+
				"never be the driving side of an admission query", name)
		}
	}
}

// stripSQLLineComments removes `-- …` line comments so the guards below match
// only executable SQL. The doc comments in execution.sql deliberately quote the
// predicates they explain, and matching those would make the test read its own
// explanation as the implementation.
func stripSQLLineComments(block string) string {
	lines := strings.Split(block, "\n")
	out := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// queryBlock returns the text of one `-- name: X ...` query up to the next
// `-- name:` marker.
func queryBlock(t *testing.T, src, name string) string {
	t.Helper()
	marker := "-- name: " + name + " "
	start := strings.Index(src, marker)
	if start < 0 {
		t.Fatalf("query %s not found in db/queries/execution.sql", name)
	}
	rest := src[start+len(marker):]
	end := strings.Index(rest, "-- name: ")
	if end < 0 {
		end = len(rest)
	}
	return rest[:end]
}

// statusListOf extracts the statuses of the runs filter that drives a capacity
// query. Exactly one such filter is expected per query; zero or two means the
// query was reshaped and the guard no longer covers it.
func statusListOf(t *testing.T, block, name string) []string {
	t.Helper()
	// `r.status IN (\n  'queued',\n ... )` — the run alias is always `r` in
	// these queries; `s.` (slots) and `ps.` (submissions) are different
	// dimensions and must not be picked up here.
	re := regexp.MustCompile(`(?s)r\.status IN \((.*?)\)`)
	matches := re.FindAllStringSubmatch(block, -1)
	if len(matches) != 1 {
		t.Fatalf("%s has %d `r.status IN (...)` filters, want exactly 1", name, len(matches))
	}
	var out []string
	for _, m := range regexp.MustCompile(`'([a-z_]+)'`).FindAllStringSubmatch(matches[0][1], -1) {
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out
}

// ── 2. 规模：大历史集下计数不变 ──────────────────────────────────────────

// seedCapacityHistory bulk-inserts N settled runs whose provider submission
// still carries `state` — exactly the shape of the real ledger after a year:
//
//	provider_submissions.state = 'accepted'
//	runs.status                = 'succeeded'
//
// A history-driven plan pays for all of these on every admission; an
// active-run-driven plan never looks at them.
func seedCapacityHistory(t *testing.T, svc *execution.Service, provider, state, runStatus string, n int) {
	t.Helper()
	ctx := context.Background()
	hash := bytes.Repeat([]byte{0x7f}, 32) // BINARY(32), not part of any key

	const batch = 500
	for off := 0; off < n; off += batch {
		size := batch
		if off+size > n {
			size = n - off
		}
		runArgs := make([]any, 0, size*4)
		subArgs := make([]any, 0, size*5)
		runQ := strings.Builder{}
		runQ.WriteString(`INSERT INTO runs (id, provider, runtime_type, status, attempt, max_attempts) VALUES `)
		subQ := strings.Builder{}
		subQ.WriteString(`INSERT INTO provider_submissions
			(run_id, submission_no, provider, idempotency_key, request_hash, state, attempt)
			VALUES `)
		for i := 0; i < size; i++ {
			if i > 0 {
				runQ.WriteString(",")
				subQ.WriteString(",")
			}
			runQ.WriteString("(?,?,?,?,1,3)")
			id := ids.New()
			runArgs = append(runArgs, id.Bytes(), provider, "agent", runStatus)

			subQ.WriteString("(?,1,?,?,?,?,1)")
			subArgs = append(subArgs, id.Bytes(), provider,
				fmt.Sprintf("hist-%s-%d-%d", strings.TrimPrefix(provider, "itest_slots_"), off+i, time.Now().UnixNano()%1e6),
				hash, state)
		}
		if _, err := svc.DB.ExecContext(ctx, runQ.String(), runArgs...); err != nil {
			t.Fatalf("seed %s history runs (offset %d): %v", runStatus, off, err)
		}
		if _, err := svc.DB.ExecContext(ctx, subQ.String(), subArgs...); err != nil {
			t.Fatalf("seed %s history submissions (offset %d): %v", state, off, err)
		}
	}
}

// TestProviderCapacityIgnoresLifetimeAcceptedHistory is the scale regression
// the 3.3.1 rewrite exists for.
//
// The fixture builds a ledger shaped like production after a year —
//
//	8 000 × runs.status='succeeded' + submission.state='accepted'
//	2 000 × runs.status='succeeded' + submission.state='unknown'
//
// — and then three ACTIVE runs, one per leg of the predicate:
//
//	A = accepted + running          counted (submission leg, shares its slot)
//	B = unknown    + waiting_external  counted (submission leg, no slot)
//	C = rejected   + queued         NOT counted (definitive refusal)
//
// The assertion is that the count is 2 and not 10 002. Both history groups are
// deliberately present: the `unknown` group proves the status leg of the AND
// is doing work, the `accepted` group proves the state leg is.
//
// No wall-clock assertion (复审报告 §三十八): CI hardware, buffer pool and
// statistics all move milliseconds around, so the test pins the COUNT, not a
// duration. The plan shape is pinned separately, by EXPLAIN.
func TestProviderCapacityIgnoresLifetimeAcceptedHistory(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	provider := slotProvider(t)
	cleanupProviderRunsOnly(t, svc.DB, provider)

	const historyAccepted = 8000
	const historyUnknown = 2000
	seedCapacityHistory(t, svc, provider, execution.SubmissionAccepted, execution.StatusSucceeded, historyAccepted)
	seedCapacityHistory(t, svc, provider, execution.SubmissionUnknown, execution.StatusSucceeded, historyUnknown)

	// Sanity: the history really is there, and really is settled.
	var historyRows, historySubs int
	if err := svc.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM runs WHERE provider = ? AND status = 'succeeded'`, provider).
		Scan(&historyRows); err != nil {
		t.Fatalf("count history runs: %v", err)
	}
	if err := svc.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM provider_submissions WHERE provider = ?`, provider).
		Scan(&historySubs); err != nil {
		t.Fatalf("count history submissions: %v", err)
	}
	if historyRows != historyAccepted+historyUnknown || historySubs != historyAccepted+historyUnknown {
		t.Fatalf("history fixture = %d runs / %d submissions, want %d/%d",
			historyRows, historySubs, historyAccepted+historyUnknown, historyAccepted+historyUnknown)
	}

	// A = accepted + running, holding a live slot (the normal in-flight run).
	// max=2 is the ceiling the three active runs are measured against.
	slots := execution.NewProviderSlots(svc.DB, provider, 2, time.Minute)
	runA := claimForSlots(t, svc, provider)
	slotA, okA, _, errA := slots.Acquire(ctx, runA.Ownership)
	if errA != nil || !okA {
		t.Fatalf("acquire A: ok=%v err=%v", okA, errA)
	}
	subA := beginSubmission(t, svc, runA.Ownership, provider)
	if err := svc.MarkSubmissionAcceptedOwned(ctx, runA.Ownership, subA, "hist-chat-1"); err != nil {
		t.Fatalf("mark A accepted: %v", err)
	}

	// B = unknown + waiting_external, no slot (the parked unresolved run).
	runB := claimForSlots(t, svc, provider)
	subB := beginSubmission(t, svc, runB.Ownership, provider)
	if err := svc.MarkSubmissionStateOwned(ctx, runB.Ownership, subB, execution.SubmissionUnknown, "itest: 3.3.1 unknown"); err != nil {
		t.Fatalf("mark B unknown: %v", err)
	}
	if err := svc.AwaitExternalOwned(ctx, runB.Ownership, "itest: 3.3.1 park"); err != nil {
		t.Fatalf("park B: %v", err)
	}

	// C = rejected + queued: the refusal releases its slot, then a dead worker
	// (expired lease) requeues the run. Rejection must never occupy capacity,
	// and neither must the requeue that follows it.
	runC := claimForSlots(t, svc, provider)
	subC := beginSubmission(t, svc, runC.Ownership, provider)
	if err := svc.MarkSubmissionStateOwned(ctx, runC.Ownership, subC, execution.SubmissionRejected, "itest: 3.3.1 refused"); err != nil {
		t.Fatalf("mark C rejected: %v", err)
	}
	expireLease(t, svc, runC.Ownership.RunID, "slot-worker")
	recoverRun(t, svc, runC.Ownership.RunID)

	if st := readRunState(t, svc.DB, runA.Ownership.RunID); st.status != execution.StatusRunning {
		t.Fatalf("A status = %q, want running", st.status)
	}
	if st := readRunState(t, svc.DB, runB.Ownership.RunID); st.status != execution.StatusWaitingExternal {
		t.Fatalf("B status = %q, want waiting_external", st.status)
	}
	if st := readRunState(t, svc.DB, runC.Ownership.RunID); st.status != execution.StatusQueued {
		t.Fatalf("C status = %q, want queued (the reaper requeues a rejected run)", st.status)
	}
	assertSlotRows(t, svc, provider, 1, "only A holds a slot")

	// THE assertion: 10 000 settled provider calls contribute nothing.
	eff, ctl, unc := capacityDepths(t, slots)
	if eff != 2 || ctl != 1 || unc != 1 {
		t.Fatalf("with %d settled history rows: effective=%d controlled=%d uncontrolled=%d, want 2/1/1 "+
			"(A accepted+running, B unknown+waiting_external, C rejected) — a history-driven "+
			"count would read %d", historyAccepted+historyUnknown, eff, ctl, unc,
			historyAccepted+historyUnknown+2)
	}

	// The bound is real, not a coincidence: with A+B occupying both slots a
	// fourth run is refused...
	runD := claimForSlots(t, svc, provider)
	slotD, okD, depthD, errD := slots.Acquire(ctx, runD.Ownership)
	if errD == nil && okD {
		_ = slots.Release(ctx, slotD)
	}
	if okD {
		t.Fatalf("D was admitted although effective=%d already reached max=2 (err=%v)", depthD, errD)
	}
	// ...and with a ceiling of 3 the exact same run is admitted, which proves
	// the refusal above was the count being exactly 2 rather than an
	// unrelated admission failure.
	roomy := execution.NewProviderSlots(svc.DB, provider, 3, time.Minute)
	slotD2, okD2, depthD2, errD2 := roomy.Acquire(ctx, runD.Ownership)
	if errD2 != nil || !okD2 {
		t.Fatalf("D was refused at max=3: ok=%v depth=%d err=%v", okD2, depthD2, errD2)
	}
	if depthD2 != 3 {
		t.Fatalf("depth after admitting D = %d, want 3 (A + B + D)", depthD2)
	}
	if err := roomy.Release(ctx, slotD2); err != nil {
		t.Fatalf("release D: %v", err)
	}
	if err := slots.Release(ctx, slotA); err != nil {
		t.Fatalf("release A: %v", err)
	}
}

// cleanupProviderRunsOnly is cleanupProviderCapacity plus the runs themselves,
// so the 10 000-row scale fixture cannot leak into the shared dev database.
// run_events has no FK to runs (0001), so its rows are removed explicitly
// rather than orphaned.
func cleanupProviderRunsOnly(t *testing.T, db *sql.DB, provider string) {
	t.Helper()
	t.Cleanup(func() {
		for _, stmt := range []string{
			`DELETE e FROM run_events e JOIN runs r ON r.id = e.run_id WHERE r.provider = ?`,
			`DELETE FROM provider_execution_slots WHERE provider = ?`,
			`DELETE FROM provider_admission_locks WHERE provider = ?`,
			`DELETE FROM provider_submissions WHERE provider = ?`,
			`DELETE FROM runs WHERE provider = ?`,
		} {
			if _, err := db.ExecContext(context.Background(), stmt, provider); err != nil {
				t.Logf("capacity scale cleanup %q: %v", stmt, err)
			}
		}
	})
}

// ── 3. EXPLAIN：join 方向 ────────────────────────────────────────────────

// TestProviderCapacityQueryIsDrivenByActiveRuns pins the query SHAPE, which is
// the actual 3.3.1-A fix. The count being right is necessary but not
// sufficient — a correct count produced by a plan that still starts at
// provider_submissions' history range will keep getting slower forever.
//
// It EXPLAINs the REAL query text the driver sends (read out of the generated
// db package, not retyped here, so the test cannot drift from production), and
// asserts:
//
//	runs                 appears in the plan BEFORE provider_submissions
//	provider_submissions is NOT reached through idx_provider_submissions_capacity
//
// The second assertion is the observable form of "the ledger is a lookup, not
// a driving range": with `run_id` pinned by the join, the row must come from
// the primary key (run_id, submission_no). Reaching it through the capacity
// index means the optimizer drove from the ledger and this patch is not doing
// its job — the mutation in scripts/falsify_review9_patch331.sh restores
// exactly that plan.
//
// Deliberately NO assertion on `rows` (复审报告 §二十): estimates move with
// statistics, and `rows < 100` would be a flaky test that fails on a table
// with no ANALYZE. The row estimates, keys and Extra values are logged
// instead, so a CI reader can see the plan without the test guessing.
func TestProviderCapacityQueryIsDrivenByActiveRuns(t *testing.T) {
	svc, _ := testEnv(t)
	provider := slotProvider(t)
	cleanupProviderRunsOnly(t, svc.DB, provider)

	// The plan under test is only meaningful against the data shape this patch
	// exists for (复审报告 §三十九): one provider with a long settled ledger and
	// a handful of live runs. On an EMPTY provider every candidate index
	// estimates a single row, the optimizer breaks the tie arbitrarily, and the
	// EXPLAIN would say nothing about production — so the fixture is built
	// first, exactly like the manual MySQL/TiDB acceptance run.
	const history = 5000
	seedCapacityHistory(t, svc, provider, execution.SubmissionAccepted, execution.StatusSucceeded, history/2)
	seedCapacityHistory(t, svc, provider, execution.SubmissionUnknown, execution.StatusSucceeded, history/2)
	seedActiveCapacitySet(t, svc, provider)

	query := generatedQueryText(t, "countProviderEffectiveInflight")
	// EXPLAIN cannot carry placeholders, so they are bound to literals in
	// declaration order. Substituting a constant is what MySQL itself does
	// after prepare, and the provider predicate is an equality on a leading
	// index column in both forms, so the plan is representative.
	bound := bindLiterals(query, "'"+escapeSQLString(provider)+"'", "'"+escapeSQLString(provider)+"'")

	// MySQL reports the ALIAS in EXPLAIN's `table` column when a table is
	// aliased, so the plan is read back through the alias map parsed out of
	// the query itself — the test stays correct if the aliases are renamed,
	// and cannot silently match the wrong table.
	aliases := tableAliases(query)
	plan := explain(t, svc.DB, bound)
	if len(plan) == 0 {
		t.Fatal("EXPLAIN returned no rows")
	}

	// The whole plan is logged BEFORE any assertion: when a guard below fails,
	// the plan is the only thing anyone wants to see, and a t.Fatalf would
	// otherwise swallow it (复审报告 §二十 asks for rows/key/Extra in the log).
	for _, row := range plan {
		t.Logf("EXPLAIN id=%s select_type=%s table=%s type=%s possible_keys=%s key=%s key_len=%s rows=%s filtered=%s Extra=%s",
			row.values["id"], row.values["select_type"], row.values["table"], row.values["type"],
			row.values["possible_keys"], row.values["key"], row.values["key_len"],
			row.values["rows"], row.values["filtered"], row.values["extra"])
	}

	// Access methods are logged here too, with the leading column of whatever
	// index the optimizer picked — the shape the guards below assert on.
	leadingOf := map[string]string{}
	for _, row := range plan {
		tbl := aliases[row.values["table"]]
		if tbl != "runs" && tbl != "provider_submissions" {
			continue
		}
		key := row.values["key"]
		leading := indexLeadingColumn(t, svc.DB, tbl, key)
		if _, seen := leadingOf[tbl]; !seen {
			leadingOf[tbl] = leading
		}
		t.Logf("ACCESS %s (%s) type=%s key=%s leading_column=%q rows=%s filtered=%s Extra=%s",
			tbl, row.values["table"], row.values["type"], key, leading, row.values["rows"],
			row.values["filtered"], row.values["extra"])
	}

	// Join order over BASE tables only: <derived2> / <union1,2> / UNION RESULT
	// describe the derived table and the set operation, not an access path.
	var base []string
	unresolved := map[string]bool{}
	for _, row := range plan {
		raw := row.values["table"]
		if strings.HasPrefix(raw, "<") || strings.EqualFold(raw, "UNION RESULT") {
			continue
		}
		tbl, ok := aliases[raw]
		if !ok {
			unresolved[raw] = true
			tbl = raw
		}
		base = append(base, tbl)
	}
	if len(unresolved) > 0 {
		names := make([]string, 0, len(unresolved))
		for n := range unresolved {
			names = append(names, n)
		}
		sort.Strings(names)
		t.Fatalf("the plan reads table(s) %v that the query text does not declare as "+
			"FROM/JOIN tables; the join-order guard cannot interpret this plan", names)
	}
	runsAt, subsAt := -1, -1
	for i, tbl := range base {
		switch tbl {
		case "runs":
			if runsAt < 0 {
				runsAt = i
			}
		case "provider_submissions":
			if subsAt < 0 {
				subsAt = i
			}
		}
	}
	if runsAt < 0 {
		t.Fatalf("the remote capacity leg never reads `runs`; plan base tables = %v", base)
	}
	if subsAt < 0 {
		t.Fatalf("the remote capacity leg never reads `provider_submissions`; plan base tables = %v", base)
	}
	if runsAt > subsAt {
		t.Fatalf("the capacity plan is HISTORY-DRIVEN: provider_submissions is read before runs "+
			"(plan base tables = %v). provider_submissions is a lifetime ledger — driving from it "+
			"makes every admission walk the accepted-history range, which is what 3.3.1A removes.",
			base)
	}

	// The access methods are the other half: `STRAIGHT_JOIN` fixes WHO drives,
	// but each table must also be reached through an index that starts at the
	// restricted column — `status` for the active-run set and `run_id` for the
	// ledger lookup. An index whose leading column is only `provider` walks
	// every run/submission of the provider, i.e. the history range again, once
	// per outer row — which is why the leading column, and not the key's name,
	// is what is asserted.
	if leading := leadingOf["runs"]; leading != "status" {
		// idx_runs_claim(status, provider, queued_at) — the active-run index
		// the report names as the intended driving access.
		t.Errorf("runs is reached through an index whose leading column is %q, want \"status\": "+
			"the active-run set must be driven by idx_runs_claim, otherwise the plan walks "+
			"the provider's settled history", leading)
	}
	if leading := leadingOf["provider_submissions"]; leading != "run_id" {
		t.Errorf("provider_submissions is reached through an index whose leading column is %q, "+
			"want \"run_id\": the ledger must be looked up by run_id "+
			"(PRIMARY KEY (run_id, submission_no)); a `provider`-leading index reads every "+
			"submission the provider ever made", leading)
	}
}

// seedActiveCapacitySet creates the two live shapes the remote capacity leg
// must see, so the EXPLAIN fixture has an active workload next to the settled
// ledger: a run that owns a slot with an accepted submission, and a parked run
// whose submission outcome is unknown.
func seedActiveCapacitySet(t *testing.T, svc *execution.Service, provider string) {
	t.Helper()
	ctx := context.Background()
	slots := execution.NewProviderSlots(svc.DB, provider, 8, time.Minute)

	live := claimForSlots(t, svc, provider)
	if _, ok, _, err := slots.Acquire(ctx, live.Ownership); err != nil || !ok {
		t.Fatalf("acquire live run: ok=%v err=%v", ok, err)
	}
	liveSub := beginSubmission(t, svc, live.Ownership, provider)
	if err := svc.MarkSubmissionAcceptedOwned(ctx, live.Ownership, liveSub, "plan-chat-1"); err != nil {
		t.Fatalf("mark live accepted: %v", err)
	}

	parked := claimForSlots(t, svc, provider)
	parkedSub := beginSubmission(t, svc, parked.Ownership, provider)
	if err := svc.MarkSubmissionStateOwned(ctx, parked.Ownership, parkedSub, execution.SubmissionUnknown, "itest: plan unknown"); err != nil {
		t.Fatalf("mark parked unknown: %v", err)
	}
	if err := svc.AwaitExternalOwned(ctx, parked.Ownership, "itest: plan park"); err != nil {
		t.Fatalf("park run: %v", err)
	}
}

// indexLeadingColumn reports the first column of a named index, looked up in
// information_schema so the assertion is about the index SHAPE rather than a
// hardcoded key name (the optimizer is free to pick any suitable index, as
// long as it starts where the predicate does).
func indexLeadingColumn(t *testing.T, db *sql.DB, table, key string) string {
	t.Helper()
	if key == "" {
		return ""
	}
	var col string
	err := db.QueryRow(
		`SELECT COLUMN_NAME FROM information_schema.STATISTICS
		 WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND INDEX_NAME = ?
		 ORDER BY SEQ_IN_INDEX LIMIT 1`, table, key).Scan(&col)
	if err != nil {
		t.Fatalf("leading column of %s.%s: %v", table, key, err)
	}
	return col
}

// explainRow is one EXPLAIN row as a name→value map. Scanning by column NAME
// (rather than positionally) keeps the helper working across MySQL 5.7, 8.0
// and TiDB, whose EXPLAIN column sets differ.
type explainRow struct {
	values map[string]string
}

// explain runs `EXPLAIN <query>` and returns the plan in returned order, which
// is the access order MySQL/TiDB intends.
func explain(t *testing.T, db *sql.DB, query string) []explainRow {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), "EXPLAIN "+query)
	if err != nil {
		t.Fatalf("EXPLAIN capacity query: %v", err)
	}
	defer func() { _ = rows.Close() }()

	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("EXPLAIN columns: %v", err)
	}
	var out []explainRow
	for rows.Next() {
		raw := make([]any, len(cols))
		holders := make([]sql.RawBytes, len(cols))
		for i := range holders {
			raw[i] = &holders[i]
		}
		if err := rows.Scan(raw...); err != nil {
			t.Fatalf("scan EXPLAIN row: %v", err)
		}
		row := explainRow{values: make(map[string]string, len(cols))}
		for i, c := range cols {
			row.values[strings.ToLower(c)] = string(holders[i])
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("EXPLAIN rows: %v", err)
	}
	return out
}

// tableAliases maps every alias declared by `FROM`/`JOIN`/`STRAIGHT_JOIN` in a
// query to its real table name, so EXPLAIN output (which reports the alias) can
// be read as table names.
func tableAliases(query string) map[string]string {
	re := regexp.MustCompile(
		`(?i)(?:\bFROM\b|\bJOIN\b|STRAIGHT_JOIN)\s+([A-Za-z_][A-Za-z0-9_]*)\s+(?:AS\s+)?([A-Za-z_][A-Za-z0-9_]*)\b`)
	out := map[string]string{}
	for _, m := range re.FindAllStringSubmatch(query, -1) {
		out[m[2]] = m[1]
		// An unaliased table is also addressable by its own name.
		out[m[1]] = m[1]
	}
	return out
}

// generatedQueryText returns the exact SQL string sqlc emits for one named
// query — the text the driver sends, so the plan under test is the production
// plan and not a hand-copied approximation.
func generatedQueryText(t *testing.T, constName string) string {
	t.Helper()
	raw, err := os.ReadFile("../../internal/gen/db/execution.sql.go")
	if err != nil {
		t.Fatalf("read generated queries: %v", err)
	}
	marker := "const " + constName + " = `"
	src := string(raw)
	start := strings.Index(src, marker)
	if start < 0 {
		t.Fatalf("generated constant %s not found", constName)
	}
	body := src[start+len(marker):]
	end := strings.Index(body, "`")
	if end < 0 {
		t.Fatalf("generated constant %s is not terminated", constName)
	}
	text := body[:end]
	// The literal starts with the `-- name: ... :one` banner line that sqlc
	// keeps for traceability; EXPLAIN wants the statement.
	if nl := strings.Index(text, "\n"); nl >= 0 {
		text = text[nl+1:]
	}
	return strings.TrimSpace(text)
}

// bindLiterals replaces the `?` placeholders with already-quoted literals, in
// order. The capacity queries contain no `?` inside a string literal, so a
// positional scan is unambiguous.
func bindLiterals(query string, args ...string) string {
	var b strings.Builder
	next := 0
	for _, r := range query {
		if r == '?' && next < len(args) {
			b.WriteString(args[next])
			next++
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// escapeSQLString escapes the characters that matter for a single-quoted MySQL
// literal. Provider keys are generated internally, so this is belt and braces.
func escapeSQLString(s string) string {
	return strings.NewReplacer(`\`, `\\`, `'`, `''`).Replace(s)
}
