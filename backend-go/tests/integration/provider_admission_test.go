// Provider Admission & Release Gate integration tests
// (docs/potal 最新架构终审暨 Production Hardening 开发执行报告.md):
//
//	P0-2 attempt accounting   — claims / admission never consume attempts
//	P1-1 priority admission   — run dispatches route to class streams
//	P1-2 retry timing         — Run.available_at == outbox.available_at
//	P2-1 session bind errors  — RowsAffected error is not swallowed
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/execution"
	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/dbtypes"
)

// ── P0-2: attempt accounting ──

// TestProviderAdmissionDoesNotConsumeAttempt: claiming a run (and losing
// the provider admission race afterwards) must not burn retry budget.
func TestProviderAdmissionDoesNotConsumeAttempt(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	runID := seedRun(t, svc, "itest_admission")

	claimed, won, err := svc.ClaimRun(ctx, runID, "worker-a", time.Minute)
	if err != nil || !won {
		t.Fatalf("claim: won=%v err=%v", won, err)
	}
	if claimed.Run.Attempt != 0 {
		t.Fatalf("attempt after claim = %d, want 0 (claims are not provider attempts)", claimed.Run.Attempt)
	}
	// Provider Inflight rejected → the worker requeues (this is what
	// Worker.requeueForAdmission does).
	if err := svc.RetryOwnedRun(ctx, claimed.Run, claimed.Ownership, "provider_inflight_limit"); err != nil {
		t.Fatalf("requeue: %v", err)
	}
	run, err := svc.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Attempt != 0 {
		t.Fatalf("attempt after admission requeue = %d, want 0", run.Attempt)
	}
	if run.Status != execution.StatusQueued {
		t.Fatalf("status=%s, want queued", run.Status)
	}
}

// TestThreeAdmissionRequeuesStillLeavesAttemptZero: three consecutive
// admission rejections (the report's max_attempts=3 scenario) must leave
// the run with a FULL retry budget — the provider was never called.
func TestThreeAdmissionRequeuesStillLeavesAttemptZero(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	svc.RequeueDelay = 0 // re-claimable immediately, keeps the test fast
	runID := seedRun(t, svc, "itest_admission")

	for i := 0; i < 3; i++ {
		claimed, won, err := svc.ClaimRun(ctx, runID, "worker-a", time.Minute)
		if err != nil || !won {
			t.Fatalf("claim %d: won=%v err=%v", i, won, err)
		}
		// Requeue immediately (a negative delay makes the run claimable at
		// once) so the loop can re-claim right away; the delay semantics
		// themselves are covered by TestRetryRunAndOutboxShareRetryAt.
		if err := svc.RetryOwnedRunAfter(ctx, claimed.Run, claimed.Ownership,
			"provider_inflight_limit", -time.Second); err != nil {
			t.Fatalf("requeue %d: %v", i, err)
		}
	}
	run, err := svc.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Attempt != 0 {
		t.Fatalf("attempt after 3 inflight requeues = %d, want 0", run.Attempt)
	}
	if run.Status != execution.StatusQueued {
		t.Fatalf("status=%s, want queued (never failed)", run.Status)
	}
}

// TestProviderAttemptConsumesAttemptAndIsFenced: only the owner may begin
// a provider attempt, and it consumes exactly one unit of budget.
func TestProviderAttemptConsumesAttemptAndIsFenced(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	runID := seedRun(t, svc, "itest_admission")

	claimed, won, err := svc.ClaimRun(ctx, runID, "worker-a", time.Minute)
	if err != nil || !won {
		t.Fatalf("claim: won=%v err=%v", won, err)
	}
	attempt, err := svc.BeginProviderAttemptOwned(ctx, claimed.Ownership)
	if err != nil {
		t.Fatalf("begin attempt: %v", err)
	}
	if attempt != 1 {
		t.Fatalf("attempt = %d, want 1", attempt)
	}
	attempt, err = svc.BeginProviderAttemptOwned(ctx, claimed.Ownership)
	if err != nil || attempt != 2 {
		t.Fatalf("second begin: attempt=%d err=%v, want 2", attempt, err)
	}
	// A stale worker (reclaimed attempt → older epoch) can never consume
	// the new owner's budget: the lease EPOCH is the write fence.
	// (The token is not re-checked here on purpose — epoch and token are
	// minted together by ClaimRun, so an epoch match already identifies one
	// claim; heartbeats additionally verify the token.)
	stale := execution.ExecutionOwnership{
		RunID:      runID,
		WorkerID:   "stale",
		LeaseEpoch: claimed.Ownership.LeaseEpoch + 1,
		LeaseToken: claimed.Ownership.LeaseToken,
	}
	if _, err := svc.BeginProviderAttemptOwned(ctx, stale); !errors.Is(err, execution.ErrLostOwnership) {
		t.Fatalf("stale begin attempt: err=%v, want ErrLostOwnership", err)
	}
}

