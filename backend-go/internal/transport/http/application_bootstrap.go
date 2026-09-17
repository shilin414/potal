package http

import (
	"net/http"
	"sort"
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

const (
	// bootstrapPoolLimit bounds the single pool query that feeds the groups.
	// The groups below are capped at 8 rows each and the category rails are
	// a handful of entries, so this only has to be "larger than any real
	// catalog": beyond it the rails and 推荐 would under-report, which is why
	// the value is deliberately generous.
	bootstrapPoolLimit = 5000

	// bootstrapGroupLimit is the home-shortcut budget per group (§35).
	bootstrapGroupLimit = 8

	// mentionCandidateLimit caps the `@` candidate list (§14). The composer
	// only needs the few applications whose names could match the token the
	// user typed.
	mentionCandidateLimit = 10
)

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
func (s *Server) GetWorkspaceBootstrap(w http.ResponseWriter, r *http.Request) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	ctx := r.Context()

	// scope=manage + include_unbound mirrors what the legacy whole-catalog
	// load used, so nothing that used to appear can disappear: a caller's own
	// private agents and the marketplace's repairable unbound agents stay in
	// the pool (visible() already applies enabled/public for non-staff).
	pool, err := s.CatalogRepo.ListApplicationPage(ctx, catalog.ApplicationPageQuery{
		Scope:          catalog.VisibleScopeManage,
		Kind:           catalog.PageQueryKindAll,
		IncludeUnbound: true,
		Limit:          bootstrapPoolLimit,
		CallerID:       caller.ID,
		IsStaff:        caller.IsStaff,
	})
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}

	usage := s.personalUsage(ctx, caller.ID)
	favorites := s.personalFavorites(ctx, caller.ID)
	providers := s.activeProviders(ctx)

	items := make([]applicationListItem, 0, len(pool))
	for _, entry := range pool {
		items = append(items, s.buildListItem(entry, providers, favorites, usage, caller))
	}

	groups := buildBootstrapGroups(items)
	out := workspaceBootstrap{
		Favorites:       summarizeAll(groups.Favorites),
		Frequent:        summarizeAll(groups.Frequent),
		Recent:          summarizeAll(groups.Recent),
		Recommended:     summarizeAll(groups.Recommended),
		RecentFixedApps: summarizeAll(groups.RecentFixedApps),
		AgentCategories: groups.AgentCategories,
		AppCategories:   groups.AppCategories,
	}
	if groups.DefaultApplication != nil {
		// The composer bound to this application renders its 技能 chips and
		// chooses its attachment entry from the runtime capabilities, so this
		// ONE row carries the two fields the display projection drops (§11).
		def := summaryOf(*groups.DefaultApplication)
		def.Skills = groups.DefaultApplication.Skills
		def.Capabilities = groups.DefaultApplication.Capabilities
		out.DefaultApplication = &def
	}
	writeJSON(w, http.StatusOK, out)
}

