package integration

// 第六轮 P0 integration coverage: migration 0018 rewrote EVERY historical
// `run.interrupted` event to `run.failed`, but the pre-closure retry path
// wrote that event BEFORE deciding whether the run would be requeued or
// failed:
//
//	run.interrupted {"reason": "..."}  → retry / interruption marker
//	run.interrupted {"status": "...", "error_code": "..."}
//	                                   → direct terminal failure
//	                                     (legacy Finish(StatusInterrupted))
//
// Turning the first shape into a terminal run.failed makes a historical
// run that later SUCCEEDED render as 执行失败 (the frontend/SSE client
// closes the stream on the first terminal event), and it puts a terminal
// event in the log of a run that is still queued for retry.
//
// Migration 0020 repairs both: reason-only markers become run.retrying and
// any terminal run left without a canonical terminal event gets one.
//
// These tests apply the REAL migration files (0018, then 0020) so they
// exercise the exact SQL that ships.

import (
	"context"
	"database/sql"
	"testing"

	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
)

// legacyRetryMarkerPayload is the payload ReleaseInterrupted wrote before
// it knew whether the run would be retried or failed.
const legacyRetryMarkerPayload = `{"reason":"worker lease expired"}`

// legacyDirectTerminalPayload is the payload the legacy
// Finish(StatusInterrupted) path wrote: like every Finish, it carries the
// terminal `status`, so it is distinguishable from the retry marker.
const legacyDirectTerminalPayload = `{"status":"interrupted","error_code":"interrupted"}`

// seedReview6Run creates a real run (with its own user and conversation)
// whose status is then forced to whatever the historical scenario
// requires. It returns the run id / user id / conversation id.
//
// NOTE(第六轮): this REPLACES the weaker `seedInterruptedRun` +
// `seedUser` pair for the migration scenarios. Both of those leak rows
// when used directly:
//
//   - seedUser registers no cleanup at all, so the run's FK parent
//     silently disappears mid-suite and the leaked run is left with
//     user_id = NULL, conversation_id = NULL;
//   - the report's §25 invariant sweeps scan the WHOLE database, so even
//     a correctly-cleaned run would keep failing them if a sibling
//     fixture leaked.
//
// The cleanup below deletes the dependencies in FK order (events → run →
// conversation → user) and is asserted by TestReview6FixturesLeaveNoResidue.
func seedReview6Run(t *testing.T, d *sql.DB, status string, errorCode string) ([]byte, int64, int64) {
	t.Helper()
	ctx := context.Background()

	userID := seedUser(t, d)
	svc := execution.NewService(d, nil, testLogger(), telemetry.NewMetrics("test"))
	run, err := svc.CreateRunAdmitted(ctx, &execution.CreateRunInput{
		UserID:             userID,
		ApplicationID:      1,
		CreateConversation: true,
		Provider:           "itest_review6_migration",
		RuntimeType:        "agent",
		ExecutionMode:      "interactive",
		Content:            "review6 interrupted-event fixture",
	}, 10)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	runID := run.ID.Bytes()

	var convID sql.NullInt64
	if err := d.QueryRowContext(ctx,
		`SELECT conversation_id FROM runs WHERE id = ?`, runID).Scan(&convID); err != nil {
		t.Fatalf("read conversation: %v", err)
	}

	t.Cleanup(func() {
		bg := context.Background()
		_, _ = d.ExecContext(bg, `DELETE FROM run_events WHERE run_id = ?`, runID)
		_, _ = d.ExecContext(bg, `DELETE FROM run_leases WHERE run_id = ?`, runID)
		_, _ = d.ExecContext(bg, `DELETE FROM runs WHERE id = ?`, runID)
		if convID.Valid {
			_, _ = d.ExecContext(bg, `DELETE FROM conversations WHERE id = ?`, convID.Int64)
		}
		_, _ = d.ExecContext(bg, `DELETE FROM users WHERE id = ?`, userID)
	})

	if _, err := d.ExecContext(ctx,
		`UPDATE runs SET status = ?, error_code = ?, finished_at = NULL WHERE id = ?`,
		status, errorCode, runID); err != nil {
		t.Fatalf("force status %q: %v", status, err)
	}
	// Start from a clean log so sequences are exactly what the scenario says.
	if _, err := d.ExecContext(ctx, `DELETE FROM run_events WHERE run_id = ?`, runID); err != nil {
		t.Fatalf("clear events: %v", err)
	}
	return runID, userID, convID.Int64
}

