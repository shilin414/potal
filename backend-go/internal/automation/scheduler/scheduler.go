// Package scheduler turns due Schedules into Runs. It owns the scan loop,
// the (schedule_id, scheduled_at) idempotency barrier and the policy
// decisions (overlap / misfire / execution window). It never talks to
// providers and never sends messages — the existing execution engine does.
package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/creation-agent-studio/backend-go/internal/automation/schedule"
	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
)

// misfireGrace is the delay beyond which a slot counts as misfired
// (scheduler downtime), unless an execution window already rejected it.
const misfireGrace = 5 * time.Minute

// RuntimeResolver supplies the enabled runtime binding for an application.
type RuntimeResolver interface {
	EnabledBinding(ctx context.Context, appID int64) (*BindingView, error)
}

// BindingView is the subset the scheduler needs (decoupled from catalog).
type BindingView struct {
	ID            int64
	ProviderKey   string
	RuntimeType   string
	ExecutionMode string
	Snapshot      map[string]any
}

// ErrNotSchedulable marks an application without an enabled binding.
var ErrNotSchedulable = errors.New("scheduler: application has no enabled runtime binding")

// Scheduler drives the due-scan loop.
type Scheduler struct {
	DB      *sql.DB
	Runs    *execution.Service
	Binding RuntimeResolver
	Log     *slog.Logger
	Metrics *telemetry.Metrics

	Interval time.Duration
	Batch    int

	nowFunc func() time.Time
}

func New(d *sql.DB, runs *execution.Service, binding RuntimeResolver, log *slog.Logger, m *telemetry.Metrics) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{DB: d, Runs: runs, Binding: binding, Log: log, Metrics: m,
		Interval: time.Second, Batch: 100, nowFunc: time.Now}
}

// Run loops until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	interval := s.Interval
	if interval <= 0 {
		interval = time.Second
	}
	batch := s.Batch
	if batch <= 0 {
		batch = 100
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	s.Log.Info("scheduler started", "interval", interval.String(), "batch", batch)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.processDue(ctx, batch)
		}
	}
}

func (s *Scheduler) q(ctx context.Context) db.Querier { return db.New(s.DB) }

// ProcessDue runs one scan tick (exported for tests and admin tooling).
func (s *Scheduler) ProcessDue(ctx context.Context) { s.processDue(ctx, s.Batch) }

func (s *Scheduler) processDue(ctx context.Context, batch int) {
	now := s.nowFunc().UTC()
	rows, err := s.q(ctx).ListDueSchedules(ctx, db.ListDueSchedulesParams{
		NextRunAt: sql.NullTime{Time: now, Valid: true},
		Limit:     int32(batch),
	})
	if err != nil {
		s.Log.Error("scheduler scan failed", "err", err)
		return
	}
	for _, row := range rows {
		slot := row.NextRunAt.Time.Truncate(time.Millisecond) // DATETIME(3) 丢纳秒
		sch := schedule.FromDBRow(row)
		if err := s.triggerSchedule(ctx, sch, slot); err != nil {
			s.Log.Error("schedule trigger failed",
				"schedule_id", sch.ID, "slot", slot.Format(time.RFC3339), "err", err)
		}
	}
}

// recordSlot atomically claims the slot with a terminal-status occurrence
// (skipped / failed) so the scan stops seeing it and no orphan pending
// occurrence can block future overlap checks. Losing the unique race is fine.
func (s *Scheduler) recordSlot(ctx context.Context, scheduleID int64, slot time.Time, status string) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()
	q := db.New(tx)
	if _, err := q.CreateScheduleOccurrence(ctx, db.CreateScheduleOccurrenceParams{
		ScheduleID: uint64(scheduleID), ScheduledAt: slot,
	}); err != nil {
		return // slot already claimed
	}
	occ, err := q.GetScheduleOccurrenceBySlot(ctx, db.GetScheduleOccurrenceBySlotParams{
		ScheduleID: uint64(scheduleID), ScheduledAt: slot,
	})
	if err != nil {
		return
	}
	if _, err := q.MarkOccurrenceStatus(ctx, db.MarkOccurrenceStatusParams{Status: status, ID: occ.ID}); err != nil {
		return
	}
	if err := s.advance(ctx, q, scheduleID, slot); err != nil {
		s.Log.Error("advance after skip failed", "schedule_id", scheduleID, "err", err)
		return
	}
	if err := tx.Commit(); err != nil {
		return
	}
	s.Log.Info("schedule occurrence recorded", "schedule_id", scheduleID, "status", status)
}

