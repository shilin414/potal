// Package execution owns the unified Run model: creation, dispatch
// (Outbox → Redis Streams), CAS claim, leases, events and artifacts.
//
// MySQL is the source of truth; Redis is only dispatch speed and realtime
// fanout. Duplicate queue messages can never duplicate execution because
// workers must win the CAS claim before doing anything.
package execution

import (
	"time"

	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
)

// Run statuses (nine states, matching the validated API contract).
const (
	StatusQueued          = "queued"
	StatusRunning         = "running"
	StatusWaitingInput    = "waiting_input"
	StatusWaitingExternal = "waiting_external"
	StatusCancelling      = "cancelling"
	StatusCancelled       = "cancelled"
	StatusSucceeded       = "succeeded"
	StatusFailed          = "failed"
	StatusInterrupted     = "interrupted"
)

// TerminalStatuses: the only states a run can finish in. New code must
// never write anything outside this set.
var TerminalStatuses = map[string]bool{
	StatusCancelled: true,
	StatusSucceeded: true,
	StatusFailed:    true,
}

// LegacyTerminalStatuses are historical terminal aliases (第五轮 P2-1).
//
//	StatusInterrupted is a PRE-CLOSURE status: the old retry path wrote it
//	and emitted run.interrupted. New code never writes it (retry emits
//	run.retrying and keeps streaming; terminal failure emits run.failed),
//	and migration 0018 rewrites every historical row to `failed`.
//
//	It is still treated as terminal for READS because a legacy row must not
//	be able to occupy a conversation or a user's outstanding quota forever:
//	before the review the "active run" predicate was
//	`status NOT IN ('cancelled','succeeded','failed')`, so one interrupted
//	run parked a conversation at 409 permanently (and counted against
//	RUN_USER_MAX_OUTSTANDING).
var LegacyTerminalStatuses = map[string]bool{
	StatusInterrupted: true,
}

// IsTerminal reports whether a status ends a run.
func IsTerminal(status string) bool { return TerminalStatuses[status] }

// IsSettled reports whether a run is finished for ADMISSION / lifecycle
// purposes: the three canonical terminal statuses plus the legacy
// `interrupted` alias above.
//
// It is the Go mirror of the SQL "active run" predicate
// `status NOT IN ('cancelled','succeeded','failed','interrupted')` used by
// CountActiveRunsByConversation / CountOutstandingRunsByUser. Use it
// wherever a SETTLED run must be excluded (SSE termination, admission,
// reconcile skip); keep IsTerminal where only canonical statuses are
// valid input (finalization).
func IsSettled(status string) bool {
	return IsTerminal(status) || LegacyTerminalStatuses[status]
}

// Unified event protocol.
const (
	EventRunStarted         = "run.started"
	EventContentStarted     = "content.started"
	EventContentDelta       = "content.delta"
	EventContentChunk       = "content.chunk"
	EventContentCompleted   = "content.completed"
	EventContentSnapshot    = "content.snapshot"
	EventToolStarted        = "tool.started"
	EventToolCompleted      = "tool.completed"
	EventArtifactDiscovered = "artifact.discovered"
	EventArtifactCreated    = "artifact.created"
	EventInputRequired      = "input.required"
	EventRunCompleted       = "run.completed"
	EventRunFailed          = "run.failed"
	EventRunCancelled       = "run.cancelled"
	EventRunRetrying        = "run.retrying"
	EventRunPoll            = "run.poll"

	// EventRunInterrupted is the LEGACY terminal event emitted by the old
	// retry path. It is kept only so historical replayed events still
	// resolve; new code never emits it (retry uses run.retrying, terminal
	// failure uses run.failed — 修复计划 §14-16).
	EventRunInterrupted = "run.interrupted"
)

// Run is a unified execution across every provider.
type Run struct {
	ID                   ids.ID
	UserID               *int64
	ApplicationID        *int64
	ConversationID       *int64
	RuntimeBindingID     *int64
	Provider             string
	RuntimeType          string
	ExternalRunID        string
	Status               string
	ProviderStatus       string
	ProviderFinishReason string
	Input                map[string]any
	Output               map[string]any
	RuntimeSnapshot      map[string]any
	Attempt              int64
	MaxAttempts          int64
	QueuedAt             time.Time
	StartedAt            *time.Time
	FinishedAt           *time.Time
	ErrorCode            string
	ErrorMessage         string
	CreatedAt            time.Time
	UpdatedAt            time.Time
	TriggerType          string
	TriggerID            *int64
	Priority             string
	AvailableAt          *time.Time

	// NOTE(修复计划 §4/§13): lease fencing deliberately does NOT live on
	// Run. Ownership is a separate immutable ExecutionOwnership value
	// produced by ClaimRun — reloading a Run from the database must never
	// be able to change (or silently drop, as the lease token) the write
	// fence. See ownership.go.
}

// InputContent returns the user content items sent to the provider.
func (r *Run) InputContent() []map[string]any {
	raw, ok := r.Input["content"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// ContentText joins the text items of input.content.
func (r *Run) ContentText() string {
	texts := make([]string, 0)
	for _, item := range r.InputContent() {
		if t, ok := item["text"].(string); ok {
			texts = append(texts, t)
		}
	}
	out := ""
	for i, t := range texts {
		if i > 0 {
			out += "\n"
		}
		out += t
	}
	return out
}

// AttachmentIDs returns the external attachment ids pinned on the run.
func (r *Run) AttachmentIDs() []string {
	raw, _ := r.Input["agent_attachment_ids"].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// ExecutionMode resolves the mode from input.mode or the snapshot.
func (r *Run) ExecutionMode() string {
	if m, ok := r.Input["mode"].(string); ok && m != "" {
		return m
	}
	if snap, ok := r.RuntimeSnapshot["execution_mode"].(string); ok {
		return snap
	}
	return "interactive"
}

// SnapshotString pulls a string out of runtime_snapshot.
func (r *Run) SnapshotString(key string) string {
	if r.RuntimeSnapshot == nil {
		return ""
	}
	s, _ := r.RuntimeSnapshot[key].(string)
	return s
}

// SnapshotInt pulls an integer out of runtime_snapshot.
func (r *Run) SnapshotInt(key string, def int) int {
	if r.RuntimeSnapshot == nil {
		return def
	}
	switch v := r.RuntimeSnapshot[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	}
	return def
}

// IsTerminalEventName reports whether an event closes the SSE stream.
// Terminal events correspond one-to-one with terminal run statuses
// (修复计划 §16-17): run.retrying is deliberately NOT terminal — a
// requeued run keeps streaming; run.interrupted is legacy-only and no
// longer closes a live stream (retry must not break the SSE lifecycle).
func IsTerminalEventName(eventType string) bool {
	switch eventType {
	case EventRunCompleted, EventRunFailed, EventRunCancelled:
		return true
	}
	return false
}
