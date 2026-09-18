package http

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	genapi "github.com/creation-agent-studio/backend-go/internal/gen/api"

	"github.com/creation-agent-studio/backend-go/internal/catalog"
)

// ───────────────────────────────── workspace bootstrap (执行报告 §9–§11, P1-1) ──
//
// The shell used to answer every one of these questions by downloading the
// WHOLE catalog (`GET /api/v2/applications`, once 1800+ rows) and projecting
// it in the browser: the default main agent, the four home shortcut groups,
// the category rails, and the `@mention` candidate array. This endpoint
// answers them once, in a payload whose size does not depend on the catalog
// size — the fix for "the shell still loads everything even though the
// pages are paginated".
//
// Cost model: ONE keyset query over the visible pool (the same anti-joined
// SQL the paged catalog uses, with a large limit — the legacy
// ListApplicationsWithBindings this replaces issued one ApplicationByID
// query PER ROW), plus the caller's own usage and favourites, which are
// bounded by what that caller actually did.

// mentionCandidateLimit caps the `@` candidate list (§14). The composer
// only needs the few applications whose names could match the token the
// user typed.
//
// note: the home-shortcut budget per group lives in
// catalog.BootstrapGroupLimit — it is the SQL LIMIT of every bootstrap
// group query, so it has exactly one definition.
const mentionCandidateLimit = 10

