package execution

import (
	"context"
	"database/sql"
	"errors"

	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
)

// ExecutionOwnership is the immutable write fence produced by ClaimRun.
//
// It represents "this worker's permission to modify the run's canonical
// state" — NOT run business data. Once produced by a winning claim it is
// never refreshed, never re-read from the database, and can never be
// adopted by a stale worker (修复计划 §5-7). The zero value is invalid:
// only the system plane (reaper, tests) may write without it, and that
// plane does not go through WorkerOwnedService.
type ExecutionOwnership struct {
	RunID      ids.ID
	WorkerID   string
	LeaseEpoch uint64
	LeaseToken ids.ID
}

// Valid reports whether the fence is usable (epoch must be ≥ 1 — epoch 0
// is the unfenced system plane and must never reach a worker write path).
func (o ExecutionOwnership) Valid() bool {
	return o.LeaseEpoch > 0 && !o.LeaseToken.IsZero() && !o.RunID.IsZero()
}

// ClaimedRun couples the execution data with the ownership that claimed
// it. Reloaded run data can replace Run, but Ownership is fixed for the
// attempt's lifetime — a GetRun refresh can never silently drop the
// lease token again (评测: Background execution loses ownership).
type ClaimedRun struct {
	Run       *Run
	Ownership ExecutionOwnership
}

// RefreshRun replaces the execution data while keeping the immutable
// fence. The refreshed run must belong to the same run id.
func (c *ClaimedRun) RefreshRun(run *Run) {
	if run == nil || run.ID != c.Ownership.RunID {
		return
	}
	c.Run = run
}

// ───────────────────────────────────────────── worker-owned write plane ──

// WorkerOwnedService is the ONLY canonical-write surface a provider
// executor may hold (修复计划 §81: 编译期约束优先于 Code Review 约束).
// Every method verifies ExecutionOwnership against the database before
// writing; there is deliberately NO method here that can bypass the
// fence — ErrLostOwnership is the only failure mode for stale writers.
type WorkerOwnedService struct {
	svc *Service
}

// WorkerOwned returns the fenced write plane over the service.
func (s *Service) WorkerOwned() *WorkerOwnedService { return &WorkerOwnedService{svc: s} }

// GetRun reloads execution data (safe read).
func (w *WorkerOwnedService) GetRun(ctx context.Context, id ids.ID) (*Run, error) {
	return w.svc.GetRun(ctx, id)
}

// AppendEvent appends a durable run event under the ownership fence.
func (w *WorkerOwnedService) AppendEvent(ctx context.Context, claimed *ClaimedRun, eventType string, payload map[string]any) error {
	return w.svc.AppendOwnedEvent(ctx, claimed.Ownership, eventType, payload)
}

// PublishTransient fans a high-frequency (non-durable) event to live SSE
// consumers only. No canonical state is written, so no fence applies.
func (w *WorkerOwnedService) PublishTransient(ctx context.Context, claimed *ClaimedRun, eventType string, payload map[string]any) {
	w.svc.PublishTransient(ctx, claimed.Run.ID, eventType, payload)
}

// UpdateExternalRunID stamps the provider chat id (set-once) under the fence.
func (w *WorkerOwnedService) UpdateExternalRunID(ctx context.Context, claimed *ClaimedRun, externalID string) error {
	return w.svc.UpdateExternalRunIDOwned(ctx, claimed.Ownership, externalID)
}

// Retry requeues the run for another attempt (non-terminal run.retrying).
func (w *WorkerOwnedService) Retry(ctx context.Context, claimed *ClaimedRun, reason string) error {
	return w.svc.RetryOwnedRun(ctx, claimed.Run, claimed.Ownership, reason)
}

// BeginProviderAttempt consumes one provider execution attempt under the
// ownership fence and updates the in-memory claim so the executor's
// retry-budget decisions see the fresh count (P0-2). Call it immediately
// before the provider submit, never at claim time.
func (w *WorkerOwnedService) BeginProviderAttempt(ctx context.Context, claimed *ClaimedRun) error {
	attempt, err := w.svc.BeginProviderAttemptOwned(ctx, claimed.Ownership)
	if err != nil {
		return err
	}
	claimed.Run.Attempt = attempt
	return nil
}

