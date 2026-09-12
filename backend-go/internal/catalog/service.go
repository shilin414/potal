package catalog

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"

	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
)

// Service implements the agent-marketplace authoring behavior.
type Service struct {
	DB       *sql.DB
	Registry *RuntimeRegistry
	Storage  StorageForAvatar
}

// StorageForAvatar is the storage boundary needed by the service
// (narrow interface — no generic repository).
type StorageForAvatar interface {
	Delete(ctx context.Context, key string) error
}

func (s *Service) q(tx *sql.Tx) db.Querier { return db.New(tx) }
func (s *Service) ProviderByKey(ctx context.Context, key string) (*Provider, error) {
	return s.repo().ProviderByKey(ctx, key)
}
func (s *Service) ApplicationByID(ctx context.Context, id int64) (*Application, error) {
	return s.repo().ApplicationByID(ctx, id)
}
func (s *Service) EnabledBinding(ctx context.Context, appID int64) (*Binding, error) {
	return s.repo().EnabledBinding(ctx, appID)
}
func (s *Service) BindingsByApplication(ctx context.Context, appID int64) ([]*Binding, error) {
	return s.repo().BindingsByApplication(ctx, appID)
}
func (s *Service) repo() *Repo { return &Repo{DB: s.DB} }

// Slug regex from the validated contract.
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// Business errors (transport maps them to the exact HTTP envelopes).
var (
	ErrNameRequired     = errors.New("请输入智能体名称")
	ErrBadSlug          = errors.New("标识仅支持小写字母、数字和连字符")
	ErrSlugTaken        = errors.New("该标识已被占用")
	ErrNotManageable    = errors.New("只有应用创建者或管理员可以修改该智能体。")
	ErrOnlyChatRenderer = errors.New("智能体市场目前只创建 chat renderer 的应用")
	ErrNoBinding        = errors.New("application has no runtime binding")
	ErrNotDefaultable   = errors.New("只有「有可用运行时绑定的聊天应用」才能设为主智能体")
	ErrDeleteReferenced = errors.New("该智能体已有会话或运行记录，无法删除；可先将其改为「不公开」。")
	ErrUnknownProvider  = errors.New("未知的运行时提供方")
	ErrProviderDisabled = errors.New("运行时提供方已停用")
	ErrUnsupportedRt    = errors.New("不支持该运行时类型")
	ErrAdapterNotReg    = errors.New("运行时适配器未注册")
)

// CreateInput mirrors ApplicationCreateRequest.
type CreateInput struct {
	Name            string
	Slug            string
	Description     string
	Icon            string
	Color           string
	CategorySlug    string
	CategoryName    string
	IsPublic        bool
	Kind            string
	RendererKey     string
	Runtime         *BindingInput
	SetDefaultAgent bool
	CreatorID       int64
	IsStaff         bool
}

// BindingInput mirrors RuntimeBindingInput.
type BindingInput struct {
	ProviderKey        string
	RuntimeType        string
	ExternalResourceID string
	IdentityMode       string
	ExecutionMode      string
	SessionPolicy      string
	ArtifactPolicy     string
	TimeoutSeconds     int64
	Config             map[string]any
}

