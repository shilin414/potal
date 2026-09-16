// 第九轮补丁 3.3-A 端到端验证：Provider EFFECTIVE capacity（复审报告 §二十一）。
//
// The property under test: max_inflight bounds REAL provider executions, not
// just the slots a live worker happens to own. A provider request that reached
// the provider and then lost its local slot — an unknown submit outcome parked
// in waiting_external, a worker crash after accept, an acceptance-persistence
// failure — must still occupy capacity, and must release it exactly once the
// run settles.
//
//	TestProviderCapacityDoesNotDoubleCountNormalExecution     (Case 1)
//	TestProviderCapacityHoldsAfterUnknownParkReleasesSlot     (Case 2)
//	TestProviderCapacityHoldsAfterAcceptedWorkerCrash         (Case 3)
//	TestProviderCapacityLetsTheSameUnresolvedRunRecover       (Case 4)
//	TestProviderCapacityReleasesOnSettledRun                  (Case 5)
//	TestProviderCapacityIgnoresRejectedSubmissions            (Case 6)
//	TestProviderCapacityHoldsAfterAcceptancePersistenceFailure (Case 7)
//	TestProviderCapacityNeverDipsDuringParkAdmissionRace      (Case 8)
//
// Every fixture uses its own provider key (slotProvider), so the counts stay
// hermetic even on the shared dev database.
package integration

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
)

// capacityFixture supplies a limiter, a claimed run and a second claimable
// run under one hermetic provider key.
type capacityFixture struct {
	svc      *execution.Service
	provider string
	slots    *execution.ProviderSlots
	runID    ids.ID
	claimed  *execution.ClaimedRun
}

// newCapacityFixture builds a max_inflight limiter over a fresh provider key
// and claims the first run.
func newCapacityFixture(t *testing.T, max int) *capacityFixture {
	t.Helper()
	svc, _ := testEnv(t)
	ctx := context.Background()
	provider := slotProvider(t)
	slots := execution.NewProviderSlots(svc.DB, provider, max, time.Minute)
	cleanupProviderCapacity(t, svc.DB, provider)
	runID := seedRun(t, svc, provider)
	claimed, won, err := svc.ClaimRun(ctx, runID, "cap-worker", time.Minute)
	if err != nil || !won {
		t.Fatalf("claim capacity run: won=%v err=%v", won, err)
	}
	return &capacityFixture{svc: svc, provider: provider, slots: slots, runID: runID, claimed: claimed}
}

// claimAnother seeds and claims an independent run under the same provider —
// the "second run" every rejection case is about.
func (f *capacityFixture) claimAnother(t *testing.T) *execution.ClaimedRun {
	t.Helper()
	ctx := context.Background()
	runID := seedRun(t, f.svc, f.provider)
	claimed, won, err := f.svc.ClaimRun(ctx, runID, "cap-worker-b", time.Minute)
	if err != nil || !won {
		t.Fatalf("claim second run: won=%v err=%v", won, err)
	}
	return claimed
}

// acquire is the admitted form of Acquire (a rejection here is a fixture bug).
func (f *capacityFixture) acquire(t *testing.T, own execution.ExecutionOwnership) *execution.ProviderSlot {
	t.Helper()
	slot, ok, depth, err := f.slots.Acquire(context.Background(), own)
	if err != nil || !ok {
		t.Fatalf("acquire: ok=%v depth=%d err=%v", ok, depth, err)
	}
	return slot
}

// beginSending consumes a provider attempt and leaves the submission in
// flight ('sending') — the state a worker is in while the HTTP call is out.
func (f *capacityFixture) beginSending(t *testing.T, own execution.ExecutionOwnership) *execution.ProviderSubmission {
	t.Helper()
	return beginSubmission(t, f.svc, own, f.provider)
}

