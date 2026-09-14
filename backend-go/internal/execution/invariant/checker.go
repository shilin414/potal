// Package invariant implements the execution invariant checker
// (修复计划 §42-51, Phase 9).
//
// The checker DETECTS violations, emits metrics and logs alerts. It never
// auto-repairs: the only automatic recovery coordinator is the reaper.
// Invariants:
//
//	A: running run ⇔ active lease exists
//	B: queued run holds no lease
//	C: terminal run holds no lease
//	D: terminal run has exactly one terminal RunEvent
//	E: running run's lease_epoch == its lease row's epoch
//	F: overlap=queue schedules never hold >1 active occurrence
//	G: pending outbox age stays below the threshold
package invariant

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
)

// Violation types exported for metric labels and alerts.
const (
	TypeRunningWithoutLease            = "running_without_lease"
	TypeQueuedWithLease                = "queued_with_lease"
	TypeTerminalWithLease              = "terminal_with_lease"
	TypeTerminalWithoutTerminalEvent   = "terminal_without_terminal_event"
	TypeLeaseEpochMismatch             = "running_lease_epoch_mismatch"
	TypeScheduleOverlapViolation       = "schedule_overlap_violation"
	TypeOutboxBacklogAge               = "outbox_backlog_age"
	TypeScheduledRunOccurrenceMismatch = "scheduled_run_occurrence_mismatch"
	TypeMissingDeliveryExecution       = "missing_delivery_execution"
)

// Checker scans the canonical state and reports invariant violations.
type Checker struct {
	DB           *sql.DB
	Log          *slog.Logger
	Metrics      *telemetry.Metrics
	OutboxMaxAge time.Duration // Invariant G threshold (default 30s)
}

// Result summarizes one check round.
type Result struct {
	TotalViolations int
}

// RunOnce executes every invariant query and reports violations via
// metrics + logs. Safe to run concurrently with live traffic (read-only
// scans; no locks are taken).
func (c *Checker) RunOnce(ctx context.Context) Result {
	q := db.New(c.DB)
	var res Result

	// A. running without lease.
	if n, err := q.CountRunningWithoutLease(ctx); err == nil && n > 0 {
		res.TotalViolations += int(n)
		c.report(TypeRunningWithoutLease, n)
	} else if err != nil {
		c.Log.Warn("invariant check failed", "type", TypeRunningWithoutLease, "err", err)
	}

	// B. queued with lease.
	if n, err := q.CountQueuedWithLease(ctx); err == nil && n > 0 {
		res.TotalViolations += int(n)
		c.report(TypeQueuedWithLease, n)
	} else if err != nil {
		c.Log.Warn("invariant check failed", "type", TypeQueuedWithLease, "err", err)
	}

	// C. terminal with lease.
	if n, err := q.CountTerminalWithLease(ctx); err == nil && n > 0 {
		res.TotalViolations += int(n)
		c.report(TypeTerminalWithLease, n)
	} else if err != nil {
		c.Log.Warn("invariant check failed", "type", TypeTerminalWithLease, "err", err)
	}

	// D. terminal without terminal event.
	if n, err := q.CountTerminalWithoutTerminalEvent(ctx); err == nil && n > 0 {
		res.TotalViolations += int(n)
		c.report(TypeTerminalWithoutTerminalEvent, n)
	} else if err != nil {
		c.Log.Warn("invariant check failed", "type", TypeTerminalWithoutTerminalEvent, "err", err)
	}

	// E. running run epoch vs its lease row epoch.
	if n, err := q.CountRunningLeaseEpochMismatch(ctx); err == nil && n > 0 {
		res.TotalViolations += int(n)
		c.report(TypeLeaseEpochMismatch, n)
	} else if err != nil {
		c.Log.Warn("invariant check failed", "type", TypeLeaseEpochMismatch, "err", err)
	}

	// F. overlap=queue parallel actives.
	if rows, err := q.CountScheduleOverlapViolations(ctx); err == nil {
		for _, row := range rows {
			res.TotalViolations += int(row.N - 1)
			c.Log.Error("invariant violated: schedule overlap",
				"type", TypeScheduleOverlapViolation,
				"schedule_id", row.ScheduleID, "active", row.N)
			if c.Metrics != nil {
				c.Metrics.InvariantViolation.WithLabelValues(TypeScheduleOverlapViolation).Add(float64(row.N - 1))
			}
		}
	} else if err != nil {
		c.Log.Warn("invariant check failed", "type", TypeScheduleOverlapViolation, "err", err)
	}

	// G. outbox backlog age.
	if age, err := q.OldestPendingOutboxAgeSeconds(ctx); err == nil {
		maxAge := c.OutboxMaxAge
		if maxAge <= 0 {
			maxAge = 30 * time.Second
		}
		if d := time.Duration(age) * time.Second; d > maxAge {
			res.TotalViolations++
			c.report(TypeOutboxBacklogAge, age)
		}
	} else {
		c.Log.Warn("invariant check failed", "type", TypeOutboxBacklogAge, "err", err)
	}

	// H. scheduled terminal run / occurrence convergence.
	if n, err := q.CountScheduledRunOccurrenceMismatch(ctx); err == nil && n > 0 {
		res.TotalViolations += int(n)
		c.report(TypeScheduledRunOccurrenceMismatch, n)
	} else if err != nil {
		c.Log.Warn("invariant check failed", "type", TypeScheduledRunOccurrenceMismatch, "err", err)
	}

	// L. durable delivery request coverage.
	if n, err := q.CountMissingDeliveryExecutions(ctx); err == nil && n > 0 {
		res.TotalViolations += int(n)
		c.report(TypeMissingDeliveryExecution, n)
	} else if err != nil {
		c.Log.Warn("invariant check failed", "type", TypeMissingDeliveryExecution, "err", err)
	}

	return res
}

func (c *Checker) report(violationType string, n int64) {
	c.Log.Error("execution invariant violated",
		"type", violationType, "count", n)
	if c.Metrics != nil {
		c.Metrics.InvariantViolation.WithLabelValues(violationType).Add(float64(n))
	}
}
