package execution

import "testing"

// 第五轮 P2-1: the `interrupted` status used to carry TWO conflicting
// contracts at once —
//
//	(a) the admission predicate treated it as non-terminal (so a legacy run
//	    pinned a conversation at 409 and consumed an outstanding slot
//	    forever);
//	(b) the frontend and terminalEventName() treated it as a terminal
//	    failure.
//
// The contract is now: `interrupted` is a PRE-CLOSURE TERMINAL ALIAS,
// equivalent to `failed`. New code never writes it and migration 0018
// rewrites historical rows to `failed`.
func TestInterruptedIsSettledButNotCanonicalTerminal(t *testing.T) {
	if IsTerminal(StatusInterrupted) {
		t.Error("IsTerminal(interrupted) = true: it must stay OUT of the canonical " +
			"terminal set so finalization never accepts/produces it")
	}
	if !IsSettled(StatusInterrupted) {
		t.Error("IsSettled(interrupted) = false: a legacy run would occupy a " +
			"conversation and an outstanding slot forever")
	}
	for _, s := range []string{StatusCancelled, StatusSucceeded, StatusFailed} {
		if !IsTerminal(s) || !IsSettled(s) {
			t.Errorf("status %q must be both terminal and settled", s)
		}
	}
	for _, s := range []string{
		StatusQueued, StatusRunning, StatusWaitingInput,
		StatusWaitingExternal, StatusCancelling,
	} {
		if IsSettled(s) {
			t.Errorf("status %q must NOT be settled (it is live work)", s)
		}
	}
}

// Finalization must reject the legacy alias outright rather than silently
// emitting a terminal event for a status the domain no longer produces.
func TestTerminalEventNameRejectsLegacyInterrupted(t *testing.T) {
	if _, err := terminalEventName(StatusInterrupted); err == nil {
		t.Fatal("terminalEventName(interrupted) succeeded: the legacy alias must not " +
			"be finalizable (migration 0018 rewrites those rows to failed)")
	}
	if got, err := terminalEventName(StatusFailed); err != nil || got != EventRunFailed {
		t.Fatalf("terminalEventName(failed) = %q, %v", got, err)
	}
	if got, err := terminalEventName(StatusCancelled); err != nil || got != EventRunCancelled {
		t.Fatalf("terminalEventName(cancelled) = %q, %v", got, err)
	}
	if got, err := terminalEventName(StatusSucceeded); err != nil || got != EventRunCompleted {
		t.Fatalf("terminalEventName(succeeded) = %q, %v", got, err)
	}
}

// A replayed legacy run.interrupted must remain READABLE, but it must not
// be treated as a terminal event that closes a live stream (that is what
// run.retrying is for).
func TestLegacyInterruptedEventDoesNotCloseLiveStream(t *testing.T) {
	if IsTerminalEventName(EventRunInterrupted) {
		t.Error("run.interrupted must not be a terminal event name: legacy replay " +
			"would close streams that the retry path keeps open")
	}
	for _, ev := range []string{EventRunCompleted, EventRunFailed, EventRunCancelled} {
		if !IsTerminalEventName(ev) {
			t.Errorf("event %q must be terminal", ev)
		}
	}
	if IsTerminalEventName(EventRunRetrying) {
		t.Error("run.retrying must not be terminal: a requeued run keeps streaming")
	}
}
