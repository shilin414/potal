// Review-round-3 fix regression tests (opt-in: STUDIO_TEST_DB=1,
// worker tests also STUDIO_TEST_REDIS=1), one per finding in the third
// review report:
//
//	P0-A  executionDenied(nil) inverted the classifier — authorized
//	      applications were treated as policy refusals, breaking every
//	      schedule create/update, automatic fire and run-now. The tests
//	      below pin the POSITIVE path with the REAL resolver chain
//	      (the round-2 suite only covered the failure paths).
//	P1-B  Pre-submit gate (Gate 2) inside the provider handler: a run
//	      disabled between the claim-time gate and the provider submit
//	      must still be killed / deferred.
//	P1-C  Gate pause defers with the ORIGINAL priority (no `retry`
//	      demotion) and converges the scheduled occurrence back to
//	      queued.
//	P2-D  run.started is only emitted AFTER the gate allows.
//	P2-E  An unknown GateAction fails closed.
package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/app"
	"github.com/creation-agent-studio/backend-go/internal/automation/schedule"
	"github.com/creation-agent-studio/backend-go/internal/catalog"
	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/identity"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
	"github.com/creation-agent-studio/backend-go/internal/platform/redisx"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
)

// ── shared round-3 helpers ──────────────────────────────────────────────

// seedUser inserts a minimal valid user (unique username) and returns id.
func seedUser(t *testing.T, d *sql.DB) int64 {
	t.Helper()
	uniq := fmt.Sprintf("%d", time.Now().UnixNano())
	res, err := d.Exec(`INSERT INTO users (username) VALUES (?)`, "itest_u3_"+uniq)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("user id: %v", err)
	}
	return id
}

