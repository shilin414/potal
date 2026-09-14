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
	"github.com/creation-agent-studio/backend-go/internal/execution"
	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
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
			s.ProcessDue(ctx)
		}
	}
}

func (s *Scheduler) q(ctx context.Context) db.Querier { return db.New(s.DB) }

// ProcessDue runs one scan tick (exported for tests and admin tooling):
// first admit any pending occurrences (overlap=queue backlog), then fire
// due schedule slots.
func (s *Scheduler) ProcessDue(ctx context.Context) {
	s.admitPending(ctx, s.Batch)
	s.processDue(ctx, s.Batch)
}

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

// advancePast fast-forwards next_run_at to the first slot strictly after
// `now` (skip / fire_once semantics) and returns how many slots were
// skipped on the way. A once schedule disables itself.
func (s *Scheduler) advancePast(ctx context.Context, q db.Querier, scheduleID int64, fromSlot, now time.Time) (int, error) {
	row, err := q.GetScheduleByID(ctx, uint64(scheduleID))
	if err != nil {
		return 0, err
	}
	sch := schedule.FromDBRow(row)
	next := fromSlot
	skipped := 0
	for i := 0; i < 100000; i++ { // hard bound: per-minute × months
		cand, err := schedule.NextRunAfter(sch.ScheduleType, sch.TriggerConfig, sch.RunAt, sch.Timezone, next)
		if err != nil {
			return 0, err
		}
		if cand.IsZero() {
			// Never fires again: disable.
			if _, err := q.SetScheduleEnabled(ctx, db.SetScheduleEnabledParams{Enabled: false, ID: uint64(scheduleID)}); err != nil {
				return 0, err
			}
			return skipped, nil
		}
		next = cand
		if next.After(now) {
			break
		}
		skipped++
	}
	_, err = q.TouchScheduleRunTimes(ctx, db.TouchScheduleRunTimesParams{
		LastRunAt: sql.NullTime{Time: fromSlot, Valid: true},
		NextRunAt: sql.NullTime{Time: next, Valid: true},
		ID:        uint64(scheduleID),
	})
	return skipped, err
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

	// Execution window policy.
	delay := now.Sub(slot)
	window := time.Duration(sch.ExecutionWindowSeconds) * time.Second
	if window > 0 && delay > window && sch.DeadlinePolicy == schedule.DeadlineSkip {
		s.recordSlot(ctx, sch.ID, slot, schedule.OccSkipped)
		return nil
	}

	// Misfire policies (scheduler downtime): the slot is older than the
	// grace period. The three policies have genuinely different
	// algorithms (评测 P1 — previously fire_once silently degraded into
	// catch-up by advancing one slot per tick).
	if delay > misfireGrace {
		switch sch.MisfirePolicy {
		case schedule.MisfireSkip:
			// Do not execute; fast-forward to the first future slot.
			if err := s.skipPast(ctx, sch, slot, now, "misfire: skip"); err != nil {
				return err
			}
			if s.Metrics != nil {
				s.Metrics.ScheduleMisfireTotal.Inc()
			}
			return nil
		case schedule.MisfireCatchUp:
			// Allowed to replay missed slots one at a time, but bounded
			// by MaxCatchUpSlots — beyond that we fast-forward like skip.
			missed, err := s.countMissed(sch, slot, now)
			if err != nil {
				return err
			}
			if missed > schedule.MaxCatchUpSlots {
				if err := s.skipPast(ctx, sch, slot, now, "misfire: catch_up over limit"); err != nil {
					return err
				}
				if s.Metrics != nil {
					s.Metrics.ScheduleMisfireTotal.Inc()
				}
				return nil
			}
			// Fall through: fire this (oldest) missed slot; the next tick
			// fires the following one until caught up.
		default:
			// fire_once (default): exactly ONE compensation run for the
			// missed window at the original planned slot, then jump
			// next_run_at past now — never slot-by-slot catch-up.
			occID, err := s.fireMisfiredOnce(ctx, sch, slot, now)
			if err != nil {
				return err
			}
			_ = occID
			if s.Metrics != nil {
				s.Metrics.ScheduleMisfireTotal.Inc()
			}
			return nil
		}
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

// skipPast claims the slot as skipped and fast-forwards next_run_at past
// now in one transaction.
func (s *Scheduler) skipPast(ctx context.Context, sch *schedule.Schedule, slot, now time.Time, reason string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := db.New(tx)
	if _, err := q.CreateScheduleOccurrence(ctx, db.CreateScheduleOccurrenceParams{
		ScheduleID: uint64(sch.ID), ScheduledAt: slot,
	}); err != nil {
		if !isDuplicate(err) {
			return err
		}
		// Slot already claimed by a concurrent scan; still advance.
	} else if occ, err := q.GetScheduleOccurrenceBySlot(ctx, db.GetScheduleOccurrenceBySlotParams{
		ScheduleID: uint64(sch.ID), ScheduledAt: slot,
	}); err == nil {
		_, _ = q.MarkOccurrenceStatus(ctx, db.MarkOccurrenceStatusParams{Status: schedule.OccSkipped, ID: occ.ID})
	}
	if _, err := s.advancePast(ctx, q, sch.ID, slot, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.Log.Info("schedule slot skipped", "schedule_id", sch.ID, "slot", slot.Format(time.RFC3339), "reason", reason)
	return nil
}

// fireMisfiredOnce creates ONE run for the oldest missed slot (original
// scheduled_at preserved) and fast-forwards next_run_at past now.
func (s *Scheduler) fireMisfiredOnce(ctx context.Context, sch *schedule.Schedule, slot, now time.Time) (int64, error) {
	binding, err := s.Binding.EnabledBinding(ctx, sch.ApplicationID)
	if err != nil {
		return 0, fmt.Errorf("resolve binding: %w", err)
	}
	if binding == nil {
		return 0, s.skipPast(ctx, sch, slot, now, "misfire: not schedulable")
	}
	occID, err := s.createOccurrenceAndRun(ctx, sch, slot, binding, now, "misfire")
	if err != nil {
		return 0, err
	}
	if s.Metrics != nil {
		s.Metrics.ScheduleTriggerDelay.WithLabelValues("misfired").Observe(now.Sub(slot).Seconds())
	}
	s.Log.Info("schedule misfired-once fired", "schedule_id", sch.ID, "occurrence_id", occID, "slot", slot.Format(time.RFC3339))
	return occID, nil
}

// countMissed counts how many slots between `slot` (exclusive) and `now`
// were missed — pure computation, no DB writes.
func (s *Scheduler) countMissed(sch *schedule.Schedule, slot, now time.Time) (int, error) {
	next := slot
	missed := 0
	for i := 0; i < 100000; i++ {
		cand, err := schedule.NextRunAfter(sch.ScheduleType, sch.TriggerConfig, sch.RunAt, sch.Timezone, next)
		if err != nil {
			return 0, err
		}
		if cand.IsZero() || cand.After(now) {
			break
		}
		next = cand
		missed++
	}
	return missed, nil
}

// createRunForOccurrenceTx creates conversation + message + run + outbox
// for an EXISTING occurrence row inside the caller's transaction and
// marks the occurrence queued. Shared by the scan path, misfire
// compensation and pending-occurrence admission.
func (s *Scheduler) createRunForOccurrenceTx(ctx context.Context, tx *sql.Tx, sch *schedule.Schedule, occID uint64, binding *BindingView, now time.Time) (ids0 string, err error) {
	q := db.New(tx)

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
			return "", fmt.Errorf("insert conversation: %w", err)
		}
		if convID, err = res.LastInsertId(); err != nil {
			return "", err
		}
		if sch.ConversationPolicy == schedule.ConversationReuse {
			// Lazily bind the reused conversation on first run.
			if _, err := q.SetScheduleConversation(ctx, db.SetScheduleConversationParams{
				ConversationID: sql.NullInt64{Int64: convID, Valid: true}, ID: uint64(sch.ID),
			}); err != nil {
				return "", err
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
		TriggerID:         int64(occID),
		Priority:          schedule.PriorityScheduledNormal,
	})
	if err != nil {
		return "", fmt.Errorf("create run: %w", err)
	}

	if _, err := q.MarkOccurrenceQueued(ctx, db.MarkOccurrenceQueuedParams{
		RunID:      sql.NullString{String: string(runID.Bytes()), Valid: true}, // BINARY(16) 原始字节
		AdmittedAt: sql.NullTime{Time: now, Valid: true},
		ID:         occID,
	}); err != nil {
		return "", err
	}
	return runID.String(), nil
}

// createOccurrenceAndRun commits occurrence + conversation + message +
// run + outbox + bookkeeping as ONE transaction (crash-safe: no orphan
// occurrences, no lost slots). mode "run_now" marks a manual trigger;
// mode "misfire" fires the missed slot once and fast-forwards
// next_run_at past now.
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

	if _, err := s.createRunForOccurrenceTx(ctx, tx, sch, occRow.ID, binding, now); err != nil {
		return 0, err
	}

	if mode == "run_now" {
		if _, err := q.SetScheduleLastRun(ctx, db.SetScheduleLastRunParams{
			LastRunAt: sql.NullTime{Time: now, Valid: true},
			ID:        uint64(sch.ID),
		}); err != nil {
			return 0, err
		}
	} else if mode == "misfire" {
		// fire_once misfire: jump to the first slot after now — never
		// replay the intermediate missed slots.
		if _, err := s.advancePast(ctx, q, sch.ID, slot, now); err != nil {
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
// full scheduled pipeline, updates last_run_at and leaves next_run_at
// untouched. Overlap policies apply to manual triggers exactly like
// scheduled ones (评测 P1): skip rejects while a run is active; queue
// creates a PENDING occurrence that the admission loop converts into a
// run as soon as the active execution goes terminal — never a parallel
// second run.
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
	if n > 0 && sch.OverlapPolicy == schedule.OverlapQueue {
		return s.enqueuePendingOccurrence(ctx, sch)
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

// enqueuePendingOccurrence records a manual trigger that must wait for
// the active execution (overlap=queue): the occurrence stays pending and
// admitPending converts it into a run later.
func (s *Scheduler) enqueuePendingOccurrence(ctx context.Context, sch *schedule.Schedule) (*schedule.Occurrence, error) {
	now := s.nowFunc().UTC().Truncate(time.Millisecond)
	if _, err := s.q(ctx).CreateScheduleOccurrence(ctx, db.CreateScheduleOccurrenceParams{
		ScheduleID: uint64(sch.ID), ScheduledAt: now,
	}); err != nil {
		if isDuplicate(err) {
			return nil, fmt.Errorf("a queued manual trigger already exists")
		}
		return nil, err
	}
	occRow, err := s.q(ctx).GetScheduleOccurrenceBySlot(ctx, db.GetScheduleOccurrenceBySlotParams{
		ScheduleID: uint64(sch.ID), ScheduledAt: now,
	})
	if err != nil {
		return nil, err
	}
	s.Log.Info("run-now queued behind active occurrence", "schedule_id", sch.ID, "occurrence_id", occRow.ID)
	return schedule.OccurrenceFromDBRow(occRow), nil
}

// admitPending converts pending occurrences (overlap=queue backlog and
// stuck misfire rows) into runs once their schedule has no queued/running
// execution. This is the unified Occurrence Admission: overlap policy
// belongs to the scheduler, not to the UI or the worker.
func (s *Scheduler) admitPending(ctx context.Context, batch int) {
	rows, err := s.q(ctx).ListAdmissiblePendingOccurrences(ctx, int32(batch))
	if err != nil {
		s.Log.Error("admission scan failed", "err", err)
		return
	}
	for _, occRow := range rows {
		if err := s.admitOne(ctx, occRow); err != nil {
			s.Log.Error("occurrence admission failed", "occurrence_id", occRow.ID, "err", err)
		}
	}
}

func (s *Scheduler) admitOne(ctx context.Context, occRow db.ScheduleOccurrence) error {
	schRow, err := s.q(ctx).GetScheduleByID(ctx, occRow.ScheduleID)
	if err != nil {
		return err
	}
	sch := schedule.FromDBRow(schRow)
	if !sch.Enabled {
		_, err := s.q(ctx).MarkOccurrenceStatus(ctx, db.MarkOccurrenceStatusParams{Status: schedule.OccSkipped, ID: occRow.ID})
		return err
	}
	// Admission check: nothing else queued/running for this schedule
	// (the pending row itself must not block its own admission).
	active, err := s.q(ctx).CountActiveOccurrencesExcluding(ctx, db.CountActiveOccurrencesExcludingParams{
		ScheduleID: occRow.ScheduleID, ID: occRow.ID,
	})
	if err != nil {
		return err
	}
	if active > 0 {
		return nil // still waiting (queue semantics)
	}
	binding, err := s.Binding.EnabledBinding(ctx, sch.ApplicationID)
	if err != nil {
		return err
	}
	if binding == nil {
		_, err := s.q(ctx).MarkOccurrenceStatus(ctx, db.MarkOccurrenceStatusParams{Status: schedule.OccFailed, ID: occRow.ID})
		if err != nil {
			return err
		}
		return ErrNotSchedulable
	}
	now := s.nowFunc().UTC()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := db.New(tx)
	// Admission lock (修复计划 §34-37): serialize concurrent admissions
	// for the SAME schedule. Two schedulers each holding a pending
	// occurrence could otherwise both count zero active occurrences and
	// create two parallel runs (write skew) — the schedules row lock
	// closes that window: the second transaction re-checks AFTER the
	// first one committed.
	if _, err := q.GetScheduleRowForUpdate(ctx, occRow.ScheduleID); err != nil {
		return err
	}
	// Re-check under the lock: another admission/scan may have raced us
	// to a queued occurrence for the same schedule.
	active2, err := q.CountActiveOccurrencesExcluding(ctx, db.CountActiveOccurrencesExcludingParams{
		ScheduleID: occRow.ScheduleID, ID: occRow.ID,
	})
	if err != nil {
		return err
	}
	if active2 > 0 {
		return nil
	}
	if _, err := s.createRunForOccurrenceTx(ctx, tx, sch, occRow.ID, binding, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.Log.Info("pending occurrence admitted", "schedule_id", sch.ID, "occurrence_id", occRow.ID)
	return nil
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