// ResolveApplication implements GET /api/v2/applications/resolve (§12).
//
// This is what makes a deep link cost ONE small request: /chat/sales used to
// require the entire catalog plus `Array.find()`. The visibility check is the
// catalog's own (`catalog.VisibleTo`), so a link can never open something the
// market would hide, and an invisible application answers 404 like a
// nonexistent one — existence is not leaked to a probe.
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

	var app *catalog.Application
	var err error
	if slug != "" {
		app, err = s.CatalogRepo.ApplicationBySlug(ctx, slug)
	} else {
		app, err = s.CatalogRepo.ApplicationByID(ctx, id)
	}
	if err != nil {
		writeDetail(w, http.StatusNotFound, "application not found")
		return
	}
	if !catalog.VisibleTo(app, catalog.VisibleScopeManage, caller.ID, caller.IsStaff) {
		writeDetail(w, http.StatusNotFound, "application not found")
		return
	}

	binding, _ := s.CatalogRepo.EnabledBinding(ctx, app.ID)
	usage := s.personalUsage(ctx, caller.ID)
	favorites := s.personalFavorites(ctx, caller.ID)
	providers := s.activeProviders(ctx)
	item := s.buildListItem(
		catalog.ApplicationWithBinding{App: app, Binding: binding},
		providers, favorites, usage, caller)
	writeJSON(w, http.StatusOK, item)
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

	// One narrow search over the same SQL the catalog page uses: `q` is the
	// token the user typed, so anything whose NAME could match is already in
	// this page (name/description/category LIKE, case-insensitively).
	pool, err := s.CatalogRepo.ListApplicationPage(ctx, catalog.ApplicationPageQuery{
		Scope:          catalog.VisibleScopeManage,
		Kind:           catalog.PageQueryKindAll,
		IncludeUnbound: true,
		Search:         q,
		Limit:          mentionCandidateLimit * 2,
		CallerID:       caller.ID,
		IsStaff:        caller.IsStaff,
	})
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}

	out := rankMentionCandidates(s.mentionSources(pool), q)

	// A slug mention (`@sales-agent`) is an EXACT identifier that the
	// substring search above may miss when the display name is unrelated
	// (e.g. slug `sales-agent`, name `销售助手`) — the client-side router
	// accepted it, so the server side must too.
	if app, err := s.CatalogRepo.ApplicationBySlug(ctx, q); err == nil &&
		catalog.VisibleTo(app, catalog.VisibleScopeManage, caller.ID, caller.IsStaff) {
		out = prependMention(out, mentionCandidate{
			ID: app.ID, Slug: app.Slug, Name: app.Name, Kind: app.Kind,
		})
	}
	if out == nil {
		out = []mentionCandidate{}
	}
	writeJSON(w, http.StatusOK, out)
}

// ─────────────────────────────────────────────────────────── derivation ──

// bootstrapGroups is the pure derivation output: everything the bootstrap
// response carries, still in the internal item shape.
type bootstrapGroups struct {
	DefaultApplication *applicationListItem
	Favorites          []applicationListItem
	Frequent           []applicationListItem
	Recent             []applicationListItem
	Recommended        []applicationListItem
	RecentFixedApps    []applicationListItem
	AgentCategories    []applicationCategory
	AppCategories      []applicationCategory
}

