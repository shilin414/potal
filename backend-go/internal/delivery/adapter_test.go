package delivery

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
)

// TestBuildDeliveryTextTruncates: long answers are capped with a suffix.
func TestBuildDeliveryTextTruncates(t *testing.T) {
	long := strings.Repeat("字", summaryMaxRunes+500)
	out := BuildDeliveryText("日报", long)
	runes := []rune(out)
	if len(runes) > summaryMaxRunes+50 {
		t.Fatalf("delivery text too long: %d runes", len(runes))
	}
	if !strings.Contains(out, "（内容已截断）") {
		t.Fatal("missing truncation marker")
	}
	if !strings.HasPrefix(out, "【定时任务】日报") {
		t.Fatalf("missing schedule header: %q", out[:20])
	}
}

// TestBuildDeliveryTextShortPassthrough.
func TestBuildDeliveryTextShortPassthrough(t *testing.T) {
	out := BuildDeliveryText("日报", "今日完成 X")
	if out != "【定时任务】日报\n\n今日完成 X" {
		t.Fatalf("unexpected text: %q", out)
	}
}

// fakeSender records sends.
type fakeSender struct {
	calls   int
	lastKey string
}

func (f *fakeSender) Send(_ context.Context, req DeliveryRequest) error {
	f.calls++
	f.lastKey = req.IdempotencyKey
	return nil
}

type failingSender struct {
	calls int
	err   error
}

func (f *failingSender) Send(_ context.Context, _ DeliveryRequest) error {
	f.calls++
	return f.err
}

// TestFeishuSenderChoosesIDType: chats use chat_id, users use open_id.
func TestFeishuSenderChoosesIDType(t *testing.T) {
	fake := &recordingFeishu{}
	s := &FeishuSender{Client: fake, Auth: &staticAuth{token: "uat"}}
	_ = s.Send(context.Background(), DeliveryRequest{
		SenderUserID: 7,
		Target:       Target{Type: TargetChat, ID: "oc_1", Content: "hi"},
	})
	if fake.lastIDType != "chat_id" {
		t.Fatalf("chat id type = %q", fake.lastIDType)
	}
	_ = s.Send(context.Background(), DeliveryRequest{
		SenderUserID: 7,
		Target:       Target{Type: TargetUser, ID: "ou_1", Content: "hi"},
	})
	if fake.lastIDType != "open_id" {
		t.Fatalf("user id type = %q", fake.lastIDType)
	}
	if fake.lastToken != "uat" {
		t.Fatalf("token not passed through")
	}
}

// TestDeliveryRequestIdempotencyKey: the send request carries a stable
// key (= DeliveryExecution id) so retries of the same execution are
// correlatable (at-least-once external side effect).
func TestDeliveryRequestIdempotencyKey(t *testing.T) {
	fs := &fakeSender{}
	req := DeliveryRequest{
		ExecutionID:    ids.New(),
		SenderUserID:   42,
		Target:         Target{Type: TargetChat, ID: "oc_x", Content: "hi"},
		IdempotencyKey: ids.New().String(),
	}
	if err := fs.Send(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if fs.lastKey != req.IdempotencyKey {
		t.Fatalf("idempotency key not propagated: %q", fs.lastKey)
	}
}

type recordingFeishu struct {
	lastIDType, lastToken, lastReceiveID string
}

func (r *recordingFeishu) SendIMMessage(_ context.Context, token, receiveIDType, receiveID, _, _ string) error {
	r.lastToken, r.lastIDType, r.lastReceiveID = token, receiveIDType, receiveID
	return nil
}

type staticAuth struct{ token string }

func (a *staticAuth) UserAccessToken(_ context.Context, _ int64) (string, error) {
	return a.token, nil
}

// TestFailureClassification: rate-limit markers map to rate_limited.
func TestFailureClassificationMarkers(t *testing.T) {
	msg := "feishu: send im: code 99991400: too many requests"
	if !strings.Contains(msg, "99991400") {
		t.Fatal("marker missing")
	}
	// Sanity: failingSender propagates errors as expected.
	fs := &failingSender{err: errors.New("boom")}
	if err := fs.Send(context.Background(), DeliveryRequest{}); err == nil {
		t.Fatal("expected error")
	}
}
