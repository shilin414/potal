package http

import (
	genapi "github.com/creation-agent-studio/backend-go/internal/gen/api"
)

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"bytes"
	"database/sql"

	"github.com/creation-agent-studio/backend-go/internal/catalog"
	"github.com/creation-agent-studio/backend-go/internal/platform/storage"
)

// ───────────────────────────────────────────── application payloads ──

// applicationListItem mirrors GET /api/v2/applications (24 keys, exact).
type applicationListItem struct {
	ID                 int64          `json:"id"`
	Slug               string         `json:"slug"`
	Name               string         `json:"name"`
	Description        string         `json:"description"`
	Icon               string         `json:"icon"`
	AvatarURL          string         `json:"avatar_url"`
	Color              string         `json:"color"`
	Kind               string         `json:"kind"`
	RendererKey        string         `json:"renderer_key"`
	ExecutorKey        string         `json:"executor_key"`
	CategorySlug       string         `json:"category_slug"`
	CategoryName       string         `json:"category_name"`
	IsPublic           bool           `json:"is_public"`
	Enabled            bool           `json:"enabled"`
	RuntimeType        string         `json:"runtime_type"`
	ProviderKey        string         `json:"provider_key"`
	ExternalResourceID string         `json:"external_resource_id"`
	IdentityMode       string         `json:"identity_mode"`
	ExecutionMode      string         `json:"execution_mode"`
	Capabilities       map[string]any `json:"capabilities"`
	IsBound            bool           `json:"is_bound"`
	IsFavorite         bool           `json:"is_favorite"`
	IsDefaultAgent     bool           `json:"is_default_agent"`
	CanManage          bool           `json:"can_manage"`
	UsageCount         int64          `json:"usage_count"`
	LastUsedAt         *string        `json:"last_used_at"`
	GlobalUsageCount   int64          `json:"global_usage_count"`
	// 技能配置 (agent-scoped prompt prefixes). Always present — an agent with
	// no skills sends `[]`, never `null`, so the mobile skill sheet needs no
	// null-guard and can distinguish "not loaded" from "none configured".
	Skills []catalog.Skill `json:"skills"`
}

// applicationDetail mirrors the compact authoring shape (18 keys).
type applicationDetail struct {
	ID                 int64  `json:"id"`
	Slug               string `json:"slug"`
	Name               string `json:"name"`
	Description        string `json:"description"`
	Icon               string `json:"icon"`
	AvatarURL          string `json:"avatar_url"`
	Color              string `json:"color"`
	Kind               string `json:"kind"`
	IsPublic           bool   `json:"is_public"`
	Enabled            bool   `json:"enabled"`
	CategorySlug       string `json:"category_slug"`
	CategoryName       string `json:"category_name"`
	IsDefaultAgent     bool   `json:"is_default_agent"`
	IsBound            bool   `json:"is_bound"`
	RuntimeType        string `json:"runtime_type"`
	ProviderKey        string `json:"provider_key"`
	ExternalResourceID string `json:"external_resource_id"`
	IdentityMode       string `json:"identity_mode"`
	ExecutionMode      string `json:"execution_mode"`
	CanManage          bool   `json:"can_manage"`
	UpdatedAt          string `json:"updated_at"`
	// 技能配置 — see applicationListItem.Skills.
	Skills []catalog.Skill `json:"skills"`
}

// avatarVersion derives the cache-busting version of an avatar from its
// storage key alone (执行报告 §9.1).
//
// The previous version was `app.UpdatedAt.Unix()`, which broke caching for
// EVERY avatar on EVERY unrelated write: `updated_at` is
// `ON UPDATE CURRENT_TIMESTAMP(3)`, and each run bumps `usage_count`, so
// chatting with an agent changed its avatar URL and re-downloaded the image
// (identical bytes) on the next render. The storage key
// `application-avatars/{appID}/{UnixNano}.{ext}` is minted once per upload and
// never changes afterwards, so a sha256 fingerprint of it changes ONLY when
// the avatar is uploaded / cleared / re-uploaded — the correct semantics.
// Chatting, usage_count bumps, favourites, renames, category edits, runtime
// changes, enable/public toggles and default-agent promotions all keep the
// same URL, letting the browser serve from cache.
func avatarVersion(avatarKey string) string {
	sum := sha256.Sum256([]byte(avatarKey))
	return hex.EncodeToString(sum[:8])
}

