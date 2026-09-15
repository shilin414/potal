package execution

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/dbtypes"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
	"github.com/creation-agent-studio/backend-go/internal/platform/redisx"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
)

// ErrNotFound marks missing runs.
var ErrNotFound = errors.New("execution: not found")

// ErrLostOwnership marks a fenced write rejected because the caller no
// longer owns the run (lease expired / reclaimed). Writers must stop
// immediately on this error — another worker owns the canonical state.
var ErrLostOwnership = errors.New("execution: lease ownership lost")

var ErrExternalRunIDConflict = errors.New("execution: external run id conflict")

var ErrOccurrenceStateConflict = errors.New("execution: schedule occurrence state conflict")

// ErrInvalidTerminalStatus marks an attempt to finalize a run with a
// non-terminal status.
var ErrInvalidTerminalStatus = errors.New("execution: invalid terminal status")

// ErrProviderAttemptsExhausted marks a provider execution attempt that
// was refused because the run's retry budget is used up. The caller must
// fail the run, not retry it.
var ErrProviderAttemptsExhausted = errors.New("execution: provider attempts exhausted")

// ErrConversationBusy marks a CreateRun rejected because the conversation
// already has a queued/running run (评测 P0-2). One conversation executes
// one turn at a time: the provider-side session must never be addressed by
// two concurrent runs, and answers must land in submission order.
var ErrConversationBusy = errors.New("execution: previous turn is still running")

// ErrConversationNotFound marks a CreateRun whose conversation vanished
// (deleted) or does not belong to the caller (评测 P1-4 lifetime race).
var ErrConversationNotFound = errors.New("execution: conversation not found")

// ErrAttachmentClaimed marks a CreateRun rejected because one of its
// attachments was concurrently claimed by another run (评测 P1-5). The
// whole run creation rolls back.
var ErrAttachmentClaimed = errors.New("execution: attachment already claimed")

// ErrUserOutstandingExceeded marks a CreateRun refused by the per-user
// outstanding-run cap (评测 P1-7). The check is atomic with the insert.
var ErrUserOutstandingExceeded = errors.New("execution: too many outstanding runs")

// Service implements the Run lifecycle on top of MySQL.
type Service struct {
	DB      *sql.DB
	Redis   *redisx.Client
	Log     *slog.Logger
	Metrics *telemetry.Metrics

	// RequeueDelay is the single source of truth for retry timing:
	// RetryOwnedRun writes Run.available_at = now+RequeueDelay AND the
	// dispatch outbox row's available_at = the same instant, so the relay
	// publishes the wakeup exactly when the run becomes claimable
	// (P1-2). Zero falls back to DefaultRequeueDelay.
	RequeueDelay time.Duration

	// CreateDeliveryExecutionsTx is an in-transaction extension used by
	// finalize to make scheduled delivery requests durable before commit.
	// Delivery execution is decoupled in a separate delivery package.
	CreateDeliveryExecutionsTx func(ctx context.Context, tx *sql.Tx, run *Run) error

	// RunDurationTimestamps reads the persisted DB-clock
	// started_at/finished_at that the duration metric observes AFTER the
	// finalize commit (第七轮 P2-1). Nil uses the SQL querier.
	//
	// It exists as a seam (same idea as aily.ProviderAPI): the property
	// that has to hold is "a failing metric read can never roll back a
	// terminal transition", and the only way to prove that against a real
	// database is to fail the read on purpose.
	RunDurationTimestamps func(ctx context.Context, runID ids.ID) (startedAt, finishedAt sql.NullTime, err error)
}

// DefaultRequeueDelay is used when Service.RequeueDelay is unset.
const DefaultRequeueDelay = time.Second

func (s *Service) requeueDelay() time.Duration {
	if s.RequeueDelay > 0 {
		return s.RequeueDelay
	}
	return DefaultRequeueDelay
}

func NewService(d *sql.DB, r *redisx.Client, log *slog.Logger, m *telemetry.Metrics) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{DB: d, Redis: r, Log: log, Metrics: m}
}

func (s *Service) q(ctx context.Context) db.Querier { return db.New(s.DB) }

// CreateRunInput carries everything needed to enqueue a run.
type CreateRunInput struct {
	UserID            int64
	ApplicationID     int64
	ConversationID    int64
	RuntimeBindingID  int64
	Provider          string
	RuntimeType       string
	ExecutionMode     string
	Content           string
	ContentItems      []map[string]any
	AttachmentIDs     []string // studio attachment ids (owned, unbound)
	ConversationTitle string
	// CreateConversation asks CreateRunInTx to create the conversation
	// inside the same transaction when ConversationID is 0 (评测 P1-5:
	// no orphan conversation when the run creation fails).
	CreateConversation bool
	RuntimeSnapshot    map[string]any
	MaxAttempts        int64
	// Scheduling provenance: zero values mean an interactive run.
	TriggerType string // "interactive_user" | "scheduled" | ...
	TriggerID   int64  // schedule_occurrences.id for scheduled runs
	Priority    string // "interactive_user" | "scheduled_normal" | ...
	AvailableAt time.Time
}

