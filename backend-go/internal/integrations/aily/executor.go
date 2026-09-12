package aily

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	snapshot := run.RuntimeSnapshot
	agentID := snapshot["external_resource_id"].(string)
	if agentID == "" {
		return e.failRun(ctx, run, "aily_no_agent_id", "missing agent_id")
	}
	identityMode := snapshot["identity_mode"].(string)
	if identityMode == "" {
		identityMode = "user"
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

func (e *Executor) classifyError(ctx context.Context, run *execution.Run, err error) error {
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

func (e *Executor) bindThreadAndRun(ctx context.Context, run *execution.Run, threadID ids.ID, agentChatID, sessionID string) {
	q := e.Svc.Querier()
	if agentChatID != "" && run.ExternalRunID == "" {
		_ = q.UpdateRunExternalID(ctx, dbUpdateExternalParams(agentChatID, run.ID.Bytes()))
	}
	if !threadID.IsZero() && sessionID != "" {
		_ = q.BindAgentThreadSession(ctx, dbBindThreadParams(sessionID, threadID.Bytes()))
	}
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
			if err := e.Svc.AppendEvent(ctx, run.ID, execution.EventContentDelta, ev.Payload); err != nil {
				e.Log.Warn("append delta failed", "err", err)
			}
		case execution.EventArtifactDiscovered:
			if err := e.recordArtifact(ctx, run, ev.Payload); err != nil {
				e.Log.Warn("record artifact failed", "err", err)
			}
		case execution.EventRunFailed:
			return e.failRun(ctx, run,
				strOf(ev.Payload["error_code"], "aily_stream_error"),
				strOf(ev.Payload["error_message"], ""))
		}
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
		return err
	}
	finalText, _ := in.Output["text"].(string)
	if finalText == "" || run.ConversationID == nil {
		return nil
	}
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
