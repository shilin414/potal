package http

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	db "github.com/creation-agent-studio/backend-go/internal/gen/db"

	"github.com/creation-agent-studio/backend-go/internal/catalog"
	"github.com/creation-agent-studio/backend-go/internal/execution"
	genapi "github.com/creation-agent-studio/backend-go/internal/gen/api"
	"github.com/creation-agent-studio/backend-go/internal/integrations/aily"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
)

// runRecord mirrors the RunSerializer wire shape.
type runRecord struct {
	ID                   string         `json:"id"`
	Organization         *int64         `json:"organization"`
	User                 *int64         `json:"user"`
	Application          int64          `json:"application"`
	Conversation         int64          `json:"conversation"`
	RuntimeBinding       int64          `json:"runtime_binding"`
	Provider             string         `json:"provider"`
	RuntimeType          string         `json:"runtime_type"`
	ExternalRunID        string         `json:"external_run_id"`
	Status               string         `json:"status"`
	ProviderStatus       string         `json:"provider_status"`
	ProviderFinishReason string         `json:"provider_finish_reason"`
	Input                map[string]any `json:"input"`
	Output               map[string]any `json:"output"`
	Attempt              int64          `json:"attempt"`
	MaxAttempts          int64          `json:"max_attempts"`
	QueuedAt             string         `json:"queued_at"`
	StartedAt            *string        `json:"started_at"`
	FinishedAt           *string        `json:"finished_at"`
	ErrorCode            string         `json:"error_code"`
	ErrorMessage         string         `json:"error_message"`
	CreatedAt            string         `json:"created_at"`
	UpdatedAt            string         `json:"updated_at"`
}

func iso(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
func isoPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := iso(*t)
	return &s
}

func toRunRecord(run *execution.Run) runRecord {
	appID := int64(0)
	if run.ApplicationID != nil {
		appID = *run.ApplicationID
	}
	convID := int64(0)
	if run.ConversationID != nil {
		convID = *run.ConversationID
	}
	bindID := int64(0)
	if run.RuntimeBindingID != nil {
		bindID = *run.RuntimeBindingID
	}
	return runRecord{
		ID:                   run.ID.String(),
		User:                 run.UserID,
		Application:          appID,
		Conversation:         convID,
		RuntimeBinding:       bindID,
		Provider:             run.Provider,
		RuntimeType:          run.RuntimeType,
		ExternalRunID:        run.ExternalRunID,
		Status:               run.Status,
		ProviderStatus:       run.ProviderStatus,
		ProviderFinishReason: run.ProviderFinishReason,
		Input:                run.Input,
		Output:               run.Output,
		Attempt:              run.Attempt,
		MaxAttempts:          run.MaxAttempts,
		QueuedAt:             iso(run.QueuedAt),
		StartedAt:            isoPtr(run.StartedAt),
		FinishedAt:           isoPtr(run.FinishedAt),
		ErrorCode:            run.ErrorCode,
		ErrorMessage:         run.ErrorMessage,
		CreatedAt:            iso(run.CreatedAt),
		UpdatedAt:            iso(run.UpdatedAt),
	}
}

