package schedule

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
)

// ErrNotFound marks missing/foreign schedules (identical to callers).
var ErrNotFound = errors.New("schedule: not found")

// ErrApplicationNotSchedulable is the POLICY refusal the ApplicationChecker
// returns when the owner may not execute the application (复审 P1-1).
// validate maps it to a 400-grade ValidationError; any OTHER error from
// the checker is an infrastructure failure and propagates untouched —
// a MySQL outage during create/update must surface as a 500, never as
// "application is not schedulable".
var ErrApplicationNotSchedulable = errors.New("schedule: application is not schedulable")

// ErrValidation marks rejected input (maps to 400/422 upstream).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// ApplicationChecker lets the scheduler domain validate that an application
// is actually executable (kind=chat + enabled runtime binding) without a
// hard dependency on the catalog package.
type ApplicationChecker interface {
	// SchedulableApplication returns nil when the application exists,
	// is enabled, is chat-kind and has an enabled runtime binding.
	SchedulableApplication(ctx context.Context, appID, ownerUserID int64) error
}

// DeliveryInput is one target submitted with create/update.
type DeliveryInput struct {
	TargetType  string `json:"target_type"`
	TargetID    string `json:"target_id"`
	TargetName  string `json:"target_name"`
	ContentMode string `json:"content_mode"`
}

// CreateInput is the validated-shape request for a new schedule.
type CreateInput struct {
	Name                   string
	Description            string
	ApplicationID          int64
	Prompt                 string
	ScheduleType           string
	Timezone               string
	RunAt                  *time.Time
	TriggerConfig          TriggerConfig
	ConversationPolicy     string
	OverlapPolicy          string
	MisfirePolicy          string
	DeadlinePolicy         string
	ExecutionWindowSeconds int
	Deliveries             []DeliveryInput
}

// Service implements schedule CRUD on MySQL.
type Service struct {
	DB    *sql.DB
	Check ApplicationChecker
	Log   *slog.Logger
	// MaxSchedules caps how many live schedules one owner may have
	// (评测 P1-7); 0 disables the cap.
	MaxSchedules int
	nowFunc      func() time.Time
}

func NewService(d *sql.DB, check ApplicationChecker, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{DB: d, Check: check, Log: log, nowFunc: time.Now}
}

func (s *Service) q(ctx context.Context) db.Querier { return db.New(s.DB) }

// dbNow resolves "now" from MySQL (Clock Authority, 评测 §十七; 第四轮
// P2): next_run_at and the "run_at must be in the future" check must not
// depend on the API host's local clock — two nodes with a little clock
// skew would otherwise compute different first slots for the same
// schedule. Falls back to the process clock only when the DB read fails
// (degraded, same policy as the scheduler).
func (s *Service) dbNow(ctx context.Context) time.Time {
	if s.DB == nil {
		return s.nowFunc().UTC()
	}
	return s.dbNowTx(ctx, db.New(s.DB))
}

// dbNowTx is dbNow inside a caller-owned transaction (CREATE/UPDATE/ENABLE
// compute the next slot under a lock, so the clock read belongs to the
// same snapshot).
func (s *Service) dbNowTx(ctx context.Context, q db.Querier) time.Time {
	t, err := q.DBNow(ctx)
	if err != nil || t.IsZero() {
		return s.nowFunc().UTC()
	}
	return t.UTC()
}

// ─────────────────────────────────────────────────────────── validation ──

func (in *CreateInput) defaults() {
	if in.Timezone == "" {
		in.Timezone = "Asia/Shanghai"
	}
	if in.ConversationPolicy == "" {
		in.ConversationPolicy = ConversationNewEachRun
	}
	if in.OverlapPolicy == "" {
		in.OverlapPolicy = OverlapQueue
	}
	if in.MisfirePolicy == "" {
		in.MisfirePolicy = MisfireFireOnce
	}
	if in.DeadlinePolicy == "" {
		in.DeadlinePolicy = DeadlineExecuteAnyway
	}
}

