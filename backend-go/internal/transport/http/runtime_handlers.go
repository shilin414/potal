package http

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/creation-agent-studio/backend-go/internal/catalog"
	"github.com/creation-agent-studio/backend-go/internal/identity"
)

// ListRuntimes implements GET /api/v2/runtimes: active providers ×
// registered adapters, rendered for the agent-marketplace form.
func (s *Server) ListRuntimes(w http.ResponseWriter, r *http.Request) {
	providers, err := s.CatalogRepo.ListActiveProviders(r.Context())
	if err != nil {
		writeSimpleError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.Registry.Describe(providers))
}

// ValidateRuntime implements POST /api/v2/runtimes/validate (best-effort
// remote visibility check with the reference's exact detail messages).
func (s *Server) ValidateRuntime(w http.ResponseWriter, r *http.Request) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	var body struct {
		ProviderKey        string `json:"provider_key"`
		RuntimeType        string `json:"runtime_type"`
		ExternalResourceID string `json:"external_resource_id"`
		IdentityMode       string `json:"identity_mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeFieldErrors(w, map[string][]string{"provider_key": {"invalid json"}})
		return
	}
	if body.RuntimeType == "" {
		body.RuntimeType = catalog.RuntimeTypeAgent
	}
	if body.IdentityMode == "" {
		body.IdentityMode = "user"
	}
	provider, err := s.CatalogRepo.ProviderByKey(r.Context(), body.ProviderKey)
	if err != nil {
		writeFieldErrors(w, map[string][]string{"provider_key": {"未知的运行时提供方：" + body.ProviderKey}})
		return
	}
	if provider.Status != "active" {
		writeFieldErrors(w, map[string][]string{"provider_key": {"运行时提供方已停用：" + body.ProviderKey}})
		return
	}
	supported := false
	for _, t := range provider.SupportedRuntimeTypes {
		if t == body.RuntimeType {
			supported = true
			break
		}
	}
	if !supported {
		writeFieldErrors(w, map[string][]string{"runtime_type": {provider.Name + " 不支持 " + body.RuntimeType + " 运行时"}})
		return
	}
	adapter, err := s.Registry.Resolve(provider.Key, body.RuntimeType)
	if err != nil {
		writeFieldErrors(w, map[string][]string{"runtime_type": {"运行时适配器未注册：" + provider.Key + ":" + body.RuntimeType}})
		return
	}
	if err := catalog.ValidateResourceID(adapter, body.ExternalResourceID); err != nil {
		writeFieldErrors(w, map[string][]string{"external_resource_id": {err.Error()}})
		return
	}

	// Remote visibility check — skip gracefully whenever identity cannot
	// be resolved (mirrors the validated degradations).
	if !adapter.Capabilities()["visibility"] {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "checked": false,
			"detail": "该运行时未声明可见性校验能力，已跳过远程校验。"})
		return
	}
	if caller.Identity == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "checked": false,
			"detail": "当前账号未绑定飞书身份，无法做可见性校验；请用飞书登录后再试，或直接保存后首次对话时验证。"})
		return
	}
	authCtx, err := adapter.BuildAuth(r.Context(), caller.ID, body.IdentityMode)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "checked": true,
			"detail": "无法获取访问凭据：" + err.Error()})
		return
	}
	visible, err := adapter.CheckVisibility(r.Context(), authCtx, body.ExternalResourceID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "checked": true,
			"detail": friendlyVisibilityError(err)})
		return
	}
	if visible {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "checked": true,
			"detail": "校验通过：当前身份对该智能体可见。"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": false, "checked": true,
		"detail": "当前身份对该智能体不可见，请在飞书侧开启 OpenAPI 渠道并把使用者加入可访问范围。"})
}

func friendlyVisibilityError(err error) string {
	var apiErr interface{ Error() string }
	if errors.As(err, &apiErr) {
		msg := err.Error()
		switch {
		case containsAll(msg, "10006"):
			return "该智能体未开启 OpenAPI 渠道，请在飞书智能体后台开启后再试。"
		case containsAll(msg, "10007"), containsAll(msg, "10011"):
			return "当前身份无权访问该智能体，请确认使用者已被加入可访问范围。"
		case containsAll(msg, "user identity"):
			return "该智能体需要用户身份校验，请使用飞书登录后再试。"
		}
		return msg
	}
	return err.Error()
}

func containsAll(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

var _ = identity.ErrNotFound
