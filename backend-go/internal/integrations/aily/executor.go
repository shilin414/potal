package aily

import (
	"context"
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
//
// Correctness contract (Execution Correctness Closure):
//   - The executor holds ONLY the ownership-fenced WorkerOwnedService —
//     there is no unfenced canonical-write path reachable from here
//     (修复计划 §81: 编译期约束).
//   - Every canonical write goes through claimed.Ownership, which is
//     immutable for the attempt lifetime — a GetRun refresh can never
//     drop the lease token again (评测 §十).
type Executor struct {
	Owned       *execution.WorkerOwnedService
	Adapter     *AgentAdapter
	Auth        *AuthResolver
	ChatsL      *execution.RateLimiter
	PollsL      *execution.RateLimiter
	ArtifactsL  *execution.RateLimiter
	PollBackoff []time.Duration
	Log         *slog.Logger
	Metrics     *telemetry.Metrics
	// Gate is the PRE-SUBMIT kill switch (第三轮 P1-B, Gate 2). The
	// worker's claim-time gate (Gate 1) runs before the handler, but the
	// run may still wait here for auth resolution and a limiter token
	// while an admin disables the application or the provider — a run
	// that has not been SUBMITTED yet must still obey the kill switch.
	// Nil disables the check (tests / non-gated deployments).
	Gate execution.RunGate
}

// Execute implements execution.Handler.
func (e *Executor) Execute(ctx context.Context, claimed *execution.ClaimedRun) error {
	run := claimed.Run
	agentID, identityMode, err := parseAilySnapshot(run.RuntimeSnapshot)
	if err != nil {
		// Configuration problems must terminate as RUN_CONFIG_INVALID, not
		// enter the panic/retry machinery (评测 P1: typed snapshot).
		return e.failRun(ctx, claimed, "run_config_invalid", err.Error())
	}
	if run.UserID == nil {
		return e.failRun(ctx, claimed, "aily_auth_error", "run has no user identity")
	}
	authCtx, err := e.Auth.Build(ctx, *run.UserID, identityMode, e.Auth.AppID)
	if err != nil {
		return e.failRun(ctx, claimed, "aily_auth_error", err.Error())
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
		return e.failRun(ctx, claimed, "aily_rate_limit", "waiting for provider rate limit cancelled")
	}

	// Pre-submit kill switch (第三轮 P1-B, Gate 2): placed AFTER the rate
	// limiter (which itself may wait) and IMMEDIATELY before
	// BeginProviderAttempt — the closest possible checkpoint to the
	// provider submit. kill → cancelled, pause/infra → deferred with the
	// original priority, unknown verdict → fail closed. No attempt is
	// consumed on any gated path: the provider never saw this run.
	if execution.PreSubmitGate(ctx, e.Owned, claimed, e.Gate, e.Log) {
		return nil
	}

	// Attempt accounting (P0-2): attempt counts PROVIDER EXECUTIONS. The
	// claim no longer consumes one, and neither does provider admission —
	// only reaching this point (auth resolved, rate limit granted, about
	// to submit) does.
	if err := e.Owned.BeginProviderAttempt(ctx, claimed); err != nil {
		if errors.Is(err, execution.ErrProviderAttemptsExhausted) {
			return e.failRun(ctx, claimed, "aily_attempts_exhausted",
				"provider retry budget exhausted before submit")
		}
		return err // ErrLostOwnership → stop writing
	}

	if run.ExecutionMode() == "interactive" {
		if err := e.executeStreaming(ctx, claimed, auth, agentID); err != nil {
			return e.classifyError(ctx, claimed, err)
		}
		return nil
	}
	if err := e.executeBackground(ctx, claimed, auth, agentID); err != nil {
		return e.classifyError(ctx, claimed, err)
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

func (e *Executor) classifyError(ctx context.Context, claimed *execution.ClaimedRun, err error) error {
	if errors.Is(err, execution.ErrLostOwnership) {
		return err // fence verdict: stop writing, never retry from here
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if errors.Is(apiErr.Kind, ErrRateLimit) {
			return e.retryOrFail(ctx, claimed, "aily_rate_limit")
		}
		if errors.Is(apiErr.Kind, ErrTimeout) {
			return e.reconcile(ctx, claimed, "")
		}
		if apiErr.Retryable() {
			return e.retryOrFail(ctx, claimed, "aily_server_error")
		}
		return e.failRun(ctx, claimed, "aily_"+kindName(apiErr.Kind), apiErr.Msg)
	}
	return e.failRun(ctx, claimed, "aily_internal_error", err.Error())
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

// thread loads (or lazily creates) the conversation's AgentThread via the
// owned service, enforcing the identity binding invariant: one
// user/agent/mode per thread.
func (e *Executor) thread(ctx context.Context, claimed *execution.ClaimedRun) (threadID ids.ID, remoteID string, err error) {
	run := claimed.Run
	if run.ConversationID == nil {
		return ids.ID{}, "", nil
	}
	expectedMode := run.SnapshotString("identity_mode")
	if expectedMode == "" {
		expectedMode = "user"
	}
	expectedSubject := fmt.Sprintf("%d", *run.UserID)
	return e.Owned.EnsureAgentThread(ctx, *run.ConversationID, ProviderKey, expectedMode, expectedSubject)
}

func (e *Executor) bindThreadAndRun(ctx context.Context, claimed *execution.ClaimedRun, threadID ids.ID, agentChatID, sessionID string) error {
	if agentChatID != "" && claimed.Run.ExternalRunID == "" {
		if err := e.Owned.UpdateExternalRunID(ctx, claimed, agentChatID); err != nil {
			return err // includes ErrLostOwnership
		}
	}
	if !threadID.IsZero() && sessionID != "" {
		if err := e.Owned.BindProviderSession(ctx, claimed, threadID, sessionID); err != nil {
			if errors.Is(err, execution.ErrLostOwnership) {
				return err
			}
			// Session conflict / transient bind failure: the run itself is
			// unaffected; log and continue (session binding is set-once).
			e.Log.Warn("bind provider session failed",
				"run_id", claimed.Run.ID.String(), "err", err)
		}
	}
	return nil
}

// deltaCoalescer batches high-frequency provider deltas into durable
// content.chunk events (评测 P1: run_events write amplification). Live
// consumers still see every delta via the transient Redis channel; MySQL
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
func (e *Executor) executeStreaming(ctx context.Context, claimed *execution.ClaimedRun, auth *catalog.ProviderAuthContext, agentID string) error {
	run := claimed.Run
	threadID, sessionID, err := e.thread(ctx, claimed)
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
		return e.Owned.AppendEvent(ctx, claimed, execution.EventContentChunk, payload)
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
			if err := e.bindThreadAndRun(ctx, claimed, threadID, chatID, newSession); err != nil {
				if errors.Is(err, execution.ErrLostOwnership) {
					return err
				}
				e.Log.Warn("bind thread/run failed", "run_id", run.ID.String(), "err", err)
			}
		case "aily.stream.transport_error":
			// Break out; reconciliation decides the terminal state.
			e.Log.Warn("aily stream transport error", "run_id", run.ID.String(), "err", ev.Payload["error"])
			return e.reconcile(ctx, claimed, externalRunID)
		case execution.EventContentDelta:
			// Transient: live SSE consumers only — never persisted as-is.
			e.Owned.PublishTransient(ctx, claimed, execution.EventContentDelta, ev.Payload)
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
			if err := e.recordArtifact(ctx, claimed, ev.Payload); err != nil {
				if errors.Is(err, execution.ErrLostOwnership) {
					return err
				}
				e.Log.Warn("record artifact failed", "err", err)
			}
		case execution.EventRunFailed:
			return e.failRun(ctx, claimed,
				strOf(ev.Payload["error_code"], "aily_stream_error"),
				strOf(ev.Payload["error_message"], ""))
		}
	}
	// Flush any tail delta before the final reconciliation.
	if err := flushChunk(); err != nil && !errors.Is(err, execution.ErrLostOwnership) {
		e.Log.Warn("flush tail chunk failed", "err", err)
	}
	// Stream ended (normally or broken): final authority is the result API.
	return e.reconcile(ctx, claimed, externalRunID)
}

// executeBackground submits async and polls until terminal.
func (e *Executor) executeBackground(ctx context.Context, claimed *execution.ClaimedRun, auth *catalog.ProviderAuthContext, agentID string) error {
	run := claimed.Run
	threadID, sessionID, err := e.thread(ctx, claimed)
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
	if err := e.bindThreadAndRun(ctx, claimed, threadID, result.ExternalRunID, result.SessionID); err != nil {
		if errors.Is(err, execution.ErrLostOwnership) {
			return err
		}
		e.Log.Warn("bind thread/run failed", "run_id", run.ID.String(), "err", err)
	}
	// Refresh the run data WITHOUT touching the ownership: the fence
	// lives on claimed.Ownership and cannot be clobbered by a reload
	// (评测 §十: the old code overwrote the lease token here).
	if refreshed, err := e.Owned.GetRun(ctx, run.ID); err == nil {
		claimed.RefreshRun(refreshed)
	}
	return e.pollUntilTerminal(ctx, claimed, auth, agentID, claimed.Run.ExternalRunID)
}

func (e *Executor) pollUntilTerminal(ctx context.Context, claimed *execution.ClaimedRun, auth *catalog.ProviderAuthContext, agentID, chatID string) error {
	run := claimed.Run
	backoff := e.PollBackoff
	if len(backoff) == 0 {
		backoff = []time.Duration{time.Second, 2 * time.Second, 3 * time.Second, 5 * time.Second}
	}
	timeout := time.Duration(run.SnapshotInt("timeout_seconds", 300)) * time.Second
	deadline := time.Now().Add(timeout)
	attempt := 0
	for {
		if time.Now().After(deadline) {
			return e.failRun(ctx, claimed, "aily_poll_timeout",
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
				return e.failRun(ctx, claimed, "aily_chat_not_found", apiErr.Msg)
			}
			continue
		}
		// Poll events are owned writes: a stale background worker must
		// STOP polling the moment the fence is lost (修复计划 §22 —
		// log-and-continue is forbidden).
		if err := e.Owned.AppendEvent(ctx, claimed, execution.EventRunPoll, map[string]any{"provider_status": status.ProviderStatus}); err != nil {
			return err
		}
		switch e.Adapter.mapper().MapProviderStatus(status.ProviderStatus) {
		case "succeeded", "failed", "cancelled":
			return e.finalizeFromResult(ctx, claimed, status.Raw)
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
func (e *Executor) reconcile(ctx context.Context, claimed *execution.ClaimedRun, externalRunID string) error {
	run := claimed.Run
	chatID := chatIDForResult(run, externalRunID)
	if chatID == "" {
		return e.failRun(ctx, claimed, "aily_no_chat_id", "stream ended without agent_chat_id")
	}
	if refreshed, err := e.Owned.GetRun(ctx, run.ID); err == nil {
		if execution.IsTerminal(refreshed.Status) {
			return nil // someone else finished it (reaper/cancel)
		}
		// Ownership is immutable on the ClaimedRun — the refresh only
		// updates execution data.
		claimed.RefreshRun(refreshed)
	}
	agentID := run.SnapshotString("external_resource_id")
	if run.UserID == nil {
		return e.failRun(ctx, claimed, "aily_auth_error", "run has no user identity")
	}
	authCtx, err := e.Auth.Build(ctx, *run.UserID, modeOrDefault(run), e.Auth.AppID)
	if err != nil {
		return e.failRun(ctx, claimed, "aily_auth_error", err.Error())
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
		return e.finalizeFromResult(ctx, claimed, status.Raw)
	default:
		return e.pollUntilTerminal(ctx, claimed, auth, agentID, chatID)
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
// artifacts recorded, then the terminal finalize transaction (run CAS +
// terminal event + assistant message + occurrence + lease cleanup).
func (e *Executor) finalizeFromResult(ctx context.Context, claimed *execution.ClaimedRun, chatResult map[string]any) error {
	run := claimed.Run
	mapper := e.Adapter.mapper()
	finalText := mapper.ExtractFinalText(chatResult)
	for _, art := range mapper.ExtractArtifacts(chatResult) {
		if err := e.recordArtifact(ctx, claimed, map[string]any{
			"external_artifact_id":   art.ExternalID,
			"provider_artifact_type": art.ProviderType,
			"name":                   art.Name,
		}); err != nil && !errors.Is(err, execution.ErrLostOwnership) {
			e.Log.Warn("record artifact failed", "run_id", run.ID.String(), "err", err)
		}
	}

	providerStatus, _ := chatResult["status"].(string)
	finishReason, _ := chatResult["finish_reason"].(string)
	mapped := mapper.MapProviderStatus(providerStatus)

	in := &execution.FinishInput{
		ProviderStatus: providerStatus,
		FinishReason:   finishReason,
	}
	switch mapped {
	case "failed":
		msg, _ := chatResult["msg"].(string)
		in.Status = execution.StatusFailed
		in.ErrorCode = "aily_provider_failed"
		in.ErrorMessage = msg
	case "cancelled":
		in.Status = execution.StatusCancelled
		in.Output = map[string]any{"text": finalText, "status": providerStatus}
	default:
		in.Status = execution.StatusSucceeded
		in.Output = map[string]any{"text": finalText, "status": providerStatus}
	}
	// Assistant message durability (修复计划 §32): the message is part of
	// the finalize transaction — a succeeded run can never lose its answer.
	if finalText != "" && run.ConversationID != nil {
		in.AssistantText = finalText
		meta, err := e.assistantMetadata(ctx, run)
		if err == nil {
			in.AssistantMetadata = meta
		}
	}
	return e.Owned.Finalize(ctx, claimed, in)
}

// assistantMetadata builds the assistant message metadata with artifact
// summaries for history replay (same shape as before the closure, but the
// message itself is now written inside the finalize transaction).
func (e *Executor) assistantMetadata(ctx context.Context, run *execution.Run) (json.RawMessage, error) {
	artifacts, err := e.Owned.ListRunArtifacts(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	meta := map[string]any{
		"run_id":   run.ID.String(),
		"provider": ProviderKey,
	}
	if len(artifacts) > 0 {
		summaries := make([]map[string]any, 0, len(artifacts))
		for _, a := range artifacts {
			summaries = append(summaries, map[string]any{
				"artifact_id":     idsMustString(a.ID),
				"name":            a.Name,
				"normalized_type": a.NormalizedType,
			})
		}
		meta["artifacts"] = summaries
	}
	return json.Marshal(meta)
}

// recordArtifact persists a RunArtifact and emits the discovery event
// with the LOCAL artifact id (the clickable handle) + external reference
// — both fenced and idempotent (stable id across re-discovery).
func (e *Executor) recordArtifact(ctx context.Context, claimed *execution.ClaimedRun, payload map[string]any) error {
	externalID, _ := payload["external_artifact_id"].(string)
	if externalID == "" {
		return nil
	}
	providerType, _ := payload["provider_artifact_type"].(string)
	name, _ := payload["name"].(string)
	mapper := e.Adapter.mapper()
	_, err := e.Owned.PersistArtifact(ctx, claimed, execution.ArtifactInput{
		ExternalID:     externalID,
		Provider:       ProviderKey,
		ProviderType:   providerType,
		Name:           name,
		NormalizedType: mapper.NormalizeArtifactType(providerType),
	})
	return err
}

// retryOrFail: a retryable provider failure either requeues (run.retrying
// — non-terminal) through the OWNED path, or fails the run for good once
// the attempts are exhausted (修复计划 §23: the old code called the
// unfenced ReleaseInterrupted here — a stale worker could delete the new
// owner's lease).
func (e *Executor) retryOrFail(ctx context.Context, claimed *execution.ClaimedRun, code string) error {
	if claimed.Run.Attempt < claimed.Run.MaxAttempts {
		return e.Owned.Retry(ctx, claimed, code)
	}
	return e.Owned.Fail(ctx, claimed, code, "rate limited after retries")
}

func (e *Executor) failRun(ctx context.Context, claimed *execution.ClaimedRun, code, message string) error {
	e.Log.Warn("aily run failed", "run_id", claimed.Run.ID.String(), "code", code, "message", message)
	return e.Owned.Fail(ctx, claimed, code, message)
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
