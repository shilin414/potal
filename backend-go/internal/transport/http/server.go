package http

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"github.com/creation-agent-studio/backend-go/internal/catalog"
	"github.com/creation-agent-studio/backend-go/internal/execution"
	genapi "github.com/creation-agent-studio/backend-go/internal/gen/api"
	"github.com/creation-agent-studio/backend-go/internal/identity"
	"github.com/creation-agent-studio/backend-go/internal/platform/config"
	"github.com/creation-agent-studio/backend-go/internal/platform/redisx"
	"github.com/creation-agent-studio/backend-go/internal/platform/storage"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
	"github.com/creation-agent-studio/backend-go/internal/transport/sse"
)

// Server implements genapi.ServerInterface. Every dependency is injected;
// no globals.
type Server struct {
	Config *config.Config
	DB     *sql.DB
	Log    *slog.Logger
	Metric *telemetry.Metrics
	Redis  *redisx.Client

	IdentityRepo *identity.Repo
	Store        *identity.SessionStore
	StateCodec   *identity.StateCodec
	Oauth        *identity.ExchangeOrchestrator
	Feishu       *identity.FeishuClient

	Catalog     *catalog.Service
	CatalogRepo *catalog.Repo
	Registry    *catalog.RuntimeRegistry

	Runs    *execution.Service
	Storage storage.Storage

	RateLimitArtifacts *execution.RateLimiter

	SSE *sse.Gateway

	router chi.Router
}

// publicRoutes are served without authentication (path+method table; the
// generated mux registers every route, auth is enforced by middleware).
var publicRoutes = map[string]bool{
	"GET /api/identity/oauth/start":     true,
	"GET /api/identity/oauth/exchange":  true,
	"POST /api/identity/admin/login":    true,
	"POST /api/auth/login/":             true,
	"POST /api/auth/logout/":            true,
	"POST /api/auth/token/refresh/":     true,
	"GET /api/v2/applications/*/avatar": true,
	"GET /api/v2/artifacts/*/open":      true,
	"GET /api/v2/runs/*/stream":         true,
}

// isInfraPath allows health/metrics probes without authentication.
func isInfraPath(path string) bool {
	return path == "/health/live" || path == "/health/ready" || path == "/metrics"
}

func isPublicRoute(method, path string) bool {
	if ok, exists := publicRoutes[method+" "+path]; exists {
		return ok
	}
	// Wildcard forms for parameterized paths.
	prefix := ""
	if i := strings.LastIndex(path, "/"); i > 0 {
		prefix = path[:i+1]
	}
	switch method + " " + prefix + "*" {
	case "GET /api/v2/applications/*/avatar":
		return strings.HasPrefix(path, "/api/v2/applications/") && strings.HasSuffix(path, "/avatar")
	case "GET /api/v2/artifacts/*/open":
		return strings.HasPrefix(path, "/api/v2/artifacts/") && strings.HasSuffix(path, "/open")
	case "GET /api/v2/runs/*/stream":
		return strings.HasPrefix(path, "/api/v2/runs/") && strings.HasSuffix(path, "/stream")
	}
	return false
}

// authMiddleware enforces authentication for everything not in publicRoutes.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicRoute(r.Method, r.URL.Path) || isInfraPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if userFrom(r.Context()) == nil {
			writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Router builds the chi router with middleware and generated routes.
func (s *Server) Router() http.Handler {
	if s.router != nil {
		return s.router
	}
	r := chi.NewRouter()
	r.Use(chimw.RealIP)
	r.Use(Recovery())
	r.Use(RequestInfo())
	r.Use(Metrics(s.Metric, normalizeRoute))
	r.Use(SessionAuth(s.Store, s.IdentityRepo))
	r.Use(s.authMiddleware)
	r.Use(CSRF(s.Store, s.Config.Env != "production"))

	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"http://localhost:3030", "http://localhost:3031", "http://localhost:3032", "http://127.0.0.1:3030"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Authorization", "Content-Type", "X-CSRF-Token", "X-Organization-ID", "X-Request-Id"},
		ExposedHeaders:   []string{"X-Request-Id"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	// Health & metrics (infra, outside the API contract).
	r.Get("/health/live", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	r.Get("/health/ready", s.handleReady)
	r.Get("/metrics", s.Metric.Handler().ServeHTTP)

	// All contract routes from the generated spec.
	genapi.HandlerFromMux(s, r)

	s.router = r
	return r
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.DB.PingContext(ctx); err != nil {
		writeSimpleError(w, http.StatusServiceUnavailable, "database not ready")
		return
	}
	if err := s.Redis.Ping(ctx).Err(); err != nil {
		writeSimpleError(w, http.StatusServiceUnavailable, "redis not ready")
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// normalizeRoute collapses ids for stable metric labels.
func normalizeRoute(r *http.Request) string {
	p := r.URL.Path
	if i := strings.Index(p, "/api/"); i > 0 {
		p = p[i:]
	}
	switch {
	case strings.HasPrefix(p, "/api/v2/applications/"):
		if strings.HasSuffix(p, "/avatar") {
			return "/api/v2/applications/{id}/avatar"
		}
		if strings.HasSuffix(p, "/favorite") {
			return "/api/v2/applications/{id}/favorite"
		}
		if strings.HasSuffix(p, "/default-agent") {
			return "/api/v2/applications/{id}/default-agent"
		}
		if strings.HasSuffix(p, "/attachments") {
			return "/api/v2/applications/{id}/attachments"
		}
		return "/api/v2/applications/{id}"
	case strings.HasPrefix(p, "/api/v2/runs/"):
		if strings.HasSuffix(p, "/events") {
			return "/api/v2/runs/{id}/events"
		}
		if strings.HasSuffix(p, "/stream") {
			return "/api/v2/runs/{id}/stream"
		}
		if strings.HasSuffix(p, "/commands") {
			return "/api/v2/runs/{id}/commands"
		}
		if strings.HasSuffix(p, "/attachments") {
			return "/api/v2/runs/{id}/attachments"
		}
		if strings.HasSuffix(p, "/artifacts") {
			return "/api/v2/runs/{id}/artifacts"
		}
		return "/api/v2/runs/{id}"
	case strings.HasPrefix(p, "/api/v2/artifacts/"):
		return "/api/v2/artifacts/{id}/open"
	}
	return p
}
