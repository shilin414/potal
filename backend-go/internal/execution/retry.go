package execution

import (
	"context"
	"encoding/json"
	"time"

	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/dbtypes"
)

// RetryOwnedRun requeues a running run for another attempt after the
// configured provider-retry backoff (RUN_REQUEUE_DELAY).
func (s *Service) RetryOwnedRun(ctx context.Context, run *Run, own ExecutionOwnership, reason string) error {
	return s.RetryOwnedRunAfter(ctx, run, own, reason, s.requeueDelay())
}

// RetryOwnedRunAfter requeues a running run for another attempt as ONE TiDB
// transaction (修复计划 §19-20, Phase 2 / Clock Authority Phase 3):
//
//  1. verify ownership (run row locked FOR UPDATE, epoch checked)
//  2. CAS runs running → queued with
//     available_at = DB_CLOCK + delay
//  3. append run.retrying (NON-terminal — SSE stays open, the frontend
//     keeps streaming; 评测 §八 lifecycle semantics)
//  4. INSERT outbox run.dispatch with the SAME DB-clock delay, so the relay
//     publishes the wakeup exactly when the run becomes claimable (P1-2:
//     publishing immediately made the woken worker lose the CAS against
//     its own not-yet-available run and wait for the fallback scan)
//  5. DELETE the run's provider slots (current and stale epochs) — a
//     requeued run holds no provider capacity
//  6. DELETE the caller's own lease row
//     COMMIT
//
// Both the run and the outbox row take their availability from the DB clock
// (never the application clock) plus the caller's delay, so retry timing has
// exactly one authority and one source of truth.
//
// Idempotency: a run already requeued by the same owner (duplicate
// dispatch) or already terminal is a no-op returning nil. A run owned by
// someone else returns ErrLostOwnership — a stale worker can never
// requeue the new owner's run, never create a duplicate dispatch and
// never drop the new owner's lease (评测 §九).
func (s *Service) RetryOwnedRunAfter(ctx context.Context, run *Run, own ExecutionOwnership, reason string, delay time.Duration) error {
	if !own.Valid() {
		return ErrLostOwnership
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := db.New(tx)

	// 1. Ownership verification under the row lock.
	row, err := verifyActiveOwnershipTx(ctx, tx, own)
	if err != nil {
		return err
	}

	// Authoritative clock read for the event payload only: the actual
	// availability below is computed by the DB itself (DATE_ADD over
	// CURRENT_TIMESTAMP(3)), so no app-clock value can leak into state.
	dbNow, err := q.CurrentDBTime(ctx)
	if err != nil {
		return err
	}
	retryAt := dbNow.Add(delay)
	delayMicros := delay.Microseconds()

	// 2. Requeue CAS (fenced by the verified epoch), DB-clock availability.
	if _, err := q.RequeueRunFenced(ctx, db.RequeueRunFencedParams{
		RetryDelayMicros: delayMicros,
		ID:               own.RunID.Bytes(),
		LeaseEpoch:       own.LeaseEpoch,
	}); err != nil {
		return err
	}

	// 3. run.retrying — deliberately NOT terminal (修复计划 §15-18).
	// Epoch 0: ownership was verified under the row lock in step 1; the
	// requeue below flips status to queued so the fenced lock predicate
	// (status='running') must NOT be re-applied here.
	sequence, err := appendEventTx(ctx, tx, own.RunID, 0, EventRunRetrying, map[string]any{
		"attempt":      row.Attempt,
		"max_attempts": row.MaxAttempts,
		"reason":       reason,
		"worker_id":    own.WorkerID,
		"retry_at":     retryAt.Format(time.RFC3339Nano),
	})
	if err != nil {
		return err
	}

	// 4. Re-dispatch through the outbox so the queue wakes a worker — at
	// the DB clock + the same delay, exactly when the run becomes claimable.
	payload, _ := json.Marshal(map[string]any{
		"run_id":         own.RunID.String(),
		"provider":       run.Provider,
		"priority_class": PriorityClassRetry,
	})
	if _, err := q.CreateOutboxEventAt(ctx, db.CreateOutboxEventAtParams{
		Aggregate:        "run",
		AggregateID:      own.RunID.Bytes(),
		EventType:        "run.dispatch",
		Payload:          dbtypes.JSONText(payload),
		RetryDelayMicros: delayMicros,
	}); err != nil {
		return err
	}

	// 5. Provider capacity cleanup — same transaction as the ownership
	// transition (a queued run must not hold a provider slot).
	if _, err := q.DeleteProviderSlotsUpToEpoch(ctx, db.DeleteProviderSlotsUpToEpochParams{
		RunID:      own.RunID.Bytes(),
		LeaseEpoch: own.LeaseEpoch,
	}); err != nil {
		return err
	}

	// 6. Lease cleanup — same transaction.
	if err := deleteOwnedLeaseTx(ctx, q, own); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	s.publishLive(ctx, own.RunID, sequence, EventRunRetrying, map[string]any{
		"attempt":      row.Attempt,
		"max_attempts": row.MaxAttempts,
		"reason":       reason,
		"worker_id":    own.WorkerID,
		"retry_at":     retryAt.Format(time.RFC3339Nano),
	})
	return nil
}
