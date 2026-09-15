package aily

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/creation-agent-studio/backend-go/internal/catalog"
)

// ProviderAPI is the Aily HTTP surface the adapter depends on. It exists
// so the SUBMIT boundary is injectable: the executor must be able to
// prove that a gated run never reaches the provider, which is only
// observable at the real StartChat / OpenStreamChat call (第四轮 P1-1 —
// a mocked executor handler proves nothing about HTTP).
type ProviderAPI interface {
	// StartChat is the async (non-streaming) submit: one HTTP POST.
	StartChat(ctx context.Context, agentID, token string, contentItems []map[string]any, attachmentIDs []string, sessionID string) (chatID, newSession string, err error)
	// OpenStreamChat sends the streaming POST synchronously and returns
	// the opened SSE body (provider already reached on success).
	OpenStreamChat(ctx context.Context, agentID, token string, contentItems []map[string]any, attachmentIDs []string, sessionID string) (io.ReadCloser, error)
	GetChatResult(ctx context.Context, agentID, token, chatID string) (json.RawMessage, error)
	UploadAttachment(ctx context.Context, agentID, token string, data []byte, filename, attachmentType, docURL string) (string, error)
	GetArtifact(ctx context.Context, agentID, token, artifactID string) (*ArtifactDownload, error)
	CheckVisibility(ctx context.Context, agentID, uat string) (bool, error)
}

var _ ProviderAPI = (*Client)(nil)

func jsonUnmarshal(raw json.RawMessage, out any) error {
	return json.Unmarshal(raw, out)
}

// AgentAdapter is the RuntimeAdapter for Aily custom agents.
//
// Resource mapping (verified Domain model — do not change):
//
//	binding.external_resource_id = Aily agent_id
//	AgentThread.remote_id        = Aily session_id   (lazy)
//	Run.external_run_id          = Aily agent_chat_id
//	attachment.external id       = Aily agent_attachment_id
//	artifact.external id         = Aily agent_artifact_id
type AgentAdapter struct {
	api  ProviderAPI
	auth *AuthResolver

	mu       sync.Mutex
	capables catalog.Capabilities
}

func NewAgentAdapter(client *Client, auth *AuthResolver) *AgentAdapter {
	return newAgentAdapter(client, auth)
}

// NewAgentAdapterWithAPI wires an explicit ProviderAPI — used by the
// submit-boundary tests (and any future transport) that must observe the
// real provider calls instead of a mocked executor handler.
func NewAgentAdapterWithAPI(api ProviderAPI, auth *AuthResolver) *AgentAdapter {
	return newAgentAdapter(api, auth)
}

func newAgentAdapter(api ProviderAPI, auth *AuthResolver) *AgentAdapter {
	return &AgentAdapter{
		api:  api,
		auth: auth,
		capables: catalog.Capabilities{
			"streaming":       true,
			"async_execution": true,
			"conversation":    true,
			"attachment":      true,
			"artifact":        true,
			"visibility":      true,
			"file_upload":     true,
			"cancel":          false, // no cancel endpoint in the API
			"resume":          false,
		},
	}
}

// Official input limits (agent_adapter validation in the reference).
const (
	maxContentItems = 100
	maxTextChars    = 10000
	maxAttachments  = 8
)

// Allowed attachment formats (official docs).
var allowedFileExts = map[string]bool{"png": true, "jpg": true, "jpeg": true, "pdf": true}

func (a *AgentAdapter) Key() string { return ProviderKey + ":agent" }

// SubmitIdempotency reports the weakest class on purpose (第九轮 P0-2).
//
// The Aily custom-agent API exposes `POST /agents/:agent_id/chats` with no
// idempotency key and no request-key lookup: a chat can only be resolved by
// the `agent_chat_id` the create call returned, and that id is exactly what
// is missing when the outcome is unknown. So a resend after an ambiguous
// failure is an INDEPENDENT second chat — it cannot be deduplicated by the
// provider.
//
// Returning "none" makes the executor park the run in waiting_external
// instead of retrying. A future adapter whose provider does accept a key
// implements catalog.IdempotencyAware and returns IdempotencyNative.
func (a *AgentAdapter) SubmitIdempotency() catalog.IdempotencyClass { return catalog.IdempotencyNone }

