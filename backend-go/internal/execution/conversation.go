package execution

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
)

// Conversation lifecycle errors (评测 P1).
var (
	// ErrConversationHasActiveRun: the conversation still has a
	// NON-TERMINAL run (anything not cancelled/succeeded/failed —
	// 第四轮 P2); deleting it would race the worker's finalize.
	ErrConversationHasActiveRun = errors.New("execution: conversation has an active run")
	// ErrConversationHasScheduledRuns: the conversation holds Runs created
	// by the scheduler (they own an occurrence and may still owe a
	// delivery) — Runs are execution-audit entities and must not disappear
	// because a sidebar entry was deleted.
	ErrConversationHasScheduledRuns = errors.New("execution: conversation has scheduled runs or pending deliveries")
)

// DeleteConversationCascade hard-deletes a conversation and its children
// in ONE transaction.
//
// Guards (评测 P1-4 + P1 lifetime):
//   - takes the conversations row lock first, which serializes with
//     CreateRunInTx, so no run can appear between the checks and the delete;
//   - refuses while any run is non-terminal;
//   - refuses when the conversation holds scheduler-created runs or any
//     delivery execution: schedule_occurrences.run_id and the delivery
//     worker's GetRunByID both depend on those rows surviving.
//
// Every child delete is checked — a partial cascade is never reported as
// success.
func (s *Service) DeleteConversationCascade(ctx context.Context, conversationID, ownerUserID int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := db.New(tx)

	if _, err := q.GetConversationRowForUpdate(ctx, uint64(conversationID)); err != nil {
		return ErrConversationNotFound
	}
	conv, err := q.GetConversationByID(ctx, uint64(conversationID))
	if err != nil || conv.UserID != uint64(ownerUserID) {
		return ErrConversationNotFound
	}
	convArg := sql.NullInt64{Int64: conversationID, Valid: true}

	active, err := q.CountActiveRunsByConversation(ctx, convArg)
	if err != nil {
		return fmt.Errorf("conversation delete: active runs: %w", err)
	}
	if active > 0 {
		return ErrConversationHasActiveRun
	}
	scheduled, err := q.CountScheduledRunsByConversation(ctx, convArg)
	if err != nil {
		return fmt.Errorf("conversation delete: scheduled runs: %w", err)
	}
	deliveries, err := q.CountDeliveriesByConversation(ctx, convArg)
	if err != nil {
		return fmt.Errorf("conversation delete: deliveries: %w", err)
	}
	if scheduled > 0 || deliveries > 0 {
		return ErrConversationHasScheduledRuns
	}

	runIDs, err := q.DeleteConversationRuns(ctx, convArg)
	if err != nil {
		return fmt.Errorf("conversation delete: list runs: %w", err)
	}
	for _, runID := range runIDs {
		if err := q.DeleteRunEvents(ctx, runID); err != nil {
			return fmt.Errorf("conversation delete: events: %w", err)
		}
		if err := q.DeleteRunArtifacts(ctx, runID); err != nil {
			return fmt.Errorf("conversation delete: artifacts: %w", err)
		}
		if err := q.DeleteRunCommands(ctx, runID); err != nil {
			return fmt.Errorf("conversation delete: commands: %w", err)
		}
		if err := q.DeleteRunLeaseByRun(ctx, runID); err != nil {
			return fmt.Errorf("conversation delete: leases: %w", err)
		}
	}
	if err := q.DeleteRunsByConversation(ctx, convArg); err != nil {
		return fmt.Errorf("conversation delete: runs: %w", err)
	}
	if err := q.DeleteConversationMessages(ctx, uint64(conversationID)); err != nil {
		return fmt.Errorf("conversation delete: messages: %w", err)
	}
	if err := q.DeleteThreadByConversation(ctx, uint64(conversationID)); err != nil {
		return fmt.Errorf("conversation delete: thread: %w", err)
	}
	if err := q.DeleteSharesByConversation(ctx, uint64(conversationID)); err != nil {
		return fmt.Errorf("conversation delete: shares: %w", err)
	}
	if err := q.UnbindConversationAttachments(ctx, convArg); err != nil {
		return fmt.Errorf("conversation delete: attachments: %w", err)
	}
	if err := q.DeleteConversation(ctx, uint64(conversationID)); err != nil {
		return fmt.Errorf("conversation delete: conversation: %w", err)
	}
	return tx.Commit()
}

// ClearConversation resets a conversation: messages AND the agent thread
// go together so the next turn starts a NEW provider session (评测 P1-3:
// "clear" means restart, not "hide local history"). Refused while a run is
// active — an in-flight answer would otherwise reappear in a conversation
// the user just emptied.
func (s *Service) ClearConversation(ctx context.Context, conversationID, ownerUserID int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := db.New(tx)

	if _, err := q.GetConversationRowForUpdate(ctx, uint64(conversationID)); err != nil {
		return ErrConversationNotFound
	}
	conv, err := q.GetConversationByID(ctx, uint64(conversationID))
	if err != nil || conv.UserID != uint64(ownerUserID) {
		return ErrConversationNotFound
	}
	active, err := q.CountActiveRunsByConversation(ctx, sql.NullInt64{Int64: conversationID, Valid: true})
	if err != nil {
		return fmt.Errorf("conversation clear: active runs: %w", err)
	}
	if active > 0 {
		return ErrConversationHasActiveRun
	}
	if err := q.DeleteConversationMessages(ctx, uint64(conversationID)); err != nil {
		return fmt.Errorf("conversation clear: messages: %w", err)
	}
	if err := q.DeleteThreadByConversation(ctx, uint64(conversationID)); err != nil {
		return fmt.Errorf("conversation clear: thread: %w", err)
	}
	if err := q.TouchConversationUpdated(ctx, uint64(conversationID)); err != nil {
		return fmt.Errorf("conversation clear: touch: %w", err)
	}
	return tx.Commit()
}
