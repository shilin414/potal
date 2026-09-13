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

// Service implements the Run lifecycle on top of TiDB.
type Service struct {
	DB      *sql.DB
	Redis   *redisx.Client
	Log     *slog.Logger
	Metrics *telemetry.Metrics

	// OnRunSucceeded fires after a winning terminal transition to
	// succeeded for scheduled runs; used for delivery fan-out. It must
	// not block on external IO and must not panic (guarded).
	OnRunSucceeded func(ctx context.Context, run *Run, output map[string]any)
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

	outboxPayload := map[string]any{
		"run_id":   runID.String(),
		"provider": in.Provider,
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
	priority := in.Priority
	if priority == "" {
		priority = DefaultPriority
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

func mustID(b []byte) ids.ID {
	var id ids.ID
	_ = id.Scan(b)
	return id
}

// ─────────────────────────────────────────────────────── claim / lease ──

// CASClaim tries to transition queued→running for one run.
// Returns true iff this worker won the race (affected rows == 1).
func (s *Service) CASClaim(ctx context.Context, runID ids.ID) (bool, error) {
	res, err := s.q(ctx).CASClaimRun(ctx, runID.Bytes())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if n == 1 {
		// Best-effort fan-out: a scheduled run's occurrence follows the
		// run into 'running'. Interactive runs have no occurrence row.
		_, _ = s.q(ctx).MarkOccurrenceRunningByRun(ctx, sql.NullString{String: string(runID.Bytes()), Valid: true})
	}
	return n == 1, err
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

// AcquireLease inserts the lease row for a claimed run.
func (s *Service) AcquireLease(ctx context.Context, runID ids.ID, workerID string, leaseSeconds time.Duration) error {
	token := ids.New()
	return s.q(ctx).CreateRunLease(ctx, db.CreateRunLeaseParams{
		RunID:      runID.Bytes(),
		WorkerID:   workerID,
		LeaseToken: token.Bytes(),
		ExpiresAt:  time.Now().UTC().Add(leaseSeconds),
	})
}

// HeartbeatLease extends the lease; worker_id in the WHERE makes it
// ownership-checked (a stale worker cannot extend a new owner's lease).
func (s *Service) HeartbeatLease(ctx context.Context, runID ids.ID, workerID string, leaseSeconds time.Duration) (bool, error) {
	res, err := s.q(ctx).HeartbeatLease(ctx, db.HeartbeatLeaseParams{
		ExpiresAt: time.Now().UTC().Add(leaseSeconds),
		RunID:     runID.Bytes(),
		WorkerID:  workerID,
	})
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ReleaseInterrupted requeues (or fails) a run after lease loss and
// removes the lease.
func (s *Service) ReleaseInterrupted(ctx context.Context, run *Run, reason string) error {
	_ = s.AppendEvent(ctx, run.ID, EventRunInterrupted, map[string]any{"reason": reason})
	if run.Attempt >= run.MaxAttempts {
		if _, err := s.q(ctx).FailExpiredRun(ctx, db.FailExpiredRunParams{
			ErrorMessage: nullText(reason),
			ID:           run.ID.Bytes(),
		}); err != nil {
			return err
		}
	} else {
		if err := s.q(ctx).RequeueRun(ctx, run.ID.Bytes()); err != nil {
			return err
		}
		// Re-dispatch through the outbox so the queue wakes a worker.
		payload, _ := json.Marshal(map[string]any{"run_id": run.ID.String(), "provider": run.Provider})
		_, _ = s.q(ctx).CreateOutboxEvent(ctx, db.CreateOutboxEventParams{
			Aggregate: "run", AggregateID: run.ID.Bytes(),
			EventType: "run.dispatch", Payload: dbtypes.JSONText(payload),
		})
	}
	return s.q(ctx).DeleteLease(ctx, run.ID.Bytes())
}

// RecoverExpiredLeases is the reaper: it re-validates each lease expiry
// under the row predicate so a heartbeat that lands first wins.
func (s *Service) RecoverExpiredLeases(ctx context.Context, limit int) (int, error) {
	runIDs, err := s.q(ctx).ListExpiredLeaseRunIDs(ctx, int32(limit))
	if err != nil {
		return 0, err
	}
	recovered := 0
	for _, raw := range runIDs {
		runID := mustID(raw)
		res, err := s.q(ctx).DeleteLeaseIfExpired(ctx, runID.Bytes())
		if err != nil {
			continue
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue // heartbeat renewed it first
		}
		run, err := s.GetRun(ctx, runID)
		if err != nil || run.Status != StatusRunning {
			continue
		}
		if err := s.ReleaseInterrupted(ctx, run, "worker lease expired"); err != nil {
			s.Log.Error("reaper release failed", "run_id", runID.String(), slogKey("err"), err)
			continue
		}
		recovered++
		if s.Metrics != nil {
			s.Metrics.LeaseExpired.Inc()
		}
	}
	return recovered, nil
}

func slogKey(k string) string { return k }

// ────────────────────────────────────────────────────────── finish ──

// FinishInput carries terminal transition fields.
type FinishInput struct {
	Status         string
	Output         map[string]any
	ProviderStatus string
	FinishReason   string
	ErrorCode      string
	ErrorMessage   string
}

// Finish applies the terminal CAS (any non-terminal → terminal) and emits
// the terminal event. Idempotent: losing the race is a no-op.
func (s *Service) Finish(ctx context.Context, run *Run, in *FinishInput) error {
	output := in.Output
	outputJSON := dbtypes.JSONText([]byte("null"))
	if output != nil {
		raw, _ := json.Marshal(output)
		outputJSON = dbtypes.JSONText(raw)
	}
	res, err := s.q(ctx).CASFinishRun(ctx, db.CASFinishRunParams{
		Status:               in.Status,
		Output:               outputJSON,
		ProviderStatus:       in.ProviderStatus,
		ProviderFinishReason: in.FinishReason,
		ErrorCode:            in.ErrorCode,
		ErrorMessage:         nullText(in.ErrorMessage),
		ID:                   run.ID.Bytes(),
	})
	if err != nil {
		return err
	}
	// Lease removal happens unconditionally (even when the CAS lost).
	_ = s.q(ctx).DeleteLease(ctx, run.ID.Bytes())

	if n, _ := res.RowsAffected(); n != 1 {
		return nil // already terminal — nothing to emit
	}
	payload := map[string]any{
		"status":          in.Status,
		"provider_status": in.ProviderStatus,
		"finish_reason":   in.FinishReason,
	}
	if in.ErrorCode != "" {
		payload["error_code"] = in.ErrorCode
	}
	if in.ErrorMessage != "" {
		payload["error_message"] = in.ErrorMessage
	}
	if output != nil {
		if t, ok := output["text"].(string); ok && t != "" {
			payload["text"] = t
		}
	}
	eventType := EventRunCompleted
	switch in.Status {
	case StatusFailed:
		eventType = EventRunFailed
	case StatusInterrupted:
		eventType = EventRunInterrupted
	}
	if err := s.AppendEvent(ctx, run.ID, eventType, payload); err != nil {
		s.Log.Error("append terminal event failed", slogKey("run_id"), run.ID.String(), slogKey("err"), err)
	}
	if s.Metrics != nil {
		s.Metrics.RunTotal.WithLabelValues(run.Provider, in.Status).Inc()
		if !run.StartedAt.IsZero() {
			s.Metrics.RunDuration.
				WithLabelValues(run.Provider, in.Status).
				Observe(time.Since(*run.StartedAt).Seconds())
		}
	}
	// Occurrence convergence: the scheduled run's occurrence follows the
	// run into a terminal state (delivery state stays separate). The CAS
	// loser (already terminal elsewhere) is a no-op.
	if run.TriggerType == TriggerTypeScheduled && run.TriggerID != nil {
		occStatus := StatusFailed
		if in.Status == StatusSucceeded {
			occStatus = StatusSucceeded
		}
		_, _ = s.q(ctx).CASFinishOccurrenceByRun(ctx, db.CASFinishOccurrenceByRunParams{
			Status: occStatus,
			RunID:  sql.NullString{String: string(run.ID.Bytes()), Valid: true},
		})
	}
	// Delivery fan-out: only the CAS winner triggers, only scheduled runs
	// have deliveries, and a hook panic must never fail the run.
	if in.Status == StatusSucceeded && s.OnRunSucceeded != nil &&
		run.TriggerType == "scheduled" && run.TriggerID != nil && run.UserID != nil {
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					s.Log.Error("run succeeded hook panicked", slogKey("run_id"), run.ID.String(), slogKey("panic"), rec)
				}
			}()
			s.OnRunSucceeded(ctx, run, output)
		}()
	}
	return nil
}

// ─────────────────────────────────────────────────────────── events ──

// AppendEvent allocates the next per-run sequence under a row lock on the
// run, inserts the event, and fans it out to Redis pub/sub (best-effort).
func (s *Service) AppendEvent(ctx context.Context, runID ids.ID, eventType string, payload map[string]any) error {
	if payload == nil {
		payload = map[string]any{}
	}
	payloadJSON, _ := json.Marshal(payload)

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Serialize sequence allocation per run via SELECT ... FOR UPDATE.
	if _, err := tx.ExecContext(ctx, `SELECT id FROM runs WHERE id = ? FOR UPDATE`, runID.Bytes()); err != nil {
		return err
	}
	var count uint64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_events WHERE run_id = ?`, runID.Bytes()).Scan(&count); err != nil {
		return err
	}
	if _, err := db.New(tx).AppendRunEvent(ctx, db.AppendRunEventParams{
		RunID:     runID.Bytes(),
		Sequence:  count + 1,
		EventType: eventType,
		Payload:   dbtypes.JSONText(payloadJSON),
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	s.publishLive(ctx, runID, count+1, eventType, payload)
	return nil
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
