// Package bridge adapts the identity Feishu client to the aily token API
// boundary (no direct import cycle between the two domains).
package bridge

import (
	"context"

	"github.com/creation-agent-studio/backend-go/internal/identity"
	aily "github.com/creation-agent-studio/backend-go/internal/integrations/aily"
)

// TokenAPI implements aily.FeishuTokenAPI over identity.FeishuClient.
type TokenAPI struct {
	Feishu *identity.FeishuClient
}

func NewTokenAPI(f *identity.FeishuClient) *TokenAPI { return &TokenAPI{Feishu: f} }

func (b *TokenAPI) RefreshUserToken(ctx context.Context, refreshToken string) (*aily.TokenResult, error) {
	out, err := b.Feishu.RefreshUserToken(ctx, refreshToken)
	if err != nil {
		return nil, err
	}
	return &aily.TokenResult{
		AccessToken:           out.AccessToken,
		RefreshToken:          out.RefreshToken,
		ExpiresIn:             out.ExpiresIn,
		RefreshTokenExpiresIn: out.RefreshTokenExpiresIn,
	}, nil
}

func (b *TokenAPI) TenantToken(ctx context.Context) (*aily.TokenResult, error) {
	out, err := b.Feishu.TenantToken(ctx)
	if err != nil {
		return nil, err
	}
	return &aily.TokenResult{AccessToken: out.TenantAccessToken, ExpiresIn: out.Expire}, nil
}
