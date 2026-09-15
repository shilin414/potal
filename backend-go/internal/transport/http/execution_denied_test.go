package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/creation-agent-studio/backend-go/internal/catalog"
)

// TestWriteExecutionDeniedProviderMissingIsConflict (第四轮 P2): the
// provider-registration sentinel used to fall through to the default
// "404 application not found", hiding a broken provider registration from
// the only caller allowed to see it (staff).
func TestWriteExecutionDeniedProviderMissingIsConflict(t *testing.T) {
	w := httptest.NewRecorder()
	writeExecutionDenied(w, catalog.ErrExecutionProviderMissing)
	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409 (provider is not registered)", w.Code)
	}
}

// TestWriteExecutionDeniedLeaksNothingToRegularCallers (control): an
// unknown/unmapped refusal still collapses to 404 for everyone.
func TestWriteExecutionDeniedLeaksNothingToRegularCallers(t *testing.T) {
	w := httptest.NewRecorder()
	writeExecutionDenied(w, catalog.ErrExecutionForbidden)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", w.Code)
	}
}
