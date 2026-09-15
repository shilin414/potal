// Review-round-2 fix regression tests (opt-in: STUDIO_TEST_DB=1), one per
// P1 the second review report asked to close before launch:
//
//	P1-1  Scheduler resolver must NOT swallow infrastructure errors —
//	      a transient DB failure retries the same slot on the next tick
//	      instead of permanently failing the occurrence.
//	P1-2  Execution-time kill switch: application/binding disabled
//	      cancels an already-queued run; provider inactive pauses it.
//	§七   Concurrent schedule PATCHes must not lose fields (row lock).
//	P1-3  The run-now pending queue is bounded (ErrPendingCapReached).
package integration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/app"
	"github.com/creation-agent-studio/backend-go/internal/automation/schedule"
	"github.com/creation-agent-studio/backend-go/internal/automation/scheduler"
	"github.com/creation-agent-studio/backend-go/internal/catalog"
	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/platform/config"
	"github.com/creation-agent-studio/backend-go/internal/platform/redisx"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
)

// ── P1-1: scheduler resolver error classification ──

// flakyResolver lets a test inject an infrastructure failure exactly
// where the old code swallowed it (any error → binding=nil, err=nil).
// It implements only EnabledBinding, so the scheduler takes the
// RuntimeResolver fallback path — the same classification applies on the
// owner-aware path (it shares authorizeForOwner + executionDenied,
// covered by internal/app unit tests).
type flakyResolver struct {
	binding *scheduler.BindingView
	err     error
}

func (r *flakyResolver) EnabledBinding(context.Context, int64) (*scheduler.BindingView, error) {
	return r.binding, r.err
}

func defaultBindingView() *scheduler.BindingView {
	return &scheduler.BindingView{
		ID: 1, ProviderKey: "feishu_aily", RuntimeType: "agent",
		ExecutionMode: "interactive", Snapshot: map[string]any{},
	}
}

// TestSchedulerAuthInfraErrorRetriesSlot: a resolver infrastructure error
// must ROLL BACK the tick (no occurrence, next_run_at untouched) so the
// same slot is retried; only a policy refusal records a failed occurrence
// and advances the schedule.
func TestSchedulerAuthInfraErrorRetriesSlot(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()
	due := time.Now().UTC().Add(-time.Second).Truncate(time.Millisecond)
	id := env.seedSchedule(t, due, schedule.OverlapQueue)

	nextRunAt := func() sql.NullTime {
		t.Helper()
		var nt sql.NullTime
		if err := env.db.QueryRowContext(ctx, `SELECT next_run_at FROM schedules WHERE id = ?`, id).Scan(&nt); err != nil {
			t.Fatalf("read next_run_at: %v", err)
		}
		return nt
	}

	// Phase 1: infrastructure failure — the slot must survive.
	env.schd.Binding = &flakyResolver{err: errors.New("itest: db connection pool exhausted")}
	before := nextRunAt()
	env.schd.ProcessDue(ctx)

	if n := env.count(t, `SELECT COUNT(*) FROM schedule_occurrences WHERE schedule_id = ?`, id); n != 0 {
		t.Fatalf("infra error produced %d occurrences, want 0 (tick must roll back)", n)
	}
	if n := env.count(t, `SELECT COUNT(*) FROM runs r JOIN schedule_occurrences o ON o.id = r.trigger_id
		WHERE o.schedule_id = ? AND r.trigger_type = 'scheduled'`, id); n != 0 {
		t.Fatalf("infra error produced %d runs, want 0", n)
	}
	after := nextRunAt()
	if !after.Valid || !after.Time.Equal(before.Time) {
		t.Fatalf("infra error advanced next_run_at %v → %v (slot lost)", before.Time, after.Time)
	}

	// Phase 2: the outage clears — the SAME slot fires normally.
	env.schd.Binding = &flakyResolver{binding: defaultBindingView()}
	env.schd.ProcessDue(ctx)
	if n := env.count(t, `SELECT COUNT(*) FROM schedule_occurrences WHERE schedule_id = ? AND status = 'queued'`, id); n != 1 {
		t.Fatalf("recovered tick produced %d queued occurrences, want 1", n)
	}
	if n := env.count(t, `SELECT COUNT(*) FROM schedule_occurrences WHERE schedule_id = ? AND status = 'failed'`, id); n != 0 {
		t.Fatalf("infra error must never record a failed occurrence, got %d", n)
	}

	// Phase 3 (control): a POLICY refusal still records the failure.
	env.schd.Binding = &flakyResolver{binding: nil}
	id2 := env.seedSchedule(t, time.Now().UTC().Add(-time.Second), schedule.OverlapQueue)
	env.schd.ProcessDue(ctx)
	if n := env.count(t, `SELECT COUNT(*) FROM schedule_occurrences WHERE schedule_id = ? AND status = 'failed'`, id2); n != 1 {
		t.Fatalf("policy refusal should record exactly one failed occurrence, got %d", n)
	}

	// Cleanup: the phase-1 invariant (next_run_at untouched on infra
	// error) leaves the slot due — soft-delete the fixtures so the shared
	// dev database does not accumulate schedules that error on every scan.
	if _, err := env.db.ExecContext(ctx,
		`UPDATE schedules SET enabled = 0, deleted_at = CURRENT_TIMESTAMP(3) WHERE id IN (?, ?)`, id, id2); err != nil {
		t.Fatalf("cleanup schedules: %v", err)
	}
}