func avatarURL(app *catalog.Application) string {
	if app.AvatarKey == "" {
		return ""
	}
	return fmt.Sprintf("/api/v2/applications/%d/avatar?v=%s", app.ID, avatarVersion(app.AvatarKey))
}

func (s *Server) appDetail(app *catalog.Application, binding *catalog.Binding, caller *AuthenticatedUser) applicationDetail {
	manage := canManageCaller(app, caller)
	d := applicationDetail{
		ID:             app.ID,
		Slug:           app.Slug,
		Name:           app.Name,
		Description:    app.Description,
		Icon:           app.Icon,
		AvatarURL:      avatarURL(app),
		Color:          app.Color,
		Kind:           app.Kind,
		IsPublic:       app.IsPublic,
		Enabled:        app.Enabled,
		CategorySlug:   app.CategorySlug,
		CategoryName:   app.CategoryName,
		IsDefaultAgent: app.IsDefaultAgent,
		CanManage:      manage,
		UpdatedAt:      app.UpdatedAt.UTC().Format(time.RFC3339),
		Skills:         app.Skills,
	}
	if binding != nil && binding.Enabled {
		d.IsBound = true
		d.RuntimeType = binding.RuntimeType
		d.ProviderKey = binding.ProviderKey
		d.ExternalResourceID = binding.ExternalResourceID
		d.IdentityMode = binding.IdentityMode
		d.ExecutionMode = binding.ExecutionMode
	}
	return d
}

func canManageCaller(app *catalog.Application, caller *AuthenticatedUser) bool {
	if caller == nil {
		return false
	}
	if caller.IsStaff {
		return true
	}
	return app.CreatedBy != nil && *app.CreatedBy == caller.ID
}

