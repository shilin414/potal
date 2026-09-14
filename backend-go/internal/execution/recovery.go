package execution

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/dbtypes"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
)

// RecoverExpiredLeases is the reaper (Recovery Coordinator — 修复计划
// §11-13, Phase 3). Each run is recovered in ONE TiDB transaction:
//
//	BEGIN
//	  SELECT the expired lease FOR UPDATE      (heartbeat-first wins)
//	  SELECT the run FOR UPDATE                (status + epoch verified)
//	  retryable:  runs → queued, run.retrying event, outbox dispatch
//	  exhausted:  runs → failed, run.failed event
//	  DELETE the expired lease (by token)
//	COMMIT
//
// The old three-step (delete lease → read run → release) had a
// crash window that re-created "running + no lease" orphans (评测 §六);
// here the run ownership transition and the lease cleanup commit or
// roll back together, so the invariant
//
//	running run ⇔ active lease
//
// can never be violated by recovery.
func (s *Service) RecoverExpiredLeases(ctx context.Context, limit int) (int, error) {
	runIDs, err := s.q(ctx).ListExpiredLeaseRunIDs(ctx, int32(limit))
	if err != nil {
		return 0, err
	}
	recovered := 0
	for _, raw := range runIDs {
		runID := mustID(raw)
		ok, err := s.recoverExpiredLeaseTx(ctx, runID)
		if err != nil {
			s.Log.Error("reaper recovery failed", "run_id", runID.String(), slogKey("err"), err)
			continue
		}
		if ok {
			recovered++
			if s.Metrics != nil {
				s.Metrics.LeaseExpired.Inc()
			}
		}
	}
	return recovered, nil
}

// recoverExpiredLeaseTx recovers one expired lease atomically. It
// returns false when there was nothing to do (lease renewed first / run
// already progressed).
func (s *Service) recoverExpiredLeaseTx(ctx context.Context, runID ids.ID) (bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	q := db.New(tx)

	// Lock the expired lease: a concurrent heartbeat that renewed it
	// makes this SELECT miss (expires_at in the future) → nothing to do.
	lease, err := q.GetExpiredLeaseForUpdate(ctx, runID.Bytes())
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil // renewed first — the owner keeps running
	}
	if err != nil {
		return false, err
	}

	// Lock the run and decide its recovery under the same transaction.
	row, err := q.GetRunForUpdate(ctx, runID.Bytes())
	if errors.Is(err, sql.ErrNoRows) {
		// Orphan lease row without a run: just drop it.
		if _, derr := q.DeleteLeaseByToken(ctx, db.DeleteLeaseByTokenParams{
			RunID: runID.Bytes(), LeaseToken: lease.LeaseToken,
		}); derr != nil {
			return false, derr
		}
		return true, tx.Commit()
	}
	if err != nil {
		return false, err
	}
	if IsTerminal(row.Status) || row.Status != StatusRunning {
		// The run moved on (finalized / requeued elsewhere) but the lease
		// row survived: clean it up and finish.
		if _, derr := q.DeleteLeaseByToken(ctx, db.DeleteLeaseByTokenParams{
			RunID: runID.Bytes(), LeaseToken: lease.LeaseToken,
		}); derr != nil {
			return false, derr
		}
		return false, tx.Commit()
	}

	// Epoch guard (修复计划 §13): the expired lease must belong to the
	// run's CURRENT epoch. A mismatch means the lease row is stale
	// bookkeeping — drop it and leave the run to its real owner.
	if row.LeaseEpoch != lease.LeaseEpoch {
		if _, derr := q.DeleteLeaseByToken(ctx, db.DeleteLeaseByTokenParams{
			RunID: runID.Bytes(), LeaseToken: lease.LeaseToken,
		}); derr != nil {
			return false, derr
		}
		return false, tx.Commit()
	}

	// The run is running on a lease that is provably expired: recover it.
	var sequence uint64
	var eventType string
	var eventPayload map[string]any
	if row.Attempt < row.MaxAttempts {
		// Retry: running → queued + run.retrying (non-terminal). Recovery
		// is IMMEDIATE (a crashed worker must not stall the run behind a
		// backoff) and both the run and its dispatch outbox row take their
		// availability from the DB clock in the same transaction, so the
		// run is claimable exactly when the relay can publish it (P1-2).
		if _, err := q.RequeueRunFencedImmediate(ctx, db.RequeueRunFencedImmediateParams{
			ID:         runID.Bytes(),
			LeaseEpoch: row.LeaseEpoch,
		}); err != nil {
			return false, err
		}
		eventType = EventRunRetrying
		eventPayload = map[string]any{
			"attempt":      row.Attempt,
			"max_attempts": row.MaxAttempts,
			"reason":       "lease_expired",
			"worker_id":    lease.WorkerID,
		}
		sequence, err = appendEventTx(ctx, tx, runID, 0, eventType, eventPayload)
		if err != nil {
			return false, err
		}
		payloadRaw, _ := json.Marshal(map[string]any{
			"run_id":         runID.String(),
			"provider":       providerOfRun(ctx, q, runID),
			"priority_class": PriorityClassRetry,
		})
		if _, err := q.CreateOutboxEvent(ctx, db.CreateOutboxEventParams{
			Aggregate:   "run",
			AggregateID: runID.Bytes(),
			EventType:   "run.dispatch",
			Payload:     dbtypes.JSONText(payloadRaw),
		}); err != nil {
			return false, err
		}
	} else {
		// Exhausted: running → failed + run.failed (terminal).
		if _, err := q.FailRunFenced(ctx, db.FailRunFencedParams{
			ErrorCode:    "lease_expired",
			ErrorMessage: nullText("worker lease expired after final attempt"),
			ID:           runID.Bytes(),
			LeaseEpoch:   row.LeaseEpoch,
		}); err != nil {
			return false, err
		}
		eventType = EventRunFailed
		eventPayload = map[string]any{
			"status":     StatusFailed,
			"error_code": "lease_expired",
			"reason":     "worker lease expired after final attempt",
		}
		sequence, err = appendEventTx(ctx, tx, runID, 0, eventType, eventPayload)
		if err != nil {
			return false, err
		}
		if err := finishOccurrenceTx(ctx, tx, row, runID, StatusFailed); err != nil {
			return false, err
		}
	}

	// Lease cleanup — same transaction as the state transition.
	leaseOwn := ExecutionOwnership{
		RunID:      runID,
		WorkerID:   lease.WorkerID,
		LeaseEpoch: row.LeaseEpoch,
		LeaseToken: mustID(lease.LeaseToken),
	}
	if err := deleteOwnedLeaseTx(ctx, q, leaseOwn); err != nil {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, err
	}
	s.publishLive(ctx, runID, sequence, eventType, eventPayload)
	return true, nil
}

// providerOfRun reads the provider for the outbox payload. Best-effort:
// GetRunByID inside the open tx (row already locked).
func providerOfRun(ctx context.Context, q db.Querier, runID ids.ID) string {
	row, err := q.GetRunByID(ctx, runID.Bytes())
	if err != nil {
		return "unknown"
	}
	return row.Provider
}
