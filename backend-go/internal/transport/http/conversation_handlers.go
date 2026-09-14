package http

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	genapi "github.com/creation-agent-studio/backend-go/internal/gen/api"
	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/identity"
	"github.com/creation-agent-studio/backend-go/internal/platform/crypto"
)

// ────────────────────────────────────────────── conversation history ──

// conversationSummaryRow mirrors the sidebar payload (ConversationSummary).
type conversationSummaryRow struct {
	ID            int64          `json:"id"`
	Title         string         `json:"title"`
	ApplicationID *int64         `json:"application_id"`
	MessageCount  int64          `json:"message_count"`
	UpdatedAt     string         `json:"updated_at"`
	LastMessage   *lastMessagePO `json:"last_message"`
}

type lastMessagePO struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
}

// ListConversations implements GET /api/conversations/ (sidebar history).
func (s *Server) ListConversations(w http.ResponseWriter, r *http.Request, params genapi.ListConversationsParams) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	ctx := r.Context()
	q := s.Runs.Querier()

	var (
		rows []conversationSummaryRow
		err  error
	)
	if params.ApplicationId != nil && *params.ApplicationId > 0 {
		items, e := q.ListConversationsForUserApp(ctx, db.ListConversationsForUserAppParams{
			UserID:        uint64(caller.ID),
			ApplicationID: sql.NullInt64{Int64: int64(*params.ApplicationId), Valid: true},
		})
		err = e
		for _, it := range items {
			rows = append(rows, summaryFromItem(int64(it.ID), it.Title, it.ApplicationID, int64(it.MessageCount), it.UpdatedAt, it.LastRole, it.LastContent, it.LastCreated))
		}
	} else {
		items, e := q.ListConversationsForUser(ctx, uint64(caller.ID))
		err = e
		for _, it := range items {
			rows = append(rows, summaryFromItem(int64(it.ID), it.Title, it.ApplicationID, int64(it.MessageCount), it.UpdatedAt, it.LastRole, it.LastContent, it.LastCreated))
		}
	}
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rows == nil {
		rows = []conversationSummaryRow{}
	}
	writeJSON(w, http.StatusOK, rows)
}

func summaryFromItem(id int64, title string, appID sql.NullInt64, count int64, updated time.Time, lastRole, lastContent string, lastCreated time.Time) conversationSummaryRow {
	out := conversationSummaryRow{
		ID:           id,
		Title:        title,
		MessageCount: count,
		UpdatedAt:    iso(updated),
	}
	if appID.Valid {
		v := appID.Int64
		out.ApplicationID = &v
	}
	if lastRole != "" && !lastCreated.IsZero() {
		out.LastMessage = &lastMessagePO{
			Role:      lastRole,
			Content:   lastContent,
			CreatedAt: iso(lastCreated),
		}
	}
	return out
}

// GetConversation implements GET /api/conversations/{id}/ — history replay
// (messages carry metadata.run_id and metadata.artifacts[] summaries) and
// the deep-link resolve (application_id).
func (s *Server) GetConversation(w http.ResponseWriter, r *http.Request, conversationId int) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	ctx := r.Context()
	q := s.Runs.Querier()
	conv, err := q.GetConversationByID(ctx, uint64(conversationId))
	if err != nil || conv.UserID != uint64(caller.ID) {
		writeDetail(w, http.StatusNotFound, "conversation not found")
		return
	}
	msgs, err := q.ListMessagesByConversation(ctx, uint64(conversationId))
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	messageItems := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		item := map[string]any{
			"id":         int64(m.ID),
			"role":       m.Role,
			"content":    m.Content,
			"created_at": iso(m.CreatedAt),
			"metadata":   nil,
		}
		if len(m.Metadata) > 0 && string(m.Metadata) != "null" {
			var meta map[string]any
			if json.Unmarshal(m.Metadata, &meta) == nil && meta != nil {
				item["metadata"] = meta
			}
		}
		messageItems = append(messageItems, item)
	}
	out := map[string]any{
		"id":             int64(conv.ID),
		"title":          conv.Title,
		"application_id": nullableID(conv.ApplicationID),
		"created_at":     iso(conv.CreatedAt),
		"updated_at":     iso(conv.UpdatedAt),
		"messages":       messageItems,
	}
	writeJSON(w, http.StatusOK, out)
}

func nullableID(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return v.Int64
}

