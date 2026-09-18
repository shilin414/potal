package http

import (
	genapi "github.com/creation-agent-studio/backend-go/internal/gen/api"
)

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/creation-agent-studio/backend-go/internal/identity"
)

// loginAttemptLimiter is a small in-process sliding-window throttle for
// the local admin login (修复计划 §41): max N attempts per key
// (username+IP) per window. Good enough for a single-instance admin
// entry point; a distributed limiter is unnecessary for staff-only
// low-volume traffic.
type loginAttemptLimiter struct {
	mu         sync.Mutex
	attempts   map[string][]time.Time
	window     time.Duration
	max        int
	maxKeys    int
	sweepEvery uint64
	calls      uint64
	now        func() time.Time
	overflow   [][]time.Time
}

func newLoginAttemptLimiter(max int, window time.Duration) *loginAttemptLimiter {
	return &loginAttemptLimiter{
		attempts:   map[string][]time.Time{},
		window:     window,
		max:        max,
		maxKeys:    adminLoginMaxKeys,
		sweepEvery: adminLoginSweepEvery,
		now:        time.Now,
		overflow:   make([][]time.Time, adminLoginOverflowBuckets),
	}
}

func (l *loginAttemptLimiter) prune(hist []time.Time, now time.Time) []time.Time {
	kept := hist[:0]
	for _, attemptedAt := range hist {
		if now.Sub(attemptedAt) < l.window {
			kept = append(kept, attemptedAt)
		}
	}
	return kept
}

func (l *loginAttemptLimiter) sweepExpiredLocked(now time.Time) {
	for key, hist := range l.attempts {
		kept := l.prune(hist, now)
		if len(kept) == 0 {
			delete(l.attempts, key)
			continue
		}
		l.attempts[key] = kept
	}
	for i, hist := range l.overflow {
		l.overflow[i] = l.prune(hist, now)
	}
}

func limiterOverflowBucket(key string, buckets int) int {
	if buckets <= 0 {
		return -1
	}
	const offset64 = uint64(1469598103934665603)
	const prime64 = uint64(1099511628211)
	hash := offset64
	for i := 0; i < len(key); i++ {
		hash ^= uint64(key[i])
		hash *= prime64
	}
	return int(hash % uint64(buckets))
}

func (l *loginAttemptLimiter) allowOverflowLocked(key string, now time.Time) bool {
	bucket := limiterOverflowBucket(key, len(l.overflow))
	if bucket < 0 {
		return false
	}
	kept := l.prune(l.overflow[bucket], now)
	if len(kept) >= l.max {
		l.overflow[bucket] = kept
		return false
	}
	l.overflow[bucket] = append(kept, now)
	return true
}

func (l *loginAttemptLimiter) allow(key string) bool {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	l.calls++
	if l.sweepEvery > 0 && l.calls%l.sweepEvery == 0 {
		l.sweepExpiredLocked(now)
	}

	hist, exists := l.attempts[key]
	kept := l.prune(hist, now)
	if len(kept) == 0 && exists {
		delete(l.attempts, key)
		exists = false
	}
	if len(kept) >= l.max {
		l.attempts[key] = kept
		return false
	}
	if !exists && l.maxKeys > 0 && len(l.attempts) >= l.maxKeys {
		// Periodic sweeps above reclaim expired entries. A fixed hash-bucket
		// overflow keeps memory and lookup time bounded while preserving the
		// same attempt budget for identities that cannot receive a dedicated
		// map entry. Collisions fail closed rather than weaken the limit.
		return l.allowOverflowLocked(key, now)
	}
	l.attempts[key] = append(kept, now)
	return true
}

// reset clears the failed-attempt budget after a successful authentication.
func (l *loginAttemptLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
	if bucket := limiterOverflowBucket(key, len(l.overflow)); bucket >= 0 {
		l.overflow[bucket] = nil
	}
}

// ──────────────────────────────────────────────────── OAuth endpoints ──

func (s *Server) OauthStart(w http.ResponseWriter, r *http.Request, params genapi.OauthStartParams) {
	if s.Config.Feishu.RedirectURI == "" {
		writeSimpleError(w, http.StatusInternalServerError, "FEISHU_REDIRECT_URI is not configured")
		return
	}
	returnTo := "/"
	if params.ReturnTo != nil {
		returnTo = identity.SanitizeReturnTo(*params.ReturnTo)
	}
	state := s.StateCodec.Dump(identity.OAuthState{ReturnTo: returnTo})
	http.Redirect(w, r, s.Feishu.AuthorizeURL(s.Config.Feishu.RedirectURI, state), http.StatusFound)
}

