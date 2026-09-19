package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// FeishuClient speaks the open-platform auth endpoints (OAuth + tokens).
type FeishuClient struct {
	baseURL   string
	appID     string
	appSecret string
	http      *http.Client
}

func NewFeishuClient(baseURL, appID, appSecret string, hc *http.Client) *FeishuClient {
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	return &FeishuClient{
		baseURL:   strings.TrimRight(baseURL, "/"),
		appID:     appID,
		appSecret: appSecret,
		http:      hc,
	}
}

// OAuth scope: offline_access plus every Aily permission the runtime needs
// (validated against the real environment 2026-09-11: without it no
// refresh_token is issued) plus the IM/contacts permissions conversation
// forwarding needs. Every scope here must ALSO be enabled on the Feishu app
// console, or the authorize page rejects the login.
const OAuthScope = "offline_access aily:agent_chat:write aily:agent_chat:read" +
	" aily:agent_attachment:write aily:agent_artifact:read aily:agent_visibility:read" +
	" im:chat:readonly im:message im:message.send_as_user contact:user:search"

// AuthorizeURL builds the OAuth authorize redirect.
func (c *FeishuClient) AuthorizeURL(redirectURI, state string) string {
	q := url.Values{}
	q.Set("client_id", c.appID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("state", state)
	q.Set("scope", OAuthScope)
	return c.baseURL + "/open-apis/authen/v1/authorize?" + q.Encode()
}

type oauthTokenResp struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data *oauthTokenData `json:"data"`
	// Some deployments flatten the fields at top level.
	AccessToken           string `json:"access_token"`
	RefreshToken          string `json:"refresh_token"`
	ExpiresIn             int64  `json:"expires_in"`
	RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"`
	Scope                 string `json:"scope"`
	TokenType             string `json:"token_type"`
}

type oauthTokenData struct {
	AccessToken           string `json:"access_token"`
	RefreshToken          string `json:"refresh_token"`
	ExpiresIn             int64  `json:"expires_in"`
	RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"`
	Scope                 string `json:"scope"`
	TokenType             string `json:"token_type"`
}

// TokenResult carries the OAuth grant outcome.
type TokenResult struct {
	AccessToken           string
	RefreshToken          string
	ExpiresIn             int64
	RefreshTokenExpiresIn int64
}

// ExchangeCode exchanges the authorization code for tokens.
func (c *FeishuClient) ExchangeCode(ctx context.Context, code, redirectURI string) (*TokenResult, error) {
	body := map[string]any{
		"grant_type":    "authorization_code",
		"client_id":     c.appID,
		"client_secret": c.appSecret,
		"code":          code,
		"redirect_uri":  redirectURI,
	}
	raw, err := c.post(ctx, "/open-apis/authen/v2/oauth/token", body)
	if err != nil {
		return nil, err
	}
	var parsed oauthTokenResp
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("feishu: token decode: %w", err)
	}
	if parsed.Code != 0 && parsed.Data == nil {
		return nil, fmt.Errorf("feishu: token exchange failed: %s", parsed.Msg)
	}
	out := &TokenResult{}
	if parsed.Data != nil {
		out.AccessToken = parsed.Data.AccessToken
		out.RefreshToken = parsed.Data.RefreshToken
		out.ExpiresIn = parsed.Data.ExpiresIn
		out.RefreshTokenExpiresIn = parsed.Data.RefreshTokenExpiresIn
	} else {
		out.AccessToken = parsed.AccessToken
		out.RefreshToken = parsed.RefreshToken
		out.ExpiresIn = parsed.ExpiresIn
		out.RefreshTokenExpiresIn = parsed.RefreshTokenExpiresIn
	}
	if out.AccessToken == "" {
		return nil, fmt.Errorf("feishu: token exchange returned no access token")
	}
	return out, nil
}

// RefreshUserToken rotates the refresh token for a new access token.
func (c *FeishuClient) RefreshUserToken(ctx context.Context, refreshToken string) (*TokenResult, error) {
	body := map[string]any{
		"grant_type":    "refresh_token",
		"client_id":     c.appID,
		"client_secret": c.appSecret,
		"refresh_token": refreshToken,
	}
	raw, err := c.post(ctx, "/open-apis/authen/v2/oauth/token", body)
	if err != nil {
		return nil, err
	}
	var parsed oauthTokenResp
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("feishu: refresh decode: %w", err)
	}
	out := &TokenResult{}
	if parsed.Data != nil {
		out.AccessToken = parsed.Data.AccessToken
		out.RefreshToken = parsed.Data.RefreshToken
		out.ExpiresIn = parsed.Data.ExpiresIn
		out.RefreshTokenExpiresIn = parsed.Data.RefreshTokenExpiresIn
	} else {
		out.AccessToken = parsed.AccessToken
		out.RefreshToken = parsed.RefreshToken
		out.ExpiresIn = parsed.ExpiresIn
		out.RefreshTokenExpiresIn = parsed.RefreshTokenExpiresIn
	}
	if out.AccessToken == "" {
		return nil, fmt.Errorf("feishu: refresh returned no access_token")
	}
	return out, nil
}

