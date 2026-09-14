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

// Service implements the Run lifecycle on top of TiDB.
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
	RuntimeSnapshot   map[string]any
	MaxAttempts       int64
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

// CreateRunInTx writes message + run + outbox inside a caller-owned
// transaction so composite units (e.g. scheduler occurrence + run) commit
// atomically. The caller owns commit/rollback.
func (s *Service) CreateRunInTx(ctx context.Context, tx *sql.Tx, in *CreateRunInput) (ids.ID, error) {
	runID := ids.New()

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

	q := db.New(tx)

	if _, err := q.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: uint64(in.ConversationID),
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
	if _, err := q.CreateRun(ctx, db.CreateRunParams{
		ID:               runID.Bytes(),
		UserID:           nullInt64(&in.UserID),
		ApplicationID:    nullInt64(&in.ApplicationID),
		ConversationID:   nullInt64(&in.ConversationID),
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
	if _, err := q.CreateOutboxEvent(ctx, db.CreateOutboxEventParams{
		Aggregate:   "run",
		AggregateID: runID.Bytes(),
		EventType:   "run.dispatch",
		Payload:     dbtypes.JSONText(outboxJSON),
	}); err != nil {
		return runID, fmt.Errorf("insert outbox: %w", err)
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
// row (carrying the new lease_epoch) in ONE TiDB transaction, then loads
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
		RunID:      runID.Bytes(),
		WorkerID:   workerID,
		LeaseToken: token.Bytes(),
		LeaseEpoch: epoch,
		ExpiresAt:  time.Now().UTC().Add(leaseSeconds),
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

// HeartbeatOwned extends the lease; the WHERE carries the lease token so
// only the owning worker (even after a worker-id recycle) can renew it.
// Returns false when ownership is gone.
func (s *Service) HeartbeatOwned(ctx context.Context, own ExecutionOwnership, leaseSeconds time.Duration) (bool, error) {
	res, err := s.q(ctx).HeartbeatLeaseFenced(ctx, db.HeartbeatLeaseFencedParams{
		ExpiresAt:  time.Now().UTC().Add(leaseSeconds),
		RunID:      own.RunID.Bytes(),
		LeaseToken: own.LeaseToken.Bytes(),
	})
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
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
// cleanup as ONE TiDB transaction (修复计划 §29-32).
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

// timeSinceSeconds measures a run's wall-clock duration for metrics.
func timeSinceSeconds(start time.Time) float64 {
	return time.Since(start).Seconds()
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