// ListApplications implements the exact list semantics (scope/kind/
// include_unbound + personal usage aggregation).
func (s *Server) ListApplications(w http.ResponseWriter, r *http.Request, params genapi.ListApplicationsParams) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	ctx := r.Context()

	kind := "chat"
	if params.Kind != nil {
		k := string(*params.Kind)
		switch k {
		case "chat", "task", "custom", "all":
			kind = k
		}
	}
	scope := "public"
	if params.Scope != nil {
		sc := string(*params.Scope)
		switch sc {
		case "public", "mine", "manage":
			scope = sc
		}
	}
	includeUnbound := params.IncludeUnbound != nil &&
		isTruthy(*params.IncludeUnbound)

	providers := map[int64]*catalog.Provider{}
	if provs, err := s.CatalogRepo.ListActiveProviders(ctx); err == nil {
		for i := range provs {
			providers[provs[i].ID] = &provs[i]
		}
	}

	// personal usage per application (own runs only) — the legacy whole-list
	// endpoint keeps its user-wide aggregation until callers migrate to the
	// paged endpoint (which aggregates only the page ids, §20).
	usage := map[int64]pageUsage{}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT application_id, COUNT(*) AS n, MAX(created_at) AS last_used FROM runs WHERE user_id = ? AND application_id IS NOT NULL GROUP BY application_id`,
		caller.ID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var appID, n int64
			var last sql.NullTime
			if err := rows.Scan(&appID, &n, &last); err == nil {
				entry := pageUsage{Count: n}
				if last.Valid {
					t := last.Time.UTC().Format(time.RFC3339)
					entry.Last = &t
				}
				usage[appID] = entry
			}
		}
	}

	favorites := map[int64]bool{}
	if favRows, err := s.DB.QueryContext(ctx,
		`SELECT application_id FROM application_favorites WHERE user_id = ?`, caller.ID); err == nil {
		defer favRows.Close()
		for favRows.Next() {
			var appID int64
			if favRows.Scan(&appID) == nil {
				favorites[appID] = true
			}
		}
	}

	out := []applicationListItem{}
	seenBound := map[int64]bool{}
	if apps, err := s.CatalogRepo.ListApplicationsWithBindings(ctx, scope, caller.ID, caller.IsStaff); err == nil {
		for _, item := range apps {
			if seenBound[item.App.ID] {
				continue
			}
			seenBound[item.App.ID] = true
			if kind != "all" && item.App.Kind != kind {
				continue
			}
			out = append(out, s.buildListItem(item, providers, favorites, usage, caller))
		}
	}
	// "Extras" (unbound apps) follow the reference semantics exactly:
	//   kind=chat        → only with include_unbound
	//   kind=task/custom → always
	//   kind=all         → non-chat always, chat only with include_unbound
	extras := false
	switch kind {
	case "task", "custom":
		extras = true
	case "all":
		extras = true
	default: // chat
		extras = includeUnbound
	}
	if extras {
		if apps, err := s.CatalogRepo.ListUnboundApplications(ctx, scope, kind, caller.ID, caller.IsStaff); err == nil {
			for _, item := range apps {
				if seenBound[item.App.ID] {
					continue
				}
				if kind == "all" && item.App.Kind == "chat" && !includeUnbound {
					continue
				}
				seenBound[item.App.ID] = true
				out = append(out, s.buildListItem(item, providers, favorites, usage, caller))
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func isTruthy(v string) bool {
	switch strings.ToLower(v) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// ────────────────────────────── keyset-paginated catalog (执行报告 §14–§17) ──

// pageUsage / pageFavorites are the per-page aggregates (§20): only the ids
// on the returned page are ever queried.
type pageUsage struct {
	Count int64
	Last  *string
}

func (s *Server) ListApplicationPage(w http.ResponseWriter, r *http.Request, params genapi.ListApplicationPageParams) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	ctx := r.Context()

	kind := catalog.PageQueryKindChat
	if params.Kind != nil {
		switch k := string(*params.Kind); k {
		case catalog.PageQueryKindChat, catalog.PageQueryKindFixed, catalog.PageQueryKindAll:
			kind = k
		}
	}
	scope := "public"
	if params.Scope != nil {
		switch sc := string(*params.Scope); sc {
		case "public", "mine", "manage":
			scope = sc
		}
	}
	includeUnbound := params.IncludeUnbound != nil && isTruthy(*params.IncludeUnbound)
	search := ""
	if params.Q != nil {
		search = strings.TrimSpace(*params.Q)
	}
	category := ""
	if params.CategorySlug != nil {
		category = strings.TrimSpace(*params.CategorySlug)
	}
	limit := 24
	if params.Limit != nil && *params.Limit > 0 {
		limit = *params.Limit
		if limit > 100 {
			limit = 100
		}
	}

	var cursorCreated time.Time
	var cursorID int64
	if params.Cursor != nil && strings.TrimSpace(*params.Cursor) != "" {
		t, id, err := catalog.DecodePageCursor(*params.Cursor)
		if err != nil {
			writeDetail(w, http.StatusBadRequest, "invalid cursor")
			return
		}
		cursorCreated, cursorID = t, id
	}

	// limit+1 probe: has_more without a COUNT(*) (§15).
	page, err := s.CatalogRepo.ListApplicationPage(ctx, catalog.ApplicationPageQuery{
		Scope:           scope,
		Kind:            kind,
		IncludeUnbound:  includeUnbound,
		Search:          search,
		CategorySlug:    category,
		Limit:           limit + 1,
		CursorCreatedAt: cursorCreated,
		CursorID:        cursorID,
		CallerID:        caller.ID,
		IsStaff:         caller.IsStaff,
	})
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	hasMore := len(page) > limit
	if hasMore {
		page = page[:limit]
	}
	nextCursor := ""
	if hasMore && len(page) > 0 {
		last := page[len(page)-1]
		nextCursor = catalog.EncodePageCursor(last.App.CreatedAt, last.App.ID)
	}

	appIDs := make([]int64, 0, len(page))
	for _, item := range page {
		appIDs = append(appIDs, item.App.ID)
	}
	usage, err := s.CatalogRepo.UsageByApplications(ctx, caller.ID, appIDs)
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	favorites, err := s.CatalogRepo.FavoritesByApplications(ctx, caller.ID, appIDs)
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}

	providers := map[int64]*catalog.Provider{}
	if provs, err := s.CatalogRepo.ListActiveProviders(ctx); err == nil {
		for i := range provs {
			providers[provs[i].ID] = &provs[i]
		}
	}

	favMap := map[int64]bool{}
	for id, fav := range favorites {
		favMap[id] = fav
	}
	usageMap := map[int64]pageUsage{}
	for id, u := range usage {
		entry := pageUsage{Count: u.Count}
		if u.Last != nil {
			t := u.Last.UTC().Format(time.RFC3339)
			entry.Last = &t
		}
		usageMap[id] = entry
	}

	items := make([]applicationListItem, 0, len(page))
	for _, item := range page {
		items = append(items, s.buildListItem(item, providers, favMap, usageMap, caller))
	}
	// Hand-rolled response (same pattern as ListApplications): the item shape
	// is the shared applicationListItem, not the generated model struct.
	writeJSON(w, http.StatusOK, struct {
		Items      []applicationListItem `json:"items"`
		NextCursor string                `json:"next_cursor"`
		HasMore    bool                  `json:"has_more"`
	}{Items: items, NextCursor: nextCursor, HasMore: hasMore})
}

func (s *Server) buildListItem(item catalog.ApplicationWithBinding, providers map[int64]*catalog.Provider,
	favorites map[int64]bool, usage map[int64]pageUsage, caller *AuthenticatedUser) applicationListItem {
	app := item.App
	capsAny := map[string]any{}
	var rt, pk, ext, im, em string
	isBound := false
	if item.Binding != nil && item.Binding.Enabled {
		isBound = true
		rt = item.Binding.RuntimeType
		pk = item.Binding.ProviderKey
		ext = item.Binding.ExternalResourceID
		im = item.Binding.IdentityMode
		em = item.Binding.ExecutionMode
		var provID int64
		if item.Binding.ProviderID != nil {
			provID = *item.Binding.ProviderID
		}
		boolCaps := item.Binding.EffectiveCapabilities(providers[provID])
		capsAny = map[string]any{}
		for k, v := range boolCaps {
			capsAny[k] = v
		}
	}
	entry := usage[app.ID]
	disp := ""
	if entry.Last != nil {
		disp = *entry.Last
	}
	return applicationListItem{
		ID: app.ID, Slug: app.Slug, Name: app.Name, Description: app.Description,
		Icon: app.Icon, AvatarURL: avatarURL(app), Color: app.Color,
		Kind: app.Kind, RendererKey: app.RendererKey, ExecutorKey: app.ExecutorKey,
		CategorySlug: app.CategorySlug, CategoryName: app.CategoryName,
		IsPublic:    app.IsPublic,
		Enabled:     app.Enabled,
		RuntimeType: rt, ProviderKey: pk, ExternalResourceID: ext,
		IdentityMode: im, ExecutionMode: em,
		Capabilities: capsAny, IsBound: isBound,
		IsFavorite:     favorites[app.ID],
		IsDefaultAgent: app.IsDefaultAgent,
		CanManage:      canManageCaller(app, caller),
		UsageCount:     entry.Count, LastUsedAt: dispTime(disp),
		GlobalUsageCount: app.UsageCount,
		Skills:           app.Skills,
	}
}

func dispTime(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (s *Server) GetApplication(w http.ResponseWriter, r *http.Request, id genapi.ApplicationId) {
	app, err := s.CatalogRepo.ApplicationByID(r.Context(), int64(id))
	if err != nil {
		writeDetail(w, http.StatusNotFound, "application not found")
		return
	}
	binding, _ := s.CatalogRepo.EnabledBinding(r.Context(), int64(id))
	writeJSON(w, http.StatusOK, s.appDetail(app, binding, userFrom(r.Context())))
}

func (s *Server) CreateApplication(w http.ResponseWriter, r *http.Request) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	// 智能体接入是管理员能力：普通用户只消费市场，不允许新建。
	if !caller.IsStaff {
		writeDetail(w, http.StatusForbidden, "只有管理员可以添加智能体，如需接入请联系平台管理员。")
		return
	}
	var body struct {
		Name            string          `json:"name"`
		Slug            string          `json:"slug"`
		Description     string          `json:"description"`
		Icon            string          `json:"icon"`
		Color           string          `json:"color"`
		CategorySlug    string          `json:"category_slug"`
		CategoryName    string          `json:"category_name"`
		IsPublic        *bool           `json:"is_public"`
		Kind            string          `json:"kind"`
		RendererKey     string          `json:"renderer_key"`
		Runtime         *runtimeInput   `json:"runtime"`
		SetDefaultAgent bool            `json:"set_default_agent"`
		Skills          []catalog.Skill `json:"skills"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeFieldErrors(w, map[string][]string{"body": {"invalid json"}})
		return
	}
	in := &catalog.CreateInput{
		Name: body.Name, Slug: body.Slug, Description: body.Description,
		Icon: body.Icon, Color: body.Color,
		CategorySlug: body.CategorySlug, CategoryName: body.CategoryName,
		IsPublic: body.IsPublic == nil || *body.IsPublic,
		Kind:     body.Kind, RendererKey: body.RendererKey,
		SetDefaultAgent: body.SetDefaultAgent,
		Skills:          body.Skills,
		CreatorID:       caller.ID,
		IsStaff:         caller.IsStaff,
	}
	if body.Runtime != nil {
		in.Runtime = body.Runtime.toCatalog()
	}
	app, binding, err := s.Catalog.Create(r.Context(), in)
	if err != nil {
		s.writeCatalogError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.appDetail(app, binding, caller))
}