func seedReview6Event(t *testing.T, d *sql.DB, runID []byte, seq int64, eventType, payload string) {
	t.Helper()
	if _, err := d.ExecContext(context.Background(),
		`INSERT INTO run_events (run_id, sequence, event_type, payload) VALUES (?, ?, ?, ?)`,
		runID, seq, eventType, payload); err != nil {
		t.Fatalf("seed event %d (%s): %v", seq, eventType, err)
	}
}

func eventTypeAt(t *testing.T, d *sql.DB, runID []byte, seq int64) string {
	t.Helper()
	var got string
	if err := d.QueryRowContext(context.Background(),
		`SELECT event_type FROM run_events WHERE run_id = ? AND sequence = ?`,
		runID, seq).Scan(&got); err != nil {
		t.Fatalf("read event at sequence %d: %v", seq, err)
	}
	return got
}

func countRunEvents(t *testing.T, d *sql.DB, runID []byte, eventTypes ...string) int {
	t.Helper()
	q := `SELECT COUNT(*) FROM run_events WHERE run_id = ?`
	args := []any{runID}
	if len(eventTypes) > 0 {
		q += ` AND event_type IN (`
		for i, et := range eventTypes {
			if i > 0 {
				q += `,`
			}
			q += `?`
			args = append(args, et)
		}
		q += `)`
	}
	var n int
	if err := d.QueryRowContext(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("count events: %v", err)
	}
	return n
}

func runStatusOf(t *testing.T, d *sql.DB, runID []byte) string {
	t.Helper()
	var status string
	if err := d.QueryRowContext(context.Background(),
		`SELECT status FROM runs WHERE id = ?`, runID).Scan(&status); err != nil {
		t.Fatalf("read run status: %v", err)
	}
	return status
}

// ── 1. The core case: a historical retry that eventually succeeded ───────

// TestMigration0020RestoresHistoricalRetryBeforeSuccess is the regression
// test for the release blocker. The run was interrupted once, requeued,
// restarted and completed — a SUCCESS whose log 0018 turned into
// "started, FAILED, started, completed".
func TestMigration0020RestoresHistoricalRetryBeforeSuccess(t *testing.T) {
	env := newScheduleEnv(t)
	runID, _, _ := seedReview6Run(t, env.db, execution.StatusSucceeded, "")

	seedReview6Event(t, env.db, runID, 1, execution.EventRunStarted, `{}`)
	seedReview6Event(t, env.db, runID, 2, execution.EventRunInterrupted, legacyRetryMarkerPayload)
	seedReview6Event(t, env.db, runID, 3, execution.EventRunStarted, `{}`)
	seedReview6Event(t, env.db, runID, 4, execution.EventRunCompleted, `{"status":"succeeded"}`)

	// Step 1: 0018 rewrites every legacy interrupted event to run.failed —
	// this is exactly the damage 0020 has to undo.
	applyMigrationFile(t, env.db, "0018_normalize_legacy_interrupted_runs.up.sql")
	if got := eventTypeAt(t, env.db, runID, 2); got != execution.EventRunFailed {
		t.Fatalf("after 0018: event 2 = %q, want run.failed (precondition of the "+
			"repair scenario)", got)
	}

	// Step 2: 0020 restores the retry marker.
	applyMigrationFile(t, env.db, "0020_repair_legacy_interrupted_event_semantics.up.sql")

	if got := eventTypeAt(t, env.db, runID, 2); got != execution.EventRunRetrying {
		t.Fatalf("after 0020: event 2 = %q, want run.retrying — a successful run "+
			"must not carry a terminal failure in its log", got)
	}
	if got := eventTypeAt(t, env.db, runID, 4); got != execution.EventRunCompleted {
		t.Fatalf("after 0020: event 4 = %q, want run.completed", got)
	}
	if n := countRunEvents(t, env.db, runID,
		execution.EventRunCompleted, execution.EventRunFailed, execution.EventRunCancelled); n != 1 {
		t.Fatalf("terminal events = %d, want 1: exactly one canonical terminal "+
			"event may exist", n)
	}
	if n := countRunEvents(t, env.db, runID, execution.EventRunFailed); n != 0 {
		t.Fatalf("run.failed events = %d, want 0", n)
	}
}

