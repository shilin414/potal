package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

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
		ID:     int64(row.ID),
		Key:    row.ProviderKey,
		Name:   row.Name,
		Status: row.Status,
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
		p := Provider{ID: int64(row.ID), Key: row.ProviderKey, Name: row.Name, Status: row.Status}
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
		IsDefaultAgent: row.IsDefaultAgent,
		UsageCount:     int64(row.UsageCount),
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
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
		ShowAll:   isStaff,
		IsPublic:  scope == "public",
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
			IsPublic: row.IsPublic, IsDefaultAgent: row.IsDefaultAgent, UsageCount: int64(row.UsageCount),
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
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

// visible mirrors the reference scope semantics exactly:
//
//	public → is_public only;  mine → own only;  manage → public | mine;
//	staff sees everything (reference: is_staff bypass in can_manage paths).
func visible(app *Application, scope string, callerID int64, isStaff bool) bool {
	if isStaff {
		return true
	}
	isOwn := app.CreatedBy != nil && *app.CreatedBy == callerID
	switch scope {
	case "mine":
		return isOwn
	case "manage":
		return app.IsPublic || isOwn
	default: // public
		return app.IsPublic
	}
}
