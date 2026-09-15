package sse

import (
	"testing"

	"github.com/creation-agent-studio/backend-go/internal/execution"
)

// 第六轮 P1: the synthetic terminal frame must preserve the STATUS the
// run actually reached. The pre-fix inline mapping only special-cased
// failed/interrupted, so a settled `cancelled` run was reported to
// reconnecting clients as run.completed — reintroducing the
// "cancelled ≠ success" defect the execution closure removed.
func TestSyntheticTerminalEventName(t *testing.T) {
	cases := []struct {
		status    string
		wantEvent string
		wantOK    bool
	}{
		// Canonical terminals map one-to-one.
		{execution.StatusSucceeded, execution.EventRunCompleted, true},
		{execution.StatusFailed, execution.EventRunFailed, true},
		{execution.StatusCancelled, execution.EventRunCancelled, true},
		// Pre-closure alias (0018 normalizes the rows; until then the
		// frame must stay a failure, never a success).
		{execution.StatusInterrupted, execution.EventRunFailed, true},
		// Live work and unknown statuses produce NO frame: fabricating
		// one would either close a live stream or invent an outcome.
		{execution.StatusQueued, "", false},
		{execution.StatusRunning, "", false},
		{execution.StatusWaitingInput, "", false},
		{execution.StatusWaitingExternal, "", false},
		{execution.StatusCancelling, "", false},
		{"", "", false},
		{"something_new", "", false},
	}
	for _, tc := range cases {
		gotEvent, gotOK := syntheticTerminalEventName(tc.status)
		if gotEvent != tc.wantEvent || gotOK != tc.wantOK {
			t.Errorf("syntheticTerminalEventName(%q) = (%q, %v), want (%q, %v)",
				tc.status, gotEvent, gotOK, tc.wantEvent, tc.wantOK)
		}
	}
}

// Every settled status must be mappable — otherwise a settled run whose
// terminal event row is missing would leave a reconnecting client with
// an open stream that never terminates (修复计划 §19).
func TestSyntheticTerminalEventNameCoversSettledStatuses(t *testing.T) {
	for _, status := range []string{
		execution.StatusSucceeded,
		execution.StatusFailed,
		execution.StatusCancelled,
		execution.StatusInterrupted,
	} {
		if !execution.IsSettled(status) {
			t.Errorf("fixture status %q is not settled — the test no longer covers "+
				"the fallback path", status)
		}
		if _, ok := syntheticTerminalEventName(status); !ok {
			t.Errorf("settled status %q has no synthetic terminal event name: its "+
				"stream can never be closed", status)
		}
	}
}

// Cancelled must never be reported as success — the single most
// important assertion of this file.
func TestSyntheticTerminalEventNameNeverMapsCancelledToSuccess(t *testing.T) {
	got, ok := syntheticTerminalEventName(execution.StatusCancelled)
	if !ok {
		t.Fatal("cancelled has no synthetic mapping")
	}
	if got == execution.EventRunCompleted {
		t.Fatal("cancelled synthesized as run.completed: a user-cancelled run " +
			"would be rendered as a success")
	}
}
