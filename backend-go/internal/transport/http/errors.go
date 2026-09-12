// Package http is the transport layer: chi routing, middleware and the
// generated ServerInterface implementation. Handlers only adapt HTTP to
// domain calls — no business logic lives here.
package http

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/creation-agent-studio/backend-go/internal/identity"
)

// Error envelopes reproduce the four legacy shapes the validated frontend
// depends on (see openapi.yaml):
//   1. {"detail": "..."}          — 403/404
//   2. {"error": "..."}           — simple JSON errors (409/502/identity)
//   3. {"field": ["msg", ...]}    — field validation
//   4. ["msg"]                    — bare array 400s

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeDetail(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]string{"detail": detail})
}

func writeSimpleError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeBare(w http.ResponseWriter, status int, msgs ...string) {
	writeJSON(w, status, msgs)
}

func writeFieldErrors(w http.ResponseWriter, fieldErrs map[string][]string) {
	writeJSON(w, http.StatusBadRequest, fieldErrs)
}

// mapDomainError translates domain sentinel errors to the exact HTTP
// envelope the frontend expects.
func mapDomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrNotFound):
		writeDetail(w, http.StatusNotFound, "not found")
	default:
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
	}
}
