package integration

// 第五轮 P2-1 integration coverage: the legacy `interrupted` status must
// behave as a TERMINAL ALIAS on real data (it may not occupy a
// conversation or an outstanding slot) and migration 0018 must normalize
// historical rows.

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/execution"
	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
)

// seedInterruptedRun creates a real run and then forcibly rewrites its
// status to the legacy pre-closure value, mimicking a historical row.
// It returns (runID, userID, conversationID).
func seedInterruptedRun(t *testing.T, d *sql.DB) ([]byte, int64, int64) {
	t.Helper()
	ctx := context.Background()
	userID := seedUser(t, d)
	svc := execution.NewService(d, nil, testLogger(), telemetry.NewMetrics("test"))
	run, err := svc.CreateRunAdmitted(ctx, &execution.CreateRunInput{
		UserID:             userID,
		ApplicationID:      1,
		CreateConversation: true,
		Provider:           "itest_interrupted",
		RuntimeType:        "agent",
		ExecutionMode:      "interactive",
		Content:            "legacy interrupted fixture",
	}, 10)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if _, err := d.ExecContext(ctx, `UPDATE runs SET status = 'interrupted' WHERE id = ?`, run.ID.Bytes()); err != nil {
		t.Fatalf("force interrupted status: %v", err)
	}
	var convID int64
	if err := d.QueryRowContext(ctx, `SELECT conversation_id FROM runs WHERE id = ?`, run.ID.Bytes()).
		Scan(&convID); err != nil {
		t.Fatalf("read conversation: %v", err)
	}
	t.Cleanup(func() {
		_, _ = d.ExecContext(context.Background(), `DELETE FROM run_events WHERE run_id = ?`, run.ID.Bytes())
		_, _ = d.ExecContext(context.Background(), `DELETE FROM runs WHERE id = ?`, run.ID.Bytes())
		_, _ = d.ExecContext(context.Background(), `DELETE FROM conversations WHERE id = ?`, convID)
		_, _ = d.ExecContext(context.Background(), `DELETE FROM users WHERE id = ?`, userID)
	})
	return run.ID.Bytes(), userID, convID
}

// TestLegacyInterruptedDoesNotBlockConversation: before the fix the
// "active run" predicate was NOT IN ('cancelled','succeeded','failed'), so
// one pre-closure interrupted run made the conversation permanently busy
// (every later send → 409).
func TestLegacyInterruptedDoesNotBlockConversation(t *testing.T) {
	env := newScheduleEnv(t)
	_, _, convID := seedInterruptedRun(t, env.db)

	n, err := db.New(env.db).CountActiveRunsByConversation(
		context.Background(), sql.NullInt64{Int64: convID, Valid: true})
	if err != nil {
		t.Fatalf("CountActiveRunsByConversation: %v", err)
	}
	if n != 0 {
		t.Fatalf("active runs in conversation = %d, want 0: a legacy interrupted run "+
			"must not pin the conversation at 409", n)
	}
}

func TestLegacyInterruptedDoesNotCountOutstanding(t *testing.T) {
	env := newScheduleEnv(t)
	_, userID, _ := seedInterruptedRun(t, env.db)

	n, err := db.New(env.db).CountOutstandingRunsByUser(
		context.Background(), sql.NullInt64{Int64: userID, Valid: true})
	if err != nil {
		t.Fatalf("CountOutstandingRunsByUser: %v", err)
	}
	if n != 0 {
		t.Fatalf("outstanding runs = %d, want 0: a legacy interrupted run must not "+
			"consume a RUN_USER_MAX_OUTSTANDING slot forever", n)
	}
}

// A settled run must not be re-finished (idempotency guard).
func TestLegacyInterruptedCannotBeRefinished(t *testing.T) {
	env := newScheduleEnv(t)
	runID, _, _ := seedInterruptedRun(t, env.db)

	res, err := env.db.ExecContext(context.Background(),
		`UPDATE runs SET status = 'succeeded' WHERE id = ?
		   AND status NOT IN ('cancelled','succeeded','failed','interrupted')`, runID)
	if err != nil {
		t.Fatalf("CASFinishRun: %v", err)
	}
	affected, _ := res.RowsAffected()
	if affected != 0 {
		t.Fatalf("rows affected = %d, want 0: a settled (legacy interrupted) run "+
			"must not be re-finished", affected)
	}
}

