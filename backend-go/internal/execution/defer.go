package execution

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/dbtypes"
)

// EventRunDeferred is the non-terminal "execution deferred" marker
// (第三轮 P1-C): the run was requeued while WAITING for a paused provider
// or an unavailable gate — not because the provider failed. It keeps the
// SSE stream open exactly like run.retrying, but consumers can
// distinguish "delayed" from "retrying after failure".
const EventRunDeferred = "run.deferred"

// DeferOwnedRunAfter requeues a RUNNING run after a delay while
// PRESERVING its business priority, as ONE database transaction
// (第三轮 P1-C):
//
//  1. verify ownership (run row locked FOR UPDATE, epoch checked)
//  2. CAS runs running → queued with
//     available_at = DB_CLOCK + delay — `priority` is NOT touched
//  3. sync the schedule occurrence running → queued (state convergence)
//  4. append run.deferred (NON-terminal — SSE stays open)
//  5. INSERT outbox run.dispatch with the SAME DB-clock delay and the
//     run's ORIGINAL priority class, so the wakeup is dispatched exactly
//     when the run becomes claimable and lands in its own fair-share
//     stream again (a paused interactive run must not come back as
//     `retry`)
//  6. DELETE the run's provider slots (current and stale epochs)
//  7. DELETE the caller's own lease row
//     COMMIT
//
// Pause semantics: "delayed execution, same priority". This is why the
// provider-retry path (RetryOwnedRunAfter, priority='retry') and the
// defer path are separate — a provider pause must never demote an
// interactive or scheduled run into the retry class (第三轮 §10-11).
//
// Idempotency and fencing mirror RetryOwnedRunAfter: a run already
// requeued by the same owner or already terminal is a no-op (nil); a run
// owned by someone else returns ErrLostOwnership.
func (s *Service) DeferOwnedRunAfter(ctx context.Context, run *Run, own ExecutionOwnership, reason string, delay time.Duration) error {
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

	// Authoritative clock read for the event payload only; the actual
	// availability below is computed by the DB itself.
	dbNow, err := q.CurrentDBTime(ctx)
	if err != nil {
		return err
	}
	deferAt := dbNow.Add(delay)
	deferMicros := delay.Microseconds()

	// 2. Defer CAS (fenced): running → queued, DB-clock availability,
	// business priority UNCHANGED.
	if _, err := q.DeferRunFenced(ctx, db.DeferRunFencedParams{
		DeferDelayMicros: deferMicros,
		ID:               own.RunID.Bytes(),
		LeaseEpoch:       own.LeaseEpoch,
	}); err != nil {
		return err
	}

	// 3. Occurrence convergence: the requeued run's occurrence goes back
	// to queued in the same transaction (no-op for interactive runs).
	if _, err := q.DeferScheduledOccurrence(ctx, sql.NullString{String: string(own.RunID.Bytes()), Valid: true}); err != nil {
		return err
	}

	// 4. run.deferred — deliberately NOT terminal. Epoch 0: ownership was
	// verified under the row lock in step 1 and the defer below flips the
	// status to queued, so the fenced lock predicate must NOT re-apply.
	sequence, err := appendEventTx(ctx, tx, own.RunID, 0, EventRunDeferred, map[string]any{
		"attempt":   row.Attempt,
		"reason":    reason,
		"worker_id": own.WorkerID,
		"defer_at":  deferAt.Format(time.RFC3339Nano),
	})
	if err != nil {
		return err
	}

	// 5. Re-dispatch through the outbox at the SAME DB-clock delay with
	// the run's ORIGINAL priority class — pause keeps its fair share.
	payload, _ := json.Marshal(map[string]any{
		"run_id":         own.RunID.String(),
		"provider":       run.Provider,
		"priority_class": PriorityClassOf(run.Priority),
	})
	if _, err := q.CreateOutboxEventAt(ctx, db.CreateOutboxEventAtParams{
		Aggregate:        "run",
		AggregateID:      own.RunID.Bytes(),
		EventType:        "run.dispatch",
		Payload:          dbtypes.JSONText(payload),
		RetryDelayMicros: deferMicros,
	}); err != nil {
		return err
	}

	// 6. Provider capacity cleanup — same transaction as the ownership
	// transition (a queued run must not hold a provider slot).
	if _, err := q.DeleteProviderSlotsUpToEpoch(ctx, db.DeleteProviderSlotsUpToEpochParams{
		RunID:      own.RunID.Bytes(),
		LeaseEpoch: own.LeaseEpoch,
	}); err != nil {
		return err
	}

	// 7. Lease cleanup — same transaction.
	if err := deleteOwnedLeaseTx(ctx, q, own); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	s.publishLive(ctx, own.RunID, sequence, EventRunDeferred, map[string]any{
		"attempt":   row.Attempt,
		"reason":    reason,
		"worker_id": own.WorkerID,
		"defer_at":  deferAt.Format(time.RFC3339Nano),
	})
	return nil
}

// DeferAfter requeues the claimed run after a delay while preserving its
// business priority (第三轮 P1-C). The fenced variant of Retry for gate
// pauses: the provider never saw this run, so no attempt is consumed and
// the run keeps its original admission class.
func (w *WorkerOwnedService) DeferAfter(ctx context.Context, claimed *ClaimedRun, reason string, delay time.Duration) error {
	return w.svc.DeferOwnedRunAfter(ctx, claimed.Run, claimed.Ownership, reason, delay)
}
