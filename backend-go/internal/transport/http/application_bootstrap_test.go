package http

// Review 12 (2026-09-17, 执行报告 §9–§14, P1-1/P1-2) + 二次复审 (P1-1/P0-5).
//
// The workspace bootstrap replaces the whole-catalog download. Its grouping
// now happens IN SQL (one bounded query per group), so what is left to test
// without a database is the SQL→response glue, which is still PURE:
//
//   · bootstrapGroupIDs  — every selected id, deduplicated, so the single
//     row fetch is exactly as wide as the groups are;
//   · bootstrapUsage     — the personal usage the ranking query already
//     returned for one row, in the shape buildListItem reads;
//   · bootstrapSummaries — group ORDER is the SQL's, never the row fetch's
//     (`IN (...)` has no order), and an unresolvable id is skipped rather
//     than rendered as an empty card;
//   · bootstrapCategoryRail — the empty slug (rows with no category) becomes
//     the `__uncategorized__` sentinel the paged endpoint understands.
//
// The mention router stays pure on purpose and keeps its own tests.

import (
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/catalog"
)

func strPtr(v string) *string { return &v }

func bsRow(id int64) catalog.BootstrapGroupRow {
	return catalog.BootstrapGroupRow{ApplicationID: id}
}

func idsOfRows(rows []catalog.BootstrapGroupRow) []int64 {
	out := make([]int64, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ApplicationID)
	}
	return out
}

func TestBootstrapGroupIDsDeduplicatesAcrossGroups(t *testing.T) {
	// One application legitimately appears in SEVERAL groups (used + recent
	// + favourite), and the row fetch must ask for it once.
	def := bsRow(1)
	groups := catalog.BootstrapGroups{
		Default:         &def,
		Favorites:       []catalog.BootstrapGroupRow{bsRow(1), bsRow(2)},
		Frequent:        []catalog.BootstrapGroupRow{bsRow(1), bsRow(3)},
		Recent:          []catalog.BootstrapGroupRow{bsRow(3)},
		Recommended:     []catalog.BootstrapGroupRow{bsRow(4)},
		RecentFixedApps: []catalog.BootstrapGroupRow{bsRow(5)},
	}
	got := bootstrapGroupIDs(groups)
	want := []int64{1, 2, 3, 4, 5}
	if len(got) != len(want) {
		t.Fatalf("ids must be deduplicated, got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ids must keep first-appearance order, got %v want %v", got, want)
		}
	}
}

func TestBootstrapGroupIDsHandlesAnEmptyWorkspace(t *testing.T) {
	// A fresh install has no default and no groups: the row fetch must be
	// skipped entirely (an `IN ()` is not valid SQL).
	if got := bootstrapGroupIDs(catalog.BootstrapGroups{}); len(got) != 0 {
		t.Fatalf("no groups must mean no row fetch, got %v", got)
	}
}

func TestBootstrapUsageReadsTheRankingQueryResult(t *testing.T) {
	used := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	row := catalog.BootstrapGroupRow{ApplicationID: 7, UsageCount: 4, LastUsedAt: &used}
	groups := catalog.BootstrapGroups{
		Frequent: []catalog.BootstrapGroupRow{row},
		Recent:   []catalog.BootstrapGroupRow{bsRow(9)},
	}

	got := bootstrapUsage(groups, 7)
	entry, ok := got[7]
	if !ok {
		t.Fatalf("usage for the resolved row must be available, got %v", got)
	}
	if entry.Count != 4 {
		t.Fatalf("usage count must come from the ranking query, got %d", entry.Count)
	}
	if entry.Last == nil || *entry.Last != used.UTC().Format(time.RFC3339) {
		t.Fatalf("last_used_at must be the RFC3339 projection buildListItem renders, got %v", entry.Last)
	}

	// A row the caller never used has no timestamp at all — not a zero time
	// rendered as 0001-01-01.
	if got := bootstrapUsage(groups, 9); got[9].Count != 0 || got[9].Last != nil {
		t.Fatalf("never-used must be count 0 + nil, got %+v", got[9])
	}
	// Recommended rows carry no personal usage by definition.
	if got := bootstrapUsage(catalog.BootstrapGroups{
		Recommended: []catalog.BootstrapGroupRow{bsRow(11)},
	}, 11); len(got) != 0 {
		t.Fatalf("推荐 must not invent usage, got %+v", got)
	}
}

func TestBootstrapSummariesKeepSQLOrderAndSkipUnresolvedIDs(t *testing.T) {
	items := map[int64]applicationListItem{
		2: {ID: 2, Name: "second"},
		1: {ID: 1, Name: "first"},
	}
	// The group order is the SQL's; the map iteration order is not.
	group := []catalog.BootstrapGroupRow{bsRow(2), bsRow(1), bsRow(99)}

	got := bootstrapSummaries(group, items)
	if len(got) != 2 {
		t.Fatalf("an id the row fetch could not resolve must be dropped, got %d", len(got))
	}
	if got[0].ID != 2 || got[1].ID != 1 {
		t.Fatalf("group order must survive the row fetch, got %+v", got)
	}
}

