// Schedule automation integration tests (opt-in: STUDIO_TEST_TIDB=1).
// They prove the scheduler correctness core on real TiDB: the
// (schedule_id, scheduled_at) unique barrier, overlap/misfire policies,
// run-now and delivery fan-out idempotency.
package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/automation/schedule"
	"github.com/creation-agent-studio/backend-go/internal/automation/scheduler"
	"github.com/creation-agent-studio/backend-go/internal/delivery"
	"github.com/creation-agent-studio/backend-go/internal/execution"
	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/config"
	"github.com/creation-agent-studio/backend-go/internal/platform/database"
	"github.com/creation-agent-studio/backend-go/internal/platform/dbtypes"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
)

type scheduleEnv struct {
	db   *sql.DB
	svc  *schedule.Service
	schd *scheduler.Scheduler
}

func newScheduleEnv(t *testing.T) *scheduleEnv {
	t.Helper()
	if os.Getenv("STUDIO_TEST_TIDB") != "1" {
		t.Skip("set STUDIO_TEST_TIDB=1 to run TiDB integration tests")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	d, err := database.Open(context.Background(), cfg.Database)
	if err != nil {
		t.Fatalf("tidb: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	resolver := &fakeResolver{binding: &scheduler.BindingView{
		ID: 1, ProviderKey: "feishu_aily", RuntimeType: "agent",
		ExecutionMode: "interactive", Snapshot: map[string]any{},
	}}
	runsSvc := execution.NewService(d, nil, testLogger(), telemetry.NewMetrics("test"))
	svc := schedule.NewService(d, nil, testLogger())
	schd := scheduler.New(d, runsSvc, resolver, testLogger(), telemetry.NewMetrics("test"))
	return &scheduleEnv{db: d, svc: svc, schd: schd}
}

type fakeResolver struct{ binding *scheduler.BindingView }

func (r *fakeResolver) EnabledBinding(context.Context, int64) (*scheduler.BindingView, error) {
	return r.binding, nil
}

// seedSchedule inserts a due schedule fixture; nextRunAt = slot to fire.
func (e *scheduleEnv) seedSchedule(t *testing.T, nextRunAt time.Time, overlap string) int64 {
	t.Helper()
	ctx := context.Background()
	q := db.New(e.db)
	trigger, _ := json.Marshal(map[string]any{"time": "09:00"})
	payload, _ := json.Marshal(map[string]any{"prompt": "test prompt"})
	res, err := q.CreateSchedule(ctx, db.CreateScheduleParams{
		OwnerUserID:        42,
		Name:               "测试任务",
		ApplicationID:      1,
		InputPayload:       payload,
		ScheduleType:       schedule.TypeDaily,
		CronExpression:     "0 9 * * *",
		TriggerConfig:      trigger,
		Timezone:           "Asia/Shanghai",
		Enabled:            true,
		ConversationPolicy: schedule.ConversationNewEachRun,
		OverlapPolicy:      overlap,
		MisfirePolicy:      schedule.MisfireFireOnce,
		DeadlinePolicy:     schedule.DeadlineExecuteAnyway,
		NextRunAt:          sql.NullTime{Time: nextRunAt, Valid: true},
	})
	if err != nil {
		t.Fatalf("seed schedule: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func (e *scheduleEnv) count(t *testing.T, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := e.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

// TestDualSchedulerSingleOccurrence: two schedulers scanning concurrently
// must produce exactly one occurrence and one run per slot.
func TestDualSchedulerSingleOccurrence(t *testing.T) {
	env := newScheduleEnv(t)
	due := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	id := env.seedSchedule(t, due, schedule.OverlapQueue)

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			env.schd.ProcessDue(context.Background())
		}()
	}
	wg.Wait()

	if n := env.count(t, `SELECT COUNT(*) FROM schedule_occurrences WHERE schedule_id = ? AND scheduled_at = ?`, id, due); n != 1 {
		t.Fatalf("occurrences = %d, want 1", n)
	}
	if n := env.count(t, `SELECT COUNT(*) FROM runs r JOIN schedule_occurrences o ON r.id = o.run_id WHERE o.schedule_id = ?`, id); n != 1 {
		t.Fatalf("linked runs = %d, want 1", n)
	}
	if n := env.count(t, `SELECT COUNT(*) FROM runs WHERE trigger_type = 'scheduled' AND trigger_id IS NOT NULL`); n < 1 {
		t.Fatalf("scheduled run provenance missing")
	}
	// next_run_at advanced beyond the fired slot.
	var next sql.NullTime
	if err := env.db.QueryRow(`SELECT next_run_at FROM schedules WHERE id = ?`, id).Scan(&next); err != nil || !next.Valid {
		t.Fatalf("next_run_at missing: %v", err)
	}
	if !next.Time.After(due) {
		t.Fatalf("next_run_at = %v, want > %v", next.Time, due)
	}
}

// TestOverlapSkip: with an actively running occurrence, a skip policy
// records the slot as skipped instead of creating a second concurrent run.
// (The previous execution is seeded as 'running' — a 'pending' row would
// now be correctly admitted by the occurrence admission loop.)
func TestOverlapSkip(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()
	due := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	id := env.seedSchedule(t, due, schedule.OverlapSkip)
	q := db.New(env.db)

	// Simulate a still-running previous occurrence.
	if _, err := q.CreateScheduleOccurrence(ctx, db.CreateScheduleOccurrenceParams{
		ScheduleID: uint64(id), ScheduledAt: due.Add(-24 * time.Hour),
	}); err != nil {
		t.Fatalf("seed active occurrence: %v", err)
	}
	prev, err := q.GetScheduleOccurrenceBySlot(ctx, db.GetScheduleOccurrenceBySlotParams{
		ScheduleID: uint64(id), ScheduledAt: due.Add(-24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.MarkOccurrenceStatus(ctx, db.MarkOccurrenceStatusParams{
		Status: schedule.OccRunning, ID: prev.ID,
	}); err != nil {
		t.Fatalf("mark previous occurrence running: %v", err)
	}

	env.schd.ProcessDue(ctx)

	if n := env.count(t, `SELECT COUNT(*) FROM schedule_occurrences WHERE schedule_id = ? AND status = 'skipped'`, id); n != 1 {
		t.Fatalf("skipped occurrences = %d, want 1", n)
	}
	if n := env.count(t, `SELECT COUNT(*) FROM runs r JOIN schedule_occurrences o ON r.id = o.run_id WHERE o.schedule_id = ?`, id); n != 0 {
		t.Fatalf("runs = %d, want 0 under overlap skip", n)
	}
}

// TestRunNowLeavesNextRunAt: run-now creates a queued occurrence at now
// and keeps next_run_at untouched.
func TestRunNowLeavesNextRunAt(t *testing.T) {
	env := newScheduleEnv(t)
	next := time.Now().UTC().Add(10 * time.Hour)
	id := env.seedSchedule(t, next, schedule.OverlapQueue)

	occ, err := env.schd.TriggerNow(context.Background(), id, 42, false)
	if err != nil {
		t.Fatalf("TriggerNow: %v", err)
	}
	if occ.Status != schedule.OccQueued {
		t.Fatalf("occurrence status = %q", occ.Status)
	}
	if n := env.count(t, `SELECT COUNT(*) FROM runs r JOIN schedule_occurrences o ON r.id = o.run_id WHERE o.id = ?`, occ.ID); n != 1 {
		t.Fatalf("run-now produced %d runs, want 1", n)
	}
	var nextAfter sql.NullTime
	if err := env.db.QueryRow(`SELECT next_run_at FROM schedules WHERE id = ?`, id).Scan(&nextAfter); err != nil {
		t.Fatal(err)
	}
	if !nextAfter.Valid || !nextAfter.Time.After(time.Now().UTC().Add(9*time.Hour)) {
		t.Fatalf("next_run_at was modified: %v", nextAfter.Time)
	}
}

// TestDeliveryFanoutIdempotent: calling the dispatcher twice for the same
// run creates exactly one delivery execution.
func TestDeliveryFanoutIdempotent(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()
	q := db.New(env.db)

	scheduleID := env.seedSchedule(t, time.Now().UTC().Add(-time.Minute), schedule.OverlapQueue)
	due := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	if _, err := q.CreateScheduleOccurrence(ctx, db.CreateScheduleOccurrenceParams{
		ScheduleID: uint64(scheduleID), ScheduledAt: due,
	}); err != nil {
		t.Fatal(err)
	}
	occ, err := q.GetScheduleOccurrenceBySlot(ctx, db.GetScheduleOccurrenceBySlotParams{
		ScheduleID: uint64(scheduleID), ScheduledAt: due,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.UpsertScheduleDelivery(ctx, db.UpsertScheduleDeliveryParams{
		ScheduleID:         uint64(scheduleID),
		Channel:            "feishu",
		SenderIdentityMode: "owner_user",
		TargetType:         "user",
		TargetID:           "ou_test",
		TargetName:         "测试用户",
		ContentMode:        "summary",
		Enabled:            true,
	}); err != nil {
		t.Fatal(err)
	}

	runID := ids.New()
	input, _ := json.Marshal(map[string]any{"content": []map[string]any{{"type": "text", "text": "t"}}})
	if _, err := q.CreateRun(ctx, db.CreateRunParams{
		ID: runID.Bytes(), UserID: sql.NullInt64{Int64: 42, Valid: true},
		ApplicationID: sql.NullInt64{Int64: 1, Valid: true},
		Provider:      "feishu_aily", RuntimeType: "agent",
		Input:       dbtypes.JSONText(input),
		TriggerType: "scheduled",
		TriggerID:   sql.NullInt64{Int64: int64(occ.ID), Valid: true},
		Priority:    "scheduled_normal",
	}); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	disp := delivery.NewDispatcher(env.db, testLogger(), telemetry.NewMetrics("test"))
	disp.OnRunSucceeded(ctx, runID.String(), "scheduled", int64(occ.ID), 42, nil)
	disp.OnRunSucceeded(ctx, runID.String(), "scheduled", int64(occ.ID), 42, nil)

	if n := env.count(t, `SELECT COUNT(*) FROM delivery_executions WHERE occurrence_id = ?`, occ.ID); n != 1 {
		t.Fatalf("delivery executions = %d, want 1 (idempotent)", n)
	}
}

// setMisfire flips a seeded schedule's misfire policy.
func (e *scheduleEnv) setMisfire(t *testing.T, id int64, policy string) {
	t.Helper()
	if _, err := e.db.Exec(`UPDATE schedules SET misfire_policy = ? WHERE id = ?`, policy, id); err != nil {
		t.Fatalf("set misfire policy: %v", err)
	}
}

// TestMisfireFireOnceSingleCompensation (评测测试矩阵 #4): a daily
// schedule whose slot is 10 days overdue (scheduler downtime) with
// fire_once policy must create exactly ONE compensation run and jump
// next_run_at into the future — never replay slot by slot.
func TestMisfireFireOnceSingleCompensation(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()
	due := time.Now().UTC().Add(-10 * 24 * time.Hour).Truncate(time.Millisecond)
	id := env.seedSchedule(t, due, schedule.OverlapQueue) // default fire_once

	env.schd.ProcessDue(ctx)

	if n := env.count(t, `SELECT COUNT(*) FROM runs r JOIN schedule_occurrences o ON r.id = o.run_id WHERE o.schedule_id = ?`, id); n != 1 {
		t.Fatalf("runs = %d, want exactly 1 compensation run", n)
	}
	var next sql.NullTime
	if err := env.db.QueryRow(`SELECT next_run_at FROM schedules WHERE id = ?`, id).Scan(&next); err != nil || !next.Valid {
		t.Fatalf("next_run_at missing: %v", err)
	}
	if !next.Time.After(time.Now().UTC()) {
		t.Fatalf("next_run_at = %v, want in the future (fast-forwarded past 10 missed slots)", next.Time)
	}

	// The next scan must not fire anything more.
	env.schd.ProcessDue(ctx)
	if n := env.count(t, `SELECT COUNT(*) FROM runs r JOIN schedule_occurrences o ON r.id = o.run_id WHERE o.schedule_id = ?`, id); n != 1 {
		t.Fatalf("runs after second scan = %d, still want 1", n)
	}
}

// TestMisfireSkipFastForward: a misfired slot under skip policy creates no
// run and fast-forwards next_run_at into the future.
func TestMisfireSkipFastForward(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()
	due := time.Now().UTC().Add(-10 * 24 * time.Hour).Truncate(time.Millisecond)
	id := env.seedSchedule(t, due, schedule.OverlapQueue)
	env.setMisfire(t, id, schedule.MisfireSkip)

	env.schd.ProcessDue(ctx)

	if n := env.count(t, `SELECT COUNT(*) FROM runs r JOIN schedule_occurrences o ON r.id = o.run_id WHERE o.schedule_id = ?`, id); n != 0 {
		t.Fatalf("runs = %d, want 0 under misfire skip", n)
	}
	var next sql.NullTime
	if err := env.db.QueryRow(`SELECT next_run_at FROM schedules WHERE id = ?`, id).Scan(&next); err != nil || !next.Valid {
		t.Fatalf("next_run_at missing: %v", err)
	}
	if !next.Time.After(time.Now().UTC()) {
		t.Fatalf("next_run_at = %v, want fast-forwarded into the future", next.Time)
	}
}

// TestMisfireCatchUpOverLimit (评测测试矩阵 #5): catch_up with 30 missed
// slots (> MaxCatchUpSlots) fast-forwards instead of flooding the queue.
func TestMisfireCatchUpOverLimit(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()
	due := time.Now().UTC().Add(-30 * 24 * time.Hour).Truncate(time.Millisecond)
	id := env.seedSchedule(t, due, schedule.OverlapQueue)
	env.setMisfire(t, id, schedule.MisfireCatchUp)

	// Several scan ticks: even with catch_up the backlog cap must hold.
	for i := 0; i < 3; i++ {
		env.schd.ProcessDue(ctx)
	}
	if n := env.count(t, `SELECT COUNT(*) FROM runs r JOIN schedule_occurrences o ON r.id = o.run_id WHERE o.schedule_id = ?`, id); n != 0 {
		t.Fatalf("runs = %d, want 0 (30 missed slots exceed MaxCatchUpSlots=%d)", n, schedule.MaxCatchUpSlots)
	}
	var next sql.NullTime
	if err := env.db.QueryRow(`SELECT next_run_at FROM schedules WHERE id = ?`, id).Scan(&next); err != nil || !next.Valid || !next.Time.After(time.Now().UTC()) {
		t.Fatalf("next_run_at not fast-forwarded: %v", next)
	}
}

// TestMisfireCatchUpBoundedReplay: a small backlog (3 missed slots)
// replays one run per scan tick and converges into the future.
func TestMisfireCatchUpBoundedReplay(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()
	due := time.Now().UTC().Add(-3 * 24 * time.Hour).Truncate(time.Millisecond)
	id := env.seedSchedule(t, due, schedule.OverlapQueue)
	env.setMisfire(t, id, schedule.MisfireCatchUp)

	env.schd.ProcessDue(ctx)
	if n := env.count(t, `SELECT COUNT(*) FROM runs r JOIN schedule_occurrences o ON r.id = o.run_id WHERE o.schedule_id = ?`, id); n != 1 {
		t.Fatalf("runs after first tick = %d, want 1 (catch_up replays one at a time)", n)
	}
	// Keep ticking until the schedule catches up (bounded well below the cap).
	for i := 0; i < 10; i++ {
		var next sql.NullTime
		if err := env.db.QueryRow(`SELECT next_run_at FROM schedules WHERE id = ?`, id).Scan(&next); err != nil {
			t.Fatal(err)
		}
		if next.Valid && next.Time.After(time.Now().UTC()) {
			break
		}
		env.schd.ProcessDue(ctx)
	}
	if n := env.count(t, `SELECT COUNT(*) FROM runs r JOIN schedule_occurrences o ON r.id = o.run_id WHERE o.schedule_id = ?`, id); n > schedule.MaxCatchUpSlots {
		t.Fatalf("runs = %d, exceeded MaxCatchUpSlots=%d", n, schedule.MaxCatchUpSlots)
	}
}

// TestRunNowQueueBehindActive (评测测试矩阵 #6): TriggerNow under
// overlap=queue with an active execution must NOT create a parallel run —
// it enqueues a pending occurrence admitted only after the active one
// finishes.
func TestRunNowQueueBehindActive(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()
	q := db.New(env.db)
	next := time.Now().UTC().Add(10 * time.Hour)
	id := env.seedSchedule(t, next, schedule.OverlapQueue)

	// Active execution for the schedule.
	past := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	if _, err := q.CreateScheduleOccurrence(ctx, db.CreateScheduleOccurrenceParams{
		ScheduleID: uint64(id), ScheduledAt: past,
	}); err != nil {
		t.Fatal(err)
	}
	active, err := q.GetScheduleOccurrenceBySlot(ctx, db.GetScheduleOccurrenceBySlotParams{
		ScheduleID: uint64(id), ScheduledAt: past,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.MarkOccurrenceStatus(ctx, db.MarkOccurrenceStatusParams{Status: schedule.OccRunning, ID: active.ID}); err != nil {
		t.Fatal(err)
	}

	// Manual trigger while active: queued as pending, no run yet.
	occ, err := env.schd.TriggerNow(ctx, id, 42, false)
	if err != nil {
		t.Fatalf("TriggerNow: %v", err)
	}
	if occ.Status != schedule.OccPending {
		t.Fatalf("triggered occurrence status = %q, want pending", occ.Status)
	}
	if n := env.count(t, `SELECT COUNT(*) FROM runs r JOIN schedule_occurrences o ON r.id = o.run_id WHERE o.id = ?`, occ.ID); n != 0 {
		t.Fatalf("run-now created %d runs behind an active execution, want 0 (no parallel runs)", n)
	}

	// Admission tick while still active: still no run.
	env.schd.ProcessDue(ctx)
	if n := env.count(t, `SELECT COUNT(*) FROM runs r JOIN schedule_occurrences o ON r.id = o.run_id WHERE o.id = ?`, occ.ID); n != 0 {
		t.Fatalf("admitted pending occurrence behind active run, want to wait")
	}

	// The active execution finishes → admission converts pending → run.
	if _, err := q.MarkOccurrenceStatus(ctx, db.MarkOccurrenceStatusParams{Status: schedule.OccSucceeded, ID: active.ID}); err != nil {
		t.Fatal(err)
	}
	env.schd.ProcessDue(ctx)
	if n := env.count(t, `SELECT COUNT(*) FROM runs r JOIN schedule_occurrences o ON r.id = o.run_id WHERE o.id = ?`, occ.ID); n != 1 {
		t.Fatalf("pending occurrence not admitted after active finished: runs = %d, want 1", n)
	}
}
