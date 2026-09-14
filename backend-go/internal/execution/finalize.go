package execution

import (
	"context"
	"database/sql"
	"encoding/json"

	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/dbtypes"
)

// FinalizeOwnedRun applies the terminal transition as ONE TiDB
// transaction (修复计划 §29-32, Phase 6):
//
//  1. verify ownership (run row locked FOR UPDATE, epoch checked)
//  2. CAS runs running → succeeded/failed/cancelled
//  3. INSERT the terminal RunEvent          (durable — invariant D)
//  4. INSERT the assistant Message           (durable — invariant: a
//     succeeded run keeps its answer)
//  5. Update the ScheduleOccurrence terminal (scheduled runs)
//  6. DELETE the caller's own lease row
//     COMMIT
//
// Redis pub/sub fan-out, metrics and the delivery hook run AFTER commit —
// they are latency/UX concerns, never correctness.
//
// Idempotency: if the run is already terminal the transaction is a no-op
// and nil is returned (a duplicate dispatch losing the race must not
// error). A non-terminal run owned by someone else returns
// ErrLostOwnership and the caller must stop writing.
func (s *Service) FinalizeOwnedRun(ctx context.Context, run *Run, own ExecutionOwnership, in *FinishInput) error {
	if !own.Valid() {
		return ErrLostOwnership
	}
	if !IsTerminal(in.Status) {
		in.Status = StatusFailed // never persist a non-terminal via finalize
	}

	output := in.Output
	outputJSON := dbtypes.JSONText([]byte("null"))
	if output != nil {
		raw, _ := json.Marshal(output)
		outputJSON = dbtypes.JSONText(raw)
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := db.New(tx)

	// 1. Ownership verification under the row lock.
	row, terminal, err := verifyOwnershipTx(ctx, tx, own)
	if err != nil {
		return err
	}
	if terminal {
		return nil // already finalized elsewhere — idempotent no-op
	}

	// 2. Terminal RunEvent — same transaction as the CAS: a terminal run
	// ALWAYS has its terminal event (评测 §十九 durability gap). NOTE:
	// appended BEFORE the CAS because the fence predicate requires
	// status='running' — we hold the row lock and have verified the epoch,
	// so the CAS below cannot lose.
	terminalPayload := map[string]any{
		"status":          in.Status,
		"provider_status": in.ProviderStatus,
		"finish_reason":   in.FinishReason,
	}
	if in.ErrorCode != "" {
		terminalPayload["error_code"] = in.ErrorCode
	}
	if in.ErrorMessage != "" {
		terminalPayload["error_message"] = in.ErrorMessage
	}
	if output != nil {
		if t, ok := output["text"].(string); ok && t != "" {
			terminalPayload["text"] = t
		}
	}
	sequence, err := appendEventTx(ctx, tx, own.RunID, own.LeaseEpoch, terminalEventName(in.Status), terminalPayload)
	if err != nil {
		return err
	}

	// 3. Terminal CAS (fenced; the row is locked so it must win).
	if _, err := q.CASFinishRunFenced(ctx, db.CASFinishRunFencedParams{
		Status:               in.Status,
		Output:               outputJSON,
		ProviderStatus:       in.ProviderStatus,
		ProviderFinishReason: in.FinishReason,
		ErrorCode:            in.ErrorCode,
		ErrorMessage:         nullText(in.ErrorMessage),
		ID:                   own.RunID.Bytes(),
		LeaseEpoch:           own.LeaseEpoch,
	}); err != nil {
		return err
	}

	// 4. Assistant message durability: inserted in the SAME transaction
	// (评测 §二十 — previously Run succeeded + crash could lose the answer).
	if in.AssistantText != "" && row.ConversationID.Valid {
		meta := in.AssistantMetadata
		if len(meta) == 0 {
			meta = assistantMetadata(own.RunID.String(), run.Provider)
		}
		if _, err := q.CreateMessage(ctx, db.CreateMessageParams{
			ConversationID: uint64(row.ConversationID.Int64),
			Role:           "assistant",
			Content:        in.AssistantText,
			Metadata:       dbtypes.JSONText(meta),
		}); err != nil {
			return err
		}
	}

	// 5. Occurrence convergence (scheduled runs): the occurrence follows
	// the run into its terminal state; delivery state stays separate.
	if row.TriggerType == TriggerTypeScheduled && row.TriggerID.Valid {
		occStatus := StatusFailed
		if in.Status == StatusSucceeded {
			occStatus = StatusSucceeded
		}
		_, _ = q.CASFinishOccurrenceByRun(ctx, db.CASFinishOccurrenceByRunParams{
			Status: occStatus,
			RunID:  sql.NullString{String: string(own.RunID.Bytes()), Valid: true},
		})
	}

	// 6. Lease cleanup — same transaction: a terminal run never keeps a lease.
	_, _ = q.DeleteLeaseByToken(ctx, db.DeleteLeaseByTokenParams{
		RunID: own.RunID.Bytes(), LeaseToken: own.LeaseToken.Bytes(),
	})

	if err := tx.Commit(); err != nil {
		return err
	}

	// ── post-commit side effects (best-effort, never correctness) ──
	s.publishLive(ctx, own.RunID, sequence, terminalEventName(in.Status), terminalPayload)
	if s.Metrics != nil {
		s.Metrics.RunTotal.WithLabelValues(run.Provider, in.Status).Inc()
		if !run.StartedAt.IsZero() {
			s.Metrics.RunDuration.
				WithLabelValues(run.Provider, in.Status).
				Observe(timeSinceSeconds(*run.StartedAt))
		}
	}
	// Delivery fan-out: only the finalize winner triggers, only scheduled
	// runs have deliveries, and a hook panic must never fail the run.
	if in.Status == StatusSucceeded && s.OnRunSucceeded != nil &&
		run.TriggerType == TriggerTypeScheduled && run.TriggerID != nil && run.UserID != nil {
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					s.Log.Error("run succeeded hook panicked", slogKey("run_id"), own.RunID.String(), slogKey("panic"), rec)
				}
			}()
			s.OnRunSucceeded(ctx, run, output)
		}()
	}
	return nil
}

// FailOwnedRun drives the run into the terminal failed state through the
// same atomic finalize transaction.
func (s *Service) FailOwnedRun(ctx context.Context, run *Run, own ExecutionOwnership, errorCode, message string) error {
	return s.FinalizeOwnedRun(ctx, run, own, &FinishInput{
		Status:       StatusFailed,
		ErrorCode:    errorCode,
		ErrorMessage: message,
	})
}

// terminalEventName maps a terminal status to its terminal event. The
// legacy interrupted status maps to run.failed — retry never produces a
// terminal event (修复计划 §16: terminal events must be unique).
func terminalEventName(status string) string {
	switch status {
	case StatusFailed, StatusInterrupted:
		return EventRunFailed
	default:
		return EventRunCompleted // succeeded / cancelled
	}
}

func assistantMetadata(runID, provider string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"run_id": runID, "provider": provider})
	return raw
}