// CreateRun implements POST /api/v2/runs: validation + lazy conversation +
// atomic (message, run, outbox).
func (s *Server) CreateRun(w http.ResponseWriter, r *http.Request) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	var body struct {
		ApplicationID  *int64   `json:"application_id"`
		Content        string   `json:"content"`
		ConversationID *int64   `json:"conversation_id"`
		AttachmentIDs  []string `json:"attachment_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeBare(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.ApplicationID == nil || *body.ApplicationID == 0 {
		writeBare(w, http.StatusBadRequest, "application_id is required")
		return
	}
	if body.Content == "" {
		writeBare(w, http.StatusBadRequest, "content is required")
		return
	}
	ctx := r.Context()
	appID := *body.ApplicationID

	// Execution authorization (评测 P0-1): the ONE gate shared with the
	// scheduler. Visibility is not authorization — a regular caller must
	// not be able to execute a private or disabled application by guessing
	// its id. Staff may execute private apps; nobody may execute a
	// disabled one.
	exe, err := s.Catalog.AuthorizeExecution(ctx, appID, caller.ID, caller.IsStaff)
	if err != nil {
		writeExecutionDenied(w, err)
		return
	}
	binding := exe.Binding

	// User admission (评测 P1-7): per-user QPS (long-lived GCRA limiter,
	// so the Redis-outage fallback actually keeps state) plus an
	// outstanding-run cap enforced atomically with the insert.
	if err := s.admitUserRun(ctx, caller.ID); err != nil {
		writeDetail(w, http.StatusTooManyRequests, err.Error())
		return
	}

	// Conversation target: reuse the caller's own conversation, or create
	// one inside the run transaction (lazy, atomic — no orphan rows).
	convID := int64(0)
	if body.ConversationID != nil && *body.ConversationID > 0 {
		var owner, appCol int64
		err := s.DB.QueryRowContext(ctx,
			`SELECT user_id, application_id FROM conversations WHERE id = ?`,
			*body.ConversationID).Scan(&owner, &appCol)
		if err != nil || owner != caller.ID || appCol != appID {
			writeBare(w, http.StatusBadRequest, "conversation not found")
			return
		}
		convID = *body.ConversationID
	}
	title := body.Content
	if len([]rune(title)) > 80 {
		title = string([]rune(title)[:80])
	}

	// Attachments: only the caller's own unbound pending attachments.
	attachmentIDs := dedupe(body.AttachmentIDs)
	if len(attachmentIDs) > 8 {
		writeBare(w, http.StatusBadRequest, "one or more attachments are invalid")
		return
	}
	if err := s.validateAttachments(ctx, attachmentIDs, caller.ID, binding.ProviderKey); err != nil {
		writeBare(w, http.StatusBadRequest, "one or more attachments are invalid")
		return
	}

	run, err := s.Runs.CreateRunAdmitted(ctx, &execution.CreateRunInput{
		UserID:             caller.ID,
		ApplicationID:      appID,
		ConversationID:     convID,
		RuntimeBindingID:   binding.ID,
		Provider:           binding.ProviderKey,
		RuntimeType:        binding.RuntimeType,
		ExecutionMode:      binding.ExecutionMode,
		Content:            body.Content,
		AttachmentIDs:      attachmentIDs,
		ConversationTitle:  title,
		CreateConversation: convID == 0,
		RuntimeSnapshot:    binding.Snapshot(),
	}, s.Config.Runner.UserMaxOutstanding)
	if err != nil {
		switch {
		case errors.Is(err, execution.ErrConversationBusy):
			// 评测 P0-2: one conversation executes one turn at a time.
			writeDetail(w, http.StatusConflict, "previous turn is still running")
		case errors.Is(err, execution.ErrAttachmentClaimed):
			writeDetail(w, http.StatusConflict, "one or more attachments were already used")
		case errors.Is(err, execution.ErrConversationNotFound):
			writeBare(w, http.StatusBadRequest, "conversation not found")
		case errors.Is(err, execution.ErrUserOutstandingExceeded):
			writeDetail(w, http.StatusTooManyRequests,
				fmt.Sprintf("too many outstanding runs (limit %d)", s.Config.Runner.UserMaxOutstanding))
		default:
			writeSimpleError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	writeJSON(w, http.StatusCreated, toRunRecord(run))
}

// writeExecutionDenied maps the unified execution gate's errors to the
// wire envelope. Regular callers always see 404 (no existence leak);
// staff get a precise conflict for the states they can see.
func writeExecutionDenied(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, catalog.ErrExecutionDisabled):
		writeDetail(w, http.StatusConflict, "application is disabled")
	case errors.Is(err, catalog.ErrExecutionNotChat):
		writeBare(w, http.StatusBadRequest, "application does not support chat runs")
	case errors.Is(err, catalog.ErrExecutionProviderInactive):
		writeDetail(w, http.StatusConflict, "provider is not active")
	case errors.Is(err, catalog.ErrNoBinding):
		writeDetail(w, http.StatusConflict, "application has no enabled runtime binding")
	default:
		writeDetail(w, http.StatusNotFound, "application not found")
	}
}

// admitUserRun applies the per-user QPS policy (评测 P1-7).
//
// The limiter is a LONG-LIVED object supplied by the server: it keeps the
// in-process fallback state per key, so a Redis outage still bounds the
// rate. Building a limiter per request (as an earlier revision did) reset
// that state on every call and turned the outage fallback into fail-open.
// The outstanding-run cap is enforced inside CreateRunAdmitted, atomically
// with the insert.
func (s *Server) admitUserRun(ctx context.Context, userID int64) error {
	limiter := s.RunAdmission
	if limiter == nil || s.Redis == nil || s.Config == nil {
		return nil
	}
	key := s.Redis.Key("rate", "runs", "user", strconv.FormatInt(userID, 10))
	ok, wait, err := limiter.AllowKey(ctx, key)
	if err != nil || ok {
		// AllowKey degrades to the in-process limiter on Redis errors and
		// returns no error in that case; only ctx cancellation surfaces.
		return nil
	}
	secs := int(wait.Seconds())
	if secs < 1 {
		secs = 1
	}
	return fmt.Errorf("too many requests, retry in %ds", secs)
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// validateAttachments enforces the ownership + provider + state rules:
// each id must be the caller's own pending, unbound attachment for the
// same provider (prevents replaying another user's provider attachment).
func (s *Server) validateAttachments(ctx context.Context, attachmentIDs []string, userID int64, providerKey string) error {
	for _, raw := range attachmentIDs {
		attID, err := ids.Parse(raw)
		if err != nil {
			return err
		}
		row, err := s.Runs.Querier().GetAttachmentByID(ctx, attID.Bytes())
		if err != nil {
			return err
		}
		if !row.CreatedBy.Valid || int64(row.CreatedBy.Int64) != userID ||
			row.Provider != providerKey || row.Status != "pending" || row.RunID.Valid {
			return errors.New("invalid attachment")
		}
	}
	return nil
}

func (s *Server) GetRun(w http.ResponseWriter, r *http.Request, runID genapi.RunId) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	id, err := ids.Parse(runID.String())
	if err != nil {
		writeDetail(w, http.StatusNotFound, "run not found")
		return
	}
	run, err := s.Runs.GetRunForUser(r.Context(), id, caller.ID, caller.IsStaff)
	if err != nil {
		writeDetail(w, http.StatusNotFound, "run not found")
		return
	}
	writeJSON(w, http.StatusOK, toRunRecord(run))
}

func (s *Server) ListRunEvents(w http.ResponseWriter, r *http.Request, runID genapi.RunId, params genapi.ListRunEventsParams) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	id, err := ids.Parse(runID.String())
	if err != nil {
		writeDetail(w, http.StatusNotFound, "run not found")
		return
	}
	if _, err := s.Runs.GetRunForUser(r.Context(), id, caller.ID, caller.IsStaff); err != nil {
		writeDetail(w, http.StatusNotFound, "run not found")
		return
	}
	after := uint64(0)
	if params.After != nil {
		after = uint64(*params.After)
	}
	events, err := s.Runs.ListEventsAfter(r.Context(), id, after)
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, events)
}

// StreamRun delegates to the SSE gateway.
func (s *Server) StreamRun(w http.ResponseWriter, r *http.Request, runID genapi.RunId) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	id, err := ids.Parse(runID.String())
	if err != nil {
		writeDetail(w, http.StatusNotFound, "run not found")
		return
	}
	run, err := s.Runs.GetRunForUser(r.Context(), id, caller.ID, caller.IsStaff)
	if err != nil {
		writeDetail(w, http.StatusNotFound, "run not found")
		return
	}
	s.SSE.Stream(w, r, run)
}

// CreateRunCommand records a run command; cancel is validated against the
// adapter capability (Aily has none → 409, matching the reference).
func (s *Server) CreateRunCommand(w http.ResponseWriter, r *http.Request, runID genapi.RunId) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	id, err := ids.Parse(runID.String())
	if err != nil {
		writeDetail(w, http.StatusNotFound, "run not found")
		return
	}
	run, err := s.Runs.GetRunForUser(r.Context(), id, caller.ID, caller.IsStaff)
	if err != nil {
		writeDetail(w, http.StatusNotFound, "run not found")
		return
	}
	var body struct {
		CommandType string         `json:"command_type"`
		Payload     map[string]any `json:"payload"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeFieldErrors(w, map[string][]string{"command_type": {"invalid json"}})
		return
	}
	switch body.CommandType {
	case "cancel", "respond", "resume":
	default:
		writeFieldErrors(w, map[string][]string{"command_type": {`"command_type" must be one of cancel, respond, resume`}})
		return
	}
	if body.CommandType == "cancel" {
		adapter, err := s.Registry.Resolve(run.Provider, run.RuntimeType)
		if err != nil || !adapter.Capabilities()["cancel"] {
			writeSimpleError(w, http.StatusConflict, "provider does not support cancel")
			return
		}
	}
	cmdID := ids.New()
	payloadJSON, _ := json.Marshal(body.Payload)
	if payloadJSON == nil {
		payloadJSON = []byte("{}")
	}
	_, err = s.Runs.Querier().CreateRunCommand(r.Context(), genCreateRunCommandParams(cmdID.Bytes(), run.ID.Bytes(), body.CommandType, payloadJSON, caller.ID))
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	now := time.Now().UTC()
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":           cmdID.String(),
		"run":          run.ID.String(),
		"command_type": body.CommandType,
		"payload":      orEmptyMap(body.Payload),
		"status":       "pending",
		"created_by":   caller.ID,
		"created_at":   iso(now),
		"resolved_at":  nil,
	})
}

func orEmptyMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func (s *Server) ListRunArtifacts(w http.ResponseWriter, r *http.Request, runID genapi.RunId) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	id, err := ids.Parse(runID.String())
	if err != nil {
		writeDetail(w, http.StatusNotFound, "run not found")
		return
	}
	if _, err := s.Runs.GetRunForUser(r.Context(), id, caller.ID, caller.IsStaff); err != nil {
		writeDetail(w, http.StatusNotFound, "run not found")
		return
	}
	rows, err := s.Runs.Querier().ListRunArtifacts(r.Context(), id.Bytes())
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, a := range rows {
		out = append(out, artifactJSON(a))
	}
	writeJSON(w, http.StatusOK, out)
}

func artifactJSON(a db.RunArtifact) map[string]any {
	out := map[string]any{
		"id":                     idsMustString(a.ID),
		"run":                    idsMustString(a.RunID),
		"provider":               a.Provider,
		"external_artifact_id":   a.ExternalArtifactID,
		"provider_artifact_type": a.ProviderArtifactType,
		"name":                   a.Name,
		"normalized_type":        a.NormalizedType,
		"storage_type":           a.StorageType,
		"cached_url_expires_at":  nil,
		"resolution_status":      a.ResolutionStatus,
		"created_at":             iso(a.CreatedAt),
		"updated_at":             iso(a.UpdatedAt),
	}
	if a.CachedUrlExpiresAt.Valid {
		t := iso(a.CachedUrlExpiresAt.Time)
		out["cached_url_expires_at"] = t
	}
	return out
}