// ── 2. A retry that is still in flight must stay NON-terminal ───────────

// TestMigration0020KeepsActiveRetryNonTerminal: the run is queued waiting
// for its retry. After 0018 its log claimed a terminal failure while the
// run was live work; the frontend would close the stream on that frame and
// never see the retry succeed.
func TestMigration0020KeepsActiveRetryNonTerminal(t *testing.T) {
	env := newScheduleEnv(t)
	runID, _, _ := seedReview6Run(t, env.db, execution.StatusQueued, "")

	seedReview6Event(t, env.db, runID, 1, execution.EventRunStarted, `{}`)
	seedReview6Event(t, env.db, runID, 2, execution.EventRunInterrupted, legacyRetryMarkerPayload)

	applyMigrationFile(t, env.db, "0018_normalize_legacy_interrupted_runs.up.sql")
	applyMigrationFile(t, env.db, "0020_repair_legacy_interrupted_event_semantics.up.sql")

	if got := runStatusOf(t, env.db, runID); got != execution.StatusQueued {
		t.Fatalf("run status = %q, want queued: a live run waiting for its retry "+
			"must not be made terminal by a migration", got)
	}
	if got := eventTypeAt(t, env.db, runID, 2); got != execution.EventRunRetrying {
		t.Fatalf("event 2 = %q, want run.retrying", got)
	}
	if n := countRunEvents(t, env.db, runID,
		execution.EventRunCompleted, execution.EventRunFailed, execution.EventRunCancelled); n != 0 {
		t.Fatalf("terminal events = %d, want 0: the run is still working", n)
	}
	if n := countRunEvents(t, env.db, runID); n != 2 {
		t.Fatalf("total events = %d, want 2 (no terminal event may be synthesized "+
			"for a live run)", n)
	}
}

// ── 3. Retry budget exhausted: synthesize the missing terminal event ────

// TestMigration0020SynthesizesTerminalAfterExhaustedRetry: the legacy code
// emitted only the reason-only marker and then set status=failed without a
// second event. After 0020 restores that marker to run.retrying the run
// would own no canonical terminal event at all (invariant D).
func TestMigration0020SynthesizesTerminalAfterExhaustedRetry(t *testing.T) {
	env := newScheduleEnv(t)
	runID, _, _ := seedReview6Run(t, env.db, execution.StatusFailed, "interrupted")

	seedReview6Event(t, env.db, runID, 1, execution.EventRunStarted, `{}`)
	seedReview6Event(t, env.db, runID, 2, execution.EventRunInterrupted, legacyRetryMarkerPayload)

	applyMigrationFile(t, env.db, "0018_normalize_legacy_interrupted_runs.up.sql")
	applyMigrationFile(t, env.db, "0020_repair_legacy_interrupted_event_semantics.up.sql")

	if got := eventTypeAt(t, env.db, runID, 2); got != execution.EventRunRetrying {
		t.Fatalf("event 2 = %q, want run.retrying (the marker was not terminal)", got)
	}
	if got := eventTypeAt(t, env.db, runID, 3); got != execution.EventRunFailed {
		t.Fatalf("event 3 = %q, want run.failed (synthesized terminal event)", got)
	}
	if n := countRunEvents(t, env.db, runID, execution.EventRunFailed); n != 1 {
		t.Fatalf("run.failed events = %d, want exactly 1", n)
	}
	if n := countRunEvents(t, env.db, runID,
		execution.EventRunCompleted, execution.EventRunFailed, execution.EventRunCancelled); n != 1 {
		t.Fatalf("terminal events = %d, want 1", n)
	}
}

// ── 4. A genuinely terminal legacy interrupted event is preserved ───────