// DeleteConversation implements DELETE /api/conversations/{id}/delete_conversation/.
// Full cascade: runs (+events/artifacts/commands/leases), messages,
// thread, attachments, then the conversation row.
//
// Lifetime safety (评测 P1-4): the whole cascade runs in ONE transaction
// and REFUSES to delete while the conversation still has a queued/running
// run — otherwise a worker's FinalizeOwnedRun could insert the assistant
// message into (or race child deletes against) a half-deleted
// conversation, leaving orphan rows.
func (s *Server) DeleteConversation(w http.ResponseWriter, r *http.Request, conversationId int) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	ctx := r.Context()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer func() { _ = tx.Rollback() }()
	q := db.New(tx)

	// Lock the conversation row first: it serializes with CreateRunInTx
	// (which takes the same lock before counting active runs), so no run
	// can be created between our active-run check and the delete.
	if _, err := q.GetConversationRowForUpdate(ctx, uint64(conversationId)); err != nil {
		writeDetail(w, http.StatusNotFound, "conversation not found")
		return
	}
	owner, err := q.GetConversationByID(ctx, uint64(conversationId))
	if err != nil || owner.UserID != uint64(caller.ID) {
		writeDetail(w, http.StatusNotFound, "conversation not found")
		return
	}
	active, err := q.CountActiveRunsByConversation(ctx, sql.NullInt64{Int64: int64(conversationId), Valid: true})
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if active > 0 {
		writeDetail(w, http.StatusConflict, "conversation has an active run")
		return
	}

	runIDs, err := q.DeleteConversationRuns(ctx, sql.NullInt64{Int64: int64(conversationId), Valid: true})
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, runID := range runIDs {
		if err := q.DeleteRunEvents(ctx, runID); err != nil {
			writeSimpleError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := q.DeleteRunArtifacts(ctx, runID); err != nil {
			writeSimpleError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := q.DeleteRunCommands(ctx, runID); err != nil {
			writeSimpleError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := q.DeleteRunLeaseByRun(ctx, runID); err != nil {
			writeSimpleError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	convArg := sql.NullInt64{Int64: int64(conversationId), Valid: true}
	if err := q.DeleteRunsByConversation(ctx, convArg); err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := q.DeleteConversationMessages(ctx, uint64(conversationId)); err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := q.DeleteThreadByConversation(ctx, uint64(conversationId)); err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := q.DeleteSharesByConversation(ctx, uint64(conversationId)); err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := q.UnbindConversationAttachments(ctx, convArg); err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := q.DeleteConversation(ctx, uint64(conversationId)); err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"detail": "会话已删除"})
}

// ClearConversation implements DELETE /api/conversations/{id}/clear/.
//
// Semantics (评测 P1-3): 清空 = 重新开始. Messages AND the agent thread are
// removed together, so the next turn starts a NEW provider session — a
// cleared conversation must not keep answering with the old context. It
// refuses while a run is active (the answer of an in-flight run would
// otherwise reappear in a conversation the user just emptied).
func (s *Server) ClearConversation(w http.ResponseWriter, r *http.Request, conversationId int) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	ctx := r.Context()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer func() { _ = tx.Rollback() }()
	q := db.New(tx)

	if _, err := q.GetConversationRowForUpdate(ctx, uint64(conversationId)); err != nil {
		writeDetail(w, http.StatusNotFound, "conversation not found")
		return
	}
	conv, err := q.GetConversationByID(ctx, uint64(conversationId))
	if err != nil || conv.UserID != uint64(caller.ID) {
		writeDetail(w, http.StatusNotFound, "conversation not found")
		return
	}
	active, err := q.CountActiveRunsByConversation(ctx, sql.NullInt64{Int64: int64(conversationId), Valid: true})
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if active > 0 {
		writeDetail(w, http.StatusConflict, "conversation has an active run")
		return
	}
	if err := q.DeleteConversationMessages(ctx, uint64(conversationId)); err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Reset the provider session too: deleting the thread makes the next
	// turn lazily create a fresh one (and a fresh Aily session).
	if err := q.DeleteThreadByConversation(ctx, uint64(conversationId)); err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := q.TouchConversationUpdated(ctx, uint64(conversationId)); err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"detail": "会话已清空"})
}

// ─────────────────────────────────────────────────────────── register ──

// AuthRegister implements POST /api/auth/register/ (password → Argon2id).
func (s *Server) AuthRegister(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username        string `json:"username"`
		Email           string `json:"email"`
		Password        string `json:"password"`
		PasswordConfirm string `json:"password_confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeFieldErrors(w, map[string][]string{"body": {"invalid json"}})
		return
	}
	fieldErrs := map[string][]string{}
	body.Username = strings.TrimSpace(body.Username)
	if body.Username == "" {
		fieldErrs["username"] = []string{"请输入用户名"}
	}
	if body.Password == "" {
		fieldErrs["password"] = []string{"请输入密码"}
	}
	if body.Password != body.PasswordConfirm {
		fieldErrs["password_confirm"] = []string{"两次输入的密码不一致"}
	}
	if len(fieldErrs) > 0 {
		writeFieldErrors(w, fieldErrs)
		return
	}
	ctx := r.Context()
	if _, err := s.IdentityRepo.UserByUsername(ctx, body.Username); err == nil {
		writeFieldErrors(w, map[string][]string{"username": {"用户名已被占用"}})
		return
	}
	hash, err := crypto.HashPassword(body.Password)
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := s.DB.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, display_name, display_id, email, role, auth_source, is_staff)
		 VALUES (?, ?, ?, '', ?, 'creator', 'local_admin', 0)`,
		body.Username, hash, body.Username, body.Email); err != nil {
		if strings.Contains(err.Error(), "Duplicate") || strings.Contains(err.Error(), "duplicate") {
			writeFieldErrors(w, map[string][]string{"username": {"用户名已被占用"}})
			return
		}
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	user, err := s.IdentityRepo.UserByUsername(ctx, body.Username)
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.setSessionCookie(w, r, user)
	writeJSON(w, http.StatusCreated, map[string]any{
		"user":   identity.SessionPayload(user, nil),
		"tokens": map[string]string{"access": "", "refresh": ""},
	})
}
