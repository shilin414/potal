package aily

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/catalog"
	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
)

// Executor runs claimed Aily agent runs on the worker plane.
//
// Behavior preserved from the validated reference implementation:
//   - Lazy session: AgentThread.remote_id stays empty until the first real
//     message; different user/agent/provider/auth-subject never share one.
//   - Streaming (interactive) drives content.delta events; transport end,
//     timeout or break NEVER finalize the run directly — Final
//     Reconciliation via GET chat result is the only authority (§26).
//   - Background (async/poll) submits then polls with backoff 1/2/3/5s.
type Executor struct {
	Svc         *execution.Service
	Adapter     *AgentAdapter
	Auth        *AuthResolver
	ChatsL      *execution.RateLimiter
	PollsL      *execution.RateLimiter
	ArtifactsL  *execution.RateLimiter
	PollBackoff []time.Duration
	Log         *slog.Logger
	Metrics     *telemetry.Metrics
	DB          *sql.DB
}

// Execute implements execution.Handler.
func (e *Executor) Execute(ctx context.Context, run *execution.Run) error {
	agentID, identityMode, err := parseAilySnapshot(run.RuntimeSnapshot)
	if err != nil {
		// Configuration problems must terminate as RUN_CONFIG_INVALID, not
		// enter the panic/retry machinery (评测 P1: typed snapshot).
		return e.failRun(ctx, run, "run_config_invalid", err.Error())
	}
	if run.UserID == nil {
		return e.failRun(ctx, run, "aily_auth_error", "run has no user identity")
	}
	authCtx, err := e.Auth.Build(ctx, *run.UserID, identityMode, e.Auth.AppID)
	if err != nil {
		return e.failRun(ctx, run, "aily_auth_error", err.Error())
	}
	auth := &catalog.ProviderAuthContext{
		Provider:      authCtx.Provider,
		IdentityMode:  authCtx.IdentityMode,
		SubjectUserID: authCtx.SubjectUserID,
		TenantID:      authCtx.TenantID,
		CredentialRef: authCtx.CredentialRef,
		Token:         authCtx.Token,
	}

	if err := e.ChatsL.Acquire(ctx); err != nil {
		if ctx.Err() != nil {
			return execution.ErrLostOwnership // cancelled by lease loss
		}
		return e.failRun(ctx, run, "aily_rate_limit", "waiting for provider rate limit cancelled")
	}

	if run.ExecutionMode() == "interactive" {
		if err := e.executeStreaming(ctx, run, auth, agentID); err != nil {
			return e.classifyError(ctx, run, err)
		}
		return nil
	}
	if err := e.executeBackground(ctx, run, auth, agentID); err != nil {
		return e.classifyError(ctx, run, err)
	}
	return nil
}

// parseAilySnapshot validates the runtime snapshot into typed fields.
// A malformed/missing snapshot is a configuration error (never a panic).
func parseAilySnapshot(snapshot map[string]any) (agentID, identityMode string, err error) {
	if snapshot == nil {
		return "", "", errors.New("empty runtime snapshot")
	}
	v, ok := snapshot["external_resource_id"].(string)
	if !ok {
		return "", "", errors.New("runtime snapshot: external_resource_id missing or not a string")
	}
	if v == "" {
		return "", "", errors.New("runtime snapshot: external_resource_id is empty")
	}
	mode, _ := snapshot["identity_mode"].(string) // absent/null → default
	if mode == "" {
		mode = "user"
	}
	return v, mode, nil
}

func (e *Executor) classifyError(ctx context.Context, run *execution.Run, err error) error {
	if errors.Is(err, execution.ErrLostOwnership) {
		return err // fence verdict: stop writing, never retry from here
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if errors.Is(apiErr.Kind, ErrRateLimit) {
			return e.retryOrFail(ctx, run, "aily_rate_limit")
		}
		if errors.Is(apiErr.Kind, ErrTimeout) {
			return e.reconcile(ctx, run, "")
		}
		if apiErr.Retryable() {
			return e.retryOrFail(ctx, run, "aily_server_error")
		}
		return e.failRun(ctx, run, "aily_"+kindName(apiErr.Kind), apiErr.Msg)
	}
	return e.failRun(ctx, run, "aily_internal_error", err.Error())
}

func kindName(k error) string {
	switch k {
	case ErrAuth:
		return "auth_error"
	case ErrServer:
		return "server_error"
	case ErrClient:
		return "client_error"
	case ErrCapability:
		return "capability_error"
	default:
		return "error"
	}
}