// advance recomputes next_run_at from the fired slot; a once schedule
// (or an exhausted trigger) disables itself.
func (s *Scheduler) advance(ctx context.Context, q db.Querier, scheduleID int64, firedSlot time.Time) error {
	row, err := q.GetScheduleByID(ctx, uint64(scheduleID))
	if err != nil {
		return err
	}
	sch := schedule.FromDBRow(row)
	next, err := schedule.NextRunAfter(sch.ScheduleType, sch.TriggerConfig, sch.RunAt, sch.Timezone, firedSlot)
	if err != nil {
		return err
	}
	var nextArg sql.NullTime
	if next.IsZero() {
		if _, err := q.SetScheduleEnabled(ctx, db.SetScheduleEnabledParams{Enabled: false, ID: uint64(scheduleID)}); err != nil {
			return err
		}
	} else {
		nextArg = sql.NullTime{Time: next, Valid: true}
	}
	_, err = q.TouchScheduleRunTimes(ctx, db.TouchScheduleRunTimesParams{
		LastRunAt: sql.NullTime{Time: firedSlot, Valid: true},
		NextRunAt: nextArg,
		ID:        uint64(scheduleID),
	})
	return err
}

// triggerSchedule applies policies then atomically creates
// occurrence + conversation + message + run + outbox + next_run_at.
func (s *Scheduler) triggerSchedule(ctx context.Context, sch *schedule.Schedule, slot time.Time) error {
	now := s.nowFunc().UTC()
	q := s.q(ctx)

	// Overlap policy: is a previous occurrence still active?
	active, err := q.HasActiveOccurrence(ctx, uint64(sch.ID))
	if err != nil {
		return fmt.Errorf("overlap check: %w", err)
	}
	if active > 0 {
		if sch.OverlapPolicy == schedule.OverlapSkip {
			s.recordSlot(ctx, sch.ID, slot, schedule.OccSkipped)
			if s.Metrics != nil {
				s.Metrics.OverlapSkippedTotal.Inc()
			}
			return nil
		}
		// queue: passively wait — the scan re-visits this slot every tick
		// (next_run_at untouched) and fires it once the previous run goes
		// terminal, which is the queued semantics (no parallel runs).
		return nil
	}

	// Execution window + misfire policies.
	delay := now.Sub(slot)
	window := time.Duration(sch.ExecutionWindowSeconds) * time.Second
	if window > 0 && delay > window && sch.DeadlinePolicy == schedule.DeadlineSkip {
		s.recordSlot(ctx, sch.ID, slot, schedule.OccSkipped)
		return nil
	}
	if delay > misfireGrace && sch.MisfirePolicy == schedule.MisfireSkip {
		s.recordSlot(ctx, sch.ID, slot, schedule.OccSkipped)
		if s.Metrics != nil {
			s.Metrics.ScheduleMisfireTotal.Inc()
		}
		return nil
	}

	binding, err := s.Binding.EnabledBinding(ctx, sch.ApplicationID)
	if err != nil {
		return fmt.Errorf("resolve binding: %w", err)
	}
	if binding == nil {
		// Record a failed occurrence so the UI shows why nothing ran.
		s.recordSlot(ctx, sch.ID, slot, schedule.OccFailed)
		return ErrNotSchedulable
	}

	occID, err := s.createOccurrenceAndRun(ctx, sch, slot, binding, now, "")
	if err != nil {
		return err
	}
	if s.Metrics != nil {
		s.Metrics.ScheduleTriggerDelay.WithLabelValues("fired").Observe(now.Sub(slot).Seconds())
	}
	s.Log.Info("schedule fired", "schedule_id", sch.ID, "occurrence_id", occID, "slot", slot.Format(time.RFC3339))
	return nil
}

