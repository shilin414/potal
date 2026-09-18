package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/identity"
	"github.com/creation-agent-studio/backend-go/internal/platform/config"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
)

type fakeSessionStore struct {
	session          *identity.Session
	revokeErr        error
	revokedToken     string
	revokeContextErr error
	refreshCount     int
}

func (f *fakeSessionStore) CookieName() string { return "studio_session" }
func (f *fakeSessionStore) CSRFName() string   { return "studio_csrf" }
func (f *fakeSessionStore) Create(context.Context, identity.Session) (string, string, error) {
	return "", "", errors.New("not implemented")
}
func (f *fakeSessionStore) Get(context.Context, string) (*identity.Session, error) {
	if f.session == nil {
		return nil, identity.ErrNotFound
	}
	return f.session, nil
}
func (f *fakeSessionStore) Revoke(ctx context.Context, token string) error {
	f.revokedToken = token
	f.revokeContextErr = ctx.Err()
	return f.revokeErr
}
func (f *fakeSessionStore) Refresh(context.Context, string) { f.refreshCount++ }

type fakeIdentityUserResolver struct {
	user *identity.User
}

func (f fakeIdentityUserResolver) UserWithIdentity(context.Context, int64) (*identity.User, *identity.FeishuIdentity, error) {
	return f.user, nil, nil
}

func TestCSRFSameOriginLogoutFallback(t *testing.T) {
	store := &fakeSessionStore{}
	tests := []struct {
		name       string
		path       string
		origin     string
		fetchSite  string
		devMode    bool
		wantStatus int
	}{
		{name: "same origin logout", path: "/api/auth/logout/", origin: "http://example.com", wantStatus: http.StatusNoContent},
		{name: "fetch metadata logout", path: "/api/auth/logout/", fetchSite: "same-origin", wantStatus: http.StatusNoContent},
		{name: "approved dev origin", path: "/api/auth/logout/", origin: "http://localhost:3030", devMode: true, wantStatus: http.StatusNoContent},
		{name: "cross origin logout", path: "/api/auth/logout/", origin: "http://evil.example", wantStatus: http.StatusForbidden},
		{name: "ordinary mutation still requires token", path: "/api/v2/applications/1", origin: "http://example.com", wantStatus: http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})
			handler := CSRF(store, tc.devMode)(next)
			request := httptest.NewRequest(http.MethodPost, "http://example.com"+tc.path, nil)
			if tc.origin != "" {
				request.Header.Set("Origin", tc.origin)
			}
			if tc.fetchSite != "" {
				request.Header.Set("Sec-Fetch-Site", tc.fetchSite)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.wantStatus)
			}
		})
	}
}

func TestSessionAuthDoesNotRefreshLogoutSession(t *testing.T) {
	store := &fakeSessionStore{session: &identity.Session{UserID: 7}}
	repo := fakeIdentityUserResolver{user: &identity.User{ID: 7, Username: "admin", IsActive: true}}
	handler := SessionAuth(store, repo)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if userFrom(r.Context()) == nil {
			t.Fatal("session was not resolved")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodPost, "/api/auth/logout/", nil)
	request.AddCookie(&http.Cookie{Name: "studio_session", Value: "token"})
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if store.refreshCount != 0 {
		t.Fatalf("logout refreshed the session %d time(s)", store.refreshCount)
	}
}

func TestSessionAuthRefreshesOrdinaryAuthenticatedRequest(t *testing.T) {
	store := &fakeSessionStore{session: &identity.Session{UserID: 7}}
	repo := fakeIdentityUserResolver{user: &identity.User{ID: 7, Username: "admin", IsActive: true}}
	handler := SessionAuth(store, repo)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	request := httptest.NewRequest(http.MethodGet, "/api/auth/session", nil)
	request.AddCookie(&http.Cookie{Name: "studio_session", Value: "token"})
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if store.refreshCount != 1 {
		t.Fatalf("ordinary request refreshed session %d time(s), want 1", store.refreshCount)
	}
}

