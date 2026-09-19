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

// 四次复审 P2-R1: the metric label mapper must not fold the three static
// catalog routes into /applications/{id}. Otherwise `page`, `resolve` and
// `resolve-mention` share one request counter / error rate / latency
// histogram with every single-row lookup.
func TestNormalizeRouteKeepsStaticCatalogRoutesDistinct(t *testing.T) {
	cases := map[string]string{
		"/api/v2/applications/page":            "/api/v2/applications/page",
		"/api/v2/applications/resolve":         "/api/v2/applications/resolve",
		"/api/v2/applications/resolve-mention": "/api/v2/applications/resolve-mention",
		"/api/v2/applications/42":              "/api/v2/applications/{id}",
		"/api/v2/applications/42/avatar":       "/api/v2/applications/{id}/avatar",
		"/api/v2/applications/42/favorite":     "/api/v2/applications/{id}/favorite",
		"/api/v2/workspace/bootstrap":          "/api/v2/workspace/bootstrap",
		"/api/v2/tasks":                        "/api/v2/tasks",
		"/api/v2/tasks/42":                     "/api/v2/tasks/{id}",
		"/api/v2/applications":                 "/api/v2/applications",
	}
	for path, want := range cases {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if got := normalizeRoute(req); got != want {
			t.Fatalf("normalizeRoute(%q) = %q, want %q", path, got, want)
		}
	}
}

// 四次复审 P1-R3: the avatar response must be a per-Cookie cache variant.
// `private, immutable` alone still lets the same browser answer user B from
// user A's cached bytes without hitting the visibility check.
func TestAvatarCacheHeadersVaryByCookie(t *testing.T) {
	rec := httptest.NewRecorder()
	writeAvatarCacheHeaders(rec, `"abc123"`)

	if got := rec.Header().Get("Vary"); got != "Cookie" {
		t.Fatalf("Vary = %q, want Cookie", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := rec.Header().Get("ETag"); got != `"abc123"` {
		t.Fatalf("ETag = %q", got)
	}
}