// OpenArtifact resolves the 24h URL (cached 23h) and 302s to it.
//
// The redirect is what inline chat <img> elements hit repeatedly; without a
// Cache-Control header every remount re-requests it. Browsers do not cache
// 302 by default, so allow a short private cache: after it expires the
// /open call still hits the 23h DB cache (no provider round-trip), and the
// signed target URL itself is valid for 24h.
func (s *Server) OpenArtifact(w http.ResponseWriter, r *http.Request, artifactId openapi_types.UUID) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	artID, err := ids.Parse(artifactId.String())
	if err != nil {
		writeDetail(w, http.StatusNotFound, "artifact not found")
		return
	}
	row, err := s.Runs.Querier().GetRunArtifactByID(r.Context(), artID.Bytes())
	if err != nil {
		writeDetail(w, http.StatusNotFound, "artifact not found")
		return
	}
	run, err := s.Runs.GetRun(r.Context(), mustIDFromBytes(row.RunID))
	if err != nil || (run.UserID != nil && *run.UserID != caller.ID && !caller.IsStaff) {
		writeDetail(w, http.StatusNotFound, "artifact not found")
		return
	}
	s.serveArtifactRedirect(w, r, row, run)
}

// serveArtifactRedirect is the shared tail of every artifact open flow:
// fresh cached URL → provider resolve → 302 to the signed target URL.
// Authorization happens in the callers; the bytes never touch this service.
//
// The redirect is what inline chat <img> elements hit repeatedly; without a
// Cache-Control header every remount re-requests it. Browsers do not cache
// 302 by default, so allow a short private cache: after it expires the
// /open call still hits the 23h DB cache (no provider round-trip), and the
// signed target URL itself is valid for 24h.
func (s *Server) serveArtifactRedirect(w http.ResponseWriter, r *http.Request, row db.RunArtifact, run *execution.Run) {
	// Cached URL still fresh? (refresh when <25min of validity remains)
	now := time.Now().UTC()
	if row.CachedExternalUrl.String != "" && row.CachedUrlExpiresAt.Valid &&
		row.CachedUrlExpiresAt.Time.After(now.Add(25*time.Minute)) {
		w.Header().Set("Cache-Control", "private, max-age=1800")
		http.Redirect(w, r, row.CachedExternalUrl.String, http.StatusFound)
		return
	}

	// Resolve through the provider adapter.
	adapter, err := s.Registry.Resolve(row.Provider, run.RuntimeType)
	if err != nil {
		writeSimpleError(w, http.StatusBadGateway, fmt.Sprintf("no resolver for provider %s", row.Provider))
		return
	}
	agentID := run.SnapshotString("external_resource_id")
	if agentID == "" {
		writeSimpleError(w, http.StatusBadGateway, "missing agent_id for artifact owner run")
		return
	}
	identityMode := run.SnapshotString("identity_mode")
	if identityMode == "" {
		identityMode = "user"
	}
	if run.UserID == nil {
		writeSimpleError(w, http.StatusBadGateway, "artifact run has no owner")
		return
	}
	authCtx, err := adapter.BuildAuth(r.Context(), *run.UserID, identityMode)
	if err != nil {
		writeSimpleError(w, http.StatusBadGateway, "无法获取访问凭据："+err.Error())
		return
	}
	if s.RateLimitArtifacts != nil {
		_ = s.RateLimitArtifacts.Acquire(r.Context())
	}
	ref, err := adapter.ResolveArtifact(r.Context(), authCtx, agentID, row.ExternalArtifactID)
	if err != nil || ref.URL == "" {
		writeSimpleError(w, http.StatusBadGateway, "artifact resolution failed")
		return
	}
	expires := now.Add(23 * time.Hour)
	name := ref.Name
	if name == "" {
		name = row.Name
	}
	if err := s.Runs.Querier().CacheArtifactURL(r.Context(), genCacheArtifactParams(ref.URL, expires, name, name, row.ID)); err != nil {
		s.Log.Warn("cache artifact url failed", "err", err)
	}
	w.Header().Set("Cache-Control", "private, max-age=1800")
	http.Redirect(w, r, ref.URL, http.StatusFound)
}

