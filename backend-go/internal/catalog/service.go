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
	DB         *sql.DB
	Registry   *RuntimeRegistry
	Storage    StorageForAvatar
	ACLEnabled bool
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
func (s *Service) repo() *Repo { return &Repo{DB: s.DB, ACLEnabled: s.ACLEnabled} }

// Slug regex from the validated contract.
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// Business errors (transport maps them to the exact HTTP envelopes).
var (
	ErrNameRequired          = errors.New("请输入智能体名称")
	ErrBadSlug               = errors.New("标识仅支持小写字母、数字和连字符")
	ErrSlugTaken             = errors.New("该标识已被占用")
	ErrNotManageable         = errors.New("只有管理员可以管理该资源。")
	ErrOnlyChatRenderer      = errors.New("智能体必须使用 chat renderer")
	ErrFixedRendererRequired = errors.New("固定应用必须提供 renderer_key")
	ErrFixedRuntimeForbidden = errors.New("固定应用不能配置 RuntimeBinding")
	ErrApplicationKind       = errors.New("不支持的应用类型")
	ErrNoBinding             = errors.New("application has no runtime binding")
	// ErrNotDefaultable (二次复审 P0-6) now also covers a DISABLED
	// application: the default agent is the one the home composer binds to,
	// and a disabled application can never execute, so promoting it would
	// bind the workspace to an agent that refuses every send.
	ErrNotDefaultable   = errors.New("只有「已启用且有可用运行时绑定的聊天应用」才能设为主智能体")
	ErrDeleteReferenced = errors.New("该智能体已有会话或运行记录，无法删除；可先将其改为「不公开」。")
	ErrUnknownProvider  = errors.New("未知的运行时提供方")
	ErrProviderDisabled = errors.New("运行时提供方已停用")
	ErrUnsupportedRt    = errors.New("不支持该运行时类型")
	ErrAdapterNotReg    = errors.New("运行时适配器未注册")
)

