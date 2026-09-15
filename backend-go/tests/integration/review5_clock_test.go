package integration

// 第五轮 P2-2: Clock Authority is only real if a FAILED clock read aborts
// the mutation. Before this round dbNow/dbNowTx silently fell back to the
// process clock, so a transient DB hiccup could write a next_run_at
// computed from one API node's local time into canonical state.
//
// These tests inject a failure into exactly ONE statement — the clock read
// (SELECT CURRENT_TIMESTAMP(3)) — while every other query keeps working,
// which is the only way to prove the fallback is gone: with a fully broken
// database the transaction would abort for unrelated reasons and the test
// would pass either way.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/automation/schedule"
	"github.com/creation-agent-studio/backend-go/internal/automation/scheduler"
	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/platform/config"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
	"github.com/go-sql-driver/mysql"
)

// clockReadSQL is the statement DBNow issues (db/queries/automation.sql).
const clockReadSQL = "CURRENT_TIMESTAMP(3)"

// clockFailConn rejects the clock read and delegates everything else to
// the real MySQL connection.
type clockFailConn struct{ driver.Conn }

func (c *clockFailConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, clockReadSQL) {
		return nil, errors.New("injected failure: db clock read")
	}
	q, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, errors.New("mysql conn does not implement QueryerContext")
	}
	return q.QueryContext(ctx, query, args)
}

type clockFailConnector struct{ inner driver.Connector }

func (w clockFailConnector) Connect(ctx context.Context) (driver.Conn, error) {
	cn, err := w.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &clockFailConn{Conn: cn}, nil
}

func (w clockFailConnector) Driver() driver.Driver { return w.inner.Driver() }

// openClockFailDB returns a *sql.DB that behaves like the real one except
// that the clock read always fails.
func openClockFailDB(t *testing.T) *sql.DB {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	mc, err := mysql.ParseDSN(cfg.Database.DSN())
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	conn, err := mysql.NewConnector(mc)
	if err != nil {
		t.Fatalf("mysql connector: %v", err)
	}
	d := sql.OpenDB(clockFailConnector{inner: conn})
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestScheduleCreateAbortsWhenDBClockUnavailable(t *testing.T) {
	env := newScheduleEnv(t)
	before := env.count(t, `SELECT COUNT(*) FROM schedules WHERE name = 'clock-fail-create'`)

	svc := schedule.NewService(openClockFailDB(t), nil, testLogger())
	runAt := time.Now().UTC().Add(time.Hour)
	_, err := svc.Create(context.Background(), 1, &schedule.CreateInput{
		Name:               "clock-fail-create",
		ApplicationID:      1,
		Prompt:             "clock authority",
		ScheduleType:       schedule.TypeOnce,
		RunAt:              &runAt,
		ConversationPolicy: schedule.ConversationNewEachRun,
		OverlapPolicy:      schedule.OverlapQueue,
		MisfirePolicy:      schedule.MisfireFireOnce,
		DeadlinePolicy:     schedule.DeadlineExecuteAnyway,
	})
	if !errors.Is(err, schedule.ErrDBClockUnavailable) {
		t.Fatalf("Create err = %v, want ErrDBClockUnavailable", err)
	}
	after := env.count(t, `SELECT COUNT(*) FROM schedules WHERE name = 'clock-fail-create'`)
	if after != before {
		t.Fatalf("schedules created = %d (before %d): a failed clock must abort the create", after-before, before)
	}
}

// The row must be untouched: no host-clock next_run_at may be written.
func TestScheduleUpdateRollsBackWhenDBClockUnavailable(t *testing.T) {
	env := newScheduleEnv(t)
	due := time.Now().UTC().Add(time.Hour).Truncate(time.Millisecond)
	id := env.seedSchedule(t, due, schedule.OverlapQueue)
	ctx := context.Background()

	var beforeNext sql.NullTime
	if err := env.db.QueryRowContext(ctx, `SELECT next_run_at FROM schedules WHERE id = ?`, id).
		Scan(&beforeNext); err != nil {
		t.Fatalf("read next_run_at: %v", err)
	}

	svc := schedule.NewService(openClockFailDB(t), nil, testLogger())
	if _, err := svc.Update(ctx, id, 42, true, &schedule.UpdateInput{}); err == nil {
		t.Fatal("Update succeeded: a failed clock read must abort the transaction, " +
			"never fall back to this host's clock")
	}

	var afterNext sql.NullTime
	if err := env.db.QueryRowContext(ctx, `SELECT next_run_at FROM schedules WHERE id = ?`, id).
		Scan(&afterNext); err != nil {
		t.Fatalf("re-read next_run_at: %v", err)
	}
	if afterNext.Time != beforeNext.Time || afterNext.Valid != beforeNext.Valid {
		t.Fatalf("next_run_at changed %v → %v: the update must roll back", beforeNext, afterNext)
	}
}

func TestScheduleSetEnabledRollsBackWhenDBClockUnavailable(t *testing.T) {
	env := newScheduleEnv(t)
	due := time.Now().UTC().Add(time.Hour).Truncate(time.Millisecond)
	id := env.seedSchedule(t, due, schedule.OverlapQueue)
	ctx := context.Background()

	// Disable first (no clock read on the disable path), then try to
	// re-enable against a broken clock: the re-enable computes next_run_at.
	if _, err := env.svc.SetEnabled(ctx, id, 42, true, false); err != nil {
		t.Fatalf("disable schedule: %v", err)
	}

	svc := schedule.NewService(openClockFailDB(t), nil, testLogger())
	if _, err := svc.SetEnabled(ctx, id, 42, true, true); err == nil {
		t.Fatal("SetEnabled(true) succeeded: a failed clock read must abort the " +
			"transaction instead of writing a host-clock next_run_at")
	}

	var enabled bool
	if err := env.db.QueryRowContext(ctx, `SELECT enabled FROM schedules WHERE id = ?`, id).
		Scan(&enabled); err != nil {
		t.Fatalf("read enabled: %v", err)
	}
	if enabled {
		t.Fatal("schedule is enabled after the aborted SetEnabled: the write must roll back")
	}
}

// A tick whose clock is unavailable must fire nothing — with the old
// fallback a skewed host would have decided due/misfire for everyone.
func TestSchedulerSkipsTickWhenDBClockUnavailable(t *testing.T) {
	env := newScheduleEnv(t)
	// A schedule that is unmistakably due.
	due := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	id := env.seedSchedule(t, due, schedule.OverlapQueue)
	ctx := context.Background()

	failDB := openClockFailDB(t)
	runs := execution.NewService(env.db, nil, testLogger(), telemetry.NewMetrics("test"))
	schd := scheduler.New(failDB, runs, &fakeResolver{binding: &scheduler.BindingView{
		ID: 1, ProviderKey: "feishu_aily", RuntimeType: "agent",
		ExecutionMode: "interactive", Snapshot: map[string]any{},
	}}, testLogger(), telemetry.NewMetrics("test"))

	schd.ProcessDue(ctx)

	n := env.count(t, `SELECT COUNT(*) FROM schedule_occurrences WHERE schedule_id = ?`, id)
	if n != 0 {
		t.Fatalf("occurrences = %d, want 0: a tick without a clock must be skipped, "+
			"not fired against the local clock", n)
	}
}
