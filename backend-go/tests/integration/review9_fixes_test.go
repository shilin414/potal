// 第九轮专项整改验证 (STUDIO_TEST_DB=1).
//
// Proven here, against the real MySQL 5.7:
//
//	P0-1  POST /v2/runs is end-to-end idempotent per client_request_id:
//	      a resend creates NOTHING new, a reused id with a different payload
//	      is refused, and a resend outranks the admission limits its own
//	      original already consumed.
//	P0-2  a provider submit whose outcome is unknown is NEVER transmitted
//	      again: the run is parked in waiting_external, the conversation is
//	      released again by the bounded sweep, and a provider chat that WAS
//	      accepted is resumed instead of duplicated.
//	P1-3  run-event sequences come from an O(1) per-run allocator (gap-free,
//	      monotonic, isolated per run) and every event read is bounded.
//
// The two P0s are behavioural: the assertions are on row COUNTS after a
// replay and on the persisted submission state, not on the presence of a
// code path.
package integration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
)

// ── helpers ──

// idempotentInput builds a submit for the given identity.
func idempotentInput(clientRequestID, content string, convID int64) *execution.CreateRunInput {
	return idempotentInputFor(42, clientRequestID, content, convID)
}

func countRunRequestIdentity(t *testing.T, svc *execution.Service, userID int64, clientRequestID string) int64 {
	t.Helper()
	var n int64
	if err := svc.DB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM run_requests WHERE user_id = ? AND client_request_id = ?`,
		userID, clientRequestID).Scan(&n); err != nil {
		t.Fatalf("count run-request identity: %v", err)
	}
	return n
}

// idempotentInputFor is the same submit for a specific user. The per-user
// OUTSTANDING cap is a whole-user count, so a test that exercises it must own
// its user outright — user 42 is shared by every fixture in this package.
func idempotentInputFor(userID int64, clientRequestID, content string, convID int64) *execution.CreateRunInput {
	in := &execution.CreateRunInput{
		UserID:          userID,
		ApplicationID:   1,
		ConversationID:  convID,
		Provider:        "itest_idem",
		RuntimeType:     "agent",
		ExecutionMode:   "interactive",
		Content:         content,
		ClientRequestID: clientRequestID,
	}
	if convID == 0 {
		in.CreateConversation = true
		in.ConversationTitle = "idem"
	}
	in.RequestHash = execution.RunRequestHash(in.ApplicationID, convID, content, in.AttachmentIDs)
	return in
}

// seedAdmissionUser materializes the fixture user that the per-user admission
// lock needs (LockUserRow takes a row lock, so the row must exist). It is
// created only when absent and removed again in that case, so the shared dev
// database is left as it was found.
func seedAdmissionUser(t *testing.T, svc *execution.Service, userID int64) int64 {
	t.Helper()
	ctx := context.Background()
	var existed int
	if err := svc.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id = ?`, userID).Scan(&existed); err != nil {
		t.Fatalf("probe user %d: %v", userID, err)
	}
	if _, err := svc.DB.ExecContext(ctx,
		`INSERT INTO users (id, username, display_name, role, is_active)
		 VALUES (?, ?, 'itest', 'creator', 1)
		 ON DUPLICATE KEY UPDATE id = id`,
		userID, fmt.Sprintf("itest_review9_user_%d", userID)); err != nil {
		t.Fatalf("seed user %d: %v", userID, err)
	}
	if existed == 0 {
		t.Cleanup(func() {
			_, _ = svc.DB.ExecContext(context.Background(), `DELETE FROM users WHERE id = ?`, userID)
		})
	}
	return userID
}

// conversationOf returns the conversation a run belongs to.
func conversationOf(t *testing.T, svc *execution.Service, run *execution.Run) int64 {
	t.Helper()
	if run.ConversationID == nil {
		t.Fatalf("run %s has no conversation", run.ID)
	}
	return *run.ConversationID
}