// DefaultTriggerType is used when a run input omits trigger info.
const DefaultTriggerType = "interactive_user"

// TriggerTypeScheduled marks runs created by the scheduler (kept local to
// avoid an execution → automation import cycle).
const TriggerTypeScheduled = "scheduled"

// DefaultPriority keeps queue fairness explicit at the call sites.
const DefaultPriority = "interactive_user"

// CreateRun atomically persists user message + run + outbox event
// (§22). The outbox row guarantees dispatch even if Redis is down.
func (s *Service) CreateRun(ctx context.Context, in *CreateRunInput) (*Run, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	runID, err := s.CreateRunInTx(ctx, tx, in)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetRun(ctx, runID)
}

// CreateRunAdmitted creates a run under the per-user outstanding cap
// (评测 P1-7). The user row is locked, the NON-TERMINAL run count is read
// under that lock and the run is inserted in the SAME transaction, so N
// concurrent submits can no longer all observe the same count and
// overshoot the cap. maxOutstanding <= 0 disables the cap.
//
// NON-TERMINAL means "not cancelled/succeeded/failed" (第四轮 P2): the
// cap is a concurrency bound on live work, not on two specific statuses.
func (s *Service) CreateRunAdmitted(ctx context.Context, in *CreateRunInput, maxOutstanding int) (*Run, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := db.New(tx)

	if maxOutstanding > 0 && in.UserID != 0 {
		if _, err := q.LockUserRow(ctx, uint64(in.UserID)); err != nil {
			return nil, fmt.Errorf("user admission lock: %w", err)
		}
		n, err := q.CountOutstandingRunsByUser(ctx, sql.NullInt64{Int64: in.UserID, Valid: true})
		if err != nil {
			return nil, fmt.Errorf("user admission count: %w", err)
		}
		if n >= int64(maxOutstanding) {
			return nil, ErrUserOutstandingExceeded
		}
	}
	runID, err := s.CreateRunInTx(ctx, tx, in)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetRun(ctx, runID)
}