func (a *AgentAdapter) Capabilities() catalog.Capabilities { return a.capables }

func (a *AgentAdapter) DisplayLabel() string      { return "飞书 Aily 自定义智能体" }
func (a *AgentAdapter) ResourceIDLabel() string   { return "Agent ID" }
func (a *AgentAdapter) ResourceIDPattern() string { return `^agent_[0-9A-Za-z]+$` }
func (a *AgentAdapter) ResourceIDRequired() bool  { return true }
func (a *AgentAdapter) ResourceIDHint() string {
	return "飞书智能体 AgentID，形如 agent_4k6wf15ngw7wu；在智能体编辑页的浏览器地址栏中获取"
}

// BuildAuth resolves studio identity → provider credential (§49).
func (a *AgentAdapter) BuildAuth(ctx context.Context, userID int64, identityMode string) (*catalog.ProviderAuthContext, error) {
	if identityMode == "" {
		identityMode = "user"
	}
	appID := a.auth.AppID
	ac, err := a.auth.Build(ctx, userID, identityMode, appID)
	if err != nil {
		return nil, err
	}
	return &catalog.ProviderAuthContext{
		Provider:      ac.Provider,
		IdentityMode:  ac.IdentityMode,
		SubjectUserID: ac.SubjectUserID,
		TenantID:      ac.TenantID,
		CredentialRef: ac.CredentialRef,
		Token:         ac.Token,
	}, nil
}

// ValidateContent enforces the official request limits before hitting Aily.
func ValidateContent(contentItems []map[string]any) error {
	if len(contentItems) == 0 {
		return fmt.Errorf("%w: user_message.content must not be empty", ErrCapability)
	}
	if len(contentItems) > maxContentItems {
		return fmt.Errorf("%w: content items exceed %d", ErrCapability, maxContentItems)
	}
	for _, item := range contentItems {
		t, _ := item["type"].(string)
		if t != "text" {
			return fmt.Errorf("%w: content type %q not supported (only text)", ErrCapability, t)
		}
		if text, _ := item["text"].(string); len([]rune(text)) > maxTextChars {
			return fmt.Errorf("%w: text exceeds %d chars", ErrCapability, maxTextChars)
		}
	}
	return nil
}

// ValidateAttachments enforces ≤8 attachment ids per chat.
func ValidateAttachments(ids []string) error {
	if len(ids) > maxAttachments {
		return fmt.Errorf("%w: attachment ids exceed %d per chat", ErrCapability, maxAttachments)
	}
	return nil
}

// ValidateSubmit enforces every LOCAL submit rule (content limits,
// attachment count). It performs no IO, so it belongs BEFORE the
// pre-submit gate: an invalid payload must never consume a provider
// attempt and never reach the provider (第四轮 P1-1).
func (a *AgentAdapter) ValidateSubmit(in *catalog.SubmitInput) error {
	if err := ValidateContent(contentFromPayload(in.Payload)); err != nil {
		return err
	}
	return ValidateAttachments(in.ExternalAttachmentIDs)
}

// Submit issues an async (non-streaming) chat: validate, then submit.
func (a *AgentAdapter) Submit(ctx context.Context, in *catalog.SubmitInput) (*catalog.SubmitResult, error) {
	if err := a.ValidateSubmit(in); err != nil {
		return nil, err
	}
	return a.SubmitPrepared(ctx, in)
}