var policyValues = map[string]map[string]bool{
	"conversation": {ConversationNewEachRun: true, ConversationReuse: true},
	"overlap":      {OverlapSkip: true, OverlapQueue: true},
	"misfire":      {MisfireFireOnce: true, MisfireSkip: true},
	"deadline":     {DeadlineSkip: true, DeadlineExecuteAnyway: true},
	"scheduleType": {TypeOnce: true, TypeDaily: true, TypeWeekly: true, TypeMonthly: true},
}

func policyValid(kind, v string) bool { return policyValues[kind][v] }

// validate enforces domain rules; caller identity checks happen upstream.
// ownerUserID feeds the AuthorizeExecution gate (评测 P0-1).
func (s *Service) validate(ctx context.Context, in *CreateInput, ownerUserID int64) error {
	return s.validateAt(ctx, in, ownerUserID, s.dbNow(ctx))
}

// validateAt validates against an explicit instant (the DB clock), so the
// "run_at must be in the future" check and the next-slot computation of
// the same request share ONE clock reading (第四轮 P2).
func (s *Service) validateAt(ctx context.Context, in *CreateInput, ownerUserID int64, now time.Time) error {
	return validateInput(ctx, in, ownerUserID, func() time.Time { return now }, s.Check)
}

func validateInput(ctx context.Context, in *CreateInput, ownerUserID int64, now func() time.Time, check ApplicationChecker) error {
	if in.Name == "" {
		return &ValidationError{Msg: "name is required"}
	}
	if in.ApplicationID <= 0 {
		return &ValidationError{Msg: "application_id is required"}
	}
	if !policyValid("scheduleType", in.ScheduleType) {
		return &ValidationError{Msg: "unsupported schedule_type"}
	}
	if !policyValid("conversation", in.ConversationPolicy) {
		return &ValidationError{Msg: "unsupported conversation_policy"}
	}
	if !policyValid("overlap", in.OverlapPolicy) {
		return &ValidationError{Msg: "unsupported overlap_policy"}
	}
	if !policyValid("misfire", in.MisfirePolicy) {
		return &ValidationError{Msg: "unsupported misfire_policy"}
	}
	if !policyValid("deadline", in.DeadlinePolicy) {
		return &ValidationError{Msg: "unsupported deadline_policy"}
	}
	if in.ExecutionWindowSeconds < 0 || in.ExecutionWindowSeconds > 86400 {
		return &ValidationError{Msg: "execution_window_seconds out of range"}
	}
	if _, err := time.LoadLocation(in.Timezone); err != nil {
		return &ValidationError{Msg: "unknown timezone"}
	}
	if in.ScheduleType == TypeOnce {
		if in.RunAt == nil || !in.RunAt.After(now()) {
			return &ValidationError{Msg: "run_at must be in the future"}
		}
	} else if err := in.TriggerConfig.Validate(in.ScheduleType); err != nil {
		return &ValidationError{Msg: err.Error()}
	}
	if in.Prompt == "" {
		return &ValidationError{Msg: "prompt is required"}
	}
	if len(in.Deliveries) > 20 {
		return &ValidationError{Msg: "too many delivery targets (max 20)"}
	}
	for _, d := range in.Deliveries {
		if d.TargetType != "user" && d.TargetType != "chat" {
			return &ValidationError{Msg: "delivery target_type must be user or chat"}
		}
		if d.TargetID == "" {
			return &ValidationError{Msg: "delivery target_id is required"}
		}
	}
	if check != nil {
		// AuthorizeExecution gate (评测 P0-1): who may run it is decided by
		// the application, validated under the owner's identity. Only the
		// policy refusal becomes a ValidationError (复审 P1-1).
		if err := check.SchedulableApplication(ctx, in.ApplicationID, ownerUserID); err != nil {
			if errors.Is(err, ErrApplicationNotSchedulable) {
				return &ValidationError{Msg: "application is not schedulable"}
			}
			return err
		}
	}
	return nil
}