// TestProviderAttemptExhaustionIsExplicit: once the budget is used up the
// attempt call fails with ErrProviderAttemptsExhausted so the executor
// fails the run instead of looping.
func TestProviderAttemptExhaustionIsExplicit(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	runID := seedRun(t, svc, "itest_admission")
	if _, err := svc.DB.ExecContext(ctx,
		`UPDATE runs SET attempt = max_attempts WHERE id = ?`, runID.Bytes()); err != nil {
		t.Fatal(err)
	}
	claimed, won, err := svc.ClaimRun(ctx, runID, "worker-a", time.Minute)
	if err != nil || !won {
		t.Fatalf("claim: won=%v err=%v", won, err)
	}
	if _, err := svc.BeginProviderAttemptOwned(ctx, claimed.Ownership); !errors.Is(err, execution.ErrProviderAttemptsExhausted) {
		t.Fatalf("begin attempt on exhausted budget: err=%v, want ErrProviderAttemptsExhausted", err)
	}
}

// TestReaperBeforeProviderAttemptDoesNotExhaustRetries: a worker that dies
// BEFORE reaching the provider must not exhaust the retry budget — the
// reaper requeues instead of failing the run.
func TestReaperBeforeProviderAttemptDoesNotExhaustRetries(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	runID := seedRun(t, svc, "itest_admission")

	for i := 0; i < 3; i++ { // max_attempts = 3 lease expiries, no provider call
		if _, won, err := svc.ClaimRun(ctx, runID, "dead-worker", time.Minute); err != nil || !won {
			t.Fatalf("claim %d: won=%v err=%v", i, won, err)
		}
		if _, err := svc.Querier().HeartbeatLease(ctx, dbForceExpireParams(runID.Bytes())); err != nil {
			t.Fatal(err)
		}
		recoverRun(t, svc, runID)
	}
	run, err := svc.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status == execution.StatusFailed {
		t.Fatalf("run failed after lease expiries without any provider call (attempt=%d)", run.Attempt)
	}
	if run.Attempt != 0 {
		t.Fatalf("attempt=%d, want 0 (no provider execution happened)", run.Attempt)
	}
}

// ── P1-2: retry timing ──