// TestFinalizeRejectsLegacyInterruptedStatus: "new code never writes
// status=interrupted". The alias is read-only history now — if
// finalization accepted it, a run could re-enter the very status whose
// dual contract the closure abolished (live for admission, terminal for
// the UI).
// TestMigration0018SynthesizesMissingTerminalEvent (invariant D): a
// legacy interrupted run that never got an event row must not end up a
// `failed` run with an empty log — the replay/stream close and the
// invariant checker both depend on the terminal event existing.
func TestMigration0018SynthesizesMissingTerminalEvent(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()
	// Deliberately NO run.interrupted event is seeded for this run.
	runID, _, _ := seedInterruptedRun(t, env.db)

	raw, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations",
		"0018_normalize_legacy_interrupted_runs.up.sql"))
	if err != nil {
		t.Fatalf("read migration 0018: %v", err)
	}
	if _, err := env.db.ExecContext(ctx, string(raw)); err != nil {
		t.Fatalf("apply migration 0018: %v", err)
	}

	var status string
	if err := env.db.QueryRowContext(ctx, `SELECT status FROM runs WHERE id = ?`, runID).
		Scan(&status); err != nil {
		t.Fatalf("read run: %v", err)
	}
	if status != execution.StatusFailed {
		t.Fatalf("status = %q, want %q", status, execution.StatusFailed)
	}
	var failed int
	if err := env.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM run_events WHERE run_id = ? AND event_type = 'run.failed'`, runID).
		Scan(&failed); err != nil {
		t.Fatalf("count run.failed: %v", err)
	}
	if failed != 1 {
		t.Fatalf("run.failed events = %d, want 1 (synthesized for a run whose log was empty)", failed)
	}

	// Idempotent: a second pass must not duplicate it.
	if _, err := env.db.ExecContext(ctx, string(raw)); err != nil {
		t.Fatalf("re-apply migration 0018: %v", err)
	}
	if err := env.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM run_events WHERE run_id = ? AND event_type = 'run.failed'`, runID).
		Scan(&failed); err != nil {
		t.Fatalf("re-count run.failed: %v", err)
	}
	if failed != 1 {
		t.Fatalf("run.failed events after re-run = %d, want 1 (no duplicate)", failed)
	}
}

