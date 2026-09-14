package execution

import (
	"context"
	"database/sql"
	"errors"

	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
)

// verifyActiveOwnershipTx is the strict worker-write verifier. A worker
// may write only while the run is running at the caller's lease epoch;
// terminal, queued and stale-epoch callers all lose ownership.
func verifyActiveOwnershipTx(ctx context.Context, tx *sql.Tx, own ExecutionOwnership) (db.GetRunForUpdateRow, error) {
	row, err := db.New(tx).GetRunForUpdate(ctx, own.RunID.Bytes())
	if errors.Is(err, sql.ErrNoRows) {
		return row, ErrNotFound
	}
	if err != nil {
		return row, err
	}
	if row.Status != StatusRunning || row.LeaseEpoch != own.LeaseEpoch {
		return row, ErrLostOwnership
	}
	return row, nil
}

// verifyFinalizeOwnershipTx keeps terminal idempotency local to finalize.
// A current owner may finalize; any already-terminal run is a no-op; a
// running run at another epoch is a stale worker.
func verifyFinalizeOwnershipTx(ctx context.Context, tx *sql.Tx, own ExecutionOwnership) (db.GetRunForUpdateRow, bool, error) {
	row, err := db.New(tx).GetRunForUpdate(ctx, own.RunID.Bytes())
	if errors.Is(err, sql.ErrNoRows) {
		return row, false, ErrNotFound
	}
	if err != nil {
		return row, false, err
	}
	if IsTerminal(row.Status) {
		return row, true, nil
	}
	if row.Status != StatusRunning || row.LeaseEpoch != own.LeaseEpoch {
		return row, false, ErrLostOwnership
	}
	return row, false, nil
}

// updateExternalRunIDTx enforces set-once/idempotent/conflict semantics
// under the active ownership lock.
func updateExternalRunIDTx(ctx context.Context, tx *sql.Tx, row db.GetRunForUpdateRow, own ExecutionOwnership, externalID string) error {
	if externalID == "" {
		return nil
	}
	switch {
	case row.ExternalRunID == "":
		if _, err := db.New(tx).UpdateRunExternalIDFenced(ctx, db.UpdateRunExternalIDFencedParams{
			ExternalRunID: externalID,
			ID:            own.RunID.Bytes(),
			LeaseEpoch:    own.LeaseEpoch,
		}); err != nil {
			return err
		}
		return nil
	case row.ExternalRunID == externalID:
		return nil
	default:
		return ErrExternalRunIDConflict
	}
}

// finishOccurrenceTx converges a scheduled run's occurrence with the run
// terminal state. A no-op is valid only when the occurrence is already in
// the same terminal state.
func finishOccurrenceTx(ctx context.Context, tx *sql.Tx, row db.GetRunForUpdateRow, runID ids.ID, runStatus string) error {
	if row.TriggerType != TriggerTypeScheduled || !row.TriggerID.Valid {
		return nil
	}
	occStatus, err := occurrenceTerminalStatus(runStatus)
	if err != nil {
		return err
	}
	res, err := db.New(tx).CASFinishOccurrenceByRun(ctx, db.CASFinishOccurrenceByRunParams{
		Status: occStatus,
		RunID:  sql.NullString{String: string(runID.Bytes()), Valid: true},
	})
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 1 {
		return nil
	}

	occ, err := db.New(tx).GetScheduleOccurrenceByID(ctx, uint64(row.TriggerID.Int64))
	if err != nil {
		return err
	}
	if !occ.RunID.Valid || occ.RunID.String != string(runID.Bytes()) || occ.Status != occStatus {
		return ErrOccurrenceStateConflict
	}
	return nil
}

// deleteOwnedLeaseTx makes lease cleanup transaction-fatal and checks
// RowsAffected. Worker-owned paths must delete exactly their own lease.
func deleteOwnedLeaseTx(ctx context.Context, q *db.Queries, own ExecutionOwnership) error {
	res, err := q.DeleteLeaseByToken(ctx, db.DeleteLeaseByTokenParams{
		RunID:      own.RunID.Bytes(),
		LeaseToken: own.LeaseToken.Bytes(),
	})
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrLostOwnership
	}
	return nil
}

func occurrenceTerminalStatus(runStatus string) (string, error) {
	switch runStatus {
	case StatusSucceeded:
		return StatusSucceeded, nil
	case StatusFailed, StatusCancelled:
		// The occurrence contract has no cancelled state in v1; both run
		// failures converge to failed without silently remapping success.
		return StatusFailed, nil
	default:
		return "", ErrInvalidTerminalStatus
	}
}
