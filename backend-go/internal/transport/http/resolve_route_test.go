package http

// /api/v2/applications/resolve vs /api/v2/applications/{id} (执行报告 §12).
//
// The generated router registers both; `ResolveApplication` would be dead
// code the moment chi preferred the parameter node, and the failure mode is
// nasty: a slug lookup would be parsed as an integer id and answer
// 400/404 while the endpoint looks present. This pins chi's precedence
// (static > param) without needing the whole server graph.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestStaticCatalogRoutesBeatTheIDParam(t *testing.T) {
	r := chi.NewRouter()
	r.Get("/api/v2/applications/{id}", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("param"))
	})
	r.Get("/api/v2/applications/resolve", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("resolve"))
	})
	r.Get("/api/v2/applications/resolve-mention", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("mention"))
	})
	r.Get("/api/v2/applications/page", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("page"))
	})

	for path, want := range map[string]string{
		"/api/v2/applications/resolve":         "resolve",
		"/api/v2/applications/resolve-mention": "mention",
		"/api/v2/applications/page":            "page",
		"/api/v2/applications/42":              "param",
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if got := rec.Body.String(); got != want {
			t.Fatalf("%s routed to %q, want %q", path, got, want)
		}
	}
}
