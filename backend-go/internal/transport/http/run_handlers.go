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
	"strings"
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

	// Idempotency envelope (第九轮 P0-1). Both fields are omitted for the
	// ordinary (non-idempotent) create so the existing 201 body is unchanged
	// for clients that do not send a client_request_id.
	ClientRequestID     string `json:"client_request_id,omitempty"`
	IdempotencyReplayed bool   `json:"idempotency_replayed,omitempty"`
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
//
// Idempotency (第九轮 P0-1): when the caller supplies a client_request_id the
// whole submit becomes replayable. The ORDER below is part of the contract,
// not an implementation detail:
//
//  1. authentication
//  2. JSON / basic validation
//  3. client_request_id + request_hash
//  4. resolve the reservation  ← BEFORE authorization and admission
//  5. application execution authorization
//  6. QPS admission
//  7. conversation + attachment validation
//  8. create (message, run, attachment claim, outbox) + reserve, one tx
//
// Step 4 must come before steps 5-7. The first attempt of a request ALREADY
// consumed those: its attachments were claimed by its run, and its run counts
// against the per-user outstanding cap. A replay that ran them first would be
// told "attachment already used" (400/409) or "too many outstanding runs"
// (429) for a request that had actually SUCCEEDED.
func (s *Server) CreateRun(w http.ResponseWriter, r *http.Request) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	var body struct {
		ApplicationID   *int64   `json:"application_id"`
		Content         string   `json:"content"`
		ConversationID  *int64   `json:"conversation_id"`
		AttachmentIDs   []string `json:"attachment_ids"`
		ClientRequestID string   `json:"client_request_id"`
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
	// The id is an opaque client token; it is rejected rather than truncated
	// when too long, because truncation would fuse two distinct request
	// identities into one (see execution.MaxClientRequestIDLen).
	clientRequestID := strings.TrimSpace(body.ClientRequestID)
	if len(clientRequestID) > execution.MaxClientRequestIDLen {
		writeBare(w, http.StatusBadRequest, "client_request_id exceeds 64 characters")
		return
	}
	ctx := r.Context()
	appID := *body.ApplicationID

	// Pure input normalization, hoisted above the admission steps because the
	// request hash is computed from it.
	attachmentIDs := dedupe(body.AttachmentIDs)
	if len(attachmentIDs) > 8 {
		writeBare(w, http.StatusBadRequest, "one or more attachments are invalid")
		return
	}
	targetConvID := int64(0)
	if body.ConversationID != nil && *body.ConversationID > 0 {
		targetConvID = *body.ConversationID
	}
	// conversation_id is hashed as REQUESTED (0 = lazy), not as resolved:
	// the first attempt does not know the conversation it is about to create,
	// so a retry of that same request hashes 0 as well and replays.
	requestHash := execution.RunRequestHash(appID, targetConvID, body.Content, attachmentIDs)

	if clientRequestID != "" {
		run, found, err := s.Runs.ResolveRunRequest(ctx, caller.ID, clientRequestID, requestHash)
		switch {
		case errors.Is(err, execution.ErrIdempotencyKeyReused):
			if s.Metric != nil {
				s.Metric.RunIdempotencyConflictTotal.Inc()
			}
			writeDetail(w, http.StatusConflict, "idempotency_key_reused")
			return
		case err != nil:
			writeSimpleError(w, http.StatusInternalServerError, err.Error())
			return
		case found:
			if s.Metric != nil {
				s.Metric.RunIdempotencyReplayTotal.Inc()
			}
			rec := toRunRecord(run)
			rec.ClientRequestID = clientRequestID
			rec.IdempotencyReplayed = true
			// 200, not 201: nothing was created by THIS request.
			writeJSON(w, http.StatusOK, rec)
			return
		}
	}

	// Execution authorization (评测 P0-1): the ONE gate shared with the
	// scheduler. Visibility is not authorization — a regular caller must
	// not be able to execute a private or disabled application by guessing
	// its id. Staff may execute private apps; nobody may execute a
	// disabled one.
	exe, err := s.Catalog.AuthorizeExecution(ctx, appID, caller.ID, caller.IsStaff)
	if err != nil {
		// Mutable-state race (第九轮补丁 3.2-C): the app could be disabled
		// BETWEEN our initial resolve and this check by the very duplicate
		// that is committing right now. A committed reservation outranks a
		// refusal caused by state the original request did not see either.
		if s.tryServeIdempotentReplay(ctx, w, clientRequestID,
			s.replayResolver(caller.ID, clientRequestID, requestHash)) {
			return
		}
		writeExecutionDenied(w, err)
		return
	}
	binding := exe.Binding

	// User admission (评测 P1-7): per-user QPS (long-lived GCRA limiter,
	// so the Redis-outage fallback actually keeps state) plus an
	// outstanding-run cap enforced atomically with the insert.
	if err := s.admitUserRun(ctx, caller.ID); err != nil {
		// A cancelled client context is not a rate-limit rejection: the
		// request is over, so abort without writing a body (第五轮 P2-3).
		// Writing 429 here would both mislabel the outcome and hide the
		// cancellation from the client's own logs.
		if ctx.Err() != nil {
			return
		}
		// A CONCURRENT RETRY of an already accepted request can land here
		// (第九轮 P1). The original may be inside its creation transaction,
		// so its reservation is invisible to this request's earlier resolve
		// while its own run already occupies the outstanding cap. Give the
		// identity a bounded moment to appear before declaring 429 for a
		// request that actually succeeded — otherwise the client's retry of
		// a lost response creates a second turn.
		if s.tryServeIdempotentReplay(ctx, w, clientRequestID,
			s.replayResolver(caller.ID, clientRequestID, requestHash)) {
			return
		}
		writeDetail(w, http.StatusTooManyRequests, err.Error())
		return
	}

	// Conversation target: reuse the caller's own conversation, or create
	// one inside the run transaction (lazy, atomic — no orphan rows).
	convID := int64(0)
	if targetConvID > 0 {
		var owner, appCol int64
		err := s.DB.QueryRowContext(ctx,
			`SELECT user_id, application_id FROM conversations WHERE id = ?`,
			targetConvID).Scan(&owner, &appCol)
		if err != nil || owner != caller.ID || appCol != appID {
			// Conversation validation race (第九轮补丁 3.2-C): the winner's
			// transaction may have changed what this check sees; a committed
			// reservation outranks the refusal.
			if s.tryServeIdempotentReplay(ctx, w, clientRequestID,
				s.replayResolver(caller.ID, clientRequestID, requestHash)) {
				return
			}
			writeBare(w, http.StatusBadRequest, "conversation not found")
			return
		}
		convID = targetConvID
	}
	title := body.Content
	if len([]rune(title)) > 80 {
		title = string([]rune(title)[:80])
	}

	// Attachments: only the caller's own unbound pending attachments.
	if err := s.validateAttachments(ctx, attachmentIDs, caller.ID, binding.ProviderKey); err != nil {
		// Attachment race (第九轮补丁 3.2-C): the most reproducible one. The
		// original request claimed these attachments inside its creation
		// transaction; the retry's initial resolve ran BEFORE that commit.
		// If the reservation appears now, this request IS that attempt.
		if s.tryServeIdempotentReplay(ctx, w, clientRequestID,
			s.replayResolver(caller.ID, clientRequestID, requestHash)) {
			return
		}
		writeBare(w, http.StatusBadRequest, "one or more attachments are invalid")
		return
	}

	run, replayed, err := s.Runs.CreateRunIdempotent(ctx, &execution.CreateRunInput{
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
		ClientRequestID:    clientRequestID,
		RequestHash:        requestHash,
	}, s.Config.Runner.UserMaxOutstanding)
	if err != nil {
		switch {
		case errors.Is(err, execution.ErrIdempotencyKeyReused):
			if s.Metric != nil {
				s.Metric.RunIdempotencyConflictTotal.Inc()
			}
			writeDetail(w, http.StatusConflict, "idempotency_key_reused")
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

	rec := toRunRecord(run)
	if clientRequestID != "" {
		rec.ClientRequestID = clientRequestID
	}
	if replayed {
		// A concurrent duplicate: the winner committed between our resolve
		// and our insert, and this request is that same request.
		if s.Metric != nil {
			s.Metric.RunIdempotencyReplayTotal.Inc()
		}
		rec.IdempotencyReplayed = true
		writeJSON(w, http.StatusOK, rec)
		return
	}
	writeJSON(w, http.StatusCreated, rec)
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
	// 第四轮 P2: without this case the sentinel fell through to the
	// default 404 "application not found", hiding a registration problem
	// from the only caller allowed to see it (staff).
	case errors.Is(err, catalog.ErrExecutionProviderMissing):
		writeDetail(w, http.StatusConflict, "provider is not registered")
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
	if limiter == nil || s.Config == nil {
		return nil
	}
	// A nil Redis client must NOT bypass admission (第四轮 P2): AllowKey
	// already degrades to the in-process GCRA when no client is
	// configured, which still bounds the rate on this node. Only the key
	// needs a fallback namespace.
	userKey := strconv.FormatInt(userID, 10)
	key := s.Config.Redis.Key("rate", "runs", "user", userKey)
	if s.Redis != nil {
		key = s.Redis.Key("rate", "runs", "user", userKey)
	}
	ok, wait, err := limiter.AllowKey(ctx, key)
	if err != nil {
		// 第五轮 P2-3: propagate instead of treating it as admission
		// success. AllowKey only returns an error for a CANCELLED CONTEXT
		// (Redis errors degrade to the in-process GCRA and return no
		// error) — swallowing it let a client that had already hung up
		// "pass" admission and run on until some later DB call reported
		// the cancellation anyway. The caller aborts the request.
		return err
	}
	if ok {
		return nil
	}
	secs := int(wait.Seconds())
	if secs < 1 {
		secs = 1
	}
	return fmt.Errorf("too many requests, retry in %ds", secs)
}

// ReplayResolveWait is how long a refused request waits for its own earlier
// attempt's reservation to commit (第九轮 P1 + 补丁 3.2-C). See
// tryServeIdempotentReplay for why that window exists at all.
const ReplayResolveWait = execution.DefaultResolveRunRequestWait

// tryServeIdempotentReplay is the ONE helper every post-miss rejection goes
// through (第九轮补丁 3.2-C). Once a request carries a client_request_id, any
// validation/admission failure between the initial resolve and the creation
// transaction can be a race the request's own earlier attempt just won —
// its reservation was invisible a moment ago and commits now. The refusal
// must therefore be interrupted by one bounded replay resolution FIRST:
//
//	original request committed   → 200 with the ORIGINAL run (replayed=true)
//	same id, DIFFERENT payload   → 409 idempotency_key_reused (a conflict was
//	                               never a 429; swallowing it mislabeled the
//	                               error AND hid the reuse)
//	resolver infrastructure err  → 500 (a DB outage must not masquerade as
//	                               client-side rate limiting)
//	still genuinely new          → false; the caller writes its own error
//
// `resolve` is injected so the semantics can be pinned without a database
// (see run_handlers_test.go): the bounded waiting itself lives in
// execution.ResolveRunRequestWithWait and is tested against a real
// uncommitted transaction there.
func (s *Server) tryServeIdempotentReplay(
	ctx context.Context,
	w http.ResponseWriter,
	clientRequestID string,
	resolve func(context.Context) (*execution.Run, bool, error),
) (handled bool) {
	if clientRequestID == "" || resolve == nil {
		return false
	}
	run, found, err := resolve(ctx)
	switch {
	case errors.Is(err, execution.ErrIdempotencyKeyReused):
		if s.Metric != nil {
			s.Metric.RunIdempotencyConflictTotal.Inc()
		}
		writeDetail(w, http.StatusConflict, "idempotency_key_reused")
		return true
	case err != nil:
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return true
	case found && run != nil:
		if s.Metric != nil {
			s.Metric.RunIdempotencyReplayTotal.Inc()
		}
		rec := toRunRecord(run)
		rec.ClientRequestID = clientRequestID
		rec.IdempotencyReplayed = true
		// 200, not 201: nothing was created by THIS request.
		writeJSON(w, http.StatusOK, rec)
		return true
	}
	return false
}

// replayResolver binds this request's identity to the execution-layer wait.
func (s *Server) replayResolver(userID int64, clientRequestID string, requestHash []byte) func(context.Context) (*execution.Run, bool, error) {
	return func(ctx context.Context) (*execution.Run, bool, error) {
		return s.Runs.ResolveRunRequestWithWait(ctx, userID, clientRequestID, requestHash, ReplayResolveWait)
	}
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

// ListRunEvents implements GET /api/v2/runs/{id}/events — one KEYSET page
// (第九轮 P1-3).
//
// Query: after (exclusive lower bound, default 0), limit (default 200, max
// 1000). The response is an envelope rather than a bare array so a client can
// walk a long history without guessing where it stopped:
//
//	{"items":[...],"next_after":1400,"has_more":true}
//
// The request is answered in bounded time and memory for a run with 100k
// events, which an unbounded read could not be.
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
	limit := 0
	if params.Limit != nil {
		limit = int(*params.Limit)
	}
	page, err := s.Runs.ListEventPage(r.Context(), id, after, limit)
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := page.Items
	if items == nil {
		items = []execution.EventRecord{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":      items,
		"next_after": page.NextAfter,
		"has_more":   page.HasMore,
	})
}

// StreamRun delegates to the SSE gateway.
//
// The resume cursor is resolved by the gateway (sse.ResumeCursor) rather than
// from params here, because it must combine TWO sources with a defined
// precedence: the `after` query parameter and the `Last-Event-ID` header that
// EventSource replays automatically. The generated params type only models
// the query half, so passing it through would split one decision across two
// places.
func (s *Server) StreamRun(w http.ResponseWriter, r *http.Request, runID genapi.RunId, _ genapi.StreamRunParams) {
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