// Create creates Application (+binding) in one transaction.
func (s *Service) Create(ctx context.Context, in *CreateInput) (*Application, *Binding, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, nil, ErrNameRequired
	}
	slug := strings.ToLower(strings.TrimSpace(in.Slug))
	if slug == "" {
		slug = "agent-" + randomHex(4)
	}
	if !slugPattern.MatchString(slug) {
		return nil, nil, ErrBadSlug
	}
	if in.RendererKey != "" && in.RendererKey != "chat" {
		return nil, nil, ErrOnlyChatRenderer
	}
	renderer := "chat"
	if in.RendererKey != "" {
		renderer = in.RendererKey
	}

	// Validate runtime up front (provider active + adapter registered + id format).
	var adapter RuntimeAdapter
	var provider *Provider
	if in.Runtime != nil {
		a, p, err := s.validateRuntimeInput(ctx, in.Runtime)
		if err != nil {
			return nil, nil, err
		}
		adapter, provider = a, p
	}

	var (
		app     *Application
		binding *Binding
	)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()

	_, categoryID, err := s.resolveCategoryTx(ctx, tx, in.CategorySlug, in.CategoryName)
	if err != nil {
		return nil, nil, err
	}

	res, err := s.q(tx).CreateApplication(ctx, db.CreateApplicationParams{
		Slug:        slug,
		Name:        name,
		Description: nullText(in.Description),
		Icon:        in.Icon,
		Color:       in.Color,
		Kind:        defaultStr(in.Kind, "chat"),
		RendererKey: renderer,
		ExecutorKey: "",
		CategoryID:  nullInt64(categoryID),
		IsPublic:    in.IsPublic,
		CreatedBy:   nullInt64(&in.CreatorID),
	})
	if err != nil {
		return nil, nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, nil, err
	}

	if in.Runtime != nil {
		b := bindingFromInput(id, in.Runtime)
		caps := adapterCapabilities(adapter)
		bid, err := UpsertBindingTx(ctx, tx, b, caps)
		if err != nil {
			return nil, nil, err
		}
		b.ID = bid
		binding = b
	}

	if in.SetDefaultAgent {
		if err := promoteDefaultTx(ctx, tx, id); err != nil {
			return nil, nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	_ = provider

	app, err = s.ApplicationByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if binding == nil && in.Runtime != nil {
		binding, _ = s.EnabledBinding(ctx, id)
	} else if binding == nil {
		binding = nil
	}
	return app, binding, nil
}

// Update applies partial edits; runtime rebinding upserts the binding.
func (s *Service) Update(ctx context.Context, appID, callerID int64, isStaff bool, name, description, icon, color *string, isPublic *bool, categorySlug, categoryName *string, runtime *BindingInput, setDefaultAgent *bool) (*Application, *Binding, error) {
	app, err := s.ApplicationByID(ctx, appID)
	if err != nil {
		return nil, nil, err
	}
	if !canManage(app, callerID, isStaff) {
		return nil, nil, ErrNotManageable
	}

	var adapter RuntimeAdapter
	if runtime != nil {
		a, _, err := s.validateRuntimeInput(ctx, runtime)
		if err != nil {
			return nil, nil, err
		}
		adapter = a
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()

	newName := app.Name
	if name != nil && strings.TrimSpace(*name) != "" {
		newName = strings.TrimSpace(*name)
	}
	newDesc := app.Description
	if description != nil {
		newDesc = *description
	}
	newIcon := app.Icon
	if icon != nil {
		newIcon = *icon
	}
	newColor := app.Color
	if color != nil {
		newColor = *color
	}
	newPublic := app.IsPublic
	if isPublic != nil {
		newPublic = *isPublic
	}
	var categoryID *int64
	if categorySlug != nil || categoryName != nil {
		cs, cn := "", ""
		if categorySlug != nil {
			cs = *categorySlug
		}
		if categoryName != nil {
			cn = *categoryName
		}
		if cs == "" && cn == "" {
			categoryID = nil
		} else {
			_, cid, err := s.resolveCategoryTx(ctx, tx, cs, cn)
			if err != nil {
				return nil, nil, err
			}
			categoryID = cid
		}
	} else {
		categoryID = app.CategoryID
	}

	if _, err := s.q(tx).UpdateApplication(ctx, db.UpdateApplicationParams{
		Name:        newName,
		Description: nullText(newDesc),
		Icon:        newIcon,
		Color:       newColor,
		IsPublic:    newPublic,
		CategoryID:  nullInt64(categoryID),
		RendererKey: app.RendererKey,
		ID:          uint64(appID),
	}); err != nil {
		return nil, nil, err
	}

	var updatedBinding *Binding
	if runtime != nil {
		b := bindingFromInput(appID, runtime)
		caps := adapterCapabilities(adapter)
		if _, err := UpsertBindingTx(ctx, tx, b, caps); err != nil {
			return nil, nil, err
		}
		updatedBinding = b
	}

	if setDefaultAgent != nil {
		if *setDefaultAgent {
			if err := promoteDefaultTx(ctx, tx, appID); err != nil {
				return nil, nil, err
			}
		} else if app.IsDefaultAgent {
			if _, err := tx.ExecContext(ctx, `UPDATE applications SET is_default_agent = 0 WHERE id = ?`, appID); err != nil {
				return nil, nil, err
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	app, err = s.ApplicationByID(ctx, appID)
	if err != nil {
		return nil, nil, err
	}
	if updatedBinding == nil {
		updatedBinding, _ = s.EnabledBinding(ctx, appID)
	}
	return app, updatedBinding, nil
}

// Delete removes an application unless conversations/runs reference it.
func (s *Service) Delete(ctx context.Context, appID, callerID int64, isStaff bool) error {
	app, err := s.ApplicationByID(ctx, appID)
	if err != nil {
		return err
	}
	if !canManage(app, callerID, isStaff) {
		return ErrNotManageable
	}
	var convCount, runCount int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversations WHERE application_id = ?`, appID).Scan(&convCount); err != nil {
		return err
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE application_id = ?`, appID).Scan(&runCount); err != nil {
		return err
	}
	if convCount > 0 || runCount > 0 {
		return ErrDeleteReferenced
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	bindings, err := s.BindingsByApplication(ctx, appID)
	if err == nil {
		for _, b := range bindings {
			if _, err := tx.ExecContext(ctx, `DELETE FROM runtime_bindings WHERE id = ?`, b.ID); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM application_favorites WHERE application_id = ?`, appID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM applications WHERE id = ?`, appID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if app.AvatarKey != "" && s.Storage != nil {
		_ = s.Storage.Delete(ctx, app.AvatarKey)
	}
	return nil
}

// SetDefaultAgent promotes a chat app with an enabled binding; the flag is
// globally unique and enforced transactionally (no conditional unique
// indexes in TiDB/MySQL).
func (s *Service) SetDefaultAgent(ctx context.Context, appID, callerID int64, isStaff bool) error {
	app, err := s.ApplicationByID(ctx, appID)
	if err != nil {
		return err
	}
	if !canManage(app, callerID, isStaff) {
		return ErrNotManageable
	}
	if app.Kind != "chat" {
		return ErrNotDefaultable
	}
	if _, err := s.EnabledBinding(ctx, appID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrNotDefaultable
		}
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := promoteDefaultTx(ctx, tx, appID); err != nil {
		return err
	}
	return tx.Commit()
}

// UnsetDefaultAgent clears the flag if this app holds it.
func (s *Service) UnsetDefaultAgent(ctx context.Context, appID, callerID int64, isStaff bool) error {
	app, err := s.ApplicationByID(ctx, appID)
	if err != nil {
		return err
	}
	if !canManage(app, callerID, isStaff) {
		return ErrNotManageable
	}
	_, err = s.DB.ExecContext(ctx, `UPDATE applications SET is_default_agent = 0 WHERE id = ? AND is_default_agent = 1`, appID)
	return err
}

// Favorite / Unfavorite are idempotent.
func (s *Service) Favorite(ctx context.Context, appID, userID int64) error {
	if _, err := s.ApplicationByID(ctx, appID); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(ctx, `INSERT IGNORE INTO application_favorites (user_id, application_id) VALUES (?, ?)`, userID, appID)
	return err
}

func (s *Service) Unfavorite(ctx context.Context, appID, userID int64) error {
	if _, err := s.ApplicationByID(ctx, appID); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(ctx, `DELETE FROM application_favorites WHERE user_id = ? AND application_id = ?`, userID, appID)
	return err
}

// validateRuntimeInput checks provider + adapter + resource id format.
func (s *Service) validateRuntimeInput(ctx context.Context, in *BindingInput) (RuntimeAdapter, *Provider, error) {
	provider, err := s.ProviderByKey(ctx, in.ProviderKey)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil, fmt.Errorf("%w：%s", ErrUnknownProvider, in.ProviderKey)
		}
		return nil, nil, err
	}
	if provider.Status != "active" {
		return nil, nil, fmt.Errorf("%w：%s", ErrProviderDisabled, in.ProviderKey)
	}
	rt := defaultStr(in.RuntimeType, RuntimeTypeAgent)
	if !containsString(provider.SupportedRuntimeTypes, rt) {
		return nil, nil, fmt.Errorf("%w", ErrUnsupportedRt)
	}
	adapter, err := s.Registry.Resolve(provider.Key, rt)
	if err != nil {
		return nil, nil, fmt.Errorf("%w：%s:%s", ErrAdapterNotReg, provider.Key, rt)
	}
	if err := ValidateResourceID(adapter, in.ExternalResourceID); err != nil {
		return nil, nil, err
	}
	return adapter, provider, nil
}

func (s *Service) resolveCategoryTx(ctx context.Context, tx *sql.Tx, slug, name string) (int64, *int64, error) {
	slug = strings.TrimSpace(slug)
	name = strings.TrimSpace(name)
	if slug == "" && name == "" {
		slug, name = "agents", "智能体"
	}
	if slug == "" {
		slug = slugify(name)
	}
	if name == "" {
		row := tx.QueryRowContext(ctx, `SELECT name FROM application_categories WHERE slug = ?`, slug)
		var n string
		if err := row.Scan(&n); err == nil {
			name = n
		} else {
			name = slug
		}
	}
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM application_categories WHERE slug = ?`, slug).Scan(&id)
	if err == nil {
		return id, &id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, nil, err
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO application_categories (slug, name, description, icon, sort_order) VALUES (?, ?, '', '', 0) ON DUPLICATE KEY UPDATE name = VALUES(name)`,
		slug, name)
	if err != nil {
		return 0, nil, err
	}
	if id, err = res.LastInsertId(); err != nil {
		// ON DUPLICATE hit: fetch the existing id.
		err2 := tx.QueryRowContext(ctx, `SELECT id FROM application_categories WHERE slug = ?`, slug).Scan(&id)
		if err2 != nil {
			return 0, nil, err
		}
	}
	return id, &id, nil
}

func promoteDefaultTx(ctx context.Context, tx *sql.Tx, appID int64) error {
	if _, err := tx.ExecContext(ctx, `UPDATE applications SET is_default_agent = 0 WHERE is_default_agent = 1`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE applications SET is_default_agent = 1 WHERE id = ?`, appID)
	return err
}

func canManage(app *Application, callerID int64, isStaff bool) bool {
	if isStaff {
		return true
	}
	return app.CreatedBy != nil && *app.CreatedBy == callerID
}

func bindingFromInput(appID int64, in *BindingInput) *Binding {
	return &Binding{
		ApplicationID:      appID,
		RuntimeType:        defaultStr(in.RuntimeType, RuntimeTypeAgent),
		ProviderKey:        in.ProviderKey,
		ExternalResourceID: in.ExternalResourceID,
		IdentityMode:       defaultStr(in.IdentityMode, IdentityModeUser),
		ExecutionMode:      defaultStr(in.ExecutionMode, ExecutionModeInteractive),
		SessionPolicy:      defaultStr(in.SessionPolicy, SessionPolicyLazy),
		ArtifactPolicy:     defaultStr(in.ArtifactPolicy, ArtifactPolicyExternalRefresh),
		TimeoutSeconds:     clampTimeout(in.TimeoutSeconds),
		Config:             in.Config,
	}
}

func adapterCapabilities(a RuntimeAdapter) map[string]any {
	if a == nil {
		return map[string]any{}
	}
	out := map[string]any{}
	for k, v := range a.Capabilities() {
		out[k] = v
	}
	return out
}

func clampTimeout(v int64) int64 {
	if v == 0 {
		return 300
	}
	if v < 10 {
		return 10
	}
	if v > 3600 {
		return 3600
	}
	return v
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastDash := true
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "category"
	}
	return out
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func defaultStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func randomHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func nullText(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
