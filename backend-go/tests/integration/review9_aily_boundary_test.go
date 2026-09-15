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
//	Test 6  POST succeeded + EOF before the first frame ⇒ PARK, not fail
//	Test 7  a first frame without agent_chat_id is the same unknown
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

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
		execution.ProviderSubmissionHash(f.provKey, nil), execution.ResendForbidden)
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
		execution.ProviderSubmissionHash(f.provKey, nil), execution.ResendForbidden); err != nil {
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

// TestStreamEofBeforeChatIdParksInsteadOfFailing is the boundary the review
// found unclosed (第九轮复审 P1).
//
// OpenStreamChat succeeding means the request has crossed the submit
// boundary — the provider may already be running the agent. The chat id only
// arrives with the FIRST SSE frame, so there is a real window in which we
// have transmitted and hold no identity:
//
//	POST ok → body EOF before frame 1 → executor has no external id
//
// Failing the run there ("aily_no_chat_id") declares terminal-failed a run
// the provider is still executing. The honest state is UNKNOWN, i.e. park
// the run in waiting_external and let the bounded sweep resolve it.
func TestStreamEofBeforeChatIdParksInsteadOfFailing(t *testing.T) {
	f := newAilySubmitFixture(t, "r9eof", "interactive", "eof before first frame")
	ctx := context.Background()
	// POST succeeds, the body is already at EOF: no frame ever arrives.
	f.rec.streamBody = ""

	if err := f.exec.Execute(ctx, f.claimed); err != nil {
		t.Fatalf("execute: %v", err)
	}

	starts, opens := f.rec.counted()
	if starts != 0 || opens != 1 {
		t.Fatalf("provider calls: StartChat=%d OpenStreamChat=%d, want 0/1 "+
			"(exactly one submit, never a resend)", starts, opens)
	}
	st := readRunState(t, f.env.db, f.runID)
	if st.status != execution.StatusWaitingExternal {
		t.Fatalf("status=%q, want waiting_external — the provider may hold this request, "+
			"so it must be parked rather than failed", st.status)
	}
	state, externalID := submissionOf(t, f)
	if state != execution.SubmissionUnknown {
		t.Fatalf("submission state=%q, want unknown (an outcome nobody can confirm)", state)
	}
	if externalID != "" {
		t.Fatalf("external_run_id=%q, want empty (it is exactly what we never learned)", externalID)
	}
	if n := countEvents(t, f.svc, f.runID, execution.EventRunRetrying); n != 0 {
		t.Fatalf("run.retrying events = %d, want 0 (parked ≠ requeued)", n)
	}
	if n := countEvents(t, f.svc, f.runID, execution.EventRunFailed); n != 0 {
		t.Fatalf("run.failed events = %d, want 0 — the run is NOT entitled to declare "+
			"failure while the provider may still be executing", n)
	}
}

// TestFirstFrameWithoutChatIdParks: a frame that arrived without an
// agent_chat_id is not proof of anything. Emitting aily.stream.started for it
// would let the executor believe it holds the external identity, so the
// adapter waits for the id and the executor parks when it never comes.
func TestFirstFrameWithoutChatIdParks(t *testing.T) {
	f := newAilySubmitFixture(t, "r9nochat", "interactive", "frame without chat id")
	ctx := context.Background()
	f.rec.streamBody = "event: message\ndata: {\"session_id\":\"sess-1\"}\n\n"

	if err := f.exec.Execute(ctx, f.claimed); err != nil {
		t.Fatalf("execute: %v", err)
	}

	starts, opens := f.rec.counted()
	if starts != 0 || opens != 1 {
		t.Fatalf("provider calls: StartChat=%d OpenStreamChat=%d, want 0/1", starts, opens)
	}
	st := readRunState(t, f.env.db, f.runID)
	if st.status != execution.StatusWaitingExternal {
		t.Fatalf("status=%q, want waiting_external (no external id ⇒ unknown, not failure)", st.status)
	}
	if state, externalID := submissionOf(t, f); state != execution.SubmissionUnknown || externalID != "" {
		t.Fatalf("submission state=%q external=%q, want unknown/\"\"", state, externalID)
	}
}

// TestMarkSubmissionStateRequiresLiveOwnershipAndLegalTransition is the
// fencing invariant of 第九轮复审 P1.
//
// provider_submissions decides whether a provider action may be transmitted
// again, i.e. whether a SECOND real execution can happen. A worker whose lease
// expired mid-flight must therefore lose this write too — otherwise the
// "Owned" suffix is a promise the code does not keep. The state transition is
// additionally a CAS: 'accepted' is monotonic and an outcome may only replace
// an in-flight 'sending', never another verdict.
func TestMarkSubmissionStateRequiresLiveOwnershipAndLegalTransition(t *testing.T) {
	f := newAilySubmitFixture(t, "r9fence", "interactive", "submission fencing")
	ctx := context.Background()

	// ── Worker A begins the submission (row = sending). ──
	sub, err := f.svc.BeginProviderSubmissionOwned(ctx, f.claimed.Ownership, f.provKey,
		execution.ProviderSubmissionHash(f.provKey, nil), execution.ResendForbidden)
	if err != nil {
		t.Fatalf("begin submission: %v", err)
	}
	if state, _ := submissionOf(t, f); state != execution.SubmissionSending {
		t.Fatalf("initial submission state=%q, want sending", state)
	}

	// ── A stalls: its lease expires, the reaper requeues, B takes over. ──
	expireLease(t, f.svc, f.runID, "itest-r9fence")
	_ = recoverRun(t, f.svc, f.runID)
	claimedB, won, err := f.svc.ClaimRun(ctx, f.runID, "worker-b", time.Minute)
	if err != nil || !won {
		t.Fatalf("B claim: won=%v err=%v", won, err)
	}
	if claimedB.Ownership.LeaseEpoch <= f.claimed.Ownership.LeaseEpoch {
		t.Fatalf("epoch did not increase on reclaim: A=%d B=%d",
			f.claimed.Ownership.LeaseEpoch, claimedB.Ownership.LeaseEpoch)
	}

	// ── A's provider call returns now: its verdict must be refused. ──
	if err := f.svc.MarkSubmissionStateOwned(ctx, f.claimed.Ownership, sub,
		execution.SubmissionUnknown, "stale worker"); !errors.Is(err, execution.ErrLostOwnership) {
		t.Fatalf("stale MarkSubmissionStateOwned: err=%v, want ErrLostOwnership "+
			"(a fenced-out worker must not write the submission ledger)", err)
	}
	if state, _ := submissionOf(t, f); state != execution.SubmissionSending {
		t.Fatalf("submission state=%q after the stale write, want unchanged sending", state)
	}

	// ── The live owner may record the outcome. ──
	if err := f.svc.MarkSubmissionStateOwned(ctx, claimedB.Ownership, sub,
		execution.SubmissionUnknown, "unconfirmed"); err != nil {
		t.Fatalf("owner MarkSubmissionStateOwned: %v", err)
	}
	if state, _ := submissionOf(t, f); state != execution.SubmissionUnknown {
		t.Fatalf("submission state=%q, want unknown", state)
	}

	// ── And the transition is one-way: 'unknown' is no longer 'sending'. ──
	if err := f.svc.MarkSubmissionStateOwned(ctx, claimedB.Ownership, sub,
		execution.SubmissionRejected, "try to rewrite history"); err == nil {
		t.Fatal("a resolved submission was rewritten to 'rejected' — that re-opens a " +
			"resend of an action whose fate is unknown")
	}
	if state, _ := submissionOf(t, f); state != execution.SubmissionUnknown {
		t.Fatalf("submission state=%q after the illegal transition, want unknown", state)
	}
}

// TestChatIdOnALaterFrameStillResumes pins the other half of the adapter fix:
// waiting for the id must not mean MISSING it.
//
// The provider's first frame is not guaranteed to carry agent_chat_id, so the
// adapter keeps listening instead of declaring "started". If it claimed
// started on the first frame, a chat id that arrives on frame 2 would never be
// emitted and the executor would park a run whose provider answered normally.
func TestChatIdOnALaterFrameStillResumes(t *testing.T) {
	f := newAilySubmitFixture(t, "r9latechat", "interactive", "chat id on frame 2")
	ctx := context.Background()
	f.rec.streamBody = "event: message\ndata: {\"session_id\":\"sess-1\"}\n\n" +
		"event: message\ndata: {\"agent_chat_id\":\"chat-1\",\"session_id\":\"sess-1\"}\n\n"

	if err := f.exec.Execute(ctx, f.claimed); err != nil {
		t.Fatalf("execute: %v", err)
	}

	starts, opens := f.rec.counted()
	if starts != 0 || opens != 1 {
		t.Fatalf("provider calls: StartChat=%d OpenStreamChat=%d, want 0/1", starts, opens)
	}
	st := readRunState(t, f.env.db, f.runID)
	if st.status == execution.StatusWaitingExternal {
		t.Fatal("the run was parked although the provider DID report its chat id on a later " +
			"frame — the adapter must not claim 'started' on a frame that carries no identity")
	}
	if st.status != execution.StatusSucceeded {
		t.Fatalf("status=%q, want succeeded", st.status)
	}
	if state, externalID := submissionOf(t, f); externalID != "chat-1" || state != execution.SubmissionAccepted {
		t.Fatalf("submission state=%q external=%q, want accepted/chat-1", state, externalID)
	}
}