// ListConversationRuns lists a conversation's runs (newest first).
func (s *Server) ListConversationRuns(w http.ResponseWriter, r *http.Request, conversationId int) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	var owner int64
	err := s.DB.QueryRowContext(r.Context(),
		`SELECT user_id FROM conversations WHERE id = ?`, int64(conversationId)).Scan(&owner)
	if err != nil || owner != caller.ID {
		writeDetail(w, http.StatusNotFound, "conversation not found")
		return
	}
	rows, err := s.Runs.Querier().ListRunsByConversation(r.Context(), sql.NullInt64{Int64: int64(conversationId), Valid: true})
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]runRecord, 0, len(rows))
	for i := range rows {
		out = append(out, toRunRecord(rowToRun(rows[i])))
	}
	writeJSON(w, http.StatusOK, out)
}

// UploadApplicationAttachment stores the file in studio storage and creates
// the pending RuntimeAttachment (the worker uploads to Aily with UAT).
func (s *Server) UploadApplicationAttachment(w http.ResponseWriter, r *http.Request, id genapi.ApplicationId) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	// Same execution gate as CreateRun (评测 P0-1): uploading an attachment
	// for an application you may not run is itself a probe vector.
	exe, err := s.Catalog.AuthorizeExecution(r.Context(), int64(id), caller.ID, caller.IsStaff)
	if err != nil {
		writeExecutionDenied(w, err)
		return
	}
	binding := exe.Binding
	if err := r.ParseMultipartForm(50 << 20); err != nil {
		writeBare(w, http.StatusBadRequest, "file or doc_url is required")
		return
	}
	attachmentType := r.FormValue("type")
	if attachmentType == "" {
		attachmentType = "file"
	}
	docURL := r.FormValue("doc_url")
	var data []byte
	var filename string
	if file, header, ferr := r.FormFile("file"); ferr == nil {
		defer file.Close()
		filename = header.Filename
		data, err = io.ReadAll(io.LimitReader(file, 41<<20))
		if err != nil {
			writeSimpleError(w, http.StatusBadGateway, "attachment read failed")
			return
		}
	} else if docURL == "" {
		writeBare(w, http.StatusBadRequest, "file or doc_url is required")
		return
	}

	// Store in studio storage first (Browser → Studio Storage → Run → Worker → Aily).
	storageKey := ""
	if len(data) > 0 {
		storageKey = fmt.Sprintf("attachments/%d/%d_%s", caller.ID, time.Now().UnixNano(), sanitizeName(filename))
		if _, err := s.Storage.Put(r.Context(), storageKey, bytes.NewReader(data), contentTypeFor(filename)); err != nil {
			writeSimpleError(w, http.StatusBadGateway, "attachment storage failed")
			return
		}
	}

	attID := ids.New()
	identityMode := binding.IdentityMode
	if identityMode == "" {
		identityMode = "user"
	}
	_, err = s.Runs.Querier().CreateAttachment(r.Context(), genCreateAttachmentParams(
		attID.Bytes(), binding.ProviderKey, attachmentType, filename, docURL, storageKey,
		contentTypeFor(filename), int64(len(data)), identityMode, fmt.Sprintf("%d", caller.ID), caller.ID))
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":                     attID.String(),
		"run":                    nil,
		"conversation":           nil,
		"provider":               binding.ProviderKey,
		"external_attachment_id": "",
		"attachment_type":        attachmentType,
		"name":                   filename,
		"source_type":            map[bool]string{true: "doc_url", false: "upload"}[docURL != ""],
		"status":                 "pending",
		"created_at":             iso(time.Now().UTC()),
	})
}