// CreateRunInTx writes message + run + outbox inside a caller-owned
// transaction so composite units (e.g. scheduler occurrence + run) commit
// atomically. The caller owns commit/rollback.
//
// Admission guarantees enforced here (评测 P0-2 / P1-5):
//   - at most ONE non-terminal run per conversation (serialized turns);
//   - attachments are claimed with a run_id IS NULL guard, so a losing
//     concurrent CreateRun rolls back instead of leaving a run whose input
//     lists an attachment bound to another run;
//   - the conversation's updated_at moves in the same transaction so the
//     sidebar order is correct (§十三).
func (s *Service) CreateRunInTx(ctx context.Context, tx *sql.Tx, in *CreateRunInput) (ids.ID, error) {
	runID := ids.New()
	q := db.New(tx)

	convID := in.ConversationID
	// Lazy conversation creation INSIDE the transaction: a failed run
	// (e.g. a lost attachment claim) must not strand an empty conversation.
	if convID == 0 && in.CreateConversation {
		var appArg sql.NullInt64
		if in.ApplicationID != 0 {
			appArg = sql.NullInt64{Int64: in.ApplicationID, Valid: true}
		}
		res, err := q.CreateConversation(ctx, db.CreateConversationParams{
			UserID:        uint64(in.UserID),
			ApplicationID: appArg,
			Title:         in.ConversationTitle,
		})
		if err != nil {
			return runID, fmt.Errorf("insert conversation: %w", err)
		}
		if convID, err = res.LastInsertId(); err != nil {
			return runID, fmt.Errorf("insert conversation: %w", err)
		}
	}

	// Conversation admission (评测 P0-2): lock the conversation row, then
	// count its non-terminal runs. Two concurrent submits serialize on the
	// lock; the loser sees the winner's queued run and is rejected.
	if convID != 0 {
		if _, err := q.GetConversationRowForUpdate(ctx, uint64(convID)); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return runID, ErrConversationNotFound
			}
			return runID, fmt.Errorf("conversation admission: %w", err)
		}
		active, err := q.CountActiveRunsByConversation(ctx, sql.NullInt64{Int64: convID, Valid: true})
		if err != nil {
			return runID, fmt.Errorf("conversation admission: %w", err)
		}
		if active > 0 {
			return runID, ErrConversationBusy
		}
	}

	contentItems := in.ContentItems
	if contentItems == nil {
		contentItems = []map[string]any{{"type": "text", "text": in.Content}}
	}
	input := map[string]any{
		"content": contentItems,
		"mode":    defaultStr(in.ExecutionMode, "interactive"),
	}
	if len(in.AttachmentIDs) > 0 {
		ids0 := make([]any, 0, len(in.AttachmentIDs))
		for _, a := range in.AttachmentIDs {
			ids0 = append(ids0, a)
		}
		input["agent_attachment_ids"] = ids0
	}

	priority := in.Priority
	if priority == "" {
		priority = DefaultPriority
	}
	// priority_class routes the dispatch to the matching priority stream
	// (P1-1): the relay maps provider + priority_class → stream, so
	// interactive runs jump ahead of a scheduled backlog on the normal
	// Redis path, not only in the fallback scan.
	outboxPayload := map[string]any{
		"run_id":         runID.String(),
		"provider":       in.Provider,
		"priority_class": PriorityClassOf(priority),
	}
	outboxJSON, _ := json.Marshal(outboxPayload)
	inputJSON, _ := json.Marshal(input)
	snapshotJSON, _ := json.Marshal(defaultMap(in.RuntimeSnapshot))

	if _, err := q.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: uint64(convID),
		Role:           "user",
		Content:        in.Content,
		Metadata:       dbtypes.JSONText(userMessageMetadata(runID, in.AttachmentIDs)),
	}); err != nil {
		return runID, fmt.Errorf("insert message: %w", err)
	}
	maxAttempts := in.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	triggerType := in.TriggerType
	if triggerType == "" {
		triggerType = DefaultTriggerType
	}
	var availableAt sql.NullTime
	if !in.AvailableAt.IsZero() {
		availableAt = sql.NullTime{Time: in.AvailableAt, Valid: true}
	}
	var convArg sql.NullInt64
	if convID != 0 {
		convArg = sql.NullInt64{Int64: convID, Valid: true}
	}
	if _, err := q.CreateRun(ctx, db.CreateRunParams{
		ID:               runID.Bytes(),
		UserID:           nullInt64(&in.UserID),
		ApplicationID:    nullInt64(&in.ApplicationID),
		ConversationID:   convArg,
		RuntimeBindingID: nullInt64(&in.RuntimeBindingID),
		Provider:         in.Provider,
		RuntimeType:      in.RuntimeType,
		Input:            dbtypes.JSONText(inputJSON),
		RuntimeSnapshot:  dbtypes.JSONText(snapshotJSON),
		MaxAttempts:      uint32(maxAttempts),
		TriggerType:      triggerType,
		TriggerID:        nullInt64NZ(in.TriggerID),
		Priority:         priority,
		AvailableAt:      availableAt,
	}); err != nil {
		return runID, fmt.Errorf("insert run: %w", err)
	}

	// Attachment claim (评测 P1-5): atomic, inside the run's transaction.
	// Any failure rolls the whole run back — never a run whose input
	// references an attachment owned by another run.
	for _, raw := range in.AttachmentIDs {
		attID, err := ids.Parse(raw)
		if err != nil {
			return runID, fmt.Errorf("attachment claim: %w", err)
		}
		res, err := q.ClaimAttachmentForRun(ctx, db.ClaimAttachmentForRunParams{
			RunID:          sql.NullString{String: string(runID.Bytes()), Valid: true},
			ConversationID: convArg,
			ID:             attID.Bytes(),
			CreatedBy:      sql.NullInt64{Int64: in.UserID, Valid: true},
		})
		if err != nil {
			return runID, fmt.Errorf("attachment claim: %w", err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return runID, ErrAttachmentClaimed
		}
	}

	if _, err := q.CreateOutboxEvent(ctx, db.CreateOutboxEventParams{
		Aggregate:   "run",
		AggregateID: runID.Bytes(),
		EventType:   "run.dispatch",
		Payload:     dbtypes.JSONText(outboxJSON),
	}); err != nil {
		return runID, fmt.Errorf("insert outbox: %w", err)
	}

	// Sidebar ordering (评测 §十三): the conversation must bubble to the
	// top when a message lands.
	if convID != 0 {
		if err := q.TouchConversationUpdated(ctx, uint64(convID)); err != nil {
			return runID, fmt.Errorf("touch conversation: %w", err)
		}
	}
	return runID, nil
}

func userMessageMetadata(runID ids.ID, attachmentIDs []string) json.RawMessage {
	meta := map[string]any{"run_id": runID.String()}
	if len(attachmentIDs) > 0 {
		meta["attachment_ids"] = attachmentIDs
	}
	raw, _ := json.Marshal(meta)
	return raw
}

func (s *Service) GetRun(ctx context.Context, id ids.ID) (*Run, error) {
	row, err := s.q(ctx).GetRunByID(ctx, id.Bytes())
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return runFromRow(row), nil
}

// GetRunForUser enforces ownership: unknown or foreign runs look identical (404).
func (s *Service) GetRunForUser(ctx context.Context, id ids.ID, userID int64, isStaff bool) (*Run, error) {
	run, err := s.GetRun(ctx, id)
	if err != nil {
		return nil, err
	}
	if run.UserID != nil && *run.UserID != userID && !isStaff {
		return nil, ErrNotFound
	}
	return run, nil
}

