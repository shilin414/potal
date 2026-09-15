package execution

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"

	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/dbtypes"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
)

// FinalizeOwnedRun applies the terminal transition as ONE database
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
		return ErrInvalidTerminalStatus
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
	row, terminal, err := verifyFinalizeOwnershipTx(ctx, tx, own)
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
	terminalEvent, err := terminalEventName(in.Status)
	if err != nil {
		return err
	}
	// The terminal event is written first, at an explicitly allocated
	// sequence: the run row is already locked by this transaction, so the
	// allocation is O(1) and cannot interleave with another writer
	// (第九轮 P1-3).
	sequence, err := allocEventSequenceTx(ctx, tx, own.RunID, 0)
	if err != nil {
		return err
	}
	if err := insertRunEventTx(ctx, tx, own.RunID, sequence, terminalEvent, terminalPayload); err != nil {
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

	// NOTE (第七轮 P2-1): nothing reads the row for metrics here. The
	// duration observation lives below, AFTER the commit — a read inside
	// this transaction (however innocent) is a precondition for
	// terminalization and can roll the terminal transition back.

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
		// Sidebar ordering (评测 §十三): the assistant answer moves the
		// conversation to the top of the list in the same commit.
		if err := q.TouchConversationUpdated(ctx, uint64(row.ConversationID.Int64)); err != nil {
			return err
		}
	}

	// 5. Occurrence convergence (scheduled runs): the occurrence follows
	// the run into its terminal state; delivery state stays separate.
	isScheduled := row.TriggerType == TriggerTypeScheduled && row.TriggerID.Valid
	if isScheduled {
		if err := finishOccurrenceTx(ctx, tx, row, own.RunID, in.Status); err != nil {
			return err
		}
	}

	// Delivery requests become durable in the same commit as terminal
	// success. The callback is pure database work; external sending stays
	// in the delivery worker and can never make a completed run fail.
	if isScheduled && in.Status == StatusSucceeded && s.CreateDeliveryExecutionsTx != nil {
		current := runFromLockedRow(run, row)
		if err := s.CreateDeliveryExecutionsTx(ctx, tx, current); err != nil {
			return err
		}
	}

	// 6. Lease cleanup — same transaction: a terminal run never keeps a lease.
	if err := deleteOwnedLeaseTx(ctx, q, own); err != nil {
		return err
	}

	// 7. Provider capacity cleanup — same transaction: a terminal run holds
	// no provider slot, so the invariant "active provider execution ⇔
	// active provider_execution_slots row" holds without waiting for the
	// worker's deferred release.
	if _, err := q.DeleteProviderSlotsUpToEpoch(ctx, db.DeleteProviderSlotsUpToEpochParams{
		RunID:      own.RunID.Bytes(),
		LeaseEpoch: own.LeaseEpoch,
	}); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	// ── post-commit side effects (best-effort, never correctness) ──
	s.publishLive(ctx, own.RunID, sequence, terminalEvent, terminalPayload)
	if s.Metrics != nil {
		s.Metrics.RunTotal.WithLabelValues(run.Provider, in.Status).Inc()
		// studio_run_duration is re-read from the ROW (not the caller's
		// snapshot) so both bounds stay on the DB clock: started_at was
		// stamped by MySQL in MarkRunStartedOwned, finished_at by the
		// CAS above. Measuring the pair against time.Now() mixed two
		// clock authorities and host skew inflated, shrank or even
		// negated the observed duration.
		s.observeRunDuration(ctx, own.RunID, run.Provider, in.Status)
	}
	return nil
}

// RunDurationObservationTimeout bounds the detached duration read. It is
// short because the observation is worth nothing next to the caller's
// latency, and finite because a wedged database must not pin a worker.
const RunDurationObservationTimeout = 2 * time.Second

// observeRunDuration records studio_run_duration for a run that just
// reached a terminal state (第七轮 P2-1).
//
// It runs strictly AFTER the finalize commit, on its own detached
// connection, and every failure mode is a WARNING: a metrics read that
// cannot complete must cost one missing sample and nothing else — never a
// rolled-back terminal transition with the run back in `running`.
//
// The context is DETACHED from the caller (NewCleanupContext: keeps values
// for log/trace correlation, drops cancellation and deadline) for the same
// reason cleanup writes are: a cancelled execution context or a
// disconnected caller must not silently suppress the observation. Both
// timestamps come from the DB clock, and a missing or non-monotonic pair is
// dropped rather than observed as a bogus sample.
func (s *Service) observeRunDuration(ctx context.Context, runID ids.ID, provider, status string) {
	if s.Metrics == nil {
		return
	}
	log := s.Log
	if log == nil {
		log = slog.Default()
	}

	obsCtx, cancel := NewCleanupContext(ctx)
	defer cancel()
	obsCtx, cancelTimeout := context.WithTimeout(obsCtx, RunDurationObservationTimeout)
	defer cancelTimeout()

	startedAt, finishedAt, err := s.runTimestamps(obsCtx, runID)
	if err != nil {
		log.Warn("run duration observation skipped",
			"run_id", runID.String(), "provider", provider, "status", status, "err", err)
		return
	}
	seconds, ok := dbClockDuration(startedAt, finishedAt)
	if !ok {
		// No sample is the correct outcome for a run that never started
		// (killed before it was allowed to execute) or a clock-skewed
		// pair; a zero or negative duration would poison the histogram.
		return
	}
	s.Metrics.RunDuration.WithLabelValues(provider, status).Observe(seconds)
}

// runTimestamps reads the persisted DB-clock bounds of a run. The nil seam
// uses the SQL querier; tests inject a failing reader to prove a metric
// read error can never roll back the finalize transaction (第七轮 P2-1).
func (s *Service) runTimestamps(ctx context.Context, runID ids.ID) (sql.NullTime, sql.NullTime, error) {
	if s.RunDurationTimestamps != nil {
		return s.RunDurationTimestamps(ctx, runID)
	}
	row, err := s.q(ctx).GetRunTimestamps(ctx, runID.Bytes())
	if err != nil {
		return sql.NullTime{}, sql.NullTime{}, err
	}
	return row.StartedAt, row.FinishedAt, nil
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

// terminalEventName maps a terminal status to its terminal event. Retry
// never produces a terminal event (修复计划 §16: terminal events must be
// unique).
//
// The legacy `interrupted` status is deliberately ABSENT (第五轮 P2-1): it
// is a pre-closure alias that new code never writes and that migration
// 0018 rewrites to `failed`, and FinalizeOwnedRun already rejects it via
// IsTerminal. Keeping the case would resurrect the two-contract conflict
// ("interrupted is live" vs "interrupted is a terminal failure").
//
// An unknown status is a programming error and returns
// ErrInvalidTerminalStatus. Defaulting it to "success" would let a bug
// fabricate a run.completed event for a state nobody defined.
func terminalEventName(status string) (string, error) {
	switch status {
	case StatusSucceeded:
		return EventRunCompleted, nil
	case StatusCancelled:
		return EventRunCancelled, nil
	case StatusFailed:
		return EventRunFailed, nil
	default:
		return "", ErrInvalidTerminalStatus
	}
}

func assistantMetadata(runID, provider string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"run_id": runID, "provider": provider})
	return raw
}