// assertRejected pins "the second run must NOT be admitted".
func (f *capacityFixture) assertRejected(t *testing.T, own execution.ExecutionOwnership, stage string) {
	t.Helper()
	slot, ok, _, err := f.slots.Acquire(context.Background(), own)
	if err == nil && ok {
		_ = f.slots.Release(context.Background(), slot)
	}
	if ok {
		t.Fatalf("%s: the second run was admitted into provider capacity", stage)
	}
}

// assertCapacity pins the three depth readings in one place.
func (f *capacityFixture) assertCapacity(t *testing.T, stage string, effective, controlled, uncontrolled int) {
	t.Helper()
	eff, ctl, unc := capacityDepths(t, f.slots)
	if eff != effective || ctl != controlled || unc != uncontrolled {
		t.Fatalf("%s: effective=%d controlled=%d uncontrolled=%d, want %d/%d/%d",
			stage, eff, ctl, unc, effective, controlled, uncontrolled)
	}
}

// capacityDepths reads the three capacity faces of one limiter.
func capacityDepths(t *testing.T, slots *execution.ProviderSlots) (effective, controlled, uncontrolled int) {
	t.Helper()
	ctx := context.Background()
	eff, err := slots.Depth(ctx)
	if err != nil {
		t.Fatalf("effective depth: %v", err)
	}
	ctl, err := slots.ControlledDepth(ctx)
	if err != nil {
		t.Fatalf("controlled depth: %v", err)
	}
	unc, err := slots.UncontrolledDepth(ctx)
	if err != nil {
		t.Fatalf("uncontrolled depth: %v", err)
	}
	return eff, ctl, unc
}

// cleanupProviderCapacity removes every capacity row of one provider. The
// fixture keys are unique per invocation, but a crashed earlier run could have
// left rows behind under the same key — clearing them makes the counts exact.
func cleanupProviderCapacity(t *testing.T, db *sql.DB, provider string) {
	t.Helper()
	t.Cleanup(func() {
		for _, stmt := range []string{
			`DELETE FROM provider_execution_slots WHERE provider = ?`,
			`DELETE FROM provider_admission_locks WHERE provider = ?`,
			`DELETE FROM provider_submissions WHERE provider = ?`,
		} {
			if _, err := db.ExecContext(context.Background(), stmt, provider); err != nil {
				t.Logf("capacity cleanup %q: %v", stmt, err)
			}
		}
	})
}

// expireProviderSlot pushes every slot of a provider into the past: the DB
// clock, not the test clock, decides expiry, so this is what "the owning
// worker crashed and its lease ran out" looks like to a later admission.
func expireProviderSlot(t *testing.T, svc *execution.Service, provider string) {
	t.Helper()
	if _, err := svc.DB.ExecContext(context.Background(),
		`UPDATE provider_execution_slots
		 SET expires_at = DATE_SUB(CURRENT_TIMESTAMP(3), INTERVAL 1 SECOND)
		 WHERE provider = ?`, provider); err != nil {
		t.Fatalf("expire provider slots: %v", err)
	}
}

// assertSlotRows pins the CONTROLLED row count straight from MySQL, so a depth
// assertion that still holds is provably carried by the submission leg and not
// by a slot that happened to survive.
func assertSlotRows(t *testing.T, svc *execution.Service, provider string, want int, stage string) {
	t.Helper()
	if got := activeSlotRows(t, svc, provider); got != want {
		t.Fatalf("%s: active provider slot rows = %d, want %d", stage, got, want)
	}
}

// ── Case 1 ──────────────────────────────────────────────────────────────

// TestProviderCapacityDoesNotDoubleCountNormalExecution: during normal
// execution a run owns a slot AND has a sending submission. Those are ONE
// provider execution, so the effective depth is 1 — not 2. The second run is
// then refused, which is what makes the bound real.
func TestProviderCapacityDoesNotDoubleCountNormalExecution(t *testing.T) {
	f := newCapacityFixture(t, 1)

	f.acquire(t, f.claimed.Ownership)
	sub := f.beginSending(t, f.claimed.Ownership)

	if sub.State != execution.SubmissionSending {
		t.Fatalf("submission state = %q, want %q", sub.State, execution.SubmissionSending)
	}
	assertSlotRows(t, f.svc, f.provider, 1, "slot + sending submission")
	f.assertCapacity(t, "slot + sending submission", 1, 1, 0)

	f.assertRejected(t, f.claimAnother(t).Ownership, "slot + sending submission")
}

