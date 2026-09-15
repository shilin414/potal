// 第九轮 P0-2 / P1-4 端到端验证：真实 Aily executor + 真实 DB，只把 provider HTTP
// 传输替换成 fake。
//
// These are the tests that make the P0-2 claim falsifiable rather than
// theoretical. The property is "a provider request that may already exist is
// never sent twice", and the only place to observe it is the provider
// transport itself — hence the call counters on the fake (OpenStreamChat /
// StartChat), not a status assertion.
//
//	Test 1  an ACCEPTED submission is resumed, not resubmitted
//	Test 2  an UNRESOLVED submission parks the run without touching the provider
//	Test 3  a timeout at the submit boundary parks the run instead of retrying
//	Test 4  a definitive refusal still follows the ordinary retry policy
//	Test 5  durable content.chunk events stay INCREMENTAL (P1-4)
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/integrations/aily"
)

// submissionOf reads the run's only provider submission.
func submissionOf(t *testing.T, f *ailySubmitFixture) (state, externalID string) {
	t.Helper()
	var st, ext string
	if err := f.env.db.QueryRowContext(context.Background(),
		`SELECT state, external_run_id FROM provider_submissions WHERE run_id = ? ORDER BY submission_no DESC LIMIT 1`,
		f.runID.Bytes()).Scan(&st, &ext); err != nil {
		t.Fatalf("load provider submission: %v", err)
	}
	return st, ext
}

// TestReclaimOfAcceptedSubmissionDoesNotReopenTheStream is the P0-2 window,
// inverted into the case that must WORK.
//
// The dangerous shape is "provider accepted, we crashed before recording it".
// Its mirror image is "provider accepted AND we recorded it" — and a worker
// that re-claims such a run must converge from the recorded chat id. Opening
// the stream again would create a SECOND provider chat for one user message.
func TestReclaimOfAcceptedSubmissionDoesNotReopenTheStream(t *testing.T) {
	f := newAilySubmitFixture(t, "r9accepted", "interactive", "already accepted")
	ctx := context.Background()

	// A previous attempt that reached the provider and recorded its answer,
	// then died before finishing.
	sub, err := f.svc.BeginProviderSubmissionOwned(ctx, f.claimed.Ownership, f.provKey,
		execution.ProviderSubmissionHash(f.provKey, nil))
	if err != nil {
		t.Fatalf("begin submission: %v", err)
	}
	if err := f.svc.MarkSubmissionAcceptedOwned(ctx, f.claimed.Ownership, sub, "chat-1"); err != nil {
		t.Fatalf("mark accepted: %v", err)
	}

	// The new worker's attempt.
	if err := f.exec.Execute(ctx, f.claimed); err != nil {
		t.Fatalf("execute: %v", err)
	}

	starts, opens := f.rec.counted()
	if starts != 0 || opens != 0 {
		t.Fatalf("provider was contacted again for an ACCEPTED submission: "+
			"StartChat=%d OpenStreamChat=%d, want 0/0 (that is a second provider chat)", starts, opens)
	}
	state, externalID := submissionOf(t, f)
	if state != execution.SubmissionAccepted || externalID != "chat-1" {
		t.Fatalf("submission state=%q external=%q, want accepted/chat-1", state, externalID)
	}
	// It converged via the result API instead.
	st := readRunState(t, f.env.db, f.runID)
	if st.status != execution.StatusSucceeded {
		t.Fatalf("status=%q, want succeeded (reconciliation is the only authority)", st.status)
	}
}

// TestUnresolvedSubmissionParksWithoutContactingTheProvider: an attempt that
// recorded its intent to transmit and then died leaves an UNRESOLVED
// submission. Nothing local can say whether the provider got it, so the next
// attempt must not touch the provider at all.
func TestUnresolvedSubmissionParksWithoutContactingTheProvider(t *testing.T) {
	f := newAilySubmitFixture(t, "r9unknown", "background", "unresolved submit")
	ctx := context.Background()

	// The crashed worker: recorded the intent, never learned the outcome.
	if _, err := f.svc.BeginProviderSubmissionOwned(ctx, f.claimed.Ownership, f.provKey,
		execution.ProviderSubmissionHash(f.provKey, nil)); err != nil {
		t.Fatalf("begin submission: %v", err)
	}

	if err := f.exec.Execute(ctx, f.claimed); err != nil {
		t.Fatalf("execute: %v", err)
	}

	starts, opens := f.rec.counted()
	if starts != 0 || opens != 0 {
		t.Fatalf("the provider was contacted again after an unresolved submit: "+
			"StartChat=%d OpenStreamChat=%d, want 0/0 (at-most-once)", starts, opens)
	}
	st := readRunState(t, f.env.db, f.runID)
	if st.status != execution.StatusWaitingExternal {
		t.Fatalf("status=%q, want waiting_external — the run must be parked, not failed and not retried", st.status)
	}
	if n := countEvents(t, f.svc, f.runID, execution.EventRunWaitingExternal); n != 1 {
		t.Fatalf("run.waiting_external events = %d, want 1", n)
	}
	// It is NOT a retry: the run must not be sitting in the queue waiting to
	// be claimed again with the same unresolved submission.
	if n := countEvents(t, f.svc, f.runID, execution.EventRunRetrying); n != 0 {
		t.Fatalf("run.retrying events = %d, want 0", n)
	}
}

