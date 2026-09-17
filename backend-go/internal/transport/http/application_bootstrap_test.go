package http

// Review 12 (2026-09-17, 执行报告 §9–§14, P1-1/P1-2): the workspace bootstrap
// replaces the whole-catalog download. Its derivation is PURE, so these tests
// need no database — which is exactly why it was written as a pure function
// rather than as SQL glued into the handler.
//
// What they pin (each one is a rule the client-side code these functions
// replace already had, so a regression here is a silent UX regression):
//   · 收藏/常用/最近使用/推荐/常用应用 are derived by kind, never mixed;
//   · 停用 applications never enter a SHORTCUT group, but the category rails
//     still count them (staff see the real market);
//   · the default agent prefers an explicit `is_default_agent` AND requires a
//     binding, falling back to the first bound chat app;
//   · 推荐 means "this caller has never used it" (usage, recency, favourite);
//   · the 其他 sentinel appears only when uncategorized rows exist, and uses
//     the same slug the paged endpoint understands;
//   · `@` candidates match name/slug only (never the description the SQL
//     pre-filter also matched), ranked exact → prefix → substring.

import (
	"testing"
)

func bsItem(id int64, name, kind string, tweak func(*applicationListItem)) applicationListItem {
	item := applicationListItem{ID: id, Slug: name, Name: name, Kind: kind, Enabled: true}
	item.Slug = "slug-" + name
	if tweak != nil {
		tweak(&item)
	}
	return item
}

func idsOf(items []applicationListItem) []int64 {
	out := make([]int64, 0, len(items))
	for _, item := range items {
		out = append(out, item.ID)
	}
	return out
}

func sameIDs(got []applicationListItem, want ...int64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i].ID != want[i] {
			return false
		}
	}
	return true
}

func TestBootstrapGroupsSplitKindsAndRankPerGroup(t *testing.T) {
	pool := []applicationListItem{
		// 1: chat, used 3 times, most recently — belongs to 常用/最近使用
		bsItem(1, "alpha", "chat", func(i *applicationListItem) {
			i.UsageCount = 3
			i.LastUsedAt = strPtr("2026-09-17T10:00:00Z")
		}),
		// 2: chat, used 9 times but older — 常用 first, 最近使用 second
		bsItem(2, "beta", "chat", func(i *applicationListItem) {
			i.UsageCount = 9
			i.LastUsedAt = strPtr("2026-09-16T10:00:00Z")
		}),
		// 3: chat, starred and never used — 收藏 only
		bsItem(3, "gamma", "chat", func(i *applicationListItem) { i.IsFavorite = true }),
		// 4: chat, never used, high global usage — 推荐
		bsItem(4, "delta", "chat", func(i *applicationListItem) { i.GlobalUsageCount = 100 }),
		// 5: chat, never used, low global usage — 推荐 second
		bsItem(5, "epsilon", "chat", func(i *applicationListItem) { i.GlobalUsageCount = 1 }),
		// 6: fixed app this caller ran — 常用应用 first
		bsItem(6, "oa", "task", func(i *applicationListItem) {
			i.LastUsedAt = strPtr("2026-09-17T09:00:00Z")
		}),
		// 7: fixed app never used — still offered (catalog order, after 6)
		bsItem(7, "report", "task", nil),
	}
	g := buildBootstrapGroups(pool)

	if !sameIDs(g.Frequent, 2, 1) {
		t.Fatalf("常用 must rank by run count desc, got %v", idsOf(g.Frequent))
	}
	if !sameIDs(g.Recent, 1, 2) {
		t.Fatalf("最近使用 must rank by recency desc, got %v", idsOf(g.Recent))
	}
	if !sameIDs(g.Favorites, 3) {
		t.Fatalf("收藏 must be the starred chat apps, got %v", idsOf(g.Favorites))
	}
	if !sameIDs(g.Recommended, 4, 5) {
		t.Fatalf("推荐 must exclude every used/starred app and rank by global usage, got %v", idsOf(g.Recommended))
	}
	if !sameIDs(g.RecentFixedApps, 6, 7) {
		t.Fatalf("常用应用 must hold non-chat apps, recently used first, got %v", idsOf(g.RecentFixedApps))
	}
	for _, item := range g.Favorites {
		if item.Kind != "chat" {
			t.Fatalf("收藏 leaked a non-chat application: %s", item.Kind)
		}
	}
}