// TestProviderCapacityHoldsAfterUnknownParkReleasesSlot: the unknown submit
// outcome parks the run in waiting_external and DELETES its slot (the run must
// not hold a worker-scoped reservation while nothing is executing locally).
// Capacity must not follow the slot down — the provider may still be running
// the request, so the reservation continues to be held by the submission.
func TestProviderCapacityHoldsAfterUnknownParkReleasesSlot(t *testing.T) {
	f := newCapacityFixture(t, 1)
	ctx := context.Background()

	f.acquire(t, f.claimed.Ownership)
	sub := f.beginSending(t, f.claimed.Ownership)
	if err := f.svc.MarkSubmissionStateOwned(ctx, f.claimed.Ownership, sub, execution.SubmissionUnknown, "itest: outcome unknown"); err != nil {
		t.Fatalf("mark unknown: %v", err)
	}
	if err := f.svc.AwaitExternalOwned(ctx, f.claimed.Ownership, "itest: unknown"); err != nil {
		t.Fatalf("park run: %v", err)
	}

	st := readRunState(t, f.svc.DB, f.runID)
	if st.status != execution.StatusWaitingExternal {
		t.Fatalf("status = %q, want waiting_external", st.status)
	}
	assertSlotRows(t, f.svc, f.provider, 0, "parked run")
	f.assertCapacity(t, "unknown + waiting_external, no slot", 1, 0, 1)

	f.assertRejected(t, f.claimAnother(t).Ownership, "unresolved provider request still outstanding")
}

// TestProviderCapacityHoldsAfterAcceptedWorkerCrash: the provider accepted
// (chat-1 recorded) and the worker died without releasing its slot. The slot
// self-heals by expiry — but the accepted external chat keeps running, so the
// capacity must not be handed to another run.
func TestProviderCapacityHoldsAfterAcceptedWorkerCrash(t *testing.T) {
	f := newCapacityFixture(t, 1)
	ctx := context.Background()

	f.acquire(t, f.claimed.Ownership)
	sub := f.beginSending(t, f.claimed.Ownership)
	if err := f.svc.MarkSubmissionAcceptedOwned(ctx, f.claimed.Ownership, sub, "chat-1"); err != nil {
		t.Fatalf("mark accepted: %v", err)
	}
	// Worker crash: no release, no finalize — only the DB-clock lease expires.
	expireProviderSlot(t, f.svc, f.provider)

	assertSlotRows(t, f.svc, f.provider, 0, "crashed worker")
	f.assertCapacity(t, "accepted + non-settled + expired slot", 1, 0, 1)

	f.assertRejected(t, f.claimAnother(t).Ownership, "accepted provider chat still running")
}

// ── Case 4 ──────────────────────────────────────────────────────────────