// CreateInput mirrors ApplicationCreateRequest.
type CreateInput struct {
	Name         string
	Slug         string
	Description  string
	Icon         string
	Color        string
	CategorySlug string
	CategoryName string
	IsPublic     bool
	Kind         string
	RendererKey  string
	Runtime      *BindingInput
	// Skills is the agent-scoped 技能配置 stored in default_config.
	Skills          []Skill
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
	if in == nil || !in.IsStaff {
		return nil, nil, ErrNotManageable
	}
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
	kind := defaultStr(strings.TrimSpace(in.Kind), "chat")
	var renderer string
	switch kind {
	case "chat":
		if in.RendererKey != "" && in.RendererKey != "chat" {
			return nil, nil, ErrOnlyChatRenderer
		}
		renderer = "chat"
	case "page", "form", "dashboard", "custom", "task":
		renderer = strings.TrimSpace(in.RendererKey)
		if renderer == "" {
			return nil, nil, ErrFixedRendererRequired
		}
		if in.Runtime != nil {
			return nil, nil, ErrFixedRuntimeForbidden
		}
		if in.SetDefaultAgent {
			return nil, nil, ErrNotDefaultable
		}
		in.Skills = nil
	default:
		return nil, nil, ErrApplicationKind
	}

	// 技能配置 is validated up front, like the runtime below: an invalid skill
	// must fail before any row is written.
	skills, err := NormalizeSkills(in.Skills)
	if err != nil {
		return nil, nil, err
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
		Kind:        kind,
		RendererKey: renderer,
		ExecutorKey: "",
		CategoryID:  nullInt64(categoryID),
		IsPublic:    false,
		CreatedBy:   nullInt64(&in.CreatorID),
	})
	if err != nil {
		return nil, nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, nil, err
	}

	// 技能配置 lands in default_config. Skipped when empty so a newly created
	// agent keeps a NULL column instead of an empty JSON document.
	if len(skills) > 0 {
		if err := s.applySkillsTx(ctx, tx, id, skills); err != nil {
			return nil, nil, err
		}
	}

	if kind != "chat" {
		if _, err := tx.ExecContext(ctx, "UPDATE applications SET enabled = 0 WHERE id = ?", id); err != nil {
			return nil, nil, err
		}
	}

	if in.Runtime != nil {
		b := bindingFromInput(id, in.Runtime)
		// Record the provider FK (评测 P1): without it the join-based
		// provider check has nothing to match and the kill switch fails
		// open for API-created bindings.
		if provider != nil {
			pid := provider.ID
			b.ProviderID = &pid
		}
		caps := adapterCapabilities(adapter)
		bid, err := UpsertBindingTx(ctx, tx, b, caps)
		if err != nil {
			return nil, nil, err
		}
		b.ID = bid
		binding = b
	}

	if in.SetDefaultAgent {
		// 三次复审 P0-R1: a create that also asks for the default flag goes
		// through the SAME transactional eligibility gate as every other
		// path — a chat app without a runtime, or with an inactive provider,
		// is refused (and the whole create rolls back).
		if err := promoteDefaultIfEligibleTx(ctx, tx, id); err != nil {
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
//
// `skills` is a POINTER so "not mentioned" and "clear it" stay distinguishable:
// nil leaves the stored 技能配置 alone (a rename must not wipe it), while a
// pointer to an empty slice clears it. Encoding that as `[]Skill` alone would
// silently erase skills on every unrelated PATCH.
func (s *Service) Update(ctx context.Context, appID, callerID int64, isStaff bool, name, description, icon, color *string, isPublic, enabled *bool, categorySlug, categoryName *string, runtime *BindingInput, setDefaultAgent *bool, skills *[]Skill) (*Application, *Binding, error) {
	app, err := s.ApplicationByID(ctx, appID)
	if err != nil {
		return nil, nil, err
	}
	if !canManage(app, callerID, isStaff) {
		return nil, nil, ErrNotManageable
	}

	var adapter RuntimeAdapter
	var provider *Provider
	if runtime != nil {
		a, p, err := s.validateRuntimeInput(ctx, runtime)
		if err != nil {
			return nil, nil, err
		}
		adapter, provider = a, p
	}

	// Validate the submitted skills BEFORE opening the transaction, so a bad
	// payload cannot leave a half-applied edit behind.
	var normalizedSkills []Skill
	if skills != nil {
		normalized, err := NormalizeSkills(*skills)
		if err != nil {
			return nil, nil, err
		}
		normalizedSkills = normalized
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

	if skills != nil {
		if err := s.applySkillsTx(ctx, tx, appID, normalizedSkills); err != nil {
			return nil, nil, err
		}
	}

	var updatedBinding *Binding
	if runtime != nil {
		b := bindingFromInput(appID, runtime)
		// Keep the provider FK in sync on every binding update (评测 P1).
		if provider != nil {
			pid := provider.ID
			b.ProviderID = &pid
		}
		caps := adapterCapabilities(adapter)
		if _, err := UpsertBindingTx(ctx, tx, b, caps); err != nil {
			return nil, nil, err
		}
		updatedBinding = b
	}

	if enabled != nil {
		if err := s.q(tx).SetApplicationEnabled(ctx, db.SetApplicationEnabledParams{
			Enabled: *enabled,
			ID:      uint64(appID),
		}); err != nil {
			return nil, nil, err
		}
		// 停用解除主智能体 (二次复审 P0-6). Switching an application off
		// must not leave it as the default: the home composer would stay
		// bound to an agent that can never execute, and the flag would
		// become invisible-immovable (the market shows 已停用 but the
		// workspace still opens it). The bootstrap then falls back to the
		// first enabled + bound chat application by itself.
		if !*enabled {
			if _, err := tx.ExecContext(ctx,
				`UPDATE applications SET is_default_agent = 0 WHERE id = ?`, appID); err != nil {
				return nil, nil, err
			}
		}
	}

	if setDefaultAgent != nil {
		if *setDefaultAgent {
			// 三次复审 P0-R1: promoteDefaultIfEligibleTx re-reads the FINAL
			// in-transaction state, so a PATCH that disabled the application
			// above can no longer re-promote it in the same request — the
			// `enabled=false + set_default_agent=true` combination is refused
			// and the WHOLE patch (including the disable) rolls back.
			if err := promoteDefaultIfEligibleTx(ctx, tx, appID); err != nil {
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

// applySkillsTx merges the 技能配置 into `default_config` inside the caller's
// transaction.
//
// The read is a FOR UPDATE, so it is a real read-modify-write rather than a
// blind overwrite: this column is shared with a legacy editor that parked
// `guided_entry_prompt_key` there, and saving skills must not take it with
// them. Doing the whole thing in the caller's transaction is what makes the
// read meaningful — outside one, FOR UPDATE would not hold the row.
func (s *Service) applySkillsTx(ctx context.Context, tx *sql.Tx, appID int64, skills []Skill) error {
	q := s.q(tx)
	raw, err := q.GetApplicationDefaultConfigForUpdate(ctx, uint64(appID))
	if err != nil {
		return err
	}
	merged, err := MergeSkills(raw, skills)
	if err != nil {
		return err
	}
	return q.UpdateApplicationDefaultConfig(ctx, db.UpdateApplicationDefaultConfigParams{
		DefaultConfig: merged,
		ID:            uint64(appID),
	})
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
	if _, err := tx.ExecContext(ctx, `DELETE FROM application_department_grants WHERE application_id = ?`, appID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM application_user_grants WHERE application_id = ?`, appID); err != nil {
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

// SetDefaultAgent promotes the application through the ONE transactional
// eligibility gate (三次复审 P0-R1): promoteDefaultIfEligibleTx re-reads the
// final in-transaction state and enforces enabled + chat + current enabled
// binding + live provider — the same predicate `Consumable` applies to
// consumption and `AuthorizeExecution` to runs. The flag is globally unique
// and enforced transactionally (no conditional unique indexes in MySQL); a
// failed promotion rolls back before the previous default was cleared.
func (s *Service) SetDefaultAgent(ctx context.Context, appID, callerID int64, isStaff bool) error {
	app, err := s.ApplicationByID(ctx, appID)
	if err != nil {
		return err
	}
	if !canManage(app, callerID, isStaff) {
		return ErrNotManageable
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := promoteDefaultIfEligibleTx(ctx, tx, appID); err != nil {
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

// Favorite / Unfavorite are idempotent — and (三次复审 P0-R4.3) no longer an
// existence oracle. The handler used to 404 only on a missing row, so
// `POST /applications/<hidden-real-id>/favorite` answered 200 while an
// unknown id answered 404, enumerating hidden applications one probe at a
// time. Both now resolve the row first and apply the SAME visibility policy
// as every other by-id surface; anything invisible answers exactly like a
// missing row (opaque ErrNotFound → 404), and a DB failure propagates as an
// infrastructure error (never a fake 404, P1-R1).
//
// A fixed application keeps its favorite relation: the ✩ flip is a real
// user-facing feature (P1-5), it just never enters the 收藏智能体 group.
func (s *Service) Favorite(ctx context.Context, appID, userID int64, isStaff bool) error {
	allowed, err := s.repo().AccessAllowed(ctx, appID, userID, isStaff)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrNotFound
	}
	_, err = s.DB.ExecContext(ctx, `INSERT IGNORE INTO application_favorites (user_id, application_id) VALUES (?, ?)`, userID, appID)
	return err
}

func (s *Service) Unfavorite(ctx context.Context, appID, userID int64, isStaff bool) error {
	allowed, err := s.repo().AccessAllowed(ctx, appID, userID, isStaff)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrNotFound
	}
	_, err = s.DB.ExecContext(ctx, `DELETE FROM application_favorites WHERE user_id = ? AND application_id = ?`, userID, appID)
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

// promoteDefaultIfEligibleTx is the ONLY way business code may promote a
// default agent (三次复审 P0-R1). The bare two-UPDATE helper it replaces ran
// from Create() and Update() with NO eligibility check, so
// `POST {set_default_agent:true}` without a runtime, and
// `PATCH {enabled:false, set_default_agent:true}` — which cleared the flag
// first and promoted right after — both produced a default agent the
// composer could never run, and the dedicated SetDefaultAgent endpoint's
// checks were trivially bypassed.
//
// Every requirement is evaluated against the application's FINAL
// in-transaction state (the row is read FOR UPDATE inside the caller's
// transaction), so an enable-toggle earlier in the same PATCH is visible
// here:
//
//	enabled = 1                      the kill switch is absolute
//	kind = 'chat'                    only the composer's kind is defaultable
//	current enabled binding          the NEWEST enabled binding wins — the
//	                                 same choice GetEnabledBinding, the
//	                                 catalog page anti-join and the bootstrap
//	                                 groups make, not "some old binding"
//	provider exists AND active       AuthorizeExecution fails closed on both
//
// Any miss returns ErrNotDefaultable, which ROLLS BACK the caller's whole
// transaction — a failed promotion must never clear the previous default
// (the clear-and-set below only runs once everything above passed).
func promoteDefaultIfEligibleTx(ctx context.Context, tx *sql.Tx, appID int64) error {
	var enabled bool
	var kind string
	err := tx.QueryRowContext(ctx,
		`SELECT enabled, kind FROM applications WHERE id = ? FOR UPDATE`, appID,
	).Scan(&enabled, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotDefaultable
	}
	if err != nil {
		return err
	}
	if !enabled || kind != "chat" {
		return ErrNotDefaultable
	}
	var bindingID int64
	var providerKey string
	err = tx.QueryRowContext(ctx,
		`SELECT id, provider_key FROM runtime_bindings
         WHERE application_id = ? AND enabled = 1
         ORDER BY id DESC LIMIT 1`, appID,
	).Scan(&bindingID, &providerKey)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotDefaultable
	}
	if err != nil {
		return err
	}
	var status string
	err = tx.QueryRowContext(ctx,
		`SELECT status FROM providers WHERE provider_key = ?`, providerKey,
	).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotDefaultable
	}
	if err != nil {
		return err
	}
	if status != "active" {
		return ErrNotDefaultable
	}
	if _, err := tx.ExecContext(ctx, `UPDATE applications SET is_default_agent = 0 WHERE is_default_agent = 1`); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE applications SET is_default_agent = 1 WHERE id = ?`, appID)
	return err
}

func canManage(app *Application, callerID int64, isStaff bool) bool {
	return isStaff
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