func runFromRow(row db.Run) *Run {
	out := &Run{
		ID:                   mustID(row.ID),
		Provider:             row.Provider,
		RuntimeType:          row.RuntimeType,
		ExternalRunID:        row.ExternalRunID,
		Status:               row.Status,
		ProviderStatus:       row.ProviderStatus,
		ProviderFinishReason: row.ProviderFinishReason,
		Attempt:              int64(row.Attempt),
		MaxAttempts:          int64(row.MaxAttempts),
		QueuedAt:             row.QueuedAt,
		ErrorCode:            row.ErrorCode,
		CreatedAt:            row.CreatedAt,
		UpdatedAt:            row.UpdatedAt,
	}
	if row.UserID.Valid {
		v := int64(row.UserID.Int64)
		out.UserID = &v
	}
	if row.ApplicationID.Valid {
		v := int64(row.ApplicationID.Int64)
		out.ApplicationID = &v
	}
	if row.ConversationID.Valid {
		v := int64(row.ConversationID.Int64)
		out.ConversationID = &v
	}
	if row.RuntimeBindingID.Valid {
		v := int64(row.RuntimeBindingID.Int64)
		out.RuntimeBindingID = &v
	}
	if row.StartedAt.Valid {
		t := row.StartedAt.Time
		out.StartedAt = &t
	}
	if row.FinishedAt.Valid {
		t := row.FinishedAt.Time
		out.FinishedAt = &t
	}
	out.TriggerType = row.TriggerType
	if out.TriggerType == "" {
		out.TriggerType = DefaultTriggerType
	}
	if row.TriggerID.Valid {
		v := row.TriggerID.Int64
		out.TriggerID = &v
	}
	out.Priority = row.Priority
	if out.Priority == "" {
		out.Priority = DefaultPriority
	}
	if row.AvailableAt.Valid {
		t := row.AvailableAt.Time
		out.AvailableAt = &t
	}
	_ = json.Unmarshal(row.Input, &out.Input)
	_ = json.Unmarshal(row.Output, &out.Output)
	_ = json.Unmarshal(row.RuntimeSnapshot, &out.RuntimeSnapshot)
	if out.Input == nil {
		out.Input = map[string]any{}
	}
	if out.Output == nil {
		out.Output = map[string]any{}
	}
	if out.RuntimeSnapshot == nil {
		out.RuntimeSnapshot = map[string]any{}
	}
	return out
}

func runFromLockedRow(base *Run, row db.GetRunForUpdateRow) *Run {
	if base == nil {
		base = &Run{}
	}
	base.ID = mustID(row.ID)
	if row.UserID.Valid {
		v := row.UserID.Int64
		base.UserID = &v
	}
	if row.TriggerID.Valid {
		v := row.TriggerID.Int64
		base.TriggerID = &v
	}
	base.TriggerType = row.TriggerType
	if base.TriggerType == "" {
		base.TriggerType = DefaultTriggerType
	}
	return base
}

func mustID(b []byte) ids.ID {
	var id ids.ID
	_ = id.Scan(b)
	return id
}

// ─────────────────────────────────────────────────────── claim / lease ──

// ClaimRun atomically transitions queued→running AND inserts the lease
// row (carrying the new lease_epoch) in ONE database transaction, then loads
// the run and returns it coupled with its immutable ExecutionOwnership
// (修复计划 §6). If the lease INSERT fails the whole claim rolls back and
// the run stays queued — a "running run without a lease" produced by the
// claim path is impossible by construction (评测 P0-2).
func (s *Service) ClaimRun(ctx context.Context, runID ids.ID, workerID string, leaseSeconds time.Duration) (*ClaimedRun, bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	q := db.New(tx)

	res, err := q.CASClaimRun(ctx, runID.Bytes())
	if err != nil {
		return nil, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	if n != 1 {
		return nil, false, nil // someone else won the claim
	}
	epoch, err := q.GetRunLeaseEpoch(ctx, runID.Bytes())
	if err != nil {
		return nil, false, err
	}
	token := ids.New()
	if err := q.CreateRunLease(ctx, db.CreateRunLeaseParams{
		RunID:       runID.Bytes(),
		WorkerID:    workerID,
		LeaseToken:  token.Bytes(),
		LeaseEpoch:  epoch,
		LeaseMicros: leaseSeconds.Microseconds(),
	}); err != nil {
		// Rollback: the run returns to 'queued' (lease INSERT failure can
		// never strand a running run without a lease).
		return nil, false, err
	}
	// Occurrence fan-out shares the claim transaction: running runs and
	// running occurrences can never diverge. Interactive runs have no row.
	if _, err := q.MarkOccurrenceRunningByRun(ctx, sql.NullString{String: string(runID.Bytes()), Valid: true}); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		return nil, false, err
	}
	return &ClaimedRun{
		Run: run,
		Ownership: ExecutionOwnership{
			RunID:      runID,
			WorkerID:   workerID,
			LeaseEpoch: epoch,
			LeaseToken: token,
		},
	}, true, nil
}