// createOccurrenceAndRun commits occurrence + conversation + message +
// run + outbox + bookkeeping as ONE transaction (crash-safe: no orphan
// occurrences, no lost slots). mode == "run_now" marks a manual trigger.
func (s *Scheduler) createOccurrenceAndRun(ctx context.Context, sch *schedule.Schedule, slot time.Time, binding *BindingView, now time.Time, mode string) (int64, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	q := db.New(tx)

	scheduledAt := slot
	if mode == "run_now" {
		scheduledAt = now.Truncate(time.Millisecond) // DATETIME(3) 丢纳秒
	}
	if _, err := q.CreateScheduleOccurrence(ctx, db.CreateScheduleOccurrenceParams{
		ScheduleID: uint64(sch.ID), ScheduledAt: scheduledAt,
	}); err != nil {
		if isDuplicate(err) {
			return 0, nil // slot already claimed (concurrent run-now/scan)
		}
		return 0, fmt.Errorf("insert occurrence: %w", err)
	}
	occRow, err := q.GetScheduleOccurrenceBySlot(ctx, db.GetScheduleOccurrenceBySlotParams{
		ScheduleID: uint64(sch.ID), ScheduledAt: scheduledAt,
	})
	if err != nil {
		return 0, err
	}

	// Conversation policy.
	convID := int64(0)
	if sch.ConversationPolicy == schedule.ConversationReuse && sch.ConversationID != nil {
		convID = *sch.ConversationID
	}
	if convID == 0 {
		res, err := q.CreateConversation(ctx, db.CreateConversationParams{
			UserID:        uint64(sch.OwnerUserID),
			ApplicationID: sql.NullInt64{Int64: sch.ApplicationID, Valid: true},
			Title:         sch.Name,
		})
		if err != nil {
			return 0, fmt.Errorf("insert conversation: %w", err)
		}
		if convID, err = res.LastInsertId(); err != nil {
			return 0, err
		}
		if sch.ConversationPolicy == schedule.ConversationReuse {
			// Lazily bind the reused conversation on first run.
			if _, err := q.SetScheduleConversation(ctx, db.SetScheduleConversationParams{
				ConversationID: sql.NullInt64{Int64: convID, Valid: true}, ID: uint64(sch.ID),
			}); err != nil {
				return 0, err
			}
		}
	}

	prompt := ""
	if p, ok := sch.InputPayload["prompt"].(string); ok {
		prompt = p
	}
	runID, err := s.Runs.CreateRunInTx(ctx, tx, &execution.CreateRunInput{
		UserID:            sch.OwnerUserID,
		ApplicationID:     sch.ApplicationID,
		ConversationID:    convID,
		RuntimeBindingID:  binding.ID,
		Provider:          binding.ProviderKey,
		RuntimeType:       binding.RuntimeType,
		ExecutionMode:     binding.ExecutionMode,
		Content:           prompt,
		ConversationTitle: sch.Name,
		RuntimeSnapshot:   binding.Snapshot,
		TriggerType:       schedule.TriggerTypeScheduled,
		TriggerID:         int64(occRow.ID),
		Priority:          schedule.PriorityScheduledNormal,
		AvailableAt:       scheduledAt,
	})
	if err != nil {
		return 0, fmt.Errorf("create run: %w", err)
	}

	if _, err := q.MarkOccurrenceQueued(ctx, db.MarkOccurrenceQueuedParams{
		RunID:      sql.NullString{String: string(runID.Bytes()), Valid: true}, // BINARY(16) 原始字节
		AdmittedAt: sql.NullTime{Time: now, Valid: true},
		ID:         occRow.ID,
	}); err != nil {
		return 0, err
	}

	if mode == "run_now" {
		if _, err := q.SetScheduleLastRun(ctx, db.SetScheduleLastRunParams{
			LastRunAt: sql.NullTime{Time: now, Valid: true},
			ID:        uint64(sch.ID),
		}); err != nil {
			return 0, err
		}
	} else if err := s.advance(ctx, q, sch.ID, slot); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int64(occRow.ID), nil
}

// TriggerNow implements run-now: a real occurrence at `now` that walks the
// full scheduled pipeline (overlap applies), updates last_run_at and leaves
// next_run_at untouched.
func (s *Scheduler) TriggerNow(ctx context.Context, scheduleID, userID int64, isStaff bool) (*schedule.Occurrence, error) {
	row, err := s.q(ctx).GetScheduleByID(ctx, uint64(scheduleID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, schedule.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if row.OwnerUserID != uint64(userID) && !isStaff {
		return nil, schedule.ErrNotFound
	}
	if !row.Enabled {
		return nil, fmt.Errorf("schedule is disabled")
	}
	sch := schedule.FromDBRow(row)

	n, err := s.q(ctx).HasActiveOccurrence(ctx, uint64(scheduleID))
	if err != nil {
		return nil, err
	}
	if n > 0 && sch.OverlapPolicy == schedule.OverlapSkip {
		return nil, fmt.Errorf("a previous run is still active (overlap policy: skip)")
	}

	binding, err := s.Binding.EnabledBinding(ctx, sch.ApplicationID)
	if err != nil {
		return nil, fmt.Errorf("resolve binding: %w", err)
	}
	if binding == nil {
		return nil, ErrNotSchedulable
	}

	occID, err := s.createOccurrenceAndRun(ctx, sch, time.Time{}, binding, s.nowFunc().UTC(), "run_now")
	if err != nil {
		return nil, err
	}
	row2, err := s.q(ctx).GetScheduleOccurrenceByID(ctx, uint64(occID))
	if err != nil {
		return nil, err
	}
	return schedule.OccurrenceFromDBRow(row2), nil
}

// GetOccurrence loads one occurrence with ownership check.
func (s *Scheduler) GetOccurrence(ctx context.Context, occID, userID int64, isStaff bool) (*schedule.Occurrence, error) {
	row, err := s.q(ctx).GetScheduleOccurrenceByID(ctx, uint64(occID))
	if err != nil {
		return nil, err
	}
	sch, err := s.q(ctx).GetScheduleByID(ctx, row.ScheduleID)
	if err != nil {
		return nil, err
	}
	if sch.OwnerUserID != uint64(userID) && !isStaff {
		return nil, schedule.ErrNotFound
	}
	return schedule.OccurrenceFromDBRow(row), nil
}

func isDuplicate(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == 1062
}
