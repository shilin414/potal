package identity

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/platform/crypto"
)

// OAuthState is the signed OAuth anti-CSRF state token.
type OAuthState struct {
	ReturnTo string `json:"return_to"`
	IssuedAt int64  `json:"iat"`
}

// StateTTL mirrors the reference implementation (10 minutes).
const StateTTL = 10 * time.Minute

// StateCodec signs and verifies OAuth state values (HMAC-SHA256).
type StateCodec struct {
	secret []byte
}

func NewStateCodec(secret string) *StateCodec {
	return &StateCodec{secret: []byte(secret)}
}

// Dump signs and encodes the state.
func (c *StateCodec) Dump(state OAuthState) string {
	if state.IssuedAt == 0 {
		state.IssuedAt = time.Now().Unix()
	}
	raw, _ := json.Marshal(state)
	body := base64.RawURLEncoding.EncodeToString(raw)
	return body + "." + c.sign(body)
}

// Load verifies signature and TTL.
func (c *StateCodec) Load(encoded string) (*OAuthState, error) {
	body, sig, ok := cut2(encoded, ".")
	if !ok || body == "" || sig == "" {
		return nil, errors.New("invalid oauth state")
	}
	if subtle.ConstantTimeCompare([]byte(c.sign(body)), []byte(sig)) != 1 {
		return nil, errors.New("invalid oauth state")
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return nil, errors.New("invalid oauth state")
	}
	var state OAuthState
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, errors.New("invalid oauth state")
	}
	if time.Since(time.Unix(state.IssuedAt, 0)) > StateTTL {
		return nil, errors.New("expired oauth state")
	}
	return &state, nil
}

func (c *StateCodec) sign(body string) string {
	mac := hmac.New(sha256.New, c.secret)
	mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func cut2(s, sep string) (string, string, bool) {
	for i := 0; i+len(sep) <= len(s); i++ {
		if s[i:i+len(sep)] == sep {
			return s[:i], s[i+len(sep):], true
		}
	}
	return s, "", false
}

// SanitizeReturnTo keeps the post-login redirect a safe in-app path.
func SanitizeReturnTo(s string) string {
	if s == "" || s[0] != '/' || (len(s) > 1 && s[1] == '/') {
		return "/"
	}
	return s
}

// OAuthResult is the outcome of a successful exchange.
type OAuthResult struct {
	User     *User
	Identity *FeishuIdentity
}

// ExchangeOrchestrator wires the OAuth exchange to user mapping and token
// encryption. Refresh token → AES-256-GCM → TiDB (never plaintext).
type ExchangeOrchestrator struct {
	Repo        *Repo
	DB          *sql.DB
	Feishu      *FeishuClient
	GCM         *crypto.AESGCM
	RedirectURI string
}

// Exchange performs code → tokens → upsert user → encrypted refresh token.
func (o *ExchangeOrchestrator) Exchange(ctx context.Context, code string) (*OAuthResult, *TokenResult, error) {
	tokens, err := o.Feishu.ExchangeCode(ctx, code, o.RedirectURI)
	if err != nil {
		return nil, nil, err
	}
	info, err := o.Feishu.GetUserInfo(ctx, tokens.AccessToken)
	if err != nil {
		return nil, nil, err
	}
	if info.OpenID == "" {
		return nil, nil, errors.New("feishu: user info missing open_id")
	}

	enc, err := o.GCM.Encrypt(tokens.RefreshToken)
	if err != nil {
		return nil, nil, errors.New("encrypt refresh token")
	}
	var refreshExpires *time.Time
	if tokens.RefreshTokenExpiresIn > 0 {
		t := time.Now().UTC().Add(time.Duration(tokens.RefreshTokenExpiresIn) * time.Second)
		refreshExpires = &t
	}

	var userID int64
	err = withTx(ctx, o.DB, func(tx *sql.Tx) error {
		id, err := UpsertFeishuUserTx(ctx, tx, info, enc, refreshExpires)
		userID = id
		return err
	})
	if err != nil {
		return nil, nil, err
	}

	user, ident, err := o.Repo.UserWithIdentity(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	return &OAuthResult{User: user, Identity: ident}, tokens, nil
}

// RotateRefresh persists a rotated refresh token after a UAT refresh.
// Used by the Aily auth resolver when Feishu returns a new refresh_token.
func (o *ExchangeOrchestrator) RotateRefresh(ctx context.Context, identityID int64, enc string, expires *time.Time) error {
	return withTx(ctx, o.DB, func(tx *sql.Tx) error {
		return RotateRefreshTokenTx(ctx, tx, identityID, enc, expires)
	})
}

func withTx(ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
