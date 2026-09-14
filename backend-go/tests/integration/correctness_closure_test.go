// Execution Correctness Closure integration tests (修复计划 §52-65 测试矩阵).
//
// These prove the closure invariants on real TiDB/Redis (opt-in via
// STUDIO_TEST_TIDB=1 / STUDIO_TEST_REDIS=1):
//
//	T1  reaper recovery is atomic — never running+no-lease
//	T2  a stale worker's retry is fenced out (no requeue, no outbox, no lease drop)
//	T3  run.retrying is a NON-terminal event
//	T4  run data refresh never drops the ownership (background path)
//	T6  stale worker cannot persist artifacts or their events
//	T7  artifact re-discovery is idempotent with a stable local id
//	T8  stale worker cannot overwrite the new owner's provider session
//	T9/T10 finalize is one transaction: terminal + event + message + lease
//	T11 dual-scheduler pending admission never runs parallel
//	T12 non-staff local accounts are rejected by the admin login
package integration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/execution"
	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/identity"
	"github.com/creation-agent-studio/backend-go/internal/platform/crypto"
	"github.com/creation-agent-studio/backend-go/internal/platform/dbtypes"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
)

// countEvents counts persisted run events of one type.
func countEvents(t *testing.T, svc *execution.Service, runID ids.ID, eventType string) int {
	t.Helper()
	events, err := svc.ListEventsAfter(context.Background(), runID, 0)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	n := 0
	for _, ev := range events {
		if ev.EventType == eventType {
			n++
		}
	}
	return n
}

// leaseExists reports whether the run currently holds a lease row.
func leaseExists(t *testing.T, svc *execution.Service, runID ids.ID) bool {
	t.Helper()
	_, err := svc.Querier().GetLease(context.Background(), runID.Bytes())
	return err == nil
}