// SubmitPrepared submits a chat whose payload already passed
// ValidateSubmit. It performs no local work, so the caller can place the
// pre-submit kill switch immediately before it: after this call the only
// remaining steps are the attempt CAS and the HTTP request.
//
// in.ProviderIdempotencyKey is intentionally NOT sent: the Aily API has no
// field to carry it (see SubmitIdempotency). Passed through the neutral
// contract anyway so the executor's call shape is provider-independent.
func (a *AgentAdapter) SubmitPrepared(ctx context.Context, in *catalog.SubmitInput) (*catalog.SubmitResult, error) {
	chatID, sessionID, err := a.api.StartChat(ctx, in.ExternalResourceID, in.Auth.Token,
		contentFromPayload(in.Payload), in.ExternalAttachmentIDs, in.SessionID)
	if err != nil {
		return nil, err
	}
	return &catalog.SubmitResult{ExternalRunID: chatID, SessionID: sessionID}, nil
}

// contentFromPayload normalizes run input content items. JSON round-trips
// produce []any of map[string]any; in-memory construction may produce
// []map[string]any directly — tolerate both.
func contentFromPayload(payload map[string]any) []map[string]any {
	switch raw := payload["content"].(type) {
	case []any:
		out := make([]map[string]any, 0, len(raw))
		for _, item := range raw {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	case []map[string]any:
		return raw
	default:
		return nil
	}
}

// Status polls 获取对话结果 and normalizes the outcome.
func (a *AgentAdapter) Status(ctx context.Context, auth *catalog.ProviderAuthContext, externalResourceID, externalRunID string) (*catalog.StatusResult, error) {
	raw, err := a.api.GetChatResult(ctx, externalResourceID, auth.Token, externalRunID)
	if err != nil {
		return nil, err
	}
	var data map[string]any
	if err := jsonUnmarshal(raw, &data); err != nil {
		return nil, err
	}
	mapper := Mapper{}
	text := mapper.ExtractFinalText(data)
	status, _ := data["status"].(string)
	finishReason, _ := data["finish_reason"].(string)
	artifacts := mapper.ExtractArtifacts(data)
	arts := make([]map[string]any, 0, len(artifacts))
	for _, ar := range artifacts {
		arts = append(arts, map[string]any{
			"external_artifact_id":   ar.ExternalID,
			"provider_artifact_type": ar.ProviderType,
			"name":                   ar.Name,
		})
	}
	return &catalog.StatusResult{
		ExternalRunID:  externalRunID,
		ProviderStatus: status,
		FinishReason:   finishReason,
		Output: map[string]any{
			"text":       text,
			"artifacts":  arts,
			"raw_status": status,
		},
		Raw: data,
	}, nil
}

// Stream issues a streaming chat and yields mapped unified events on a
// channel. The first event always surfaces run/session identity so the
// caller can persist agent_chat_id / session_id even if the stream breaks.
func (a *AgentAdapter) Stream(ctx context.Context, in *catalog.SubmitInput) (<-chan catalog.StreamEvent, func(), error) {
	if err := a.ValidateSubmit(in); err != nil {
		return nil, nil, err
	}
	return a.StreamPrepared(ctx, in)
}

// StreamPrepared opens the stream SYNCHRONOUSLY and then pumps the
// frames in a goroutine. The HTTP POST happens on the calling goroutine
// (第四轮 P1-1): the caller can therefore run the pre-submit kill switch
// on the line immediately above this call and still be the last
// checkpoint before the provider sees the request.
//
// The returned cancel closes the SSE body as well as the derived context,
// so a cancelled handler cannot leak the provider connection.
func (a *AgentAdapter) StreamPrepared(ctx context.Context, in *catalog.SubmitInput) (<-chan catalog.StreamEvent, func(), error) {
	body, err := a.api.OpenStreamChat(ctx, in.ExternalResourceID, in.Auth.Token,
		contentFromPayload(in.Payload), in.ExternalAttachmentIDs, in.SessionID)
	if err != nil {
		return nil, nil, err
	}
	out := make(chan catalog.StreamEvent, 64)
	cctx, cancel := context.WithCancel(ctx)
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			_ = body.Close()
		})
	}
	go func() {
		defer close(out)
		defer stop()
		started := false
		mapper := Mapper{}
		err := pumpSSE(cctx, body, func(eventName string, data []byte) error {
			parsed := mapper.ParseSSEData(data)
			if !started {
				started = true
				chatID, _ := parsed["agent_chat_id"].(string)
				sessionID, _ := parsed["session_id"].(string)
				if err := emitStreamEvent(cctx, out, catalog.StreamEvent{
					EventType: "aily.stream.started",
					Payload: map[string]any{
						"agent_chat_id": chatID,
						"session_id":    sessionID,
					},
				}); err != nil {
					return err
				}
			}
			for _, ev := range mapper.ToUnified(eventName, parsed) {
				if err := emitStreamEvent(cctx, out, catalog.StreamEvent{
					EventType: ev.Type,
					Payload:   ev.Payload,
				}); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			// Transport-level failure: emit a failed event? No — the
			// executor reconciles via GetChatResult instead (§26).
			_ = emitStreamEvent(cctx, out, catalog.StreamEvent{
				EventType: "aily.stream.transport_error",
				Payload:   map[string]any{"error": err.Error()},
			})
		}
	}()
	return out, stop, nil
}

// emitStreamEvent is the ONLY way the stream producer may hand an event to
// the consumer (第五轮 P1-2). A bare `out <- ev` on a full buffer cannot be
// interrupted by closing the SSE body or cancelling the context, so an
// early executor exit (lost lease ownership, DB error, worker shutdown)
// would strand the producer goroutine forever — and `defer close(out)` /
// `defer stop()` would never run.
func emitStreamEvent(ctx context.Context, out chan<- catalog.StreamEvent, ev catalog.StreamEvent) error {
	select {
	case out <- ev:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// UploadAttachment streams bytes to Aily under the caller's UAT.
func (a *AgentAdapter) UploadAttachment(ctx context.Context, auth *catalog.ProviderAuthContext, externalResourceID string, in *catalog.AttachmentInput) (string, error) {
	if in.AttachmentType == "image" || in.AttachmentType == "file" {
		ext := fileExt(in.Filename)
		if !allowedFileExts[ext] {
			return "", fmt.Errorf("%w: file type .%s not allowed (png/jpg/pdf only)", ErrCapability, ext)
		}
		if in.AttachmentType == "image" && int64(len(in.Data)) > 5*1024*1024 {
			return "", fmt.Errorf("%w: image exceeds 5MB", ErrCapability)
		}
		if int64(len(in.Data)) > 40*1024*1024 {
			return "", fmt.Errorf("%w: file exceeds 40MB", ErrCapability)
		}
	}
	return a.api.UploadAttachment(ctx, externalResourceID, auth.Token,
		in.Data, in.Filename, in.AttachmentType, in.DocURL)
}

// ResolveArtifact fetches a fresh 24h signed URL for an artifact.
func (a *AgentAdapter) ResolveArtifact(ctx context.Context, auth *catalog.ProviderAuthContext, externalResourceID, externalArtifactID string) (*catalog.ArtifactRef, error) {
	art, err := a.api.GetArtifact(ctx, externalResourceID, auth.Token, externalArtifactID)
	if err != nil {
		return nil, err
	}
	return &catalog.ArtifactRef{
		ExternalArtifactID: art.ArtifactID,
		Name:               art.Name,
		URL:                art.URL,
	}, nil
}

// CheckVisibility requires UAT (docs) — tenant mode is refused.
func (a *AgentAdapter) CheckVisibility(ctx context.Context, auth *catalog.ProviderAuthContext, externalResourceID string) (bool, error) {
	if auth.IdentityMode != "user" {
		return false, fmt.Errorf("%w: Aily visibility check requires user identity (UAT)", ErrCapability)
	}
	return a.api.CheckVisibility(ctx, externalResourceID, auth.Token)
}

func fileExt(name string) string {
	out := ""
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '.' {
			out = name[i+1:]
			break
		}
	}
	return lower(out)
}

func lower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// mapper exposes the shared payload mapper to the executor.
func (a *AgentAdapter) mapper() Mapper { return Mapper{} }
