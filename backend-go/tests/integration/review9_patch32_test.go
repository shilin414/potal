// 第九轮补丁 3.2-B 端到端验证（复审报告 §五/§六/§七/§八）：
//
//	TestBackgroundSubmitSuccessWithoutChatIDParks
//	    POST 200 但响应体没有 agent_chat_id ⇒ unknown + waiting_external，
//	    绝不 poll、绝不假失败（与 streaming 首帧无 id 同一提交边界语义）。
//	TestBackgroundAcceptedPersistenceFailureStopsExecution
//	    Provider 已返回 chat id，但 MarkSubmissionAccepted 写库失败 ⇒
//	    不 poll、不 finalize、不重新 submit；lease 过期后 reaper 收敛为
//	    waiting_external。
//	TestStreamingAcceptedPersistenceFailureStopsTheStream
//	    streaming 同一写库失败 ⇒ 流消费立即停止，不得 finalize succeeded，
//	    不得产生第二次 OpenStreamChat。
//
// The persistence failure is injected with a REAL InnoDB condition (the
// repo's established recipe): a holder transaction locks the runs row
// FOR UPDATE while the tuned session (innodb_lock_wait_timeout = 1) tries to
// verify ownership inside MarkSubmissionAcceptedOwned — a deterministic 1205,
// no production failpoint.
package integration

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/catalog"
	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
)

// lockRunsRowDuringSubmit takes the runs-row lock INSIDE the provider call,
// i.e. after beginSubmit succeeded and before the acceptance write runs. The
// holder tx is rolled back by the returned release func (safe to call twice).
func lockRunsRowDuringSubmit(t *testing.T, f *ailySubmitFixture, runID [16]byte) (release func()) {
	t.Helper()
	holder, err := f.svc.DB.Conn(context.Background())
	if err != nil {
		t.Fatalf("holder conn: %v", err)
	}
	var holderTx *sql.Tx
	f.rec.onSubmit = func(kind string) {
		tx, err := holder.BeginTx(context.Background(), nil)
		if err != nil {
			t.Logf("holder begin tx: %v", err)
			return
		}
		var locked []byte
		if err := tx.QueryRowContext(context.Background(),
			`SELECT id FROM runs WHERE id = ? FOR UPDATE`, runID[:]).Scan(&locked); err != nil {
			t.Logf("lock runs row: %v", err)
			_ = tx.Rollback()
			return
		}
		holderTx = tx
	}
	once := false
	return func() {
		if once {
			return
		}
		once = true
		if holderTx != nil {
			_ = holderTx.Rollback()
		}
		_ = holder.Close()
	}
}

// TestBackgroundSubmitSuccessWithoutChatIDParks is the background mirror of
// TestStreamEofBeforeChatIdParksInsteadOfFailing (第九轮补丁 3.2-B §五).
//
// A StartChat that returns HTTP success WITHOUT an agent_chat_id has already
// crossed the submit boundary: the provider may have created the chat. The
// client layer refuses that shape (ErrServer ⇒ outcome unknown) and the
// executor parks the run instead of polling "" or declaring a failure.
func TestBackgroundSubmitSuccessWithoutChatIDParks(t *testing.T) {
	f := newAilySubmitFixture(t, "r932nochat", "background", "200 without chat id")
	ctx := context.Background()
	f.rec.emptyChatID = true

	if err := f.exec.Execute(ctx, f.claimed); err != nil {
		t.Fatalf("execute: %v", err)
	}

	starts, opens := f.rec.counted()
	if starts != 1 || opens != 0 {
		t.Fatalf("provider calls: StartChat=%d OpenStreamChat=%d, want 1/0 (exactly one submit)", starts, opens)
	}
	if n := f.rec.polled(); n != 0 {
		t.Fatalf("GetChatResult calls = %d, want 0 — an unknown identity must never be polled", n)
	}
	st := readRunState(t, f.env.db, f.runID)
	if st.status != execution.StatusWaitingExternal {
		t.Fatalf("status=%q, want waiting_external — the provider may hold this request", st.status)
	}
	if state, externalID := submissionOf(t, f); state != execution.SubmissionUnknown || externalID != "" {
		t.Fatalf("submission state=%q external=%q, want unknown/\"\"", state, externalID)
	}
	if n := countEvents(t, f.svc, f.runID, execution.EventRunRetrying); n != 0 {
		t.Fatalf("run.retrying events = %d, want 0 (parked ≠ requeued)", n)
	}
	if n := countEvents(t, f.svc, f.runID, execution.EventRunFailed); n != 0 {
		t.Fatalf("run.failed events = %d, want 0 — an empty chat id is not proof of failure", n)
	}
}

