package aily

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExtractTextNestedObject(t *testing.T) {
	m := Mapper{}
	// Real-environment shape: delta text arrives as a nested content object.
	data := map[string]any{"text": map[string]any{"text": "收到", "type": "content"}}
	if got := m.ExtractText(data); got != "收到" {
		t.Fatalf("nested text = %q, want 收到", got)
	}
	// Plain string still works.
	if got := m.ExtractText(map[string]any{"text": "hello"}); got != "hello" {
		t.Fatalf("plain text = %q, want hello", got)
	}
	// Missing → empty, never a crash.
	if got := m.ExtractText(map[string]any{}); got != "" {
		t.Fatalf("empty = %q", got)
	}
}

func TestExtractArtifactsCrossItemPairing(t *testing.T) {
	m := Mapper{}
	// Real-environment shape: markdown text item and artifact entry are two
	// separate content items; the filename pairs them.
	result := map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": "这是图 ![a](artifacts/报告/画图.png) 与 ![b](artifacts/dog2.png)"},
			map[string]any{"type": "text", "text": "额外文字"},
			map[string]any{"type": "image", "agent_artifact_id": "art-1", "artifact_type": "sandbox_file"},
			map[string]any{"type": "image", "agent_artifact_id": "art-2", "artifact_type": "sandbox_file"},
		},
	}
	arts := m.ExtractArtifacts(result)
	if len(arts) != 2 {
		t.Fatalf("got %d artifacts, want 2", len(arts))
	}
	if arts[0].Name != "画图.png" {
		t.Fatalf("artifact[0].name = %q, want 画图.png (multi-level dir)", arts[0].Name)
	}
	if arts[1].Name != "dog2.png" {
		t.Fatalf("artifact[1].name = %q, want dog2.png", arts[1].Name)
	}
}

func TestExtractArtifactsNamedEntryWins(t *testing.T) {
	m := Mapper{}
	result := map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": "![x](artifacts/from-md.png)"},
			map[string]any{"type": "file", "agent_artifact_id": "art-9", "artifact_type": "sandbox_file", "name": "official.png"},
		},
	}
	arts := m.ExtractArtifacts(result)
	if len(arts) != 1 || arts[0].Name != "official.png" {
		t.Fatalf("named entry should keep its own name, got %+v", arts)
	}
}

func TestMapProviderStatus(t *testing.T) {
	m := Mapper{}
	cases := map[string]string{
		"Completed": "succeeded",
		"Running":   "running",
		"Failed":    "failed",
		"Cancelled": "cancelled",
		"Weird":     "",
		"":          "",
	}
	for in, want := range cases {
		if got := m.MapProviderStatus(in); got != want {
			t.Fatalf("MapProviderStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToUnifiedUnknownEventKeepsRunAlive(t *testing.T) {
	m := Mapper{}
	// Undocumented event shape must not crash or fail the run.
	events := m.ToUnified("aily_custom_future_event", map[string]any{"foo": "bar"})
	if len(events) != 0 {
		t.Fatalf("unknown textless event should map to nothing, got %+v", events)
	}
	events = m.ToUnified("aily_custom_future_event", map[string]any{"text": "hi"})
	if len(events) != 1 || events[0].Type != "content.delta" {
		t.Fatalf("unknown text event should pass through as delta, got %+v", events)
	}
}

func TestToUnifiedArtifactDiscovery(t *testing.T) {
	m := Mapper{}
	events := m.ToUnified("message", map[string]any{
		"agent_artifact_id": "art-42",
		"artifact_type":     "sandbox_file",
	})
	found := false
	for _, ev := range events {
		if ev.Type == "artifact.discovered" && ev.Payload["external_artifact_id"] == "art-42" {
			found = true
		}
	}
	if !found {
		t.Fatalf("artifact discovery missing from %+v", events)
	}
}

func TestParseSSEDataTolerant(t *testing.T) {
	m := Mapper{}
	// Malformed JSON keeps the raw payload — never kills the run.
	data := m.ParseSSEData([]byte(`{"text": "unclosed`))
	if _, ok := data["raw"]; !ok {
		t.Fatalf("malformed payload should be preserved raw, got %v", data)
	}
	good := m.ParseSSEData([]byte(`{"text": "ok"}`))
	if good["text"] != "ok" {
		t.Fatalf("good payload parsed wrong: %v", good)
	}
}

func TestNormalizeArtifactType(t *testing.T) {
	m := Mapper{}
	if got := m.NormalizeArtifactType("sandbox_file"); got != "file" {
		t.Fatalf("sandbox_file → %q", got)
	}
	if got := m.NormalizeArtifactType("image"); got != "image" {
		t.Fatalf("image → %q", got)
	}
	if got := m.NormalizeArtifactType(""); got != "file" {
		t.Fatalf("empty → %q", got)
	}
}

// SSE line-level behavior: event names set state; data lines map.
func TestStreamEventNameSwitching(t *testing.T) {
	m := Mapper{}
	payloads := []string{
		`{"agent_chat_id": "777", "session_id": "conversation_x", "event": "start"}`,
		`{"text": {"text": "收到", "type": "content"}}`,
	}
	var (
		chatID  string
		session string
		texts   []string
	)
	for _, raw := range payloads {
		parsed := m.ParseSSEData([]byte(raw))
		if v, ok := parsed["agent_chat_id"].(string); ok && v != "" {
			chatID = v
		}
		if v, ok := parsed["session_id"].(string); ok && v != "" {
			session = v
		}
		if txt := m.ExtractText(parsed); txt != "" {
			texts = append(texts, txt)
		}
	}
	if chatID != "777" || session != "conversation_x" {
		t.Fatalf("identity not surfaced: chat=%q session=%q", chatID, session)
	}
	if strings.Join(texts, "") != "收到" {
		t.Fatalf("text not extracted: %v", texts)
	}
	var _ = json.Marshal
}