// countConversationRows counts the observable effects of ONE submit:
// conversations, user messages, runs and outbox dispatches.
func countConversationRows(t *testing.T, svc *execution.Service, convID int64) (runs, messages, outbox int64) {
	t.Helper()
	ctx := context.Background()
	if err := svc.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM runs WHERE conversation_id = ?`, convID).Scan(&runs); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if err := svc.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE conversation_id = ? AND role = 'user'`, convID).Scan(&messages); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if err := svc.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM outbox_events o JOIN runs r ON r.id = o.aggregate_id
		  WHERE o.aggregate = 'run' AND o.event_type = 'run.dispatch' AND r.conversation_id = ?`,
		convID).Scan(&outbox); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	return runs, messages, outbox
}

// cleanupConversation removes a fixture conversation and everything hanging
// off it, in FK order. It is scoped to ids the test itself created, so it can
// never reach another test's rows — and it exists because a fixture left
// behind makes the NEXT run's "expected 0" assertions meaningless.
func cleanupConversation(t *testing.T, svc *execution.Service, convID int64) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		stmts := []string{
			`DELETE e FROM run_events e JOIN runs r ON r.id = e.run_id WHERE r.conversation_id = ?`,
			`DELETE a FROM run_artifacts a JOIN runs r ON r.id = a.run_id WHERE r.conversation_id = ?`,
			`DELETE c FROM run_commands c JOIN runs r ON r.id = c.run_id WHERE r.conversation_id = ?`,
			`DELETE s FROM provider_execution_slots s JOIN runs r ON r.id = s.run_id WHERE r.conversation_id = ?`,
			`DELETE l FROM run_leases l JOIN runs r ON r.id = l.run_id WHERE r.conversation_id = ?`,
			`DELETE p FROM provider_submissions p JOIN runs r ON r.id = p.run_id WHERE r.conversation_id = ?`,
			`DELETE o FROM outbox_events o JOIN runs r ON r.id = o.aggregate_id WHERE r.conversation_id = ?`,
			`DELETE FROM run_requests WHERE run_id IN (SELECT id FROM runs WHERE conversation_id = ?)`,
			`DELETE FROM runs WHERE conversation_id = ?`,
			`DELETE FROM messages WHERE conversation_id = ?`,
			`DELETE FROM agent_threads WHERE conversation_id = ?`,
			`DELETE FROM conversations WHERE id = ?`,
		}
		for _, stmt := range stmts {
			if _, err := svc.DB.ExecContext(ctx, stmt, convID); err != nil {
				t.Logf("cleanup %q: %v", stmt, err)
			}
		}
	})
}

// parkFixture supplies a claimed run owned by this test.
type parkFixture struct {
	svc     *execution.Service
	runID   ids.ID
	claimed *execution.ClaimedRun
	convID  int64
}

func newParkFixture(t *testing.T, provider string) *parkFixture {
	t.Helper()
	svc, _ := testEnv(t)
	ctx := context.Background()
	convID := seedConversation(t, svc)
	cleanupConversation(t, svc, convID)
	runID := seedRunWithConversation(t, svc, provider, convID)
	claimed, won, err := svc.ClaimRun(ctx, runID, "worker-park", time.Minute)
	if err != nil || !won {
		t.Fatalf("claim: won=%v err=%v", won, err)
	}
	return &parkFixture{svc: svc, runID: runID, claimed: claimed, convID: convID}
}

// ── P0-1: request idempotency ──

// TestCreateRunIdempotentReplayCreatesNothingNew is the core P0-1 property:
// the SAME request submitted twice yields ONE turn. Asserted on row counts
// rather than on the returned run id, because the hazard is a second user
// message and a second provider chat, not a second pointer.
func TestCreateRunIdempotentReplayCreatesNothingNew(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	const reqID = "review9-replay"

	first, replayed, err := svc.CreateRunIdempotent(ctx, idempotentInput(reqID, "hello", 0), 0)
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}
	if replayed {
		t.Fatal("the first submit reported a replay")
	}
	convID := conversationOf(t, svc, first)
	cleanupConversation(t, svc, convID)

	second, replayed, err := svc.CreateRunIdempotent(ctx, idempotentInput(reqID, "hello", 0), 0)
	if err != nil {
		t.Fatalf("resend: %v", err)
	}
	if !replayed {
		t.Fatal("a resend with the same client_request_id and payload was not reported as a replay")
	}
	if second.ID != first.ID {
		t.Fatalf("resend returned run %s, want the original %s", second.ID, first.ID)
	}
	if got := conversationOf(t, svc, second); got != convID {
		t.Fatalf("resend produced conversation %d, want the original %d", got, convID)
	}

	runs, messages, outbox := countConversationRows(t, svc, convID)
	if runs != 1 || messages != 1 || outbox != 1 {
		t.Fatalf("after the resend: runs=%d messages=%d outbox=%d, want 1/1/1 "+
			"(a replay must write nothing new)", runs, messages, outbox)
	}
	// Exactly one reservation, still pointing at the original run.
	var n int64
	if err := svc.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM run_requests WHERE user_id = 42 AND client_request_id = ?`, reqID).Scan(&n); err != nil {
		t.Fatalf("count reservations: %v", err)
	}
	if n != 1 {
		t.Fatalf("run_requests rows = %d, want 1", n)
	}
}

// TestCreateRunIdempotentConflictOnDifferentPayload: the same identity with a
// different request is a client error, not a replay. Returning the original
// run here would silently discard the user's new message.
func TestCreateRunIdempotentConflictOnDifferentPayload(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	const reqID = "review9-conflict"

	first, _, err := svc.CreateRunIdempotent(ctx, idempotentInput(reqID, "original", 0), 0)
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}
	convID := conversationOf(t, svc, first)
	cleanupConversation(t, svc, convID)

	_, _, err = svc.CreateRunIdempotent(ctx, idempotentInput(reqID, "edited", 0), 0)
	if !errors.Is(err, execution.ErrIdempotencyKeyReused) {
		t.Fatalf("same id, different content: err=%v, want ErrIdempotencyKeyReused", err)
	}
	runs, messages, _ := countConversationRows(t, svc, convID)
	if runs != 1 || messages != 1 {
		t.Fatalf("after the conflict: runs=%d messages=%d, want 1/1", runs, messages)
	}
}

// TestCreateRunIdempotentResolveIsReadOnly: resolving must not create anything
// — it is the read the handler performs BEFORE authorization and admission, so
// any write here would happen even for requests that are never admitted.
func TestCreateRunIdempotentResolveIsReadOnly(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	convID := seedConversation(t, svc)
	cleanupConversation(t, svc, convID)

	reqID := fmt.Sprintf("review9-resolve-%d", time.Now().UnixNano())
	const userID = int64(42)
	if before := countRunRequestIdentity(t, svc, userID, reqID); before != 0 {
		t.Fatalf("unique fixture unexpectedly existed before resolve: %d row(s)", before)
	}
	hash := execution.RunRequestHash(1, convID, "payload", nil)
	run, found, err := svc.ResolveRunRequest(ctx, userID, reqID, hash)
	if err != nil || found || run != nil {
		t.Fatalf("resolve of an unknown request: run=%v found=%v err=%v, want nil/false/nil", run, found, err)
	}
	if after := countRunRequestIdentity(t, svc, userID, reqID); after != 0 {
		t.Fatalf("a read-only resolve inserted %d reservation(s) for its fixture identity", after)
	}
}

