package aily

import (
	"testing"

	"github.com/creation-agent-studio/backend-go/internal/execution"
)

func TestChatIDForResultFallsBackToStreamingID(t *testing.T) {
	run := &execution.Run{}

	if got := chatIDForResult(run, "chat-from-stream"); got != "chat-from-stream" {
		t.Fatalf("chatIDForResult() = %q, want streaming chat id", got)
	}
}

func TestChatIDForResultPrefersPersistedID(t *testing.T) {
	run := &execution.Run{ExternalRunID: "chat-from-db"}

	if got := chatIDForResult(run, "chat-from-stream"); got != "chat-from-db" {
		t.Fatalf("chatIDForResult() = %q, want persisted chat id", got)
	}
}
