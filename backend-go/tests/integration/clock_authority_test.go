// Clock Authority integration tests (Admission Fairness & Distributed Lease
// Hardening, Phase 3): lease expiry, retry availability and delivery retry
// timing are all derived from the DATABASE clock. The application clock may
// be skewed arbitrarily; it can neither extend nor shorten any of them.
package integration

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/delivery"
	"github.com/creation-agent-studio/backend-go/internal/execution"
	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
)

// dbDeltaMicros returns "TIMESTAMPDIFF(MICROSECOND, CURRENT_TIMESTAMP(3), ts)"
// for a single-row query — a positive value means ts is in the DB future.
func dbDeltaMicros(t *testing.T, svc *execution.Service, query string, args ...any) int64 {
	t.Helper()
	var micros int64
	if err := svc.DB.QueryRowContext(context.Background(), query, args...).Scan(&micros); err != nil {
		t.Fatalf("db clock delta: %v", err)
	}
	return micros
}

func withinTolerance(t *testing.T, label string, got, want time.Duration, tolerance time.Duration) {
	t.Helper()
	delta := got - want
	if delta < 0 {
		delta = -delta
	}
	if delta > tolerance {
		t.Fatalf("%s = %s, want %s (±%s)", label, got, want, tolerance)
	}
}

// TestLeaseExpiryUsesDatabaseClock: a claimed lease expires exactly
// leaseSeconds after the DATABASE now, not after the worker's clock.
func TestLeaseExpiryUsesDatabaseClock(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	const lease = 90 * time.Second
	runID := seedRun(t, svc, "itest_clock")
	if _, won, err := svc.ClaimRun(ctx, runID, "clock-worker", lease); err != nil || !won {
		t.Fatalf("claim: won=%v err=%v", won, err)
	}
	got := time.Duration(dbDeltaMicros(t, svc,
		`SELECT TIMESTAMPDIFF(MICROSECOND, CURRENT_TIMESTAMP(3), expires_at)
		 FROM run_leases WHERE run_id = ?`, runID.Bytes())) * time.Microsecond
	withinTolerance(t, "lease expiry", got, lease, 10*time.Second)

	// A negative extension (test/reaper fixture) expires the lease inside
	// the DB, proving the value is applied by the DB clock itself.
	if _, err := svc.Querier().HeartbeatLeaseFenced(ctx, db.HeartbeatLeaseFencedParams{
		LeaseMicros: -int64(2 * time.Second / time.Microsecond),
		RunID:       runID.Bytes(),
		LeaseToken:  mustLeaseToken(t, svc, runID),
	}); err != nil {
		t.Fatalf("negative heartbeat: %v", err)
	}
	if n := dbDeltaMicros(t, svc,
		`SELECT COUNT(*) FROM run_leases
		 WHERE run_id = ? AND expires_at <= CURRENT_TIMESTAMP(3)`, runID.Bytes()); n != 1 {
		t.Fatalf("leases marked expired = %d, want 1 (DB clock must apply the negative delay)", n)
	}

	// Expiry is terminal ownership loss: the production fenced heartbeat
	// must not revive the row during the pre-reaper window.
	res, err := svc.Querier().HeartbeatLeaseFenced(ctx, db.HeartbeatLeaseFencedParams{
		LeaseMicros: int64(10 * time.Minute / time.Microsecond),
		RunID:       runID.Bytes(),
		LeaseToken:  mustLeaseToken(t, svc, runID),
	})
	if err != nil {
		t.Fatalf("expired heartbeat: %v", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 0 {
		t.Fatalf("expired heartbeat revived lease: affected=%d err=%v, want 0", n, err)
	}
	// Hygiene: recover instead of reviving so this shared dev database does
	// not retain an expired lease for unrelated tests.
	recoverRun(t, svc, runID)
}

// mustLeaseToken loads the run's current lease token.
func mustLeaseToken(t *testing.T, svc *execution.Service, runID ids.ID) []byte {
	t.Helper()
	var token []byte
	if err := svc.DB.QueryRowContext(context.Background(),
		`SELECT lease_token FROM run_leases WHERE run_id = ?`, runID.Bytes()).Scan(&token); err != nil {
		t.Fatalf("load lease token: %v", err)
	}
	return token
}

// TestRetryAvailabilityIsDatabaseClockBased: the requeued run and its
// dispatch outbox row both become available exactly RequeueDelay after the
// DATABASE now.
func TestRetryAvailabilityIsDatabaseClockBased(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	const delay = 4 * time.Second
	svc.RequeueDelay = delay
	runID := seedRun(t, svc, "itest_clock_retry")
	claimed, won, err := svc.ClaimRun(ctx, runID, "clock-worker", time.Minute)
	if err != nil || !won {
		t.Fatalf("claim: won=%v err=%v", won, err)
	}
	if err := svc.RetryOwnedRun(ctx, claimed.Run, claimed.Ownership, "aily_rate_limit"); err != nil {
		t.Fatalf("retry: %v", err)
	}

	runDelta := time.Duration(dbDeltaMicros(t, svc,
		`SELECT TIMESTAMPDIFF(MICROSECOND, CURRENT_TIMESTAMP(3), available_at)
		 FROM runs WHERE id = ?`, runID.Bytes())) * time.Microsecond
	withinTolerance(t, "run available_at", runDelta, delay, 3*time.Second)

	outboxDelta := time.Duration(dbDeltaMicros(t, svc,
		`SELECT TIMESTAMPDIFF(MICROSECOND, CURRENT_TIMESTAMP(3), available_at)
		 FROM outbox_events
		 WHERE aggregate = 'run' AND aggregate_id = ? AND event_type = 'run.dispatch'
		 ORDER BY id DESC LIMIT 1`, runID.Bytes())) * time.Microsecond
	withinTolerance(t, "outbox available_at", outboxDelta, delay, 3*time.Second)

	// Admission requeues pass their own (short) delay and get the same
	// DB-clock treatment (a fresh run: the one above is not yet claimable).
	admitRun := seedRun(t, svc, "itest_clock_retry")
	claimed2, won, err := svc.ClaimRun(ctx, admitRun, "clock-worker", time.Minute)
	if err != nil || !won {
		t.Fatalf("re-claim: won=%v err=%v", won, err)
	}
	if err := svc.RetryOwnedRunAfter(ctx, claimed2.Run, claimed2.Ownership, "provider_inflight_limit", execution.AdmissionRequeueDelay); err != nil {
		t.Fatalf("admission requeue: %v", err)
	}
	admissionDelta := time.Duration(dbDeltaMicros(t, svc,
		`SELECT TIMESTAMPDIFF(MICROSECOND, CURRENT_TIMESTAMP(3), available_at)
		 FROM runs WHERE id = ?`, admitRun.Bytes())) * time.Microsecond
	withinTolerance(t, "admission available_at", admissionDelta, execution.AdmissionRequeueDelay, 2*time.Second)
}

// TestDeliveryRetryTimeIsDatabaseClockBased: delivery requeues and stuck-row
// reclamation measure their timing against the DB clock.
func TestDeliveryRetryTimeIsDatabaseClockBased(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	execID := seedDeliveryExecution(t, svc, "clock_retry")

	// pending → sending (the delivery worker's claim).
	if res, err := svc.Querier().CASClaimDelivery(ctx, execID.Bytes()); err != nil {
		t.Fatalf("claim delivery: %v", err)
	} else if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("claim delivery affected %d rows, want 1", n)
	}

	const backoff = 30 * time.Second
	if _, err := svc.Querier().RequeueDelivery(ctx, db.RequeueDeliveryParams{
		BackoffMicros: backoff.Microseconds(),
		ErrorCode:     "send_failed",
		ErrorMessage:  sql.NullString{String: "boom", Valid: true},
		ID:            execID.Bytes(),
	}); err != nil {
		t.Fatalf("requeue delivery: %v", err)
	}
	got := time.Duration(dbDeltaMicros(t, svc,
		`SELECT TIMESTAMPDIFF(MICROSECOND, CURRENT_TIMESTAMP(3), next_attempt_at)
		 FROM delivery_executions WHERE id = ?`, execID.Bytes())) * time.Microsecond
	withinTolerance(t, "delivery next_attempt_at", got, backoff, 5*time.Second)

	// Stuck 'sending' rows are reclaimed/reaped only after the DB-clock
	// lease lapsed: an attempt that just started must NOT be reclaimed...
	if res, err := svc.Querier().ReclaimStuckDeliveries(ctx, (10 * time.Minute).Microseconds()); err != nil {
		t.Fatalf("reclaim fresh delivery: %v", err)
	} else if n, _ := res.RowsAffected(); n != 0 {
		t.Fatalf("fresh sending delivery reclaimed (%d rows), the DB clock must guard it", n)
	}
	// ...while one whose updated_at is provably older is.
	if _, err := svc.DB.ExecContext(ctx,
		`UPDATE delivery_executions
		 SET status = 'sending', attempt = max_attempts,
		     updated_at = DATE_SUB(CURRENT_TIMESTAMP(3), INTERVAL 10 MINUTE)
		 WHERE id = ?`, execID.Bytes()); err != nil {
		t.Fatalf("age delivery: %v", err)
	}
	if res, err := svc.Querier().FailStuckDeliveries(ctx, db.FailStuckDeliveriesParams{
		ErrorMessage: sql.NullString{String: "worker crashed", Valid: true},
		LeaseMicros:  (time.Minute).Microseconds(),
	}); err != nil {
		t.Fatalf("fail stuck delivery: %v", err)
	} else if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("aged exhausted delivery reaped %d rows, want 1", n)
	}
	var status string
	if err := svc.DB.QueryRowContext(ctx,
		`SELECT status FROM delivery_executions WHERE id = ?`, execID.Bytes()).Scan(&status); err != nil {
		t.Fatalf("load delivery: %v", err)
	}
	if status != "failed" {
		t.Fatalf("delivery status = %s, want failed", status)
	}
}