// GetUserInfo resolves the Feishu identity of a user access token.
func (c *FeishuClient) GetUserInfo(ctx context.Context, userAccessToken string) (*FeishuUserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/open-apis/authen/v1/user_info", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+userAccessToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("feishu: user info: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var envelope struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data *FeishuUserInfo `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("feishu: user info decode: %w", err)
	}
	if envelope.Code != 0 || envelope.Data == nil {
		return nil, fmt.Errorf("feishu: user info failed: %s", envelope.Msg)
	}
	return envelope.Data, nil
}

func (c *FeishuClient) post(ctx context.Context, path string, body any) ([]byte, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("feishu: %s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("feishu: %s: HTTP %d", path, resp.StatusCode)
	}
	return raw, nil
}

// TenantTokenResult carries the app credential.
type TenantTokenResult struct {
	TenantAccessToken string `json:"tenant_access_token"`
	Expire            int64  `json:"expire"`
}

// TenantToken fetches the app TAT (tenant-mode provider bindings only).
func (c *FeishuClient) TenantToken(ctx context.Context) (*TenantTokenResult, error) {
	body := map[string]any{
		"app_id":     c.appID,
		"app_secret": c.appSecret,
	}
	raw, err := c.post(ctx, "/open-apis/auth/v3/tenant_access_token/internal", body)
	if err != nil {
		return nil, err
	}
	var out TenantTokenResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("feishu: tat decode: %w", err)
	}
	if out.TenantAccessToken == "" {
		return nil, fmt.Errorf("feishu: tenant token response missing token")
	}
	return &out, nil
}

// ─────────────────────────────────────────────────────── IM (forwarding) ──
//
// Minimal IM surface for conversation forwarding: list the sender's groups,
// search coworkers, and deliver one message. Targets resolve either against
// the sender's user_access_token (their own Feishu view) or the app's tenant
// token; callers attempt UAT first and fall back to TAT when the app lacks
// the user-mode scope.

// FeishuChat is one group the token holder can address.
type FeishuChat struct {
	ChatID    string
	Name      string
	AvatarURL string
}

// FeishuContactUser is one coworker match from the directory search.
type FeishuContactUser struct {
	OpenID    string
	Name      string
	AvatarURL string
}

// feishuEnvelope is the standard {code, msg, data} wrapper.
type feishuEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// authorizedGET issues a GET with a bearer token and decodes the envelope,
// translating business errors (code != 0) into Go errors.
func (c *FeishuClient) authorizedGET(ctx context.Context, token, path string) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("feishu: %s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var env feishuEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("feishu: %s decode: %w", path, err)
	}
	if env.Code != 0 {
		return nil, fmt.Errorf("feishu: %s: code %d: %s", path, env.Code, env.Msg)
	}
	return env.Data, nil
}

// ListUserChats lists the chats (groups) the token holder belongs to,
// following the provider's page_token continuation until has_more is false.
// The first page alone capped every picker at 100 groups (五次复审 P1-3) —
// chat 101+ could never be selected. maxChatPages is only a defensive bound
// against a misbehaving provider (100 pages × 100 = 10 000 chats).
//
// TRUNCATED != COMPLETE (六次复审 P2-2): reaching the safety limit while the
// provider still reports has_more is an ERROR, not a silent partial list —
// the caller would treat the first 10 000 chats as the complete set. A
// repeated page_token (provider cycling the same continuation) likewise
// fails fast instead of appending the same page forever.
func (c *FeishuClient) ListUserChats(ctx context.Context, token string) ([]FeishuChat, error) {
	const maxChatPages = 100
	var out []FeishuChat
	pageToken := ""
	seenTokens := map[string]struct{}{}
	completed := false
	for page := 0; page < maxChatPages; page++ {
		path := "/open-apis/im/v1/chats?page_size=100"
		if pageToken != "" {
			path += "&page_token=" + url.QueryEscape(pageToken)
		}
		data, err := c.authorizedGET(ctx, token, path)
		if err != nil {
			return nil, err
		}
		var payload struct {
			Items []struct {
				ChatID string          `json:"chat_id"`
				Name   string          `json:"name"`
				Avatar json.RawMessage `json:"avatar"`
			} `json:"items"`
			HasMore   bool   `json:"has_more"`
			PageToken string `json:"page_token"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return nil, fmt.Errorf("feishu: chats decode: %w", err)
		}
		for _, it := range payload.Items {
			out = append(out, FeishuChat{
				ChatID:    it.ChatID,
				Name:      it.Name,
				AvatarURL: avatarFromRaw(it.Avatar),
			})
		}
		if !payload.HasMore {
			completed = true
			break
		}
		// has_more=true + empty page_token (七次复审 P1-1): the provider
		// says another page exists but hands us no way to read it. Treating
		// that as "complete" would silently drop chat 101+ while the caller
		// believes it has the full list — TRUNCATED != COMPLETE, so fail loud.
		if payload.PageToken == "" {
			return nil, fmt.Errorf("feishu: chat has_more without page_token")
		}
		if _, dup := seenTokens[payload.PageToken]; dup {
			return nil, fmt.Errorf("feishu: repeated chat page token %q", payload.PageToken)
		}
		seenTokens[payload.PageToken] = struct{}{}
		pageToken = payload.PageToken
	}
	if !completed {
		return nil, fmt.Errorf("feishu: chat pagination exceeded safety limit (%d pages)", maxChatPages)
	}
	return out, nil
}