// buildBootstrapGroups derives the home/mobile groups from ONE pool, with the
// same rules the client-side helpers used to apply (so no shortcut changes
// behaviour, only where it is computed):
//
//	收藏      chat + starred, most recently used first
//	常用智能体 chat + this caller has run it, most runs first
//	最近使用   chat + this caller ran it, most recent first
//	推荐      chat + this caller has NEVER used it, most used globally first
//	常用应用   非-chat: 最近打开的在前，其余按目录顺序补足 (首页 固定应用 入口)
//	默认智能体  explicit is_default_agent, else the first bound chat app
//
// 停用 (enabled=false) applications are dropped from every GROUP — they are
// consumption entries — while the category rails keep counting what the
// catalog actually holds (staff can see disabled rows, and the rails must
// still describe the market). Pure + DB-free so it is unit tested directly.
func buildBootstrapGroups(pool []applicationListItem) bootstrapGroups {
	var g bootstrapGroups
	chat := make([]applicationListItem, 0, len(pool))
	fixed := make([]applicationListItem, 0, len(pool))
	for _, item := range pool {
		if item.Kind == "chat" {
			chat = append(chat, item)
		} else {
			fixed = append(fixed, item)
		}
	}

	// 默认智能体: the explicit main agent wins; otherwise the first bound chat
	// application in the pool's own (created_at ascending) order — exactly
	// resolveDefaultApplication's fallback, so the composer keeps working on a
	// fresh install with nothing configured.
	for i := range chat {
		if !chat[i].IsBound {
			continue
		}
		if chat[i].IsDefaultAgent {
			app := chat[i]
			g.DefaultApplication = &app
			break
		}
		if g.DefaultApplication == nil {
			app := chat[i]
			g.DefaultApplication = &app
		}
	}

	enabled := func(items []applicationListItem, pick func(applicationListItem) bool) []applicationListItem {
		out := make([]applicationListItem, 0, len(items))
		for _, item := range items {
			if !item.Enabled || !pick(item) {
				continue
			}
			out = append(out, item)
		}
		return out
	}

	g.Favorites = enabled(chat, func(item applicationListItem) bool { return item.IsFavorite })
	sort.SliceStable(g.Favorites, func(i, j int) bool {
		if a, b := usedAt(g.Favorites[i]), usedAt(g.Favorites[j]); a != b {
			return a.After(b)
		}
		return byName(g.Favorites[i], g.Favorites[j])
	})
	g.Favorites = capItems(g.Favorites)

	g.Frequent = enabled(chat, func(item applicationListItem) bool { return item.UsageCount > 0 })
	sort.SliceStable(g.Frequent, func(i, j int) bool {
		if a, b := g.Frequent[i].UsageCount, g.Frequent[j].UsageCount; a != b {
			return a > b
		}
		if a, b := usedAt(g.Frequent[i]), usedAt(g.Frequent[j]); a != b {
			return a.After(b)
		}
		return byName(g.Frequent[i], g.Frequent[j])
	})
	g.Frequent = capItems(g.Frequent)

	g.Recent = enabled(chat, func(item applicationListItem) bool { return !usedAt(item).IsZero() })
	sort.SliceStable(g.Recent, func(i, j int) bool {
		return usedAt(g.Recent[i]).After(usedAt(g.Recent[j]))
	})
	g.Recent = capItems(g.Recent)

	// 推荐 means "not used yet", so it must consider EVERY used application —
	// not only those that survived the per-group caps above.
	g.Recommended = enabled(chat, func(item applicationListItem) bool {
		return item.UsageCount == 0 && usedAt(item).IsZero() && !item.IsFavorite
	})
	sort.SliceStable(g.Recommended, func(i, j int) bool {
		if a, b := g.Recommended[i].GlobalUsageCount, g.Recommended[j].GlobalUsageCount; a != b {
			return a > b
		}
		return byName(g.Recommended[i], g.Recommended[j])
	})
	g.Recommended = capItems(g.Recommended)

	// 常用应用: fixed applications the caller recently opened come first, then
	// the rest in catalog order — the group must still SHOW the fixed apps on
	// a workspace nobody has used yet (the previous client-side list was
	// `non-chat enabled, first 8`, so a "used only" filter would silently
	// empty the 首页 entry on a fresh install).
	g.RecentFixedApps = enabled(fixed, func(applicationListItem) bool { return true })
	sort.SliceStable(g.RecentFixedApps, func(i, j int) bool {
		a, b := usedAt(g.RecentFixedApps[i]), usedAt(g.RecentFixedApps[j])
		if !a.Equal(b) {
			return a.After(b)
		}
		return false // stable: keep the pool's created_at order
	})
	g.RecentFixedApps = capItems(g.RecentFixedApps)

	g.AgentCategories = categoryRails(chat)
	g.AppCategories = categoryRails(fixed)
	return g
}

// mentionSource is the only part of an application the `@` router needs.
type mentionSource struct {
	ID   int64
	Slug string
	Name string
	Kind string
}

// mentionSources projects a catalog page onto the mention identity fields.
func (s *Server) mentionSources(pool []catalog.ApplicationWithBinding) []mentionSource {
	out := make([]mentionSource, 0, len(pool))
	for _, entry := range pool {
		if entry.App == nil {
			continue
		}
		out = append(out, mentionSource{
			ID: entry.App.ID, Slug: entry.App.Slug, Name: entry.App.Name, Kind: entry.App.Kind,
		})
	}
	return out
}