// seedConversation inserts a conversation row for assistant-message tests.
func seedConversation(t *testing.T, svc *execution.Service) int64 {
	t.Helper()
	res, err := svc.Querier().CreateConversation(context.Background(), db.CreateConversationParams{
		UserID:        42,
		ApplicationID: sql.NullInt64{Int64: 1, Valid: true},
		Title:         "closure-test",
	})
	if err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// seedRunWithConversation seeds a queued run pinned to a conversation.
func seedRunWithConversation(t *testing.T, svc *execution.Service, provider string, convID int64) ids.ID {
	t.Helper()
	ctx := context.Background()
	runID := ids.New()
	inputJSON := []byte(`{"content":[{"type":"text","text":"t"}],"mode":"background"}`)
	snapshotJSON := []byte(`{"external_resource_id":"agent_test"}`)
	if _, err := svc.Querier().CreateRun(ctx, db.CreateRunParams{
		ID:              runID.Bytes(),
		ConversationID:  sql.NullInt64{Int64: convID, Valid: true},
		Provider:        provider,
		RuntimeType:     "agent",
		Input:           dbtypes.JSONText(inputJSON),
		RuntimeSnapshot: dbtypes.JSONText(snapshotJSON),
		MaxAttempts:     3,
	}); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	return runID
}

// ── T1: reaper recovery atomicity ──

// TestReaperRecoveryAtomicRetry: after a lease expiry the recovery
// transaction leaves a COMPLETE state — queued + run.retrying event +
// dispatch outbox + NO lease. The intermediate "running + no lease"
// orphan window of the old three-step reaper is structurally gone.
func TestReaperRecoveryAtomicRetry(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	runID := seedRun(t, svc, "itest_closure")

	if _, won, err := svc.ClaimRun(ctx, runID, "dead-worker", time.Minute); err != nil || !won {
		t.Fatalf("claim: won=%v err=%v", won, err)
	}
	if _, err := svc.Querier().HeartbeatLease(ctx, dbForceExpireParams(runID.Bytes())); err != nil {
		t.Fatalf("force expire: %v", err)
	}

	recoverRun(t, svc, runID)

	run, err := svc.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != execution.StatusQueued {
		t.Fatalf("status=%s, want queued", run.Status)
	}
	if leaseExists(t, svc, runID) {
		t.Fatal("lease survived recovery — queued runs must not hold leases")
	}
	if n := countEvents(t, svc, runID, execution.EventRunRetrying); n != 1 {
		t.Fatalf("run.retrying events = %d, want exactly 1", n)
	}
	// Dispatch outbox must exist in the same transaction: no orphan queued
	// run without a wake-up. (A live outbox relay on the shared dev
	// environment may already have marked it published — the row must
	// exist, its dispatch state is the relay's business.)
	var n int64
	if err := svc.DB.QueryRow(
		`SELECT COUNT(*) FROM outbox_events WHERE aggregate = 'run' AND aggregate_id = ? AND event_type = 'run.dispatch'`,
		runID.Bytes()).Scan(&n); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if n != 1 {
		t.Fatalf("run.dispatch outbox rows = %d, want 1 (atomic re-dispatch)", n)
	}
}

// TestReaperRecoveryAtomicFail: attempts exhausted → the recovery
// transaction fails the run AND writes its terminal run.failed event in
// the same commit (invariant D: terminal run ⇒ terminal event).
func TestReaperRecoveryAtomicFail(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	runID := seedRun(t, svc, "itest_closure")

	for i := 0; i < 3; i++ { // max_attempts = 3
		claimed, won, err := svc.ClaimRun(ctx, runID, "dead-worker", time.Minute)
		if err != nil || !won {
			t.Fatalf("claim %d: won=%v err=%v", i, won, err)
		}
		// attempt counts PROVIDER EXECUTIONS (P0-2): the claim alone no
		// longer burns retry budget, so the dead worker must have reached
		// the provider — exactly what the executor does right before the
		// submit.
		if _, err := svc.BeginProviderAttemptOwned(ctx, claimed.Ownership); err != nil {
			t.Fatalf("begin provider attempt %d: %v", i, err)
		}
		if _, err := svc.Querier().HeartbeatLease(ctx, dbForceExpireParams(runID.Bytes())); err != nil {
			t.Fatalf("force expire %d: %v", i, err)
		}
		recoverRun(t, svc, runID)
	}
	run, err := svc.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != execution.StatusFailed || run.ErrorCode != "lease_expired" {
		t.Fatalf("status=%s code=%s, want failed/lease_expired", run.Status, run.ErrorCode)
	}
	if leaseExists(t, svc, runID) {
		t.Fatal("terminal run must not hold a lease (invariant C)")
	}
	if n := countEvents(t, svc, runID, execution.EventRunFailed); n != 1 {
		t.Fatalf("run.failed events = %d, want exactly 1 (terminal event durability)", n)
	}
	if n := countEvents(t, svc, runID, execution.EventRunInterrupted); n != 0 {
		t.Fatalf("legacy run.interrupted events = %d, want 0", n)
	}
}

// ── T2: stale worker retry fenced ──

func TestStaleWorkerRetryFenced(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	runID := seedRun(t, svc, "itest_closure")

	claimedA, _, err := svc.ClaimRun(ctx, runID, "worker-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// A's lease lapses; reaper requeues; B takes over.
	expireLease(t, svc, runID, "worker-a")
	recoverRun(t, svc, runID)
	claimedB, _, err := svc.ClaimRun(ctx, runID, "worker-b", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	retryingBefore := countEvents(t, svc, runID, execution.EventRunRetrying)

	// A wakes up holding a 429 and calls the owned retry.
	if err := svc.RetryOwnedRun(ctx, claimedA.Run, claimedA.Ownership, "aily_rate_limit"); !errors.Is(err, execution.ErrLostOwnership) {
		t.Fatalf("stale retry: err=%v, want ErrLostOwnership", err)
	}
	run, _ := svc.GetRun(ctx, runID)
	if run.Status != execution.StatusRunning {
		t.Fatalf("stale A requeued B's run: status=%s", run.Status)
	}
	if !leaseExists(t, svc, runID) {
		t.Fatal("stale A deleted B's lease")
	}
	if got := countEvents(t, svc, runID, execution.EventRunRetrying); got != retryingBefore {
		t.Fatalf("stale A appended run.retrying events (before=%d after=%d)", retryingBefore, got)
	}
	// B's own retry must work.
	if err := svc.RetryOwnedRun(ctx, claimedB.Run, claimedB.Ownership, "aily_rate_limit"); err != nil {
		t.Fatalf("owner retry: %v", err)
	}
	run, _ = svc.GetRun(ctx, runID)
	if run.Status != execution.StatusQueued {
		t.Fatalf("owner retry status=%s, want queued", run.Status)
	}
	if leaseExists(t, svc, runID) {
		t.Fatal("owner retry must drop its own lease")
	}
}

// ── T3: run.retrying is non-terminal ──

func TestRetryEventNonTerminal(t *testing.T) {
	if execution.IsTerminalEventName(execution.EventRunRetrying) {
		t.Fatal("run.retrying must NOT be a terminal event")
	}
	if !execution.IsTerminalEventName(execution.EventRunCompleted) ||
		!execution.IsTerminalEventName(execution.EventRunFailed) ||
		!execution.IsTerminalEventName(execution.EventRunCancelled) {
		t.Fatal("completed/failed/cancelled must stay terminal")
	}
	// The legacy interrupted event no longer closes live streams.
	if execution.IsTerminalEventName(execution.EventRunInterrupted) {
		t.Fatal("legacy run.interrupted must not be classified terminal anymore")
	}
}

// ── T4: refresh never drops ownership (background path shape) ──

func TestBackgroundRefreshPreservesOwnership(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	runID := seedRun(t, svc, "itest_closure")

	claimed, _, err := svc.ClaimRun(ctx, runID, "bg-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// Background executor shape: GetRun refresh → RefreshRun on the claim.
	refreshed, err := svc.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	claimed.RefreshRun(refreshed)
	if claimed.Ownership.LeaseToken != claimed.Ownership.LeaseToken || claimed.Ownership.LeaseEpoch == 0 {
		t.Fatal("ownership mutated by refresh")
	}
	// Finalize must still clean the lease (the old bug left it behind
	// because the refreshed run carried a zero token).
	if err := svc.FinalizeOwnedRun(ctx, claimed.Run, claimed.Ownership, &execution.FinishInput{
		Status: execution.StatusSucceeded,
		Output: map[string]any{"text": "done"},
	}); err != nil {
		t.Fatalf("finalize after refresh: %v", err)
	}
	if leaseExists(t, svc, runID) {
		t.Fatal("lease survived finalize after DB refresh — the token was dropped")
	}
}

// ── T6: stale worker artifact write fenced ──

func TestArtifactFencing(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	runID := seedRun(t, svc, "itest_closure")

	claimedA, _, err := svc.ClaimRun(ctx, runID, "worker-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// A discovers the artifact once (as the owner).
	if _, err := svc.PersistArtifactOwned(ctx, claimedA.Ownership, execution.ArtifactInput{
		ExternalID:   "file_ext_1",
		Provider:     "feishu_aily",
		ProviderType: "sandbox_file",
		Name:         "a.txt",
	}); err != nil {
		t.Fatalf("owner persist artifact: %v", err)
	}
	// A's lease lapses; B takes over.
	expireLease(t, svc, runID, "worker-a")
	recoverRun(t, svc, runID)
	if _, _, err := svc.ClaimRun(ctx, runID, "worker-b", time.Minute); err != nil {
		t.Fatal(err)
	}

	// A's late discovery must be fenced out entirely.
	if _, err := svc.PersistArtifactOwned(ctx, claimedA.Ownership, execution.ArtifactInput{
		ExternalID: "file_ext_2",
		Provider:   "feishu_aily",
		Name:       "stale.txt",
	}); !errors.Is(err, execution.ErrLostOwnership) {
		t.Fatalf("stale artifact persist: err=%v, want ErrLostOwnership", err)
	}
	arts, err := svc.Querier().ListRunArtifacts(ctx, runID.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(arts) != 1 {
		t.Fatalf("artifacts = %d, want exactly 1 (stale write blocked)", len(arts))
	}
	events := 0
	for _, ev := range mustEvents(t, svc, runID) {
		if ev == execution.EventArtifactDiscovered {
			events++
		}
	}
	if events != 1 {
		t.Fatalf("artifact.discovered events = %d, want 1", events)
	}
}

// mustEvents lists event type names.
func mustEvents(t *testing.T, svc *execution.Service, runID ids.ID) []string {
	t.Helper()
	evs, err := svc.ListEventsAfter(context.Background(), runID, 0)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	out := make([]string, 0, len(evs))
	for _, ev := range evs {
		out = append(out, ev.EventType)
	}
	return out
}

// TestArtifactRediscoveryStableID: the same external artifact discovered
// twice by the SAME owner yields one row and one stable local id.
func TestArtifactRediscoveryStableID(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	runID := seedRun(t, svc, "itest_closure")

	claimed, _, err := svc.ClaimRun(ctx, runID, "owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	id1, err := svc.PersistArtifactOwned(ctx, claimed.Ownership, execution.ArtifactInput{
		ExternalID: "same_ext", Provider: "feishu_aily", Name: "x", NormalizedType: "file",
	})
	if err != nil {
		t.Fatal(err)
	}
	id2, err := svc.PersistArtifactOwned(ctx, claimed.Ownership, execution.ArtifactInput{
		ExternalID: "same_ext", Provider: "feishu_aily", Name: "x", NormalizedType: "file",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("re-discovery ids differ: %s != %s (unstable local artifact id)", id1, id2)
	}
	arts, err := svc.Querier().ListRunArtifacts(ctx, runID.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(arts) != 1 {
		t.Fatalf("artifact rows = %d, want 1", len(arts))
	}
}

// ── T8: provider session fencing ──

func TestProviderSessionFence(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	convID := seedConversation(t, svc)
	runID := seedRunWithConversation(t, svc, "feishu_aily", convID)

	// Thread for the conversation.
	threadID := ids.New()
	if _, err := svc.Querier().CreateAgentThread(ctx, db.CreateAgentThreadParams{
		ID: threadID.Bytes(), ConversationID: uint64(convID),
		Provider: "feishu_aily", AuthMode: "user", AuthSubjectKey: "42",
	}); err != nil {
		t.Fatal(err)
	}

	claimedA, _, err := svc.ClaimRun(ctx, runID, "worker-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// A binds session_A first (as the owner).
	if err := svc.BindProviderSessionOwned(ctx, claimedA.Ownership, threadID, "session_A"); err != nil {
		t.Fatalf("owner bind: %v", err)
	}
	// Idempotent re-bind of the same session.
	if err := svc.BindProviderSessionOwned(ctx, claimedA.Ownership, threadID, "session_A"); err != nil {
		t.Fatalf("idempotent re-bind: %v", err)
	}

	// A loses the run; B takes over and binds session_B.
	expireLease(t, svc, runID, "worker-a")
	recoverRun(t, svc, runID)
	claimedB, _, err := svc.ClaimRun(ctx, runID, "worker-b", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.BindProviderSessionOwned(ctx, claimedB.Ownership, threadID, "session_B"); err != nil {
		t.Fatalf("new owner bind: %v", err)
	}

	// A's late bind must be fenced out (ownership lost), never overwrite.
	if err := svc.BindProviderSessionOwned(ctx, claimedA.Ownership, threadID, "session_A2"); !errors.Is(err, execution.ErrLostOwnership) {
		t.Fatalf("stale bind: err=%v, want ErrLostOwnership", err)
	}
	row, err := svc.Querier().GetAgentThreadByConversation(ctx, uint64(convID))
	if err != nil {
		t.Fatal(err)
	}
	if row.RemoteID != "session_B" {
		t.Fatalf("remote_id = %q, want session_B (stale worker overwrote it)", row.RemoteID)
	}

	// The CURRENT owner may rebind a NEW session (the old one belongs to a
	// dead attempt); only stale workers are fenced out.
	if err := svc.BindProviderSessionOwned(ctx, claimedB.Ownership, threadID, "session_C"); err != nil {
		t.Fatalf("owner rebind: %v", err)
	}
	row, err = svc.Querier().GetAgentThreadByConversation(ctx, uint64(convID))
	if err != nil {
		t.Fatal(err)
	}
	if row.RemoteID != "session_C" {
		t.Fatalf("remote_id after owner rebind = %q, want session_C", row.RemoteID)
	}
}

// ── T9/T10: finalize transaction durability ──

func TestFinalizeTransactionDurability(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	convID := seedConversation(t, svc)
	runID := seedRunWithConversation(t, svc, "feishu_aily", convID)

	claimed, _, err := svc.ClaimRun(ctx, runID, "finalizer", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	err = svc.FinalizeOwnedRun(ctx, claimed.Run, claimed.Ownership, &execution.FinishInput{
		Status:            execution.StatusSucceeded,
		Output:            map[string]any{"text": "最终回答"},
		ProviderStatus:    "Completed",
		FinishReason:      "stop",
		AssistantText:     "最终回答",
		AssistantMetadata: mustMeta(runID),
	})
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}

	// Terminal state + exactly one terminal event + assistant message +
	// no lease — all from one transaction.
	run, _ := svc.GetRun(ctx, runID)
	if run.Status != execution.StatusSucceeded {
		t.Fatalf("status=%s, want succeeded", run.Status)
	}
	if n := countEvents(t, svc, runID, execution.EventRunCompleted); n != 1 {
		t.Fatalf("run.completed events = %d, want exactly 1", n)
	}
	if leaseExists(t, svc, runID) {
		t.Fatal("terminal run kept its lease (invariant C)")
	}
	var msgN int64
	if err := svc.DB.QueryRow(
		`SELECT COUNT(*) FROM messages WHERE conversation_id = ? AND role = 'assistant'`,
		convID).Scan(&msgN); err != nil {
		t.Fatal(err)
	}
	if msgN != 1 {
		t.Fatalf("assistant messages = %d, want 1 (answer must be durable with the run)", msgN)
	}

	// Idempotent re-finalize: no second terminal event, no second message.
	if err := svc.FinalizeOwnedRun(ctx, claimed.Run, claimed.Ownership, &execution.FinishInput{
		Status:        execution.StatusSucceeded,
		Output:        map[string]any{"text": "最终回答"},
		AssistantText: "最终回答",
	}); err != nil {
		t.Fatalf("re-finalize: %v", err)
	}
	if n := countEvents(t, svc, runID, execution.EventRunCompleted); n != 1 {
		t.Fatalf("run.completed events after re-finalize = %d, want still exactly 1", n)
	}
	if err := svc.DB.QueryRow(
		`SELECT COUNT(*) FROM messages WHERE conversation_id = ? AND role = 'assistant'`,
		convID).Scan(&msgN); err != nil {
		t.Fatal(err)
	}
	if msgN != 1 {
		t.Fatalf("assistant messages after re-finalize = %d, want still 1", msgN)
	}
}

// ── T11: dual-scheduler pending admission ──

func TestDualSchedulerPendingAdmissionNoParallel(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()
	due := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	schedID := env.seedSchedule(t, due, "queue")

	// First manual trigger creates the active occurrence; the second must
	// queue behind it as PENDING (overlap=queue semantics).
	if _, err := env.schd.TriggerNow(ctx, schedID, 42, true); err != nil {
		t.Fatalf("run-now #1: %v", err)
	}
	occ2, err := env.schd.TriggerNow(ctx, schedID, 42, true)
	if err != nil {
		t.Fatalf("run-now #2: %v", err)
	}
	if occ2 == nil {
		t.Fatal("second run-now did not queue behind the active occurrence")
	}

	// Fail the active execution (run + occurrence) so the pendings become
	// admissible, then let two schedulers race to admit them. With the
	// schedules-row admission lock, at most ONE occurrence may be active.
	if _, err := env.db.Exec(
		// run_id is BINARY(16) holding the raw 16 bytes, so it can be
		// compared to runs.id directly. Wrapping it in UNHEX() — as this
		// test used to — works on TiDB but is a hard error on MySQL 5.7
		// (Error 1411: Incorrect string value for function unhex), which
		// is exactly the engine the mysql57 gate runs.
		`UPDATE runs SET status='failed', error_code='killed', finished_at=CURRENT_TIMESTAMP(3)
		 WHERE id IN (SELECT run_id FROM schedule_occurrences WHERE schedule_id = ? AND run_id IS NOT NULL)`,
		schedID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.Exec(
		`UPDATE schedule_occurrences SET status='failed', finished_at=CURRENT_TIMESTAMP(3)
		 WHERE schedule_id = ? AND status IN ('queued','running')`, schedID); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			env.schd.ProcessDue(ctx)
		}()
	}
	wg.Wait()

	if n := env.count(t,
		`SELECT COUNT(*) FROM schedule_occurrences WHERE schedule_id = ? AND status IN ('queued','running')`,
		schedID); n > 1 {
		t.Fatalf("overlap=queue schedule has %d active occurrences after dual admission, want <= 1", n)
	}
}

// ── T12: non-staff local account rejected ──

func TestAdminLoginRejectsNonStaff(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()

	hash, err := crypto.HashPassword("correct-horse")
	if err != nil {
		t.Fatal(err)
	}
	// Non-staff user WITH a valid password: must never pass admin login.
	if _, err := svc.Querier().CreateUser(ctx, db.CreateUserParams{
		Username:     fmt.Sprintf("nonstaff_%d", time.Now().UnixNano()),
		PasswordHash: hash,
		Role:         "creator",
		AuthSource:   "local_admin",
		IsStaff:      false,
	}); err != nil {
		t.Fatal(err)
	}
	repo := identity.NewRepo(svc.DB)
	if _, err := repo.VerifyLocalAdmin(ctx, userOf(t, svc, false), "correct-horse"); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("non-staff login: err=%v, want ErrNotFound", err)
	}
	// Staff user with the same password passes.
	staffName := fmt.Sprintf("staff_%d", time.Now().UnixNano())
	if _, err := svc.Querier().CreateUser(ctx, db.CreateUserParams{
		Username:     staffName,
		PasswordHash: hash,
		Role:         "admin",
		AuthSource:   "local_admin",
		IsStaff:      true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.VerifyLocalAdmin(ctx, staffName, "correct-horse"); err != nil {
		t.Fatalf("staff login: %v", err)
	}
}

// userOf looks up the (unique) non-staff fixture username created above.
func userOf(t *testing.T, svc *execution.Service, _ bool) string {
	t.Helper()
	var name string
	if err := svc.DB.QueryRow(
		`SELECT username FROM users WHERE is_staff = 0 AND password_hash != '' ORDER BY id DESC LIMIT 1`,
	).Scan(&name); err != nil {
		t.Fatalf("lookup non-staff user: %v", err)
	}
	return name
}

func mustMeta(runID ids.ID) []byte {
	return []byte(fmt.Sprintf(`{"run_id":%q,"provider":"feishu_aily"}`, runID.String()))
}