func TestBootstrapGroupsDefaultAgentNeedsExplicitFlagAndBinding(t *testing.T) {
	pool := []applicationListItem{
		bsItem(1, "first-bound", "chat", func(i *applicationListItem) { i.IsBound = true }),
		bsItem(2, "explicit-default", "chat", func(i *applicationListItem) {
			i.IsBound = true
			i.IsDefaultAgent = true
		}),
		bsItem(3, "unbound-default", "chat", func(i *applicationListItem) { i.IsDefaultAgent = true }),
	}
	g := buildBootstrapGroups(pool)
	if g.DefaultApplication == nil || g.DefaultApplication.ID != 2 {
		t.Fatalf("the explicit default agent must win, got %+v", g.DefaultApplication)
	}

	// Nothing marked: the FIRST bound chat app is the fallback (the
	// resolveDefaultApplication rule) — the unbound row is skipped even when
	// it is flagged, because a binding-less agent cannot be sent to.
	g = buildBootstrapGroups(pool[:1])
	if g.DefaultApplication == nil || g.DefaultApplication.ID != 1 {
		t.Fatalf("fallback must be the first bound chat app, got %+v", g.DefaultApplication)
	}
	g = buildBootstrapGroups([]applicationListItem{pool[2]})
	if g.DefaultApplication != nil {
		t.Fatalf("an unbound agent must not become the default, got %+v", g.DefaultApplication)
	}
}

func TestBootstrapGroupsDropDisabledFromGroupsButKeepRails(t *testing.T) {
	pool := []applicationListItem{
		bsItem(1, "live", "chat", func(i *applicationListItem) {
			i.CategorySlug = "it"
			i.CategoryName = "IT运维"
		}),
		bsItem(2, "disabled", "chat", func(i *applicationListItem) {
			i.Enabled = false
			i.CategorySlug = "it"
			i.CategoryName = "IT运维"
			i.UsageCount = 50
			i.LastUsedAt = strPtr("2026-09-17T10:00:00Z")
		}),
	}
	g := buildBootstrapGroups(pool)
	for _, group := range [][]applicationListItem{g.Frequent, g.Recent} {
		for _, item := range group {
			if !item.Enabled {
				t.Fatalf("a 停用 application must never enter a shortcut group: %v", idsOf(group))
			}
		}
	}
	if len(g.AgentCategories) != 1 || g.AgentCategories[0].Count != 2 {
		t.Fatalf("the category rail must still count disabled rows, got %+v", g.AgentCategories)
	}
}

func TestBootstrapCategoryRailsOnlyAddOtherWhenNeeded(t *testing.T) {
	g := buildBootstrapGroups([]applicationListItem{
		bsItem(1, "a", "chat", func(i *applicationListItem) {
			i.CategorySlug = "hr"
			i.CategoryName = "人力"
		}),
		bsItem(2, "b", "chat", func(i *applicationListItem) {
			i.CategorySlug = "hr"
			i.CategoryName = "人力"
		}),
		bsItem(3, "c", "chat", nil), // no category
		bsItem(4, "d", "task", func(i *applicationListItem) {
			i.CategorySlug = "oa"
			i.CategoryName = "OA"
		}),
	})
	if len(g.AgentCategories) != 2 {
		t.Fatalf("agent rail = 人力 + 其他, got %+v", g.AgentCategories)
	}
	if g.AgentCategories[0].Slug != "hr" || g.AgentCategories[0].Count != 2 {
		t.Fatalf("first-appearance order + counts are wrong: %+v", g.AgentCategories)
	}
	other := g.AgentCategories[1]
	if other.Slug != "__uncategorized__" || other.Name != "其他" || other.Count != 1 {
		t.Fatalf("其他 sentinel is wrong: %+v", other)
	}
	// The fixed rail has no uncategorized row, so it must NOT offer 其他.
	if len(g.AppCategories) != 1 || g.AppCategories[0].Slug != "oa" {
		t.Fatalf("app rail must be OA only, got %+v", g.AppCategories)
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

func strPtr(v string) *string { return &v }