// TestCreateRunIdempotentConcurrentReplay is the scenario the review opens
// with: N concurrent resends of ONE client action. The assertion is that the
// database ends up with a single turn, whichever way the race resolves.
func TestCreateRunIdempotentConcurrentReplay(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	const reqID = "review9-concurrent"
	const n = 20

	var created, replayedCount, other atomic.Int64
	runIDs := make([]string, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			run, replayed, err := svc.CreateRunIdempotent(ctx, idempotentInput(reqID, "burst", 0), 0)
			switch {
			case err != nil:
				other.Add(1)
				t.Logf("concurrent submit %d: %v", i, err)
			default:
				if replayed {
					replayedCount.Add(1)
				} else {
					created.Add(1)
				}
				runIDs[i] = run.ID.String()
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if other.Load() != 0 {
		t.Fatalf("%d concurrent submits failed outright, want 0", other.Load())
	}
	if created.Load() != 1 {
		t.Fatalf("%d duplicate runs were created by concurrent resends, want exactly 1", created.Load())
	}

	// Every response must point at that one run.
	winner := ""
	for _, id := range runIDs {
		if id == "" {
			continue
		}
		if winner == "" {
			winner = id
		} else if id != winner {
			t.Fatalf("concurrent resends returned different runs: %s vs %s", winner, id)
		}
	}
	var rid ids.ID
	if err := rid.Scan(winner); err != nil {
		t.Fatalf("parse winner: %v", err)
	}
	run, err := svc.GetRun(ctx, rid)
	if err != nil {
		t.Fatalf("load winner: %v", err)
	}
	convID := conversationOf(t, svc, run)
	cleanupConversation(t, svc, convID)

	runs, messages, outbox := countConversationRows(t, svc, convID)
	if runs != 1 || messages != 1 || outbox != 1 {
		t.Fatalf("after %d concurrent resends: runs=%d messages=%d outbox=%d, want 1/1/1", n, runs, messages, outbox)
	}
}

// TestCreateRunIdempotentReservationRollsBackWithFailure: a reserved identity
// must never exist without its run. A submit refused by the conversation
// admission leaves NO reservation, so the client's retry is still free to
// create the run.
func TestCreateRunIdempotentReservationRollsBackWithFailure(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	convID := seedConversation(t, svc)
	cleanupConversation(t, svc, convID)

	// Occupy the conversation with a live turn.
	if _, _, err := svc.CreateRunIdempotent(ctx, idempotentInput("", "first turn", convID), 0); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	const reqID = "review9-rollback"
	if _, _, err := svc.CreateRunIdempotent(ctx, idempotentInput(reqID, "second turn", convID), 0); !errors.Is(err, execution.ErrConversationBusy) {
		t.Fatalf("second turn err=%v, want ErrConversationBusy", err)
	}
	var n int64
	if err := svc.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM run_requests WHERE user_id = 42 AND client_request_id = ?`, reqID).Scan(&n); err != nil {
		t.Fatalf("count reservations: %v", err)
	}
	if n != 0 {
		t.Fatal("a refused submit left its request identity reserved: the client's retry " +
			"would be answered with a 200 replay for a run that never existed")
	}

	// Retry after the conversation frees up really creates the run.
	if _, err := svc.DB.ExecContext(ctx,
		`UPDATE runs SET status = 'succeeded' WHERE conversation_id = ?`, convID); err != nil {
		t.Fatalf("free the conversation: %v", err)
	}
	run, replayed, err := svc.CreateRunIdempotent(ctx, idempotentInput(reqID, "second turn", convID), 0)
	if err != nil {
		t.Fatalf("retry after the refusal: %v", err)
	}
	if replayed {
		t.Fatal("the retry was reported as a replay although nothing had been created")
	}
	if run.Status != execution.StatusQueued {
		t.Fatalf("retry created run with status %q, want queued", run.Status)
	}
}

// TestCreateRunReplayOutranksAdmissionLimits is the subtle half of P0-1: the
// first attempt of a request consumed admission (it is an outstanding run and
// its attachments are claimed). A resend must therefore be answered BEFORE
// those checks, or a successful request would be reported as a limit
// violation on its own retry.
func TestCreateRunReplayOutranksAdmissionLimits(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	// A user nobody else uses: the outstanding cap counts EVERY non-settled run
	// of a user, so sharing user 42 would make this test depend on whatever
	// other fixtures happen to be live.
	const userID = 90420042
	seedAdmissionUser(t, svc, userID)
	const reqID = "review9-outstanding"

	first, _, err := svc.CreateRunIdempotent(ctx, idempotentInputFor(userID, reqID, "one", 0), 1)
	if err != nil {
		t.Fatalf("first submit under a cap of 1: %v", err)
	}
	convID := conversationOf(t, svc, first)
	cleanupConversation(t, svc, convID)

	// A DIFFERENT request is refused by the outstanding cap, as it should be.
	if _, _, err := svc.CreateRunIdempotent(ctx, idempotentInputFor(userID, "review9-other", "two", 0), 1); !errors.Is(err, execution.ErrUserOutstandingExceeded) {
		t.Fatalf("a second distinct request err=%v, want ErrUserOutstandingExceeded (the cap must still work)", err)
	}

	// The resend of the FIRST request succeeds even though the cap is full:
	// the run it would be counted against is its own.
	again, replayed, err := svc.CreateRunIdempotent(ctx, idempotentInputFor(userID, reqID, "one", 0), 1)
	if err != nil {
		t.Fatalf("resend under a full outstanding cap: %v — a replay must outrank admission limits", err)
	}
	if !replayed || again.ID != first.ID {
		t.Fatalf("resend: replayed=%v id=%s, want true / %s", replayed, again.ID, first.ID)
	}
}

// ── P0-2: provider submission state machine ──

// TestSubmissionUnknownForbidsResend: once an attempt has recorded the intent
// to transmit, a second begin must refuse — this is what stops the crash
// window from becoming a second provider chat.
func TestSubmissionUnknownForbidsResend(t *testing.T) {
	f := newParkFixture(t, "itest_parked")
	ctx := context.Background()
	provider := "itest_parked"
	hash := submissionFixtureHash(provider)

	sub, err := f.svc.BeginProviderSubmissionOwned(ctx, f.claimed.Ownership, provider, hash, execution.ResendForbidden)
	if err != nil {
		t.Fatalf("begin submission: %v", err)
	}
	if sub.State != execution.SubmissionSending {
		t.Fatalf("state = %q, want %q", sub.State, execution.SubmissionSending)
	}

	if _, err := f.svc.BeginProviderSubmissionOwned(ctx, f.claimed.Ownership, provider, hash, execution.ResendForbidden); !errors.Is(err, execution.ErrProviderSubmitUnknown) {
		t.Fatalf("resend over an in-flight submission: err=%v, want ErrProviderSubmitUnknown", err)
	}
	// Even a different payload must not produce a second transmit while the
	// first may be at the provider: the second key would be a second action.
	if _, err := f.svc.BeginProviderSubmissionOwned(ctx, f.claimed.Ownership, provider,
		execution.ProviderSubmissionHash(provider, []byte(`{"content":[{"type":"text","text":"edited"}]}`)), execution.ResendForbidden); !errors.Is(err, execution.ErrProviderSubmitUnknown) {
		t.Fatalf("resend with a changed payload over an in-flight submission: err=%v, want ErrProviderSubmitUnknown", err)
	}

	run, err := f.svc.GetRun(ctx, f.runID)
	if err != nil {
		t.Fatalf("load run: %v", err)
	}
	if run.Attempt != 1 {
		t.Fatalf("attempt = %d, want 1 (a refused begin must not consume budget)", run.Attempt)
	}
	var n int64
	if err := f.svc.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM provider_submissions WHERE run_id = ?`, f.runID.Bytes()).Scan(&n); err != nil {
		t.Fatalf("count submissions: %v", err)
	}
	if n != 1 {
		t.Fatalf("provider_submissions rows = %d, want 1 (one external action, one identity)", n)
	}
}

// TestSubmissionAcceptedIsResumedNotResubmitted: the outcome the review calls
// "Provider 已接受，但本地尚未记录" is precisely what must NOT happen — so the
// inverse (accepted and recorded) has to be resumable. The submission state
// and the run's external id are written in ONE transaction, and a later begin
// hands the id back instead of a green light to submit.
func TestSubmissionAcceptedIsResumedNotResubmitted(t *testing.T) {
	f := newParkFixture(t, "itest_parked_accepted")
	ctx := context.Background()
	provider := "itest_parked_accepted"
	hash := submissionFixtureHash(provider)

	sub, err := f.svc.BeginProviderSubmissionOwned(ctx, f.claimed.Ownership, provider, hash, execution.ResendForbidden)
	if err != nil {
		t.Fatalf("begin submission: %v", err)
	}
	if err := f.svc.MarkSubmissionAcceptedOwned(ctx, f.claimed.Ownership, sub, "chat-abc"); err != nil {
		t.Fatalf("mark accepted: %v", err)
	}

	// Both halves landed together.
	run, err := f.svc.GetRun(ctx, f.runID)
	if err != nil {
		t.Fatalf("load run: %v", err)
	}
	if run.ExternalRunID != "chat-abc" {
		t.Fatalf("run.external_run_id = %q, want chat-abc", run.ExternalRunID)
	}

	again, err := f.svc.BeginProviderSubmissionOwned(ctx, f.claimed.Ownership, provider, hash, execution.ResendForbidden)
	if err != nil {
		t.Fatalf("begin after acceptance: %v", err)
	}
	if again.State != execution.SubmissionAccepted {
		t.Fatalf("state = %q, want %q — a re-claim must resume, not resubmit", again.State, execution.SubmissionAccepted)
	}
	if again.ExternalRunID != "chat-abc" {
		t.Fatalf("resumed external id = %q, want chat-abc", again.ExternalRunID)
	}
	if again.IdempotencyKey != sub.IdempotencyKey {
		t.Fatalf("idempotency key changed: %q → %q", sub.IdempotencyKey, again.IdempotencyKey)
	}
}

// TestSubmissionRejectedAllowsRetryWithTheSameKey: a definitive refusal is the
// ONE outcome that leaves the action re-transmittable, and it must keep the
// same provider-facing key so a provider that honours keys still sees one
// action.
func TestSubmissionRejectedAllowsRetryWithTheSameKey(t *testing.T) {
	f := newParkFixture(t, "itest_parked_rejected")
	ctx := context.Background()
	provider := "itest_parked_rejected"
	hash := submissionFixtureHash(provider)

	first, err := f.svc.BeginProviderSubmissionOwned(ctx, f.claimed.Ownership, provider, hash, execution.ResendForbidden)
	if err != nil {
		t.Fatalf("begin submission: %v", err)
	}
	if err := f.svc.MarkSubmissionStateOwned(ctx, f.claimed.Ownership, first, execution.SubmissionRejected, "400"); err != nil {
		t.Fatalf("mark rejected: %v", err)
	}
	second, err := f.svc.BeginProviderSubmissionOwned(ctx, f.claimed.Ownership, provider, hash, execution.ResendForbidden)
	if err != nil {
		t.Fatalf("retry after a refusal: %v", err)
	}
	if second.SubmissionNo != first.SubmissionNo || second.IdempotencyKey != first.IdempotencyKey {
		t.Fatalf("a retry of the same payload changed identity: no %d→%d key %q→%q",
			first.SubmissionNo, second.SubmissionNo, first.IdempotencyKey, second.IdempotencyKey)
	}
	if second.Attempt != 2 {
		t.Fatalf("attempt = %d, want 2", second.Attempt)
	}
}

// TestNativeIdempotencyMayResendOnTheSameKey closes the loop the review found
// missing (第九轮复审 P2).
//
// classifySubmitFailure already said "a native-idempotent provider may retry
// after an unknown outcome", but BeginProviderSubmission refused EVERY
// 'unknown' submission, so the two halves of the policy disagreed and that
// branch was dead code. An unconfirmed submission is now resendable — under
// the SAME submission number and the SAME provider-facing key, with attempt
// incremented — but ONLY when the caller proves the provider can collapse a
// resend on that key.
func TestNativeIdempotencyMayResendOnTheSameKey(t *testing.T) {
	f := newParkFixture(t, "itest_parked_native")
	ctx := context.Background()
	provider := "itest_parked_native"
	hash := submissionFixtureHash(provider)

	first, err := f.svc.BeginProviderSubmissionOwned(ctx, f.claimed.Ownership, provider, hash, execution.ResendOnUnknownSubmission)
	if err != nil {
		t.Fatalf("begin submission: %v", err)
	}
	if err := f.svc.MarkSubmissionStateOwned(ctx, f.claimed.Ownership, first, execution.SubmissionUnknown, "timeout"); err != nil {
		t.Fatalf("mark unknown: %v", err)
	}

	// The provider deduplicates on the stable key, so transmitting again is
	// allowed — as the SAME external action, not a new one.
	second, err := f.svc.BeginProviderSubmissionOwned(ctx, f.claimed.Ownership, provider, hash, execution.ResendOnUnknownSubmission)
	if err != nil {
		t.Fatalf("resend for a native-idempotent provider: %v (the capability is "+
			"declared, so an unconfirmed submit must be re-transmittable)", err)
	}
	if second.State != execution.SubmissionSending {
		t.Fatalf("state = %q, want %q", second.State, execution.SubmissionSending)
	}
	if second.SubmissionNo != first.SubmissionNo || second.IdempotencyKey != first.IdempotencyKey {
		t.Fatalf("a resend changed the external identity: no %d→%d key %q→%q — "+
			"the whole point of the stable key is that a resend is the same action",
			first.SubmissionNo, second.SubmissionNo, first.IdempotencyKey, second.IdempotencyKey)
	}
	if second.Attempt != 2 {
		t.Fatalf("attempt = %d, want 2 (a resend still costs an attempt)", second.Attempt)
	}
	var n int64
	if err := f.svc.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM provider_submissions WHERE run_id = ?`, f.runID.Bytes()).Scan(&n); err != nil {
		t.Fatalf("count submissions: %v", err)
	}
	if n != 1 {
		t.Fatalf("provider_submissions rows = %d, want 1 (a resend reuses the row)", n)
	}

	// …and the same capability is what makes it legal: with the ordinary
	// (at-most-once) policy the very same state still refuses.
	if err := f.svc.MarkSubmissionStateOwned(ctx, f.claimed.Ownership, second, execution.SubmissionUnknown, "timeout again"); err != nil {
		t.Fatalf("mark unknown: %v", err)
	}
	if _, err := f.svc.BeginProviderSubmissionOwned(ctx, f.claimed.Ownership, provider, hash, execution.ResendForbidden); !errors.Is(err, execution.ErrProviderSubmitUnknown) {
		t.Fatalf("resend without the capability: err=%v, want ErrProviderSubmitUnknown "+
			"(at-most-once is the default; only a declared capability relaxes it)", err)
	}
}

// TestResolveRunRequestWithWaitBridgesAnUncommittedReservation is the 第九轮
// P1 replay-availability property, against a REAL uncommitted transaction.
//
// A single read cannot close it: request A may be milliseconds from
// committing. So A holds the reservation inside an OPEN transaction (invisible
// to everyone else), and the losing concurrent request must nevertheless end
// up with A's run once A commits — instead of walking away into a 429.
func TestResolveRunRequestWithWaitBridgesAnUncommittedReservation(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	const userID = 90420099
	seedAdmissionUser(t, svc, userID)

	// The winner: created the ordinary way, so the row (and the FK the
	// reservation needs) is real.
	const reqID = "review9-wait"
	convID := int64(0)
	first, _, err := svc.CreateRunIdempotent(ctx, idempotentInputFor(userID, reqID, "one", convID), 5)
	if err != nil {
		t.Fatalf("seed first run: %v", err)
	}
	cleanupConversation(t, svc, conversationOf(t, svc, first))

	// A SECOND connection holds an UNCOMMITTED reservation for a DIFFERENT
	// run of the same identity — exactly what an in-flight duplicate looks
	// like from the loser's side.
	holder, err := svc.DB.Conn(ctx)
	if err != nil {
		t.Fatalf("holder conn: %v", err)
	}
	defer func() { _ = holder.Close() }()
	if _, err := holder.ExecContext(ctx,
		`DELETE FROM run_requests WHERE user_id = ? AND client_request_id = ?`, userID, reqID); err != nil {
		t.Fatalf("clear earlier reservation: %v", err)
	}
	secondRun, _, err := svc.CreateRunIdempotent(ctx, idempotentInputFor(userID, "review9-wait-2", "two", convID), 5)
	if err != nil {
		t.Fatalf("seed second run: %v", err)
	}
	cleanupConversation(t, svc, conversationOf(t, svc, secondRun))
	hash := execution.RunRequestHash(1, convID, "two", nil)
	// One reservation per run (uniq_run_requests_run), so the row the second
	// run created for its own id has to go before this fixture re-points the
	// identity at it.
	if _, err := holder.ExecContext(ctx, `DELETE FROM run_requests WHERE run_id = ?`, secondRun.ID.Bytes()); err != nil {
		t.Fatalf("clear the second run's own reservation: %v", err)
	}
	tx, err := holder.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin holder tx: %v", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO run_requests (user_id, client_request_id, run_id, request_hash)
		 VALUES (?, ?, ?, ?)`, userID, reqID, secondRun.ID.Bytes(), hash); err != nil {
		_ = tx.Rollback()
		t.Fatalf("insert uncommitted reservation: %v", err)
	}

	// Uncommitted ⇒ invisible: one plain read must NOT see it, otherwise
	// this test would not be exercising the wait at all.
	if _, found, err := svc.ResolveRunRequest(ctx, userID, reqID, hash); err != nil || found {
		_ = tx.Rollback()
		t.Fatalf("ResolveRunRequest found=%v err=%v while the reservation is uncommitted — "+
			"the fixture does not reproduce the race", found, err)
	}

	// The loser waits; the winner commits a moment later.
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(150 * time.Millisecond)
		_ = tx.Commit()
	}()

	run, found, err := svc.ResolveRunRequestWithWait(ctx, userID, reqID, hash, 3*time.Second)
	if err != nil {
		t.Fatalf("ResolveRunRequestWithWait: %v", err)
	}
	<-done
	if !found {
		t.Fatal("the loser never saw the winner's reservation: a duplicate would have been " +
			"rejected with 429 for a request that actually succeeded")
	}
	if run.ID != secondRun.ID {
		t.Fatalf("resolved run = %s, want %s (the ORIGINAL run of this identity)", run.ID, secondRun.ID)
	}
}