// TestProviderCapacityLetsTheSameUnresolvedRunRecover: an unresolved run that
// the reaper requeued must be able to re-acquire its own capacity. Counting
// the run against itself would reject it forever and the queue entry that
// parks it in waiting_external would never be consumed — a self-deadlock no
// lease or timeout can break.
func TestProviderCapacityLetsTheSameUnresolvedRunRecover(t *testing.T) {
	f := newCapacityFixture(t, 1)
	ctx := context.Background()

	f.acquire(t, f.claimed.Ownership)
	f.beginSending(t, f.claimed.Ownership)

	// The worker dies: the run lease and the slot both expire, the reaper
	// requeues the run (deleting the slot in the same transaction).
	expireLease(t, f.svc, f.runID, "cap-worker")
	_ = recoverRun(t, f.svc, f.runID)

	// Still occupied: one unresolved provider execution, now unowned.
	assertSlotRows(t, f.svc, f.provider, 0, "requeued run")
	f.assertCapacity(t, "sending + queued, no slot", 1, 0, 1)

	claimedB, won, err := f.svc.ClaimRun(ctx, f.runID, "cap-worker-b", time.Minute)
	if err != nil || !won {
		t.Fatalf("reclaim: won=%v err=%v", won, err)
	}

	// THE assertion: the run is admitted although it is itself the run the
	// effective count is holding capacity for.
	slot, ok, depth, err := f.slots.Acquire(ctx, claimedB.Ownership)
	if err != nil || !ok {
		t.Fatalf("unresolved run could not re-acquire its own capacity: ok=%v depth=%d err=%v "+
			"(a global count self-deadlocks here)", ok, depth, err)
	}
	if depth != 1 {
		t.Fatalf("depth after recovery = %d, want 1 (other runs + this one)", depth)
	}

	// And the recovery must NOT transmit a second provider request: the
	// 'sending' submission is the answer.
	if _, err := f.svc.BeginProviderSubmissionOwned(ctx, claimedB.Ownership, f.provider,
		submissionFixtureHash(f.provider), execution.ResendForbidden); !errors.Is(err, execution.ErrProviderSubmitUnknown) {
		t.Fatalf("begin after recovery: err=%v, want ErrProviderSubmitUnknown — a recurring "+
			"unresolved submission must never be retransmitted", err)
	}

	// The convergence chain ends in waiting_external, slot released, capacity
	// still held.
	if err := f.svc.AwaitExternalOwned(ctx, claimedB.Ownership, "itest: unresolved on recovery"); err != nil {
		t.Fatalf("park recovered run: %v", err)
	}
	if err := f.slots.Release(ctx, slot); err != nil {
		t.Fatalf("release recovered slot: %v", err)
	}
	assertSlotRows(t, f.svc, f.provider, 0, "recovered and parked")
	f.assertCapacity(t, "recovered, unresolved, parked", 1, 0, 1)
}

// ── Case 5 ──────────────────────────────────────────────────────────────

// TestProviderCapacityReleasesOnSettledRun: the submission ledger is HISTORY —
// it keeps state='accepted' after the run succeeds and is never rewritten. The
// capacity predicate must therefore read runs.status, or every completed run
// would pin a slot for the lifetime of the table.
func TestProviderCapacityReleasesOnSettledRun(t *testing.T) {
	f := newCapacityFixture(t, 1)
	ctx := context.Background()

	f.acquire(t, f.claimed.Ownership)
	sub := f.beginSending(t, f.claimed.Ownership)
	if err := f.svc.MarkSubmissionAcceptedOwned(ctx, f.claimed.Ownership, sub, "chat-1"); err != nil {
		t.Fatalf("mark accepted: %v", err)
	}
	f.assertCapacity(t, "accepted while running", 1, 1, 0)

	run, err := f.svc.GetRun(ctx, f.runID)
	if err != nil {
		t.Fatalf("load run: %v", err)
	}
	if err := f.svc.FinalizeOwnedRun(ctx, run, f.claimed.Ownership, &execution.FinishInput{
		Status:         execution.StatusSucceeded,
		Output:         map[string]any{"text": "done"},
		ProviderStatus: "Completed",
	}); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	// The finalize transaction deletes the slot AND settles the run: both legs
	// of the union are gone, so the accepted ledger row stops counting.
	assertSlotRows(t, f.svc, f.provider, 0, "finalized run")
	f.assertCapacity(t, "accepted + succeeded history", 0, 0, 0)

	// The capacity is genuinely free, not merely uncounted.
	f.acquire(t, f.claimAnother(t).Ownership)
}

// ── Case 6 ──────────────────────────────────────────────────────────────

