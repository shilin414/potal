// Package delivery sends finished scheduled-run results to Feishu.
// Business code depends only on Sender; the Feishu adapter is one
// implementation (Email/Webhook come later per the architecture doc).
package delivery

import (
	"context"
	"fmt"
	"strings"
)

// Channel / identity / content constants (first stage scope).
const (
	ChannelFeishu   = "feishu"
	SenderOwnerUser = "owner_user"
	ContentSummary  = "summary"

	TargetUser = "user"
	TargetChat = "chat"

	// ProviderKey routes delivery outbox events through the shared relay
	// to the queue:feishu_delivery stream.
	ProviderKey = "feishu_delivery"
)

// summaryMaxRunes caps the delivered answer text.
const summaryMaxRunes = 4000

// Target is a resolved send destination.
type Target struct {
	Type    string // user | chat
	ID      string
	Content string
}

// Sender delivers one message to one target.
type Sender interface {
	Send(ctx context.Context, senderUserID int64, t Target) error
}

// UATResolver returns the owner's user access token.
type UATResolver interface {
	UserAccessToken(ctx context.Context, userID int64) (string, error)
}

// feishuSender is the identity.FeishuClient surface the adapter needs.
type feishuSender interface {
	SendIMMessage(ctx context.Context, token, receiveIDType, receiveID, msgType, content string) error
}

// FeishuSender sends as the schedule owner via Feishu IM.
type FeishuSender struct {
	Client feishuSender
	Auth   UATResolver
}

// Send implements Sender. receive_id_type follows the target type.
func (s *FeishuSender) Send(ctx context.Context, senderUserID int64, t Target) error {
	token, err := s.Auth.UserAccessToken(ctx, senderUserID)
	if err != nil {
		return fmt.Errorf("delivery: owner uat: %w", err)
	}
	idType := "open_id"
	if t.Type == TargetChat {
		idType = "chat_id"
	}
	content, _ := marshalJSON(map[string]string{"text": t.Content})
	return s.Client.SendIMMessage(ctx, token, idType, t.ID, "text", string(content))
}

// BuildDeliveryText renders the delivered message body: a schedule header
// plus the (truncated) answer. Runs never ship raw artifacts in v1.
func BuildDeliveryText(scheduleName, answer string) string {
	var b strings.Builder
	b.WriteString("【定时任务】")
	b.WriteString(scheduleName)
	b.WriteString("\n\n")
	if runes := []rune(answer); len(runes) > summaryMaxRunes {
		answer = string(runes[:summaryMaxRunes]) + "…（内容已截断）"
	}
	b.WriteString(answer)
	return b.String()
}