// TestRetryRunAndOutboxShareRetryAt: the requeued run's available_at and
// the dispatch outbox row's available_at must be the SAME instant, so the
// relay wakes a worker exactly when the run becomes claimable.
func TestRetryRunAndOutboxShareRetryAt(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	svc.RequeueDelay = 3 * time.Second
	runID := seedRun(t, svc, "itest_retry_at")

	claimed, won, err := svc.ClaimRun(ctx, runID, "worker-a", time.Minute)
	if err != nil || !won {
		t.Fatalf("claim: won=%v err=%v", won, err)
	}
	before := time.Now().UTC()
	if err := svc.RetryOwnedRun(ctx, claimed.Run, claimed.Ownership, "aily_rate_limit"); err != nil {
		t.Fatalf("retry: %v", err)
	}

	run, err := svc.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.AvailableAt == nil {
		t.Fatal("retried run has no available_at")
	}
	if !run.AvailableAt.After(before) {
		t.Fatalf("available_at=%s is not in the future (hardcoded delay regression?)", run.AvailableAt)
	}
	var outboxAvailable time.Time
	if err := svc.DB.QueryRowContext(ctx,
		`SELECT available_at FROM outbox_events
		 WHERE aggregate='run' AND aggregate_id=? AND event_type='run.dispatch'
		 ORDER BY id DESC LIMIT 1`, runID.Bytes()).Scan(&outboxAvailable); err != nil {
		t.Fatalf("load outbox: %v", err)
	}
	delta := run.AvailableAt.Sub(outboxAvailable)
	if delta < 0 {
		delta = -delta
	}
	if delta > 50*time.Millisecond {
		t.Fatalf("run available_at=%s vs outbox available_at=%s differ by %s, want the same retryAt",
			run.AvailableAt, outboxAvailable, delta)
	}
	// The relay must not publish it early.
	pending, err := svc.Querier().ListPendingOutbox(ctx, 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range pending {
		if idsFromBytes(row.AggregateID) == runID.String() {
			t.Fatalf("retry dispatch is already relayable (available_at=%s)", outboxAvailable)
		}
	}
}

// TestReaperRequeueIsImmediatelyClaimableAndInSync: crash recovery must be
// immediate (no retry backoff) and must keep Run.available_at and the
// dispatch outbox row's available_at on the SAME instant. CI on MySQL
// caught the previous shape (reaper used now+backoff) as a hard failure:
// the run stayed unclaimable while the worker had already been woken.
func TestReaperRequeueIsImmediatelyClaimableAndInSync(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	runID := seedRun(t, svc, "itest_reaper_now")

	if _, won, err := svc.ClaimRun(ctx, runID, "dead-worker", time.Minute); err != nil || !won {
		t.Fatalf("claim: won=%v err=%v", won, err)
	}
	if _, err := svc.Querier().HeartbeatLease(ctx, dbForceExpireParams(runID.Bytes())); err != nil {
		t.Fatal(err)
	}
	recoverRun(t, svc, runID)

	// 1. The recovered run is claimable right away.
	if _, won, err := svc.ClaimRun(ctx, runID, "live-worker", time.Minute); err != nil || !won {
		t.Fatalf("claim right after recovery: won=%v err=%v, want immediate claimability", won, err)
	}
	run, err := svc.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.AvailableAt == nil {
		t.Fatal("recovered run has no available_at")
	}
	// 2. Run availability ≡ outbox availability (same DB clock).
	var published time.Time
	if err := svc.DB.QueryRowContext(ctx,
		`SELECT available_at FROM outbox_events
		 WHERE aggregate='run' AND aggregate_id=? AND event_type='run.dispatch'
		 ORDER BY id DESC LIMIT 1`, runID.Bytes()).Scan(&published); err != nil {
		t.Fatalf("load outbox: %v", err)
	}
	delta := run.AvailableAt.Sub(published)
	if delta < 0 {
		delta = -delta
	}
	if delta > 50*time.Millisecond {
		t.Fatalf("reaper run available_at=%s vs outbox available_at=%s differ by %s",
			run.AvailableAt, published, delta)
	}
}

// ── P1-1: priority admission routing ──

// TestRelayRoutesRunDispatchByPriorityClass: run dispatches land in the
// per-class stream, so the normal Redis path honours priority instead of
// being FIFO.
func TestRelayRoutesRunDispatchByPriorityClass(t *testing.T) {
	svc, rdb := testEnv(t)
	if rdb == nil {
		t.Skip("needs STUDIO_TEST_REDIS=1")
	}
	ctx := context.Background()
	const provider = "itest_priority"
	runID := seedRun(t, svc, provider)

	// Flush pending rows so the relay batch contains only our fixture.
	if _, err := svc.DB.ExecContext(ctx,
		`UPDATE outbox_events SET status='published' WHERE status='pending'`); err != nil {
		t.Fatalf("flush outbox: %v", err)
	}
	payload := dbtypes.JSONText([]byte(`{"run_id":"` + runID.String() + `","provider":"` + provider +
		`","priority_class":"scheduled"}`))
	if _, err := svc.Querier().CreateOutboxEvent(ctx, db.CreateOutboxEventParams{
		Aggregate:   "run",
		AggregateID: runID.Bytes(),
		EventType:   "run.dispatch",
		Payload:     payload,
	}); err != nil {
		t.Fatalf("create outbox: %v", err)
	}

	relay := execution.NewRelay(svc, rdb, 10)
	if _, err := relay.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	scheduled := execution.PriorityStream(rdb, provider, execution.PriorityClassScheduled)
	entries, err := rdb.XRange(ctx, scheduled, "-", "+").Result()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatalf("no entry in the scheduled class stream %s", scheduled)
	}
	if got, _ := entries[len(entries)-1].Values["run_id"].(string); got != runID.String() {
		t.Fatalf("scheduled stream run_id=%q, want %s", got, runID.String())
	}
	// The legacy single stream must NOT receive class-routed dispatches.
	legacy, err := rdb.XRange(ctx, execution.QueueStream(rdb, provider), "-", "+").Result()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range legacy {
		if got, _ := e.Values["run_id"].(string); got == runID.String() {
			t.Fatal("class-routed dispatch leaked into the legacy FIFO stream")
		}
	}
}

// TestCreateRunOutboxCarriesPriorityClass: interactive runs are published
// with priority_class=interactive by the run-creation transaction itself.
func TestCreateRunOutboxCarriesPriorityClass(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	run, err := svc.CreateRun(ctx, &execution.CreateRunInput{
		UserID:         42,
		ConversationID: 0,
		Provider:       "itest_priority_create",
		RuntimeType:    "agent",
		ExecutionMode:  "interactive",
		Content:        "hello",
		MaxAttempts:    3,
		Priority:       execution.DefaultPriority,
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	var payload string
	if err := svc.DB.QueryRowContext(ctx,
		`SELECT payload FROM outbox_events WHERE aggregate='run' AND aggregate_id=? ORDER BY id DESC LIMIT 1`,
		run.ID.Bytes()).Scan(&payload); err != nil {
		t.Fatalf("load outbox payload: %v", err)
	}
	// The JSON column is normalized by TiDB (spaces, key order), so parse
	// it instead of substring matching.
	var decoded struct {
		PriorityClass string `json:"priority_class"`
		Provider      string `json:"provider"`
	}
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("decode outbox payload %q: %v", payload, err)
	}
	if decoded.PriorityClass != execution.PriorityClassInteractive {
		t.Fatalf("outbox priority_class=%q, want %q (payload=%s)",
			decoded.PriorityClass, execution.PriorityClassInteractive, payload)
	}
}