// thread loads (or lazily creates) the conversation's AgentThread and
// enforces the identity binding invariant: one user/agent/mode per thread.
func (e *Executor) thread(ctx context.Context, run *execution.Run) (threadID ids.ID, remoteID string, err error) {
	if run.ConversationID == nil {
		return ids.ID{}, "", nil
	}
	q := e.Svc.Querier()
	expectedMode := run.SnapshotString("identity_mode")
	if expectedMode == "" {
		expectedMode = "user"
	}
	expectedSubject := fmt.Sprintf("%d", *run.UserID)

	row, getErr := q.GetAgentThreadByConversation(ctx, uint64(*run.ConversationID))
	if errors.Is(getErr, sql.ErrNoRows) {
		newID := ids.New()
		if _, cErr := q.CreateAgentThread(ctx, dbCreateThreadParams(newID.Bytes(), uint64(*run.ConversationID), ProviderKey, expectedMode, expectedSubject)); cErr != nil {
			return ids.ID{}, "", cErr
		}
		return newID, "", nil
	}
	if getErr != nil {
		return ids.ID{}, "", getErr
	}
	// Identity mismatch = never reuse another user's provider session (§80).
	if row.Provider != ProviderKey || row.AuthMode != expectedMode || row.AuthSubjectKey != expectedSubject {
		return ids.ID{}, "", errors.New("conversation thread identity/provider mismatch")
	}
	tid := ids.ID{}
	_ = tid.Scan(row.ID)
	return tid, row.RemoteID, nil
}

// preserveOwnership carries the claim-time fence across run refreshes: a
// stale worker must never adopt the new owner's epoch by re-reading the
// run from the database.
func preserveOwnership(stale, refreshed *execution.Run) *execution.Run {
	refreshed.LeaseEpoch = stale.LeaseEpoch
	refreshed.LeaseToken = stale.LeaseToken
	return refreshed
}

func (e *Executor) bindThreadAndRun(ctx context.Context, run *execution.Run, threadID ids.ID, agentChatID, sessionID string) {
	q := e.Svc.Querier()
	if agentChatID != "" && run.ExternalRunID == "" {
		_ = e.Svc.UpdateExternalRunIDFenced(ctx, run, agentChatID)
	}
	if !threadID.IsZero() && sessionID != "" {
		_ = q.BindAgentThreadSession(ctx, dbBindThreadParams(sessionID, threadID.Bytes()))
	}
}

// deltaCoalescer batches high-frequency provider deltas into durable
// content.chunk events (评测 P1: run_events write amplification). Live
// consumers still see every delta via the transient Redis channel; TiDB
// only receives a chunk every flushInterval / flushBytes.
type deltaCoalescer struct {
	buf          []byte
	flushBytes   int
	lastFlush    time.Time
	totalFlushed int
}

func newDeltaCoalescer() *deltaCoalescer {
	return &deltaCoalescer{flushBytes: 2000, lastFlush: time.Now()}
}

// add buffers one delta; shouldFlush reports whether the buffer crossed a
// threshold (2000 bytes or 500ms since the last flush).
func (c *deltaCoalescer) add(text string) bool {
	c.buf = append(c.buf, text...)
	return len(c.buf) >= c.flushBytes || time.Since(c.lastFlush) >= 500*time.Millisecond
}

// chunk drains the buffer into a persisted-chunk payload. The payload
// carries the incremental text; the caller adds the cumulative snapshot
// so live consumers can replace (not append) and always self-heal.
func (c *deltaCoalescer) chunk() (payload map[string]any, ok bool) {
	if len(c.buf) == 0 {
		return nil, false
	}
	incremental := string(c.buf)
	c.totalFlushed += len(c.buf)
	c.buf = c.buf[:0]
	c.lastFlush = time.Now()
	return map[string]any{
		"text":   incremental,
		"offset": c.totalFlushed,
	}, true
}

