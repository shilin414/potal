package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	genapi "github.com/creation-agent-studio/backend-go/internal/gen/api"
)

// ─────────────────────────────────────────────── feishu forwarding ──
//
// Delivers a share snapshot card to Feishu users/groups. The share token is
// validated against the caller's own shares, the link origin comes from the
// request Origin header, and each target send tries the caller's user token
// before falling back to the app token (the OAuth grant may not carry the
// IM scope; the bot usually does).

const feishuForwardMaxTargets = 20

// ListFeishuForwardTargets implements GET /api/v2/feishu/forward/targets.
func (s *Server) ListFeishuForwardTargets(w http.ResponseWriter, r *http.Request, params genapi.ListFeishuForwardTargetsParams) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	if s.FeishuAuth == nil {
		writeSimpleError(w, http.StatusInternalServerError, "feishu auth resolver unavailable")
		return
	}
	uat, err := s.FeishuAuth.UserAccessToken(r.Context(), caller.ID)
	if err != nil {
		writeDetail(w, http.StatusBadRequest, "请先绑定飞书账号后使用转发")
		return
	}
	query := ""
	if params.Query != nil {
		query = strings.TrimSpace(*params.Query)
	}
	targets := make([]map[string]any, 0)
	switch params.Type {
	case genapi.ListFeishuForwardTargetsParamsTypeChat:
		chats, err := s.Feishu.ListUserChats(r.Context(), uat)
		if err != nil {
			writeFeishuError(w, err, "获取群聊列表失败")
			return
		}
		for _, c := range chats {
			if query != "" && !strings.Contains(c.Name, query) {
				continue
			}
			targets = append(targets, map[string]any{
				"id": c.ChatID, "name": c.Name, "avatar_url": c.AvatarURL, "target_type": "chat",
			})
		}
	case genapi.ListFeishuForwardTargetsParamsTypeUser:
		users, err := s.Feishu.SearchFeishuUsers(r.Context(), uat, query)
		if err != nil {
			writeFeishuError(w, err, "搜索联系人失败")
			return
		}
		for _, u := range users {
			targets = append(targets, map[string]any{
				"id": u.OpenID, "name": u.Name, "avatar_url": u.AvatarURL, "target_type": "user",
			})
		}
	default:
		writeDetail(w, http.StatusBadRequest, "type 必须是 user 或 chat")
		return
	}
	writeJSON(w, http.StatusOK, targets)
}