// ClaimCandidates lists queued run ids for a provider (fallback scan;
// normal wakeups come from Redis Streams).
func (s *Service) ClaimCandidates(ctx context.Context, provider string, limit int) ([]ids.ID, error) {
	rows, err := s.q(ctx).ListQueuedRunIDs(ctx, db.ListQueuedRunIDsParams{
		Provider: provider,
		Limit:    int32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]ids.ID, 0, len(rows))
	for _, raw := range rows {
		out = append(out, mustID(raw))
	}
	return out, nil
}

// BeginProviderAttemptOwned consumes ONE provider execution attempt for
// the owning worker (P0-2). Semantics:
//
//	attempt == provider execution count — NOT a claim count
//	claim / admission requeue / inflight rejection never consume one
//
// It runs in one transaction under the run row lock:
//
//	verify ownership (running + epoch)
//	attempt >= max_attempts → ErrProviderAttemptsExhausted
//	attempt++ → return the new count
//
// The caller invokes it immediately before the real provider submit
// (Auth → rate limit → BeginProviderAttempt → StartChat/Submit), so a run
// that never reached the provider never burns retry budget.
func (s *Service) BeginProviderAttemptOwned(ctx context.Context, own ExecutionOwnership) (int64, error) {
	if !own.Valid() {
		return 0, ErrLostOwnership
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	row, err := verifyActiveOwnershipTx(ctx, tx, own)
	if err != nil {
		return 0, err
	}
	if int64(row.Attempt) >= int64(row.MaxAttempts) {
		return 0, ErrProviderAttemptsExhausted
	}
	if _, err := db.New(tx).BeginProviderAttemptFenced(ctx, db.BeginProviderAttemptFencedParams{
		ID:         own.RunID.Bytes(),
		LeaseEpoch: own.LeaseEpoch,
	}); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int64(row.Attempt) + 1, nil
}

// MarkRunStartedOwned stamps started_at for the owning worker (第四轮 P2).
//
// Semantics: started_at means "this run was allowed to execute", not "a
// worker claimed it". The claim no longer stamps it, so a run killed by the
// execution gate stays NULL instead of advertising a start it never had.
//
// It is fenced by the lease epoch and idempotent (COALESCE): a run deferred
// for a paused provider and later re-claimed keeps its FIRST start time, so
// RunDuration measures execution wall-clock rather than the last requeue.
//
// It returns the CANONICAL DATABASE timestamp (第五轮 P2-4). The worker
// must copy it onto its in-memory Run: the snapshot was loaded at claim
// time, when started_at was still NULL, and FinalizeOwnedRun only observes
// studio_run_duration when run.StartedAt is set — without this the metric
// silently stopped recording for every run.
//
// Failure here is an observability loss, not a correctness loss — the caller
// logs and continues.
func (s *Service) MarkRunStartedOwned(ctx context.Context, own ExecutionOwnership) (time.Time, error) {
	if !own.Valid() {
		return time.Time{}, ErrLostOwnership
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return time.Time{}, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := verifyActiveOwnershipTx(ctx, tx, own); err != nil {
		return time.Time{}, err
	}
	if _, err := db.New(tx).MarkRunStartedFenced(ctx, db.MarkRunStartedFencedParams{
		ID:         own.RunID.Bytes(),
		LeaseEpoch: own.LeaseEpoch,
	}); err != nil {
		return time.Time{}, err
	}
	// Read back inside the SAME transaction: the value is the row's, and
	// COALESCE may have preserved a start from an earlier attempt.
	started, err := db.New(tx).GetRunStartedAt(ctx, db.GetRunStartedAtParams{
		ID:         own.RunID.Bytes(),
		LeaseEpoch: own.LeaseEpoch,
	})
	if err != nil {
		return time.Time{}, err
	}
	if err := tx.Commit(); err != nil {
		return time.Time{}, err
	}
	if !started.Valid {
		return time.Time{}, ErrLostOwnership
	}
	return started.Time.UTC(), nil
}

// HeartbeatOwned extends the lease; the WHERE carries the lease token so
// only the owning worker (even after a worker-id recycle) can renew it.
// Expiry comes from the DB clock (Phase 3), so a skewed worker clock can
// neither extend nor shorten the lease. Returns false when ownership is gone.
func (s *Service) HeartbeatOwned(ctx context.Context, own ExecutionOwnership, leaseSeconds time.Duration) (bool, error) {
	res, err := s.q(ctx).HeartbeatLeaseFenced(ctx, db.HeartbeatLeaseFencedParams{
		LeaseMicros: leaseSeconds.Microseconds(),
		RunID:       own.RunID.Bytes(),
		LeaseToken:  own.LeaseToken.Bytes(),
	})
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err == nil && n != 1 && s.Metrics != nil {
		s.Metrics.RunOwnershipLostTotal.Inc()
	}
	return n == 1, err
}

// HeartbeatOwnedWithSlot extends the run lease and, when the caller still
// holds it, renews the ownership-scoped provider slot in the SAME database
// transaction (Admission Fairness & Lease Hardening §20): Run Ownership
// alive ⇔ Provider Slot alive becomes an invariant instead of two
// independently drifting leases.
//
// Contract — once the run is in provider execution this is BOTH OR NEITHER:
//
//	err == nil, leaseOK && slotOK   → both renewed in one commit
//	err == nil, !leaseOK            → ownership gone; nothing committed
//	err == ErrProviderSlotLost      → the DB PROVED the slot is gone; the
//	                                  lease renewal was rolled back
//	err != nil (infrastructure)     → nothing committed; the caller keeps
//	                                  executing until the last confirmed TTL
//	                                  and then fails closed
//
// A failed slot renewal never leaves a committed lease extension behind.
// Committing one used to produce "run ownership alive, provider slot
// missing", which silently breaks max_inflight: the freed capacity is
// counted by the next admission (max_inflight - 1 < max), a new run is
// admitted against a slot that is still busy, and real provider concurrency
// reaches max_inflight + 1 — the capacity limit stops being a safety bound.
//
// The renewal stays XX-only: a lost slot is never recreated here. The
// system cannot know whether a new admission already consumed the released
// capacity, and recreating it would need the full admission serialization
// (provider lock → run lock → lease lock) inside the heartbeat path.
func (s *Service) HeartbeatOwnedWithSlot(ctx context.Context, own ExecutionOwnership, leaseSeconds time.Duration, slot *ProviderSlot, slotLease time.Duration) (leaseOK bool, slotOK bool, err error) {
	if !own.Valid() {
		return false, false, ErrLostOwnership
	}
	if slot == nil {
		// No provider execution has started: the run lease is the only
		// lease to keep alive, and there is no capacity to hold.
		ok, err := s.HeartbeatOwned(ctx, own, leaseSeconds)
		return ok, true, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, false, err
	}
	defer func() { _ = tx.Rollback() }()
	q := db.New(tx)

	res, err := q.HeartbeatLeaseFenced(ctx, db.HeartbeatLeaseFencedParams{
		LeaseMicros: leaseSeconds.Microseconds(),
		RunID:       own.RunID.Bytes(),
		LeaseToken:  own.LeaseToken.Bytes(),
	})
	if err != nil {
		return false, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, false, err
	}
	if n != 1 {
		// Ownership lost: do NOT touch the slot — it belongs to the
		// recovered attempt's accounting now and expires on its own
		// (or was already cleaned by the reaper in the same transaction
		// that requeued the run).
		if s.Metrics != nil {
			s.Metrics.RunOwnershipLostTotal.Inc()
		}
		return false, false, nil
	}

	if slot.Provider == "" {
		// Malformed slot descriptor: never guess a provider, and never
		// commit the lease renewal against a reservation that cannot be
		// renewed. The whole merged heartbeat rolls back.
		return false, false, errors.New("execution: provider slot has no provider")
	}
	slotLease = slotLeaseOrDefault(slotLease)
	sres, err := q.TouchProviderSlot(ctx, db.TouchProviderSlotParams{
		LeaseMicros: slotLease.Microseconds(),
		Provider:    slot.Provider,
		RunID:       slot.RunID.Bytes(),
		LeaseEpoch:  slot.LeaseEpoch,
		LeaseToken:  slot.LeaseToken.Bytes(),
	})
	if err != nil {
		// Transient infrastructure failure (lock wait timeout, connection
		// hiccup, ...): the DB could not confirm the reservation, which is
		// NOT proof that it is gone. Roll the lease renewal back too — the
		// caller keeps running on the last CONFIRMED TTL and retries on the
		// next tick; a partial commit is exactly the unbounded-capacity
		// state this function exists to prevent.
		s.Log.Warn("provider slot renew failed in merged heartbeat — rolling the lease renewal back",
			slogKey("run_id"), own.RunID.String(), slogKey("err"), err)
		return false, false, err
	}
	sn, err := sres.RowsAffected()
	if err != nil {
		return false, false, err
	}
	if sn != 1 {
		// The round-trip SUCCEEDED and reported 0 changed rows, which —
		// now that the renewal writes heartbeat_at monotonically — means
		// the ownership-scoped slot no longer exists (expired, released or
		// superseded): confirmed capacity loss. Roll the lease extension
		// back so this worker cannot keep executing provider calls that no
		// longer count against max_inflight.
		return false, false, ErrProviderSlotLost
	}

	if err := tx.Commit(); err != nil {
		return false, false, err
	}
	return true, true, nil
}

// slotLeaseOrDefault guarantees a positive slot TTL for the merged heartbeat
// when the caller passes an unset lease.
func slotLeaseOrDefault(d time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return DefaultProviderSlotLease
}

// CheckOwnership verifies the caller still holds the run at the given
// lease epoch (run must still be running). Strict: epoch 0 never passes —
// the unfenced system plane does not call this.
func (s *Service) CheckOwnership(ctx context.Context, runID ids.ID, epoch uint64) error {
	if epoch == 0 {
		return ErrLostOwnership
	}
	var one int
	err := s.DB.QueryRowContext(ctx,
		`SELECT 1 FROM runs WHERE id = ? AND status = 'running' AND lease_epoch = ?`,
		runID.Bytes(), epoch).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrLostOwnership
	}
	return err
}

// UpdateExternalRunIDOwned records the provider chat id (set-once) with
// the lease fence: stale workers cannot stamp their external id onto a
// run owned by someone else.
func (s *Service) UpdateExternalRunIDOwned(ctx context.Context, own ExecutionOwnership, externalID string) error {
	if externalID == "" {
		return nil
	}
	if !own.Valid() {
		return ErrLostOwnership
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := verifyActiveOwnershipTx(ctx, tx, own)
	if err != nil {
		return err
	}
	if err := updateExternalRunIDTx(ctx, tx, row, own, externalID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func slogKey(k string) string { return k }

// ────────────────────────────────────────────────────────── finish ──

// FinishInput carries terminal transition fields. The finalize
// transaction (FinalizeOwnedRun) persists the terminal CAS, the terminal
// RunEvent, the assistant message, the occurrence state and the lease
// cleanup as ONE database transaction (修复计划 §29-32).
type FinishInput struct {
	Status         string
	Output         map[string]any
	ProviderStatus string
	FinishReason   string
	ErrorCode      string
	ErrorMessage   string

	// AssistantText + AssistantMetadata: when non-empty (and the run has
	// a conversation), the assistant message is inserted INSIDE the
	// finalize transaction — a succeeded run can never lose its answer.
	AssistantText     string
	AssistantMetadata json.RawMessage
}

// ─────────────────────────────────────────────────────────── events ──

// AppendEvent is the SYSTEM-plane event writer (unfenced: scheduler
// bookkeeping, diagnostics). Workers must use AppendOwnedEvent — see
// ownership.go for the fenced plane.
func (s *Service) AppendEvent(ctx context.Context, runID ids.ID, eventType string, payload map[string]any) error {
	return s.appendEvent(ctx, runID, 0, eventType, payload)
}

// AppendOwnedEvent is the worker-owned variant: the sequence-allocation
// lock also checks the lease fence, so a stale worker (lease reclaimed by
// another owner) is rejected with ErrLostOwnership before any write.
func (s *Service) AppendOwnedEvent(ctx context.Context, own ExecutionOwnership, eventType string, payload map[string]any) error {
	if !own.Valid() {
		return ErrLostOwnership
	}
	return s.appendEvent(ctx, own.RunID, own.LeaseEpoch, eventType, payload)
}

func (s *Service) appendEvent(ctx context.Context, runID ids.ID, epoch uint64, eventType string, payload map[string]any) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	sequence, err := appendEventTx(ctx, tx, runID, epoch, eventType, payload)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	s.publishLive(ctx, runID, sequence, eventType, payload)
	return nil
}

// appendEventTx appends one event inside a caller-owned transaction.
// The run row is locked (SELECT ... FOR UPDATE) to serialize sequence
// allocation per run; with epoch > 0 the lock statement also verifies
// ownership — no row = the caller lost the run.
func appendEventTx(ctx context.Context, tx *sql.Tx, runID ids.ID, epoch uint64, eventType string, payload map[string]any) (uint64, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	payloadJSON, _ := json.Marshal(payload)

	var lockEpoch uint64
	lockSQL := `SELECT id FROM runs WHERE id = ? FOR UPDATE`
	if epoch > 0 {
		lockSQL = `SELECT lease_epoch FROM runs WHERE id = ? AND status = 'running' AND lease_epoch = ? FOR UPDATE`
		if err := tx.QueryRowContext(ctx, lockSQL, runID.Bytes(), epoch).Scan(&lockEpoch); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, ErrLostOwnership
			}
			return 0, err
		}
	} else if _, err := tx.ExecContext(ctx, lockSQL, runID.Bytes()); err != nil {
		return 0, err
	}
	var count uint64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_events WHERE run_id = ?`, runID.Bytes()).Scan(&count); err != nil {
		return 0, err
	}
	if _, err := db.New(tx).AppendRunEvent(ctx, db.AppendRunEventParams{
		RunID:     runID.Bytes(),
		Sequence:  count + 1,
		EventType: eventType,
		Payload:   dbtypes.JSONText(payloadJSON),
	}); err != nil {
		return 0, err
	}
	return count + 1, nil
}

// nextEventSequenceTx returns the sequence a new event must use, from the
// count read under the caller's run-row lock.
//
// COUNT(*)+1 is only valid because EVERY event writer takes the run row
// FOR UPDATE first (see appendEventTx): the count cannot change between
// the read and the INSERT, so sequence stays gap-free and unique. Callers
// that already hold the lock may compute it themselves and pass it to
// AppendRunEventAtSequence.
func nextEventSequenceTx(ctx context.Context, tx *sql.Tx, runID ids.ID) (uint64, error) {
	var count uint64
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM run_events WHERE run_id = ?`, runID.Bytes()).Scan(&count); err != nil {
		return 0, err
	}
	return count + 1, nil
}