// avatarFromRaw tolerates both shapes seen in the wild: a plain URL string
// and the contact-v3 object {avatar_72, avatar_240, ...}.
func avatarFromRaw(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var obj struct {
		Avatar240 string `json:"avatar_240"`
		Avatar72  string `json:"avatar_72"`
		Origin    string `json:"avatar_origin"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return firstNonEmptyStr(obj.Avatar240, obj.Avatar72, obj.Origin)
	}
	return ""
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// FeishuContactUserPage is one page of the search/v1/user results, mirroring
// the official response shape (feishu-doc 通讯录/用户/搜索用户:
// data.{users, has_more, page_token}).
type FeishuContactUserPage struct {
	Users     []FeishuContactUser
	HasMore   bool
	PageToken string
}

// SearchFeishuUsers searches the org directory for coworkers, ONE page per
// call. Requires a user_access_token (the search/v1/user endpoint accepts
// nothing else — tenant-wide search would also decouple "who is visible"
// from "who I can message as myself").
//
// The response is decoded STRICTLY against the official contract:
// data.users / data.has_more / data.page_token (六次复审 P1-2). The old code
// read data.entities — a field that never exists in this response — so every
// search compiled, passed CI and still returned zero contacts. The field
// names below are contract, not suggestion: a fixture test
// (TestSearchFeishuUsersDecodesOfficialUsersShape) pins them to the official
// documentation so "users" can never silently become "entities" again.
// pageSize is clamped to the provider range 1-200; pageToken "" = first page.
func (c *FeishuClient) SearchFeishuUsers(ctx context.Context, token, query string, pageSize int, pageToken string) (*FeishuContactUserPage, error) {
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 200 {
		pageSize = 200
	}
	q := url.Values{}
	q.Set("query", query)
	q.Set("page_size", strconv.Itoa(pageSize))
	if pageToken != "" {
		q.Set("page_token", pageToken)
	}
	data, err := c.authorizedGET(ctx, token, "/open-apis/search/v1/user?"+q.Encode())
	if err != nil {
		return nil, err
	}
	var payload struct {
		HasMore   bool   `json:"has_more"`
		PageToken string `json:"page_token"`
		Users     []struct {
			OpenID string `json:"open_id"`
			// Decoded for contract fidelity; deliveries address users by
			// open_id (receive_id_type=open_id), user_id is unused today.
			UserID string          `json:"user_id"`
			Name   string          `json:"name"`
			Avatar json.RawMessage `json:"avatar"`
		} `json:"users"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("feishu: search user decode: %w", err)
	}
	// Continuation invariant (七次复审 P1-1): has_more=true obligates a
	// non-empty page_token, and the token must make progress (the official
	// contract guarantees page_token != "" whenever has_more is true). Either
	// violation would truncate the directory search while the caller still
	// believes the picker saw every match — fail loud instead.
	if payload.HasMore {
		if payload.PageToken == "" {
			return nil, fmt.Errorf("feishu: user search has_more without page_token")
		}
		if pageToken != "" && payload.PageToken == pageToken {
			return nil, fmt.Errorf("feishu: user search page_token made no progress")
		}
	}
	out := &FeishuContactUserPage{
		Users:     make([]FeishuContactUser, 0, len(payload.Users)),
		HasMore:   payload.HasMore,
		PageToken: payload.PageToken,
	}
	for _, it := range payload.Users {
		out.Users = append(out.Users, FeishuContactUser{
			OpenID:    it.OpenID,
			Name:      it.Name,
			AvatarURL: avatarFromRaw(it.Avatar),
		})
	}
	return out, nil
}