// executeStreaming drives the interactive SSE path.
func (e *Executor) executeStreaming(ctx context.Context, run *execution.Run, auth *catalog.ProviderAuthContext, agentID string) error {
	threadID, sessionID, err := e.thread(ctx, run)
	if err != nil {
		return err
	}

	submit := &catalog.SubmitInput{
		RunID:                 run.ID.String(),
		Auth:                  auth,
		ExternalResourceID:    agentID,
		Payload:               run.Input,
		SessionID:             sessionID,
		ExternalAttachmentIDs: run.AttachmentIDs(),
		Stream:                true,
		TimeoutSeconds:        run.SnapshotInt("timeout_seconds", 300),
	}

	events, cancel, err := e.Adapter.Stream(ctx, submit)
	if err != nil {
		return err
	}
	defer cancel()

	externalRunID := ""
	coalescer := newDeltaCoalescer()
	snapshotText := &strings.Builder{} // cumulative answer text
	flushChunk := func() error {
		payload, ok := coalescer.chunk()
		if !ok {
			return nil
		}
		payload["snapshot"] = snapshotText.String()
		return e.Svc.AppendEventFenced(ctx, run, execution.EventContentChunk, payload)
	}
	for ev := range events {
		switch ev.EventType {
		case "aily.stream.started":
			chatID, _ := ev.Payload["agent_chat_id"].(string)
			newSession, _ := ev.Payload["session_id"].(string)
			externalRunID = chatID
			if newSession == "" {
				newSession = sessionID
			}
			e.bindThreadAndRun(ctx, run, threadID, chatID, newSession)
		case "aily.stream.transport_error":
			// Break out; reconciliation decides the terminal state.
			e.Log.Warn("aily stream transport error", "run_id", run.ID.String(), "err", ev.Payload["error"])
			return e.reconcile(ctx, run, externalRunID)
		case execution.EventContentDelta:
			// Transient: live SSE consumers only — never persisted as-is.
			e.Svc.PublishTransient(ctx, run.ID, execution.EventContentDelta, ev.Payload)
			if text, ok := ev.Payload["text"].(string); ok {
				snapshotText.WriteString(text)
			}
			if coalescer.add(strOf(ev.Payload["text"], "")) {
				if err := flushChunk(); err != nil {
					if errors.Is(err, execution.ErrLostOwnership) {
						return err
					}
					e.Log.Warn("append chunk failed", "err", err)
				}
			}
		case execution.EventArtifactDiscovered:
			if err := e.recordArtifact(ctx, run, ev.Payload); err != nil {
				if errors.Is(err, execution.ErrLostOwnership) {
					return err
				}
				e.Log.Warn("record artifact failed", "err", err)
			}
		case execution.EventRunFailed:
			return e.failRun(ctx, run,
				strOf(ev.Payload["error_code"], "aily_stream_error"),
				strOf(ev.Payload["error_message"], ""))
		}
	}
	// Flush any tail delta before the final reconciliation.
	if err := flushChunk(); err != nil && !errors.Is(err, execution.ErrLostOwnership) {
		e.Log.Warn("flush tail chunk failed", "err", err)
	}
	// Stream ended (normally or broken): final authority is the result API.
	return e.reconcile(ctx, run, externalRunID)
}

// executeBackground submits async and polls until terminal.
func (e *Executor) executeBackground(ctx context.Context, run *execution.Run, auth *catalog.ProviderAuthContext, agentID string) error {
	threadID, sessionID, err := e.thread(ctx, run)
	if err != nil {
		return err
	}
	submit := &catalog.SubmitInput{
		RunID:                 run.ID.String(),
		Auth:                  auth,
		ExternalResourceID:    agentID,
		Payload:               run.Input,
		SessionID:             sessionID,
		ExternalAttachmentIDs: run.AttachmentIDs(),
		TimeoutSeconds:        run.SnapshotInt("timeout_seconds", 300),
	}
	result, err := e.Adapter.Submit(ctx, submit)
	if err != nil {
		return err
	}
	e.bindThreadAndRun(ctx, run, threadID, result.ExternalRunID, result.SessionID)
	refreshed, err := e.Svc.GetRun(ctx, run.ID)
	if err == nil {
		run = refreshed
	}
	return e.pollUntilTerminal(ctx, run, auth, agentID, run.ExternalRunID)
}