func TestSessionAuthRevokesInactiveUserSession(t *testing.T) {
	store := &fakeSessionStore{session: &identity.Session{UserID: 7}}
	repo := fakeIdentityUserResolver{user: &identity.User{ID: 7, Username: "disabled", IsStaff: true, IsActive: false}}
	reached := false
	handler := SessionAuth(store, repo)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		if userFrom(r.Context()) != nil {
			t.Fatal("inactive user authenticated")
		}
	}))
	request := httptest.NewRequest(http.MethodGet, "/api/auth/session", nil)
	request.AddCookie(&http.Cookie{Name: "studio_session", Value: "token"})
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if !reached || store.revokedToken != "token" || store.refreshCount != 0 {
		t.Fatalf("reached=%v revoked=%q refresh=%d", reached, store.revokedToken, store.refreshCount)
	}
}

func TestClientIPTrustsOnlyConfiguredProxyPeer(t *testing.T) {
	tests := []struct {
		name       string
		server     *Server
		remoteAddr string
		xRealIP    string
		want       string
	}{
		{
			name:       "direct client headers ignored",
			server:     &Server{},
			remoteAddr: "203.0.113.9:43210",
			xRealIP:    "198.51.100.2",
			want:       "203.0.113.9",
		},
		{
			name: "trusted proxy forwards validated client",
			server: &Server{Config: &config.Config{Auth: config.AuthConfig{
				TrustedProxyCIDRs: []string{"10.0.0.0/8"},
			}}},
			remoteAddr: "10.20.30.40:43210",
			xRealIP:    "198.51.100.2",
			want:       "198.51.100.2",
		},
		{
			name: "untrusted proxy cannot forward",
			server: &Server{Config: &config.Config{Auth: config.AuthConfig{
				TrustedProxyCIDRs: []string{"10.0.0.0/8"},
			}}},
			remoteAddr: "192.0.2.10:43210",
			xRealIP:    "198.51.100.2",
			want:       "192.0.2.10",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/identity/admin/login", nil)
			request.RemoteAddr = tc.remoteAddr
			request.Header.Set("X-Forwarded-For", "198.51.100.1")
			request.Header.Set("X-Real-IP", tc.xRealIP)
			request.Header.Set("True-Client-IP", "198.51.100.3")
			if got := tc.server.clientIP(request); got != tc.want {
				t.Fatalf("clientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLoginAttemptLimiterBlocksSixthFailure(t *testing.T) {
	limiter := newLoginAttemptLimiter(5, 5*time.Minute)
	for i := 0; i < 5; i++ {
		if !limiter.allow("admin|127.0.0.1") {
			t.Fatalf("attempt %d unexpectedly blocked", i+1)
		}
	}
	if limiter.allow("admin|127.0.0.1") {
		t.Fatal("sixth failed attempt must be blocked")
	}
}

func TestLoginAttemptLimiterSuccessResetsFailureBudget(t *testing.T) {
	limiter := newLoginAttemptLimiter(5, 5*time.Minute)
	key := "admin|127.0.0.1"
	for i := 0; i < 4; i++ {
		if !limiter.allow(key) {
			t.Fatalf("failure reservation %d unexpectedly blocked", i+1)
		}
	}

	// The handler calls reset immediately after successful credential checks.
	limiter.reset(key)
	for i := 0; i < 10; i++ {
		if !limiter.allow(key) {
			t.Fatalf("successful login %d unexpectedly consumed the failure budget", i+1)
		}
		limiter.reset(key)
	}

	if !limiter.allow("other-admin|127.0.0.1") {
		t.Fatal("another username must have an independent budget")
	}
}

func TestLoginAttemptLimiterSweepsExpiredKeys(t *testing.T) {
	now := time.Unix(1_000, 0)
	limiter := newLoginAttemptLimiter(5, time.Minute)
	limiter.now = func() time.Time { return now }
	limiter.sweepEvery = 1
	if !limiter.allow("old|127.0.0.1") {
		t.Fatal("initial key unexpectedly blocked")
	}
	now = now.Add(2 * time.Minute)
	if !limiter.allow("new|127.0.0.1") {
		t.Fatal("new key unexpectedly blocked after expiry sweep")
	}
	if _, exists := limiter.attempts["old|127.0.0.1"]; exists {
		t.Fatal("expired limiter key was not removed")
	}
}

func TestLoginAttemptLimiterBoundsDistinctKeys(t *testing.T) {
	limiter := newLoginAttemptLimiter(5, 5*time.Minute)
	limiter.maxKeys = 3
	limiter.sweepEvery = 1_000
	for _, key := range []string{"a|ip", "b|ip", "c|ip"} {
		if !limiter.allow(key) {
			t.Fatalf("key %q unexpectedly blocked before capacity", key)
		}
	}
	for i := 0; i < 5; i++ {
		if !limiter.allow("d|ip") {
			t.Fatalf("overflow attempt %d unexpectedly blocked", i+1)
		}
	}
	if limiter.allow("d|ip") {
		t.Fatal("overflow identity exceeded the per-key budget")
	}
	if got := len(limiter.attempts); got != 3 {
		t.Fatalf("limiter keys = %d, want hard bound 3", got)
	}
}

func TestLoginAttemptLimiterStaysBoundedUnderUsernameSpray(t *testing.T) {
	limiter := newLoginAttemptLimiter(5, 5*time.Minute)
	for i := 0; i < adminLoginMaxKeys; i++ {
		if !limiter.allow(fmt.Sprintf("user-%05d|127.0.0.1", i)) {
			t.Fatalf("key %d unexpectedly blocked before the configured bound", i)
		}
	}
	for i := 0; i < 5; i++ {
		if !limiter.allow("overflow|127.0.0.1") {
			t.Fatalf("overflow attempt %d unexpectedly blocked", i+1)
		}
	}
	if limiter.allow("overflow|127.0.0.1") {
		t.Fatal("overflow identity exceeded the bounded fallback budget")
	}
	if got := len(limiter.attempts); got != adminLoginMaxKeys {
		t.Fatalf("limiter keys = %d, want %d", got, adminLoginMaxKeys)
	}
}

func TestAdminLoginRequestBodyIsBounded(t *testing.T) {
	oversized := `{"username":"` + strings.Repeat("a", adminLoginBodyMaxBytes) + `","password":"x"}`
	tests := []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
		path    string
	}{
		{name: "admin", handler: (&Server{}).AdminLogin, path: "/api/identity/admin/login"},
		{name: "legacy", handler: (&Server{}).AuthLogin, path: "/api/auth/login/"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(oversized))
			tc.handler(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 for oversized login body", recorder.Code)
			}
		})
	}
}

func TestAdminLoginPeerBudgetBoundsUsernameSpray(t *testing.T) {
	server := &Server{}
	request := httptest.NewRequest(http.MethodPost, "/api/identity/admin/login", nil)
	request.RemoteAddr = "203.0.113.20:43210"
	for i := 0; i < adminLoginMaxPeerAttempts; i++ {
		if !server.allowAdminLogin(fmt.Sprintf("user-%03d", i), request) {
			t.Fatalf("attempt %d unexpectedly blocked before peer budget", i+1)
		}
	}
	if server.allowAdminLogin("overflow", request) {
		t.Fatal("same peer bypassed the independent username-spray budget")
	}
	if got := len(server.adminLoginAttempts().attempts); got != adminLoginMaxPeerAttempts {
		t.Fatalf("composite keys = %d, want %d", got, adminLoginMaxPeerAttempts)
	}
}

func TestAdminLoginCompositeCapacityFallsBackToPeerBudget(t *testing.T) {
	server := &Server{}
	keyLimiter := server.adminLoginAttempts()
	keyLimiter.maxKeys = 1
	first := httptest.NewRequest(http.MethodPost, "/api/identity/admin/login", nil)
	first.RemoteAddr = "203.0.113.30:1000"
	if !server.allowAdminLogin("first", first) {
		t.Fatal("first composite key unexpectedly blocked")
	}
	second := httptest.NewRequest(http.MethodPost, "/api/identity/admin/login", nil)
	second.RemoteAddr = "203.0.113.31:1000"
	for i := 0; i < adminLoginMaxAttempts; i++ {
		if !server.allowAdminLogin("second", second) {
			t.Fatalf("overflow identity attempt %d unexpectedly blocked", i+1)
		}
	}
	if server.allowAdminLogin("second", second) {
		t.Fatal("overflow identity bypassed the five-attempt composite budget")
	}
	if got := len(keyLimiter.attempts); got != 1 {
		t.Fatalf("composite map grew to %d entries, want hard bound 1", got)
	}
}

func TestNormalizeAdminUsername(t *testing.T) {
	if got, ok := normalizeAdminUsername("  admin  "); !ok || got != "admin" {
		t.Fatalf("normalized username = %q/%v, want admin/true", got, ok)
	}
	if _, ok := normalizeAdminUsername(""); ok {
		t.Fatal("empty username accepted")
	}
	if _, ok := normalizeAdminUsername(string(make([]rune, adminLoginUsernameMaxRunes+1))); ok {
		t.Fatal("overlong username accepted")
	}
}

func TestAuthLogoutExpiresSessionAndCSRFCookies(t *testing.T) {
	server := &Server{
		Config: &config.Config{Session: config.SessionConfig{Secure: true}},
		Store:  identity.NewSessionStore(nil, time.Hour, "studio_session", "studio_csrf"),
	}
	recorder := httptest.NewRecorder()
	server.AuthLogout(recorder, httptest.NewRequest("POST", "/api/auth/logout/", nil))

	cookies := recorder.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("logout cookies=%d, want 2: %#v", len(cookies), cookies)
	}
	byName := map[string]bool{}
	for _, cookie := range cookies {
		if cookie.MaxAge != -1 || cookie.Value != "" || cookie.Path != "/" || !cookie.Secure {
			t.Fatalf("cookie %s was not fully expired: %#v", cookie.Name, cookie)
		}
		byName[cookie.Name] = true
		if cookie.Name == "studio_session" && !cookie.HttpOnly {
			t.Fatal("session cookie must remain HttpOnly when expired")
		}
		if cookie.Name == "studio_csrf" && cookie.HttpOnly {
			t.Fatal("CSRF cookie must remain readable when expired")
		}
	}
	if !byName["studio_session"] || !byName["studio_csrf"] {
		t.Fatalf("logout did not expire the full cookie pair: %#v", byName)
	}
}