// PublishTransient fans a HIGH-FREQUENCY event (content.delta) to live
// SSE consumers through Redis pub/sub WITHOUT persisting it (评测 P1:
// run_events write amplification). Transient frames use sequence 0 —
// the SSE gateway forwards them live but they never appear in replay.
func (s *Service) PublishTransient(ctx context.Context, runID ids.ID, eventType string, payload map[string]any) {
	s.publishLive(ctx, runID, 0, eventType, payload)
}

// publishLive fans an event out to the SSE gateway via Redis pub/sub.
// Best-effort: Redis failure degrades latency, never correctness.
func (s *Service) publishLive(ctx context.Context, runID ids.ID, sequence uint64, eventType string, payload map[string]any) {
	if s.Redis == nil {
		return
	}
	frame, _ := json.Marshal(map[string]any{
		"run_id":     runID.String(),
		"sequence":   sequence,
		"event_type": eventType,
		"payload":    payload,
		"created_at": time.Now().UTC().Format(time.RFC3339Nano),
	})
	pubCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Redis.Publish(pubCtx, s.Redis.RunEventsChannel(runID.String()), frame).Err(); err != nil {
		s.Log.Warn("publish run event failed", slogKey("run_id"), runID.String(), slogKey("err"), err)
	}
}

// ListEventsAfter returns persisted events with sequence > after.
func (s *Service) ListEventsAfter(ctx context.Context, runID ids.ID, after uint64) ([]EventRecord, error) {
	rows, err := s.q(ctx).ListRunEventsAfter(ctx, db.ListRunEventsAfterParams{
		RunID:    runID.Bytes(),
		Sequence: after,
	})
	if err != nil {
		return nil, err
	}
	out := make([]EventRecord, 0, len(rows))
	for _, r := range rows {
		var payload map[string]any
		_ = json.Unmarshal([]byte(r.Payload), &payload)
		out = append(out, EventRecord{
			ID:        int64(r.ID),
			RunID:     mustID(r.RunID).String(),
			Sequence:  r.Sequence,
			EventType: r.EventType,
			Payload:   payload,
			CreatedAt: r.CreatedAt,
		})
	}
	return out, nil
}

