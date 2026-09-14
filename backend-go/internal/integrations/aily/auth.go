package aily

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/crypto"
	"github.com/creation-agent-studio/backend-go/internal/platform/redisx"
)

// ProviderKey is the catalog provider key.
const ProviderKey = "feishu_aily"

// TokenEndpoints (Feishu open platform).
const (
	tenantTokenPath = "/open-apis/auth/v3/tenant_access_token/internal"
	userTokenPath   = "/open-apis/authen/v2/oauth/token"
)

// AuthResolver maps studio identity → Aily credentials.
//
// Separation invariants (§12/§13):
//   - Studio sessions never double as provider credentials.
//   - user identity (identity_mode=user) REQUIRES the user's UAT — the
//     real agent rejects app identity (10009).
//   - Refresh tokens live AES-256-GCM encrypted in MySQL; access tokens
//     only in the Redis TTL cache.
type AuthResolver struct {
	DB       *sql.DB
	Redis    *redisx.Client
	Feishu   FeishuTokenAPI
	GCM      *crypto.AESGCM
	AppID    string
	OnRotate func(ctx context.Context, identityID int64, enc string, expires *time.Time) error
}

// FeishuTokenAPI is the subset of the Feishu client needed here.
type FeishuTokenAPI interface {
	RefreshUserToken(ctx context.Context, refreshToken string) (*TokenResult, error)
	TenantToken(ctx context.Context) (*TokenResult, error)
}

// TokenResult mirrors identity.FeishuClient results to avoid an import
// cycle at this boundary.
type TokenResult struct {
	AccessToken           string
	RefreshToken          string
	ExpiresIn             int64
	RefreshTokenExpiresIn int64
}

// AuthContext is the resolved credential (in-memory only).
type AuthContext struct {
	Provider      string
	IdentityMode  string
	SubjectUserID string
	TenantID      string
	CredentialRef string
	Token         string
}

func (r *AuthResolver) uatKey(userID int64) string {
	return r.Redis.Key("provider", "aily", "uat", fmt.Sprintf("%d", userID))
}

// UserAccessToken returns a cached or freshly refreshed UAT.
func (r *AuthResolver) UserAccessToken(ctx context.Context, userID int64) (string, error) {
	if cached, err := r.Redis.Get(ctx, r.uatKey(userID)).Result(); err == nil && cached != "" {
		var record struct {
			Token     string  `json:"token"`
			ExpiresAt float64 `json:"expires_at"`
		}
		if json.Unmarshal([]byte(cached), &record) == nil && record.ExpiresAt-float64(time.Now().Unix()) > 30 {
			return record.Token, nil
		}
	}

	q := db.New(r.DB)
	row, err := q.GetFeishuIdentityByLocalUser(ctx, uint64(userID))
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("user has no feishu identity; re-login through Feishu OAuth")
	}
	if err != nil {
		return "", err
	}
	gcm := r.GCM
	refreshPlain, err := gcm.Decrypt(row.RefreshTokenEnc.String)
	if err != nil || refreshPlain == "" {
		return "", fmt.Errorf("user has no stored refresh token; re-login through Feishu OAuth")
	}

	tokens, err := r.Feishu.RefreshUserToken(ctx, refreshPlain)
	if err != nil {
		return "", fmt.Errorf("feishu token refresh failed: %w", err)
	}

	// Rotate the stored refresh token when Feishu issues a new one.
	if tokens.RefreshToken != "" && tokens.RefreshToken != refreshPlain {
		if enc, err := gcm.Encrypt(tokens.RefreshToken); err == nil {
			var expires *time.Time
			if tokens.RefreshTokenExpiresIn > 0 {
				t := time.Now().UTC().Add(time.Duration(tokens.RefreshTokenExpiresIn) * time.Second)
				expires = &t
			}
			if r.OnRotate != nil {
				_ = r.OnRotate(ctx, int64(row.ID), enc, expires)
			}
		}
	}

	ttl := tokens.ExpiresIn
	if ttl <= 0 {
		ttl = 7200
	}
	record, _ := json.Marshal(map[string]any{
		"token":      tokens.AccessToken,
		"expires_at": time.Now().Unix() + ttl,
	})
	_ = r.Redis.Set(ctx, r.uatKey(userID), record, time.Duration(ttl)*time.Second).Err()
	return tokens.AccessToken, nil
}

// TenantAccessToken returns the app TAT (tenant-mode bindings only).
func (r *AuthResolver) TenantAccessToken(ctx context.Context, appID string) (string, error) {
	key := r.Redis.Key("provider", "aily", "tat", appID)
	if cached, err := r.Redis.Get(ctx, key).Result(); err == nil && cached != "" {
		var record struct {
			Token     string  `json:"token"`
			ExpiresAt float64 `json:"expires_at"`
		}
		if json.Unmarshal([]byte(cached), &record) == nil && record.ExpiresAt-float64(time.Now().Unix()) > 30 {
			return record.Token, nil
		}
	}
	tokens, err := r.Feishu.TenantToken(ctx)
	if err != nil {
		return "", err
	}
	ttl := tokens.ExpiresIn
	if ttl <= 0 {
		ttl = 7000
	}
	record, _ := json.Marshal(map[string]any{
		"token":      tokens.AccessToken,
		"expires_at": time.Now().Unix() + ttl,
	})
	_ = r.Redis.Set(ctx, key, record, time.Duration(ttl)*time.Second).Err()
	return tokens.AccessToken, nil
}

// Build resolves the AuthContext for a run's identity mode.
func (r *AuthResolver) Build(ctx context.Context, userID int64, identityMode, appID string) (*AuthContext, error) {
	if identityMode == "tenant" {
		token, err := r.TenantAccessToken(ctx, appID)
		if err != nil {
			return nil, err
		}
		return &AuthContext{
			Provider:      ProviderKey,
			IdentityMode:  "tenant",
			TenantID:      appID,
			CredentialRef: "feishu_tat:" + appID,
			Token:         token,
		}, nil
	}
	token, err := r.UserAccessToken(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &AuthContext{
		Provider:      ProviderKey,
		IdentityMode:  "user",
		SubjectUserID: fmt.Sprintf("%d", userID),
		TenantID:      appID,
		CredentialRef: fmt.Sprintf("feishu_uat:%d", userID),
		Token:         token,
	}, nil
}
