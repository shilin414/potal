// Provider Inflight Durable Truth integration tests (Admission Fairness &
// Distributed Lease Hardening, Phase 2):
//
//	max_inflight is enforced from TiDB, so Redis loss, a worker restart or
//	clock skew can never raise real provider concurrency above the limit.
//	Slots are ownership-scoped and DB-clock leased: a stale worker can
//	neither delete nor renew the new owner's slot, and a crashed worker's
//	capacity self-heals.
package integration

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/platform/config"
	"github.com/creation-agent-studio/backend-go/internal/platform/database"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
)

// slotProvider gives every test invocation its own provider key: runs,
// streams and slots are then hermetic even on a shared dev database that
// still carries rows from earlier (possibly crashed) test runs.
func slotProvider(t *testing.T) string {
	name := strings.ToLower(t.Name())
	if len(name) > 30 {
		name = name[:30]
	}
	return "itest_slots_" + name + "_" + strconv.FormatInt(time.Now().UnixNano()%1e10, 36)
}

// newTestSlots builds a durable slot store and cleans its (provider-scoped)
// rows afterwards.
func newTestSlots(t *testing.T, svc *execution.Service, provider string, max int, lease time.Duration) *execution.ProviderSlots {
	t.Helper()
	slots := execution.NewProviderSlots(svc.DB, provider, max, lease)
	t.Cleanup(func() {
		_, _ = svc.DB.ExecContext(context.Background(),
			`DELETE FROM provider_execution_slots WHERE provider = ?`, provider)
	})
	return slots
}

// claimForSlots seeds and claims a run so slot admission exercises real
// ownership (the Acquire fence rejects non-owners).
func claimForSlots(t *testing.T, svc *execution.Service, provider string) *execution.ClaimedRun {
	t.Helper()
	ctx := context.Background()
	runID := seedRun(t, svc, provider)
	claimed, won, err := svc.ClaimRun(ctx, runID, "slot-worker", time.Minute)
	if err != nil || !won {
		t.Fatalf("claim for slots: won=%v err=%v", won, err)
	}
	return claimed
}