// TestBackgroundAcceptedPersistenceFailureStopsExecution (第九轮补丁 3.2-B §六
// background half): the provider answered chat-1, but the ONE transaction
// that records 'accepted' + external_run_id cannot commit. The executor must
// stop the whole provider execution chain — no poll, no finalize, no second
// submit — and the run later converges (lease expiry → reaper → new owner
// sees a 'sending' submission → waiting_external).
func TestBackgroundAcceptedPersistenceFailureStopsExecution(t *testing.T) {
	f := newAilySubmitFixture(t, "r932accbg", "background", "acceptance write fails")
	ctx := context.Background()

	// The acceptance write runs on a session with a 1s lock wait timeout;
	// the holder locks the runs row the moment the provider call starts.
	tuned := newSessionTunedService(t, "SET SESSION innodb_lock_wait_timeout = 1")
	exec := *f.exec
	exec.Owned = tuned.WorkerOwned()
	release := lockRunsRowDuringSubmit(t, f, f.runID)
	defer release()

	execErr := exec.Execute(ctx, f.claimed)
	release()

	if execErr == nil {
		t.Fatal("execute returned nil although the acceptance ledger could not be written — " +
			"the run would have polled an unknown chat")
	}
	starts, opens := f.rec.counted()
	if starts != 1 || opens != 0 {
		t.Fatalf("provider calls: StartChat=%d OpenStreamChat=%d, want 1/0 (never a second submit)", starts, opens)
	}
	if n := f.rec.polled(); n != 0 {
		t.Fatalf("GetChatResult calls = %d, want 0 — a run without a durable accepted identity must not poll", n)
	}
	st := readRunState(t, f.env.db, f.runID)
	if st.status == execution.StatusSucceeded || st.status == execution.StatusFailed || st.status == execution.StatusWaitingExternal {
		t.Fatalf("status=%q, want running — nobody is entitled to resolve a run whose provider "+
			"chat is real but unrecorded; only the lease/reaper path converges it", st.status)
	}
	if state, externalID := submissionOf(t, f); state != execution.SubmissionSending || externalID != "" {
		t.Fatalf("submission state=%q external=%q, want sending/\"\" (the acceptance never landed)", state, externalID)
	}

	// ── Recovery: the lease expires, the reaper requeues, a new owner parks. ──
	expireLease(t, f.svc, f.runID, "itest-r932accbg")
	_ = recoverRun(t, f.svc, f.runID)
	claimedB, won, err := f.svc.ClaimRun(ctx, f.runID, "worker-r932accbg", time.Minute)
	if err != nil || !won {
		t.Fatalf("reclaim: won=%v err=%v", won, err)
	}
	if err := f.exec.Execute(ctx, claimedB); err != nil {
		t.Fatalf("reclaim execute: %v", err)
	}
	starts, opens = f.rec.counted()
	if starts != 1 || opens != 0 {
		t.Fatalf("provider calls after reclaim: StartChat=%d OpenStreamChat=%d, want 1/0 — "+
			"the 'sending' submission must park, not resubmit", starts, opens)
	}
	st = readRunState(t, f.env.db, f.runID)
	if st.status != execution.StatusWaitingExternal {
		t.Fatalf("status after reclaim=%q, want waiting_external", st.status)
	}
}

// TestStreamingAcceptedPersistenceFailureStopsTheStream (第九轮补丁 3.2-B §六
// streaming half): stream.started carries chat-1, the acceptance write fails,
// and the stream consumption must stop IMMEDIATELY — a later succeeded
// reconciliation would otherwise finalize a run whose submission ledger still
// says 'sending' with no external id.
func TestStreamingAcceptedPersistenceFailureStopsTheStream(t *testing.T) {
	f := newAilySubmitFixture(t, "r932accst", "interactive", "stream acceptance write fails")
	ctx := context.Background()

	tuned := newSessionTunedService(t, "SET SESSION innodb_lock_wait_timeout = 1")
	exec := *f.exec
	exec.Owned = tuned.WorkerOwned()
	release := lockRunsRowDuringSubmit(t, f, f.runID)
	defer release()

	execErr := exec.Execute(ctx, f.claimed)
	release()

	if execErr == nil {
		t.Fatal("execute returned nil although the acceptance ledger could not be written")
	}
	starts, opens := f.rec.counted()
	if starts != 0 || opens != 1 {
		t.Fatalf("provider calls: StartChat=%d OpenStreamChat=%d, want 0/1 (exactly one submit)", starts, opens)
	}
	if n := f.rec.polled(); n != 0 {
		t.Fatalf("GetChatResult calls = %d, want 0 — the stream must not reconcile after the "+
			"acceptance ledger failed", n)
	}
	st := readRunState(t, f.env.db, f.runID)
	if st.status != execution.StatusRunning {
		t.Fatalf("status=%q, want running — the run stays running until the lease expires and "+
			"the reaper hands it over (never finalized on an unrecorded acceptance)", st.status)
	}
	if state, externalID := submissionOf(t, f); state != execution.SubmissionSending || externalID != "" {
		t.Fatalf("submission state=%q external=%q, want sending/\"\"", state, externalID)
	}
	if n := countEvents(t, f.svc, f.runID, execution.EventRunFailed); n != 0 {
		t.Fatalf("run.failed events = %d, want 0", n)
	}
	if n := countEvents(t, f.svc, f.runID, execution.EventRunRetrying); n != 0 {
		t.Fatalf("run.retrying events = %d, want 0 (the provider already holds the action)", n)
	}
}