type runtimeInput struct {
	ProviderKey        string         `json:"provider_key"`
	RuntimeType        string         `json:"runtime_type"`
	ExternalResourceID string         `json:"external_resource_id"`
	IdentityMode       string         `json:"identity_mode"`
	ExecutionMode      string         `json:"execution_mode"`
	SessionPolicy      string         `json:"session_policy"`
	ArtifactPolicy     string         `json:"artifact_policy"`
	TimeoutSeconds     int64          `json:"timeout_seconds"`
	Config             map[string]any `json:"config"`
}

func (r *runtimeInput) toCatalog() *catalog.BindingInput {
	return &catalog.BindingInput{
		ProviderKey:        r.ProviderKey,
		RuntimeType:        r.RuntimeType,
		ExternalResourceID: r.ExternalResourceID,
		IdentityMode:       r.IdentityMode,
		ExecutionMode:      r.ExecutionMode,
		SessionPolicy:      r.SessionPolicy,
		ArtifactPolicy:     r.ArtifactPolicy,
		TimeoutSeconds:     r.TimeoutSeconds,
		Config:             r.Config,
	}
}

func (s *Server) UpdateApplication(w http.ResponseWriter, r *http.Request, id genapi.ApplicationId) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	var body struct {
		Name            *string          `json:"name"`
		Description     *string          `json:"description"`
		Icon            *string          `json:"icon"`
		Color           *string          `json:"color"`
		IsPublic        *bool            `json:"is_public"`
		Enabled         *bool            `json:"enabled"`
		CategorySlug    *string          `json:"category_slug"`
		CategoryName    *string          `json:"category_name"`
		Runtime         *runtimeInput    `json:"runtime"`
		SetDefaultAgent *bool            `json:"set_default_agent"`
		Skills          *[]catalog.Skill `json:"skills"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeFieldErrors(w, map[string][]string{"body": {"invalid json"}})
		return
	}
	var runtime *catalog.BindingInput
	if body.Runtime != nil {
		runtime = body.Runtime.toCatalog()
	}
	app, binding, err := s.Catalog.Update(r.Context(), int64(id), caller.ID, caller.IsStaff,
		body.Name, body.Description, body.Icon, body.Color, body.IsPublic, body.Enabled,
		body.CategorySlug, body.CategoryName, runtime, body.SetDefaultAgent, body.Skills)
	if err != nil {
		s.writeCatalogError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.appDetail(app, binding, caller))
}

func (s *Server) DeleteApplication(w http.ResponseWriter, r *http.Request, id genapi.ApplicationId) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	if err := s.Catalog.Delete(r.Context(), int64(id), caller.ID, caller.IsStaff); err != nil {
		s.writeCatalogError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) writeCatalogError(w http.ResponseWriter, err error) {
	switch {
	case err == catalog.ErrNameRequired:
		writeFieldErrors(w, map[string][]string{"name": {err.Error()}})
	case err == catalog.ErrBadSlug:
		writeFieldErrors(w, map[string][]string{"slug": {err.Error()}})
	case err == catalog.ErrSlugTaken:
		writeFieldErrors(w, map[string][]string{"slug": {err.Error()}})
	case err == catalog.ErrOnlyChatRenderer:
		writeFieldErrors(w, map[string][]string{"renderer_key": {err.Error()}})
	case err == catalog.ErrNotManageable:
		writeDetail(w, http.StatusForbidden, err.Error())
	case err == catalog.ErrNotDefaultable:
		writeFieldErrors(w, map[string][]string{"detail": {err.Error()}})
	case err == catalog.ErrDeleteReferenced:
		writeDetail(w, http.StatusConflict, err.Error())
	case errors.Is(err, catalog.ErrInvalidSkill):
		// A malformed 技能配置 is the caller's payload, not a server fault:
		// surface it on the skills field (the message names the offending
		// entry) instead of a generic 500.
		writeFieldErrors(w, map[string][]string{"skills": {err.Error()}})
	case err == catalog.ErrNoBinding:
		writeBare(w, http.StatusBadRequest, err.Error())
	default:
		msg := err.Error()
		if strings.Contains(msg, "：") && (strings.Contains(msg, "提供方") || strings.Contains(msg, "运行时")) {
			// provider/runtime field errors
			field := "provider_key"
			if strings.Contains(msg, "运行时") {
				field = "runtime_type"
			}
			writeFieldErrors(w, map[string][]string{field: {msg}})
			return
		}
		if strings.Contains(msg, "不能为空") || strings.Contains(msg, "格式不正确") {
			writeFieldErrors(w, map[string][]string{"external_resource_id": {msg}})
			return
		}
		writeSimpleError(w, http.StatusInternalServerError, msg)
	}
}

// ────────────────────────────────────────────────── favorite / default ──

func (s *Server) FavoriteApplication(w http.ResponseWriter, r *http.Request, id genapi.ApplicationId) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	if err := s.Catalog.Favorite(r.Context(), int64(id), caller.ID); err != nil {
		writeDetail(w, http.StatusNotFound, "application not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"application_id": int64(id), "is_favorite": true})
}

func (s *Server) UnfavoriteApplication(w http.ResponseWriter, r *http.Request, id genapi.ApplicationId) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	if err := s.Catalog.Unfavorite(r.Context(), int64(id), caller.ID); err != nil {
		writeDetail(w, http.StatusNotFound, "application not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"application_id": int64(id), "is_favorite": false})
}

func (s *Server) SetDefaultAgent(w http.ResponseWriter, r *http.Request, id genapi.ApplicationId) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	if err := s.Catalog.SetDefaultAgent(r.Context(), int64(id), caller.ID, caller.IsStaff); err != nil {
		s.writeCatalogError(w, err)
		return
	}
	app, _ := s.CatalogRepo.ApplicationByID(r.Context(), int64(id))
	binding, _ := s.CatalogRepo.EnabledBinding(r.Context(), int64(id))
	writeJSON(w, http.StatusOK, s.appDetail(app, binding, caller))
}

func (s *Server) UnsetDefaultAgent(w http.ResponseWriter, r *http.Request, id genapi.ApplicationId) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	if err := s.Catalog.UnsetDefaultAgent(r.Context(), int64(id), caller.ID, caller.IsStaff); err != nil {
		s.writeCatalogError(w, err)
		return
	}
	app, _ := s.CatalogRepo.ApplicationByID(r.Context(), int64(id))
	binding, _ := s.CatalogRepo.EnabledBinding(r.Context(), int64(id))
	writeJSON(w, http.StatusOK, s.appDetail(app, binding, caller))
}

// ───────────────────────────────────────────────────────────── avatar ──

var allowedAvatarExts = map[string]bool{"png": true, "jpg": true, "jpeg": true, "gif": true, "webp": true}

const avatarMaxBytes = 2 * 1024 * 1024

func (s *Server) GetApplicationAvatar(w http.ResponseWriter, r *http.Request, id genapi.ApplicationId) {
	app, err := s.CatalogRepo.ApplicationByID(r.Context(), int64(id))
	if err != nil || app.AvatarKey == "" {
		writeDetail(w, http.StatusNotFound, "avatar not set")
		return
	}
	// Long-lived conditional caching (执行报告 §10): the URL is versioned by
	// the avatar's storage key, so an unchanged avatar keeps the same URL and
	// may be cached for a year. The ETag supports legacy URLs without the
	// version parameter (or a proxy stripping it) via If-None-Match → 304.
	version := avatarVersion(app.AvatarKey)
	etag := `"` + version + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	if matchesETag(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	rc, _, err := s.Storage.Open(r.Context(), app.AvatarKey)
	if err != nil {
		writeDetail(w, http.StatusNotFound, "avatar not set")
		return
	}
	defer rc.Close()
	ct := mime.TypeByExtension(filepath.Ext(app.AvatarKey))
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	_, _ = io.Copy(w, rc)
}