func (s *Server) OauthExchange(w http.ResponseWriter, r *http.Request, params genapi.OauthExchangeParams) {
	if params.Code == "" {
		writeSimpleError(w, http.StatusBadRequest, "missing code")
		return
	}
	state, err := s.StateCodec.Load(params.State)
	if err != nil {
		writeSimpleError(w, http.StatusBadRequest, "invalid or expired oauth state")
		return
	}
	result, _, err := s.Oauth.Exchange(r.Context(), params.Code)
	if err != nil {
		s.Log.Error("oauth exchange failed", "err", err)
		writeSimpleError(w, http.StatusBadGateway, "oauth exchange failed")
		return
	}
	if result.User == nil || !result.User.IsActive {
		writeDetail(w, http.StatusForbidden, "账号已停用，请联系管理员。")
		return
	}
	// A re-authorization grants the scopes requested at authorize time; a
	// cached user access token (2h TTL) predating it would keep failing with
	// the old scope set — drop it so the next provider call re-refreshes.
	if s.Redis != nil {
		_ = s.Redis.Del(r.Context(), s.Redis.Key(
			"provider", "aily", "uat", fmt.Sprintf("%d", result.User.ID))).Err()
	}
	s.setSessionCookie(w, r, result.User)
	writeJSON(w, http.StatusOK, map[string]any{
		"user":      identity.SessionPayload(result.User, result.Identity),
		"tokens":    map[string]string{"access": "", "refresh": ""},
		"return_to": state.ReturnTo,
	})
}

func (s *Server) GetSession(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	if user == nil {
		writeSimpleError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	writeJSON(w, http.StatusOK, identity.SessionPayload(user.User, user.Identity))
}

// adminLoginMaxAttempts / adminLoginWindow: 5 failed attempts per
// username+IP within 5 minutes lock that combination out (修复计划 §41).
const (
	adminLoginMaxAttempts             = 5
	adminLoginMaxPeerAttempts         = 100
	adminLoginWindow                  = 5 * time.Minute
	adminLoginMaxKeys                 = 10_000
	adminLoginOverflowBuckets         = 2_048
	adminLoginSweepEvery       uint64 = 100
	adminLoginUsernameMaxRunes        = 150
	adminLoginBodyMaxBytes            = 16 * 1024
	sessionRevokeTimeout              = 3 * time.Second
)

// clientIP returns the socket peer unless that peer is an explicitly trusted
// reverse proxy. Trusted proxies may supply X-Real-IP, which the shipped Nginx
// configuration overwrites from its own socket peer; arbitrary forwarding
// headers from direct clients are never accepted.
func (s *Server) clientIP(r *http.Request) string {
	peer := remoteIP(r.RemoteAddr)
	if peer == nil {
		return r.RemoteAddr
	}
	if !s.isTrustedProxy(peer) {
		return peer.String()
	}
	if forwarded := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); forwarded != nil {
		return forwarded.String()
	}
	return peer.String()
}

func remoteIP(remoteAddr string) net.IP {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = strings.Trim(remoteAddr, "[]")
	}
	return net.ParseIP(host)
}

func (s *Server) isTrustedProxy(peer net.IP) bool {
	s.trustedProxyOnce.Do(func() {
		if s.Config == nil {
			return
		}
		for _, raw := range s.Config.Auth.TrustedProxyCIDRs {
			_, network, err := net.ParseCIDR(raw)
			if err == nil {
				s.trustedProxyNets = append(s.trustedProxyNets, network)
			}
		}
	})
	for _, network := range s.trustedProxyNets {
		if network.Contains(peer) {
			return true
		}
	}
	return false
}

func (s *Server) adminLoginKey(username string, r *http.Request) string {
	return username + "|" + s.clientIP(r)
}

func (s *Server) adminLoginAttempts() *loginAttemptLimiter {
	s.adminLimiterOnce.Do(func() {
		s.adminLoginLimiter = newLoginAttemptLimiter(adminLoginMaxAttempts, adminLoginWindow)
		s.adminLoginPeerLimiter = newLoginAttemptLimiter(adminLoginMaxPeerAttempts, adminLoginWindow)
	})
	return s.adminLoginLimiter
}

