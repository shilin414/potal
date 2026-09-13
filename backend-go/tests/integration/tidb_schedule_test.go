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

// TestOverlapSkip: with an active occurrence, a skip policy records the
// slot as skipped instead of creating a second concurrent run.
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
