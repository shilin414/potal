// Package aily implements the Feishu Aily custom-agent OpenAPI client and
// runtime adapter.
//
// Endpoints follow the official docs exactly
// (飞书开放平台文档/飞书aily/自定义智能体):
//
//	POST   /aily/v1/agents/:agent_id/chats                      发起智能体对话
//	GET    /aily/v1/agents/:agent_id/chats/:agent_chat_id        获取对话结果
//	POST   /aily/v1/agents/:agent_id/sessions                    创建会话
//	POST   /aily/v1/agents/:agent_id/attachments                 上传附件
//	GET    /aily/v1/agents/:agent_id/artifacts/:agent_artifact_id 下载智能体产物
//	POST   /aily/v1/agents/:agent_id/agent_visibility/check       校验可见性
//
// All calls use Bearer tokens; chat/attachment/visibility default to the
// calling user's UAT (the target agent rejects app identity — verified in
// the real environment, code 10009).
package aily

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Errors classify provider failures for the retry policy.
var (
	ErrRateLimit  = errors.New("aily: rate limited")
	ErrAuth       = errors.New("aily: auth error")
	ErrServer     = errors.New("aily: server error")
	ErrTimeout    = errors.New("aily: timeout")
	ErrCapability = errors.New("aily: capability not supported")
	ErrClient     = errors.New("aily: client error")
)

// APIError carries the Feishu business code / HTTP status.
type APIError struct {
	Kind       error
	Msg        string
	Code       int64
	HTTPStatus int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("aily: %s (code=%d http=%d)", e.Msg, e.Code, e.HTTPStatus)
}
func (e *APIError) Unwrap() error { return e.Kind }

// Retryable reports whether the call may be retried.
func (e *APIError) Retryable() bool {
	return errors.Is(e.Kind, ErrRateLimit) || errors.Is(e.Kind, ErrServer)
}

// Client is the Aily OpenAPI client. One instance per process — it shares
// a tuned *http.Transport (KeepAlive, idle pool, TLS reuse).
type Client struct {
	baseURL string
	http    *http.Client
}

func NewClient(baseURL string, hc *http.Client) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: hc}
}

