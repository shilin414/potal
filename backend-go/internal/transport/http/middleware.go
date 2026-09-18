package http

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/identity"
	"github.com/creation-agent-studio/backend-go/internal/platform/logging"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
)

type ctxKey string

const (
	userCtxKey    ctxKey = "studio.user"
	sessionCtxKey ctxKey = "studio.sessionToken"
	requestIDKey  ctxKey = "studio.request_id"
)

// AuthenticatedUser is the resolved caller.
type AuthenticatedUser struct {
	*identity.User
	Identity *identity.FeishuIdentity
}

func userFrom(ctx context.Context) *AuthenticatedUser {
	u, _ := ctx.Value(userCtxKey).(*AuthenticatedUser)
	return u
}

func sessionTokenFrom(ctx context.Context) string {
	s, _ := ctx.Value(sessionCtxKey).(string)
	return s
}

type identityUserResolver interface {
	UserWithIdentity(ctx context.Context, id int64) (*identity.User, *identity.FeishuIdentity, error)
}

// SessionAuth resolves the opaque session cookie to a user.
func SessionAuth(store IdentitySessionStore, repo identityUserResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(store.CookieName())
			if err == nil && c.Value != "" {
				if sess, err := store.Get(r.Context(), c.Value); err == nil {
					if user, ident, err := repo.UserWithIdentity(r.Context(), sess.UserID); err == nil {
						if user == nil || !user.IsActive {
							_ = store.Revoke(r.Context(), c.Value)
							next.ServeHTTP(w, r)
							return
						}
						ctx := context.WithValue(r.Context(), userCtxKey, &AuthenticatedUser{User: user, Identity: ident})
						ctx = context.WithValue(ctx, sessionCtxKey, c.Value)
						// Logout must never extend the token immediately before a
						// best-effort revoke; a revoke failure should leave only the
						// pre-existing TTL, not refresh it to a full session window.
						if r.URL.Path != "/api/auth/logout/" {
							store.Refresh(r.Context(), c.Value) // sliding TTL
						}
						next.ServeHTTP(w, r.WithContext(ctx))
						return
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireAuth rejects unauthenticated callers (401 like DRF).
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if userFrom(r.Context()) == nil {
			writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func logoutOriginAllowed(r *http.Request, devMode bool) bool {
	if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "same-origin") {
		return true
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return false
	}
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	if strings.EqualFold(parsed.Host, r.Host) {
		return true
	}
	if !devMode {
		return false
	}
	switch strings.ToLower(origin) {
	case "http://localhost:3030", "http://localhost:3031", "http://localhost:3032", "http://127.0.0.1:3030":
		return true
	default:
		return false
	}
}

// CSRF guards every state-changing request via double-submit cookie
// (X-CSRF-Token header vs studio_csrf cookie) plus Origin checking.
func CSRF(store IdentitySessionStore, devMode bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}
			// Exempt: the OAuth exchange is protected by the signed state.
			if strings.HasPrefix(r.URL.Path, "/api/identity/oauth/") ||
				strings.HasPrefix(r.URL.Path, "/api/identity/admin/login") ||
				strings.HasPrefix(r.URL.Path, "/api/auth/login/") ||
				strings.HasPrefix(r.URL.Path, "/api/auth/token/refresh/") {
				next.ServeHTTP(w, r)
				return
			}
			c, cookieErr := r.Cookie(store.CSRFName())
			csrfValid := cookieErr == nil && c.Value != "" &&
				identity.ValidateCSRF(r.Header.Get("X-CSRF-Token"), c.Value)
			if csrfValid {
				next.ServeHTTP(w, r)
				return
			}
			// Logout is a session-destroying operation. If the readable CSRF
			// cookie was independently evicted, still allow a browser request
			// whose Origin / Fetch Metadata proves it is same-origin; otherwise
			// the HttpOnly session cookie could survive while the UI logs out.
			if r.URL.Path == "/api/auth/logout/" && logoutOriginAllowed(r, devMode) {
				next.ServeHTTP(w, r)
				return
			}
			if cookieErr != nil || c.Value == "" {
				writeSimpleError(w, http.StatusForbidden, "CSRF cookie missing")
				return
			}
			writeSimpleError(w, http.StatusForbidden, "CSRF verification failed")
		})
	}
}

// RequestInfo decorates the request with a request ID and a scoped logger.
func RequestInfo() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := randomID()
			logger := logging.FromContext(r.Context()).With(logging.KeyRequestID, id)
			ctx := logging.IntoContext(r.Context(), logger)
			ctx = context.WithValue(ctx, requestIDKey, id)
			w.Header().Set("X-Request-Id", id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Recovery converts panics into 500s instead of killing connections.
func Recovery() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logging.FromContext(r.Context()).Error("panic recovered",
						"panic", rec, "path", r.URL.Path)
					writeSimpleError(w, http.StatusInternalServerError, "internal error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// Metrics records per-route latency and counts.
func Metrics(m *telemetry.Metrics, routeNormalizer func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			route := routeNormalizer(r)
			if route != "" {
				status := itoa(sw.status)
				m.HTTPDuration.WithLabelValues(route, r.Method, status).Observe(time.Since(start).Seconds())
				m.HTTPRequests.WithLabelValues(route, r.Method, status).Inc()
			}
		})
	}
}

// statusWriter records the status code for metrics. It must forward
// Flush (SSE lives behind it) and expose Unwrap so
// http.ResponseController can reach the underlying writer.
type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusWriter) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

// Flush forwards to the underlying writer — required by the SSE gateway
// (a wrapper without Flush makes w.(http.Flusher) fail and kills streams).
func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the real ResponseWriter.
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