// UploadRunAttachment binds a file onto a queued run.
func (s *Server) UploadRunAttachment(w http.ResponseWriter, r *http.Request, runID genapi.RunId) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	id, err := ids.Parse(runID.String())
	if err != nil {
		writeDetail(w, http.StatusNotFound, "run not found")
		return
	}
	run, err := s.Runs.GetRunForUser(r.Context(), id, caller.ID, caller.IsStaff)
	if err != nil {
		writeDetail(w, http.StatusNotFound, "run not found")
		return
	}
	if err := r.ParseMultipartForm(50 << 20); err != nil {
		writeBare(w, http.StatusBadRequest, "file or doc_url is required")
		return
	}
	attachmentType := r.FormValue("type")
	if attachmentType == "" {
		attachmentType = "file"
	}
	docURL := r.FormValue("doc_url")
	var data []byte
	var filename string
	if file, header, ferr := r.FormFile("file"); ferr == nil {
		defer file.Close()
		filename = header.Filename
		data, err = io.ReadAll(io.LimitReader(file, 41<<20))
		if err != nil {
			writeSimpleError(w, http.StatusBadGateway, "attachment read failed")
			return
		}
	} else if docURL == "" {
		writeBare(w, http.StatusBadRequest, "file or doc_url is required")
		return
	}

	agentID := run.SnapshotString("external_resource_id")
	if agentID == "" {
		writeBare(w, http.StatusBadRequest, "run has no external_resource_id")
		return
	}

	// Upload to the provider under the run owner's UAT (queued-run path).
	adapter, err := s.Registry.Resolve(run.Provider, run.RuntimeType)
	if err != nil {
		writeSimpleError(w, http.StatusBadGateway, "attachment upload failed")
		return
	}
	if run.UserID == nil {
		writeDetail(w, http.StatusNotFound, "run not found")
		return
	}
	authCtx, err := adapter.BuildAuth(r.Context(), *run.UserID, modeOr(run, "user"))
	if err != nil {
		writeSimpleError(w, http.StatusBadGateway, "attachment upload failed: "+err.Error())
		return
	}
	externalID, err := adapter.UploadAttachment(r.Context(), authCtx, agentID, &catalog.AttachmentInput{
		Data:           data,
		Filename:       filename,
		ContentType:    contentTypeFor(filename),
		AttachmentType: attachmentType,
		DocURL:         docURL,
	})
	if err != nil {
		var apiErr *aily.APIError
		if errors.As(err, &apiErr) {
			writeSimpleError(w, http.StatusBadGateway, "attachment upload failed: "+apiErr.Msg)
		} else {
			writeSimpleError(w, http.StatusBadGateway, "attachment upload failed: "+err.Error())
		}
		return
	}

	attID := ids.New()
	storageKey := ""
	if len(data) > 0 {
		storageKey = fmt.Sprintf("attachments/%d/%d_%s", caller.ID, time.Now().UnixNano(), sanitizeName(filename))
		_, _ = s.Storage.Put(r.Context(), storageKey, bytes.NewReader(data), contentTypeFor(filename))
	}
	if _, err := s.Runs.Querier().CreateAttachment(r.Context(), genCreateAttachmentParamsExt(
		attID.Bytes(), run.ID.Bytes(), run.Provider, attachmentType, filename, docURL, storageKey,
		contentTypeFor(filename), int64(len(data)), modeOr(run, "user"),
		fmt.Sprintf("%d", *run.UserID), caller.ID, externalID)); err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if conv := run.ConversationID; conv != nil {
		if err := s.Runs.Querier().BindAttachmentToRun(r.Context(), genBindAttachmentParams(attID.Bytes(), run.ID.Bytes(), uint64(*conv))); err != nil {
			s.Log.Warn("bind attachment failed", "err", err)
		}
	} else {
		if err := s.Runs.Querier().BindAttachmentToRun(r.Context(), genBindAttachmentParams(attID.Bytes(), run.ID.Bytes(), 0)); err != nil {
			s.Log.Warn("bind attachment failed", "err", err)
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":                     attID.String(),
		"run":                    run.ID.String(),
		"conversation":           run.ConversationID,
		"provider":               run.Provider,
		"external_attachment_id": externalID,
		"attachment_type":        attachmentType,
		"name":                   filename,
		"source_type":            map[bool]string{true: "doc_url", false: "upload"}[docURL != ""],
		"status":                 "uploaded",
		"created_at":             iso(time.Now().UTC()),
	})
}

