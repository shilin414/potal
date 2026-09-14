// Package delivery sends finished scheduled-run results to Feishu.
// Business code depends only on Sender; the Feishu adapter is one
// implementation (Email/Webhook come later per the architecture doc).
package delivery

import (
	"context"
	"fmt"
	"strings"

	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
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
//
// Delivery semantics: AT LEAST ONCE. The database side is exactly-once
// per DeliveryExecution (CAS claim), but the external send is a side
// effect outside any transaction: a crash between "provider accepted the
// send" and "CAS → succeeded" makes the reclaimer retry, and the user may
// see the message twice. DeliveryRequest carries a stable
// IdempotencyKey (= DeliveryExecution.ID) so adapters can (a) pass it to
// providers that support native idempotency, or (b) log/correlate
// duplicates. Feishu IM in v1 has no idempotency parameter, so this
// adapter is explicitly at-least-once.
type Sender interface {
	Send(ctx context.Context, req DeliveryRequest) error
}

// DeliveryRequest is one delivery attempt.
type DeliveryRequest struct {
	ExecutionID ids.ID
	// SenderUserID is the user whose UAT sends the message.
	SenderUserID int64
	Target       Target
	// IdempotencyKey is stable across retries of the same
	// DeliveryExecution (= ExecutionID as a canonical string).
	IdempotencyKey string
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
//
// Feishu IM (im/v1/messages) accepts no idempotency parameter, so the
// key is recorded for observability only: duplicate deliveries caused by
// a crash between send and CAS are inherent to the at-least-once model
// and are observable through studio_delivery_send_total{idempotency}.
func (s *FeishuSender) Send(ctx context.Context, req DeliveryRequest) error {
	token, err := s.Auth.UserAccessToken(ctx, req.SenderUserID)
	if err != nil {
		return fmt.Errorf("delivery: owner uat: %w", err)
	}
	idType := "open_id"
	if req.Target.Type == TargetChat {
		idType = "chat_id"
	}
	content, _ := marshalJSON(map[string]string{"text": req.Target.Content})
	return s.Client.SendIMMessage(ctx, token, idType, req.Target.ID, "text", string(content))
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
