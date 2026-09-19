package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	genapi "github.com/creation-agent-studio/backend-go/internal/gen/api"
	"github.com/creation-agent-studio/backend-go/internal/identity"
)

// The OpenAPI contract declares q maxLength: 200, but oapi-codegen generates
// plain structs without constraint validation — the runtime gate in
// ListSchedules is what actually enforces it (四次复审 P2-6).

func TestScheduleQueryTooLongCountsRunesNotBytes(t *testing.T) {
	tests := []struct {
		name string
		q    string
		want bool
	}{
		{name: "empty", q: "", want: false},
		{name: "short ascii", q: "inventory", want: false},
		{name: "exactly 200 ascii", q: strings.Repeat("a", 200), want: false},
		{name: "201 ascii", q: strings.Repeat("a", 201), want: true},
		// 200 CJK runes are 600 BYTES — the contract limits characters.
		{name: "exactly 200 cjk (600 bytes)", q: strings.Repeat("好", 200), want: false},
		{name: "201 cjk", q: strings.Repeat("好", 201), want: true},
		// Padding is trimmed before counting: 200 real runes + padding pass.
		{name: "padded 200 runes", q: "   " + strings.Repeat("好", 200) + "  ", want: false},
		{name: "padded 201 runes", q: " " + strings.Repeat("好", 201), want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := scheduleQueryTooLong(tc.q); got != tc.want {
				t.Fatalf("scheduleQueryTooLong(%q) = %v, want %v", tc.q, got, tc.want)
			}
		})
	}
}

func TestListSchedulesRejectsOverlongQueryAtRuntime(t *testing.T) {
	// A nil Schedules service proves the gate rejects BEFORE the request can
	// reach the domain: a 400 must come back without touching the store.
	s := &Server{}
	q := strings.Repeat("好", 201)
	params := genapi.ListSchedulesParams{Q: &q}

	r := httptest.NewRequest(http.MethodGet, "/api/v2/schedules?q="+q, nil)
	r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &AuthenticatedUser{
		User: &identity.User{ID: 1, IsActive: true},
	}))
	w := httptest.NewRecorder()
	s.ListSchedules(w, r, params)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	if body := w.Body.String(); !strings.Contains(body, "200") {
		t.Fatalf("body should name the 200-character limit, got %s", body)
	}
}

func TestListSchedulesQueryGateRunsAfterAuth(t *testing.T) {
	// An overlong q without a caller is 401, not 400 — the gate must not
	// leak validation feedback to unauthenticated traffic.
	s := &Server{}
	q := strings.Repeat("好", 201)
	params := genapi.ListSchedulesParams{Q: &q}

	r := httptest.NewRequest(http.MethodGet, "/api/v2/schedules", nil)
	w := httptest.NewRecorder()
	s.ListSchedules(w, r, params)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}
