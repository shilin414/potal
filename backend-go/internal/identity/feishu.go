package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

// ListUserChats lists the chats (groups) the token holder belongs to.
func (c *FeishuClient) ListUserChats(ctx context.Context, token string) ([]FeishuChat, error) {
	data, err := c.authorizedGET(ctx, token, "/open-apis/im/v1/chats?page_size=100")
	if err != nil {
		return nil, err
	}
	var payload struct {
		Items []struct {
			ChatID string          `json:"chat_id"`
			Name   string          `json:"name"`
			Avatar json.RawMessage `json:"avatar"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("feishu: chats decode: %w", err)
	}
	out := make([]FeishuChat, 0, len(payload.Items))
	for _, it := range payload.Items {
		out = append(out, FeishuChat{
			ChatID:    it.ChatID,
			Name:      it.Name,
			AvatarURL: avatarFromRaw(it.Avatar),
		})
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

// SearchFeishuUsers searches the org directory for coworkers. Requires a
// user_access_token (the app-only token has no directory search).
func (c *FeishuClient) SearchFeishuUsers(ctx context.Context, token, query string) ([]FeishuContactUser, error) {
	q := url.Values{}
	q.Set("query", query)
	q.Set("page_size", "20")
	data, err := c.authorizedGET(ctx, token, "/open-apis/search/v1/user?"+q.Encode())
	if err != nil {
		return nil, err
	}
	var payload struct {
		Entities []struct {
			OpenID string          `json:"open_id"`
			ID     string          `json:"id"`
			Name   string          `json:"name"`
			Avatar json.RawMessage `json:"avatar"`
		} `json:"entities"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("feishu: search user decode: %w", err)
	}
	out := make([]FeishuContactUser, 0, len(payload.Entities))
	for _, it := range payload.Entities {
		id := it.OpenID
		if id == "" {
			id = it.ID
		}
		out = append(out, FeishuContactUser{
			OpenID:    id,
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
