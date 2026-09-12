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

// OAuth scope: offline_access plus every Aily permission the runtime needs.
// This set was validated against the real environment (2026-09-11): without
// it no refresh_token is issued and Aily user-identity calls fail.
const OAuthScope = "offline_access aily:agent_chat:write aily:agent_chat:read" +
	" aily:agent_attachment:write aily:agent_artifact:read aily:agent_visibility:read"

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
