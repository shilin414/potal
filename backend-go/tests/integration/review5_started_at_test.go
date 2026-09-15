package integration

// 第五轮 P2-4: started_at is written to the DB at gate-allow, but the
// worker's in-memory Run snapshot was loaded at CLAIM time — when
// started_at was still NULL — and was never refreshed. FinalizeOwnedRun
// only observes studio_run_duration when run.StartedAt != nil, so after
// the fourth round the metric silently stopped recording for every run.
//
// Fix: MarkRunStartedOwned returns the canonical DB timestamp and the
// worker copies it onto claimed.Run.

import (
	"context"
	"database/sql"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/app"
	"github.com/creation-agent-studio/backend-go/internal/catalog"
	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// startedCaptureHandler records whether the handler saw a Run snapshot
// that already carried its start time.
type startedCaptureHandler struct {
	calls      atomic.Int64
	sawStarted atomic.Bool
}

func (h *startedCaptureHandler) Execute(_ context.Context, claimed *execution.ClaimedRun) error {
	h.calls.Add(1)
	if claimed != nil && claimed.Run != nil &&
		claimed.Run.StartedAt != nil && !claimed.Run.StartedAt.IsZero() {
		h.sawStarted.Store(true)
	}
	return nil
}

// TestMarkRunStartedOwnedReturnsCanonicalDBTimestamp: the returned value
// must be the ROW's timestamp — not a locally generated one — because
// COALESCE may preserve a start from an earlier attempt.
func TestMarkRunStartedOwnedReturnsCanonicalDBTimestamp(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	userID := seedUser(t, env.db)
	t.Cleanup(func() {
		_, _ = env.db.ExecContext(context.Background(), `DELETE FROM runs WHERE user_id = ?`, userID)
		_, _ = env.db.ExecContext(context.Background(), `DELETE FROM users WHERE id = ?`, userID)
	})

	svc := execution.NewService(env.db, nil, silentLogger(), telemetry.NewMetrics("test"))
	run, err := svc.CreateRunAdmitted(ctx, &execution.CreateRunInput{
		UserID:             userID,
		ApplicationID:      1,
		CreateConversation: true,
		Provider:           "itest_started_at",
		RuntimeType:        "agent",
		ExecutionMode:      "interactive",
		Content:            "started_at return value",
	}, 10)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	claimed, won, err := svc.ClaimRun(ctx, run.ID, "itest-started-"+itoa(suffix), time.Minute)
	if err != nil || !won {
		t.Fatalf("claim run: won=%v err=%v", won, err)
	}

	got, err := svc.MarkRunStartedOwned(ctx, claimed.Ownership)
	if err != nil {
		t.Fatalf("MarkRunStartedOwned: %v", err)
	}
	if got.IsZero() {
		t.Fatal("MarkRunStartedOwned returned the zero time, want the DB timestamp")
	}

	var dbStarted sql.NullTime
	if err := env.db.QueryRowContext(ctx,
		`SELECT started_at FROM runs WHERE id = ?`, run.ID.Bytes()).Scan(&dbStarted); err != nil {
		t.Fatalf("read started_at: %v", err)
	}
	if !dbStarted.Valid {
		t.Fatal("started_at is NULL in the DB after MarkRunStartedOwned")
	}
	if !got.Equal(dbStarted.Time) {
		t.Fatalf("returned %v, DB row %v: the returned timestamp must be the DB's",
			got, dbStarted.Time)
	}

	// COALESCE: a second stamp keeps the FIRST start, and the return value
	// must reflect that (not "now").
	time.Sleep(5 * time.Millisecond)
	again, err := svc.MarkRunStartedOwned(ctx, claimed.Ownership)
	if err != nil {
		t.Fatalf("second MarkRunStartedOwned: %v", err)
	}
	if !again.Equal(dbStarted.Time) {
		t.Fatalf("second call returned %v, want the first start %v (COALESCE)",
			again, dbStarted.Time)
	}
}

// TestWorkerRefreshesStartedAtOnClaimedRun: the whole point of the round —
// the handler must see a Run snapshot that carries its start time, so
// FinalizeOwnedRun can observe studio_run_duration.
func TestWorkerRefreshesStartedAtOnClaimedRun(t *testing.T) {
	env := newScheduleEnv(t)
	rdb := testRedis(t)
	if rdb == nil {
		t.Skip("set STUDIO_TEST_REDIS=1 to run worker tests")
	}
	ctx := context.Background()
	provKey, appID, bindingID := gateFixture(t, env.db, "started-sync")

	runsSvc := execution.NewService(env.db, rdb, silentLogger(), telemetry.NewMetrics("test"))
	convID := seedConversation(t, runsSvc)
	if _, err := runsSvc.CreateRun(ctx, &execution.CreateRunInput{
		UserID: 42, ApplicationID: appID, ConversationID: convID,
		RuntimeBindingID: bindingID, Provider: provKey,
		RuntimeType: "agent", ExecutionMode: "interactive",
		Content: "started_at in-memory sync",
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}

	handler := &startedCaptureHandler{}
	worker := newSlotWorker(runsSvc, rdb, provKey, "started-sync-worker", handler, nil, 1)
	worker.Gate = &app.ExecutionGate{Catalog: &catalog.Service{DB: env.db}}
	worker.ScanEvery = 200 * time.Millisecond

	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() { worker.Run(wctx); close(done) }()

	waitUntil(t, "handler executed", 10*time.Second, func() bool {
		return handler.calls.Load() >= 1
	})
	cancel()
	<-done

	if !handler.sawStarted.Load() {
		t.Fatal("handler received a ClaimedRun with StartedAt == nil: " +
			"the in-memory snapshot was never refreshed, so studio_run_duration " +
			"records nothing for this run")
	}
}

// TestFinalizeObservesRunDurationForStartedRun: end-to-end proof of the
// metric — a finalized run whose snapshot carries started_at produces
// exactly one studio_run_execution_seconds observation.
func TestFinalizeObservesRunDurationForStartedRun(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()
	userID := seedUser(t, env.db)
	t.Cleanup(func() {
		_, _ = env.db.ExecContext(context.Background(), `DELETE FROM runs WHERE user_id = ?`, userID)
		_, _ = env.db.ExecContext(context.Background(), `DELETE FROM users WHERE id = ?`, userID)
	})

	const provider = "itest_duration"
	metrics := telemetry.NewMetrics("test")
	svc := execution.NewService(env.db, nil, silentLogger(), metrics)
	run, err := svc.CreateRunAdmitted(ctx, &execution.CreateRunInput{
		UserID:             userID,
		ApplicationID:      1,
		CreateConversation: true,
		Provider:           provider,
		RuntimeType:        "agent",
		ExecutionMode:      "interactive",
		Content:            "duration observation",
	}, 10)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	claimed, won, err := svc.ClaimRun(ctx, run.ID, "itest-duration", time.Minute)
	if err != nil || !won {
		t.Fatalf("claim run: won=%v err=%v", won, err)
	}
	startedAt, err := svc.MarkRunStartedOwned(ctx, claimed.Ownership)
	if err != nil {
		t.Fatalf("MarkRunStartedOwned: %v", err)
	}
	// Exactly what the worker now does.
	claimed.Run.StartedAt = &startedAt

	if err := svc.FinalizeOwnedRun(ctx, claimed.Run, claimed.Ownership, &execution.FinishInput{
		Status:        execution.StatusSucceeded,
		Output:        map[string]any{"text": "done"},
		AssistantText: "done",
	}); err != nil {
		t.Fatalf("FinalizeOwnedRun: %v", err)
	}

	var m dto.Metric
	obs := metrics.RunDuration.WithLabelValues(provider, execution.StatusSucceeded).(prometheus.Histogram)
	if err := obs.Write(&m); err != nil {
		t.Fatalf("read histogram: %v", err)
	}
	if got := m.GetHistogram().GetSampleCount(); got != 1 {
		t.Fatalf("studio_run_execution_seconds samples = %d, want 1 "+
			"(a started run must contribute exactly one observation)", got)
	}
}

// A run killed by the gate has no start time and must contribute nothing.
func TestFinalizeSkipsRunDurationForUnstartedRun(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()
	userID := seedUser(t, env.db)
	t.Cleanup(func() {
		_, _ = env.db.ExecContext(context.Background(), `DELETE FROM runs WHERE user_id = ?`, userID)
		_, _ = env.db.ExecContext(context.Background(), `DELETE FROM users WHERE id = ?`, userID)
	})

	const provider = "itest_duration_killed"
	metrics := telemetry.NewMetrics("test")
	svc := execution.NewService(env.db, nil, silentLogger(), metrics)
	run, err := svc.CreateRunAdmitted(ctx, &execution.CreateRunInput{
		UserID:             userID,
		ApplicationID:      1,
		CreateConversation: true,
		Provider:           provider,
		RuntimeType:        "agent",
		ExecutionMode:      "interactive",
		Content:            "no duration",
	}, 10)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	claimed, won, err := svc.ClaimRun(ctx, run.ID, "itest-duration-killed", time.Minute)
	if err != nil || !won {
		t.Fatalf("claim run: won=%v err=%v", won, err)
	}
	// No MarkRunStartedOwned: the gate killed the run before it was
	// allowed to execute.
	if err := svc.FinalizeOwnedRun(ctx, claimed.Run, claimed.Ownership, &execution.FinishInput{
		Status:    execution.StatusCancelled,
		ErrorCode: "execution_disabled",
		Output:    map[string]any{},
	}); err != nil {
		t.Fatalf("FinalizeOwnedRun: %v", err)
	}

	var m dto.Metric
	obs := metrics.RunDuration.WithLabelValues(provider, execution.StatusCancelled).(prometheus.Histogram)
	if err := obs.Write(&m); err != nil {
		t.Fatalf("read histogram: %v", err)
	}
	if got := m.GetHistogram().GetSampleCount(); got != 0 {
		t.Fatalf("studio_run_execution_seconds samples = %d, want 0 "+
			"(a run that never started has no duration)", got)
	}
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [24]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