// ─────────────────────────────────────────────────────────────── create ──

// Create inserts a schedule (enabled by default) with its deliveries in
// one transaction. next_run_at is derived from the trigger.
//
// The whole request is anchored to ONE DB-clock reading (第四轮 P2):
// validation ("run_at must be in the future") and NextRunAfter see the
// same instant, and it is the database's instant — never the API host's.
func (s *Service) Create(ctx context.Context, ownerUserID int64, in *CreateInput) (*Schedule, error) {
	in.defaults()
	now := s.dbNow(ctx)
	if err := s.validateAt(ctx, in, ownerUserID, now); err != nil {
		return nil, err
	}
	next, err := NextRunAfter(in.ScheduleType, in.TriggerConfig, in.RunAt, in.Timezone, now)
	if err != nil {
		return nil, &ValidationError{Msg: err.Error()}
	}
	cron := in.TriggerConfig.CronExpression(in.ScheduleType)
	triggerJSON, _ := json.Marshal(in.TriggerConfig)
	payload := map[string]any{"prompt": in.Prompt}
	payloadJSON, _ := json.Marshal(payload)

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := db.New(tx)

	// Per-user schedule quota (评测 P1-7): the count runs under the user's
	// row lock inside the create transaction, so N concurrent creates can
	// no longer all observe the same "49" and overshoot the cap.
	if s.MaxSchedules > 0 {
		if _, err := q.LockUserRow(ctx, uint64(ownerUserID)); err != nil {
			return nil, err
		}
		n, err := q.CountSchedulesByOwner(ctx, uint64(ownerUserID))
		if err != nil {
			return nil, err
		}
		if n >= int64(s.MaxSchedules) {
			return nil, &ValidationError{Msg: "定时任务数量已达上限"}
		}
	}

	var nextArg sql.NullTime
	if !next.IsZero() {
		nextArg = sql.NullTime{Time: next, Valid: true}
	}
	var runAtArg sql.NullTime
	if in.RunAt != nil {
		runAtArg = sql.NullTime{Time: in.RunAt.UTC(), Valid: true}
	}
	res, err := q.CreateSchedule(ctx, db.CreateScheduleParams{
		OwnerUserID:            uint64(ownerUserID),
		Name:                   in.Name,
		Description:            nullString(in.Description),
		ApplicationID:          uint64(in.ApplicationID),
		InputPayload:           payloadJSON,
		ScheduleType:           in.ScheduleType,
		CronExpression:         cron,
		TriggerConfig:          triggerJSON,
		Timezone:               in.Timezone,
		RunAt:                  runAtArg,
		Enabled:                true,
		ConversationPolicy:     in.ConversationPolicy,
		ConversationID:         sql.NullInt64{},
		OverlapPolicy:          in.OverlapPolicy,
		MisfirePolicy:          in.MisfirePolicy,
		ExecutionWindowSeconds: uint32(in.ExecutionWindowSeconds),
		DeadlinePolicy:         in.DeadlinePolicy,
		NextRunAt:              nextArg,
	})
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	if err := s.replaceDeliveries(ctx, q, id, in.Deliveries); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.Get(ctx, id, ownerUserID, false)
}

// replaceDeliveries rewrites the delivery target set inside a transaction.
func (s *Service) replaceDeliveries(ctx context.Context, q db.Querier, scheduleID int64, in []DeliveryInput) error {
	if err := q.DeleteScheduleDeliveries(ctx, uint64(scheduleID)); err != nil {
		return err
	}
	for _, d := range in {
		contentMode := d.ContentMode
		if contentMode == "" {
			contentMode = "summary"
		}
		if _, err := q.UpsertScheduleDelivery(ctx, db.UpsertScheduleDeliveryParams{
			ScheduleID:         uint64(scheduleID),
			Channel:            "feishu",
			SenderIdentityMode: "owner_user",
			TargetType:         d.TargetType,
			TargetID:           d.TargetID,
			TargetName:         d.TargetName,
			ContentMode:        contentMode,
			Enabled:            true,
		}); err != nil {
			return fmt.Errorf("upsert delivery: %w", err)
		}
	}
	return nil
}

