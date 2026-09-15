package integration

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/app"
	"github.com/creation-agent-studio/backend-go/internal/catalog"
	"github.com/creation-agent-studio/backend-go/internal/execution"
	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
)

// TestKilledRunHasNoStartedAt (第四轮 P2 §17): started_at means "this run
// was allowed to execute". A run cancelled by the execution gate never ran,
// so it must NOT carry a start time — before this change CASClaimRun
// stamped started_at at claim time, and a killed run advertised a start it
// never earned (and RunDuration recorded a bogus sample).
func TestKilledRunHasNoStartedAt(t *testing.T) {
	env := newScheduleEnv(t)
	rdb := testRedis(t)
	if rdb == nil {
		t.Skip("set STUDIO_TEST_REDIS=1 to run worker tests")
	}
	ctx := context.Background()
	provKey, appID, bindingID := gateFixture(t, env.db, "started-kill")

	runsSvc := execution.NewService(env.db, rdb, silentLogger(), telemetry.NewMetrics("test"))
	convID := seedConversation(t, runsSvc)
	run, err := runsSvc.CreateRun(ctx, &execution.CreateRunInput{
		UserID: 42, ApplicationID: appID, ConversationID: convID,
		RuntimeBindingID: bindingID, Provider: provKey,
		RuntimeType: "agent", ExecutionMode: "interactive",
		Content: "started_at kill check",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if _, err := env.db.ExecContext(ctx, `UPDATE applications SET enabled = 0 WHERE id = ?`, appID); err != nil {
		t.Fatalf("disable application: %v", err)
	}

	handler := &gateHandler{}
	worker := newSlotWorker(runsSvc, rdb, provKey, "started-kill-worker", handler, nil, 1)
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

	var started sql.NullTime
	if err := env.db.QueryRowContext(ctx,
		`SELECT started_at FROM runs WHERE id = ?`, run.ID.Bytes()).Scan(&started); err != nil {
		t.Fatalf("read started_at: %v", err)
	}
	if started.Valid {
		t.Fatalf("killed run has started_at=%v, want NULL (never allowed to execute)", started.Time)
	}
}

// TestAllowedRunStampsStartedAt: the mirror of the test above — a run the
// gate ALLOWED is stamped at the gate-allowed point, and the stamp is
// idempotent (COALESCE) so a deferred/re-claimed run keeps its FIRST start.
func TestAllowedRunStampsStartedAt(t *testing.T) {
	env := newScheduleEnv(t)
	rdb := testRedis(t)
	if rdb == nil {
		t.Skip("set STUDIO_TEST_REDIS=1 to run worker tests")
	}
	ctx := context.Background()
	provKey, appID, bindingID := gateFixture(t, env.db, "started-ok")

	runsSvc := execution.NewService(env.db, rdb, silentLogger(), telemetry.NewMetrics("test"))
	convID := seedConversation(t, runsSvc)
	run, err := runsSvc.CreateRun(ctx, &execution.CreateRunInput{
		UserID: 42, ApplicationID: appID, ConversationID: convID,
		RuntimeBindingID: bindingID, Provider: provKey,
		RuntimeType: "agent", ExecutionMode: "interactive",
		Content: "started_at allow check",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	handler := &gateHandler{}
	worker := newSlotWorker(runsSvc, rdb, provKey, "started-ok-worker", handler, nil, 1)
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

	var started sql.NullTime
	if err := env.db.QueryRowContext(ctx,
		`SELECT started_at FROM runs WHERE id = ?`, run.ID.Bytes()).Scan(&started); err != nil {
		t.Fatalf("read started_at: %v", err)
	}
	if !started.Valid {
		t.Fatal("allowed run has started_at NULL, want the gate-allowed timestamp")
	}

	// Idempotence: a later stamp (e.g. after a provider-pause deferral and
	// re-claim) must not overwrite the first start time.
	first := started.Time
	past := first.Add(-2 * time.Second)
	if _, err := env.db.ExecContext(ctx,
		`UPDATE runs SET started_at = ? WHERE id = ?`, past, run.ID.Bytes()); err != nil {
		t.Fatalf("seed earlier started_at: %v", err)
	}
	var epoch uint64
	if err := env.db.QueryRowContext(ctx,
		`SELECT lease_epoch FROM runs WHERE id = ?`, run.ID.Bytes()).Scan(&epoch); err != nil {
		t.Fatalf("read lease_epoch: %v", err)
	}
	if _, err := db.New(env.db).MarkRunStartedFenced(ctx, db.MarkRunStartedFencedParams{
		ID:         run.ID.Bytes(),
		LeaseEpoch: epoch,
	}); err != nil {
		t.Fatalf("re-stamp started_at: %v", err)
	}
	var after sql.NullTime
	if err := env.db.QueryRowContext(ctx,
		`SELECT started_at FROM runs WHERE id = ?`, run.ID.Bytes()).Scan(&after); err != nil {
		t.Fatalf("re-read started_at: %v", err)
	}
	if !after.Valid || !after.Time.Equal(past) {
		t.Fatalf("started_at after re-stamp = %v, want %v unchanged (COALESCE)", after, past)
	}
}