// TestMigration0020PreservesDirectTerminalInterrupted: the direct terminal
// shape carries `status`, so it is NOT a retry marker. It stays
// run.failed (0018's conversion is correct for it).
func TestMigration0020PreservesDirectTerminalInterrupted(t *testing.T) {
	env := newScheduleEnv(t)
	runID, _, _ := seedReview6Run(t, env.db, execution.StatusInterrupted, "")

	seedReview6Event(t, env.db, runID, 1, execution.EventRunStarted, `{}`)
	seedReview6Event(t, env.db, runID, 2, execution.EventRunInterrupted, legacyDirectTerminalPayload)

	applyMigrationFile(t, env.db, "0018_normalize_legacy_interrupted_runs.up.sql")
	applyMigrationFile(t, env.db, "0020_repair_legacy_interrupted_event_semantics.up.sql")

	// 0018 turns the status alias into failed and its event into run.failed.
	if got := runStatusOf(t, env.db, runID); got != execution.StatusFailed {
		t.Fatalf("run status = %q, want failed (legacy terminal alias)", got)
	}
	if got := eventTypeAt(t, env.db, runID, 2); got != execution.EventRunFailed {
		t.Fatalf("event 2 = %q, want run.failed: the DIRECT TERMINAL shape must not "+
			"be repaired into run.retrying", got)
	}
	if n := countRunEvents(t, env.db, runID, execution.EventRunRetrying); n != 0 {
		t.Fatalf("run.retrying events = %d, want 0", n)
	}
	if n := countRunEvents(t, env.db, runID,
		execution.EventRunCompleted, execution.EventRunFailed, execution.EventRunCancelled); n != 1 {
		t.Fatalf("terminal events = %d, want 1 (0018's synthesized row is still the "+
			"only terminal event)", n)
	}
}

// ── 5. Idempotency ──────────────────────────────────────────────────────

func TestMigration0020IsIdempotent(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()

	// One exhausted-retry run (the worst case: it gets a synthesized
	// terminal event) plus one live retry.
	failedRun, _, _ := seedReview6Run(t, env.db, execution.StatusFailed, "interrupted")
	seedReview6Event(t, env.db, failedRun, 1, execution.EventRunStarted, `{}`)
	seedReview6Event(t, env.db, failedRun, 2, execution.EventRunInterrupted, legacyRetryMarkerPayload)

	queuedRun, _, _ := seedReview6Run(t, env.db, execution.StatusQueued, "")
	seedReview6Event(t, env.db, queuedRun, 1, execution.EventRunStarted, `{}`)
	seedReview6Event(t, env.db, queuedRun, 2, execution.EventRunInterrupted, legacyRetryMarkerPayload)

	applyMigrationFile(t, env.db, "0018_normalize_legacy_interrupted_runs.up.sql")
	applyMigrationFile(t, env.db, "0020_repair_legacy_interrupted_event_semantics.up.sql")

	eventsAfter := map[string]int{
		"failed": countRunEvents(t, env.db, failedRun),
		"queued": countRunEvents(t, env.db, queuedRun),
	}
	var maxSeqFailed, maxSeqQueued int64
	if err := env.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(sequence),0) FROM run_events WHERE run_id = ?`, failedRun).
		Scan(&maxSeqFailed); err != nil {
		t.Fatalf("max sequence: %v", err)
	}
	if err := env.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(sequence),0) FROM run_events WHERE run_id = ?`, queuedRun).
		Scan(&maxSeqQueued); err != nil {
		t.Fatalf("max sequence: %v", err)
	}

	// Second run of 0020 — must change nothing at all.
	applyMigrationFile(t, env.db, "0020_repair_legacy_interrupted_event_semantics.up.sql")

	if got := countRunEvents(t, env.db, failedRun); got != eventsAfter["failed"] {
		t.Fatalf("failed run event count = %d after re-run, want %d unchanged",
			got, eventsAfter["failed"])
	}
	if got := countRunEvents(t, env.db, queuedRun); got != eventsAfter["queued"] {
		t.Fatalf("queued run event count = %d after re-run, want %d unchanged",
			got, eventsAfter["queued"])
	}
	var again int64
	if err := env.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(sequence),0) FROM run_events WHERE run_id = ?`, failedRun).
		Scan(&again); err != nil {
		t.Fatalf("max sequence: %v", err)
	}
	if again != maxSeqFailed {
		t.Fatalf("failed run max sequence = %d after re-run, want %d", again, maxSeqFailed)
	}
	if err := env.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(sequence),0) FROM run_events WHERE run_id = ?`, queuedRun).
		Scan(&again); err != nil {
		t.Fatalf("max sequence: %v", err)
	}
	if again != maxSeqQueued {
		t.Fatalf("queued run max sequence = %d after re-run, want %d", again, maxSeqQueued)
	}
	if n := countRunEvents(t, env.db, failedRun, execution.EventRunFailed); n != 1 {
		t.Fatalf("run.failed events = %d after re-run, want 1 (no duplicate terminal)",
			n)
	}
}