// TestCascadeDeleteRemovesRoundNineChildTables (第九轮 P1): hard-deleting a
// conversation must not leave run_requests or provider_submissions behind.
//
// Neither table is reachable by an FK cascade from `runs`, so omitting them
// leaves a permanent idempotency reservation pointing at a run that no longer
// exists: the next replay of that client_request_id resolves a row whose run
// was deleted and answers 500 forever.
func TestCascadeDeleteRemovesRoundNineChildTables(t *testing.T) {
	f := newParkFixture(t, "itest_cascade9")
	ctx := context.Background()
	provider := "itest_cascade9"
	hash := submissionFixtureHash(provider)

	sub, err := f.svc.BeginProviderSubmissionOwned(ctx, f.claimed.Ownership, provider, hash, execution.ResendForbidden)
	if err != nil {
		t.Fatalf("begin submission: %v", err)
	}
	// A definitive refusal, so no row is left in an unresolved state that a
	// later policy change could interpret.
	if err := f.svc.MarkSubmissionStateOwned(ctx, f.claimed.Ownership, sub, execution.SubmissionRejected, "400"); err != nil {
		t.Fatalf("mark rejected: %v", err)
	}
	if err := f.svc.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM provider_submissions WHERE run_id = ?`, f.runID.Bytes()).Scan(new(int64)); err != nil {
		t.Fatalf("probe provider_submissions: %v", err)
	}
	// The identity is a fixed string, so clear any earlier attempt's row
	// first: a leftover from a run whose cascade did NOT delete it is
	// indistinguishable from the row this test is about to create.
	if _, err := f.svc.DB.ExecContext(ctx,
		`DELETE FROM run_requests WHERE client_request_id = ?`, "review9-cascade"); err != nil {
		t.Fatalf("clear stale reservation: %v", err)
	}
	if _, err := f.svc.DB.ExecContext(ctx,
		`INSERT INTO run_requests (user_id, client_request_id, run_id, request_hash)
		 VALUES (42, ?, ?, ?) ON DUPLICATE KEY UPDATE run_id = run_id`,
		"review9-cascade", f.runID.Bytes(), hash); err != nil {
		t.Fatalf("seed run_requests: %v", err)
	}

	// A terminal run: the cascade refuses to delete live work.
	if err := f.svc.FailOwnedRun(ctx, f.claimed.Run, f.claimed.Ownership, "itest_done", "done"); err != nil {
		t.Fatalf("fail run: %v", err)
	}
	convID := f.convID
	if err := f.svc.DeleteConversationCascade(ctx, convID, 42); err != nil {
		t.Fatalf("DeleteConversationCascade: %v", err)
	}

	for _, tc := range []struct {
		table string
		stmt  string
	}{
		{"runs", `SELECT COUNT(*) FROM runs WHERE conversation_id = ?`},
		{"run_events", `SELECT COUNT(*) FROM run_events e JOIN runs r ON r.id = e.run_id WHERE r.conversation_id = ?`},
		{"provider_submissions", `SELECT COUNT(*) FROM provider_submissions p JOIN runs r ON r.id = p.run_id WHERE r.conversation_id = ?`},
		{"run_requests", `SELECT COUNT(*) FROM run_requests q JOIN runs r ON r.id = q.run_id WHERE r.conversation_id = ?`},
	} {
		var n int64
		if err := f.svc.DB.QueryRowContext(ctx, tc.stmt, convID).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", tc.table, err)
		}
		if n != 0 {
			t.Fatalf("%s rows after the cascade = %d, want 0 — a stale row here leaves a "+
				"client_request_id whose run no longer exists", tc.table, n)
		}
	}
	// The reservation row itself must be gone, not merely orphaned.
	var orphan int64
	if err := f.svc.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM run_requests WHERE client_request_id = ?`, "review9-cascade").Scan(&orphan); err != nil {
		t.Fatalf("count orphan reservations: %v", err)
	}
	if orphan != 0 {
		t.Fatalf("run_requests rows for 'review9-cascade' = %d, want 0: a replay of that id "+
			"would resolve a deleted run and answer 500", orphan)
	}
}

