package directory

import (
	"testing"
	"time"
)

func TestValidateAndBuildClosure(t *testing.T) {
	deps := []stagedDepartment{
		{OpenID: "root", ParentOpenID: "0", Active: true},
		{OpenID: "finance", ParentOpenID: "root", Active: true},
		{OpenID: "finance-2", ParentOpenID: "finance", Active: true},
	}
	users := []stagedUser{{OpenID: "ou-1", Departments: []string{"finance-2"}}}
	edges, err := validateAndBuildClosure(deps, users)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"root/root": 0, "root/finance": 1, "finance/finance": 0, "root/finance-2": 2, "finance/finance-2": 1, "finance-2/finance-2": 0}
	if len(edges) != len(want) {
		t.Fatalf("edges=%d want=%d: %#v", len(edges), len(want), edges)
	}
	for _, e := range edges {
		k := e.AncestorOpenID + "/" + e.DescendantOpenID
		if depth, ok := want[k]; !ok || depth != e.Depth {
			t.Fatalf("unexpected edge %#v", e)
		}
	}
}
func TestValidateAndBuildClosureRejectsCycleAndMissingMembership(t *testing.T) {
	if _, err := validateAndBuildClosure([]stagedDepartment{{OpenID: "a", ParentOpenID: "b"}, {OpenID: "b", ParentOpenID: "a"}}, nil); err == nil {
		t.Fatal("expected cycle error")
	}
	if _, err := validateAndBuildClosure([]stagedDepartment{{OpenID: "a", ParentOpenID: "0"}}, []stagedUser{{OpenID: "u", Departments: []string{"missing"}}}); err == nil {
		t.Fatal("expected missing department error")
	}
}
func TestNextRunAt(t *testing.T) {
	from := mustTime(t, "2026-09-18T01:30:00Z")
	next, err := NextRunAt(SyncConfig{ScheduleType: "interval", IntervalMinutes: 60, Timezone: "Asia/Shanghai"}, from)
	if err != nil || !next.Equal(mustTime(t, "2026-09-18T02:30:00Z")) {
		t.Fatalf("next=%s err=%v", next, err)
	}
	next, err = NextRunAt(SyncConfig{ScheduleType: "daily", DailyTime: "02:00", Timezone: "Asia/Shanghai"}, from)
	if err != nil || !next.Equal(mustTime(t, "2026-09-18T18:00:00Z")) {
		t.Fatalf("daily=%s err=%v", next, err)
	}
}
func mustTime(t *testing.T, v string) time.Time {
	t.Helper()
	x, err := time.Parse(time.RFC3339, v)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func TestValidateSnapshotShrinkRejectsMassReduction(t *testing.T) {
	if err := validateSnapshotShrink(100, 1000, 1100, 20, 900, 1000); err == nil {
		t.Fatal("expected department shrink rejection")
	}
	if err := validateSnapshotShrink(100, 1000, 1100, 90, 600, 1000); err == nil {
		t.Fatal("expected user shrink rejection")
	}
	if err := validateSnapshotShrink(100, 1000, 1100, 90, 900, 600); err == nil {
		t.Fatal("expected membership shrink rejection")
	}
	if err := validateSnapshotShrink(9, 9, 9, 1, 1, 1); err == nil {
		t.Fatal("expected small baseline shrink rejection")
	}
	if err := validateSnapshotShrink(100, 1000, 1100, 80, 800, 900); err != nil {
		t.Fatalf("unexpected rejection: %v", err)
	}
}
