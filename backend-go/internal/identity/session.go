package identity

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/platform/crypto"
	"github.com/creation-agent-studio/backend-go/internal/platform/redisx"
)

// Session is the opaque studio session payload.
type Session struct {
	UserID     int64     `json:"user_id"`
	Username   string    `json:"username"`
	AuthSource string    `json:"auth_source"`
	IsStaff    bool      `json:"is_staff"`
	CreatedAt  time.Time `json:"created_at"`
}

// SessionStore keeps opaque sessions in Redis keyed by the SHA-256 of the
// token (never the raw token value).
type SessionStore struct {
	rdb        *redisx.Client
	ttl        time.Duration
	cookieName string
	csrfName   string
}

func NewSessionStore(rdb *redisx.Client, ttl time.Duration, cookieName, csrfName string) *SessionStore {
	return &SessionStore{rdb: rdb, ttl: ttl, cookieName: cookieName, csrfName: csrfName}
}

func (s *SessionStore) CookieName() string { return s.cookieName }
func (s *SessionStore) CSRFName() string   { return s.csrfName }

func (s *SessionStore) key(tokenHash string) string {
	return s.rdb.Key("session", tokenHash)
}

// Create mints a new session token pair (cookie value + CSRF token).
func (s *SessionStore) Create(ctx context.Context, sess Session) (token, csrf string, err error) {
	token, err = crypto.RandomToken()
	if err != nil {
		return "", "", err
	}
	csrf, err = crypto.RandomToken()
	if err != nil {
		return "", "", err
	}
	sess.CreatedAt = time.Now().UTC()
	raw, err := json.Marshal(sess)
	if err != nil {
		return "", "", err
	}
	if err := s.rdb.Set(ctx, s.key(crypto.HashToken(token)), raw, s.ttl).Err(); err != nil {
		return "", "", err
	}
	return token, csrf, nil
}

// Get resolves a token to its session; expired/missing tokens fail.
func (s *SessionStore) Get(ctx context.Context, token string) (*Session, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	raw, err := s.rdb.Get(ctx, s.key(crypto.HashToken(token))).Bytes()
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	if err != nil {
		return nil, ErrNotFound
	}
	var sess Session
	if err := json.Unmarshal(raw, &sess); err != nil {
		return nil, ErrNotFound
	}
	return &sess, nil
}

// Revoke deletes the session (logout is immediate).
func (s *SessionStore) Revoke(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	return s.rdb.Del(ctx, s.key(crypto.HashToken(token))).Err()
}

// Refresh extends the TTL of a live session (sliding expiration on activity).
func (s *SessionStore) Refresh(ctx context.Context, token string) {
	if token == "" {
		return
	}
	_ = s.rdb.Expire(ctx, s.key(crypto.HashToken(token)), s.ttl).Err()
}

// ValidateCSRF implements double-submit cookie validation: the
// X-CSRF-Token header must equal the studio_csrf cookie value.
func ValidateCSRF(headerValue, cookieValue string) bool {
	if headerValue == "" || cookieValue == "" {
		return false
	}
	return crypto.ConstantTimeEqual(headerValue, cookieValue)
}