// TestParkedRunIsReleasedByTheGraceSweep closes the loop on waiting_external:
// parking is only safe if it is BOUNDED. A parked run is non-settled, so it
// blocks its conversation (409) and counts against the user's quota — an
// unbounded park would be a permanent zombie.
func TestParkedRunIsReleasedByTheGraceSweep(t *testing.T) {
	f := newParkFixture(t, "itest_parked_sweep")
	ctx := context.Background()
	provider := "itest_parked_sweep"

	if _, err := f.svc.BeginProviderSubmissionOwned(ctx, f.claimed.Ownership, provider, submissionFixtureHash(provider), execution.ResendForbidden); err != nil {
		t.Fatalf("begin submission: %v", err)
	}
	if err := f.svc.AwaitExternalOwned(ctx, f.claimed.Ownership, "provider submit outcome unknown"); err != nil {
		t.Fatalf("park run: %v", err)
	}

	run, err := f.svc.GetRun(ctx, f.runID)
	if err != nil {
		t.Fatalf("load parked run: %v", err)
	}
	if run.Status != execution.StatusWaitingExternal {
		t.Fatalf("status = %q, want %q", run.Status, execution.StatusWaitingExternal)
	}
	if n := countEvents(t, f.svc, f.runID, execution.EventRunWaitingExternal); n != 1 {
		t.Fatalf("run.waiting_external events = %d, want 1", n)
	}
	if leaseExists(t, f.svc, f.runID) {
		t.Fatal("a parked run must not keep its lease: the attempt is over")
	}
	if n := countActiveRunsInConversation(t, f.svc, f.convID); n != 1 {
		t.Fatalf("active runs in the conversation = %d, want 1 (a parked run is non-settled)", n)
	}

	// Inside the grace window OUR run must be untouched: the outcome may still
	// be resolvable, and resolving early would report a failure for a request
	// the provider may be executing right now.
	//
	// The assertion is on THIS run, not on the sweep's return count: the sweep
	// is a global batch operation over a SHARED dev database, so its count also
	// includes other tests' (and earlier rounds') fixtures — asserting on it
	// would make this test depend on unrelated state.
	svcGrace := *f.svc
	svcGrace.WaitingExternalGrace = time.Hour
	if _, err := svcGrace.ExpireParkedExternalRuns(ctx, 50); err != nil {
		t.Fatalf("sweep inside the grace: %v", err)
	}
	if run, err := f.svc.GetRun(ctx, f.runID); err != nil {
		t.Fatalf("reload after the in-grace sweep: %v", err)
	} else if run.Status != execution.StatusWaitingExternal {
		t.Fatalf("status after the in-grace sweep = %q, want %q (the grace window must hold)",
			run.Status, execution.StatusWaitingExternal)
	}

	// Backdate the park past the grace window (the row is written once when
	// it enters waiting_external, so updated_at IS the park instant).
	if _, err := f.svc.DB.ExecContext(ctx,
		`UPDATE runs SET updated_at = DATE_SUB(CURRENT_TIMESTAMP(3), INTERVAL 2 HOUR) WHERE id = ?`,
		f.runID.Bytes()); err != nil {
		t.Fatalf("backdate park: %v", err)
	}
	if _, err := svcGrace.ExpireParkedExternalRuns(ctx, 50); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	run, err = f.svc.GetRun(ctx, f.runID)
	if err != nil {
		t.Fatalf("reload run: %v", err)
	}
	if run.Status != execution.StatusFailed || run.ErrorCode != "provider_submit_unknown" {
		t.Fatalf("resolved status=%q code=%q, want failed/provider_submit_unknown", run.Status, run.ErrorCode)
	}
	if n := countEvents(t, f.svc, f.runID, execution.EventRunFailed); n != 1 {
		t.Fatalf("terminal run.failed events = %d, want exactly 1", n)
	}
	// The conversation is usable again — the point of bounding the park.
	if n := countActiveRunsInConversation(t, f.svc, f.convID); n != 0 {
		t.Fatalf("active runs after the sweep = %d, want 0", n)
	}
	if _, err := f.svc.CreateRun(ctx, &execution.CreateRunInput{
		UserID:         42,
		ApplicationID:  1,
		ConversationID: f.convID,
		Provider:       provider,
		RuntimeType:    "agent",
		ExecutionMode:  "interactive",
		Content:        "next turn after the park",
	}); err != nil {
		t.Fatalf("the conversation must accept a new turn after the sweep: %v", err)
	}
}