// SendIMMessage delivers one message. content is the provider's JSON-encoded
// content string (e.g. the interactive card object marshalled to a string).
func (c *FeishuClient) SendIMMessage(ctx context.Context, token, receiveIDType, receiveID, msgType, content string) error {
	body := map[string]any{
		"receive_id": receiveID,
		"msg_type":   msgType,
		"content":    content,
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/open-apis/im/v1/messages?receive_id_type="+receiveIDType, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("feishu: send im: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var env feishuEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("feishu: send im decode: %w", err)
	}
	if env.Code != 0 {
		return fmt.Errorf("feishu: send im: code %d: %s", env.Code, env.Msg)
	}
	return nil
}

// FeishuAPIError is a structured Open Platform business error. Directory
// synchronization keeps the code so the admin console can distinguish missing
// permissions from transient transport failures without parsing strings.
type FeishuAPIError struct {
	Path string
	Code int
	Msg  string
}

func (e *FeishuAPIError) Error() string {
	return fmt.Sprintf("feishu: %s: code %d: %s", e.Path, e.Code, e.Msg)
}

// authorizedPOST issues an app/user-authorized JSON POST. Directory pages can
// be large, so the response is bounded at 8 MiB instead of using an unlimited
// ReadAll.
func (c *FeishuClient) authorizedPOST(ctx context.Context, token, path string, body any) (json.RawMessage, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 6; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(buf))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("feishu: %s: %w", path, err)
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("feishu: %s read: %w", path, readErr)
		}
		var env feishuEnvelope
		decodeErr := json.Unmarshal(raw, &env)
		rateLimited := resp.StatusCode == http.StatusTooManyRequests || (decodeErr == nil && env.Code == 99991400)
		if rateLimited && attempt < 5 {
			delay := time.Duration(250*(1<<attempt)) * time.Millisecond
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
			continue
		}
		if decodeErr != nil {
			if resp.StatusCode >= 400 {
				return nil, fmt.Errorf("feishu: %s: HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(raw)))
			}
			return nil, fmt.Errorf("feishu: %s decode: %w", path, decodeErr)
		}
		if env.Code != 0 {
			return nil, &FeishuAPIError{Path: path, Code: env.Code, Msg: env.Msg}
		}
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("feishu: %s: HTTP %d", path, resp.StatusCode)
		}
		return env.Data, nil
	}
	return nil, fmt.Errorf("feishu: %s: retry exhausted", path)
}

type DirectoryI18nText struct {
	DefaultValue string            `json:"default_value"`
	I18nValue    map[string]string `json:"i18n_value"`
}

func (t DirectoryI18nText) Display() string {
	if strings.TrimSpace(t.DefaultValue) != "" {
		return strings.TrimSpace(t.DefaultValue)
	}
	for _, key := range []string{"zh_cn", "en_us", "ja_jp"} {
		if v := strings.TrimSpace(t.I18nValue[key]); v != "" {
			return v
		}
	}
	return ""
}

type DirectoryDepartment struct {
	DepartmentID       string            `json:"department_id"`
	Name               DirectoryI18nText `json:"name"`
	ParentDepartmentID string            `json:"parent_department_id"`
	OrderWeight        string            `json:"order_weight"`
	EnabledStatus      *bool             `json:"enabled_status"`
}

type DirectoryDepartmentPage struct {
	Departments []DirectoryDepartment
	HasMore     bool
	PageToken   string
	Abnormals   []DirectoryAbnormal
}

type DirectoryAbnormal struct {
	ID          string         `json:"id"`
	RowError    int            `json:"row_error"`
	FieldErrors map[string]int `json:"field_errors"`
}

type DirectoryEmployeeDepartment struct {
	DepartmentID string `json:"department_id"`
}