// ─────────────────────────────────────────────────────────── read/list ──

// Get enforces ownership: unknown or foreign schedules look identical.
func (s *Service) Get(ctx context.Context, id, userID int64, isStaff bool) (*Schedule, error) {
	row, err := s.q(ctx).GetScheduleByID(ctx, uint64(id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if row.OwnerUserID != uint64(userID) && !isStaff {
		return nil, ErrNotFound
	}
	// Soft-deleted (评测 §十二): gone for every owner surface, while the
	// delivery worker keeps resolving the row directly for its name.
	if row.DeletedAt.Valid {
		return nil, ErrNotFound
	}
	return FromDBRow(row), nil
}

// ListFilter narrows the owner list.
type ListFilter struct {
	Status   string // all | running | paused | failed
	BeforeID int64
	Limit    int
}

// ScheduleWithStatus adds list-time derived facts.
type ScheduleWithStatus struct {
	*Schedule
	LastOccurrence *Occurrence `json:"last_occurrence"`
}

// List returns one keyset page for the owner with the latest occurrence
// attached per schedule.
func (s *Service) List(ctx context.Context, userID int64, f ListFilter) ([]ScheduleWithStatus, error) {
	status := f.Status
	if status == "" {
		status = "all"
	}
	limit := f.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.q(ctx).ListSchedulesByOwner(ctx, db.ListSchedulesByOwnerParams{
		OwnerUserID: uint64(userID),
		Status:      status,
		BeforeID:    uint64(f.BeforeID),
		Limit:       int32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]ScheduleWithStatus, 0, len(rows))
	ids := make([]uint64, 0, len(rows))
	for _, r := range rows {
		out = append(out, ScheduleWithStatus{Schedule: FromDBRow(r)})
		ids = append(ids, r.ID)
	}
	if len(ids) > 0 {
		occs, err := s.q(ctx).ListLatestOccurrencesForSchedules(ctx, ids)
		if err != nil {
			return nil, err
		}
		bySchedule := make(map[uint64]db.ScheduleOccurrence, len(occs))
		for _, o := range occs {
			bySchedule[o.ScheduleID] = o
		}
		for i := range out {
			if o, ok := bySchedule[uint64(out[i].ID)]; ok {
				occ := occurrenceFromDBRow(o)
				out[i].LastOccurrence = occ
			}
		}
	}
	return out, nil
}

// ListOccurrences returns one keyset page of history for one schedule.
func (s *Service) ListOccurrences(ctx context.Context, scheduleID, userID int64, isStaff bool, beforeID int64, limit int) ([]*Occurrence, error) {
	if _, err := s.Get(ctx, scheduleID, userID, isStaff); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.q(ctx).ListOccurrencesBySchedule(ctx, db.ListOccurrencesByScheduleParams{
		ScheduleID: uint64(scheduleID),
		BeforeID:   uint64(beforeID),
		Limit:      int32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]*Occurrence, 0, len(rows))
	occIDs := make([]uint64, 0, len(rows))
	for _, r := range rows {
		out = append(out, occurrenceFromDBRow(r))
		occIDs = append(occIDs, r.ID)
	}
	// Attach per-target delivery results (Run ≠ Delivery).
	if len(occIDs) > 0 {
		dels, err := s.q(ctx).ListDeliveryExecutionsByOccurrences(ctx, occIDs)
		if err != nil {
			return nil, err
		}
		byOcc := make(map[uint64][]DeliveryExecution)
		for _, d := range dels {
			byOcc[d.OccurrenceID] = append(byOcc[d.OccurrenceID], DeliveryExecutionFromDBRow(d))
		}
		for _, occ := range out {
			occ.Deliveries = byOcc[uint64(occ.ID)]
		}
	}
	return out, nil
}

// ─────────────────────────────────────────────────────────────── update ──

// UpdateInput carries patch fields; nil pointers keep current values.
type UpdateInput struct {
	Name                   *string
	Description            *string
	Prompt                 *string
	ApplicationID          *int64
	ScheduleType           *string
	Timezone               *string
	RunAt                  **time.Time
	TriggerConfig          *TriggerConfig
	ConversationPolicy     *string
	OverlapPolicy          *string
	MisfirePolicy          *string
	DeadlinePolicy         *string
	ExecutionWindowSeconds *int
	Deliveries             *[]DeliveryInput
}

// Update applies the patch and recomputes next_run_at from now.
//
// Row-lock unification (复审 §七): the WHOLE decision — ownership,
// merge of the patch onto the CURRENT row, validation and next_run_at
// computation — happens inside ONE transaction that holds the schedules
// row lock (the same GetScheduleRowForUpdate the scheduler admission
// uses). Two concurrent PATCHes used to read the same old row in Go and
// the later full-struct UPDATE clobbered the earlier writer's fields
// (lost update); SetEnabled could also compute next_run_at from a stale
// trigger_config. Rule: any business-state change to a schedule first
// takes the schedule row lock.
func (s *Service) Update(ctx context.Context, id, userID int64, isStaff bool, in *UpdateInput) (*Schedule, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := db.New(tx)

	// Ownership + current state under the row lock. A soft-deleted
	// schedule has no locked row (deleted_at IS NULL predicate) and looks
	// exactly like a missing one.
	row, err := q.GetScheduleRowForUpdate(ctx, uint64(id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if row.OwnerUserID != uint64(userID) && !isStaff {
		return nil, ErrNotFound
	}
	cur := FromDBRow(row)

	next := &CreateInput{
		Name:                   cur.Name,
		Description:            cur.Description,
		ApplicationID:          cur.ApplicationID,
		ScheduleType:           cur.ScheduleType,
		Timezone:               cur.Timezone,
		RunAt:                  cur.RunAt,
		TriggerConfig:          cur.TriggerConfig,
		ConversationPolicy:     cur.ConversationPolicy,
		OverlapPolicy:          cur.OverlapPolicy,
		MisfirePolicy:          cur.MisfirePolicy,
		DeadlinePolicy:         cur.DeadlinePolicy,
		ExecutionWindowSeconds: cur.ExecutionWindowSeconds,
	}
	if cur.InputPayload != nil {
		if p, ok := cur.InputPayload["prompt"].(string); ok {
			next.Prompt = p
		}
	}
	if in.Name != nil {
		next.Name = *in.Name
	}
	if in.Description != nil {
		next.Description = *in.Description
	}
	if in.Prompt != nil {
		next.Prompt = *in.Prompt
	}
	if in.ApplicationID != nil {
		next.ApplicationID = *in.ApplicationID
	}
	if in.ScheduleType != nil {
		next.ScheduleType = *in.ScheduleType
	}
	if in.Timezone != nil {
		next.Timezone = *in.Timezone
	}
	if in.RunAt != nil {
		next.RunAt = *in.RunAt
	}
	if in.TriggerConfig != nil {
		next.TriggerConfig = *in.TriggerConfig
	}
	if in.ConversationPolicy != nil {
		next.ConversationPolicy = *in.ConversationPolicy
	}
	if in.OverlapPolicy != nil {
		next.OverlapPolicy = *in.OverlapPolicy
	}
	if in.MisfirePolicy != nil {
		next.MisfirePolicy = *in.MisfirePolicy
	}
	if in.DeadlinePolicy != nil {
		next.DeadlinePolicy = *in.DeadlinePolicy
	}
	if in.ExecutionWindowSeconds != nil {
		next.ExecutionWindowSeconds = *in.ExecutionWindowSeconds
	}
	next.defaults()
	// Execution authorization is decided for the SCHEDULE OWNER, not for
	// the caller (评测 P2): a staff member editing someone else's schedule
	// must not point it at an application the owner cannot execute — that
	// would save successfully and then fail on every fire.
	// DB clock, read under the locked row (第四轮 P2).
	now := s.dbNowTx(ctx, q)
	if err := s.validateAt(ctx, next, cur.OwnerUserID, now); err != nil {
		return nil, err
	}
	// Recompute from now, from the LOCKED row's merged state; missed slots
	// while editing are not replayed.
	nr, err := NextRunAfter(next.ScheduleType, next.TriggerConfig, next.RunAt, next.Timezone, now)
	if err != nil {
		return nil, &ValidationError{Msg: err.Error()}
	}
	var nextArg sql.NullTime
	if !nr.IsZero() {
		nextArg = sql.NullTime{Time: nr, Valid: true}
	}
	cron := next.TriggerConfig.CronExpression(next.ScheduleType)
	triggerJSON, _ := jsonMarshal(next.TriggerConfig)
	payloadJSON, _ := jsonMarshal(map[string]any{"prompt": next.Prompt})
	var runAtArg sql.NullTime
	if next.RunAt != nil {
		runAtArg = sql.NullTime{Time: next.RunAt.UTC(), Valid: true}
	}
	var convArg sql.NullInt64
	if cur.ConversationID != nil && next.ConversationPolicy == ConversationReuse {
		convArg = sql.NullInt64{Int64: *cur.ConversationID, Valid: true}
	}
	if _, err := q.UpdateSchedule(ctx, db.UpdateScheduleParams{
		Name:                   next.Name,
		Description:            nullString(next.Description),
		InputPayload:           payloadJSON,
		ScheduleType:           next.ScheduleType,
		CronExpression:         cron,
		TriggerConfig:          triggerJSON,
		Timezone:               next.Timezone,
		RunAt:                  runAtArg,
		ConversationPolicy:     next.ConversationPolicy,
		ConversationID:         convArg,
		OverlapPolicy:          next.OverlapPolicy,
		MisfirePolicy:          next.MisfirePolicy,
		ExecutionWindowSeconds: uint32(next.ExecutionWindowSeconds),
		DeadlinePolicy:         next.DeadlinePolicy,
		NextRunAt:              nextArg,
		ID:                     uint64(id),
	}); err != nil {
		return nil, err
	}
	if in.Deliveries != nil {
		if err := s.replaceDeliveries(ctx, q, id, *in.Deliveries); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.Get(ctx, id, userID, isStaff)
}

// SetEnabled flips the switch; enabling recomputes next_run_at from now
// (slots missed while disabled are not replayed). The decision runs under
// the schedules row lock (复审 §七): the trigger config the next slot is
// computed from is the committed one, and a concurrent PATCH cannot
// interleave between the read and the enable.
func (s *Service) SetEnabled(ctx context.Context, id, userID int64, isStaff, enabled bool) (*Schedule, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := db.New(tx)

	row, err := q.GetScheduleRowForUpdate(ctx, uint64(id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if row.OwnerUserID != uint64(userID) && !isStaff {
		return nil, ErrNotFound
	}

	if enabled {
		cur := FromDBRow(row)
		// DB clock, read under the schedules row lock (第四轮 P2).
		nr, err := NextRunAfter(cur.ScheduleType, cur.TriggerConfig, cur.RunAt, cur.Timezone, s.dbNowTx(ctx, q))
		if err != nil {
			return nil, err
		}
		var nextArg sql.NullTime
		if !nr.IsZero() {
			nextArg = sql.NullTime{Time: nr, Valid: true}
		}
		if _, err := q.SetScheduleEnabled(ctx, db.SetScheduleEnabledParams{Enabled: true, ID: uint64(id)}); err != nil {
			return nil, err
		}
		if _, err := q.SetScheduleNextRun(ctx, db.SetScheduleNextRunParams{NextRunAt: nextArg, ID: uint64(id)}); err != nil {
			return nil, err
		}
	} else if _, err := q.SetScheduleEnabled(ctx, db.SetScheduleEnabledParams{Enabled: false, ID: uint64(id)}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.Get(ctx, id, userID, isStaff)
}

// Delete soft-deletes the schedule (评测 §十二): pending delivery
// executions still resolve schedule.Name to build their Feishu message,
// so the row must survive. The scheduler scan (ListDueSchedules filters
// deleted_at IS NULL) and every owner surface stop seeing it; occurrences,
// runs and delivery executions are kept as history.
//
// The delivery CONFIG is kept too (评测 P1 — lock order): physically
// deleting schedule_deliveries here would take locks in the order
// deliveries→schedules, while the scheduler's admission takes
// schedules→deliveries, which is a deadlock cycle. Keeping the config also
// preserves the legacy fallback for occurrences whose delivery_snapshot_at
// is still NULL.
func (s *Service) Delete(ctx context.Context, id, userID int64, isStaff bool) error {
	if _, err := s.Get(ctx, id, userID, isStaff); err != nil {
		return err
	}
	if _, err := s.q(ctx).DeleteSchedule(ctx, uint64(id)); err != nil {
		return err
	}
	return nil
}

// ListDeliveries returns the delivery targets of an owned schedule.
func (s *Service) ListDeliveries(ctx context.Context, scheduleID, userID int64, isStaff bool) ([]*Delivery, error) {
	if _, err := s.Get(ctx, scheduleID, userID, isStaff); err != nil {
		return nil, err
	}
	rows, err := s.q(ctx).ListDeliveriesBySchedule(ctx, uint64(scheduleID))
	if err != nil {
		return nil, err
	}
	out := make([]*Delivery, 0, len(rows))
	for _, r := range rows {
		out = append(out, deliveryFromDBRow(r))
	}
	return out, nil
}

func deliveryFromDBRow(r db.ScheduleDelivery) *Delivery {
	return &Delivery{
		ID:                 int64(r.ID),
		ScheduleID:         int64(r.ScheduleID),
		Channel:            r.Channel,
		SenderIdentityMode: r.SenderIdentityMode,
		TargetType:         r.TargetType,
		TargetID:           r.TargetID,
		TargetName:         r.TargetName,
		ContentMode:        r.ContentMode,
		Enabled:            r.Enabled,
	}
}

// PreviewRuns computes the next `count` trigger times for a candidate
// trigger — the authoritative answer (the UI never predicts locally).
// Only trigger-related fields are validated; the full form check runs on
// save.
func PreviewRuns(in *CreateInput, count int) ([]time.Time, error) {
	in.defaults()
	if !policyValid("scheduleType", in.ScheduleType) {
		return nil, &ValidationError{Msg: "unsupported schedule_type"}
	}
	if in.ScheduleType == TypeOnce {
		if in.RunAt == nil {
			return nil, &ValidationError{Msg: "run_at is required"}
		}
	} else if err := in.TriggerConfig.Validate(in.ScheduleType); err != nil {
		return nil, &ValidationError{Msg: err.Error()}
	}
	if _, err := time.LoadLocation(in.Timezone); err != nil {
		return nil, &ValidationError{Msg: "unknown timezone"}
	}
	now := time.Now().UTC()
	out := make([]time.Time, 0, count)
	after := now
	for i := 0; i < count; i++ {
		nr, err := NextRunAfter(in.ScheduleType, in.TriggerConfig, in.RunAt, in.Timezone, after)
		if err != nil || nr.IsZero() {
			break
		}
		out = append(out, nr)
		after = nr
	}
	return out, nil
}

// ─────────────────────────────────────────────────────────────── helpers ──

func nullString(v string) sql.NullString {
	if v == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: v, Valid: true}
}

func jsonMarshal(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return b, nil
}
