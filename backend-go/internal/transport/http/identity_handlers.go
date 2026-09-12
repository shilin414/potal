package http

import (
	genapi "github.com/creation-agent-studio/backend-go/internal/gen/api"
)

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/identity"
)

// ──────────────────────────────────────────────────── OAuth endpoints ──

func (s *Server) OauthStart(w http.ResponseWriter, r *http.Request, params genapi.OauthStartParams) {
	if s.Config.Feishu.RedirectURI == "" {
		writeSimpleError(w, http.StatusInternalServerError, "FEISHU_REDIRECT_URI is not configured")
		return
	}
	returnTo := "/"
	if params.ReturnTo != nil {
		returnTo = identity.SanitizeReturnTo(*params.ReturnTo)
	}
	state := s.StateCodec.Dump(identity.OAuthState{ReturnTo: returnTo})
	http.Redirect(w, r, s.Feishu.AuthorizeURL(s.Config.Feishu.RedirectURI, state), http.StatusFound)
}

func (s *Server) OauthExchange(w http.ResponseWriter, r *http.Request, params genapi.OauthExchangeParams) {
	if params.Code == "" {
		writeSimpleError(w, http.StatusBadRequest, "missing code")
		return
	}
	state, err := s.StateCodec.Load(params.State)
	if err != nil {
		writeSimpleError(w, http.StatusBadRequest, "invalid or expired oauth state")
		return
	}
	result, _, err := s.Oauth.Exchange(r.Context(), params.Code)
	if err != nil {
		s.Log.Error("oauth exchange failed", "err", err)
		writeSimpleError(w, http.StatusBadGateway, "oauth exchange failed")
		return
	}
	s.setSessionCookie(w, r, result.User)
	writeJSON(w, http.StatusOK, map[string]any{
		"user":      identity.SessionPayload(result.User, result.Identity),
		"tokens":    map[string]string{"access": "", "refresh": ""},
		"return_to": state.ReturnTo,
	})
}

func (s *Server) GetSession(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	if user == nil {
		writeSimpleError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	writeJSON(w, http.StatusOK, identity.SessionPayload(user.User, user.Identity))
}

func (s *Server) AdminLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeSimpleError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeSimpleError(w, http.StatusBadRequest, "invalid json")
		return
	}
	user, err := s.IdentityRepo.VerifyLocalAdmin(r.Context(), body.Username, body.Password)
	if err != nil {
		writeSimpleError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	s.setSessionCookie(w, r, user)
	writeJSON(w, http.StatusOK, map[string]any{
		"id":           user.ID,
		"username":     user.Username,
		"is_staff":     user.IsStaff,
		"auth_source":  identity.AuthSourceLocalAdmin,
		"display_name": user.DisplayName,
		"display_id":   user.DisplayID,
	})
}

// ───────────────────────────────────────────── legacy auth endpoints ──

func (s *Server) AuthLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeDetail(w, http.StatusBadRequest, "invalid body")
		return
	}
	user, err := s.IdentityRepo.VerifyLocalAdmin(r.Context(), body.Username, body.Password)
	if err != nil {
		writeDetail(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	s.setSessionCookie(w, r, user)
	writeJSON(w, http.StatusOK, map[string]any{
		"user":   identity.SessionPayload(user, nil),
		"tokens": map[string]string{"access": "", "refresh": ""},
	})
}

func (s *Server) AuthLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(s.Store.CookieName()); err == nil {
		_ = s.Store.Revoke(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     s.Store.CookieName(),
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.Config.Session.Secure,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]string{"detail": "登出成功"})
}

func (s *Server) GetMe(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	if user == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	writeJSON(w, http.StatusOK, identity.SessionPayload(user.User, user.Identity))
}

func (s *Server) PatchMe(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	if user == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	var body struct {
		Email     *string `json:"email"`
		Bio       *string `json:"bio"`
		DisplayID *string `json:"display_id"`
		Username  *string `json:"username"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeFieldErrors(w, map[string][]string{"body": {"invalid json"}})
		return
	}
	newUsername := user.Username
	if body.Username != nil && *body.Username != "" {
		newUsername = *body.Username
	}
	newEmail := user.Email
	if body.Email != nil {
		newEmail = *body.Email
	}
	newBio := user.Bio
	if body.Bio != nil {
		newBio = *body.Bio
	}
	newDisplayID := user.DisplayID
	if body.DisplayID != nil {
		newDisplayID = *body.DisplayID
	}
	if _, err := s.DB.ExecContext(r.Context(),
		`UPDATE users SET username = ?, email = ?, bio = ?, display_id = ? WHERE id = ?`,
		newUsername, newEmail, newBio, newDisplayID, user.ID); err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	updated, ident, err := s.IdentityRepo.UserWithIdentity(r.Context(), user.ID)
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, identity.SessionPayload(updated, ident))
}

func (s *Server) TokenRefresh(w http.ResponseWriter, r *http.Request) {
	// The opaque-session backend no longer issues JWTs; the legacy shape
	// stays so old clients degrade gracefully instead of crashing.
	writeJSON(w, http.StatusOK, map[string]string{"access": "", "refresh": ""})
}

// ──────────────────────────────────────────────────────── cookies ──

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, user *identity.User) {
	token, csrf, err := s.Store.Create(r.Context(), identity.Session{
		UserID:     user.ID,
		Username:   user.Username,
		AuthSource: user.AuthSource,
		IsStaff:    user.IsStaff,
	})
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, "session create failed")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     s.Store.CookieName(),
		Value:    token,
		Path:     "/",
		MaxAge:   int(s.Config.Session.TTL / time.Second),
		HttpOnly: true,
		Secure:   s.Config.Session.Secure,
		SameSite: http.SameSiteLaxMode,
	})
	// Readable CSRF cookie for the SPA double-submit.
	http.SetCookie(w, &http.Cookie{
		Name:     s.Store.CSRFName(),
		Value:    csrf,
		Path:     "/",
		MaxAge:   int(s.Config.Session.TTL / time.Second),
		HttpOnly: false,
		Secure:   s.Config.Session.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}