type apiEnvelope struct {
	Code int64           `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func (c *Client) classify(resp *http.Response, body []byte) *APIError {
	var env apiEnvelope
	_ = json.Unmarshal(body, &env)
	msg := env.Msg
	if msg == "" {
		msg = resp.Status
	}
	var kind error
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		kind = ErrRateLimit
	case resp.StatusCode >= 500:
		kind = ErrServer
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		kind = ErrAuth
	case resp.StatusCode >= 400:
		kind = ErrClient
	case env.Code != 0:
		if env.Code == 50001 {
			kind = ErrServer
		} else if env.Code == 401 || env.Code == 403 {
			kind = ErrAuth
		} else {
			kind = ErrClient
		}
	default:
		return nil
	}
	return &APIError{Kind: kind, Msg: msg, Code: env.Code, HTTPStatus: resp.StatusCode}
}

func (c *Client) do(ctx context.Context, method, path, token string, body any) (json.RawMessage, error) {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, &APIError{Kind: ErrTimeout, Msg: err.Error()}
		}
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if apiErr := c.classify(resp, raw); apiErr != nil {
		return nil, apiErr
	}
	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("aily: decode %s: %w", path, err)
	}
	return env.Data, nil
}

// ─────────────────────────────────────────────────────────── chat ──

// StartChat issues one chat turn. With stream=true the response IS the SSE
// stream (handled by StreamChat), so this method is for the async path.
func (c *Client) StartChat(ctx context.Context, agentID, token string, contentItems []map[string]any, attachmentIDs []string, sessionID string) (chatID, newSession string, err error) {
	body := map[string]any{
		"user_message": buildUserMessage(contentItems, attachmentIDs),
	}
	if sessionID != "" {
		body["session_id"] = sessionID
	}
	data, err := c.do(ctx, http.MethodPost, "/aily/v1/agents/"+agentID+"/chats", token, body)
	if err != nil {
		return "", "", err
	}
	var out struct {
		AgentChatID string `json:"agent_chat_id"`
		SessionID   string `json:"session_id"`
	}
	_ = json.Unmarshal(data, &out)
	return out.AgentChatID, out.SessionID, nil
}

func buildUserMessage(contentItems []map[string]any, attachmentIDs []string) map[string]any {
	msg := map[string]any{"content": contentItems}
	if len(attachmentIDs) > 0 {
		msg["agent_attachment_ids"] = attachmentIDs
	}
	return msg
}

// StreamChat starts a streaming chat and yields raw SSE `data:` JSON
// payloads with the current event name. The transport caps at ~5 minutes
// per the docs: timeouts are reconciliation triggers, not run failures.
func (c *Client) StreamChat(ctx context.Context, agentID, token string, contentItems []map[string]any, attachmentIDs []string, sessionID string, fn func(eventName string, data []byte) error) error {
	body := map[string]any{
		"user_message": buildUserMessage(contentItems, attachmentIDs),
		"stream":       true,
	}
	if sessionID != "" {
		body["session_id"] = sessionID
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/aily/v1/agents/"+agentID+"/chats", bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return &APIError{Kind: ErrTimeout, Msg: err.Error()}
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if apiErr := c.classify(resp, raw); apiErr != nil {
			return apiErr
		}
		return &APIError{Kind: ErrServer, Msg: "stream HTTP " + resp.Status, HTTPStatus: resp.StatusCode}
	}

	currentEvent := ""
	br := newSSEReader(resp.Body)
	for {
		line, ok, err := br.Next(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return nil // EOF: stream complete (reconciliation follows)
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "event:") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(trimmed, "event:"))
			continue
		}
		if strings.HasPrefix(trimmed, ":") {
			continue // comment/keepalive
		}
		if !strings.HasPrefix(trimmed, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
		if payload == "" {
			continue
		}
		if err := fn(currentEvent, []byte(payload)); err != nil {
			return err
		}
	}
}

// GetChatResult is the final reconciliation source (获取对话结果).
func (c *Client) GetChatResult(ctx context.Context, agentID, token, chatID string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, "/aily/v1/agents/"+agentID+"/chats/"+chatID, token, nil)
}

// ─────────────────────────────────────────────────────── attachments ──

// UploadAttachment uploads an input attachment; returns agent_attachment_id.
func (c *Client) UploadAttachment(ctx context.Context, agentID, token string, data []byte, filename, attachmentType, docURL string) (string, error) {
	var body bytes.Buffer
	boundary := "studioBoundary" + randomHex(12)
	w := newMultipartWriter(&body, boundary)
	if attachmentType == "image" || attachmentType == "file" {
		if data == nil {
			return "", &APIError{Kind: ErrCapability, Msg: "file bytes required"}
		}
		_ = w.addField("type", attachmentType)
		_ = w.addFile("file", filename, data)
	} else if attachmentType == "feishu_doc" || attachmentType == "bitable" {
		if docURL == "" {
			return "", &APIError{Kind: ErrCapability, Msg: "doc_url required"}
		}
		_ = w.addField("type", attachmentType)
		_ = w.addField("doc_url", docURL)
	} else {
		return "", &APIError{Kind: ErrCapability, Msg: "unsupported attachment type " + attachmentType}
	}
	_ = w.close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/aily/v1/agents/"+agentID+"/attachments", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if apiErr := c.classify(resp, raw); apiErr != nil {
		return "", apiErr
	}
	var env apiEnvelope
	_ = json.Unmarshal(raw, &env)
	var out struct {
		AgentAttachmentID string `json:"agent_attachment_id"`
	}
	_ = json.Unmarshal(env.Data, &out)
	return out.AgentAttachmentID, nil
}

// ──────────────────────────────────────────────────────── artifacts ──

// ArtifactDownload is the resolver result (URL valid 24h only).
type ArtifactDownload struct {
	ArtifactID string
	Name       string
	URL        string
}

// GetArtifact resolves the current 24h signed URL for an artifact.
func (c *Client) GetArtifact(ctx context.Context, agentID, token, artifactID string) (*ArtifactDownload, error) {
	data, err := c.do(ctx, http.MethodGet, "/aily/v1/agents/"+agentID+"/artifacts/"+artifactID, token, nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		AgentArtifact struct {
			ArtifactID string `json:"artifact_id"`
			Name       string `json:"name"`
			URL        string `json:"url"`
		} `json:"agent_artifact"`
	}
	_ = json.Unmarshal(data, &out)
	return &ArtifactDownload{
		ArtifactID: out.AgentArtifact.ArtifactID,
		Name:       out.AgentArtifact.Name,
		URL:        out.AgentArtifact.URL,
	}, nil
}

// ─────────────────────────────────────────────────────── visibility ──

// CheckVisibility validates the current user's channel visibility (UAT
// only; channel_type currently only web_sdk per the docs).
func (c *Client) CheckVisibility(ctx context.Context, agentID, uat string) (bool, error) {
	data, err := c.do(ctx, http.MethodPost, "/aily/v1/agents/"+agentID+"/agent_visibility/check", uat,
		map[string]any{"channel_type": "web_sdk"})
	if err != nil {
		return false, err
	}
	var out struct {
		Visibility bool `json:"visibility"`
	}
	_ = json.Unmarshal(data, &out)
	return out.Visibility, nil
}

func randomHex(n int) string {
	b := make([]byte, (n+1)/2)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:n]
}