// TestSweptParkedRunStaysFailed: the sweep must not be able to overwrite a run
// that was resolved concurrently (a re-run of the sweep, or a cancel that won
// the race). Sweeping twice is the cheapest way to prove it.
func TestSweptParkedRunStaysFailed(t *testing.T) {
	f := newParkFixture(t, "itest_parked_idempotent")
	ctx := context.Background()
	provider := "itest_parked_idempotent"

	if _, err := f.svc.BeginProviderSubmissionOwned(ctx, f.claimed.Ownership, provider, submissionFixtureHash(provider), execution.ResendForbidden); err != nil {
		t.Fatalf("begin submission: %v", err)
	}
	if err := f.svc.AwaitExternalOwned(ctx, f.claimed.Ownership, "unknown"); err != nil {
		t.Fatalf("park run: %v", err)
	}
	if _, err := f.svc.DB.ExecContext(ctx,
		`UPDATE runs SET updated_at = DATE_SUB(CURRENT_TIMESTAMP(3), INTERVAL 2 HOUR) WHERE id = ?`,
		f.runID.Bytes()); err != nil {
		t.Fatalf("backdate park: %v", err)
	}
	svcGrace := *f.svc
	svcGrace.WaitingExternalGrace = time.Hour

	if n, err := svcGrace.ExpireParkedExternalRuns(ctx, 50); err != nil || n != 1 {
		t.Fatalf("first sweep: n=%d err=%v, want 1/nil", n, err)
	}
	if n, err := svcGrace.ExpireParkedExternalRuns(ctx, 50); err != nil || n != 0 {
		t.Fatalf("second sweep: n=%d err=%v, want 0/nil (already terminal)", n, err)
	}
	// The assertion that matters: OUR run was already terminal, so the second
	// sweep could not have re-resolved it to anything else.
	if run, err := f.svc.GetRun(ctx, f.runID); err != nil {
		t.Fatalf("reload run: %v", err)
	} else if run.Status != execution.StatusFailed {
		t.Fatalf("status = %q, want failed (a repeated sweep must not rewrite a resolved run)", run.Status)
	}
	if n := countEvents(t, f.svc, f.runID, execution.EventRunFailed); n != 1 {
		t.Fatalf("terminal run.failed events = %d, want exactly 1 (invariant D)", n)
	}
}

