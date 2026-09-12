// Package identity owns users, Feishu identity mapping and the studio
// session. Studio sessions (opaque cookie) are fully separated from
// provider credentials (Feishu UAT) — they never share a token.
package identity

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/crypto"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
)

// User is the studio account.
type User struct {
	ID          int64     `json:"id"`
	Username    string    `json:"username"`
	PasswordSet bool      `json:"-"`
	DisplayName string    `json:"display_name"`
	DisplayID   string    `json:"display_id"`
	Email       string    `json:"email"`
	AvatarURL   string    `json:"avatar_url"`
	Bio         string    `json:"bio"`
	Role        string    `json:"role"`
	AuthSource  string    `json:"auth_source"`
	IsStaff     bool      `json:"is_staff"`
	IsActive    bool      `json:"-"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Auth sources.
const (
	AuthSourceFeishu     = "feishu"
	AuthSourceLocalAdmin = "local_admin"
)

// FeishuIdentity maps a local user to the Feishu account plus the
// encrypted refresh token (the only provider credential persisted).
type FeishuIdentity struct {
	ID                    int64
	UserID                int64
	OpenID                string
	UnionID               string
	FeishuUserID          string
	DisplayName           string
	AvatarURL             string
	RefreshTokenEnc       []byte
	RefreshTokenExpiresAt *time.Time
	LastLoginAt           *time.Time
}

// ErrNotFound marks missing entities.
var ErrNotFound = errors.New("identity: not found")

// DisplayIdentity resolves the "姓名（工号）" pair used everywhere in the UI.
// Precedence mirrors the validated reference behavior:
//
//	display_name = feishu display name → username
//	display_id   = user.display_id → feishu user_id → local id
func (u *User) DisplayIdentity(id *FeishuIdentity) (string, string) {
	name := u.DisplayName
	if name == "" {
		name = u.Username
	}
	disp := u.DisplayID
	if disp == "" && id != nil {
		disp = id.FeishuUserID
	}
	if disp == "" {
		disp = fmt.Sprintf("%d", u.ID)
	}
	return name, disp
}

// SessionUser is the canonical payload returned by session endpoints.
type SessionUser struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	Email       string `json:"email,omitempty"`
	Role        string `json:"role,omitempty"`
	Avatar      string `json:"avatar,omitempty"`
	AvatarURL   string `json:"avatar_url"`
	AuthSource  string `json:"auth_source,omitempty"`
	IsStaff     bool   `json:"is_staff"`
	DisplayName string `json:"display_name"`
	DisplayID   string `json:"display_id"`
}

// SessionPayload builds the canonical user shape (single source of truth).
func SessionPayload(u *User, id *FeishuIdentity) SessionUser {
	name, disp := u.DisplayIdentity(id)
	avatar := u.AvatarURL
	if avatar == "" && id != nil {
		avatar = id.AvatarURL
	}
	return SessionUser{
		ID:          u.ID,
		Username:    u.Username,
		Email:       u.Email,
		Role:        u.Role,
		Avatar:      avatar,
		AvatarURL:   avatar,
		AuthSource:  u.AuthSource,
		IsStaff:     u.IsStaff,
		DisplayName: name,
		DisplayID:   disp,
	}
}

// NewUUIDBinary returns a fresh BINARY(16) value for gen/db inserts.
func NewUUIDBinary() []byte { b := ids.New(); return b.Bytes() }

func userFromRow(r db.User) *User {
	return &User{
		ID:          int64(r.ID),
		Username:    r.Username,
		PasswordSet: r.PasswordHash != "",
		DisplayName: r.DisplayName,
		DisplayID:   r.DisplayID,
		Email:       r.Email,
		AvatarURL:   r.AvatarUrl,
		Bio:         r.Bio.String,
		Role:        r.Role,
		AuthSource:  r.AuthSource,
		IsStaff:     r.IsStaff,
		IsActive:    r.IsActive,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
}

func feishuFromRow(r db.FeishuIdentity) *FeishuIdentity {
	out := &FeishuIdentity{
		ID:              int64(r.ID),
		UserID:          int64(r.UserID),
		OpenID:          r.OpenID.String,
		UnionID:         r.UnionID,
		FeishuUserID:    r.FeishuUserID.String,
		DisplayName:     r.DisplayName,
		AvatarURL:       r.AvatarUrl,
		RefreshTokenEnc: []byte(r.RefreshTokenEnc.String),
	}
	if r.RefreshTokenExpiresAt.Valid {
		t := r.RefreshTokenExpiresAt.Time
		out.RefreshTokenExpiresAt = &t
	}
	if r.LastLoginAt.Valid {
		t := r.LastLoginAt.Time
		out.LastLoginAt = &t
	}
	return out
}

// DecryptedRefreshToken decrypts the stored refresh token.
func (f *FeishuIdentity) DecryptedRefreshToken(gcm *crypto.AESGCM) (string, error) {
	if len(f.RefreshTokenEnc) == 0 {
		return "", errors.New("identity: no stored refresh token")
	}
	return gcm.Decrypt(string(f.RefreshTokenEnc))
}

// EncryptRefreshToken encrypts a refresh token for storage.
func EncryptRefreshToken(gcm *crypto.AESGCM, token string) ([]byte, error) {
	out, err := gcm.Encrypt(token)
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

// FeishuUserInfo is the subset of authen/v1/user_info we consume.
type FeishuUserInfo struct {
	Name       string `json:"name"`
	EnName     string `json:"en_name"`
	OpenID     string `json:"open_id"`
	UnionID    string `json:"union_id"`
	UserID     string `json:"user_id"`
	EmployeeNo string `json:"employee_no"`
	TenantKey  string `json:"tenant_key"`
	AvatarURL  string `json:"avatar_url"`
	// Some tenants expose avatar_* variants; accept the common ones.
	AvatarBig   string `json:"avatar_big"`
	AvatarThumb string `json:"avatar_thumb"`
}

func (u FeishuUserInfo) BestAvatar() string {
	if u.AvatarURL != "" {
		return u.AvatarURL
	}
	if u.AvatarBig != "" {
		return u.AvatarBig
	}
	return u.AvatarThumb
}

func (u FeishuUserInfo) BestDisplayName() string {
	if u.Name != "" {
		return u.Name
	}
	return u.EnName
}

// EnsureJSON marshals v for JSON columns; nil for nil input.
func EnsureJSON(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return raw
}
