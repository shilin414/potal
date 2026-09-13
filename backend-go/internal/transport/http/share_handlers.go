package http

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/creation-agent-studio/backend-go/internal/platform/dbtypes"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

// ────────────────────────────────────────────── conversation shares ──
//
// A share is a server-side snapshot: the selected messages (with their
// artifact references) are persisted with a random token at creation time,
// and the public read endpoint returns ONLY those messages. Filtering must
// never move to the client — a URL that merely names message ids over an
// endpoint returning the whole conversation leaks everything the moment the
// suffix is stripped.
//
// Artifacts stay metadata-only: the file bytes keep living at the provider,
// and the receiver resolves them through the token-scoped
// /public/shares/{token}/artifacts/{id}/open endpoint, which 302s to a fresh
// provider signed URL after checking the artifact belongs to the snapshot.

// newShareToken returns a 256-bit random hex token (unguessable, URL-safe).
func newShareToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// shareArtifactRef / shareMessageEntry define the persisted snapshot shape.
type shareArtifactRef struct {
	ArtifactID string `json:"artifact_id"`
	Name       string `json:"name"`
}

type shareMessageEntry struct {
	ID        int64              `json:"id"`
	Artifacts []shareArtifactRef `json:"artifacts"`
}

// CreateConversationShare implements POST /api/v2/conversations/{id}/shares/.
func (s *Server) CreateConversationShare(w http.ResponseWriter, r *http.Request, conversationId int) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	var body struct {
		MessageIds []int64 `json:"message_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeDetail(w, http.StatusBadRequest, "invalid json")
		return
	}
	if len(body.MessageIds) == 0 {
		writeDetail(w, http.StatusBadRequest, "message_ids 不能为空")
		return
	}
	ctx := r.Context()
	q := s.Runs.Querier()
	conv, err := q.GetConversationByID(ctx, uint64(conversationId))
	if err != nil || conv.UserID != uint64(caller.ID) {
		writeDetail(w, http.StatusNotFound, "conversation not found")
		return
	}
	// Validate every requested id against the conversation and canonicalise
	// the order to the conversation's message order (no client-controlled
	// sequence, no ids smuggled from another conversation).
	msgs, err := q.ListMessagesByConversation(ctx, uint64(conversationId))
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	pos := make(map[uint64]int, len(msgs))
	for i, m := range msgs {
		pos[m.ID] = i
	}
	requested := make([]int, 0, len(body.MessageIds))
	for _, id := range body.MessageIds {
		if id <= 0 {
			writeDetail(w, http.StatusBadRequest, "message_ids 含有无效的消息 ID")
			return
		}
		if _, ok := pos[uint64(id)]; !ok {
			writeDetail(w, http.StatusBadRequest, "所选消息不属于当前对话")
			return
		}
		requested = append(requested, int(id))
	}
	sort.Slice(requested, func(a, b int) bool { return pos[uint64(requested[a])] < pos[uint64(requested[b])] })

	// Persist the snapshot: selected message ids plus their artifact
	// references (metadata only) so public viewers can resolve inline files.
	byID := make(map[uint64]db.Message, len(msgs))
	for _, m := range msgs {
		byID[m.ID] = m
	}
	entries := make([]shareMessageEntry, 0, len(requested))
	for _, id := range requested {
		entry := shareMessageEntry{ID: int64(id)}
		var meta map[string]any
		if raw := byID[uint64(id)].Metadata; len(raw) > 0 && string(raw) != "null" {
			if json.Unmarshal(raw, &meta) == nil && meta != nil {
				entry.Artifacts = shareArtifactsFromMeta(meta["artifacts"])
			}
		}
		entries = append(entries, entry)
	}
	snapshotJSON, err := json.Marshal(entries)
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	token, err := newShareToken()
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := q.CreateConversationShare(ctx, db.CreateConversationShareParams{
		Token:          token,
		ConversationID: uint64(conversationId),
		UserID:         uint64(caller.ID),
		Snapshot:       dbtypes.JSONText(snapshotJSON),
	}); err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"share_token":   token,
		"message_count": len(requested),
		"created_at":    iso(conv.UpdatedAt),
	})
}

// shareArtifactsFromMeta extracts {artifact_id, name} pairs from a message
// metadata "artifacts" array, tolerating arbitrary extra fields.
func shareArtifactsFromMeta(raw any) []shareArtifactRef {
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return nil
	}
	out := make([]shareArtifactRef, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["artifact_id"].(string)
		if id == "" {
			id, _ = m["external_artifact_id"].(string)
		}
		if id == "" {
			continue
		}
		name, _ := m["name"].(string)
		out = append(out, shareArtifactRef{ArtifactID: id, Name: name})
	}
	return out
}

// RevokeConversationShare implements DELETE
// /api/v2/conversations/{id}/shares/{token}/ — public links stop resolving.
func (s *Server) RevokeConversationShare(w http.ResponseWriter, r *http.Request, conversationId int, shareToken string) {
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
	if err := q.RevokeConversationShare(ctx, db.RevokeConversationShareParams{
		Token:  shareToken,
		UserID: uint64(caller.ID),
	}); err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"detail": "分享已撤销"})
}

// GetPublicShare implements GET /api/v2/public/shares/{token}/ (public route).
func (s *Server) GetPublicShare(w http.ResponseWriter, r *http.Request, shareToken string) {
	ctx := r.Context()
	q := s.Runs.Querier()
	share, err := q.GetConversationShareByToken(ctx, shareToken)
	if err != nil || share.RevokedAt.Valid {
		writeDetail(w, http.StatusNotFound, "分享不存在或已撤销")
		return
	}
	var entries []shareMessageEntry
	if err := json.Unmarshal(share.Snapshot, &entries); err != nil || len(entries) == 0 {
		writeDetail(w, http.StatusNotFound, "分享不存在或已撤销")
		return
	}
	conv, err := q.GetConversationByID(ctx, share.ConversationID)
	if err != nil {
		writeDetail(w, http.StatusNotFound, "分享不存在或已撤销")
		return
	}
	// Snapshot filtering happens HERE, server-side: only the snapshotted
	// messages leave the database, never the rest of the conversation.
	msgs, err := q.ListMessagesByConversation(ctx, share.ConversationID)
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	byID := make(map[uint64]db.Message, len(msgs))
	for _, m := range msgs {
		byID[m.ID] = m
	}
	shared := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		m, ok := byID[uint64(e.ID)]
		if !ok {
			continue // message deleted after the snapshot was taken
		}
		item := map[string]any{
			"role":       m.Role,
			"content":    m.Content,
			"created_at": iso(m.CreatedAt),
		}
		if len(e.Artifacts) > 0 {
			item["artifacts"] = e.Artifacts
		}
		shared = append(shared, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"title":     conv.Title,
		"shared_at": iso(share.CreatedAt),
		"messages":  shared,
	})
}

// OpenPublicShareArtifact implements GET
// /api/v2/public/shares/{token}/artifacts/{id}/open (public route).
func (s *Server) OpenPublicShareArtifact(w http.ResponseWriter, r *http.Request, shareToken string, artifactId openapi_types.UUID) {
	ctx := r.Context()
	q := s.Runs.Querier()
	share, err := q.GetConversationShareByToken(ctx, shareToken)
	if err != nil || share.RevokedAt.Valid {
		writeDetail(w, http.StatusNotFound, "分享不存在或已撤销")
		return
	}
	var entries []shareMessageEntry
	if err := json.Unmarshal(share.Snapshot, &entries); err != nil {
		writeDetail(w, http.StatusNotFound, "分享不存在或已撤销")
		return
	}
	// Capability check: only artifacts referenced by the snapshot may be
	// resolved — this endpoint must never become an open artifact proxy.
	requested := strings.ToLower(artifactId.String())
	allowed := false
	for _, e := range entries {
		for _, a := range e.Artifacts {
			if strings.EqualFold(a.ArtifactID, requested) {
				allowed = true
				break
			}
		}
	}
	if !allowed {
		writeDetail(w, http.StatusNotFound, "产物不在分享范围内")
		return
	}
	artID, err := ids.Parse(requested)
	if err != nil {
		writeDetail(w, http.StatusNotFound, "artifact not found")
		return
	}
	row, err := q.GetRunArtifactByID(ctx, artID.Bytes())
	if err != nil {
		writeDetail(w, http.StatusNotFound, "artifact not found")
		return
	}
	run, err := s.Runs.GetRun(ctx, mustIDFromBytes(row.RunID))
	if err != nil {
		writeDetail(w, http.StatusNotFound, "artifact not found")
		return
	}
	s.serveArtifactRedirect(w, r, row, run)
}