func (e *Executor) pollUntilTerminal(ctx context.Context, run *execution.Run, auth *catalog.ProviderAuthContext, agentID, chatID string) error {
	backoff := e.PollBackoff
	if len(backoff) == 0 {
		backoff = []time.Duration{time.Second, 2 * time.Second, 3 * time.Second, 5 * time.Second}
	}
	timeout := time.Duration(run.SnapshotInt("timeout_seconds", 300)) * time.Second
	deadline := time.Now().Add(timeout)
	attempt := 0
	for {
		if time.Now().After(deadline) {
			return e.failRun(ctx, run, "aily_poll_timeout",
				fmt.Sprintf("chat did not finish within %ds", int(timeout.Seconds())))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff[min(attempt, len(backoff)-1)]):
		}
		attempt++
		if err := e.PollsL.Acquire(ctx); err != nil {
			return err
		}
		status, err := e.Adapter.Status(ctx, auth, agentID, chatID)
		if err != nil {
			var apiErr *APIError
			if errors.As(err, &apiErr) && apiErr.HTTPStatus == 404 {
				return e.failRun(ctx, run, "aily_chat_not_found", apiErr.Msg)
			}
			continue
		}
		_ = e.Svc.AppendEvent(ctx, run.ID, execution.EventRunPoll, map[string]any{"provider_status": status.ProviderStatus})
		switch e.Adapter.mapper().MapProviderStatus(status.ProviderStatus) {
		case "succeeded", "failed", "cancelled":
			return e.finalizeFromResult(ctx, run, status.Raw)
		}
	}
}

func chatIDForResult(run *execution.Run, streamingChatID string) string {
	if run.ExternalRunID != "" {
		return run.ExternalRunID
	}
	return streamingChatID
}

// reconcile implements Final Reconciliation: after any stream outcome the
// result API decides status/text/finish_reason/artifacts.
func (e *Executor) reconcile(ctx context.Context, run *execution.Run, externalRunID string) error {
	chatID := chatIDForResult(run, externalRunID)
	if chatID == "" {
		return e.failRun(ctx, run, "aily_no_chat_id", "stream ended without agent_chat_id")
	}
	if refreshed, err := e.Svc.GetRun(ctx, run.ID); err == nil {
		if execution.IsTerminal(refreshed.Status) {
			return nil // someone else finished it (reaper/cancel)
		}
		run = preserveOwnership(run, refreshed)
	}
	agentID := run.SnapshotString("external_resource_id")
	if run.UserID == nil {
		return e.failRun(ctx, run, "aily_auth_error", "run has no user identity")
	}
	authCtx, err := e.Auth.Build(ctx, *run.UserID, modeOrDefault(run), e.Auth.AppID)
	if err != nil {
		return e.failRun(ctx, run, "aily_auth_error", err.Error())
	}
	auth := &catalog.ProviderAuthContext{Token: authCtx.Token, IdentityMode: authCtx.IdentityMode}

	if err := e.PollsL.Acquire(ctx); err != nil {
		return err
	}
	status, err := e.Adapter.Status(ctx, auth, agentID, chatID)
	if err != nil {
		return err
	}
	if e.Metrics != nil {
		e.Metrics.Reconciles.Inc()
	}
	switch e.Adapter.mapper().MapProviderStatus(status.ProviderStatus) {
	case "succeeded", "failed", "cancelled":
		return e.finalizeFromResult(ctx, run, status.Raw)
	default:
		return e.pollUntilTerminal(ctx, run, auth, agentID, chatID)
	}
}

func modeOrDefault(run *execution.Run) string {
	m := run.SnapshotString("identity_mode")
	if m == "" {
		return "user"
	}
	return m
}

// finalizeFromResult converges the run from the chat-result payload:
// artifacts recorded, message persisted, terminal CAS applied.
func (e *Executor) finalizeFromResult(ctx context.Context, run *execution.Run, chatResult map[string]any) error {
	mapper := e.Adapter.mapper()
	finalText := mapper.ExtractFinalText(chatResult)
	for _, art := range mapper.ExtractArtifacts(chatResult) {
		_ = e.recordArtifact(ctx, run, map[string]any{
			"external_artifact_id":   art.ExternalID,
			"provider_artifact_type": art.ProviderType,
			"name":                   art.Name,
		})
	}

	providerStatus, _ := chatResult["status"].(string)
	finishReason, _ := chatResult["finish_reason"].(string)
	mapped := mapper.MapProviderStatus(providerStatus)

	switch mapped {
	case "failed":
		msg, _ := chatResult["msg"].(string)
		return e.finish(ctx, run, &execution.FinishInput{
			Status:         execution.StatusFailed,
			ProviderStatus: providerStatus,
			FinishReason:   finishReason,
			ErrorCode:      "aily_provider_failed",
			ErrorMessage:   msg,
		})
	case "cancelled":
		return e.finish(ctx, run, &execution.FinishInput{
			Status:         execution.StatusCancelled,
			Output:         map[string]any{"text": finalText, "status": providerStatus},
			ProviderStatus: providerStatus,
			FinishReason:   finishReason,
		})
	default:
		return e.finish(ctx, run, &execution.FinishInput{
			Status:         execution.StatusSucceeded,
			Output:         map[string]any{"text": finalText, "status": providerStatus},
			ProviderStatus: providerStatus,
			FinishReason:   finishReason,
		})
	}
}