func TestAuthLogoutClearsCookiesAndReportsRevokeFailure(t *testing.T) {
	store := &fakeSessionStore{revokeErr: errors.New("redis unavailable")}
	metrics := telemetry.NewMetrics("test")
	server := &Server{
		Config: &config.Config{Session: config.SessionConfig{Secure: true}},
		Store:  store,
		Metric: metrics,
	}
	request := httptest.NewRequest(http.MethodPost, "/api/auth/logout/", nil)
	canceledCtx, cancel := context.WithCancel(request.Context())
	cancel()
	request = request.WithContext(canceledCtx)
	request.AddCookie(&http.Cookie{Name: "studio_session", Value: "copied-token"})
	recorder := httptest.NewRecorder()

	server.AuthLogout(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("logout status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	if store.revokedToken != "copied-token" {
		t.Fatalf("revoked token = %q, want copied-token", store.revokedToken)
	}
	if store.revokeContextErr != nil {
		t.Fatalf("revoke inherited canceled client context: %v", store.revokeContextErr)
	}
	if cookies := recorder.Result().Cookies(); len(cookies) != 2 {
		t.Fatalf("logout failure still must expire both cookies, got %#v", cookies)
	}
	families, err := metrics.Registry.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, family := range families {
		if family.GetName() == "studio_session_revoke_failures_total" {
			if len(family.Metric) != 1 || family.Metric[0].Counter.GetValue() != 1 {
				t.Fatalf("revoke failure metric = %#v, want 1", family.Metric)
			}
			return
		}
	}
	t.Fatal("studio_session_revoke_failures_total was not emitted")
}