// activeSlotRows counts the provider's active rows straight from TiDB.
func activeSlotRows(t *testing.T, svc *execution.Service, provider string) int {
	t.Helper()
	var n int
	if err := svc.DB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM provider_execution_slots
		 WHERE provider = ? AND expires_at > CURRENT_TIMESTAMP(3)`, provider).Scan(&n); err != nil {
		t.Fatalf("count active slots: %v", err)
	}
	return n
}

// TestProviderSlotsEnforceGlobalMaxAcrossWorkers: the durable semaphore is
// global per provider — the (max+1)-th distinct ownership is rejected.
func TestProviderSlotsEnforceGlobalMaxAcrossWorkers(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	provider := slotProvider(t)
	const max = 3
	slots := newTestSlots(t, svc, provider, max, time.Minute)

	for i := 0; i < max; i++ {
		claimed := claimForSlots(t, svc, provider)
		if _, ok, _, err := slots.Acquire(ctx, claimed.Ownership); err != nil || !ok {
			t.Fatalf("acquire %d: ok=%v err=%v", i, ok, err)
		}
	}
	if got := activeSlotRows(t, svc, provider); got != max {
		t.Fatalf("active slot rows = %d, want %d", got, max)
	}
	over := claimForSlots(t, svc, provider)
	if _, ok, depth, err := slots.Acquire(ctx, over.Ownership); err != nil || ok || depth != max {
		t.Fatalf("acquire beyond max: ok=%v depth=%d err=%v, want rejection at %d", ok, depth, err, max)
	}
	// Releasing one frees exactly one slot.
	if _, ok, _, err := slots.Acquire(ctx, over.Ownership); err != nil || ok {
		t.Fatalf("second over-max acquire: ok=%v err=%v", ok, err)
	}
}

// TestProviderCapacityIsRedisIndependent — the report's "Redis restart may
// not raise max_inflight" requirement. Capacity lives in TiDB: deleting the
// legacy Redis inflight key (or losing Redis entirely) has no effect, and a
// FRESH worker process (new DB handle, no shared memory) observes the same
// accounting.
func TestProviderCapacityIsRedisIndependent(t *testing.T) {
	svc, rdb := testEnv(t)
	ctx := context.Background()
	provider := slotProvider(t)
	const max = 2
	slots := newTestSlots(t, svc, provider, max, time.Minute)

	for i := 0; i < max; i++ {
		claimed := claimForSlots(t, svc, provider)
		if _, ok, _, err := slots.Acquire(ctx, claimed.Ownership); err != nil || !ok {
			t.Fatalf("acquire %d: ok=%v err=%v", i, ok, err)
		}
	}
	// Simulate the Redis-side state loss that previously reset the cap.
	if rdb != nil {
		if err := rdb.Del(ctx, rdb.Key("provider", provider, "inflight")).Err(); err != nil {
			t.Fatalf("delete legacy inflight key: %v", err)
		}
	}

	// A "restarted worker": a brand-new store over a brand-new DB handle.
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	db2, err := database.Open(ctx, cfg.Database)
	if err != nil {
		t.Fatalf("second db handle: %v", err)
	}
	t.Cleanup(func() { _ = db2.Close() })
	restarted := execution.NewProviderSlots(db2, provider, max, time.Minute)
	if depth, err := restarted.Depth(ctx); err != nil || depth != max {
		t.Fatalf("restarted worker depth = %d err=%v, want %d (durable state)", depth, err, max)
	}
	over := claimForSlots(t, svc, provider)
	if _, ok, _, err := restarted.Acquire(ctx, over.Ownership); err != nil || ok {
		t.Fatalf("restarted worker admitted over max: ok=%v err=%v", ok, err)
	}
}

// TestProviderSlotReleaseIsOwnershipScoped: worker A's slot is cleaned by the
// reaper when its lease expires; worker B (new epoch) holds its own row, and
// A's stale release/renew can never touch it.
func TestProviderSlotReleaseIsOwnershipScoped(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	provider := slotProvider(t)
	slots := newTestSlots(t, svc, provider, 8, time.Minute)

	claimedA := claimForSlots(t, svc, provider)
	slotA, ok, _, err := slots.Acquire(ctx, claimedA.Ownership)
	if err != nil || !ok {
		t.Fatalf("A acquire: ok=%v err=%v", ok, err)
	}

	// A dies: lease expires, the reaper requeues the run and removes its
	// provider slot in the same transaction.
	expireLease(t, svc, claimedA.Run.ID, "slot-worker")
	recoverRun(t, svc, claimedA.Run.ID)
	if got := activeSlotRows(t, svc, provider); got != 0 {
		t.Fatalf("reaper left %d active slot(s), want 0 (crashed attempt cleaned)", got)
	}

	// B re-claims the run (new epoch, new token) and acquires its own slot.
	claimedB, won, err := svc.ClaimRun(ctx, claimedA.Run.ID, "worker-b", time.Minute)
	if err != nil || !won {
		t.Fatalf("B claim: won=%v err=%v", won, err)
	}
	if claimedB.Ownership.LeaseEpoch <= claimedA.Ownership.LeaseEpoch {
		t.Fatalf("epoch did not increase: A=%d B=%d",
			claimedA.Ownership.LeaseEpoch, claimedB.Ownership.LeaseEpoch)
	}
	slotB, ok, _, err := slots.Acquire(ctx, claimedB.Ownership)
	if err != nil || !ok {
		t.Fatalf("B acquire: ok=%v err=%v", ok, err)
	}
	if slotA.Member == slotB.Member {
		t.Fatalf("ownership-scoped slot members collided: %q", slotA.Member)
	}

	// A wakes up: release is a no-op for B's row; renew reports the slot
	// lost instead of resurrecting A's own row.
	if err := slots.Release(ctx, slotA); err != nil {
		t.Fatalf("stale release: %v", err)
	}
	if got := activeSlotRows(t, svc, provider); got != 1 {
		t.Fatalf("active slot rows = %d after stale release, want 1 (B's)", got)
	}
	if err := slots.Renew(ctx, slotA); !errors.Is(err, execution.ErrProviderSlotLost) {
		t.Fatalf("stale renew: err=%v, want ErrProviderSlotLost", err)
	}
	if got := activeSlotRows(t, svc, provider); got != 1 {
		t.Fatalf("stale renew recreated a slot: rows=%d, want 1", got)
	}
	if err := slots.Release(ctx, slotB); err != nil {
		t.Fatalf("B release: %v", err)
	}
	if got := activeSlotRows(t, svc, provider); got != 0 {
		t.Fatalf("active slot rows = %d after B release, want 0", got)
	}
}

// TestProviderSlotRenewMissingDoesNotRecreate: renewing a released slot must
// fail instead of resurrecting capacity accounting.
func TestProviderSlotRenewMissingDoesNotRecreate(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	provider := slotProvider(t)
	slots := newTestSlots(t, svc, provider, 2, time.Minute)

	claimed := claimForSlots(t, svc, provider)
	slot, ok, _, err := slots.Acquire(ctx, claimed.Ownership)
	if err != nil || !ok {
		t.Fatalf("acquire: ok=%v err=%v", ok, err)
	}
	if err := slots.Release(ctx, slot); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := slots.Renew(ctx, slot); !errors.Is(err, execution.ErrProviderSlotLost) {
		t.Fatalf("renew of a released slot: err=%v, want ErrProviderSlotLost", err)
	}
	if got := activeSlotRows(t, svc, provider); got != 0 {
		t.Fatalf("renew recreated the slot: rows=%d, want 0", got)
	}
}

// TestProviderSlotAcquireIsIdempotent: re-acquiring with the SAME ownership
// refreshes the existing row instead of consuming a second slot.
func TestProviderSlotAcquireIsIdempotent(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	provider := slotProvider(t)
	slots := newTestSlots(t, svc, provider, 2, time.Minute)

	claimed := claimForSlots(t, svc, provider)
	if _, ok, _, err := slots.Acquire(ctx, claimed.Ownership); err != nil || !ok {
		t.Fatalf("first acquire: ok=%v err=%v", ok, err)
	}
	if _, ok, _, err := slots.Acquire(ctx, claimed.Ownership); err != nil || !ok {
		t.Fatalf("re-acquire same ownership: ok=%v err=%v, want admitted", ok, err)
	}
	if got := activeSlotRows(t, svc, provider); got != 1 {
		t.Fatalf("active slot rows = %d after re-acquire, want 1", got)
	}
}

// TestProviderSlotCrashExpiryReturnsCapacity: a crashed worker's slot expires
// with its DB-clock lease instead of pinning provider capacity forever.
func TestProviderSlotCrashExpiryReturnsCapacity(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	provider := slotProvider(t)
	slots := newTestSlots(t, svc, provider, 1, 700*time.Millisecond)

	claimed := claimForSlots(t, svc, provider)
	if _, ok, _, err := slots.Acquire(ctx, claimed.Ownership); err != nil || !ok {
		t.Fatalf("first acquire: ok=%v err=%v", ok, err)
	}
	other := claimForSlots(t, svc, provider)
	if _, ok, _, err := slots.Acquire(ctx, other.Ownership); err != nil || ok {
		t.Fatalf("second acquire at capacity 1: ok=%v err=%v, want rejection", ok, err)
	}
	// The crashed owner never releases; capacity returns by DB-clock expiry.
	time.Sleep(900 * time.Millisecond)
	if depth, err := slots.Depth(ctx); err != nil || depth != 0 {
		t.Fatalf("depth=%d err=%v after slot lease expiry, want 0", depth, err)
	}
	if _, ok, _, err := slots.Acquire(ctx, other.Ownership); err != nil || !ok {
		t.Fatalf("acquire after crash expiry: ok=%v err=%v, want admitted", ok, err)
	}
}

// TestProviderSlotRejectsStaleOwnership: a worker that wakes up after its run
// was reaped cannot insert a capacity slot — the fence is checked from TiDB,
// so a stale process cannot pollute the semaphore.
func TestProviderSlotRejectsStaleOwnership(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	provider := slotProvider(t)
	slots := newTestSlots(t, svc, provider, 4, time.Minute)

	claimedA := claimForSlots(t, svc, provider)
	expireLease(t, svc, claimedA.Run.ID, "slot-worker")
	recoverRun(t, svc, claimedA.Run.ID)

	if _, ok, _, err := slots.Acquire(ctx, claimedA.Ownership); !errors.Is(err, execution.ErrLostOwnership) || ok {
		t.Fatalf("stale acquire: ok=%v err=%v, want ErrLostOwnership", ok, err)
	}
	if got := activeSlotRows(t, svc, provider); got != 0 {
		t.Fatalf("stale acquire inserted %d slot(s)", got)
	}
	// The current owner is admitted normally.
	claimedB, won, err := svc.ClaimRun(ctx, claimedA.Run.ID, "worker-b", time.Minute)
	if err != nil || !won {
		t.Fatalf("B claim: won=%v err=%v", won, err)
	}
	if _, ok, _, err := slots.Acquire(ctx, claimedB.Ownership); err != nil || !ok {
		t.Fatalf("current owner acquire: ok=%v err=%v", ok, err)
	}
}

// TestMergedHeartbeatRenewsLeaseAndSlot (§20): one transaction renews both,
// so "Run Ownership alive ⇔ Provider Slot alive" holds; a lost slot is
// reported without breaking the run lease.
func TestMergedHeartbeatRenewsLeaseAndSlot(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	provider := slotProvider(t)
	slots := newTestSlots(t, svc, provider, 2, time.Minute)

	claimed := claimForSlots(t, svc, provider)
	slot, ok, _, err := slots.Acquire(ctx, claimed.Ownership)
	if err != nil || !ok {
		t.Fatalf("acquire: ok=%v err=%v", ok, err)
	}

	leaseOK, slotOK, err := svc.HeartbeatOwnedWithSlot(ctx, claimed.Ownership, time.Minute, slot, time.Minute)
	if err != nil || !leaseOK || !slotOK {
		t.Fatalf("merged heartbeat: leaseOK=%v slotOK=%v err=%v", leaseOK, slotOK, err)
	}
	if !leaseRowAlive(t, svc, claimed.Run.ID.Bytes()) {
		t.Fatal("run lease not renewed by the merged heartbeat")
	}

	// Slot disappears (e.g. released by an operator / expired): the lease
	// heartbeat must still succeed and report the slot lost.
	if _, err := svc.DB.ExecContext(ctx,
		`DELETE FROM provider_execution_slots WHERE provider = ? AND run_id = ?`,
		provider, claimed.Run.ID.Bytes()); err != nil {
		t.Fatalf("drop slot: %v", err)
	}
	leaseOK, slotOK, err = svc.HeartbeatOwnedWithSlot(ctx, claimed.Ownership, time.Minute, slot, time.Minute)
	if err != nil || !leaseOK {
		t.Fatalf("merged heartbeat after slot loss: leaseOK=%v err=%v, want leaseOK=true", leaseOK, err)
	}
	if slotOK {
		t.Fatal("merged heartbeat reported a lost slot as renewed")
	}
	if got := activeSlotRows(t, svc, provider); got != 0 {
		t.Fatalf("heartbeat recreated the lost slot: rows=%d", got)
	}
	// Ownership loss is reported as leaseOK=false. The fence is the lease
	// TOKEN (a recycled epoch with the old token is still stale).
	stale := execution.ExecutionOwnership{
		RunID:      claimed.Ownership.RunID,
		WorkerID:   claimed.Ownership.WorkerID,
		LeaseEpoch: claimed.Ownership.LeaseEpoch,
		LeaseToken: ids.New(),
	}
	if leaseOK, _, err := svc.HeartbeatOwnedWithSlot(ctx, stale, time.Minute, slot, time.Minute); err != nil || leaseOK {
		t.Fatalf("stale merged heartbeat: leaseOK=%v err=%v, want false", leaseOK, err)
	}
}

// TestFinalizeCleansProviderSlot: terminalization removes the slot in the
// finalize transaction (invariant M holds without waiting for the worker's
// deferred release).
func TestFinalizeCleansProviderSlot(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	provider := slotProvider(t)
	slots := newTestSlots(t, svc, provider, 2, time.Minute)

	claimed := claimForSlots(t, svc, provider)
	if _, ok, _, err := slots.Acquire(ctx, claimed.Ownership); err != nil || !ok {
		t.Fatalf("acquire: ok=%v err=%v", ok, err)
	}
	if err := svc.FinalizeOwnedRun(ctx, claimed.Run, claimed.Ownership, &execution.FinishInput{
		Status: execution.StatusSucceeded,
		Output: map[string]any{"text": "done"},
	}); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if got := activeSlotRows(t, svc, provider); got != 0 {
		t.Fatalf("active slot rows after finalize = %d, want 0", got)
	}
	var orphans int
	if err := svc.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM provider_execution_slots s
		 LEFT JOIN runs r ON r.id = s.run_id AND r.status = 'running' AND r.lease_epoch = s.lease_epoch
		 WHERE s.provider = ? AND s.expires_at > CURRENT_TIMESTAMP(3) AND r.id IS NULL`,
		provider).Scan(&orphans); err != nil {
		t.Fatalf("orphan check: %v", err)
	}
	if orphans != 0 {
		t.Fatalf("orphan provider slots = %d, want 0 (invariant M)", orphans)
	}
}