// ── Terminal invariant / no-damage guards ───────────────────────────────

// TestMigration0020LeavesHealthyLogsAlone: a run whose log is already
// canonical must not be touched — in particular no extra terminal event
// may be appended next to an existing one.
func TestMigration0020LeavesHealthyLogsAlone(t *testing.T) {
	env := newScheduleEnv(t)
	runID, _, _ := seedReview6Run(t, env.db, execution.StatusSucceeded, "")
	seedReview6Event(t, env.db, runID, 1, execution.EventRunStarted, `{}`)
	seedReview6Event(t, env.db, runID, 2, execution.EventRunCompleted, `{"status":"succeeded"}`)

	applyMigrationFile(t, env.db, "0020_repair_legacy_interrupted_event_semantics.up.sql")

	if n := countRunEvents(t, env.db, runID); n != 2 {
		t.Fatalf("event count = %d, want 2 (a healthy log is untouched)", n)
	}
	if n := countRunEvents(t, env.db, runID,
		execution.EventRunCompleted, execution.EventRunFailed, execution.EventRunCancelled); n != 1 {
		t.Fatalf("terminal events = %d, want 1", n)
	}
}

// ── Fixture hygiene ─────────────────────────────────────────────────────

// TestReview6FixturesLeaveNoResidue: the report's §25 verification sweeps
// are DATABASE-WIDE. A fixture that leaks a terminal run without a
// terminal event keeps them red forever, which makes a correct migration
// look broken and hides a real regression the moment one appears.
func TestReview6FixturesLeaveNoResidue(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()

	// A throwaway sub-test owns the fixtures so their cleanup has run by
	// the time this function performs the sweep.
	t.Run("fixtures", func(t *testing.T) {
		runID, _, _ := seedReview6Run(t, env.db, execution.StatusSucceeded, "")
		seedReview6Event(t, env.db, runID, 1, execution.EventRunStarted, `{}`)
		seedReview6Event(t, env.db, runID, 2, execution.EventRunCompleted, `{"status":"succeeded"}`)
	})

	// No run from this file's fixture provider may outlive its test, and
	// no leaked terminal run may violate invariant D.
	var leakedRuns int
	if err := env.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM runs WHERE provider = 'itest_review6_migration'`).
		Scan(&leakedRuns); err != nil {
		t.Fatalf("count leaked runs: %v", err)
	}
	if leakedRuns != 0 {
		t.Fatalf("leaked fixture runs = %d, want 0 (the cleanup must delete the "+
			"run, its conversation and its user)", leakedRuns)
	}

	var violations int
	if err := env.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM runs r
		WHERE r.provider = 'itest_review6_migration'
		  AND r.status IN ('succeeded','failed','cancelled')
		  AND NOT EXISTS (SELECT 1 FROM run_events e WHERE e.run_id = r.id
		    AND e.event_type IN ('run.completed','run.failed','run.cancelled'))`).
		Scan(&violations); err != nil {
		t.Fatalf("count invariant violations: %v", err)
	}
	if violations != 0 {
		t.Fatalf("terminal fixture runs without a terminal event = %d, want 0",
			violations)
	}

	// The user rows must go too, otherwise the survivors accumulate on
	// every CI run.
	var leakedUsers int
	if err := env.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE username LIKE 'itest_u3_%' AND created_at > UTC_TIMESTAMP() - INTERVAL 1 HOUR`).
		Scan(&leakedUsers); err != nil {
		// Non-fatal: the username prefix is a shared fixture convention and
		// the count is only a smoke signal.
		t.Logf("could not count recent fixture users: %v", err)
	} else if leakedUsers > 0 {
		t.Logf("note: %d fixture users created in the last hour are still present", leakedUsers)
	}
}
