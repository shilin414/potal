package execution

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/dbtypes"
)

// RetryOwnedRun requeues a running run for another attempt as ONE TiDB
// transaction (修复计划 §19-20, Phase 2):
//
//  1. verify ownership (run row locked FOR UPDATE, epoch checked)
//  2. CAS runs running → queued with available_at = retryAt
//  3. append run.retrying (NON-terminal — SSE stays open, the frontend
//     keeps streaming; 评测 §八 lifecycle semantics)
//  4. INSERT outbox run.dispatch with the SAME retryAt as available_at —
//     Run availability and outbox publishing must never diverge (P1-2:
//     publishing immediately made the woken worker lose the CAS against
//     its own not-yet-available run and wait for the fallback scan)
//  5. DELETE the caller's own lease row
//     COMMIT
//
// retryAt = now + Service.RequeueDelay (RUN_REQUEUE_DELAY), so retry
// timing has exactly one source of truth.
//
// Idempotency: a run already requeued by the same owner (duplicate
// dispatch) or already terminal is a no-op returning nil. A run owned by
// someone else returns ErrLostOwnership — a stale worker can never
// requeue the new owner's run, never create a duplicate dispatch and
// never drop the new owner's lease (评测 §九).
func (s *Service) RetryOwnedRun(ctx context.Context, run *Run, own ExecutionOwnership, reason string) error {
	return s.RetryOwnedRunAt(ctx, run, own, reason, time.Now().UTC().Add(s.requeueDelay()))
}

// RetryOwnedRunAt is RetryOwnedRun with an explicit retry instant (both
// the run's available_at and the outbox row use it).
func (s *Service) RetryOwnedRunAt(ctx context.Context, run *Run, own ExecutionOwnership, reason string, retryAt time.Time) error {
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

	// 2. Requeue CAS (fenced by the verified epoch), carrying retryAt.
	if _, err := q.RequeueRunFenced(ctx, db.RequeueRunFencedParams{
		AvailableAt: sql.NullTime{Time: retryAt, Valid: true},
		ID:          own.RunID.Bytes(),
		LeaseEpoch:  own.LeaseEpoch,
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
	// retryAt, exactly when the run becomes claimable.
	payload, _ := json.Marshal(map[string]any{
		"run_id":         own.RunID.String(),
		"provider":       run.Provider,
		"priority_class": PriorityClassRetry,
	})
	if _, err := q.CreateOutboxEventAt(ctx, db.CreateOutboxEventAtParams{
		Aggregate:   "run",
		AggregateID: own.RunID.Bytes(),
		EventType:   "run.dispatch",
		Payload:     dbtypes.JSONText(payload),
		AvailableAt: retryAt,
	}); err != nil {
		return err
	}

	// 5. Lease cleanup — same transaction.
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
