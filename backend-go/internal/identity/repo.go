package identity

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/crypto"
	"github.com/creation-agent-studio/backend-go/internal/platform/dbtypes"
)

// Repo persists users and Feishu identities through sqlc.
type Repo struct {
	q db.Querier
}

func NewRepo(dbtx db.DBTX) *Repo { return &Repo{q: db.New(dbtx)} }

func (r *Repo) Querier() db.Querier { return r.q }

// UserWithIdentity loads a user plus its (optional) Feishu identity.
func (r *Repo) UserWithIdentity(ctx context.Context, id int64) (*User, *FeishuIdentity, error) {
	row, err := r.q.GetUserByID(ctx, uint64(id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	u := userFromRow(row)
	ident, _ := r.IdentityByLocalUser(ctx, u.ID) // identity is optional
	return u, ident, nil
}

func (r *Repo) UserByUsername(ctx context.Context, username string) (*User, error) {
	row, err := r.q.GetUserByUsername(ctx, username)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return userFromRow(row), nil
}

func (r *Repo) IdentityByOpenID(ctx context.Context, openID string) (*FeishuIdentity, error) {
	return r.identityBy(ctx, openID, "")
}

func (r *Repo) IdentityByFeishuUserID(ctx context.Context, fuID string) (*FeishuIdentity, error) {
	return r.identityBy(ctx, "", fuID)
}

func (r *Repo) identityBy(ctx context.Context, openID, fuID string) (*FeishuIdentity, error) {
	var (
		row db.FeishuIdentity
		err error
	)
	if openID != "" {
		row, err = r.q.GetFeishuIdentityByOpenID(ctx, nullString(openID))
	} else {
		row, err = r.q.GetFeishuIdentityByFeishuUserID(ctx, nullString(fuID))
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return feishuFromRow(row), nil
}

func (r *Repo) IdentityByLocalUser(ctx context.Context, userID int64) (*FeishuIdentity, error) {
	row, err := r.q.GetFeishuIdentityByLocalUser(ctx, uint64(userID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return feishuFromRow(row), nil
}

// UpsertFeishuUserTx creates or updates the user + identity in one
// transaction. runs inside a caller-managed tx (OAuth exchange).
func UpsertFeishuUserTx(ctx context.Context, tx *sql.Tx, info *FeishuUserInfo, refreshEnc string, refreshExpires *time.Time) (int64, error) {
	q := db.New(tx)

	// 1. Match by open_id, then by feishu_user_id (never by display values).
	if ident, err := q.GetFeishuIdentityByOpenID(ctx, nullString(info.OpenID)); err == nil {
		if err := q.UpdateFeishuIdentityLogin(ctx, db.UpdateFeishuIdentityLoginParams{
			DisplayName:           info.BestDisplayName(),
			AvatarUrl:             info.BestAvatar(),
			RefreshTokenEnc:       nullString(refreshEnc),
			RefreshTokenExpiresAt: nullTime(refreshExpires),
			FuUserID:              firstNonEmpty(info.UserID, info.EmployeeNo),
			ID:                    ident.ID,
		}); err != nil {
			return 0, err
		}
		// Refresh the local user's avatar/name snapshot.
		if _, err := q.GetUserByID(ctx, ident.UserID); err == nil {
			if _, err := tx.ExecContext(ctx,
				`UPDATE users SET avatar_url = IF(? = '', avatar_url, ?), display_name = IF(? = '', display_name, ?) WHERE id = ?`,
				info.BestAvatar(), info.BestAvatar(), info.BestDisplayName(), info.BestDisplayName(), ident.UserID,
			); err != nil {
				return 0, err
			}
		}
		return int64(ident.UserID), nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}

	if fuID := firstNonEmpty(info.UserID, info.EmployeeNo); fuID != "" {
		if ident, err := q.GetFeishuIdentityByFeishuUserID(ctx, nullString(fuID)); err == nil {
			if err := q.UpdateFeishuIdentityLogin(ctx, db.UpdateFeishuIdentityLoginParams{
				DisplayName:           info.BestDisplayName(),
				AvatarUrl:             info.BestAvatar(),
				RefreshTokenEnc:       nullString(refreshEnc),
				RefreshTokenExpiresAt: nullTime(refreshExpires),
				FuUserID:              fuID,
				ID:                    ident.ID,
			}); err != nil {
				return 0, err
			}
			return int64(ident.UserID), nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
	}

	// 2. New user: unique username with _N suffix on collision.
	base := info.EnName
	if base == "" {
		base = info.Name
	}
	base = sanitizeUsername(base)
	username := base
	for i := 1; ; i++ {
		var exists bool
		row := tx.QueryRowContext(ctx, `SELECT COUNT(*) > 0 FROM users WHERE username = ?`, username)
		if err := row.Scan(&exists); err != nil {
			return 0, err
		}
		if !exists {
			break
		}
		username = base + "_" + itoa(i)
	}
	res, err := q.CreateUser(ctx, db.CreateUserParams{
		Username:     username,
		PasswordHash: "", // Feishu users never log in with a password
		DisplayName:  info.BestDisplayName(),
		DisplayID:    firstNonEmpty(info.UserID, info.EmployeeNo),
		Email:        "",
		Role:         "creator",
		AuthSource:   AuthSourceFeishu,
		IsStaff:      false,
	})
	if err != nil {
		return 0, err
	}
	uid, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := q.CreateFeishuIdentity(ctx, db.CreateFeishuIdentityParams{
		UserID:                uint64(uid),
		OpenID:                nullString(info.OpenID),
		UnionID:               info.UnionID,
		FeishuUserID:          nullString(firstNonEmpty(info.UserID, info.EmployeeNo)),
		DisplayName:           info.BestDisplayName(),
		AvatarUrl:             info.BestAvatar(),
		RefreshTokenEnc:       nullString(refreshEnc),
		RefreshTokenExpiresAt: nullTime(refreshExpires),
		LastLoginAt:           nullTime(nowPtr()),
	}); err != nil {
		return 0, err
	}
	return uid, nil
}

// RotateRefreshTokenTx persists the rotated (encrypted) refresh token.
func RotateRefreshTokenTx(ctx context.Context, tx *sql.Tx, identityID int64, enc string, expires *time.Time) error {
	return db.New(tx).RotateRefreshToken(ctx, db.RotateRefreshTokenParams{
		RefreshTokenEnc:       nullString(enc),
		RefreshTokenExpiresAt: nullTime(expires),
		ID:                    uint64(identityID),
	})
}

// UpdatePassword sets an Argon2id hash (local admin flow).
func (r *Repo) UpdatePassword(ctx context.Context, userID int64, hash string) error {
	return r.q.UpdateUserPassword(ctx, db.UpdateUserPasswordParams{PasswordHash: hash, ID: uint64(userID)})
}

// VerifyLocalAdmin checks username + Argon2id password AND requires the
// account to be staff (修复计划 §39-40: a non-staff local account must
// never reach the admin login, nor the legacy transition endpoint —
// Feishu SSO is the only entry for normal users).
func (r *Repo) VerifyLocalAdmin(ctx context.Context, username, password string) (*User, error) {
	row, err := r.q.GetUserByUsername(ctx, username)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u := userFromRow(row)
	if !u.IsStaff {
		// Local password login is admin-only (staff); normal users are
		// Feishu OAuth only — reject before touching the hash so timing
		// also cannot distinguish staff from non-staff accounts.
		return nil, ErrNotFound
	}
	if !u.PasswordSet {
		return nil, ErrNotFound
	}
	// Argon2id is the native format; Django pbkdf2 hashes keep legacy
	// accounts working after the xiaoan3 data migration.
	var ok bool
	var err2 error
	if strings.HasPrefix(row.PasswordHash, "$argon2id$") {
		ok, err2 = crypto.VerifyPassword(password, row.PasswordHash)
	} else if strings.HasPrefix(row.PasswordHash, "pbkdf2_sha256$") {
		ok, err2 = crypto.VerifyDjangoPassword(password, row.PasswordHash)
	} else {
		return nil, ErrNotFound
	}
	if err2 != nil || !ok {
		return nil, ErrNotFound
	}
	return u, nil
}

// WriteAuditLog records an admin-auth audit event (修复计划 §41). It
// never stores credentials and never fails the caller's request: auditing
// is observability, not an auth dependency.
func (r *Repo) WriteAuditLog(ctx context.Context, userID *int64, action, resourceID string, detail map[string]any) {
	raw, _ := json.Marshal(detail)
	_ = r.q.CreateAuditLog(ctx, db.CreateAuditLogParams{
		UserID:     nullInt64(userID),
		Action:     action,
		ResourceID: resourceID,
		Detail:     dbtypes.JSONText(raw),
	})
}

func nullString(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }
func nullInt64(v *int64) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *v, Valid: true}
}
func nullTime(t *time.Time) sql.NullTime {
	if t == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *t, Valid: true}
}
func nowPtr() *time.Time { t := time.Now().UTC(); return &t }
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
func sanitizeUsername(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return "user"
	}
	return string(out)
}
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}