// ForwardShareToFeishu implements POST /api/v2/feishu/forward.
func (s *Server) ForwardShareToFeishu(w http.ResponseWriter, r *http.Request) {
	caller := userFrom(r.Context())
	if caller == nil {
		writeDetail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	if s.FeishuAuth == nil {
		writeSimpleError(w, http.StatusInternalServerError, "feishu auth resolver unavailable")
		return
	}
	var body genapi.ForwardShareToFeishuJSONBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeDetail(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.ShareToken == "" || len(body.Targets) == 0 || len(body.Targets) > feishuForwardMaxTargets {
		writeDetail(w, http.StatusBadRequest, "share_token 与 targets（1-20 个）必填")
		return
	}
	// Only the share owner may forward it.
	share, err := s.Runs.Querier().GetConversationShareByToken(r.Context(), body.ShareToken)
	if err != nil || share.UserID != uint64(caller.ID) || share.RevokedAt.Valid {
		writeDetail(w, http.StatusBadRequest, "分享不存在或已撤销")
		return
	}
	conv, err := s.Runs.Querier().GetConversationByID(r.Context(), share.ConversationID)
	if err != nil {
		writeDetail(w, http.StatusBadRequest, "分享不存在或已撤销")
		return
	}
	var entries []shareMessageEntry
	_ = json.Unmarshal(share.Snapshot, &entries)

	uat, uatErr := s.FeishuAuth.UserAccessToken(r.Context(), caller.ID)
	if uatErr != nil {
		writeDetail(w, http.StatusBadRequest, "请先绑定飞书账号后使用转发")
		return
	}

	origin := shareOrigin(r)
	shareURL := origin + "/share/" + body.ShareToken
	sender := caller.User.DisplayName
	if sender == "" {
		sender = caller.User.Username
	}
	card := feishuShareCard(sender, conv.Title, len(entries), shareURL)
	cardJSON, _ := json.Marshal(card)
	content := string(cardJSON)

	results := make([]map[string]any, 0, len(body.Targets))
	success := 0
	for _, t := range body.Targets {
		if t.Id == "" || (t.TargetType != "user" && t.TargetType != "chat") {
			results = append(results, map[string]any{"target_id": t.Id, "ok": false, "error": "无效的转发目标"})
			continue
		}
		receiveIDType := "open_id"
		if t.TargetType == "chat" {
			receiveIDType = "chat_id"
		}
		err := s.Feishu.SendIMMessage(r.Context(), uat, receiveIDType, t.Id, "interactive", content)
		ok := err == nil
		if ok {
			success++
		}
		item := map[string]any{"target_id": t.Id, "ok": ok}
		if !ok {
			item["error"] = feishuScopeHint(err, "发送失败")
		}
		results = append(results, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"results":       results,
		"success_count": success,
		"fail_count":    len(results) - success,
	})
}

// feishuShareCard builds the interactive card payload (before JSON-encoding
// into the message content string).
func feishuShareCard(sender, title string, messageCount int, shareURL string) map[string]any {
	headline := title
	if headline == "" {
		headline = "分享的对话"
	}
	md := fmt.Sprintf("**%s** 分享了一段对话（%d 条消息）", sender, messageCount)
	return map[string]any{
		"config": map[string]any{"wide_screen_mode": true},
		"header": map[string]any{
			"template": "orange",
			"title":    map[string]any{"tag": "plain_text", "content": "对话分享：" + headline},
		},
		"elements": []any{
			map[string]any{"tag": "div", "text": map[string]any{"tag": "lark_md", "content": md}},
			map[string]any{
				"tag": "action",
				"actions": []any{
					map[string]any{
						"tag":  "button",
						"text": map[string]any{"tag": "plain_text", "content": "查看对话"},
						"type": "primary",
						"url":  shareURL,
					},
				},
			},
			map[string]any{
				"tag": "note",
				"elements": []any{
					map[string]any{"tag": "plain_text", "content": "只读快照，仅包含分享时选中的消息 · Creation Agent Studio"},
				},
			},
		},
	}
}

// shareOrigin derives the SPA origin for the share link from the request's
// Origin header (CORS makes every cross-origin API call carry it), falling
// back to Referer, then the dev origin.
func shareOrigin(r *http.Request) string {
	if o := r.Header.Get("Origin"); o != "" {
		if u, err := url.Parse(o); err == nil && u.Scheme != "" && u.Host != "" {
			return u.Scheme + "://" + u.Host
		}
	}
	if ref := r.Header.Get("Referer"); ref != "" {
		if u, err := url.Parse(ref); err == nil && u.Scheme != "" && u.Host != "" {
			return u.Scheme + "://" + u.Host
		}
	}
	return "http://localhost:3030"
}

// feishuScopeHint rewrites the common permission failures into a hint the
// sender can act on, instead of a raw Feishu error code.
func feishuScopeHint(err error, fallback string) string {
	if err == nil {
		return fallback
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "99991679"):
		return "飞书权限不足，需要重新授权（缺少转发所需的 IM / 通讯录权限）"
	case strings.Contains(msg, "token refresh"):
		return "飞书授权已过期，请重新绑定飞书账号"
	default:
		return fallback + "：" + msg
	}
}

// writeFeishuError maps a Feishu API failure onto the response envelope.
// Scope errors come back as 403 so the frontend offers the re-authorization
// flow; everything else is a 502 upstream failure.
func writeFeishuError(w http.ResponseWriter, err error, fallback string) {
	if err != nil && strings.Contains(err.Error(), "99991679") {
		writeDetail(w, http.StatusForbidden, feishuScopeHint(err, fallback))
		return
	}
	writeSimpleError(w, http.StatusBadGateway, feishuScopeHint(err, fallback))
}