// TestProviderCapacityIgnoresRejectedSubmissions: a definitive refusal means
// the provider holds NO external action. Counting it would leak one unit of
// capacity on every normal 400/401/403/429 — a slow, permanent capacity
// shrinkage under ordinary error traffic.
func TestProviderCapacityIgnoresRejectedSubmissions(t *testing.T) {
	f := newCapacityFixture(t, 1)
	ctx := context.Background()

	sub := f.beginSending(t, f.claimed.Ownership)
	if err := f.svc.MarkSubmissionStateOwned(ctx, f.claimed.Ownership, sub, execution.SubmissionRejected, "itest: refused"); err != nil {
		t.Fatalf("mark rejected: %v", err)
	}
	if st := readRunState(t, f.svc.DB, f.runID); st.status != execution.StatusRunning {
		t.Fatalf("status = %q, want running (a refusal does not settle the run)", st.status)
	}

	assertSlotRows(t, f.svc, f.provider, 0, "rejected submission")
	f.assertCapacity(t, "rejected, no slot, non-settled run", 0, 0, 0)

	f.acquire(t, f.claimAnother(t).Ownership)
}

// ── Case 7 ──────────────────────────────────────────────────────────────

// TestProviderCapacityHoldsAfterAcceptancePersistenceFailure reuses the 3.2
// real-1205 recipe: the provider answered an external id but the transaction
// that records 'accepted' cannot commit. The executor stops the chain and the
// worker releases its slot — and the capacity must NOT disappear with it,
// exactly because the authoritative source is the submission ledger (state
// stays 'sending'), not the released slot.
func TestProviderCapacityHoldsAfterAcceptancePersistenceFailure(t *testing.T) {
	f := newAilySubmitFixture(t, "r933acc", "background", "acceptance persistence failure + capacity")
	ctx := context.Background()

	slots := execution.NewProviderSlots(f.env.db, f.provKey, 1, time.Minute)
	t.Cleanup(func() {
		for _, stmt := range []string{
			`DELETE FROM provider_execution_slots WHERE provider = ?`,
			`DELETE FROM provider_admission_locks WHERE provider = ?`,
			`DELETE FROM provider_submissions WHERE provider = ?`,
		} {
			_, _ = f.env.db.ExecContext(context.Background(), stmt, f.provKey)
		}
	})

	slot, ok, _, err := slots.Acquire(ctx, f.claimed.Ownership)
	if err != nil || !ok {
		t.Fatalf("acquire before execute: ok=%v err=%v", ok, err)
	}

	tuned := newSessionTunedService(t, "SET SESSION innodb_lock_wait_timeout = 1")
	exec := *f.exec
	exec.Owned = tuned.WorkerOwned()
	release := lockRunsRowDuringSubmit(t, f, f.runID)
	execErr := exec.Execute(ctx, f.claimed)
	release()

	if execErr == nil {
		t.Fatal("execute returned nil although the acceptance ledger could not be written")
	}
	starts, opens := f.rec.counted()
	if starts != 1 || opens != 0 {
		t.Fatalf("provider calls: StartChat=%d OpenStreamChat=%d, want 1/0 (never a second submit)", starts, opens)
	}
	if state, externalID := submissionOf(t, f); state != execution.SubmissionSending || externalID != "" {
		t.Fatalf("submission state=%q external=%q, want sending/\"\"", state, externalID)
	}

	// The worker's deferred release runs after the handler returns.
	if err := slots.Release(ctx, slot); err != nil {
		t.Fatalf("release slot: %v", err)
	}
	if got := activeSlotRows(t, f.svc, f.provKey); got != 0 {
		t.Fatalf("acceptance persistence failure: active slot rows = %d, want 0", got)
	}

	// controlled=0 (no worker holds it) but uncontrolled=1: the provider chat
	// may be real, so the effective bound still counts it.
	eff, ctl, unc := capacityDepths(t, slots)
	if eff != 1 || ctl != 0 || unc != 1 {
		t.Fatalf("after the acceptance write failed: effective=%d controlled=%d uncontrolled=%d, want 1/0/1",
			eff, ctl, unc)
	}

	// A second run must not be admitted into the provider's capacity.
	other := claimForSlots(t, f.svc, f.provKey)
	slotB, okB, _, errB := slots.Acquire(ctx, other.Ownership)
	if errB == nil && okB {
		_ = slots.Release(ctx, slotB)
	}
	if okB {
		t.Fatalf("second run admitted although the provider may hold chat-1 (err=%v)", errB)
	}
}