func TestBootstrapCategoryRailRendersTheUncategorizedSentinelLast(t *testing.T) {
	rail := bootstrapCategoryRail([]catalog.BootstrapCategory{
		{Slug: "hr", Name: "人力", Count: 2},
		{Slug: "", Name: "", Count: 3},
		{Slug: "it", Name: "", Count: 1},
	})
	if len(rail) != 3 {
		t.Fatalf("rail = 人力 + IT + 其他, got %+v", rail)
	}
	if rail[0].Slug != "hr" || rail[0].Count != 2 {
		t.Fatalf("first-appearance order + counts are wrong: %+v", rail)
	}
	// A category with no name falls back to its slug rather than rendering
	// an empty tab.
	if rail[1].Slug != "it" || rail[1].Name != "it" {
		t.Fatalf("a nameless category must fall back to its slug: %+v", rail[1])
	}
	other := rail[2]
	if other.Slug != catalog.PageUncategorizedSlug || other.Name != "其他" || other.Count != 3 {
		t.Fatalf("其他 sentinel must be last and use the paged endpoint's slug: %+v", other)
	}
}

func TestBootstrapCategoryRailOmitsOtherWhenEverythingIsCategorized(t *testing.T) {
	rail := bootstrapCategoryRail([]catalog.BootstrapCategory{
		{Slug: "oa", Name: "OA", Count: 4},
	})
	if len(rail) != 1 || rail[0].Slug != "oa" {
		t.Fatalf("no uncategorized rows must mean no 其他 tab, got %+v", rail)
	}
}

func TestRankMentionCandidatesIgnoresDescriptionOnlyMatches(t *testing.T) {
	pool := []mentionSource{
		{ID: 1, Slug: "sales", Name: "销售助手"},
		{ID: 2, Slug: "sales-daily", Name: "销售日报"},
		{ID: 3, Slug: "it-ops", Name: "运维小安"}, // description mentions 销售 in SQL only
	}
	got := rankMentionCandidates(pool, "销售")
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 2 {
		t.Fatalf("expected the two name matches in order, got %+v", got)
	}

	// An exact slug beats a name prefix; an exact name beats both.
	got = rankMentionCandidates(pool, "sales")
	if len(got) == 0 || got[0].ID != 1 {
		t.Fatalf("exact slug/name must rank first, got %+v", got)
	}
	got = rankMentionCandidates(pool, "销售日报")
	if len(got) == 0 || got[0].ID != 2 {
		t.Fatalf("exact name must rank first, got %+v", got)
	}
	// Case-insensitivity is the whole point for ASCII names.
	got = rankMentionCandidates([]mentionSource{{ID: 9, Slug: "sales-agent", Name: "Sales Agent"}}, "sales")
	if len(got) != 1 || got[0].ID != 9 {
		t.Fatalf("mention matching must be case-insensitive, got %+v", got)
	}
	if out := rankMentionCandidates(pool, "zzz"); len(out) != 0 {
		t.Fatalf("no match must answer an empty list, got %+v", out)
	}
}

func TestRankMentionCandidatesCapsAndPrependsWithoutDuplicate(t *testing.T) {
	pool := make([]mentionSource, 0, 25)
	for i := 0; i < 25; i++ {
		pool = append(pool, mentionSource{ID: int64(i + 1), Slug: "n", Name: "sales-agent"})
	}
	if got := rankMentionCandidates(pool, "sales"); len(got) != mentionCandidateLimit {
		t.Fatalf("candidates must be capped at %d, got %d", mentionCandidateLimit, len(got))
	}

	list := []mentionCandidate{{ID: 1, Slug: "a", Name: "A", Kind: "chat"}}
	out := prependMention(list, mentionCandidate{ID: 1, Slug: "a", Name: "A", Kind: "chat"})
	if len(out) != 1 {
		t.Fatalf("an exact slug hit already present must not be duplicated: %+v", out)
	}
	out = prependMention(list, mentionCandidate{ID: 2, Slug: "b", Name: "B", Kind: "chat"})
	if len(out) != 2 || out[0].ID != 2 {
		t.Fatalf("the exact slug hit must lead the list: %+v", out)
	}
}

// The CONSUME predicate is what stops a staff caller from opening an
// application the run API would refuse (二次复审 P0-5). It is the shared
// contract behind resolve, @mention, the bootstrap groups and
// AuthorizeExecution, so it is pinned here too.
func TestUsableRequiresEnabledAndABindingForChat(t *testing.T) {
	chat := &catalog.Application{Kind: "chat", Enabled: true}
	fixed := &catalog.Application{Kind: "task", Enabled: true}
	bound := &catalog.Binding{Enabled: true}

	if catalog.Usable(chat, bound) != true {
		t.Fatal("an enabled + bound chat application must be usable")
	}
	if catalog.Usable(chat, nil) != false {
		t.Fatal("a chat application without a binding must NOT be usable")
	}
	if catalog.Usable(chat, &catalog.Binding{Enabled: false}) != false {
		t.Fatal("a DISABLED binding must NOT be usable")
	}
	if !catalog.Usable(fixed, nil) {
		t.Fatal("a fixed application is a build-delivered page: no binding needed")
	}
	// The admin kill switch is absolute, for every kind and every caller.
	disabled := &catalog.Application{Kind: "chat", Enabled: false}
	if catalog.Usable(disabled, bound) != false {
		t.Fatal("a disabled application must never be usable")
	}
	disabledFixed := &catalog.Application{Kind: "task", Enabled: false}
	if catalog.Usable(disabledFixed, nil) != false {
		t.Fatal("a disabled fixed application must never be usable")
	}
	if catalog.Usable(nil, bound) != false {
		t.Fatal("a missing application must never be usable")
	}
}
