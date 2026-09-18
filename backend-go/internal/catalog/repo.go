package catalog

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/dbtypes"
)

// ErrNotFound marks missing catalog rows.
var ErrNotFound = errors.New("catalog: not found")

// Repo is the sqlc-backed catalog repository.
type Repo struct {
	DB *sql.DB
}

func (r *Repo) q(ctx context.Context) db.Querier { return db.New(r.DB) }
func (r *Repo) qtx(tx *sql.Tx) db.Querier        { return db.New(tx) }

func (r *Repo) ProviderByKey(ctx context.Context, key string) (*Provider, error) {
	row, err := r.q(ctx).GetProviderByKey(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p := &Provider{
		ID:          int64(row.ID),
		Key:         row.ProviderKey,
		Name:        row.Name,
		Status:      row.Status,
		MaxInflight: int(row.MaxInflight),
	}
	_ = json.Unmarshal(row.SupportedRuntimeTypes, &p.SupportedRuntimeTypes)
	_ = json.Unmarshal(row.Capabilities, &p.Capabilities)
	if p.Capabilities == nil {
		p.Capabilities = map[string]any{}
	}
	return p, nil
}

func (r *Repo) ListActiveProviders(ctx context.Context) ([]Provider, error) {
	rows, err := r.q(ctx).ListActiveProviders(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Provider, 0, len(rows))
	for _, row := range rows {
		p := Provider{ID: int64(row.ID), Key: row.ProviderKey, Name: row.Name, Status: row.Status, MaxInflight: int(row.MaxInflight)}
		_ = json.Unmarshal(row.SupportedRuntimeTypes, &p.SupportedRuntimeTypes)
		_ = json.Unmarshal(row.Capabilities, &p.Capabilities)
		if p.Capabilities == nil {
			p.Capabilities = map[string]any{}
		}
		out = append(out, p)
	}
	return out, nil
}

func appFromDetail(row db.GetApplicationByIDRow) *Application {
	a := &Application{
		ID:             int64(row.ID),
		Slug:           row.Slug,
		Name:           row.Name,
		Description:    row.Description,
		Icon:           row.Icon,
		AvatarKey:      row.AvatarKey,
		Color:          row.Color,
		Kind:           row.Kind,
		RendererKey:    row.RendererKey,
		ExecutorKey:    row.ExecutorKey,
		CategorySlug:   row.CategorySlug.String,
		CategoryName:   row.CategoryName.String,
		IsPublic:       row.IsPublic,
		Enabled:        row.Enabled,
		IsDefaultAgent: row.IsDefaultAgent,
		UsageCount:     int64(row.UsageCount),
		// 技能配置 lives inside default_config; project it once here so every
		// caller (mobile sheet, market form) reads the same parsed shape.
		Skills:    ParseSkills(row.DefaultConfig),
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
	if row.CategoryID.Valid {
		v := int64(row.CategoryID.Int64)
		a.CategoryID = &v
	}
	if row.CreatedBy.Valid {
		v := int64(row.CreatedBy.Int64)
		a.CreatedBy = &v
	}
	return a
}

func (r *Repo) ApplicationByID(ctx context.Context, id int64) (*Application, error) {
	row, err := r.q(ctx).GetApplicationByID(ctx, uint64(id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return appFromDetail(row), nil
}

func (r *Repo) ApplicationBySlug(ctx context.Context, slug string) (*Application, error) {
	row, err := r.q(ctx).GetApplicationBySlug(ctx, slug)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a := appFromDetail(db.GetApplicationByIDRow(row))
	return a, nil
}

// ConsumptionBundle is the single-object read `Consumable` decides on: the
// application, its current enabled binding (nil when the application has
// none) and that binding's provider status (nil when the provider row is
// missing — the same fail-closed shape AuthorizeExecution reads).
type ConsumptionBundle struct {
	Application    *Application
	Binding        *Binding
	ProviderStatus *string
}

// ConsumptionBundleByID loads one application's consumption facts with ONE
// joined query (三次复审 P0-R3) — application + current enabled binding
// (newest wins, mirroring GetEnabledBinding) + provider — instead of the
// ApplicationByID → EnabledBinding → ProviderByKey serial walk. The join
// keeps `resolve` / `@mention` on the same provider fact AuthorizeExecution
// reads, and ErrNotFound maps exactly like every other single-row read.
func (r *Repo) ConsumptionBundleByID(ctx context.Context, id int64) (*ConsumptionBundle, error) {
	row, err := r.q(ctx).GetConsumptionBundle(ctx, uint64(id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	app := &Application{
		ID:             int64(row.ID),
		Slug:           row.Slug,
		Name:           row.Name,
		Description:    row.Description,
		Icon:           row.Icon,
		AvatarKey:      row.AvatarKey,
		Color:          row.Color,
		Kind:           row.Kind,
		RendererKey:    row.RendererKey,
		ExecutorKey:    row.ExecutorKey,
		CategorySlug:   row.CategorySlug.String,
		CategoryName:   row.CategoryName.String,
		IsPublic:       row.IsPublic,
		Enabled:        row.Enabled,
		IsDefaultAgent: row.IsDefaultAgent,
		UsageCount:     int64(row.UsageCount),
		Skills:         ParseSkills(row.DefaultConfig),
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
	if row.CategoryID.Valid {
		v := int64(row.CategoryID.Int64)
		app.CategoryID = &v
	}
	if row.CreatedBy.Valid {
		v := int64(row.CreatedBy.Int64)
		app.CreatedBy = &v
	}
	out := &ConsumptionBundle{Application: app}
	if !row.BindingID.Valid {
		return out, nil
	}
	b := &Binding{
		ID:                 row.BindingID.Int64,
		ApplicationID:      app.ID,
		ProviderKey:        row.BindingProviderKey.String,
		RuntimeType:        row.BindingRuntimeType.String,
		ExternalResourceID: row.BindingExternalResourceID.String,
		IdentityMode:       row.BindingIdentityMode.String,
		ExecutionMode:      row.BindingExecutionMode.String,
		SessionPolicy:      row.BindingSessionPolicy.String,
		ArtifactPolicy:     row.BindingArtifactPolicy.String,
		TimeoutSeconds:     int64(row.BindingTimeoutSeconds.Int32),
		Enabled:            row.BindingEnabled.Bool,
	}
	if row.BindingProviderID.Valid {
		v := int64(row.BindingProviderID.Int64)
		b.ProviderID = &v
	}
	_ = json.Unmarshal(row.BindingCapabilities, &b.Capabilities)
	_ = json.Unmarshal(row.BindingConfig, &b.Config)
	if b.Capabilities == nil {
		b.Capabilities = map[string]any{}
	}
	if b.Config == nil {
		b.Config = map[string]any{}
	}
	out.Binding = b
	if row.ProviderStatus.Valid {
		v := row.ProviderStatus.String
		out.ProviderStatus = &v
	}
	return out, nil
}

func (r *Repo) DefaultAgent(ctx context.Context) (*Application, error) {
	row, err := r.q(ctx).GetDefaultAgent(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return appFromDetail(db.GetApplicationByIDRow(row)), nil
}

func bindingFromRow(row db.RuntimeBinding) *Binding {
	b := &Binding{
		ID:                 int64(row.ID),
		ApplicationID:      int64(row.ApplicationID),
		ProviderKey:        row.ProviderKey,
		RuntimeType:        row.RuntimeType,
		ExternalResourceID: row.ExternalResourceID,
		IdentityMode:       row.IdentityMode,
		ExecutionMode:      row.ExecutionMode,
		SessionPolicy:      row.SessionPolicy,
		ArtifactPolicy:     row.ArtifactPolicy,
		TimeoutSeconds:     int64(row.TimeoutSeconds),
		Enabled:            row.Enabled,
	}
	if row.ProviderID.Valid {
		v := int64(row.ProviderID.Int64)
		b.ProviderID = &v
	}
	_ = json.Unmarshal(row.Capabilities, &b.Capabilities)
	_ = json.Unmarshal(row.Config, &b.Config)
	return b
}

func (r *Repo) BindingsByApplication(ctx context.Context, appID int64) ([]*Binding, error) {
	rows, err := r.q(ctx).ListBindingsByApplication(ctx, uint64(appID))
	if err != nil {
		return nil, err
	}
	out := make([]*Binding, 0, len(rows))
	for _, row := range rows {
		out = append(out, bindingFromRow(row))
	}
	return out, nil
}

func (r *Repo) EnabledBinding(ctx context.Context, appID int64) (*Binding, error) {
	row, err := r.q(ctx).GetEnabledBinding(ctx, uint64(appID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return bindingFromRow(row), nil
}

func (r *Repo) BindingByID(ctx context.Context, id int64) (*Binding, error) {
	row, err := r.q(ctx).GetBindingByID(ctx, uint64(id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return bindingFromRow(row), nil
}

// UpsertBindingTx inserts or updates the (application, runtime_type,
// provider_key) binding inside a transaction, always re-enabling it.
func UpsertBindingTx(ctx context.Context, tx *sql.Tx, b *Binding, caps map[string]any) (int64, error) {
	q := db.New(tx)
	capsJSON := dbtypes.JSONText(mustJSON(caps))
	configJSON := dbtypes.JSONText(mustJSON(b.Config))
	row, findErr := q.FindBinding(ctx, db.FindBindingParams{
		ApplicationID: uint64(b.ApplicationID),
		RuntimeType:   b.RuntimeType,
		ProviderKey:   b.ProviderKey,
	})
	switch {
	case errors.Is(findErr, sql.ErrNoRows):
		res, err := q.CreateBinding(ctx, db.CreateBindingParams{
			ApplicationID:      uint64(b.ApplicationID),
			ProviderID:         nullInt64(b.ProviderID),
			ProviderKey:        b.ProviderKey,
			RuntimeType:        b.RuntimeType,
			ExternalResourceID: b.ExternalResourceID,
			IdentityMode:       b.IdentityMode,
			ExecutionMode:      b.ExecutionMode,
			SessionPolicy:      b.SessionPolicy,
			ArtifactPolicy:     b.ArtifactPolicy,
			Capabilities:       capsJSON,
			Config:             configJSON,
			TimeoutSeconds:     uint32(b.TimeoutSeconds),
		})
		if err != nil {
			return 0, err
		}
		id, err := res.LastInsertId()
		return id, err
	case findErr != nil:
		return 0, findErr
	default:
		if _, err := q.UpdateBinding(ctx, db.UpdateBindingParams{
			ProviderID:         nullInt64(b.ProviderID),
			ExternalResourceID: b.ExternalResourceID,
			IdentityMode:       b.IdentityMode,
			ExecutionMode:      b.ExecutionMode,
			SessionPolicy:      b.SessionPolicy,
			ArtifactPolicy:     b.ArtifactPolicy,
			Capabilities:       capsJSON,
			Config:             configJSON,
			TimeoutSeconds:     uint32(b.TimeoutSeconds),
			ID:                 row.ID,
		}); err != nil {
			return 0, err
		}
		return int64(row.ID), nil
	}
}

func mustJSON(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return raw
}

func nullInt64(v *int64) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *v, Valid: true}
}

// ApplicationWithBinding pairs an app with its first enabled binding.
type ApplicationWithBinding struct {
	App     *Application
	Binding *Binding
}

// ListApplicationsWithBindings returns apps that have ≥1 enabled binding,
// filtered by visibility scope, ordered by created_at (list semantics).
func (r *Repo) ListApplicationsWithBindings(ctx context.Context, scope string, callerID int64, isStaff bool) ([]ApplicationWithBinding, error) {
	bindRows, err := r.q(ctx).ListEnabledBindings(ctx)
	if err != nil {
		return nil, err
	}
	appIDs := make([]uint64, 0, len(bindRows))
	seen := map[uint64]bool{}
	for _, b := range bindRows {
		if !seen[b.ApplicationID] {
			seen[b.ApplicationID] = true
			appIDs = append(appIDs, b.ApplicationID)
		}
	}
	out := make([]ApplicationWithBinding, 0, len(appIDs))
	for _, appID := range appIDs {
		app, err := r.ApplicationByID(ctx, int64(appID))
		if err != nil {
			continue
		}
		if !visible(app, scope, callerID, isStaff) {
			continue
		}
		var binding *Binding
		for _, b := range bindRows {
			if b.ApplicationID == appID {
				binding = bindingFromRow(b)
				break
			}
		}
		out = append(out, ApplicationWithBinding{App: app, Binding: binding})
	}
	return out, nil
}

// ListUnboundApplications returns apps without any enabled binding
// ("extras" in the list semantics).
func (r *Repo) ListUnboundApplications(ctx context.Context, scope, kind string, callerID int64, isStaff bool) ([]ApplicationWithBinding, error) {
	rows, err := r.q(ctx).ListApplicationsByVisibility(ctx, db.ListApplicationsByVisibilityParams{
		ShowAll: isStaff,
		// The pre-filter must be a SUPERSET of what visible() accepts:
		// manage scope includes other users' public applications, so the
		// SQL side keeps is_public rows as well (visible() re-checks).
		IsPublic:  scope == "public" || scope == "manage",
		CreatedBy: sql.NullInt64{Int64: callerID, Valid: scope != "public"},
		Limit:     1000,
	})
	if err != nil {
		return nil, err
	}
	bound := map[uint64]bool{}
	if bindRows, err := r.q(ctx).ListEnabledBindings(ctx); err == nil {
		for _, b := range bindRows {
			bound[b.ApplicationID] = true
		}
	}
	out := make([]ApplicationWithBinding, 0)
	for _, row := range rows {
		if bound[row.ID] {
			continue
		}
		if kind != "all" && row.Kind != kind {
			continue
		}
		app := &Application{
			ID: int64(row.ID), Slug: row.Slug, Name: row.Name,
			Description: row.Description.String, Icon: row.Icon, AvatarKey: row.AvatarKey,
			Color: row.Color, Kind: row.Kind, RendererKey: row.RendererKey, ExecutorKey: row.ExecutorKey,
			CategorySlug: row.CategorySlug.String, CategoryName: row.CategoryName.String,
			IsPublic: row.IsPublic, Enabled: row.Enabled, IsDefaultAgent: row.IsDefaultAgent,
			UsageCount: int64(row.UsageCount),
			CreatedAt:  row.CreatedAt, UpdatedAt: row.UpdatedAt,
		}
		if row.CreatedBy.Valid {
			v := int64(row.CreatedBy.Int64)
			app.CreatedBy = &v
		}
		if !visible(app, scope, callerID, isStaff) {
			continue
		}
		out = append(out, ApplicationWithBinding{App: app})
	}
	return out, nil
}

// visible — the 2026-09 access policy:
//
//   - staff (管理员) sees everything: only admins author agents / toggle
//     应用中心 switches, so they need the full catalog for management.
//   - a disabled application (enabled=0) is hidden from every non-admin
//     surface — home shortcuts, switcher, agent market and app center.
//   - a private application (is_public=0, 「仅自己可见」) is admin-only:
//     regular users never see it, even when they created it (legacy rows).
//   - a public application is visible to everyone; per-user usability is
//     still governed by the provider-side identity visibility check the
//     worker runs against the caller's own UAT.
func visible(app *Application, scope string, callerID int64, isStaff bool) bool {
	if isStaff {
		return true
	}
	if !app.Enabled {
		return false
	}
	isOwn := app.CreatedBy != nil && *app.CreatedBy == callerID
	if scope == "mine" {
		return isOwn
	}
	// public / manage: regular users only ever see published applications —
	// only admins can author agents now, so a non-staff caller has nothing
	// of its own to merge into manage scope.
	return app.IsPublic
}

// VisibleTo exposes the access policy above as a single-row predicate.
//
// Single-application resolution (GET /v2/applications/resolve, 执行报告 §12,
// and the bootstrap groups) must apply EXACTLY the same rule as the catalog
// list; re-implementing it at a call site is how "the deep link opens an
// application the market hides" bugs are born. `scope` uses the same values
// as the list endpoints: public | mine | manage.
func VisibleTo(app *Application, scope string, callerID int64, isStaff bool) bool {
	return visible(app, scope, callerID, isStaff)
}

// VisibleScopeManage is the scope the workspace surfaces resolve with.
const VisibleScopeManage = "manage"

// ConsumptionFacts carries every fact the consumption decision needs. All
// three are single-object reads; ProviderStatus is nil when the binding's
// provider_key has no providers row (the same "fail closed" shape
// GetExecutionAuthBundle produces for AuthorizeExecution).
type ConsumptionFacts struct {
	Application    *Application
	Binding        *Binding
	ProviderStatus *string
}

// Consumable is THE consumption predicate (三次复审 P0-R3) — the one rule
// every consumer surface and the execution gate share:
//
//	application.enabled = 1
//	AND (kind <> 'chat'
//	     OR (enabled binding exists
//	         AND provider row exists
//	         AND provider.status = 'active'))
//
// It is exactly what AuthorizeExecution enforces (评测 P0-1): enabled apps
// only, a runtime for chat apps, and — since P0-R3 — a LIVE provider behind
// that runtime. Before this existed `Usable` stopped at the binding, so a
// provider an admin switched off still flowed through every consumer surface
// (catalog page, resolve, @mention, bootstrap, default agent) and only POST
// /v2/runs refused — the "能看到、能打开，真正发送却失败" shape P0-5 fixed
// for bindings, back with a different trigger.
//
// Visibility answers "who may SEE the row" (visible(), a management concern)
// and stays orthogonal: consume surfaces apply BOTH.
func Consumable(f ConsumptionFacts) bool {
	if f.Application == nil || !f.Application.Enabled {
		return false
	}
	if f.Application.Kind != "chat" {
		// A fixed application is a page delivered by the build; no runtime
		// and no provider behind it.
		return true
	}
	if f.Binding == nil || !f.Binding.Enabled {
		return false
	}
	return f.ProviderStatus != nil && *f.ProviderStatus == "active"
}

// PageModeManage / PageModeConsume select whether a catalog page lists rows
// the caller may MANAGE or rows the caller may actually USE (二次复审 P0-5).
//
//	manage   the authoring surfaces (智能体市场, 应用中心): everything
//	         `visible()` allows, including disabled / private / unbound.
//	consume  every surface that opens or runs something (切换器, 移动端
//	         目录, bootstrap 分组, resolve, @mention): additionally filtered
//	         by `Usable`.
const (
	PageModeManage  = "manage"
	PageModeConsume = "consume"
)

// ─────────────────────────────────────────── keyset-paginated catalog page ──

// PageCursor is the opaque keyset cursor: base64url(JSON) of the
// (created_at, id) anchor in ascending order — the same order the legacy
// list used, so page one keeps the existing UI ordering.
type PageCursor struct {
	CreatedAt time.Time `json:"c"`
	ID        int64     `json:"i"`
}

// EncodePageCursor renders the anchor as an opaque string for next_cursor.
func EncodePageCursor(t time.Time, id int64) string {
	payload, _ := json.Marshal(PageCursor{CreatedAt: t.UTC(), ID: id})
	return base64.RawURLEncoding.EncodeToString(payload)
}

// ErrInvalidCursor is returned for a cursor that decodes but carries no
// usable position (执行报告 §21 / P2-3).
var ErrInvalidCursor = errors.New("invalid cursor")

// DecodePageCursor parses a next_cursor round-trip. Any malformed input is
// an error — the handler answers 400 instead of silently restarting the walk.
//
// A payload that decodes cleanly but carries a zero anchor (`{}`, or
// `{"c":"0001-01-01T00:00:00Z","i":0}`) is REJECTED too: the zero value is
// exactly what "first page" means internally, so accepting it would let a
// hand-crafted cursor silently restart the walk at page one instead of
// failing loudly. The fields are not signed — a cursor is a pagination
// position, not a capability (§21), so an HMAC would buy nothing.
func DecodePageCursor(raw string) (time.Time, int64, error) {
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, 0, err
	}
	var c PageCursor
	if err := json.Unmarshal(payload, &c); err != nil {
		return time.Time{}, 0, err
	}
	if c.ID <= 0 || c.CreatedAt.IsZero() {
		return time.Time{}, 0, ErrInvalidCursor
	}
	return c.CreatedAt, c.ID, nil
}

// PageQueryKindChat / PageQueryKindFixed / PageQueryKindAll select which
// application kinds a catalog page carries. "fixed" (应用中心) means
// kind <> 'chat' — one SQL predicate instead of a Go-side re-filter.
const (
	PageQueryKindChat  = "chat"
	PageQueryKindFixed = "fixed"
	PageQueryKindAll   = "all"
)

// PageUncategorizedSlug is the sentinel category the mobile sheet uses for
// its 其他 tab (rows with no category at all). Mirrors the frontend's
// UNCATEGORIZED_SLUG; never a real category slug.
const PageUncategorizedSlug = "__uncategorized__"

// ApplicationPageQuery describes ONE keyset page request. Every filter maps
// 1:1 onto a ListApplicationPage WHERE term — the SQL encodes the full
// visible() policy so the Go layer must not re-filter (that would under-fill
// pages and break cursor determinism).
type ApplicationPageQuery struct {
	// Scope: public | mine | manage (same values as the legacy list).
	Scope string
	// Mode: PageModeManage (default) | PageModeConsume — see PageModeManage.
	Mode string
	// Kind: PageQueryKindChat (default) | PageQueryKindFixed | PageQueryKindAll.
	Kind string
	// IncludeUnbound keeps binding-less rows (the legacy include_unbound).
	IncludeUnbound bool
	// Search is a case-insensitive substring match on name/description;
	// empty means no filter.
	Search string
	// CategorySlug filters by category; empty means no filter.
	// PageUncategorizedSlug matches rows WITHOUT a category instead.
	CategorySlug string
	// Limit is the page size (the caller queries limit+1 to detect has_more).
	Limit int
	// CursorCreatedAt + CursorID are the keyset position (inclusive anchor:
	// strictly-after semantics). Zero value = first page.
	CursorCreatedAt time.Time
	CursorID        int64
	CallerID        int64
	IsStaff         bool
}

// boolArg — sqlc emits the boolean query flags as interface{}; pass 1/0 so
// the MySQL driver binds a plain integer either way.
func boolArg(v bool) int {
	if v {
		return 1
	}
	return 0
}

// ListApplicationPage returns ONE ordered page of ApplicationWithBinding.
//
// The enabled binding is resolved by the SQL anti-join (newest enabled
// binding wins — same choice GetEnabledBinding makes), so the page costs one
// query instead of the legacy ListEnabledBindings + N×ApplicationByID.
func (r *Repo) ListApplicationPage(ctx context.Context, q ApplicationPageQuery) ([]ApplicationWithBinding, error) {
	mineOnly := !q.IsStaff && q.Scope == "mine"
	search := strings.TrimSpace(q.Search)
	uncategorized := q.CategorySlug == PageUncategorizedSlug
	// consume mode applies `Usable` IN SQL, not in Go: filtering afterwards
	// would under-fill pages and break the keyset cursor's determinism
	// (the same reason visible() lives in the WHERE clause).
	consumeOnly := q.Mode == PageModeConsume
	// narg probe: nil → `? IS NULL` short-circuits the LIKE branches; any
	// non-nil value activates them.
	var searchArg interface{}
	if search != "" {
		searchArg = int(1)
	}

	rows, err := r.q(ctx).ListApplicationPage(ctx, db.ListApplicationPageParams{
		ShowAll:        boolArg(q.IsStaff),
		MineOnly:       boolArg(mineOnly),
		PageCallerID:   sql.NullInt64{Int64: q.CallerID, Valid: true},
		PublicOnly:     boolArg(!q.IsStaff && !mineOnly),
		ConsumeOnly:    boolArg(consumeOnly),
		KindChatOnly:   boolArg(q.Kind == PageQueryKindChat),
		KindFixedOnly:  boolArg(q.Kind == PageQueryKindFixed),
		KindAll:        boolArg(q.Kind != PageQueryKindChat && q.Kind != PageQueryKindFixed),
		AllowUnbound:   boolArg(q.IncludeUnbound),
		Search:         searchArg,
		SearchNameLike: likePattern(search),
		// The same needle feeds the description AND the category-name branch
		// (see the query's comment): one placeholder, two comparisons.
		SearchDescLike: likePattern(search),
		CategorySlug:   sql.NullString{String: q.CategorySlug, Valid: q.CategorySlug != ""},
		CategoryIsNull: boolArg(uncategorized),
		// First page: anchor far in the past, so `created_at > ?` is a no-op.
		CursorCreatedGt: q.CursorCreatedAt,
		CursorCreatedEq: q.CursorCreatedAt,
		CursorIDGt:      uint64(max64(q.CursorID, 0)),
		Limit:           int32(max64(int64(q.Limit), 1)),
	})
	if err != nil {
		return nil, err
	}
	out := make([]ApplicationWithBinding, 0, len(rows))
	for _, row := range rows {
		out = append(out, applicationPageRow(row))
	}
	return out, nil
}

// likePattern renders the SQL LIKE needle for the search filter. The
// wildcards a name may legitimately contain (`_` in slug-style names, `%`)
// are escaped, so the SQL match stays equivalent to the client-side
// substring filter it replaced — a bare `_` would silently widen the match
// to any single character (MySQL LIKE uses backslash as the default escape).
func likePattern(search string) string {
	if search == "" {
		return "%"
	}
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(search)
	return "%" + escaped + "%"
}

// mentionContains / mentionPrefix render the two LIKE shapes the dedicated
// mention query needs. Both reuse the same escaping rule as `likePattern`, so
// `%`, `_` and `\` are compared as LITERAL characters (四次复审 P1-R2).
func mentionContains(q string) string {
	return likePattern(q)
}

func mentionPrefix(q string) string {
	if q == "" {
		return "%"
	}
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
	return escaped + "%"
}

// ResolveMentionCandidates answers the `@` router with ONE ranked query.
//
// This is deliberately NOT ListApplicationPage: that search also matches
// description / category name so a page of description-only noise could fill
// the LIMIT before a real name/slug match. Here the LIMIT is applied AFTER
// the exact / prefix / substring ranking, and only name/slug can match at
// all (四次复审 P1-R2).
func (r *Repo) ResolveMentionCandidates(
	ctx context.Context,
	q string,
	isStaff bool,
	limit int,
) ([]MentionCandidate, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := r.q(ctx).ResolveMentionCandidates(ctx, db.ResolveMentionCandidatesParams{
		ShowAll: boolArg(isStaff),
		// narg-free by design: an empty query is rejected by the handler, so
		// the two LIKE parameters are always real needles.
		Contains: mentionContains(q),
		Exact:    q,
		Prefix:   mentionPrefix(q),
		Limit:    int32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]MentionCandidate, 0, len(rows))
	for _, row := range rows {
		out = append(out, MentionCandidate{
			ID:   int64(row.ID),
			Slug: row.Slug,
			Name: row.Name,
			Kind: row.Kind,
		})
	}
	return out, nil
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func favoriteIDs(appIDs []int64) []uint64 {
	out := make([]uint64, 0, len(appIDs))
	for _, id := range appIDs {
		out = append(out, uint64(max64(id, 0)))
	}
	return out
}

func applicationPageRow(row db.ListApplicationPageRow) ApplicationWithBinding {
	app := &Application{
		ID:             int64(row.ID),
		Slug:           row.Slug,
		Name:           row.Name,
		Description:    row.Description,
		Icon:           row.Icon,
		AvatarKey:      row.AvatarKey,
		Color:          row.Color,
		Kind:           row.Kind,
		RendererKey:    row.RendererKey,
		ExecutorKey:    row.ExecutorKey,
		CategorySlug:   row.CategorySlug.String,
		CategoryName:   row.CategoryName.String,
		IsPublic:       row.IsPublic,
		Enabled:        row.Enabled,
		IsDefaultAgent: row.IsDefaultAgent,
		UsageCount:     int64(row.UsageCount),
		Skills:         ParseSkills(row.DefaultConfig),
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
	if row.CategoryID.Valid {
		v := row.CategoryID.Int64
		app.CategoryID = &v
	}
	if row.CreatedBy.Valid {
		v := row.CreatedBy.Int64
		app.CreatedBy = &v
	}
	out := ApplicationWithBinding{App: app}
	if !row.BindingID.Valid {
		return out
	}
	b := &Binding{
		ID:                 row.BindingID.Int64,
		ApplicationID:      app.ID,
		ProviderKey:        row.BindingProviderKey.String,
		RuntimeType:        row.BindingRuntimeType.String,
		ExternalResourceID: row.BindingExternalResourceID.String,
		IdentityMode:       row.BindingIdentityMode.String,
		ExecutionMode:      row.BindingExecutionMode.String,
		SessionPolicy:      row.BindingSessionPolicy.String,
		ArtifactPolicy:     row.BindingArtifactPolicy.String,
		TimeoutSeconds:     int64(row.BindingTimeoutSeconds.Int32),
		Enabled:            row.BindingEnabled.Bool,
	}
	if row.BindingProviderID.Valid {
		v := row.BindingProviderID.Int64
		b.ProviderID = &v
	}
	_ = json.Unmarshal(row.BindingCapabilities, &b.Capabilities)
	_ = json.Unmarshal(row.BindingConfig, &b.Config)
	if b.Capabilities == nil {
		b.Capabilities = map[string]any{}
	}
	if b.Config == nil {
		b.Config = map[string]any{}
	}
	out.Binding = b
	return out
}

// applicationRowByID projects a bootstrap row-fetch row onto the SAME
// ApplicationWithBinding shape `applicationPageRow` produces.
//
// It is a deliberate mirror rather than a shared helper: sqlc emits one
// struct per query, so the two projections cannot share a signature — but
// they MUST stay field-for-field identical, because both feed
// `buildListItem` and a divergence would show up as a bootstrap card missing
// a field a market card has.
func applicationRowByID(row db.ListApplicationRowsByIDsRow) ApplicationWithBinding {
	app := &Application{
		ID:             int64(row.ID),
		Slug:           row.Slug,
		Name:           row.Name,
		Description:    row.Description,
		Icon:           row.Icon,
		AvatarKey:      row.AvatarKey,
		Color:          row.Color,
		Kind:           row.Kind,
		RendererKey:    row.RendererKey,
		ExecutorKey:    row.ExecutorKey,
		CategorySlug:   row.CategorySlug.String,
		CategoryName:   row.CategoryName.String,
		IsPublic:       row.IsPublic,
		Enabled:        row.Enabled,
		IsDefaultAgent: row.IsDefaultAgent,
		UsageCount:     int64(row.UsageCount),
		Skills:         ParseSkills(row.DefaultConfig),
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
	if row.CategoryID.Valid {
		v := row.CategoryID.Int64
		app.CategoryID = &v
	}
	if row.CreatedBy.Valid {
		v := row.CreatedBy.Int64
		app.CreatedBy = &v
	}
	out := ApplicationWithBinding{App: app}
	if !row.BindingID.Valid {
		return out
	}
	b := &Binding{
		ID:                 row.BindingID.Int64,
		ApplicationID:      app.ID,
		ProviderKey:        row.BindingProviderKey.String,
		RuntimeType:        row.BindingRuntimeType.String,
		ExternalResourceID: row.BindingExternalResourceID.String,
		IdentityMode:       row.BindingIdentityMode.String,
		ExecutionMode:      row.BindingExecutionMode.String,
		SessionPolicy:      row.BindingSessionPolicy.String,
		ArtifactPolicy:     row.BindingArtifactPolicy.String,
		TimeoutSeconds:     int64(row.BindingTimeoutSeconds.Int32),
		Enabled:            row.BindingEnabled.Bool,
	}
	if row.BindingProviderID.Valid {
		v := row.BindingProviderID.Int64
		b.ProviderID = &v
	}
	_ = json.Unmarshal(row.BindingCapabilities, &b.Capabilities)
	_ = json.Unmarshal(row.BindingConfig, &b.Config)
	if b.Capabilities == nil {
		b.Capabilities = map[string]any{}
	}
	if b.Config == nil {
		b.Config = map[string]any{}
	}
	out.Binding = b
	return out
}

// PersonalUsage is a user's run history for ONE application.
type PersonalUsage struct {
	Count int64
	Last  *time.Time
}

// UsageByApplications aggregates the caller's runs for EXACTLY the ids on
// the current page (执行报告 §20): never the user's entire run history.
func (r *Repo) UsageByApplications(ctx context.Context, userID int64, appIDs []int64) (map[int64]PersonalUsage, error) {
	out := map[int64]PersonalUsage{}
	if len(appIDs) == 0 {
		return out, nil
	}
	ids := make([]sql.NullInt64, 0, len(appIDs))
	for _, id := range appIDs {
		ids = append(ids, sql.NullInt64{Int64: id, Valid: true})
	}
	rows, err := r.q(ctx).UserUsageByApplications(ctx, db.UserUsageByApplicationsParams{
		UserID:     sql.NullInt64{Int64: userID, Valid: true},
		PageAppIds: ids,
	})
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		entry := PersonalUsage{Count: row.UsageCount}
		if t, ok := row.LastUsedAt.(time.Time); ok {
			tt := t
			entry.Last = &tt
		}
		if row.ApplicationID.Valid {
			out[row.ApplicationID.Int64] = entry
		}
	}
	return out, nil
}

// FavoritesByApplications resolves the caller's favorites among one page's
// ids.
func (r *Repo) FavoritesByApplications(ctx context.Context, userID int64, appIDs []int64) (map[int64]bool, error) {
	out := map[int64]bool{}
	if len(appIDs) == 0 {
		return out, nil
	}
	rows, err := r.q(ctx).FavoritesByApplications(ctx, db.FavoritesByApplicationsParams{
		UserID:         uint64(userID),
		FavoriteAppIds: favoriteIDs(appIDs),
	})
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[int64(row)] = true
	}
	return out, nil
}

// ──────────────────────────────────────────────── bootstrap groups (P1-1) ──

// BootstrapGroupLimit is the home-shortcut budget per group (§35). It is the
// single source of truth for the group size: the SQL takes it as its LIMIT,
// so the query cannot return more rows than the UI renders.
const BootstrapGroupLimit = 8

// BootstrapGroupRow is one member of a bootstrap group: the application id
// plus the PERSONAL usage facts the group was ordered by.
//
// The ranking queries return these instead of full rows so that the group
// ORDER survives the single row-fetch that follows (a plain `IN (...)` has
// no order). `LastUsedAt == nil` means "never used".
type BootstrapGroupRow struct {
	ApplicationID int64
	UsageCount    int64
	LastUsedAt    *time.Time
}

// BootstrapGroups is every ordered group the workspace bootstrap answers
// with, still as (id, usage) pairs — the caller resolves the rows once.
type BootstrapGroups struct {
	Default         *BootstrapGroupRow
	Favorites       []BootstrapGroupRow
	Frequent        []BootstrapGroupRow
	Recent          []BootstrapGroupRow
	Recommended     []BootstrapGroupRow
	RecentFixedApps []BootstrapGroupRow
}

// BootstrapCategory is one entry of a category rail.
type BootstrapCategory struct {
	Slug  string
	Name  string
	Count int64
}

// BootstrapCategories holds the two category rails: 智能体 (chat) and
// 应用中心 (non-chat).
type BootstrapCategories struct {
	Agents []BootstrapCategory
	Apps   []BootstrapCategory
}

// BootstrapGroupQuery describes one bootstrap group request.
type BootstrapGroupQuery struct {
	CallerID int64
	IsStaff  bool
}

// BootstrapGroups loads the ordered shortcut groups in a FIXED number of
// queries, each returning at most BootstrapGroupLimit rows (二次复审 P1-1).
//
// This replaces the previous `ListApplicationPage{Limit: 5000}` + Go-side
// projection, whose cost grew with the catalog and whose result was silently
// wrong past row 5000: a category, the top 推荐 agent or the only valid
// default fallback that sorted later simply disappeared. Here every group is
// the database's own answer to its own business question.
//
// `sql.ErrNoRows` from the default-agent query is NOT an error: a workspace
// with no usable chat agent yet has no default, and the composer simply
// starts unbound.
func (r *Repo) BootstrapGroups(ctx context.Context, q BootstrapGroupQuery) (BootstrapGroups, error) {
	var out BootstrapGroups
	caller := sql.NullInt64{Int64: q.CallerID, Valid: true}
	showAll := boolArg(q.IsStaff)
	limit := int32(BootstrapGroupLimit)
	favUser := uint64(max64(q.CallerID, 0))

	def, err := r.q(ctx).BootstrapDefaultApplication(ctx, db.BootstrapDefaultApplicationParams{
		CallerID: caller, ShowAll: showAll,
	})
	switch {
	case err == nil:
		row := bootstrapGroupRow(def.ID, def.PersonalUsageCount, def.LastUsedAt)
		out.Default = &row
	case errors.Is(err, sql.ErrNoRows):
		// No usable default yet — legitimate, not a failure.
	default:
		return out, err
	}

	if rows, err := r.q(ctx).BootstrapFavoriteApplications(ctx, db.BootstrapFavoriteApplicationsParams{
		CallerID: caller, FavUserID: favUser, ShowAll: showAll, Limit: limit,
	}); err != nil {
		return out, err
	} else {
		out.Favorites = bootstrapGroupRowsFavorite(rows)
	}

	if rows, err := r.q(ctx).BootstrapFrequentApplications(ctx, db.BootstrapFrequentApplicationsParams{
		CallerID: caller, ShowAll: showAll, Limit: limit,
	}); err != nil {
		return out, err
	} else {
		out.Frequent = bootstrapGroupRowsFrequent(rows)
	}

	if rows, err := r.q(ctx).BootstrapRecentApplications(ctx, db.BootstrapRecentApplicationsParams{
		CallerID: caller, ShowAll: showAll, Limit: limit,
	}); err != nil {
		return out, err
	} else {
		out.Recent = bootstrapGroupRowsRecent(rows)
	}

	if rows, err := r.q(ctx).BootstrapRecommendedApplications(ctx, db.BootstrapRecommendedApplicationsParams{
		CallerID: caller, ShowAll: showAll, FavUserID: favUser, Limit: limit,
	}); err != nil {
		return out, err
	} else {
		out.Recommended = bootstrapGroupRowsRecommended(rows)
	}

	if rows, err := r.q(ctx).BootstrapRecentFixedApplications(ctx, db.BootstrapRecentFixedApplicationsParams{
		CallerID: caller, ShowAll: showAll, Limit: limit,
	}); err != nil {
		return out, err
	} else {
		out.RecentFixedApps = bootstrapGroupRowsFixed(rows)
	}
	return out, nil
}

// BootstrapCategories loads the two category rails with a SQL GROUP BY
// (二次复审 §17): one row per category, counted and ordered by first
// appearance. Rows with no category come back with an empty slug, which the
// HTTP layer renders as the `__uncategorized__` sentinel.
func (r *Repo) BootstrapCategories(ctx context.Context, isStaff bool) (BootstrapCategories, error) {
	var out BootstrapCategories
	showAll := boolArg(isStaff)
	agents, err := r.q(ctx).BootstrapAgentCategories(ctx, showAll)
	if err != nil {
		return out, err
	}
	out.Agents = bootstrapAgentCategories(agents)
	apps, err := r.q(ctx).BootstrapAppCategories(ctx, showAll)
	if err != nil {
		return out, err
	}
	out.Apps = bootstrapAppCategories(apps)
	return out, nil
}

// ListApplicationRowsByIDs materialises the applications the bootstrap
// groups selected — bounded by the groups themselves (≤ ~40 rows), never by
// the catalog. The visibility + consume predicates are applied again so this
// fetch is safe on its own.
func (r *Repo) ListApplicationRowsByIDs(ctx context.Context, appIDs []int64, isStaff bool) ([]ApplicationWithBinding, error) {
	out := make([]ApplicationWithBinding, 0, len(appIDs))
	if len(appIDs) == 0 {
		return out, nil
	}
	rows, err := r.q(ctx).ListApplicationRowsByIDs(ctx, db.ListApplicationRowsByIDsParams{
		AppIds:  favoriteIDs(appIDs),
		ShowAll: boolArg(isStaff),
	})
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out = append(out, applicationRowByID(row))
	}
	return out, nil
}

// bootstrapGroupRow converts one sqlc group row. Every group query returns
// the identical `(id, personal_usage_count, last_used_at)` triple, so the
// conversion is shared; the per-query wrappers below only adapt the slice
// type (sqlc emits one struct per query).
func bootstrapGroupRow(id uint64, count int64, last interface{}) BootstrapGroupRow {
	row := BootstrapGroupRow{ApplicationID: int64(id), UsageCount: count}
	if t, ok := last.(time.Time); ok {
		tt := t
		row.LastUsedAt = &tt
	}
	return row
}

func bootstrapGroupRowsDefault(rows []db.BootstrapDefaultApplicationRow) []BootstrapGroupRow {
	out := make([]BootstrapGroupRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, bootstrapGroupRow(row.ID, row.PersonalUsageCount, row.LastUsedAt))
	}
	return out
}

func bootstrapGroupRowsFavorite(rows []db.BootstrapFavoriteApplicationsRow) []BootstrapGroupRow {
	out := make([]BootstrapGroupRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, bootstrapGroupRow(row.ID, row.PersonalUsageCount, row.LastUsedAt))
	}
	return out
}

func bootstrapGroupRowsFrequent(rows []db.BootstrapFrequentApplicationsRow) []BootstrapGroupRow {
	out := make([]BootstrapGroupRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, bootstrapGroupRow(row.ID, row.PersonalUsageCount, row.LastUsedAt))
	}
	return out
}

func bootstrapGroupRowsRecent(rows []db.BootstrapRecentApplicationsRow) []BootstrapGroupRow {
	out := make([]BootstrapGroupRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, bootstrapGroupRow(row.ID, row.PersonalUsageCount, row.LastUsedAt))
	}
	return out
}

func bootstrapGroupRowsRecommended(rows []db.BootstrapRecommendedApplicationsRow) []BootstrapGroupRow {
	out := make([]BootstrapGroupRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, bootstrapGroupRow(row.ID, row.PersonalUsageCount, row.LastUsedAt))
	}
	return out
}

func bootstrapGroupRowsFixed(rows []db.BootstrapRecentFixedApplicationsRow) []BootstrapGroupRow {
	out := make([]BootstrapGroupRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, bootstrapGroupRow(row.ID, row.PersonalUsageCount, row.LastUsedAt))
	}
	return out
}

func bootstrapAgentCategories(rows []db.BootstrapAgentCategoriesRow) []BootstrapCategory {
	out := make([]BootstrapCategory, 0, len(rows))
	for _, row := range rows {
		out = append(out, BootstrapCategory{
			Slug: row.CategorySlug, Name: row.CategoryName, Count: row.CategoryCount})
	}
	return out
}

func bootstrapAppCategories(rows []db.BootstrapAppCategoriesRow) []BootstrapCategory {
	out := make([]BootstrapCategory, 0, len(rows))
	for _, row := range rows {
		out = append(out, BootstrapCategory{
			Slug: row.CategorySlug, Name: row.CategoryName, Count: row.CategoryCount})
	}
	return out
}