// seedDeliveryExecution builds schedule → occurrence → delivery execution
// fixtures and returns the execution id.
func seedDeliveryExecution(t *testing.T, svc *execution.Service, targetID string) ids.ID {
	t.Helper()
	ctx := context.Background()
	runID, occ := seedScheduledRun(t, svc)
	res, err := svc.Querier().UpsertScheduleDelivery(ctx, db.UpsertScheduleDeliveryParams{
		ScheduleID:         occ.ScheduleID,
		Channel:            delivery.ChannelFeishu,
		SenderIdentityMode: delivery.SenderOwnerUser,
		TargetType:         delivery.TargetChat,
		TargetID:           targetID,
		TargetName:         "clock target",
		ContentMode:        delivery.ContentSummary,
		Enabled:            true,
	})
	if err != nil {
		t.Fatalf("upsert schedule delivery: %v", err)
	}
	deliveryID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("schedule delivery id: %v", err)
	}
	execID := ids.New()
	if _, err := svc.Querier().CreateDeliveryExecution(ctx, db.CreateDeliveryExecutionParams{
		ID:                 execID.Bytes(),
		OccurrenceID:       occ.ID,
		RunID:              runID.Bytes(),
		ScheduleDeliveryID: uint64(deliveryID),
		SenderUserID:       42,
		TargetType:         delivery.TargetChat,
		TargetID:           targetID,
	}); err != nil {
		t.Fatalf("create delivery execution: %v", err)
	}
	return execID
}