// blockingHandler holds the provider slot until released, so a second run
// provably arrives while capacity is exhausted.
type blockingHandler struct {
	release chan struct{}
	svc     *execution.Service
	calls   *atomic.Int64

	mu   sync.Mutex
	seen []string
}

func (h *blockingHandler) Execute(ctx context.Context, claimed *execution.ClaimedRun) error {
	h.calls.Add(1)
	h.mu.Lock()
	h.seen = append(h.seen, claimed.Run.ID.String())
	h.mu.Unlock()
	select {
	case <-h.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return h.svc.FinalizeOwnedRun(ctx, claimed.Run, claimed.Ownership, &execution.FinishInput{
		Status: execution.StatusSucceeded,
		Output: map[string]any{"text": "done"},
	})
}

// TestWorkerHoldsRunBackWhenProviderSlotIsFull: the real worker consumes two
// queued runs while max_inflight=1 — ONE of them wins the slot and executes
// (blocking in the handler), the other is claimed but rejected by the
// DURABLE provider slot, requeued for admission WITHOUT consuming a provider
// attempt, and only executes once the winner released its slot. Which run
// wins is a race, so the test derives both roles from the observable state
// instead of assuming an order.
func TestWorkerHoldsRunBackWhenProviderSlotIsFull(t *testing.T) {
	svc, rdb := testEnv(t)
	if rdb == nil {
		t.Skip("needs STUDIO_TEST_REDIS=1")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := slotProvider(t) + "_worker"
	runA := seedRun(t, svc, provider)
	runB := seedRun(t, svc, provider)
	slots := newTestSlots(t, svc, provider, 1, time.Minute)

	group := "itest-slots-" + t.Name()
	stream := execution.PriorityStream(rdb, provider, execution.PriorityClassInteractive)
	if err := rdb.XGroupCreateMkStream(ctx, stream, group, "0").Err(); err != nil &&
		err.Error() != "BUSYGROUP Consumer Group name already exists" {
		t.Fatalf("create group: %v", err)
	}
	handler := &blockingHandler{
		release: make(chan struct{}),
		svc:     svc,
		calls:   &atomic.Int64{},
	}
	worker := &execution.Worker{
		Svc:           svc,
		RDB:           rdb,
		Provider:      provider,
		WorkerID:      "slots-worker",
		Group:         group,
		Handler:       handler,
		Concurrency:   2,
		Lease:         time.Minute,
		Heartbeat:     5 * time.Second,
		ScanEvery:     time.Second, // re-claims the admission-requeued run
		ReclaimAfter:  time.Minute,
		Log:           testLogger(),
		ProviderSlots: slots,
	}
	go worker.Run(ctx)

	for _, runID := range []ids.ID{runA, runB} {
		if err := rdb.XAdd(ctx, &goredis.XAddArgs{
			Stream: stream,
			Values: map[string]any{"run_id": runID.String()},
		}).Err(); err != nil {
			t.Fatalf("xadd: %v", err)
		}
	}

	// Phase 1: exactly one run wins the single slot and blocks inside the
	// handler; the other must be requeued for provider admission.
	var admitted, held ids.ID
	deadline := time.Now().Add(25 * time.Second)
	for {
		switch reasonOf(t, svc, runA, runB) {
		case "A":
			admitted, held = runB, runA
		case "B":
			admitted, held = runA, runB
		}
		if !held.IsZero() {
			break
		}
		if time.Now().After(deadline) {
			runAState, _ := svc.GetRun(ctx, runA)
			runBState, _ := svc.GetRun(ctx, runB)
			t.Fatalf("neither run was held back for provider admission (A=%s B=%s, handler calls=%d)",
				runAState.Status, runBState.Status, handler.calls.Load())
		}
		time.Sleep(100 * time.Millisecond)
	}
	if depth, err := slots.Depth(ctx); err != nil || depth != 1 {
		t.Fatalf("provider depth = %d err=%v, want 1 (the admitted attempt's slot)", depth, err)
	}
	if handler.countFor(admitted) != 1 {
		t.Fatalf("admitted run %s executed %d times while capacity was 1, want 1",
			admitted, handler.countFor(admitted))
	}
	if got := handler.countFor(held); got != 0 {
		t.Fatalf("held run %s entered the handler %d time(s) while capacity was exhausted", held, got)
	}
	heldState, err := svc.GetRun(ctx, held)
	if err != nil {
		t.Fatalf("load held run: %v", err)
	}
	if heldState.Attempt != 0 {
		t.Fatalf("admission-rejected run consumed %d provider attempt(s), want 0", heldState.Attempt)
	}
	if heldState.Status == execution.StatusSucceeded {
		t.Fatalf("held run %s succeeded without ever entering the handler", held)
	}

	// Phase 2: releasing the admitted run frees the slot; the held run then
	// executes and both finish.
	close(handler.release)
	deadline = time.Now().Add(30 * time.Second)
	var statusAdmitted, statusHeld string
	for time.Now().Before(deadline) {
		admittedState, errA := svc.GetRun(ctx, admitted)
		heldRunState, errH := svc.GetRun(ctx, held)
		if errA != nil || errH != nil {
			t.Fatalf("load runs: %v / %v", errA, errH)
		}
		statusAdmitted, statusHeld = admittedState.Status, heldRunState.Status
		if statusAdmitted == execution.StatusSucceeded && statusHeld == execution.StatusSucceeded {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if statusAdmitted != execution.StatusSucceeded || statusHeld != execution.StatusSucceeded {
		t.Fatalf("after capacity returned: admitted run=%s held run=%s, want both succeeded",
			statusAdmitted, statusHeld)
	}
	if got := handler.calls.Load(); got != 2 {
		handler.mu.Lock()
		seen := append([]string(nil), handler.seen...)
		handler.mu.Unlock()
		t.Fatalf("handler executed %d times, want exactly 2 (one held, one admitted later); runs=%v", got, seen)
	}
	for _, runID := range []ids.ID{runA, runB} {
		if got := handler.countFor(runID); got != 1 {
			t.Fatalf("run %s executed %d times, want exactly 1", runID, got)
		}
	}
}

// reasonOf reports which of the two runs was last requeued for provider
// admission ("A", "B" or "").
func reasonOf(t *testing.T, svc *execution.Service, runA, runB ids.ID) string {
	t.Helper()
	if runRetryingReason(t, svc, runA) == "provider_inflight_limit" {
		return "A"
	}
	if runRetryingReason(t, svc, runB) == "provider_inflight_limit" {
		return "B"
	}
	return ""
}

// countFor reports how many times the handler ran for one run.
func (h *blockingHandler) countFor(runID ids.ID) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, seen := range h.seen {
		if seen == runID.String() {
			n++
		}
	}
	return n
}

// runRetryingReason returns the reason of the run's last run.retrying event.
func runRetryingReason(t *testing.T, svc *execution.Service, runID ids.ID) string {
	t.Helper()
	events, err := svc.ListEventsAfter(context.Background(), runID, 0)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	reason := ""
	for _, ev := range events {
		if ev.EventType != execution.EventRunRetrying {
			continue
		}
		if r, ok := ev.Payload["reason"].(string); ok {
			reason = r
		}
	}
	return reason
}

// leaseRowAlive reports whether the run currently holds a non-expired lease
// per the DB clock.
func leaseRowAlive(t *testing.T, svc *execution.Service, runID []byte) bool {
	t.Helper()
	var alive int
	if err := svc.DB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM run_leases WHERE run_id = ? AND expires_at > CURRENT_TIMESTAMP(3)`,
		runID).Scan(&alive); err != nil {
		t.Fatalf("lease probe: %v", err)
	}
	return alive == 1
}