// seedScheduleForApp is seedSchedule with an explicit application and
// owner: the REAL resolver chain authorizes the actual (app, owner) pair.
func (e *scheduleEnv) seedScheduleForApp(t *testing.T, nextRunAt time.Time, overlap string, appID, ownerID int64) int64 {
	t.Helper()
	ctx := context.Background()
	trigger, _ := json.Marshal(map[string]any{"time": "09:00"})
	payload, _ := json.Marshal(map[string]any{"prompt": "round3 prompt"})
	res, err := db.New(e.db).CreateSchedule(ctx, db.CreateScheduleParams{
		OwnerUserID:        uint64(ownerID),
		Name:               "round3 任务",
		ApplicationID:      uint64(appID),
		InputPayload:       payload,
		ScheduleType:       schedule.TypeDaily,
		CronExpression:     "0 9 * * *",
		TriggerConfig:      trigger,
		Timezone:           "UTC",
		Enabled:            true,
		ConversationPolicy: schedule.ConversationNewEachRun,
		OverlapPolicy:      overlap,
		MisfirePolicy:      schedule.MisfireFireOnce,
		DeadlinePolicy:     schedule.DeadlineExecuteAnyway,
		NextRunAt:          sql.NullTime{Time: nextRunAt, Valid: true},
	})
	if err != nil {
		t.Fatalf("seed schedule for app: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// runState is a compact projection of one run row for assertions.
type runState struct {
	status    string
	priority  string
	attempt   int64
	errorCode string
	avail     sql.NullTime
}

func readRunState(t *testing.T, d *sql.DB, runID ids.ID) runState {
	t.Helper()
	var s runState
	err := d.QueryRow(`SELECT status, priority, attempt, error_code, available_at
		FROM runs WHERE id = ?`, runID.Bytes()).
		Scan(&s.status, &s.priority, &s.attempt, &s.errorCode, &s.avail)
	if err != nil {
		t.Fatalf("read run %s: %v", runID.String(), err)
	}
	return s
}

func eventCount(t *testing.T, d *sql.DB, runID ids.ID, eventType string) int64 {
	t.Helper()
	var n int64
	if err := d.QueryRow(`SELECT COUNT(*) FROM run_events WHERE run_id = ? AND event_type = ?`,
		runID.Bytes(), eventType).Scan(&n); err != nil {
		t.Fatalf("count %s events: %v", eventType, err)
	}
	return n
}

func softDeleteSchedule(t *testing.T, d *sql.DB, id int64) {
	t.Helper()
	if _, err := d.Exec(`UPDATE schedules SET enabled = 0, deleted_at = CURRENT_TIMESTAMP(3) WHERE id = ?`, id); err != nil {
		t.Fatalf("soft-delete schedule %d: %v", id, err)
	}
}

// realResolver is intentionally unused — see realResolver usage inline;
// kept minimal to avoid dead code.

// ── P0-A: the positive path with the real resolver chain ───────────────

// TestEnabledBindingForValidOwnerReturnsBinding (第三轮 P0-A): a valid
// user + public enabled application + enabled binding + active provider
// MUST resolve a binding. The inverted executionDenied(nil) used to turn
// exactly this successful authorization into (nil, nil).
func TestEnabledBindingForValidOwnerReturnsBinding(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()
	_, appID, _ := gateFixture(t, env.db, "p0resolve")
	userID := seedUser(t, env.db)

	catalogSvc := &catalog.Service{DB: env.db}
	users := identity.NewRepo(env.db)
	res := app.NewBindingResolver(catalogSvc, users)

	binding, err := res.EnabledBindingFor(ctx, appID, userID)
	if err != nil {
		t.Fatalf("authorized owner got err=%v, want nil", err)
	}
	if binding == nil {
		t.Fatal("authorized owner got nil binding — executionDenied(nil) regression is back")
	}
	if binding.ProviderKey == "" || binding.ID == 0 {
		t.Fatalf("resolved binding is empty: %+v", binding)
	}
}

// TestSchedulableCheckerAllowsValidApplication (第三轮 P0-A): schedule
// create/update runs the same authorization — a valid application must be
// schedulable (nil), a disabled one must remain a policy denial.
func TestSchedulableCheckerAllowsValidApplication(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()
	provKey, appID, _ := gateFixture(t, env.db, "p0sched")
	userID := seedUser(t, env.db)

	catalogSvc := &catalog.Service{DB: env.db}
	users := identity.NewRepo(env.db)
	checker := app.NewSchedulableChecker(catalogSvc, users)

	if err := checker.SchedulableApplication(ctx, appID, userID); err != nil {
		t.Fatalf("valid application rejected: %v (executionDenied(nil) regression)", err)
	}

	// Control: a disabled application is still a POLICY denial.
	if _, err := env.db.ExecContext(ctx, `UPDATE applications SET enabled = 0 WHERE id = ?`, appID); err != nil {
		t.Fatalf("disable app: %v", err)
	}
	if err := checker.SchedulableApplication(ctx, appID, userID); !errors.Is(err, schedule.ErrApplicationNotSchedulable) {
		t.Fatalf("disabled application err=%v, want ErrApplicationNotSchedulable", err)
	}
	_ = provKey
}

// TestRealResolverAllowsScheduledFire (第三轮 P0-A, the key regression):
// REAL resolver → authorizeForOwner → executionDenied chain, REAL due
// schedule. ProcessDue must create 1 queued occurrence + 1 queued run and
// 0 failed occurrences — the inverted classifier failed every fire.
func TestRealResolverAllowsScheduledFire(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()
	_, appID, _ := gateFixture(t, env.db, "p0fire")
	userID := seedUser(t, env.db)

	// Swap the fake resolver for the REAL one.
	env.schd.Binding = app.NewBindingResolver(&catalog.Service{DB: env.db}, identity.NewRepo(env.db))

	due := time.Now().UTC().Add(-time.Second).Truncate(time.Millisecond)
	id := env.seedScheduleForApp(t, due, schedule.OverlapQueue, appID, userID)
	defer softDeleteSchedule(t, env.db, id)

	env.schd.ProcessDue(ctx)

	if n := env.count(t, `SELECT COUNT(*) FROM schedule_occurrences WHERE schedule_id = ?`, id); n != 1 {
		t.Fatalf("occurrences=%d, want 1", n)
	}
	if n := env.count(t, `SELECT COUNT(*) FROM schedule_occurrences WHERE schedule_id = ? AND status = 'queued'`, id); n != 1 {
		t.Fatalf("queued occurrences=%d, want 1 (the P0 turned every fire into 'failed')", n)
	}
	if n := env.count(t, `SELECT COUNT(*) FROM schedule_occurrences WHERE schedule_id = ? AND status = 'failed'`, id); n != 0 {
		t.Fatalf("failed occurrences=%d, want 0 (authorized fire must not be a policy denial)", n)
	}
	if n := env.count(t, `SELECT COUNT(*) FROM runs r JOIN schedule_occurrences o ON o.id = r.trigger_id
		WHERE o.schedule_id = ? AND r.trigger_type = 'scheduled' AND r.status = 'queued'`, id); n != 1 {
		t.Fatalf("queued scheduled runs=%d, want 1", n)
	}
}

// TestRealResolverAllowsTriggerNow (第三轮 P0-A): run-now uses the same
// owner-aware authorization — a valid application must actually enqueue.
func TestRealResolverAllowsTriggerNow(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()
	_, appID, _ := gateFixture(t, env.db, "p0now")
	userID := seedUser(t, env.db)

	env.schd.Binding = app.NewBindingResolver(&catalog.Service{DB: env.db}, identity.NewRepo(env.db))

	id := env.seedScheduleForApp(t, time.Now().UTC().Add(time.Hour), schedule.OverlapQueue, appID, userID)
	defer softDeleteSchedule(t, env.db, id)

	occ, err := env.schd.TriggerNow(ctx, id, userID, false)
	if err != nil {
		t.Fatalf("run-now on a valid application failed: %v (executionDenied(nil) regression)", err)
	}
	if occ == nil || occ.Status != schedule.OccQueued {
		t.Fatalf("run-now occurrence=%+v, want queued", occ)
	}
	if n := env.count(t, `SELECT COUNT(*) FROM schedule_occurrences WHERE schedule_id = ? AND run_id IS NOT NULL`, id); n != 1 {
		t.Fatalf("run-now occurrences with run=%d, want 1", n)
	}
}

// ── P1-C: pause defers with the original priority + occurrence sync ────

// TestProviderPausePreservesRunPriority (第三轮 P1-C): a gate pause must
// NOT demote the run into the retry class — the run goes back to queued
// with its original priority, a future available_at, no lease, no slot
// and a dispatch outbox row that keeps the original priority class.
func TestProviderPausePreservesRunPriority(t *testing.T) {
	env := newScheduleEnv(t)
	rdb := testRedis(t)
	ctx := context.Background()
	provKey, appID, bindingID := gateFixture(t, env.db, "deferprio")

	runsSvc := execution.NewService(env.db, rdb, silentLogger(), telemetry.NewMetrics("test"))
	convID := seedConversation(t, runsSvc)
	run, err := runsSvc.CreateRun(ctx, &execution.CreateRunInput{
		UserID: 42, ApplicationID: appID, ConversationID: convID,
		RuntimeBindingID: bindingID, Provider: provKey,
		RuntimeType: "agent", ExecutionMode: "interactive",
		Content: "defer priority check",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if run.Priority != execution.DefaultPriority {
		t.Fatalf("fixture priority=%q, want %q", run.Priority, execution.DefaultPriority)
	}

	claimed, won, err := runsSvc.ClaimRun(ctx, run.ID, "itest-defer", time.Minute)
	if err != nil || !won {
		t.Fatalf("claim: won=%v err=%v", won, err)
	}

	before := readRunState(t, env.db, run.ID)
	if before.status != "running" {
		t.Fatalf("claimed run status=%q, want running", before.status)
	}

	if err := runsSvc.DeferOwnedRunAfter(ctx, claimed.Run, claimed.Ownership, "provider_disabled", time.Second); err != nil {
		t.Fatalf("defer: %v", err)
	}

	after := readRunState(t, env.db, run.ID)
	if after.status != "queued" {
		t.Fatalf("deferred run status=%q, want queued", after.status)
	}
	if after.priority != before.priority {
		t.Fatalf("defer changed priority %q → %q (pause must preserve the business priority)", before.priority, after.priority)
	}
	if !after.avail.Valid || !after.avail.Time.After(time.Now().UTC()) {
		t.Fatalf("deferred run available_at=%v, want future", after.avail)
	}

	// Lease must be gone; the dispatch outbox row must keep the ORIGINAL
	// priority class (interactive), not `retry`.
	if n := env.count(t, `SELECT COUNT(*) FROM run_leases WHERE run_id = ?`, run.ID.Bytes()); n != 0 {
		t.Fatalf("deferred run still holds %d lease rows", n)
	}
	var payload []byte
	if err := env.db.QueryRow(`SELECT payload FROM outbox_events
		WHERE aggregate_id = ? AND event_type = 'run.dispatch' ORDER BY id DESC LIMIT 1`,
		run.ID.Bytes()).Scan(&payload); err != nil {
		t.Fatalf("read dispatch outbox: %v", err)
	}
	var p map[string]any
	if err := json.Unmarshal(payload, &p); err != nil {
		t.Fatalf("outbox payload: %v", err)
	}
	if got, _ := p["priority_class"].(string); got != execution.PriorityClassInteractive {
		t.Fatalf("deferred dispatch priority_class=%q, want %q (pause must not enter the retry stream)", got, execution.PriorityClassInteractive)
	}
	// run.deferred is non-terminal and carries the reason.
	if eventCount(t, env.db, run.ID, execution.EventRunDeferred) != 1 {
		t.Fatal("want exactly one run.deferred event")
	}
	if n := env.count(t, `SELECT COUNT(*) FROM provider_execution_slots WHERE run_id = ?`, run.ID.Bytes()); n != 0 {
		t.Fatalf("deferred run still holds %d provider slots", n)
	}
	_ = provKey
}

// TestProviderPauseRequeuesOccurrence (第三轮 §12): deferring a claimed
// scheduled run must converge its occurrence back to queued in the same
// breath — runs queued + occurrences running was the inconsistent state.
func TestProviderPauseRequeuesOccurrence(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()
	due := time.Now().UTC().Add(-time.Second).Truncate(time.Millisecond)
	id := env.seedSchedule(t, due, schedule.OverlapQueue)
	defer softDeleteSchedule(t, env.db, id)

	env.schd.ProcessDue(ctx)
	var runIDRaw []byte
	if err := env.db.QueryRow(`SELECT run_id FROM schedule_occurrences WHERE schedule_id = ? AND status = 'queued' LIMIT 1`, id).Scan(&runIDRaw); err != nil {
		t.Fatalf("queued occurrence: %v", err)
	}
	rid := ids.ID{}
	if err := rid.Scan(runIDRaw); err != nil {
		t.Fatalf("parse run id: %v", err)
	}

	runsSvc := execution.NewService(env.db, nil, silentLogger(), telemetry.NewMetrics("test"))
	claimed, won, err := runsSvc.ClaimRun(ctx, rid, "itest-defer-occ", time.Minute)
	if err != nil || !won {
		t.Fatalf("claim: won=%v err=%v", won, err)
	}
	if st := env.count(t, `SELECT COUNT(*) FROM schedule_occurrences WHERE run_id = ? AND status = 'running'`, rid.Bytes()); st != 1 {
		t.Fatalf("claimed occurrence running=%d, want 1 (claim fan-out)", st)
	}

	priorityBefore := readRunState(t, env.db, rid).priority
	if err := runsSvc.DeferOwnedRunAfter(ctx, claimed.Run, claimed.Ownership, "provider_disabled", time.Second); err != nil {
		t.Fatalf("defer: %v", err)
	}

	if st := env.count(t, `SELECT COUNT(*) FROM schedule_occurrences WHERE run_id = ? AND status = 'queued'`, rid.Bytes()); st != 1 {
		t.Fatalf("deferred occurrence queued=%d, want 1 (running→queued convergence)", st)
	}
	if st := env.count(t, `SELECT COUNT(*) FROM schedule_occurrences WHERE run_id = ? AND status = 'running'`, rid.Bytes()); st != 0 {
		t.Fatalf("deferred occurrence still running=%d, want 0", st)
	}
	after := readRunState(t, env.db, rid)
	if after.status != "queued" || after.priority != priorityBefore {
		t.Fatalf("deferred run status=%q priority=%q, want queued + unchanged %q", after.status, after.priority, priorityBefore)
	}
}

// ── P1-B: pre-submit gate (Gate 2) with a barrier ───────────────────────

// gate2Handler mimics the Aily executor's pre-submit section: Gate 1
// already allowed the run, the handler blocks on a barrier BEFORE the
// provider submit, then re-checks the gate via the REAL
// execution.PreSubmitGate — the exact code path the executor runs.
type gate2Handler struct {
	owned   *execution.WorkerOwnedService
	gate    execution.RunGate
	entered chan struct{}
	enter   sync.Once
	release chan struct{}
	submits atomic.Int64
}

func (h *gate2Handler) Execute(ctx context.Context, claimed *execution.ClaimedRun) error {
	h.enter.Do(func() { close(h.entered) })
	<-h.release // blocked between Gate 1 and the submit (auth/limiter wait)
	if execution.PreSubmitGate(ctx, h.owned, claimed, h.gate, silentLogger()) {
		return nil // gated: killed or deferred by Gate 2
	}
	h.submits.Add(1) // the provider would receive the request here
	return h.owned.Finalize(ctx, claimed, &execution.FinishInput{Status: execution.StatusSucceeded})
}

func newGate2Fixture(t *testing.T, suffix string) (*scheduleEnv, *redisx.Client, *execution.Service, ids.ID, string, int64) {
	t.Helper()
	env := newScheduleEnv(t)
	rdb := testRedis(t)
	if rdb == nil {
		t.Skip("set STUDIO_TEST_REDIS=1 to run worker tests")
	}
	ctx := context.Background()
	provKey, appID, bindingID := gateFixture(t, env.db, suffix)
	runsSvc := execution.NewService(env.db, rdb, silentLogger(), telemetry.NewMetrics("test"))
	convID := seedConversation(t, runsSvc)
	run, err := runsSvc.CreateRun(ctx, &execution.CreateRunInput{
		UserID: 42, ApplicationID: appID, ConversationID: convID,
		RuntimeBindingID: bindingID, Provider: provKey,
		RuntimeType: "agent", ExecutionMode: "interactive",
		Content: "gate2 " + suffix,
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	return env, rdb, runsSvc, run.ID, provKey, appID
}

// TestApplicationDisabledBetweenWorkerGateAndSubmitIsKilled (第三轮 P1-B):
// Gate 1 allowed the run, the admin disabled the application while the
// handler was waiting pre-submit, Gate 2 must cancel it — zero provider
// submits, zero consumed attempts.
func TestApplicationDisabledBetweenWorkerGateAndSubmitIsKilled(t *testing.T) {
	env, rdb, runsSvc, runID, provKey, appID := newGate2Fixture(t, "g2kill")
	ctx := context.Background()

	gate := &app.ExecutionGate{Catalog: &catalog.Service{DB: env.db}}
	handler := &gate2Handler{
		owned:   runsSvc.WorkerOwned(),
		gate:    gate,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	worker := newSlotWorker(runsSvc, rdb, provKey, "g2kill-worker", handler, nil, 1)
	worker.Gate = gate // Gate 1: enabled at claim time
	worker.ScanEvery = 200 * time.Millisecond

	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() { worker.Run(wctx); close(done) }()

	select {
	case <-handler.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("handler never reached the pre-submit section")
	}

	// The admin acts INSIDE the Gate-1 → submit window.
	if _, err := env.db.ExecContext(ctx, `UPDATE applications SET enabled = 0 WHERE id = ?`, appID); err != nil {
		t.Fatalf("disable application: %v", err)
	}
	close(handler.release)

	waitUntil(t, "run cancelled by pre-submit gate", 10*time.Second, func() bool {
		return readRunState(t, env.db, runID).status == "cancelled"
	})
	cancel()
	<-done

	st := readRunState(t, env.db, runID)
	if st.errorCode != "execution_disabled" {
		t.Fatalf("error_code=%q, want execution_disabled", st.errorCode)
	}
	if handler.submits.Load() != 0 {
		t.Fatalf("provider submitted %d times after kill, want 0", handler.submits.Load())
	}
	if st.attempt != 0 {
		t.Fatalf("killed run consumed %d attempts, want 0", st.attempt)
	}
}

// TestProviderDisabledBetweenWorkerGateAndSubmitIsDeferred (第三轮 P1-B):
// provider deactivated inside the pre-submit window → Gate 2 defers the
// run with its original priority; no submit, no attempt.
func TestProviderDisabledBetweenWorkerGateAndSubmitIsDeferred(t *testing.T) {
	env, rdb, runsSvc, runID, provKey, _ := newGate2Fixture(t, "g2pause")
	ctx := context.Background()

	gate := &app.ExecutionGate{Catalog: &catalog.Service{DB: env.db}}
	handler := &gate2Handler{
		owned:   runsSvc.WorkerOwned(),
		gate:    gate,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	worker := newSlotWorker(runsSvc, rdb, provKey, "g2pause-worker", handler, nil, 1)
	worker.Gate = gate
	worker.ScanEvery = 200 * time.Millisecond

	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() { worker.Run(wctx); close(done) }()

	select {
	case <-handler.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("handler never reached the pre-submit section")
	}

	if _, err := env.db.ExecContext(ctx, `UPDATE providers SET status = 'inactive' WHERE provider_key = ?`, provKey); err != nil {
		t.Fatalf("deactivate provider: %v", err)
	}
	close(handler.release)

	waitUntil(t, "run deferred by pre-submit gate", 10*time.Second, func() bool {
		st := readRunState(t, env.db, runID)
		return st.status == "queued" && st.avail.Valid && st.avail.Time.After(time.Now().UTC().Add(10*time.Second))
	})
	cancel()
	<-done

	st := readRunState(t, env.db, runID)
	if st.priority != execution.DefaultPriority {
		t.Fatalf("deferred run priority=%q, want unchanged %q", st.priority, execution.DefaultPriority)
	}
	if handler.submits.Load() != 0 {
		t.Fatalf("provider submitted %d times while paused, want 0", handler.submits.Load())
	}
	if st.attempt != 0 {
		t.Fatalf("deferred run consumed %d attempts, want 0", st.attempt)
	}
}

// ── P2-D: run.started only after the gate allows ────────────────────────

// TestGateKillDoesNotEmitRunStarted (第三轮 P2-D): a run killed at claim
// time goes queued → cancelled with NO run.started — the provider slot,
// the handler and the fake lifecycle event never happen.
func TestGateKillDoesNotEmitRunStarted(t *testing.T) {
	env, rdb, runsSvc, runID, provKey, appID := newGate2Fixture(t, "evkill")
	ctx := context.Background()

	if _, err := env.db.ExecContext(ctx, `UPDATE applications SET enabled = 0 WHERE id = ?`, appID); err != nil {
		t.Fatalf("disable application: %v", err)
	}

	handler := &gateHandler{}
	worker := newSlotWorker(runsSvc, rdb, provKey, "evkill-worker", handler, nil, 1)
	worker.Gate = &app.ExecutionGate{Catalog: &catalog.Service{DB: env.db}}
	worker.ScanEvery = 200 * time.Millisecond

	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() { worker.Run(wctx); close(done) }()

	waitUntil(t, "run cancelled", 10*time.Second, func() bool {
		return readRunState(t, env.db, runID).status == "cancelled"
	})
	// Give a duplicate dispatch no chance to sneak a started in.
	time.Sleep(500 * time.Millisecond)
	cancel()
	<-done

	if n := eventCount(t, env.db, runID, execution.EventRunStarted); n != 0 {
		t.Fatalf("killed run emitted %d run.started events, want 0", n)
	}
	if n := eventCount(t, env.db, runID, execution.EventRunCancelled); n != 1 {
		t.Fatalf("killed run emitted %d run.cancelled events, want 1", n)
	}
	if handler.calls.Load() != 0 {
		t.Fatalf("handler executed %d times, want 0", handler.calls.Load())
	}
}

// TestGatePauseDoesNotEmitRepeatedRunStarted (第三轮 P2-D): a provider
// paused at claim time defers WITHOUT started/retrying churn — exactly
// one run.deferred, zero run.started.
func TestGatePauseDoesNotEmitRepeatedRunStarted(t *testing.T) {
	env, rdb, runsSvc, runID, provKey, _ := newGate2Fixture(t, "evpause")
	ctx := context.Background()

	if _, err := env.db.ExecContext(ctx, `UPDATE providers SET status = 'inactive' WHERE provider_key = ?`, provKey); err != nil {
		t.Fatalf("deactivate provider: %v", err)
	}

	handler := &gateHandler{}
	worker := newSlotWorker(runsSvc, rdb, provKey, "evpause-worker", handler, nil, 1)
	worker.Gate = &app.ExecutionGate{Catalog: &catalog.Service{DB: env.db}}
	worker.ScanEvery = 200 * time.Millisecond

	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() { worker.Run(wctx); close(done) }()

	waitUntil(t, "run deferred", 10*time.Second, func() bool {
		st := readRunState(t, env.db, runID)
		return st.status == "queued" && st.avail.Valid && st.avail.Time.After(time.Now().UTC().Add(10*time.Second))
	})
	// The pause backoff is 30s: within this window no re-claim can occur,
	// so a second deferred (or any started) would mean a hot loop.
	time.Sleep(2 * time.Second)
	cancel()
	<-done

	if n := eventCount(t, env.db, runID, execution.EventRunStarted); n != 0 {
		t.Fatalf("paused run emitted %d run.started events, want 0", n)
	}
	if n := eventCount(t, env.db, runID, execution.EventRunRetrying); n != 0 {
		t.Fatalf("paused run emitted %d run.retrying events, want 0 (defer, not retry)", n)
	}
	if n := eventCount(t, env.db, runID, execution.EventRunDeferred); n != 1 {
		t.Fatalf("paused run emitted %d run.deferred events, want exactly 1 (no hot loop)", n)
	}
	if handler.calls.Load() != 0 {
		t.Fatalf("handler executed %d times, want 0", handler.calls.Load())
	}
}

// ── P2-E: unknown GateAction fails closed ───────────────────────────────

// fixedGate returns a canned verdict — used to simulate a broken gate.
type fixedGate struct {
	action execution.GateAction
	err    error
}

func (g *fixedGate) CheckRun(context.Context, *execution.Run) (execution.GateAction, error) {
	return g.action, g.err
}

// TestUnknownGateActionFailsClosed (第三轮 P2-E): a gate that returns an
// unknown action must NEVER let the run reach the provider — the run is
// deferred (fail closed), no handler call, no run.started.
func TestUnknownGateActionFailsClosed(t *testing.T) {
	env, rdb, runsSvc, runID, provKey, _ := newGate2Fixture(t, "g2unknown")
	ctx := context.Background()

	handler := &gateHandler{}
	worker := newSlotWorker(runsSvc, rdb, provKey, "g2unknown-worker", handler, nil, 1)
	worker.Gate = &fixedGate{action: execution.GateAction("bogus")}
	worker.ScanEvery = 200 * time.Millisecond

	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() { worker.Run(wctx); close(done) }()

	waitUntil(t, "run fail-closed deferred", 10*time.Second, func() bool {
		st := readRunState(t, env.db, runID)
		return st.status == "queued" && st.avail.Valid && st.avail.Time.After(time.Now().UTC())
	})
	// One defer cycle (1s backoff) may repeat — the invariant is that the
	// handler is NEVER reached and no started event is ever emitted.
	time.Sleep(1500 * time.Millisecond)
	cancel()
	<-done

	if handler.calls.Load() != 0 {
		t.Fatalf("handler executed %d times on an unknown gate action, want 0 (fail-open)", handler.calls.Load())
	}
	if n := eventCount(t, env.db, runID, execution.EventRunStarted); n != 0 {
		t.Fatalf("fail-closed run emitted %d run.started events, want 0", n)
	}
	st := readRunState(t, env.db, runID)
	if st.status != "queued" || st.attempt != 0 {
		t.Fatalf("fail-closed run status=%q attempt=%d, want queued/0", st.status, st.attempt)
	}
}