type DirectoryEmployeeName struct {
	Name DirectoryI18nText `json:"name"`
}

type DirectoryEmployeeBaseInfo struct {
	EmployeeID   string                        `json:"employee_id"`
	Name         DirectoryEmployeeName         `json:"name"`
	Avatar       json.RawMessage               `json:"avatar"`
	Departments  []DirectoryEmployeeDepartment `json:"departments"`
	ActiveStatus int                           `json:"active_status"`
	IsResigned   *bool                         `json:"is_resigned"`
}

type DirectoryEmployee struct {
	BaseInfo DirectoryEmployeeBaseInfo `json:"base_info"`
}

type DirectoryEmployeePage struct {
	Employees []DirectoryEmployee
	HasMore   bool
	PageToken string
	Abnormals []DirectoryAbnormal
}

func (e DirectoryEmployee) OpenID() string      { return e.BaseInfo.EmployeeID }
func (e DirectoryEmployee) DisplayName() string { return e.BaseInfo.Name.Name.Display() }
func (e DirectoryEmployee) AvatarURL() string   { return avatarFromRaw(e.BaseInfo.Avatar) }

func quoteDirectoryFilterValue(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
func jsonDirectoryFilterValue(values []string) string {
	raw, _ := json.Marshal(values)
	return string(raw)
}

func directoryPageRequest(pageToken string) map[string]any {
	return map[string]any{"page_size": 100, "page_token": pageToken}
}

// ListDirectoryDepartments reads one full-directory page with application
// identity. Empty filter conditions mean "all departments" per directory/v1.
func (c *FeishuClient) ListDirectoryDepartments(ctx context.Context, token, parentOpenID, pageToken string) (*DirectoryDepartmentPage, error) {
	const path = "/open-apis/directory/v1/departments/filter?employee_id_type=open_id&department_id_type=open_department_id"
	data, err := c.authorizedPOST(ctx, token, path, map[string]any{
		"filter": map[string]any{"conditions": []any{map[string]any{
			"field": "parent_department_id", "operator": "eq", "value": quoteDirectoryFilterValue(parentOpenID),
		}}},
		"required_fields": []string{"department_id", "name", "parent_department_id", "order_weight"},
		"page_request":    directoryPageRequest(pageToken),
	})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Departments  []DirectoryDepartment `json:"departments"`
		PageResponse struct {
			HasMore   bool   `json:"has_more"`
			PageToken string `json:"page_token"`
		} `json:"page_response"`
		Abnormals []DirectoryAbnormal `json:"abnormals"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("feishu: directory departments decode: %w", err)
	}
	return &DirectoryDepartmentPage{Departments: payload.Departments, HasMore: payload.PageResponse.HasMore, PageToken: payload.PageResponse.PageToken, Abnormals: payload.Abnormals}, nil
}

// ListDirectoryEmployees reads one employee page using open_id, matching the
// identifier stored by Feishu OAuth in feishu_identities.open_id.
func (c *FeishuClient) ListDirectoryEmployees(ctx context.Context, token string, departmentIDs []string, staffStatus int, pageToken string) (*DirectoryEmployeePage, error) {
	const path = "/open-apis/directory/v1/employees/filter?employee_id_type=open_id&department_id_type=open_department_id"
	data, err := c.authorizedPOST(ctx, token, path, map[string]any{
		"filter": map[string]any{"conditions": []any{
			map[string]any{"field": "base_info.departments.department_id", "operator": "in", "value": jsonDirectoryFilterValue(departmentIDs)},
			map[string]any{"field": "work_info.staff_status", "operator": "eq", "value": strconv.Itoa(staffStatus)},
		}},
		"required_fields": []string{
			"base_info.employee_id", "base_info.name", "base_info.avatar",
			"base_info.departments.department_id", "base_info.active_status", "base_info.is_resigned",
		},
		"page_request": directoryPageRequest(pageToken),
	})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Employees    []DirectoryEmployee `json:"employees"`
		PageResponse struct {
			HasMore   bool   `json:"has_more"`
			PageToken string `json:"page_token"`
		} `json:"page_response"`
		Abnormals []DirectoryAbnormal `json:"abnormals"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("feishu: directory employees decode: %w", err)
	}
	return &DirectoryEmployeePage{Employees: payload.Employees, HasMore: payload.PageResponse.HasMore, PageToken: payload.PageResponse.PageToken, Abnormals: payload.Abnormals}, nil
}