func modeOr(run *execution.Run, def string) string {
	m := run.SnapshotString("identity_mode")
	if m == "" {
		return def
	}
	return m
}

func sanitizeName(name string) string {
	out := make([]rune, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "file"
	}
	return string(out)
}

func contentTypeFor(name string) string {
	switch {
	case hasSuffixFold(name, ".png"):
		return "image/png"
	case hasSuffixFold(name, ".jpg"), hasSuffixFold(name, ".jpeg"):
		return "image/jpeg"
	case hasSuffixFold(name, ".pdf"):
		return "application/pdf"
	case hasSuffixFold(name, ".gif"):
		return "image/gif"
	case hasSuffixFold(name, ".webp"):
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}

func hasSuffixFold(s, suffix string) bool {
	if len(s) < len(suffix) {
		return false
	}
	tail := s[len(s)-len(suffix):]
	for i := 0; i < len(suffix); i++ {
		a, b := tail[i], suffix[i]
		if a >= 'A' && a <= 'Z' {
			a += 'a' - 'A'
		}
		if a != b {
			return false
		}
	}
	return true
}

func idsMustString(b []byte) string {
	id := ids.ID{}
	_ = id.Scan(b)
	return id.String()
}

func rowToRun(row db.Run) *execution.Run {
	return execution.RunFromDBRow(row)
}

func mustIDFromBytes(b []byte) ids.ID {
	id := ids.ID{}
	_ = id.Scan(b)
	return id
}
