package schedule

import (
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return loc
}

// TestNextRunAfterDaily: daily 09:00 rolls to tomorrow when today passed.
func TestNextRunAfterDaily(t *testing.T) {
	loc := mustLoc(t, "Asia/Shanghai")
	after := time.Date(2026, 9, 13, 10, 0, 0, 0, loc) // Sunday 10:00 local
	next, err := NextRunAfter(TypeDaily, TriggerConfig{Time: "09:00"}, nil, "Asia/Shanghai", after.UTC())
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 14, 9, 0, 0, 0, loc)
	if !next.Equal(want.UTC()) {
		t.Fatalf("next = %v, want %v", next, want.UTC())
	}
}

// TestNextRunAfterDailySameDay: a later slot today still fires today.
func TestNextRunAfterDailySameDay(t *testing.T) {
	loc := mustLoc(t, "Asia/Shanghai")
	after := time.Date(2026, 9, 13, 8, 0, 0, 0, loc)
	next, _ := NextRunAfter(TypeDaily, TriggerConfig{Time: "09:00"}, nil, "Asia/Shanghai", after.UTC())
	want := time.Date(2026, 9, 13, 9, 0, 0, 0, loc)
	if !next.Equal(want.UTC()) {
		t.Fatalf("next = %v, want %v", next, want.UTC())
	}
}

// TestNextRunAfterWeeklyMultiDay: picks the nearest of several weekdays.
func TestNextRunAfterWeeklyMultiDay(t *testing.T) {
	loc := mustLoc(t, "Asia/Shanghai")
	// 2026-09-13 is a Sunday. Next Mon(1) 09:00 is 2026-09-14.
	after := time.Date(2026, 9, 13, 12, 0, 0, 0, loc)
	next, err := NextRunAfter(TypeWeekly, TriggerConfig{Time: "09:00", DaysOfWeek: []int{1, 3, 5}}, nil, "Asia/Shanghai", after.UTC())
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 14, 9, 0, 0, 0, loc)
	if !next.Equal(want.UTC()) {
		t.Fatalf("next = %v, want %v", next, want.UTC())
	}
}

// TestNextRunAfterMonthlyClampShortMonth: 31st runs on Feb 28/29.
func TestNextRunAfterMonthlyClampShortMonth(t *testing.T) {
	loc := mustLoc(t, "Asia/Shanghai")
	after := time.Date(2026, 1, 31, 10, 0, 0, 0, loc)
	next, err := NextRunAfter(TypeMonthly, TriggerConfig{Time: "09:00", DayOfMonth: 31}, nil, "Asia/Shanghai", after.UTC())
	if err != nil {
		t.Fatal(err)
	}
	// Feb 2026 has 28 days → clamp to Feb 28.
	want := time.Date(2026, 2, 28, 9, 0, 0, 0, loc)
	if !next.Equal(want.UTC()) {
		t.Fatalf("next = %v, want %v", next, want.UTC())
	}
}

// TestNextRunAfterMonthlyLeapDay: 2028 is a leap year → Feb 29.
func TestNextRunAfterMonthlyLeapDay(t *testing.T) {
	loc := mustLoc(t, "Asia/Shanghai")
	after := time.Date(2028, 1, 31, 10, 0, 0, 0, loc)
	next, _ := NextRunAfter(TypeMonthly, TriggerConfig{Time: "09:00", DayOfMonth: 31}, nil, "Asia/Shanghai", after.UTC())
	want := time.Date(2028, 2, 29, 9, 0, 0, 0, loc)
	if !next.Equal(want.UTC()) {
		t.Fatalf("next = %v, want %v", next, want.UTC())
	}
}

// TestNextRunAfterOnce: past run_at yields zero (never again).
func TestNextRunAfterOnce(t *testing.T) {
	loc := mustLoc(t, "Asia/Shanghai")
	runAt := time.Date(2026, 9, 12, 9, 0, 0, 0, loc)
	next, err := NextRunAfter(TypeOnce, TriggerConfig{}, &runAt, "Asia/Shanghai",
		time.Date(2026, 9, 13, 0, 0, 0, 0, loc).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if !next.IsZero() {
		t.Fatalf("expected zero for past once slot, got %v", next)
	}
}

// TestValidate rejects bad configs.
func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		typ  string
		cfg  TriggerConfig
		ok   bool
	}{
		{"daily ok", TypeDaily, TriggerConfig{Time: "09:00"}, true},
		{"daily bad time", TypeDaily, TriggerConfig{Time: "24:99"}, false},
		{"weekly no days", TypeWeekly, TriggerConfig{Time: "09:00"}, false},
		{"weekly bad day", TypeWeekly, TriggerConfig{Time: "09:00", DaysOfWeek: []int{7}}, false},
		{"monthly ok", TypeMonthly, TriggerConfig{Time: "09:00", DayOfMonth: 31}, true},
		{"monthly bad dom", TypeMonthly, TriggerConfig{Time: "09:00", DayOfMonth: 0}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate(tc.typ)
			if tc.ok != (err == nil) {
				t.Fatalf("Validate() = %v, wantOk %v", err, tc.ok)
			}
		})
	}
}

// TestCronExpression: informational derivation matches the trigger.
func TestCronExpression(t *testing.T) {
	cfg := TriggerConfig{Time: "09:30", DaysOfWeek: []int{1, 3, 5}, DayOfMonth: 15}
	if got := cfg.CronExpression(TypeDaily); got != "30 9 * * *" {
		t.Fatalf("daily cron = %q", got)
	}
	if got := cfg.CronExpression(TypeWeekly); got != "30 9 * * 1,3,5" {
		t.Fatalf("weekly cron = %q", got)
	}
	if got := cfg.CronExpression(TypeMonthly); got != "30 9 15 * *" {
		t.Fatalf("monthly cron = %q", got)
	}
}

// TestPreviewRunsIsMonotonic: preview slots strictly increase.
func TestPreviewRunsIsMonotonic(t *testing.T) {
	in := &CreateInput{
		ScheduleType:  TypeDaily,
		Timezone:      "Asia/Shanghai",
		Prompt:        "p",
		TriggerConfig: TriggerConfig{Time: "09:00"},
	}
	runs, err := PreviewRuns(in, 3)
	if err != nil || len(runs) != 3 {
		t.Fatalf("PreviewRuns = %v, %v", runs, err)
	}
	for i := 1; i < len(runs); i++ {
		if !runs[i].After(runs[i-1]) {
			t.Fatalf("preview not monotonic: %v then %v", runs[i-1], runs[i])
		}
	}
}