// Fail drives the run into the terminal failed state.
func (w *WorkerOwnedService) Fail(ctx context.Context, claimed *ClaimedRun, errorCode, message string) error {
	return w.svc.FailOwnedRun(ctx, claimed.Run, claimed.Ownership, errorCode, message)
}

// Finalize drives the terminal transition: run CAS + terminal event +
// assistant message + occurrence + lease cleanup in ONE transaction.
func (w *WorkerOwnedService) Finalize(ctx context.Context, claimed *ClaimedRun, in *FinishInput) error {
	return w.svc.FinalizeOwnedRun(ctx, claimed.Run, claimed.Ownership, in)
}

// PersistArtifact upserts a RunArtifact and emits artifact.discovered
// with the ACTUAL stored row id, under the ownership fence.
func (w *WorkerOwnedService) PersistArtifact(ctx context.Context, claimed *ClaimedRun, art ArtifactInput) (ids.ID, error) {
	return w.svc.PersistArtifactOwned(ctx, claimed.Ownership, art)
}

// ListRunArtifacts loads the run's artifacts (safe read).
func (w *WorkerOwnedService) ListRunArtifacts(ctx context.Context, runID ids.ID) ([]db.RunArtifact, error) {
	return w.svc.q(ctx).ListRunArtifacts(ctx, runID.Bytes())
}

// BindProviderSession binds the provider session id onto the
// conversation's agent thread — set-once / idempotent, never a blind
// overwrite, and fenced by run ownership (修复计划 §27-28).
func (w *WorkerOwnedService) BindProviderSession(ctx context.Context, claimed *ClaimedRun, threadID ids.ID, sessionID string) error {
	return w.svc.BindProviderSessionOwned(ctx, claimed.Ownership, threadID, sessionID)
}

// EnsureAgentThread loads (or lazily creates) the conversation's agent
// thread and enforces the identity invariant: one
// user/agent/provider/auth-subject per thread. Thread creation is
// conversation-scoped bookkeeping, not run canonical state.
//
// Concurrency (评测 P0-2): the create path is an atomic get-or-create —
// UNIQUE(conversation_id) decides a race, the loser re-reads the winner's
// row instead of failing. Combined with the conversation-level run
// serialization this makes "two remote sessions for one conversation"
// unreachable.
func (w *WorkerOwnedService) EnsureAgentThread(ctx context.Context, conversationID int64, provider, authMode, subjectKey string) (threadID ids.ID, remoteID string, err error) {
	if conversationID == 0 {
		return ids.ID{}, "", nil
	}
	q := w.svc.q(ctx)
	row, getErr := q.GetAgentThreadByConversation(ctx, uint64(conversationID))
	if errors.Is(getErr, sql.ErrNoRows) {
		newID := ids.New()
		if _, cErr := q.CreateAgentThread(ctx, db.CreateAgentThreadParams{
			ID:             newID.Bytes(),
			ConversationID: uint64(conversationID),
			Provider:       provider,
			AuthMode:       authMode,
			AuthSubjectKey: subjectKey,
		}); cErr != nil {
			// A concurrent first turn won the UNIQUE(conversation_id)
			// race — re-read its row rather than failing the run.
			row, getErr = q.GetAgentThreadByConversation(ctx, uint64(conversationID))
			if getErr != nil {
				return ids.ID{}, "", cErr
			}
			return w.checkThreadIdentity(row, provider, authMode, subjectKey)
		}
		return newID, "", nil
	}
	if getErr != nil {
		return ids.ID{}, "", getErr
	}
	return w.checkThreadIdentity(row, provider, authMode, subjectKey)
}

// checkThreadIdentity rejects reusing a thread created under a different
// identity/provider — never leak another user's provider session.
func (w *WorkerOwnedService) checkThreadIdentity(row db.AgentThread, provider, authMode, subjectKey string) (ids.ID, string, error) {
	if row.Provider != provider || row.AuthMode != authMode || row.AuthSubjectKey != subjectKey {
		return ids.ID{}, "", errors.New("conversation thread identity/provider mismatch")
	}
	tid := ids.ID{}
	_ = tid.Scan(row.ID)
	return tid, row.RemoteID, nil
}