// matchesETag reports whether an If-None-Match header covers the current
// etag. Deliberately lenient (list / W/-prefixed / weak comparison): the
// value is a server-minted 16-hex fingerprint, so a false positive would
// require a sha256 collision on the same storage key.
func matchesETag(ifNoneMatch, etag string) bool {
	if ifNoneMatch == "" {
		return false
	}
	if strings.TrimSpace(ifNoneMatch) == "*" {
		return true
	}
	for _, candidate := range strings.Split(ifNoneMatch, ",") {
		if strings.TrimSpace(candidate) == etag {
			return true
		}
	}
	return false
}

func (s *Server) UploadApplicationAvatar(w http.ResponseWriter, r *http.Request, id genapi.ApplicationId) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	app, err := s.CatalogRepo.ApplicationByID(r.Context(), int64(id))
	if err != nil {
		writeDetail(w, http.StatusNotFound, "application not found")
		return
	}
	if !canManageCaller(app, caller) {
		writeDetail(w, http.StatusForbidden, catalog.ErrNotManageable.Error())
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeFieldErrors(w, map[string][]string{"file": {"请选择头像文件"}})
		return
	}
	defer file.Close()
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(header.Filename), "."))
	if !allowedAvatarExts[ext] {
		writeFieldErrors(w, map[string][]string{"file": {"头像仅支持 gif / jpeg / jpg / png / webp 格式"}})
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, avatarMaxBytes+1))
	if err != nil || len(data) > avatarMaxBytes {
		writeFieldErrors(w, map[string][]string{"file": {"头像不能超过 2MB"}})
		return
	}
	key, err := storage.SanitizeKey(fmt.Sprintf("application-avatars/%d/%d%s", app.ID, time.Now().UnixNano(), "."+ext))
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := s.Storage.Put(r.Context(), key, bytes.NewReader(data), mime.TypeByExtension("."+ext)); err != nil {
		writeSimpleError(w, http.StatusInternalServerError, "avatar upload failed")
		return
	}
	old := app.AvatarKey
	if _, err := s.DB.ExecContext(r.Context(), `UPDATE applications SET avatar_key = ? WHERE id = ?`, key, app.ID); err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if old != "" && old != key {
		_ = s.Storage.Delete(r.Context(), old)
	}
	updated, _ := s.CatalogRepo.ApplicationByID(r.Context(), int64(id))
	binding, _ := s.CatalogRepo.EnabledBinding(r.Context(), int64(id))
	writeJSON(w, http.StatusOK, s.appDetail(updated, binding, caller))
}

func (s *Server) ClearApplicationAvatar(w http.ResponseWriter, r *http.Request, id genapi.ApplicationId) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	app, err := s.CatalogRepo.ApplicationByID(r.Context(), int64(id))
	if err != nil {
		writeDetail(w, http.StatusNotFound, "application not found")
		return
	}
	if !canManageCaller(app, caller) {
		writeDetail(w, http.StatusForbidden, catalog.ErrNotManageable.Error())
		return
	}
	if app.AvatarKey != "" {
		if _, err := s.DB.ExecContext(r.Context(), `UPDATE applications SET avatar_key = '' WHERE id = ?`, app.ID); err != nil {
			writeSimpleError(w, http.StatusInternalServerError, err.Error())
			return
		}
		_ = s.Storage.Delete(r.Context(), app.AvatarKey)
	}
	updated, _ := s.CatalogRepo.ApplicationByID(r.Context(), int64(id))
	binding, _ := s.CatalogRepo.EnabledBinding(r.Context(), int64(id))
	writeJSON(w, http.StatusOK, s.appDetail(updated, binding, caller))
}

// helpers

func paramToInt(id genapi.ApplicationId) int64 { return int64(id) }