// rankMentionCandidates orders the `@` candidates the way the composer's
// router consumes them: an exact name/slug match first (so `@销售助手` cannot
// be shadowed), then name prefixes (longest first is the router's job — it
// only looks at the few rows returned here), then plain name substrings.
//
// Only NAME and SLUG matches count: the SQL pre-filter also matches the
// description and the category name, and turning those into mention
// candidates would make `@IT` open an unrelated agent that merely mentions IT.
func rankMentionCandidates(pool []mentionSource, q string) []mentionCandidate {
	needle := strings.ToLower(q)
	type ranked struct {
		candidate mentionCandidate
		rank      int
	}
	matches := make([]ranked, 0, len(pool))
	for _, item := range pool {
		name := strings.ToLower(item.Name)
		slug := strings.ToLower(item.Slug)
		rank := -1
		switch {
		case slug == needle || name == needle:
			rank = 0
		case strings.HasPrefix(name, needle):
			rank = 1
		case strings.HasPrefix(slug, needle):
			rank = 2
		case strings.Contains(name, needle):
			rank = 3
		default:
			continue
		}
		matches = append(matches, ranked{
			candidate: mentionCandidate{ID: item.ID, Slug: item.Slug, Name: item.Name, Kind: item.Kind},
			rank:      rank,
		})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].rank != matches[j].rank {
			return matches[i].rank < matches[j].rank
		}
		// Shorter names first: they are the more likely intent behind a
		// partial token.
		if a, b := len([]rune(matches[i].candidate.Name)), len([]rune(matches[j].candidate.Name)); a != b {
			return a < b
		}
		return matches[i].candidate.Name < matches[j].candidate.Name
	})
	if len(matches) > mentionCandidateLimit {
		matches = matches[:mentionCandidateLimit]
	}
	out := make([]mentionCandidate, 0, len(matches))
	for _, m := range matches {
		out = append(out, m.candidate)
	}
	return out
}

// prependMention puts an exact slug hit in front of the ranked list, without
// duplicating it.
func prependMention(list []mentionCandidate, head mentionCandidate) []mentionCandidate {
	out := make([]mentionCandidate, 0, len(list)+1)
	out = append(out, head)
	for _, item := range list {
		if item.ID == head.ID {
			continue
		}
		out = append(out, item)
	}
	if len(out) > mentionCandidateLimit {
		out = out[:mentionCandidateLimit]
	}
	return out
}

func summarizeAll(items []applicationListItem) []applicationSummary {
	out := make([]applicationSummary, 0, len(items))
	for _, item := range items {
		out = append(out, summaryOf(item))
	}
	return out
}

// categoryRails renders the ordered category list for one kind slice:
// first-appearance order (the pool is created_at ascending, the same order
// the market lists applications in), with the 其他 sentinel appended only
// when rows genuinely have no category.
func categoryRails(items []applicationListItem) []applicationCategory {
	out := make([]applicationCategory, 0, 8)
	index := map[string]int{}
	uncategorized := int64(0)
	for _, item := range items {
		slug := strings.TrimSpace(item.CategorySlug)
		if slug == "" {
			uncategorized++
			continue
		}
		if at, ok := index[slug]; ok {
			out[at].Count++
			continue
		}
		name := strings.TrimSpace(item.CategoryName)
		if name == "" {
			name = slug
		}
		index[slug] = len(out)
		out = append(out, applicationCategory{Slug: slug, Name: name, Count: 1})
	}
	// The sentinel is the SAME string the paged endpoint understands
	// (catalog.PageUncategorizedSlug), so a tap on 其他 needs no client-side
	// special case.
	if uncategorized > 0 {
		out = append(out, applicationCategory{
			Slug: catalog.PageUncategorizedSlug, Name: "其他", Count: uncategorized})
	}
	return out
}

func byName(a, b applicationListItem) bool { return a.Name < b.Name }

func capItems(items []applicationListItem) []applicationListItem {
	if len(items) > bootstrapGroupLimit {
		return items[:bootstrapGroupLimit]
	}
	return items
}

// usedAt reads the per-caller last-used timestamp; a missing or unparsable
// value is "never used" (zero time), which is what the group predicates mean.
func usedAt(item applicationListItem) time.Time {
	if item.LastUsedAt == nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, *item.LastUsedAt)
	if err != nil {
		return time.Time{}
	}
	return t
}
