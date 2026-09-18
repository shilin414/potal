package http

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/identity"
	"github.com/creation-agent-studio/backend-go/internal/platform/config"
)

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
