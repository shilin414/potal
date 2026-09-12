package aily

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Mapper translates raw Aily payloads to the unified event protocol.
//
// Tolerance rules learned from the real environment (must keep):
//  1. SSE text may be a nested object {'text': {'text': ..., 'type': ...}}
//     instead of a plain string.
//  2. A generated file arrives as TWO content items: a text item with
//     markdown `![alt](artifacts/<name>)` and a separate artifact item
//     carrying agent_artifact_id with empty text. The filename pairs them.
//  3. Unknown SSE event shapes must never crash a run — keep the raw
//     payload (json.RawMessage) and parse defensively.
type Mapper struct{}

// Status buckets observed across the Aily docs and the real environment.
var (
	statusRunning   = []string{"running", "in_progress", "generating", "opening"}
	statusSucceeded = []string{"completed", "succeeded", "done", "stop"}
	statusFailed    = []string{"failed", "error"}
	statusCancelled = []string{"cancelled", "canceled"}
)

// MapProviderStatus maps an Aily status to a unified run status; "" keeps.
func (Mapper) MapProviderStatus(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	for _, v := range statusRunning {
		if s == v {
			return "running"
		}
	}
	for _, v := range statusSucceeded {
		if s == v {
			return "succeeded"
		}
	}
	for _, v := range statusFailed {
		if s == v {
			return "failed"
		}
	}
	for _, v := range statusCancelled {
		if s == v {
			return "cancelled"
		}
	}
	return ""
}

// ExtractFinalText joins the text items of a chat result.
func (m Mapper) ExtractFinalText(chatResult map[string]any) string {
	out := ""
	items, _ := chatResult["content"].([]any)
	for _, item := range items {
		cm, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := cm["type"].(string); t == "text" {
			if txt, ok := cm["text"].(string); ok {
				out += txt
			}
		}
	}
	return out
}

var markdownArtifactRe = regexp.MustCompile(`\((?:\./)?artifacts?/(?:[^)/]+/)*([^)/]+\.\w+)\)`)

// DiscoveredArtifact pairs an external artifact id with a display name.
type DiscoveredArtifact struct {
	ExternalID   string
	ProviderType string
	Name         string
}

// ExtractArtifacts implements the cross-item pairing rule (rule 2 above).
func (m Mapper) ExtractArtifacts(chatResult map[string]any) []DiscoveredArtifact {
	items, _ := chatResult["content"].([]any)

	// First collect markdown filenames from text items in content order.
	// (All matches per item: one text blob may embed several files.)
	var pending []string
	for _, item := range items {
		cm, ok := item.(map[string]any)
		if !ok || cm["agent_artifact_id"] != nil {
			continue
		}
		if txt, _ := cm["text"].(string); txt != "" {
			for _, match := range markdownArtifactRe.FindAllStringSubmatch(txt, -1) {
				if len(match) > 1 {
					pending = append(pending, match[1])
				}
			}
		}
	}

	out := make([]DiscoveredArtifact, 0)
	for _, item := range items {
		cm, ok := item.(map[string]any)
		if !ok {
			continue
		}
		externalID, _ := cm["agent_artifact_id"].(string)
		if externalID == "" {
			continue
		}
		name, _ := cm["name"].(string)
		if name == "" {
			for len(pending) > 0 {
				candidate := pending[0]
				pending = pending[1:]
				if candidate != "" {
					name = candidate
					break
				}
			}
		}
		ptype, _ := cm["artifact_type"].(string)
		out = append(out, DiscoveredArtifact{ExternalID: externalID, ProviderType: ptype, Name: name})
	}
	return out
}

// NormalizeArtifactType maps Aily artifact_type → platform normalized_type.
func (Mapper) NormalizeArtifactType(providerType string) string {
	t := strings.ToLower(providerType)
	switch {
	case t == "":
		return "file"
	case strings.Contains(t, "image"), strings.Contains(t, "picture"):
		return "image"
	case strings.Contains(t, "sandbox_file"), strings.Contains(t, "file"):
		return "file"
	case strings.Contains(t, "feishu"), strings.Contains(t, "doc"), strings.Contains(t, "wiki"):
		return "feishu_doc"
	case strings.Contains(t, "bitable"), strings.Contains(t, "sheet"), strings.Contains(t, "base"):
		return "bitable"
	case strings.Contains(t, "video"):
		return "video"
	default:
		return "file"
	}
}

// ExtractText pulls the display text out of an SSE data field, tolerating
// both plain strings and the nested {'text': {'text': ...}} object.
func (m Mapper) ExtractText(data map[string]any) string {
	value, ok := firstOf(data, "text", "content", "delta")
	if !ok {
		return ""
	}
	switch v := value.(type) {
	case string:
		return v
	case map[string]any:
		inner, ok := firstOf(v, "text", "content")
		if !ok {
			return ""
		}
		s, _ := inner.(string)
		return s
	default:
		return ""
	}
}

func firstOf(m map[string]any, keys ...string) (any, bool) {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			return v, true
		}
	}
	return nil, false
}

// ParseSSEData defensively decodes a raw data payload; malformed JSON
// keeps its raw form under "raw" instead of killing the run.
func (m Mapper) ParseSSEData(raw []byte) map[string]any {
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return map[string]any{"raw": string(raw)}
	}
	return out
}

// ToUnified converts one parsed SSE event into zero or more unified
// protocol events. The final authority is always GetChatResult.
func (m Mapper) ToUnified(eventName string, data map[string]any) []UnifiedEvent {
	name := strings.ToLower(eventName)
	var out []UnifiedEvent

	switch {
	case containsAny(name, "delta", "message", "text"):
		if text := m.ExtractText(data); text != "" {
			out = append(out, UnifiedEvent{Type: "content.delta", Payload: map[string]any{"text": text}})
		}
	case containsAny(name, "start", "begin"):
		out = append(out, UnifiedEvent{Type: "content.started", Payload: map[string]any{}})
	case containsAny(name, "done", "complete", "finish", "end"):
		if text := m.ExtractText(data); text != "" {
			out = append(out, UnifiedEvent{Type: "content.delta", Payload: map[string]any{"text": text}})
		}
		fr, _ := data["finish_reason"].(string)
		out = append(out, UnifiedEvent{Type: "content.completed", Payload: map[string]any{"finish_reason": fr}})
	case containsAny(name, "error", "fail"):
		code, _ := data["code"].(string)
		if code == "" {
			if f, ok := data["code"].(float64); ok {
				code = json.Number(fmtF(f)).String()
			}
		}
		msg, _ := firstOf(data, "msg", "message")
		out = append(out, UnifiedEvent{Type: "run.failed", Payload: map[string]any{
			"error_code":    orDefault(code, "aily_stream_error"),
			"error_message": orDefault(str(msg), ""),
		}})
	default:
		// Unknown event: pass text through as a delta; never fail the run.
		if text := m.ExtractText(data); text != "" {
			out = append(out, UnifiedEvent{Type: "content.delta", Payload: map[string]any{"text": text}})
		}
	}

	// Artifact discovery rides on any event shape.
	if id, _ := data["agent_artifact_id"].(string); id != "" {
		ptype, _ := data["artifact_type"].(string)
		out = append(out, UnifiedEvent{Type: "artifact.discovered", Payload: map[string]any{
			"external_artifact_id":   id,
			"provider_artifact_type": ptype,
		}})
	}
	return out
}

// UnifiedEvent is one mapped protocol event.
type UnifiedEvent struct {
	Type    string
	Payload map[string]any
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func fmtF(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}
