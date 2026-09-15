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

// TestDeltaCoalescerSharesOneByteCoordinateSystem pins 第九轮补丁 3.2-A:
// the transient delta's end offset and the durable chunk's end offset must
// come from the SAME absolute UTF-8 byte counter. The SSE gateway drains
// buffered live deltas AFTER replaying the coalesced chunk, so a client can
// only drop a late delta by comparing its offset against the chunk's — which
// only works if both sides measure the answer in UTF-8 bytes (A=1, 中=3,
// 🚀=4), never in runes or UTF-16 code units.
func TestDeltaCoalescerSharesOneByteCoordinateSystem(t *testing.T) {
	c := newDeltaCoalescer()

	cases := []struct {
		text      string
		endOffset int
	}{
		{"A", 1},
		{"中", 4}, // 3 UTF-8 bytes: 1 + 3
		{"🚀", 8}, // 4 UTF-8 bytes: 4 + 4
	}
	for _, tc := range cases {
		end, _ := c.add(tc.text)
		if end != tc.endOffset {
			t.Fatalf("add(%q) end offset = %d, want %d", tc.text, end, tc.endOffset)
		}
	}

	payload, ok := c.chunk()
	if !ok {
		t.Fatal("chunk() = not ok, want a durable chunk for the buffered answer")
	}
	if off, _ := payload["offset"].(int); off != 8 {
		t.Fatalf("durable chunk offset = %v, want 8 — transient deltas and durable chunks "+
			"would disagree about where the answer ends", payload["offset"])
	}
	if text, _ := payload["text"].(string); text != "A中🚀" {
		t.Fatalf("durable chunk text = %q, want A中🚀", text)
	}

	// The counter keeps going after a flush: the next delta continues the
	// same coordinate system, and a later chunk covers it end to end.
	end, _ := c.add("!")
	if end != 9 {
		t.Fatalf("add after flush end offset = %d, want 9", end)
	}
	payload, _ = c.chunk()
	if off, _ := payload["offset"].(int); off != 9 {
		t.Fatalf("second chunk offset = %v, want 9", payload["offset"])
	}
}
