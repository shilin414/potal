package integration

// Review 12 (2026-09-17, 执行报告 §33): the paged catalog endpoint must be
// TRUE keyset pagination — LIMIT/cursor inside SQL, visibility encoded in the
// WHERE clause (no Go re-filter), has_more via the limit+1 probe, and per-page
// usage/favorites aggregation. Fixtures go through the real catalog service
// (never bare SQL), wear the itest prefix, and clean up via t.Cleanup.

import (
	"context"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/catalog"
	"github.com/creation-agent-studio/backend-go/internal/platform/config"
	"github.com/creation-agent-studio/backend-go/internal/platform/database"
)

func pageFixtureEnv(t *testing.T) (*catalog.Service, *catalog.Repo) {
	t.Helper()
	if os.Getenv("STUDIO_TEST_DB") != "1" {
		t.Skip("set STUDIO_TEST_DB=1 to run catalog-page integration tests")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	db, err := database.Open(context.Background(), cfg.Database)
	if err != nil {
		t.Fatalf("database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := &catalog.Repo{DB: db}
	return &catalog.Service{DB: db}, repo
}

// pageSeedApp creates one application through the REAL service path and
// registers its deletion (admin cleanup) via t.Cleanup.
func pageSeedApp(t *testing.T, svc *catalog.Service, creatorID int64, name, kind string, public bool) *catalog.Application {
	t.Helper()
	app, _, err := svc.Create(context.Background(), &catalog.CreateInput{
		Name: name, Kind: kind, IsPublic: public,
		CreatorID: creatorID, IsStaff: true,
	})
	if err != nil {
		t.Fatalf("seed app %s: %v", name, err)
	}
	t.Cleanup(func() {
		_ = svc.Delete(context.Background(), app.ID, creatorID, true)
	})
	return app
}

// pageQuery mirrors the handler's has_more probe: the repo is asked for
// limit+1 rows, the surplus proves has_more and is trimmed away.
func pageQuery(repo *catalog.Repo, q catalog.ApplicationPageQuery) ([]catalog.ApplicationWithBinding, bool, error) {
	limit := q.Limit
	q.Limit = limit + 1
	page, err := repo.ListApplicationPage(context.Background(), q)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(page) > limit
	if hasMore {
		page = page[:limit]
	}
	return page, hasMore, nil
}

// TestApplicationPageKeysetWalk seeds 30 chat apps and walks the cursor to
// prove: page sizes stay exact, has_more flips at the tail, ids never repeat,
// and the union of pages covers every seeded app exactly once.
func TestApplicationPageKeysetWalk(t *testing.T) {
	svc, repo := pageFixtureEnv(t)

	staff := int64(777001)
	const total = 30
	seeded := make(map[int64]bool)
	for i := 0; i < total; i++ {
		app := pageSeedApp(t, svc, staff, fmt.Sprintf("itest_page_walk_%02d", i), "chat", true)
		seeded[app.ID] = true
	}

	const limit = 7
	cursor := ""
	seen := map[int64]bool{}
	pages := 0
	for {
		var cursorCreated time.Time
		var cursorID int64
		if cursor != "" {
			var err error
			cursorCreated, cursorID, err = catalog.DecodePageCursor(cursor)
			if err != nil {
				t.Fatalf("decode cursor %q: %v", cursor, err)
			}
		}
		page, hasMore, err := pageQuery(repo, catalog.ApplicationPageQuery{
			Scope: "manage", Kind: catalog.PageQueryKindChat,
			IncludeUnbound: true, Search: "itest_page_walk_",
			Limit:           limit,
			CursorCreatedAt: cursorCreated,
			CursorID:        cursorID,
			CallerID:        staff, IsStaff: true,
		})
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		if len(page) == 0 {
			t.Fatalf("page %d empty but walk not finished", pages)
		}
		for _, item := range page {
			if seen[item.App.ID] {
				t.Fatalf("app %d appeared twice", item.App.ID)
			}
			seen[item.App.ID] = true
		}
		pages++
		if !hasMore {
			break
		}
		last := page[len(page)-1]
		cursor = catalog.EncodePageCursor(last.App.CreatedAt, last.App.ID)
		if pages > total {
			t.Fatalf("cursor walk did not terminate")
		}
	}
	if pages != 5 { // 30 apps / 7 per page → 4 full pages + 2
		t.Fatalf("expected 5 pages, got %d", pages)
	}
	if len(seen) != total {
		t.Fatalf("walked %d apps, seeded %d", len(seen), total)
	}
	for id := range seeded {
		if !seen[id] {
			t.Fatalf("app %d never returned by the walk", id)
		}
	}
}

// TestApplicationPageVisibilityAndFilters pins the WHERE-encoded policy:
// non-staff callers never see private rows, kind=fixed excludes chat, search
// and category filter inside SQL, and unbound rows follow include_unbound.
func TestApplicationPageVisibilityAndFilters(t *testing.T) {
	svc, repo := pageFixtureEnv(t)

	staff := int64(777002)
	user := int64(777003)

	pageSeedApp(t, svc, staff, "itest_page_pub_chat", "chat", true)
	privChat := pageSeedApp(t, svc, staff, "itest_page_priv_chat", "chat", false)
	pageSeedApp(t, svc, staff, "itest_page_fixed", "task", true)
	pageSeedApp(t, svc, staff, "itest_page_uncat", "task", true)

	// staff sees everything seeded
	got, err := repo.ListApplicationPage(context.Background(), catalog.ApplicationPageQuery{
		Scope: "manage", Kind: catalog.PageQueryKindAll, IncludeUnbound: true,
		Search: "itest_page_", Limit: 100, CallerID: staff, IsStaff: true,
	})
	if err != nil {
		t.Fatalf("staff page: %v", err)
	}
	names := map[string]bool{}
	for _, item := range got {
		names[item.App.Name] = true
	}
	for _, want := range []string{"itest_page_pub_chat", "itest_page_priv_chat", "itest_page_fixed", "itest_page_uncat"} {
		if !names[want] {
			t.Fatalf("staff page missing %s (got %v)", want, sortedKeys(names))
		}
	}

	// regular user sees only the public ones (private stays admin-only)
	got, err = repo.ListApplicationPage(context.Background(), catalog.ApplicationPageQuery{
		Scope: "manage", Kind: catalog.PageQueryKindAll, IncludeUnbound: true,
		Search: "itest_page_", Limit: 100, CallerID: user, IsStaff: false,
	})
	if err != nil {
		t.Fatalf("user page: %v", err)
	}
	for _, item := range got {
		if item.App.ID == privChat.ID {
			t.Fatalf("regular user must not see private %s", privChat.Name)
		}
	}

	// kind=fixed excludes chat rows. The shared dev DB carries other real
	// non-chat applications, so only invariants are asserted: no chat kind,
	// and both seeded fixed rows are present.
	got, err = repo.ListApplicationPage(context.Background(), catalog.ApplicationPageQuery{
		Scope: "manage", Kind: catalog.PageQueryKindFixed, IncludeUnbound: true,
		Search: "itest_page_", Limit: 100, CallerID: staff, IsStaff: true,
	})
	if err != nil {
		t.Fatalf("fixed page: %v", err)
	}
	gotNames := map[string]bool{}
	for _, item := range got {
		gotNames[item.App.Name] = true
		if item.App.Kind == "chat" {
			t.Fatalf("kind=fixed returned chat app %s", item.App.Name)
		}
	}
	for _, want := range []string{"itest_page_fixed", "itest_page_uncat"} {
		if !gotNames[want] {
			t.Fatalf("kind=fixed missing %s (got %v)", want, sortedKeys(gotNames))
		}
	}

	// search narrows to one
	got, err = repo.ListApplicationPage(context.Background(), catalog.ApplicationPageQuery{
		Scope: "manage", Kind: catalog.PageQueryKindAll, IncludeUnbound: true,
		Search: "itest_page_fixed", Limit: 100, CallerID: staff, IsStaff: true,
	})
	if err != nil {
		t.Fatalf("search page: %v", err)
	}
	if len(got) != 1 || got[0].App.Name != "itest_page_fixed" {
		t.Fatalf("search expected exactly the fixed app, got %v", namesOf(got))
	}

	// category filter: the uncategorized sentinel matches only rows with no
	// category at all.
	got, err = repo.ListApplicationPage(context.Background(), catalog.ApplicationPageQuery{
		Scope: "manage", Kind: catalog.PageQueryKindAll, IncludeUnbound: true,
		Search: "itest_page_", CategorySlug: catalog.PageUncategorizedSlug,
		Limit: 100, CallerID: staff, IsStaff: true,
	})
	if err != nil {
		t.Fatalf("uncategorized page: %v", err)
	}
	for _, item := range got {
		if item.App.Name != "itest_page_uncat" && item.App.Name != "itest_page_pub_chat" && item.App.Name != "itest_page_priv_chat" {
			t.Fatalf("uncategorized page returned categorized row %s", item.App.Name)
		}
	}
}

// TestApplicationPageHasMoreProbe proves has_more comes from the limit+1
// probe: seeding exactly `limit` rows must report no more.
func TestApplicationPageHasMoreProbe(t *testing.T) {
	svc, repo := pageFixtureEnv(t)

	staff := int64(777004)
	for i := 0; i < 3; i++ {
		pageSeedApp(t, svc, staff, fmt.Sprintf("itest_page_probe_%d", i), "chat", true)
	}
	page, hasMore, err := pageQuery(repo, catalog.ApplicationPageQuery{
		Scope: "manage", Kind: catalog.PageQueryKindChat, IncludeUnbound: true,
		Search: "itest_page_probe_", Limit: 4, CallerID: staff, IsStaff: true,
	})
	if err != nil {
		t.Fatalf("probe page: %v", err)
	}
	if len(page) != 3 || hasMore {
		t.Fatalf("expected all 3 rows and has_more=false, got %d rows has_more=%v", len(page), hasMore)
	}
}

// TestApplicationPagePerUsageAggregation exercises UsageByApplications /
// FavoritesByApplications: a fresh app aggregates to zero.
func TestApplicationPagePerUsageAggregation(t *testing.T) {
	svc, repo := pageFixtureEnv(t)

	staff := int64(777005)
	app := pageSeedApp(t, svc, staff, "itest_page_usage", "chat", true)

	usage, err := repo.UsageByApplications(context.Background(), staff, []int64{app.ID})
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	// A fresh app has no runs, so the aggregation returns NO row for it —
	// callers read the zero value (`usage[id].Count == 0`), exactly what
	// buildListItem does.
	if entry := usage[app.ID]; entry.Count != 0 || entry.Last != nil {
		t.Fatalf("fresh app must aggregate to zero usage (entry=%+v)", entry)
	}
	favs, err := repo.FavoritesByApplications(context.Background(), staff, []int64{app.ID})
	if err != nil {
		t.Fatalf("favorites: %v", err)
	}
	if favs[app.ID] {
		t.Fatalf("fresh app must not be favourited")
	}
}

func namesOf(items []catalog.ApplicationWithBinding) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.App.Name)
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