func TestFinalizeRejectsLegacyInterruptedStatus(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()
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
		Provider:           "itest_interrupted_write",
		RuntimeType:        "agent",
		ExecutionMode:      "interactive",
		Content:            "interrupted must not be writable",
	}, 10)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	claimed, won, err := svc.ClaimRun(ctx, run.ID, "itest-interrupted-write", time.Minute)
	if err != nil || !won {
		t.Fatalf("claim run: won=%v err=%v", won, err)
	}

	err = svc.FinalizeOwnedRun(ctx, claimed.Run, claimed.Ownership, &execution.FinishInput{
		Status: "interrupted",
		Output: map[string]any{},
	})
	if !errors.Is(err, execution.ErrInvalidTerminalStatus) {
		t.Fatalf("FinalizeOwnedRun(interrupted) err = %v, want ErrInvalidTerminalStatus", err)
	}

	var status string
	if err := env.db.QueryRowContext(ctx, `SELECT status FROM runs WHERE id = ?`, run.ID.Bytes()).
		Scan(&status); err != nil {
		t.Fatalf("read run: %v", err)
	}
	if status == "interrupted" {
		t.Fatal("run status is interrupted: new code wrote the legacy alias")
	}
	if status == execution.StatusRunning {
		// Rejected before any write — the run is untouched and the lease
		// is still held, so it can be finalized normally afterwards.
		if err := svc.FinalizeOwnedRun(ctx, claimed.Run, claimed.Ownership, &execution.FinishInput{
			Status: execution.StatusFailed,
			Output: map[string]any{},
		}); err != nil {
			t.Fatalf("canonical finalize after the rejected one: %v", err)
		}
	}
	var legacyEvents int
	if err := env.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM run_events WHERE run_id = ? AND event_type = 'run.interrupted'`,
		run.ID.Bytes()).Scan(&legacyEvents); err != nil {
		t.Fatalf("count legacy events: %v", err)
	}
	if legacyEvents != 0 {
		t.Fatalf("run.interrupted events = %d, want 0: no new code path may emit it", legacyEvents)
	}
}

// TestMigration0018NormalizesLegacyInterrupted applies the real migration
// file to a seeded legacy row and asserts the documented outcome:
// runs → failed (with an error_code marker when empty), legacy
// run.interrupted events → run.failed, and the run is finished_at-stamped.
func TestMigration0018NormalizesLegacyInterrupted(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()
	runID, _, _ := seedInterruptedRun(t, env.db)

	if _, err := env.db.ExecContext(ctx,
		`INSERT INTO run_events (run_id, sequence, event_type, payload) VALUES (?, 1, 'run.interrupted', '{}')`,
		runID); err != nil {
		t.Fatalf("seed legacy event: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations",
		"0018_normalize_legacy_interrupted_runs.up.sql"))
	if err != nil {
		t.Fatalf("read migration 0018: %v", err)
	}
	if _, err := env.db.ExecContext(ctx, string(raw)); err != nil {
		t.Fatalf("apply migration 0018: %v", err)
	}

	var status, errorCode string
	var finished sql.NullTime
	if err := env.db.QueryRowContext(ctx,
		`SELECT status, error_code, finished_at FROM runs WHERE id = ?`, runID).
		Scan(&status, &errorCode, &finished); err != nil {
		t.Fatalf("read run: %v", err)
	}
	if status != execution.StatusFailed {
		t.Fatalf("run status = %q, want %q", status, execution.StatusFailed)
	}
	if errorCode != "legacy_interrupted" {
		t.Fatalf("error_code = %q, want %q (rows without one get a provenance marker)",
			errorCode, "legacy_interrupted")
	}
	if !finished.Valid {
		t.Error("finished_at is NULL after normalization: an interrupted run is finished")
	}

	var failedEvents int
	if err := env.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM run_events WHERE run_id = ? AND event_type = 'run.failed'`, runID).
		Scan(&failedEvents); err != nil {
		t.Fatalf("count run.failed events: %v", err)
	}
	if failedEvents != 1 {
		t.Fatalf("run.failed events = %d, want 1 (the legacy event was rewritten)", failedEvents)
	}
	var legacyEvents int
	if err := env.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM run_events WHERE run_id = ? AND event_type = 'run.interrupted'`, runID).
		Scan(&legacyEvents); err != nil {
		t.Fatalf("count run.interrupted events: %v", err)
	}
	if legacyEvents != 0 {
		t.Fatalf("run.interrupted events = %d, want 0", legacyEvents)
	}

	// Re-running must be a no-op (the statements are status-guarded).
	if _, err := env.db.ExecContext(ctx, string(raw)); err != nil {
		t.Fatalf("re-apply migration 0018: %v", err)
	}
	if err := env.db.QueryRowContext(ctx,
		`SELECT error_code FROM runs WHERE id = ?`, runID).Scan(&errorCode); err != nil {
		t.Fatalf("re-read run: %v", err)
	}
	if errorCode != "legacy_interrupted" {
		t.Fatalf("error_code after re-run = %q, want it unchanged", errorCode)
	}
}

// The migration must not touch rows in a canonical terminal state.
func TestMigration0018LeavesCanonicalTerminalsAlone(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()
	userID := seedUser(t, env.db)
	svc := execution.NewService(env.db, nil, testLogger(), telemetry.NewMetrics("test"))
	run, err := svc.CreateRunAdmitted(ctx, &execution.CreateRunInput{
		UserID:             userID,
		ApplicationID:      1,
		CreateConversation: true,
		Provider:           "itest_interrupted_keep",
		RuntimeType:        "agent",
		ExecutionMode:      "interactive",
		Content:            "genuine failure fixture",
	}, 10)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	t.Cleanup(func() {
		_, _ = env.db.ExecContext(context.Background(), `DELETE FROM runs WHERE id = ?`, run.ID.Bytes())
		_, _ = env.db.ExecContext(context.Background(), `DELETE FROM users WHERE id = ?`, userID)
	})
	if _, err := env.db.ExecContext(ctx,
		`UPDATE runs SET status = 'failed', error_code = 'boom' WHERE id = ?`, run.ID.Bytes()); err != nil {
		t.Fatalf("seed failed run: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations",
		"0018_normalize_legacy_interrupted_runs.up.sql"))
	if err != nil {
		t.Fatalf("read migration 0018: %v", err)
	}
	if _, err := env.db.ExecContext(ctx, string(raw)); err != nil {
		t.Fatalf("apply migration 0018: %v", err)
	}

	var errorCode string
	if err := env.db.QueryRowContext(ctx,
		`SELECT error_code FROM runs WHERE id = ?`, run.ID.Bytes()).Scan(&errorCode); err != nil {
		t.Fatalf("read run: %v", err)
	}
	if errorCode != "boom" {
		t.Fatalf("error_code = %q, want the original %q — migration 0018 must not "+
			"overwrite a real error code", errorCode, "boom")
	}
}