func (s *Server) adminLoginPeerAttempts() *loginAttemptLimiter {
	s.adminLoginAttempts()
	return s.adminLoginPeerLimiter
}

func (s *Server) allowAdminLogin(username string, r *http.Request) bool {
	peer := s.clientIP(r)
	if !s.adminLoginPeerAttempts().allow(peer) {
		return false
	}
	return s.adminLoginAttempts().allow(s.adminLoginKey(username, r))
}

func (s *Server) resetAdminLogin(username string, r *http.Request) {
	s.adminLoginAttempts().reset(s.adminLoginKey(username, r))
	s.adminLoginPeerAttempts().reset(s.clientIP(r))
}

func normalizeAdminUsername(raw string) (string, bool) {
	username := strings.TrimSpace(raw)
	if username == "" || utf8.RuneCountInString(username) > adminLoginUsernameMaxRunes {
		return "", false
	}
	return username, true
}

func (s *Server) AdminLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeSimpleError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, adminLoginBodyMaxBytes)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeSimpleError(w, http.StatusBadRequest, "invalid json")
		return
	}
	username, ok := normalizeAdminUsername(body.Username)
	if !ok {
		writeSimpleError(w, http.StatusBadRequest, "invalid username")
		return
	}
	body.Username = username
	if !s.allowAdminLogin(body.Username, r) {
		w.Header().Set("Retry-After", "300")
		writeSimpleError(w, http.StatusTooManyRequests, "too many failed attempts, retry later")
		return
	}
	user, err := s.IdentityRepo.VerifyLocalAdmin(r.Context(), body.Username, body.Password)
	if err != nil {
		// Audit failure (never credentials) + throttle accounting.
		s.IdentityRepo.WriteAuditLog(r.Context(), nil, "admin.login.failed", body.Username, map[string]any{
			"ip": s.clientIP(r),
		})
		writeSimpleError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	s.resetAdminLogin(body.Username, r)
	s.IdentityRepo.WriteAuditLog(r.Context(), &user.ID, "admin.login.success", body.Username, map[string]any{
		"ip": s.clientIP(r),
	})
	s.setSessionCookie(w, r, user)
	writeJSON(w, http.StatusOK, map[string]any{
		"id":           user.ID,
		"username":     user.Username,
		"is_staff":     user.IsStaff,
		"auth_source":  identity.AuthSourceLocalAdmin,
		"display_name": user.DisplayName,
		"display_id":   user.DisplayID,
	})
}

// ───────────────────────────────────────────── legacy auth endpoints ──