// ── Case 8 ──────────────────────────────────────────────────────────────

// TestProviderCapacityNeverDipsDuringParkAdmissionRace: parking a run
// (slot deletion + status waiting_external + submission unknown) is ONE
// canonical transition, so a concurrent admission must never observe a
// capacity=0 window and slip in.
//
// A sampler reads the effective depth for the WHOLE duration of the park
// transaction while a second run hammers Acquire — neither may see free
// capacity.
func TestProviderCapacityNeverDipsDuringParkAdmissionRace(t *testing.T) {
	f := newCapacityFixture(t, 1)
	ctx := context.Background()

	f.acquire(t, f.claimed.Ownership)
	sub := f.beginSending(t, f.claimed.Ownership)
	if err := f.svc.MarkSubmissionStateOwned(ctx, f.claimed.Ownership, sub, execution.SubmissionUnknown, "itest: outcome unknown"); err != nil {
		t.Fatalf("mark unknown: %v", err)
	}
	// Precondition: the pre-park state already occupies exactly one unit, so
	// every sample below must be >= 1.
	f.assertCapacity(t, "pre-park", 1, 1, 0)

	other := f.claimAnother(t)

	// Sampler first, so it is already spinning when the park begins. The
	// counters are atomic because the sampler outlives this goroutine's
	// borrow of them under -race.
	var minSeen, samples int64
	atomic.StoreInt64(&minSeen, 1<<30)
	sampleStop := make(chan struct{})
	sampleDone := make(chan struct{})
	go func() {
		defer close(sampleDone)
		for {
			select {
			case <-sampleStop:
				return
			default:
			}
			if n, err := f.slots.Depth(ctx); err == nil {
				atomic.AddInt64(&samples, 1)
				for {
					cur := atomic.LoadInt64(&minSeen)
					if int64(n) >= cur || atomic.CompareAndSwapInt64(&minSeen, cur, int64(n)) {
						break
					}
				}
			}
		}
	}()

	var admitted int64
	var acqWG sync.WaitGroup
	acqWG.Add(1)
	go func() {
		defer acqWG.Done()
		for i := 0; i < 4; i++ {
			slot, ok, _, err := f.slots.Acquire(ctx, other.Ownership)
			if err == nil && ok {
				atomic.AddInt64(&admitted, 1)
				_ = f.slots.Release(ctx, slot)
			}
		}
	}()

	parkErr := f.svc.AwaitExternalOwned(ctx, f.claimed.Ownership, "itest: park under admission race")
	close(sampleStop)
	<-sampleDone
	acqWG.Wait()
	if parkErr != nil {
		t.Fatalf("park: %v", parkErr)
	}

	if atomic.LoadInt64(&samples) == 0 {
		t.Fatal("the capacity monitor collected no samples — the race was not exercised")
	}
	if seen := atomic.LoadInt64(&minSeen); seen < 1 {
		t.Fatalf("effective capacity dipped to %d while a run was being parked — slot deletion, "+
			"waiting_external and unknown must be one canonical transition", seen)
	}
	if n := atomic.LoadInt64(&admitted); n > 0 {
		t.Fatalf("%d concurrent admission(s) slipped through the park window", n)
	}

	assertSlotRows(t, f.svc, f.provider, 0, "after the park race")
	f.assertCapacity(t, "after the park race", 1, 0, 1)
}