// EventRecord is the wire shape of a run event.
type EventRecord struct {
	ID        int64          `json:"id"`
	RunID     string         `json:"run"`
	Sequence  uint64         `json:"sequence"`
	EventType string         `json:"event_type"`
	Payload   map[string]any `json:"payload"`
	CreatedAt time.Time      `json:"created_at"`
}

// ─────────────────────────────────────────────────────────── helpers ──

func defaultStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func nullInt64(v *int64) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *v, Valid: true}
}

// nullInt64NZ maps 0 to SQL NULL (no trigger row reference).
func nullInt64NZ(v int64) sql.NullInt64 {
	if v == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: v, Valid: true}
}

func nullText(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }

// dbClockDuration turns a run's DB-clock started_at/finished_at pair into
// a metric duration (第六轮 P2).
//
// Both bounds MUST come from the database clock: started_at is written by
// MySQL (MarkRunStartedOwned / CASClaimRun) and finished_at by
// CASFinishRunFenced, so measuring the pair against time.Now() on the
// worker host mixes two clock authorities and reports a skewed — even
// negative — duration whenever the host drifts.
//
// Returns ok=false when either timestamp is missing (a run killed before
// it was allowed to execute has no start) or when the pair is not
// monotonic (clock skew between the two statements): an unobservable
// duration must be DROPPED, never recorded as a bogus sample.
func dbClockDuration(startedAt, finishedAt sql.NullTime) (float64, bool) {
	if !startedAt.Valid || !finishedAt.Valid {
		return 0, false
	}
	seconds := finishedAt.Time.Sub(startedAt.Time).Seconds()
	if seconds < 0 {
		return 0, false
	}
	return seconds, true
}

func defaultMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// Querier exposes the sqlc querier for cross-domain workers (aily executor).
func (s *Service) Querier() db.Querier { return db.New(s.DB) }

// RunFromDBRow exposes the gen/db row → domain conversion for transport.
func RunFromDBRow(row db.Run) *Run { return runFromRow(row) }