// AuthLogin is the LEGACY local login kept only as a deprecated
// transition endpoint (修复计划 §40). Policy:
//   - staff-only: VerifyLocalAdmin rejects non-staff accounts, so normal
//     users can never enter the Studio with a local password (Feishu SSO
//     only).
//   - deprecated: sunset header + audit, same throttle as the admin page.
//     The current frontend never calls it — removal is a follow-up.
func (s *Server) AuthLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Deprecation", "true")
	w.Header().Set("Sunset", "Sat, 31 Dec 2026 23:59:59 GMT")
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, adminLoginBodyMaxBytes)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeDetail(w, http.StatusBadRequest, "invalid body")
		return
	}
	username, ok := normalizeAdminUsername(body.Username)
	if !ok {
		writeDetail(w, http.StatusBadRequest, "invalid username")
		return
	}
	body.Username = username
	if !s.allowAdminLogin(body.Username, r) {
		w.Header().Set("Retry-After", "300")
		writeDetail(w, http.StatusTooManyRequests, "尝试过于频繁，请稍后再试")
		return
	}
	user, err := s.IdentityRepo.VerifyLocalAdmin(r.Context(), body.Username, body.Password)
	if err != nil {
		s.IdentityRepo.WriteAuditLog(r.Context(), nil, "admin.login.failed", body.Username, map[string]any{
			"ip":     s.clientIP(r),
			"legacy": true,
		})
		writeDetail(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	s.resetAdminLogin(body.Username, r)
	s.IdentityRepo.WriteAuditLog(r.Context(), &user.ID, "admin.login.success", body.Username, map[string]any{
		"ip":     s.clientIP(r),
		"legacy": true,
	})
	s.setSessionCookie(w, r, user)
	writeJSON(w, http.StatusOK, map[string]any{
		"user":   identity.SessionPayload(user, nil),
		"tokens": map[string]string{"access": "", "refresh": ""},
	})
}

func (s *Server) AuthLogout(w http.ResponseWriter, r *http.Request) {
	var revokeErr error
	if c, err := r.Cookie(s.Store.CookieName()); err == nil {
		// Revocation is security-critical and must not be canceled merely
		// because the browser navigated away or its request timeout elapsed.
		revokeCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), sessionRevokeTimeout)
		revokeErr = s.Store.Revoke(revokeCtx, c.Value)
		cancel()
	}
	// The browser boundary closes even when Redis cannot revoke the server
	// token. The non-200 response preserves that security distinction while
	// the frontend's finally block still completes local logout.
	s.clearSessionCookies(w)
	if revokeErr != nil {
		if s.Log != nil {
			s.Log.Error("session revoke failed", "err", revokeErr)
		}
		if s.Metric != nil && s.Metric.SessionRevokeFailuresTotal != nil {
			s.Metric.SessionRevokeFailuresTotal.Inc()
		}
		writeSimpleError(w, http.StatusServiceUnavailable, "session revoke failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"detail": "登出成功"})
}

func (s *Server) GetMe(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	if user == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	writeJSON(w, http.StatusOK, identity.SessionPayload(user.User, user.Identity))
}

func (s *Server) PatchMe(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	if user == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	var body struct {
		Email     *string `json:"email"`
		Bio       *string `json:"bio"`
		DisplayID *string `json:"display_id"`
		Username  *string `json:"username"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeFieldErrors(w, map[string][]string{"body": {"invalid json"}})
		return
	}
	newUsername := user.Username
	if body.Username != nil && *body.Username != "" {
		newUsername = *body.Username
	}
	newEmail := user.Email
	if body.Email != nil {
		newEmail = *body.Email
	}
	newBio := user.Bio
	if body.Bio != nil {
		newBio = *body.Bio
	}
	newDisplayID := user.DisplayID
	if body.DisplayID != nil {
		newDisplayID = *body.DisplayID
	}
	if _, err := s.DB.ExecContext(r.Context(),
		`UPDATE users SET username = ?, email = ?, bio = ?, display_id = ? WHERE id = ?`,
		newUsername, newEmail, newBio, newDisplayID, user.ID); err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	updated, ident, err := s.IdentityRepo.UserWithIdentity(r.Context(), user.ID)
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, identity.SessionPayload(updated, ident))
}

func (s *Server) TokenRefresh(w http.ResponseWriter, r *http.Request) {
	// The opaque-session backend no longer issues JWTs; the legacy shape
	// stays so old clients degrade gracefully instead of crashing.
	writeJSON(w, http.StatusOK, map[string]string{"access": "", "refresh": ""})
}

// ──────────────────────────────────────────────────────── cookies ──

func (s *Server) clearSessionCookies(w http.ResponseWriter) {
	for _, cookie := range []struct {
		name     string
		httpOnly bool
	}{
		{name: s.Store.CookieName(), httpOnly: true},
		{name: s.Store.CSRFName(), httpOnly: false},
	} {
		http.SetCookie(w, &http.Cookie{
			Name:     cookie.name,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: cookie.httpOnly,
			Secure:   s.Config.Session.Secure,
			SameSite: http.SameSiteLaxMode,
		})
	}
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, user *identity.User) {
	token, csrf, err := s.Store.Create(r.Context(), identity.Session{
		UserID:     user.ID,
		Username:   user.Username,
		AuthSource: user.AuthSource,
		IsStaff:    user.IsStaff,
	})
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, "session create failed")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     s.Store.CookieName(),
		Value:    token,
		Path:     "/",
		MaxAge:   int(s.Config.Session.TTL / time.Second),
		HttpOnly: true,
		Secure:   s.Config.Session.Secure,
		SameSite: http.SameSiteLaxMode,
	})
	// Readable CSRF cookie for the SPA double-submit.
	http.SetCookie(w, &http.Cookie{
		Name:     s.Store.CSRFName(),
		Value:    csrf,
		Path:     "/",
		MaxAge:   int(s.Config.Session.TTL / time.Second),
		HttpOnly: false,
		Secure:   s.Config.Session.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}