type applicationCategory struct {
	Slug  string `json:"slug"`
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

type workspaceBootstrap struct {
	DefaultApplication *applicationSummary   `json:"default_application"`
	Favorites          []applicationSummary  `json:"favorites"`
	Frequent           []applicationSummary  `json:"frequent"`
	Recent             []applicationSummary  `json:"recent"`
	Recommended        []applicationSummary  `json:"recommended"`
	RecentFixedApps    []applicationSummary  `json:"recent_fixed_apps"`
	AgentCategories    []applicationCategory `json:"agent_categories"`
	AppCategories      []applicationCategory `json:"app_categories"`
}

type mentionCandidate struct {
	ID   int64  `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// GetWorkspaceBootstrap implements GET /api/v2/workspace/bootstrap.
//
// Cost model (二次复审 P1-1): a FIXED number of queries, each capped at
// `catalog.BootstrapGroupLimit` rows, plus ONE row fetch over the ≤ ~40 ids
// those queries selected. Nothing here scales with the catalog:
//
//	DB rows examined   bounded by the indexes
//	Go Application objects  ≤ ~40
//	HTTP response      ~30 rows
//
// The previous implementation ran one `ListApplicationPage{Limit: 5000}` and
// derived the groups in Go. That only made the RESPONSE constant-size; rows
// examined, Go memory and CPU still grew with the catalog, and past row 5000
// the answer was silently WRONG — a category that only appears later, the
// top-`usage_count` 推荐 agent, or the only valid default fallback would
// simply vanish.
//
// Every group is a CONSUMPTION list (P0-5): `enabled = 1` and, for
// `kind = chat`, an enabled runtime binding are enforced in SQL, so no
// shortcut can offer an application the run API would refuse.
func (s *Server) GetWorkspaceBootstrap(w http.ResponseWriter, r *http.Request) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	ctx := r.Context()
	if s.Metric != nil {
		s.Metric.WorkspaceBootstrapRequestsTotal.Inc()
	}

	groups, err := s.CatalogRepo.BootstrapGroups(ctx, catalog.BootstrapGroupQuery{
		CallerID: caller.ID, IsStaff: caller.IsStaff,
	})
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	categories, err := s.CatalogRepo.BootstrapCategories(ctx, caller.IsStaff)
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// ONE row fetch for every id the groups selected. The order lives in the
	// group slices, not in the `IN (...)`, so rows are indexed by id and
	// re-emitted in group order below.
	ids := bootstrapGroupIDs(groups)
	rows, err := s.CatalogRepo.ListApplicationRowsByIDs(ctx, ids, caller.IsStaff)
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Favourites are the ONE personal fact the ranking queries do not carry
	// for every group (推荐 excludes them by definition, but 常用 / 最近 /
	// 常用应用 must still show the ✩), so it is one bounded `IN (...)`.
	favorites, err := s.CatalogRepo.FavoritesByApplications(ctx, caller.ID, ids)
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	providers, err := s.activeProviderIndex(ctx)
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}

	items := make(map[int64]applicationListItem, len(rows))
	for _, row := range rows {
		items[row.App.ID] = s.buildListItem(row, providers, favorites,
			bootstrapUsage(groups, row.App.ID), caller)
	}

	out := workspaceBootstrap{
		Favorites:       bootstrapSummaries(groups.Favorites, items),
		Frequent:        bootstrapSummaries(groups.Frequent, items),
		Recent:          bootstrapSummaries(groups.Recent, items),
		Recommended:     bootstrapSummaries(groups.Recommended, items),
		RecentFixedApps: bootstrapSummaries(groups.RecentFixedApps, items),
		AgentCategories: bootstrapCategoryRail(categories.Agents),
		AppCategories:   bootstrapCategoryRail(categories.Apps),
	}
	if groups.Default != nil {
		if item, ok := items[groups.Default.ApplicationID]; ok {
			// The composer bound to this application renders its 技能 chips
			// and chooses its attachment entry from the runtime capabilities,
			// so this ONE row carries the two fields the display projection
			// drops (§11).
			def := summaryOf(item)
			def.Skills = item.Skills
			def.Capabilities = item.Capabilities
			out.DefaultApplication = &def
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// bootstrapGroupIDs collects every id the groups selected, deduplicated — the
// input of the single row fetch.
func bootstrapGroupIDs(groups catalog.BootstrapGroups) []int64 {
	seen := map[int64]bool{}
	out := make([]int64, 0, catalog.BootstrapGroupLimit*6)
	appendRow := func(row *catalog.BootstrapGroupRow) {
		if row == nil || seen[row.ApplicationID] {
			return
		}
		seen[row.ApplicationID] = true
		out = append(out, row.ApplicationID)
	}
	appendRow(groups.Default)
	for i := range groups.Favorites {
		appendRow(&groups.Favorites[i])
	}
	for i := range groups.Frequent {
		appendRow(&groups.Frequent[i])
	}
	for i := range groups.Recent {
		appendRow(&groups.Recent[i])
	}
	for i := range groups.Recommended {
		appendRow(&groups.Recommended[i])
	}
	for i := range groups.RecentFixedApps {
		appendRow(&groups.RecentFixedApps[i])
	}
	return out
}

// bootstrapUsage rebuilds the per-application usage map `buildListItem`
// expects, from the personal usage the ranking query already returned for
// that row.
func bootstrapUsage(groups catalog.BootstrapGroups, id int64) map[int64]pageUsage {
	entry := func(row *catalog.BootstrapGroupRow) (pageUsage, bool) {
		if row == nil {
			return pageUsage{}, false
		}
		out := pageUsage{Count: row.UsageCount}
		if row.LastUsedAt != nil {
			t := row.LastUsedAt.UTC().Format(time.RFC3339)
			out.Last = &t
		}
		return out, true
	}
	if groups.Default != nil && groups.Default.ApplicationID == id {
		if u, ok := entry(groups.Default); ok {
			return map[int64]pageUsage{id: u}
		}
	}
	for i := range groups.Favorites {
		if groups.Favorites[i].ApplicationID != id {
			continue
		}
		if u, ok := entry(&groups.Favorites[i]); ok {
			return map[int64]pageUsage{id: u}
		}
	}
	for i := range groups.Frequent {
		if groups.Frequent[i].ApplicationID != id {
			continue
		}
		if u, ok := entry(&groups.Frequent[i]); ok {
			return map[int64]pageUsage{id: u}
		}
	}
	for i := range groups.Recent {
		if groups.Recent[i].ApplicationID != id {
			continue
		}
		if u, ok := entry(&groups.Recent[i]); ok {
			return map[int64]pageUsage{id: u}
		}
	}
	for i := range groups.RecentFixedApps {
		if groups.RecentFixedApps[i].ApplicationID != id {
			continue
		}
		if u, ok := entry(&groups.RecentFixedApps[i]); ok {
			return map[int64]pageUsage{id: u}
		}
	}
	return nil
}

// bootstrapSummaries emits one group in the order the SQL returned, skipping
// ids the row fetch could not resolve (a row deleted between the two
// queries, or one whose visibility changed).
func bootstrapSummaries(
	group []catalog.BootstrapGroupRow,
	items map[int64]applicationListItem,
) []applicationSummary {
	out := make([]applicationSummary, 0, len(group))
	for _, row := range group {
		item, ok := items[row.ApplicationID]
		if !ok {
			continue
		}
		out = append(out, summaryOf(item))
	}
	return out
}

// bootstrapCategoryRail renders one category rail, turning the SQL's empty
// slug (rows with no category at all) into the `__uncategorized__` sentinel
// the paged endpoint understands — so a tap on 其他 needs no client-side
// special case.
func bootstrapCategoryRail(rows []catalog.BootstrapCategory) []applicationCategory {
	out := make([]applicationCategory, 0, len(rows)+1)
	for _, row := range rows {
		slug := strings.TrimSpace(row.Slug)
		if slug == "" {
			// Deferred: appended once at the end so the rail keeps the
			// first-appearance order of the real categories.
			continue
		}
		name := strings.TrimSpace(row.Name)
		if name == "" {
			name = slug
		}
		out = append(out, applicationCategory{Slug: slug, Name: name, Count: row.Count})
	}
	for _, row := range rows {
		if strings.TrimSpace(row.Slug) != "" {
			continue
		}
		out = append(out, applicationCategory{
			Slug: catalog.PageUncategorizedSlug, Name: "其他", Count: row.Count})
	}
	return out
}

// ResolveApplication implements GET /api/v2/applications/resolve (§12).
//
// This is what makes a deep link cost ONE small request: /chat/sales used to
// require the entire catalog plus `Array.find()`. The visibility check is the
// catalog's own (`catalog.VisibleTo`), so a link can never open something the
// market would hide, and an invisible application answers 404 like a
// nonexistent one — existence is not leaked to a probe.
//
// Resolution is a CONSUMPTION entry point (二次复审 P0-5, 三次复审 P0-R3):
// the facts come from ONE joined read (`ConsumptionBundleByID`) and go
// through `catalog.Consumable`, which since P0-R3 also requires the binding's
// PROVIDER to exist and be active — the same predicate AuthorizeExecution
// enforces. Without it a staff caller could open /chat/whatever and only
// discover at send time that the run is refused because an admin had
// switched the provider off.
//
// Error mapping (三次复审 P1-R1): ErrNotFound → 404; ANY other failure
// (DB timeout, connection reset) → 500. Collapsing both into 404 made a
// backend outage tell the frontend "应用不存在" and destroyed deep links.
func (s *Server) ResolveApplication(w http.ResponseWriter, r *http.Request, params genapi.ResolveApplicationParams) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	ctx := r.Context()

	slug := ""
	if params.Slug != nil {
		slug = strings.TrimSpace(*params.Slug)
	}
	id := int64(0)
	if params.Id != nil {
		id = int64(*params.Id)
	}
	if slug == "" && id <= 0 {
		writeDetail(w, http.StatusBadRequest, "either slug or id is required")
		return
	}

	var bundle *catalog.ConsumptionBundle
	var err error
	if slug != "" {
		bundle, err = s.resolveBundleBySlug(ctx, slug)
	} else {
		bundle, err = s.CatalogRepo.ConsumptionBundleByID(ctx, id)
	}
	if err != nil {
		if errors.Is(err, catalog.ErrNotFound) {
			writeDetail(w, http.StatusNotFound, "application not found")
			return
		}
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	app := bundle.Application
	if !catalog.VisibleTo(app, catalog.VisibleScopeManage, caller.ID, caller.IsStaff) {
		writeDetail(w, http.StatusNotFound, "application not found")
		return
	}
	if !catalog.Consumable(catalog.ConsumptionFacts{
		Application:    app,
		Binding:        bundle.Binding,
		ProviderStatus: bundle.ProviderStatus,
	}) {
		writeDetail(w, http.StatusNotFound, "application not found")
		return
	}

	// ONE application ⇒ ONE usage row (二次复审 P1-2). `personalUsage()`
	// re-aggregated the caller's ENTIRE run history on every deep link —
	// the very cost the paged endpoint was refactored to avoid. Both
	// queries are index-covered by idx_runs_user_application_created
	// (migration 0025).
	usage, err := s.CatalogRepo.UsageByApplications(ctx, caller.ID, []int64{app.ID})
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	favorites, err := s.CatalogRepo.FavoritesByApplications(ctx, caller.ID, []int64{app.ID})
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	item := s.buildListItem(
		catalog.ApplicationWithBinding{App: app, Binding: bundle.Binding},
		providerIndexOf(bundle.Provider), favorites, usageMapOf(usage), caller)
	writeJSON(w, http.StatusOK, item)
}

// resolveBundleBySlug resolves the consumption bundle by slug. The bundle
// query keys on id, so the slug is translated first — same ErrNotFound /
// DB-error split as the id path (三次复审 P1-R1).
func (s *Server) resolveBundleBySlug(ctx context.Context, slug string) (*catalog.ConsumptionBundle, error) {
	app, err := s.CatalogRepo.ApplicationBySlug(ctx, slug)
	if err != nil {
		return nil, err
	}
	return s.CatalogRepo.ConsumptionBundleByID(ctx, app.ID)
}

// ResolveApplicationMention implements GET /api/v2/applications/resolve-mention
// (§14): the server side of `@销售助手 帮我查一下`.
func (s *Server) ResolveApplicationMention(w http.ResponseWriter, r *http.Request, params genapi.ResolveApplicationMentionParams) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	ctx := r.Context()
	q := strings.TrimSpace(params.Q)
	if q == "" {
		writeDetail(w, http.StatusBadRequest, "q is required")
		return
	}

	// Dedicated mention SQL (四次复审 P1-R2): rank name/slug matches IN SQL,
	// THEN LIMIT. Reusing ListApplicationPage let description/category noise
	// consume the whole pre-filter budget before the real candidate was seen.
	// The query itself applies the same visibility + consumption predicates
	// as the consume page, so a disabled / unbound / inactive-provider agent
	// can never be offered here.
	candidates, err := s.CatalogRepo.ResolveMentionCandidates(
		ctx, q, caller.IsStaff, mentionCandidateLimit,
	)
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}

	out := make([]mentionCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, mentionCandidate{
			ID: candidate.ID, Slug: candidate.Slug,
			Name: candidate.Name, Kind: candidate.Kind,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