// TestSubmitTimeoutParksInsteadOfRetrying is the review's headline scenario: a
// timeout at the submit boundary means "the request may be there", and the
// Aily API cannot deduplicate or be asked. The run must be parked.
func TestSubmitTimeoutParksInsteadOfRetrying(t *testing.T) {
	f := newAilySubmitFixture(t, "r9timeout", "background", "submit timeout")
	ctx := context.Background()
	f.rec.chatErr = &aily.APIError{Kind: aily.ErrTimeout, Msg: "context deadline exceeded"}

	if err := f.exec.Execute(ctx, f.claimed); err != nil {
		t.Fatalf("execute: %v", err)
	}

	// Exactly ONE provider call: the failure must not be retried in place.
	starts, opens := f.rec.counted()
	if starts != 1 || opens != 0 {
		t.Fatalf("provider calls: StartChat=%d OpenStreamChat=%d, want 1/0", starts, opens)
	}
	st := readRunState(t, f.env.db, f.runID)
	if st.status != execution.StatusWaitingExternal {
		t.Fatalf("status=%q, want waiting_external (a timed-out submit may have been delivered)", st.status)
	}
	if st.errorCode != "" {
		// waiting_external carries no terminal error code; the code is on
		// the event payload / the resolved failure.
		t.Logf("run error_code while parked = %q", st.errorCode)
	}
	state, _ := submissionOf(t, f)
	if state != execution.SubmissionUnknown {
		t.Fatalf("submission state=%q, want unknown (the outcome is genuinely unknown, "+
			"and the next attempt needs that fact to refuse a resend)", state)
	}
	if n := countEvents(t, f.svc, f.runID, execution.EventRunRetrying); n != 0 {
		t.Fatalf("run.retrying events = %d, want 0 — a timeout must never be retried for a "+
			"provider that cannot deduplicate a resend", n)
	}
}

// TestDefinitiveRefusalStillUsesTheOrdinaryFailPolicy: a 400 is an ANSWER —
// the provider looked at the request and refused it — so it must NOT be
// parked. Parking here would turn every bad request into a manual
// intervention.
func TestDefinitiveRefusalStillUsesTheOrdinaryFailPolicy(t *testing.T) {
	f := newAilySubmitFixture(t, "r9refused", "background", "definitive refusal")
	ctx := context.Background()
	f.rec.chatErr = &aily.APIError{Kind: aily.ErrClient, Msg: "invalid content", HTTPStatus: 400}

	if err := f.exec.Execute(ctx, f.claimed); err != nil {
		t.Fatalf("execute: %v", err)
	}

	st := readRunState(t, f.env.db, f.runID)
	if st.status == execution.StatusWaitingExternal {
		t.Fatal("a definitively refused submit was parked: the provider holds nothing, " +
			"so parking only delays a failure the provider already stated")
	}
	if st.status != execution.StatusFailed {
		t.Fatalf("status=%q, want failed", st.status)
	}
	state, _ := submissionOf(t, f)
	if state != execution.SubmissionRejected {
		t.Fatalf("submission state=%q, want rejected (the only state that re-opens a retry)", state)
	}
}

// TestStreamingChunkEventsAreIncremental pins 第九轮 P1-4 end to end, on the
// REAL executor writing REAL run_events rows.
//
// A cumulative `snapshot` on every chunk makes the run's durable data
// quadratic in the answer length: a 1 MB answer writes 2KB + 4KB + ... + 1MB
// ≈ 250 MB. That is invisible in small assertions, so this test streams a long
// answer through the actual coalescing path and measures the stored bytes.
func TestStreamingChunkEventsAreIncremental(t *testing.T) {
	f := newAilySubmitFixture(t, "r9chunk", "interactive", "long answer")
	ctx := context.Background()

	// 400 deltas of 100 bytes = 40 KB, i.e. ~20 flushes at the 2000-byte
	// threshold. A cumulative snapshot would store ~420 KB instead.
	const tokens = 400
	piece := strings.Repeat("x", 99) + " "
	var sb strings.Builder
	for i := 0; i < tokens; i++ {
		payload, _ := json.Marshal(map[string]any{
			"agent_chat_id": "chat-1", "session_id": "sess-1", "text": piece,
		})
		fmt.Fprintf(&sb, "event: message\ndata: %s\n\n", payload)
	}
	f.rec.streamBody = sb.String()

	if err := f.exec.Execute(ctx, f.claimed); err != nil {
		t.Fatalf("execute: %v", err)
	}

	events := listAllEvents(t, f.svc, f.runID)
	answer := strings.Repeat(piece, tokens)
	var chunks int
	var storedBytes int
	var lastOffset int
	for _, ev := range events {
		if ev.EventType != execution.EventContentChunk {
			continue
		}
		chunks++
		if _, hasSnapshot := ev.Payload["snapshot"]; hasSnapshot {
			t.Fatalf("content.chunk #%d carries a cumulative snapshot: durable event data "+
				"grows quadratically with the answer length (P1-4)", chunks)
		}
		if len(ev.Payload) != 2 {
			t.Fatalf("content.chunk #%d payload keys = %v, want exactly text + offset", chunks, ev.Payload)
		}
		text, _ := ev.Payload["text"].(string)
		storedBytes += len(text)
		off, _ := ev.Payload["offset"].(float64)
		if int(off) <= lastOffset {
			t.Fatalf("chunk #%d offset = %v, want strictly greater than %d", chunks, off, lastOffset)
		}
		lastOffset = int(off)
	}
	if chunks < 2 {
		t.Fatalf("content.chunk events = %d, want >= 2 (the test must actually cross the flush threshold)", chunks)
	}
	// Incremental means the stored text IS the answer, byte for byte: no
	// repetition, no loss.
	if storedBytes != len(answer) {
		t.Fatalf("stored chunk text = %d bytes, want %d (the answer length)", storedBytes, len(answer))
	}
	if lastOffset != len(answer) {
		t.Fatalf("final offset = %d, want %d", lastOffset, len(answer))
	}
}