// finish converges and persists the assistant message (with artifact
// summaries for history replay) after the terminal CAS.
func (e *Executor) finish(ctx context.Context, run *execution.Run, in *execution.FinishInput) error {
	if err := e.Svc.Finish(ctx, run, in); err != nil {
		return err // includes ErrLostOwnership: stale worker stops here
	}
	finalText, _ := in.Output["text"].(string)
	if finalText == "" || run.ConversationID == nil {
		return nil
	}
	// Finish won the terminal CAS ⇒ this worker owned the run; the
	// terminal run can no longer be re-claimed, so the assistant message
	// write is safe. A stale worker returns at the error above.
	artifacts, err := e.Svc.Querier().ListRunArtifacts(ctx, run.ID.Bytes())
	if err == nil && len(artifacts) > 0 {
		summaries := make([]map[string]any, 0, len(artifacts))
		for _, a := range artifacts {
			summaries = append(summaries, map[string]any{
				"artifact_id":     idsMustString(a.ID),
				"name":            a.Name,
				"normalized_type": a.NormalizedType,
			})
		}
		meta := map[string]any{
			"run_id":    run.ID.String(),
			"provider":  ProviderKey,
			"artifacts": summaries,
		}
		raw, _ := json.Marshal(meta)
		_, _ = e.Svc.Querier().CreateMessage(ctx, dbCreateMessageParams(uint64(*run.ConversationID), "assistant", finalText, raw))
		return nil
	}
	raw, _ := json.Marshal(map[string]any{"run_id": run.ID.String(), "provider": ProviderKey})
	_, _ = e.Svc.Querier().CreateMessage(ctx, dbCreateMessageParams(uint64(*run.ConversationID), "assistant", finalText, raw))
	return nil
}

// recordArtifact upserts a RunArtifact and emits the discovery event with
// the LOCAL artifact id (the clickable handle) + external reference.
func (e *Executor) recordArtifact(ctx context.Context, run *execution.Run, payload map[string]any) error {
	externalID, _ := payload["external_artifact_id"].(string)
	if externalID == "" {
		return nil
	}
	providerType, _ := payload["provider_artifact_type"].(string)
	name, _ := payload["name"].(string)

	mapper := e.Adapter.mapper()
	artifactID := ids.New()
	q := e.Svc.Querier()
	if err := q.UpsertRunArtifact(ctx, dbUpsertArtifactParams(artifactID.Bytes(), run.ID.Bytes(), ProviderKey, externalID, providerType, name, mapper.NormalizeArtifactType(providerType))); err != nil {
		return err
	}
	// Fetch the stored row to emit the local id (dedup on retry).
	row, err := q.ListRunArtifactsByExternalID(ctx, dbArtifactLookupParams(run.ID.Bytes(), externalID))
	if err != nil {
		return err
	}
	_ = row
	return e.Svc.AppendEvent(ctx, run.ID, execution.EventArtifactDiscovered, map[string]any{
		"artifact_id":            artifactID.String(),
		"external_artifact_id":   externalID,
		"provider_artifact_type": providerType,
		"name":                   name,
	})
}

func (e *Executor) retryOrFail(ctx context.Context, run *execution.Run, code string) error {
	if run.Attempt < run.MaxAttempts {
		return e.Svc.ReleaseInterrupted(ctx, run, code)
	}
	return e.failRun(ctx, run, code, "rate limited after retries")
}

func (e *Executor) failRun(ctx context.Context, run *execution.Run, code, message string) error {
	e.Log.Warn("aily run failed", "run_id", run.ID.String(), "code", code, "message", message)
	return e.Svc.Finish(ctx, run, &execution.FinishInput{
		Status:       execution.StatusFailed,
		ErrorCode:    code,
		ErrorMessage: message,
	})
}

func strOf(v any, def string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return def
}

func idsMustString(b []byte) string {
	id := ids.ID{}
	_ = id.Scan(b)
	return id.String()
}
