// Package schedule owns the Schedule domain: what runs, when, with which
// policies. Schedule only answers "when to create a Run"; it never calls
// providers or sends messages itself.
package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule types (first stage): structured triggers only — no raw user cron.
const (
	TypeOnce    = "once"
	TypeDaily   = "daily"
	TypeWeekly  = "weekly"
	TypeMonthly = "monthly"
)

// Policies.
const (
	OverlapSkip  = "skip"
	OverlapQueue = "queue"

	MisfireFireOnce = "fire_once"
	MisfireSkip     = "skip"

	ConversationNewEachRun = "new_each_run"
	ConversationReuse      = "reuse"

	DeadlineSkip          = "skip"
	DeadlineExecuteAnyway = "execute_anyway"
)

// TriggerConfig is the structured trigger the UI submits; the backend
// derives timing from it. cron_expression stays an internal artifact.
type TriggerConfig struct {
	// Time is "HH:MM" in the schedule timezone (daily/weekly/monthly).
	Time string `json:"time,omitempty"`
	// DaysOfWeek: 0=Sunday … 6=Saturday (weekly, multiple allowed).
	DaysOfWeek []int `json:"days_of_week,omitempty"`
	// DayOfMonth: 1..31; short months clamp to the last day.
	DayOfMonth int `json:"day_of_month,omitempty"`
}

// Validate checks the config against its schedule type.
func (t TriggerConfig) Validate(scheduleType string) error {
	switch scheduleType {
	case TypeOnce:
		return nil
	case TypeDaily:
		return t.validateTime()
	case TypeWeekly:
		if err := t.validateTime(); err != nil {
			return err
		}
		if len(t.DaysOfWeek) == 0 {
			return fmt.Errorf("weekly trigger needs at least one day_of_week")
		}
		for _, d := range t.DaysOfWeek {
			if d < 0 || d > 6 {
				return fmt.Errorf("day_of_week %d out of range 0-6", d)
			}
		}
		return nil
	case TypeMonthly:
		if err := t.validateTime(); err != nil {
			return err
		}
		if t.DayOfMonth < 1 || t.DayOfMonth > 31 {
			return fmt.Errorf("day_of_month out of range 1-31")
		}
		return nil
	default:
		return fmt.Errorf("unsupported schedule_type %q", scheduleType)
	}
}

func (t TriggerConfig) validateTime() error {
	if len(t.Time) != 5 || t.Time[2] != ':' {
		return fmt.Errorf("time must be HH:MM")
	}
	h, err1 := strconv.Atoi(t.Time[:2])
	m, err2 := strconv.Atoi(t.Time[3:])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return fmt.Errorf("time must be HH:MM (00:00-23:59)")
	}
	return nil
}

// CronExpression renders an informational 5-field cron (no seconds) —
// internal/debug use; the UI never asks users to write cron.
func (t TriggerConfig) CronExpression(scheduleType string) string {
	hm := strings.SplitN(t.Time, ":", 2)
	if len(hm) != 2 {
		return ""
	}
	m, errM := strconv.Atoi(hm[1])
	h, errH := strconv.Atoi(hm[0])
	if errM != nil || errH != nil {
		return ""
	}
	switch scheduleType {
	case TypeDaily:
		return fmt.Sprintf("%d %d * * *", m, h)
	case TypeWeekly:
		ds := make([]string, 0, len(t.DaysOfWeek))
		for _, d := range t.DaysOfWeek {
			ds = append(ds, strconv.Itoa(d))
		}
		return fmt.Sprintf("%d %d * * %s", m, h, strings.Join(ds, ","))
	case TypeMonthly:
		return fmt.Sprintf("%d %d %d * *", m, h, t.DayOfMonth)
	}
	return ""
}

// NextRunAfter returns the first trigger strictly after `after` (UTC in,
// UTC out). For monthly, 29/30/31 clamp to the last day of short months.
// Returns zero time when the schedule never fires again (exhausted once).
func NextRunAfter(scheduleType string, cfg TriggerConfig, runAt *time.Time, timezone string, after time.Time) (time.Time, error) {
	switch scheduleType {
	case TypeOnce:
		if runAt == nil {
			return time.Time{}, fmt.Errorf("once schedule requires run_at")
		}
		if runAt.After(after) {
			return *runAt, nil
		}
		return time.Time{}, nil
	case TypeDaily, TypeWeekly, TypeMonthly:
		loc, err := time.LoadLocation(timezone)
		if err != nil {
			return time.Time{}, fmt.Errorf("unknown timezone %q: %w", timezone, err)
		}
		if err := cfg.Validate(scheduleType); err != nil {
			return time.Time{}, err
		}
		hh, _ := strconv.Atoi(cfg.Time[:2])
		mm, _ := strconv.Atoi(cfg.Time[3:])
		local := after.In(loc)
		day := time.Date(local.Year(), local.Month(), local.Day(), hh, mm, 0, 0, loc)
		if scheduleType == TypeDaily {
			if !day.After(after) {
				day = day.AddDate(0, 0, 1)
			}
			return day.UTC(), nil
		}
		if scheduleType == TypeWeekly {
			for i := 0; i < 8; i++ { // 7 days is enough; 8 for paranoia
				cand := day.AddDate(0, 0, i)
				if matchesWeekday(cand, cfg.DaysOfWeek) && cand.After(after) {
					return cand.UTC(), nil
				}
			}
			return time.Time{}, fmt.Errorf("weekly: no matching weekday")
		}
		// Monthly: scan up to 14 months. Anchor on the first of the
		// month — AddDate on day 31 would overflow short months.
		monthStart := time.Date(local.Year(), local.Month(), 1, hh, mm, 0, 0, loc)
		for i := 0; i < 14; i++ {
			cand := clampToMonth(monthStart.AddDate(0, i, 0), cfg.DayOfMonth, hh, mm)
			if cand.After(after) {
				return cand.UTC(), nil
			}
		}
		return time.Time{}, fmt.Errorf("monthly: no future slot")
	default:
		return time.Time{}, fmt.Errorf("unsupported schedule_type %q", scheduleType)
	}
}

func matchesWeekday(t time.Time, days []int) bool {
	wd := int(t.Weekday())
	for _, d := range days {
		if d == wd {
			return true
		}
	}
	return false
}

// clampToMonth builds day-of-month `dom` at hh:mm for cand's month,
// clamping to the last day when the month is shorter (31 → Feb 28/29).
func clampToMonth(monthAnchor time.Time, dom, hh, mm int) time.Time {
	last := time.Date(monthAnchor.Year(), monthAnchor.Month()+1, 0, 0, 0, 0, 0, monthAnchor.Location())
	day := dom
	if day > last.Day() {
		day = last.Day()
	}
	return time.Date(monthAnchor.Year(), monthAnchor.Month(), day, hh, mm, 0, 0, monthAnchor.Location())
}