// ── P1-3: event sequence allocation and bounded reads ──

// TestEventSequenceAllocatorIsGapFreeMonotonicAndPerRun pins the properties the
// allocator must preserve while removing the O(history) COUNT(*)+1 scan.
func TestEventSequenceAllocatorIsGapFreeMonotonicAndPerRun(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	convID := seedConversation(t, svc)
	cleanupConversation(t, svc, convID)
	runA := seedRunWithConversation(t, svc, "itest_seq_a", convID)
	runB := seedRunWithConversation(t, svc, "itest_seq_b", convID)

	const n = 25
	for i := 0; i < n; i++ {
		if err := svc.AppendEvent(ctx, runA, execution.EventRunPoll, map[string]any{"i": i}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	events := listAllEvents(t, svc, runA)
	if len(events) != n {
		t.Fatalf("events = %d, want %d", len(events), n)
	}
	for i, ev := range events {
		if ev.Sequence != uint64(i+1) {
			t.Fatalf("event %d has sequence %d, want %d (gap or renumbering)", i, ev.Sequence, i+1)
		}
	}

	// The counter is the row's, and it must sit exactly one past the last
	// allocated value — the allocator's whole contract.
	var next uint64
	if err := svc.DB.QueryRowContext(ctx,
		`SELECT next_event_sequence FROM runs WHERE id = ?`, runA.Bytes()).Scan(&next); err != nil {
		t.Fatalf("read allocator: %v", err)
	}
	if next != n+1 {
		t.Fatalf("next_event_sequence = %d, want %d", next, n+1)
	}

	// A second run has its OWN counter: sequences are per-run, so one run's
	// history can never shift another's.
	other := listAllEvents(t, svc, runB)
	if len(other) != 0 {
		t.Fatalf("run B has %d events, want 0 (fixture leak)", len(other))
	}
	if err := svc.AppendEvent(ctx, runB, execution.EventRunPoll, map[string]any{"first": true}); err != nil {
		t.Fatalf("append to run B: %v", err)
	}
	other = listAllEvents(t, svc, runB)
	if len(other) != 1 || other[0].Sequence != 1 {
		t.Fatalf("run B's first event sequence = %v, want 1", other)
	}
}

// TestEventPageIsBoundedAndResumable: keyset paging must visit every event
// exactly once, and `has_more` must be honest at the boundary.
func TestEventPageIsBoundedAndResumable(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	convID := seedConversation(t, svc)
	cleanupConversation(t, svc, convID)
	runID := seedRunWithConversation(t, svc, "itest_page", convID)

	const total = 25
	for i := 0; i < total; i++ {
		if err := svc.AppendEvent(ctx, runID, execution.EventRunPoll, map[string]any{"i": i}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	seen := make([]uint64, 0, total)
	cursor := uint64(0)
	pages := 0
	for {
		page, err := svc.ListEventPage(ctx, runID, cursor, 10)
		if err != nil {
			t.Fatalf("page from %d: %v", cursor, err)
		}
		pages++
		if len(page.Items) > 10 {
			t.Fatalf("page returned %d items, want <= 10 (the limit must be enforced)", len(page.Items))
		}
		for _, ev := range page.Items {
			if ev.Sequence <= cursor {
				t.Fatalf("page from %d returned sequence %d", cursor, ev.Sequence)
			}
			seen = append(seen, ev.Sequence)
		}
		cursor = page.NextAfter
		if !page.HasMore {
			break
		}
		if pages > total {
			t.Fatal("paging did not terminate")
		}
	}
	if len(seen) != total {
		t.Fatalf("walked %d events, want %d", len(seen), total)
	}
	for i, seq := range seen {
		if seq != uint64(i+1) {
			t.Fatalf("walk visited sequence %d at position %d, want %d", seq, i, i+1)
		}
	}

	// An oversized limit is clamped, not honoured: the bound is a safety
	// property, so a caller cannot ask for the whole history.
	page, err := svc.ListEventPage(ctx, runID, 0, 1_000_000)
	if err != nil {
		t.Fatalf("oversized page: %v", err)
	}
	if len(page.Items) > execution.MaxEventPageSize {
		t.Fatalf("oversized page returned %d items, want <= %d", len(page.Items), execution.MaxEventPageSize)
	}
}

// ── helpers used by the P0-2 tests ──

func countActiveRunsInConversation(t *testing.T, svc *execution.Service, convID int64) int64 {
	t.Helper()
	var n int64
	if err := svc.DB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM runs WHERE conversation_id = ?
		  AND status NOT IN ('cancelled','succeeded','failed','interrupted')`, convID).Scan(&n); err != nil {
		t.Fatalf("count active runs: %v", err)
	}
	return n
}