// ── P1-2: execution-time kill switch ──

// gateHandler records whether the provider handler was ever reached.
type gateHandler struct{ calls atomic.Int64 }

func (h *gateHandler) Execute(context.Context, *execution.ClaimedRun) error {
	h.calls.Add(1)
	return nil
}

// testRedis opens Redis for worker tests; nil (test skipped upstream)
// when STUDIO_TEST_REDIS != 1.
func testRedis(t *testing.T) *redisx.Client {
	t.Helper()
	if os.Getenv("STUDIO_TEST_REDIS") != "1" {
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	rdb, err := redisx.Open(context.Background(), cfg.Redis)
	if err != nil {
		t.Fatalf("redis: %v", err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

// gateFixture seeds a unique provider+application+binding and returns the
// provider key, application id and binding id. Slug/provider keys carry a
// per-run suffix: applications.slug is UNIQUE and the suite may run
// repeatedly against the same dev database.
func gateFixture(t *testing.T, d *sql.DB, suffix string) (string, int64, int64) {
	t.Helper()
	uniq := fmt.Sprintf("%d", time.Now().UnixNano())
	provKey := "itest_gate_" + suffix + "_" + uniq
	provID := seedAuthzProvider(t, d, provKey, "active")
	appID := seedAuthzFixture(t, d, authzFixture{
		slug: "itest-gate-app-" + suffix + "-" + uniq, kind: "chat",
		isPublic: true, enabled: true, provider: provKey, providerID: provID,
		resource: "agent_gate_" + suffix,
	})
	var bindingID int64
	if err := d.QueryRow(`SELECT id FROM runtime_bindings WHERE application_id = ? ORDER BY id DESC LIMIT 1`, appID).Scan(&bindingID); err != nil {
		t.Fatalf("load binding: %v", err)
	}
	return provKey, appID, bindingID
}

// TestQueuedRunHonorsProviderKillSwitchKill: an application disabled AFTER
// its run was queued must be CANCELLED at claim time — never handed to the
// provider handler (P1-2, hard kill semantics).
func TestQueuedRunHonorsProviderKillSwitchKill(t *testing.T) {
	env := newScheduleEnv(t)
	rdb := testRedis(t)
	if rdb == nil {
		t.Skip("set STUDIO_TEST_REDIS=1 to run worker tests")
	}
	ctx := context.Background()
	provKey, appID, bindingID := gateFixture(t, env.db, "kill")

	runsSvc := execution.NewService(env.db, rdb, silentLogger(), telemetry.NewMetrics("test"))
	convID := seedConversation(t, runsSvc)
	run, err := runsSvc.CreateRun(ctx, &execution.CreateRunInput{
		UserID: 42, ApplicationID: appID, ConversationID: convID,
		RuntimeBindingID: bindingID, Provider: provKey,
		RuntimeType: "agent", ExecutionMode: "interactive",
		Content: "kill switch check",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	// Admin kills the application AFTER the run is queued — the admission
	// gate cannot see this run anymore, the execution-time gate must.
	if _, err := env.db.ExecContext(ctx, `UPDATE applications SET enabled = 0 WHERE id = ?`, appID); err != nil {
		t.Fatalf("disable application: %v", err)
	}

	handler := &gateHandler{}
	worker := newSlotWorker(runsSvc, rdb, provKey, "gate-kill-worker", handler, nil, 1)
	worker.Gate = &app.ExecutionGate{Catalog: &catalog.Service{DB: env.db}}
	worker.ScanEvery = 200 * time.Millisecond

	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() { worker.Run(wctx); close(done) }()

	waitUntil(t, "run cancelled by gate", 10*time.Second, func() bool {
		var status string
		if err := env.db.QueryRowContext(ctx, `SELECT status FROM runs WHERE id = ?`, run.ID.Bytes()).Scan(&status); err != nil {
			return false
		}
		return status == "cancelled"
	})
	cancel()
	<-done

	var code string
	if err := env.db.QueryRowContext(ctx,
		`SELECT error_code FROM runs WHERE id = ?`, run.ID.Bytes()).Scan(&code); err != nil {
		t.Fatalf("read cancelled run: %v", err)
	}
	if code != "execution_disabled" {
		t.Fatalf("cancelled run error_code=%q, want execution_disabled", code)
	}
	if handler.calls.Load() != 0 {
		t.Fatalf("handler executed %d times for a killed run, want 0", handler.calls.Load())
	}
}

// TestQueuedRunHonorsProviderKillSwitchPause: a provider deactivated AFTER
// its run was queued must PAUSE the run (requeue, keep waiting), not
// cancel it — reactivating the provider resumes the work untouched.
func TestQueuedRunHonorsProviderKillSwitchPause(t *testing.T) {
	env := newScheduleEnv(t)
	rdb := testRedis(t)
	if rdb == nil {
		t.Skip("set STUDIO_TEST_REDIS=1 to run worker tests")
	}
	ctx := context.Background()
	provKey, appID, bindingID := gateFixture(t, env.db, "pause")

	runsSvc := execution.NewService(env.db, rdb, silentLogger(), telemetry.NewMetrics("test"))
	convID := seedConversation(t, runsSvc)
	run, err := runsSvc.CreateRun(ctx, &execution.CreateRunInput{
		UserID: 42, ApplicationID: appID, ConversationID: convID,
		RuntimeBindingID: bindingID, Provider: provKey,
		RuntimeType: "agent", ExecutionMode: "interactive",
		Content: "pause switch check",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	if _, err := env.db.ExecContext(ctx, `UPDATE providers SET status = 'inactive' WHERE provider_key = ?`, provKey); err != nil {
		t.Fatalf("deactivate provider: %v", err)
	}

	handler := &gateHandler{}
	worker := newSlotWorker(runsSvc, rdb, provKey, "gate-pause-worker", handler, nil, 1)
	worker.Gate = &app.ExecutionGate{Catalog: &catalog.Service{DB: env.db}}
	worker.ScanEvery = 200 * time.Millisecond

	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() { worker.Run(wctx); close(done) }()

	waitUntil(t, "run requeued by gate", 10*time.Second, func() bool {
		var status string
		var avail sql.NullTime
		if err := env.db.QueryRowContext(ctx,
			`SELECT status, available_at FROM runs WHERE id = ?`, run.ID.Bytes()).Scan(&status, &avail); err != nil {
			return false
		}
		return status == "queued" && avail.Valid && avail.Time.After(time.Now().UTC().Add(10*time.Second))
	})
	// Prove the worker does NOT hot-loop into the handler while paused.
	time.Sleep(2 * time.Second)
	cancel()
	<-done

	if handler.calls.Load() != 0 {
		t.Fatalf("handler executed %d times for a paused run, want 0", handler.calls.Load())
	}
	var attempt int64
	if err := env.db.QueryRowContext(ctx, `SELECT attempt FROM runs WHERE id = ?`, run.ID.Bytes()).Scan(&attempt); err != nil {
		t.Fatalf("read attempt: %v", err)
	}
	if attempt != 0 {
		t.Fatalf("paused run consumed %d provider attempts, want 0 (pause must not burn budget)", attempt)
	}
}

// ── §七: concurrent schedule PATCH must not lose fields ──

// TestConcurrentSchedulePatchDoesNotLoseFields: two clients PATCH
// different fields of the same schedule concurrently; because the merge
// now happens on the row locked inside the transaction, the final row
// must contain BOTH changes (no lost update).
func TestConcurrentSchedulePatchDoesNotLoseFields(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()
	id := env.seedSchedule(t, time.Now().UTC().Add(time.Hour), schedule.OverlapQueue)

	prompt := "B-prompt"
	tz := "UTC"
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, errs[0] = env.svc.Update(ctx, id, 42, false, &schedule.UpdateInput{Prompt: &prompt})
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, errs[1] = env.svc.Update(ctx, id, 42, false, &schedule.UpdateInput{Timezone: &tz})
	}()
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("patch %d failed: %v", i, err)
		}
	}

	sch, err := env.svc.Get(ctx, id, 42, false)
	if err != nil {
		t.Fatalf("get after patches: %v", err)
	}
	if got, _ := sch.InputPayload["prompt"].(string); got != prompt {
		t.Fatalf("prompt=%q, want %q (lost update on concurrent PATCH)", got, prompt)
	}
	if sch.Timezone != tz {
		t.Fatalf("timezone=%q, want %q (lost update on concurrent PATCH)", sch.Timezone, tz)
	}
}

// ── P1-3: run-now pending queue bound ──

// TestRunNowPendingQueueHasBound: with overlap=queue, the FIRST run-now
// behind an active execution enqueues one pending occurrence; further
// run-nows are rejected with ErrPendingCapReached — a loop on /run-now
// can no longer grow the pending backlog (复审 P1-3), including under
// concurrency (the cap is checked under the schedules row lock).
func TestRunNowPendingQueueHasBound(t *testing.T) {
	env := newScheduleEnv(t)
	env.schd.MaxPendingManual = 1
	ctx := context.Background()
	// Due far in the future so the scan never interferes.
	id := env.seedSchedule(t, time.Now().UTC().Add(time.Hour), schedule.OverlapQueue)

	// First run-now creates the active (queued) run immediately.
	if _, err := env.schd.TriggerNow(ctx, id, 42, false); err != nil {
		t.Fatalf("first run-now: %v", err)
	}
	// Second run-now queues ONE pending occurrence (cap 1, 0 used).
	occ, err := env.schd.TriggerNow(ctx, id, 42, false)
	if err != nil {
		t.Fatalf("second run-now (first queued): %v", err)
	}
	if occ == nil || occ.Status != schedule.OccPending {
		t.Fatalf("second run-now status=%v, want pending", occ)
	}
	// A further sequential run-now hits the cap.
	if _, err := env.schd.TriggerNow(ctx, id, 42, false); !errors.Is(err, scheduler.ErrPendingCapReached) {
		t.Fatalf("third run-now err=%v, want ErrPendingCapReached", err)
	}

	// Concurrency check: parallel run-nows must leave exactly 1 pending —
	// the cap is enforced under the schedules row lock, so no racer can
	// observe a stale count and overshoot it.
	const burst = 6
	var capped atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := env.schd.TriggerNow(ctx, id, 42, false); errors.Is(err, scheduler.ErrPendingCapReached) {
				capped.Add(1)
			}
		}()
	}
	wg.Wait()
	pending := env.count(t, `SELECT COUNT(*) FROM schedule_occurrences WHERE schedule_id = ? AND status = 'pending'`, id)
	if pending != 1 {
		t.Fatalf("pending occurrences after burst=%d, want exactly 1 (cap overshoot)", pending)
	}
	if capped.Load() == 0 {
		t.Fatal("burst of run-nows: none reported the cap — the error is misclassified")
	}
	// Cleanup: keep the fixture out of the shared dev database's due scan.
	if _, err := env.db.ExecContext(ctx,
		`UPDATE schedules SET enabled = 0, deleted_at = CURRENT_TIMESTAMP(3) WHERE id = ?`, id); err != nil {
		t.Fatalf("cleanup schedule: %v", err)
	}
}

// ── §十二: concurrency boundaries for the two admission caps ──

// TestUserMaxOutstandingConcurrentBoundary (复审 §十二): the per-user
// outstanding cap is only a REAL bound if concurrent submits cannot
// overshoot it. The count and the insert share one transaction under the
// users row lock, so K is a hard ceiling — but that is exactly the kind of
// invariant a sequential test cannot prove (sequentially, "read then
// insert" and "lock, read, insert" behave identically).
//
// Every racer creates its OWN conversation (CreateConversation) so the
// only contested resource is the cap itself: a conversation-busy error
// would mask an overshoot and make the test pass for the wrong reason.
func TestUserMaxOutstandingConcurrentBoundary(t *testing.T) {
	env := newScheduleEnv(t)
	svc := execution.NewService(env.db, nil, silentLogger(), telemetry.NewMetrics("test"))
	ctx := context.Background()
	userID := seedUser(t, env.db)
	t.Cleanup(func() {
		_, _ = env.db.ExecContext(context.Background(),
			`UPDATE runs SET status = 'cancelled' WHERE user_id = ?`, userID)
	})

	const limit = 3
	const burst = 12

	var (
		wg         sync.WaitGroup
		admitted   atomic.Int64
		rejected   atomic.Int64
		mu         sync.Mutex
		unexpected []error
	)
	start := make(chan struct{})
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release every racer at once
			_, err := svc.CreateRunAdmitted(ctx, &execution.CreateRunInput{
				UserID:             userID,
				ApplicationID:      1,
				CreateConversation: true,
				Provider:           "itest_boundary",
				RuntimeType:        "agent",
				ExecutionMode:      "interactive",
				Content:            fmt.Sprintf("boundary turn %d", i),
			}, limit)
			switch {
			case err == nil:
				admitted.Add(1)
			case errors.Is(err, execution.ErrUserOutstandingExceeded):
				rejected.Add(1)
			default:
				mu.Lock()
				unexpected = append(unexpected, err)
				mu.Unlock()
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if len(unexpected) > 0 {
		t.Fatalf("unexpected errors from the racers: %v", unexpected)
	}
	if admitted.Load() != limit {
		t.Fatalf("admitted=%d, want exactly %d — the cap was overshot under concurrency", admitted.Load(), limit)
	}
	if rejected.Load() != burst-limit {
		t.Fatalf("rejected=%d, want %d (every loser must report the cap, not a silent failure)", rejected.Load(), burst-limit)
	}
	// The database is the authority: the cap bounds LIVE work, so the
	// non-terminal row count must match what the callers were told.
	live := env.count(t,
		`SELECT COUNT(*) FROM runs WHERE user_id = ? AND status NOT IN ('cancelled','succeeded','failed','interrupted')`, userID)
	if live != limit {
		t.Fatalf("live runs=%d, want %d", live, limit)
	}
}

// TestScheduleMaxConcurrentBoundary (复审 §十二): same invariant for the
// per-user schedule quota. Create counts under the users row lock inside
// the create transaction, so N concurrent creates must settle at exactly
// the cap rather than all observing the same pre-insert count.
func TestScheduleMaxConcurrentBoundary(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()
	userID := seedUser(t, env.db)
	t.Cleanup(func() {
		_, _ = env.db.ExecContext(context.Background(),
			`UPDATE schedules SET enabled = 0, deleted_at = CURRENT_TIMESTAMP(3) WHERE owner_user_id = ?`, userID)
	})

	const limit = 3
	const burst = 12
	env.svc.MaxSchedules = limit

	var (
		wg         sync.WaitGroup
		created    atomic.Int64
		quota      atomic.Int64
		mu         sync.Mutex
		unexpected []error
	)
	start := make(chan struct{})
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := env.svc.Create(ctx, userID, &schedule.CreateInput{
				Name:          fmt.Sprintf("boundary schedule %d", i),
				ApplicationID: 1,
				Prompt:        "boundary prompt",
				ScheduleType:  schedule.TypeDaily,
				TriggerConfig: schedule.TriggerConfig{Time: "09:00"},
				Timezone:      "Asia/Shanghai",
			})
			var ve *schedule.ValidationError
			switch {
			case err == nil:
				created.Add(1)
			case errors.As(err, &ve):
				quota.Add(1)
			default:
				mu.Lock()
				unexpected = append(unexpected, err)
				mu.Unlock()
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if len(unexpected) > 0 {
		t.Fatalf("unexpected errors from the racers: %v", unexpected)
	}
	if created.Load() != limit {
		t.Fatalf("created=%d, want exactly %d — the schedule quota was overshot under concurrency", created.Load(), limit)
	}
	if quota.Load() != burst-limit {
		t.Fatalf("quota rejections=%d, want %d", quota.Load(), burst-limit)
	}
	live := env.count(t,
		`SELECT COUNT(*) FROM schedules WHERE owner_user_id = ? AND deleted_at IS NULL`, userID)
	if live != limit {
		t.Fatalf("live schedules=%d, want %d", live, limit)
	}
}