// ── 第九轮补丁 3.2-C: replay-before-error at every post-miss refusal ──────

// TestReplayAfterAttachmentClaimRaceServesTheOriginalRun is 复审 §9.1 against
// a real transaction: the winner's run claimed the attachment and committed;
// the retry's initial resolve ran BEFORE that commit (reproduced here with an
// uncommitted reservation) and therefore missed. The retry then fails
// attachment validation — for an attachment ITS OWN earlier attempt already
// consumed — and the bounded replay resolution must hand back the original
// run instead of the 400.
func TestReplayAfterAttachmentClaimRaceServesTheOriginalRun(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	const userID = 90420303
	seedAdmissionUser(t, svc, userID)
	const reqID = "review932-attach"

	// The attachment the request carries, pending and unbound.
	attID := ids.New()
	if _, err := svc.DB.ExecContext(ctx,
		`INSERT INTO runtime_attachments (id, run_id, conversation_id, provider,
		 external_attachment_id, attachment_type, name, source_type, source_url,
		 storage_key, content_type, size_bytes, auth_mode, auth_subject_key, status, metadata, created_by)
		 VALUES (?, NULL, NULL, 'itest_idem', '', 'file', 'itest.pdf', 'upload', '',
		         '', '', 0, 'user', ?, 'pending', NULL, ?)`,
		attID.Bytes(), fmt.Sprintf("%d", userID), userID); err != nil {
		t.Fatalf("seed attachment: %v", err)
	}
	t.Cleanup(func() {
		_, _ = svc.DB.ExecContext(context.Background(), `DELETE FROM runtime_attachments WHERE id = ?`, attID.Bytes())
	})

	in := idempotentInputFor(userID, reqID, "with attachment", 0)
	in.AttachmentIDs = []string{attID.String()}
	in.RequestHash = execution.RunRequestHash(in.ApplicationID, 0, in.Content, in.AttachmentIDs)

	// The winner: its creation transaction claims the attachment and takes
	// the reservation, all committed.
	winner, _, err := svc.CreateRunIdempotent(ctx, in, 5)
	if err != nil {
		t.Fatalf("winner submit: %v", err)
	}
	cleanupConversation(t, svc, conversationOf(t, svc, winner))

	// Re-stage the reservation as UNCOMMITTED: what the loser's initial
	// resolve sees before the winner's transaction commits.
	holder, err := svc.DB.Conn(ctx)
	if err != nil {
		t.Fatalf("holder conn: %v", err)
	}
	defer func() { _ = holder.Close() }()
	if _, err := holder.ExecContext(ctx,
		`DELETE FROM run_requests WHERE user_id = ? AND client_request_id = ?`, userID, reqID); err != nil {
		t.Fatalf("clear committed reservation: %v", err)
	}
	tx, err := holder.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("holder tx: %v", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO run_requests (user_id, client_request_id, run_id, request_hash)
		 VALUES (?, ?, ?, ?)`, userID, reqID, winner.ID.Bytes(), in.RequestHash); err != nil {
		_ = tx.Rollback()
		t.Fatalf("insert uncommitted reservation: %v", err)
	}

	// The loser's initial resolve MISSES — otherwise this test would not be
	// exercising the post-refusal replay at all.
	if _, found, err := svc.ResolveRunRequest(ctx, userID, reqID, in.RequestHash); err != nil || found {
		_ = tx.Rollback()
		t.Fatalf("initial resolve found=%v err=%v while the reservation is uncommitted", found, err)
	}

	// The retry fails attachment validation for real: the winner (committed)
	// owns the attachment now.
	var claimedRun []byte
	if err := svc.DB.QueryRowContext(ctx,
		`SELECT run_id FROM runtime_attachments WHERE id = ?`, attID.Bytes()).Scan(&claimedRun); err != nil {
		_ = tx.Rollback()
		t.Fatalf("read attachment claim: %v", err)
	}
	if string(claimedRun) != string(winner.ID.Bytes()) {
		_ = tx.Rollback()
		t.Fatalf("attachment run_id = %x, want the winner's (the refusal the retry hits must be real)", claimedRun)
	}

	// The refusal is interrupted by the bounded replay; the winner commits
	// mid-wait, exactly as an in-flight duplicate would.
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(150 * time.Millisecond)
		_ = tx.Commit()
	}()
	run, found, err := svc.ResolveRunRequestWithWait(ctx, userID, reqID, in.RequestHash, 3*time.Second)
	<-done
	if err != nil || !found {
		t.Fatalf("replay resolution found=%v err=%v — a duplicate refused for its own "+
			"earlier attempt's attachment would be told 400 instead of 200", found, err)
	}
	if run.ID != winner.ID {
		t.Fatalf("resolved run = %s, want the original %s", run.ID, winner.ID)
	}
}

// TestReplayAfterAuthorizationRaceServesTheOriginalRun is 复审 §9.2: the app
// was disabled WHILE the original request was committing. The retry's initial
// resolve misses, its execution authorization is refused — and the bounded
// replay must still answer with the original run, because the reservation
// proves the request was already served by an attempt that WAS authorized.
func TestReplayAfterAuthorizationRaceServesTheOriginalRun(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	const userID = 90420404
	seedAdmissionUser(t, svc, userID)
	const reqID = "review932-auth"

	_, appID, _ := gateFixture(t, svc.DB, "r932auth")
	in := idempotentInputFor(userID, reqID, "one", 0)
	in.ApplicationID = appID
	in.Provider = "itest_gate_r932auth"
	in.RequestHash = execution.RunRequestHash(appID, 0, in.Content, nil)

	winner, _, err := svc.CreateRunIdempotent(ctx, in, 5)
	if err != nil {
		t.Fatalf("winner submit: %v", err)
	}
	cleanupConversation(t, svc, conversationOf(t, svc, winner))

	// The admin disables the application mid-flight.
	if _, err := svc.DB.ExecContext(ctx, `UPDATE applications SET enabled = 0 WHERE id = ?`, appID); err != nil {
		t.Fatalf("disable application: %v", err)
	}

	holder, err := svc.DB.Conn(ctx)
	if err != nil {
		t.Fatalf("holder conn: %v", err)
	}
	defer func() { _ = holder.Close() }()
	if _, err := holder.ExecContext(ctx,
		`DELETE FROM run_requests WHERE user_id = ? AND client_request_id = ?`, userID, reqID); err != nil {
		t.Fatalf("clear committed reservation: %v", err)
	}
	tx, err := holder.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("holder tx: %v", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO run_requests (user_id, client_request_id, run_id, request_hash)
		 VALUES (?, ?, ?, ?)`, userID, reqID, winner.ID.Bytes(), in.RequestHash); err != nil {
		_ = tx.Rollback()
		t.Fatalf("insert uncommitted reservation: %v", err)
	}

	// The loser's initial resolve misses (uncommitted), and its execution
	// authorization is REFUSED — the refusal the handler would hit before
	// the replay attempt.
	if _, found, err := svc.ResolveRunRequest(ctx, userID, reqID, in.RequestHash); err != nil || found {
		_ = tx.Rollback()
		t.Fatalf("initial resolve found=%v err=%v while the reservation is uncommitted", found, err)
	}
	cat := &catalog.Service{DB: svc.DB}
	if _, aerr := cat.AuthorizeExecution(ctx, appID, userID, false); aerr == nil {
		_ = tx.Rollback()
		t.Fatal("the disabled application was authorized — the fixture does not reproduce the refusal")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(150 * time.Millisecond)
		_ = tx.Commit()
	}()
	run, found, err := svc.ResolveRunRequestWithWait(ctx, userID, reqID, in.RequestHash, 3*time.Second)
	<-done
	if err != nil || !found {
		t.Fatalf("replay resolution found=%v err=%v — a duplicate refused by a disable that "+
			"raced its own earlier attempt would be told 409/404 instead of 200", found, err)
	}
	if run.ID != winner.ID {
		t.Fatalf("resolved run = %s, want the original %s", run.ID, winner.ID)
	}
}
