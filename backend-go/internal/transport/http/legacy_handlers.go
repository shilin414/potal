package http

import (
	"net/http"
)

// Legacy compat shims: the Django "local creative agent" model becomes an
// Application in G12 (Codex/GraphFlow providers). Until then these return
// empty collections so the marketplace and agent editor render runtime
// agents without error toasts.
func (s *Server) ListLegacyAgents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}

func (s *Server) ListLegacyAgentCategories(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}

func (s *Server) ListSkills(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}
