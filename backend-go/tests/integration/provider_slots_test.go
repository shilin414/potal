// Provider Inflight Durable Truth integration tests (Admission Fairness &
// Distributed Lease Hardening, Phase 2):
//
//	max_inflight is enforced from MySQL, so Redis loss, a worker restart or
//	clock skew can never raise real provider concurrency above the limit.
//	Slots are ownership-scoped and DB-clock leased: a stale worker can
//	neither delete nor renew the new owner's slot, and a crashed worker's
//	capacity self-heals.
package integration

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
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
	"github.com/creation-agent-studio/backend-go/internal/platform/redisx"
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
		_, _ = svc.DB.ExecContext(context.Background(),
			`DELETE FROM provider_admission_locks WHERE provider = ?`, provider)
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

// activeSlotRows counts the provider's active rows straight from MySQL.
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

// TestConcurrentProviderAdmissionOnFreshProvider serializes the very first
// admission decision for a provider. Two failure modes are pinned here:
//
//   - materializing the admission-lock row inside the capacity transaction
//     coupled first-provider creation to the decision, so the very first
//     concurrent admission resolved on a write conflict instead of on
//     capacity;
//   - serializing with a locking READ alone is not enough: SELECT ... FOR
//     UPDATE does not stop a concurrent decision from observing the same
//     pre-insert depth, so every contender counts zero active slots
//     (observed in CI: admitted=8 rejected=0, and two runs concurrently
//     admitted by the real worker with max_inflight=1). The decision
//     therefore performs a CONFLICTING WRITE on the shared lock row.
func TestConcurrentProviderAdmissionOnFreshProvider(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	provider := slotProvider(t)
	slots := newTestSlots(t, svc, provider, 1, time.Minute)
	// Deliberately high contention: with only a handful of contenders a fast
	// local database can finish the winning decision before the others even
	// count, hiding an unserialized decision. The burst must be wide enough
	// that interleaving is guaranteed on any machine.
	const contenders = 64
	claimed := make([]*execution.ClaimedRun, 0, contenders)
	for i := 0; i < contenders; i++ {
		claimed = append(claimed, claimForSlots(t, svc, provider))
	}

	type result struct {
		ok    bool
		depth int
		err   error
	}
	start := make(chan struct{})
	results := make(chan result, len(claimed))
	var wg sync.WaitGroup
	for _, run := range claimed {
		run := run
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, ok, depth, err := slots.Acquire(ctx, run.Ownership)
			results <- result{ok: ok, depth: depth, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	admitted, rejected := 0, 0
	for got := range results {
		if got.err != nil {
			t.Fatalf("concurrent admission returned an infrastructure error: %v", got.err)
		}
		if got.ok {
			admitted++
		} else {
			rejected++
			if got.depth != 1 {
				t.Fatalf("capacity rejection depth=%d, want 1", got.depth)
			}
		}
	}
	if admitted != 1 || rejected != contenders-1 {
		t.Fatalf("concurrent admission: admitted=%d rejected=%d, want 1/%d", admitted, rejected, contenders-1)
	}
	if got := activeSlotRows(t, svc, provider); got != 1 {
		t.Fatalf("active slot rows=%d after concurrent admission, want 1", got)
	}
	// The serialization must be a conflicting WRITE on the shared admission
	// row (migration 0013), not a locking read: a read-only decision cannot
	// serialize under optimistic transactions. Exactly the committed
	// decisions persist a write (rejected decisions roll theirs back), so a
	// locking-read implementation would leave the counter at zero.
	if got := admissionLockWrites(t, svc, provider); got != uint64(admitted) {
		t.Fatalf("admission lock row written %d time(s), want %d (one per admitted decision)", got, admitted)
	}
}

// admissionLockWrites reads the per-provider serialization counter.
func admissionLockWrites(t *testing.T, svc *execution.Service, provider string) uint64 {
	t.Helper()
	var n uint64
	if err := svc.DB.QueryRowContext(context.Background(),
		`SELECT admissions FROM provider_admission_locks WHERE provider = ?`, provider).Scan(&n); err != nil {
		t.Fatalf("read admission lock counter: %v", err)
	}
	return n
}

// TestProviderCapacityIsRedisIndependent — the report's "Redis restart may
// not raise max_inflight" requirement. Capacity lives in MySQL: deleting the
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
	slots := newTestSlots(t, svc, provider, 1, 5*time.Second)

	claimed := claimForSlots(t, svc, provider)
	// Claim the contender before starting the short slot lease. On a shared
	// remote MySQL, creating a run can itself take longer than the lease and
	// would turn the capacity assertion into an accidental expiry test.
	other := claimForSlots(t, svc, provider)
	if _, ok, _, err := slots.Acquire(ctx, claimed.Ownership); err != nil || !ok {
		t.Fatalf("first acquire: ok=%v err=%v", ok, err)
	}
	if _, ok, _, err := slots.Acquire(ctx, other.Ownership); err != nil || ok {
		t.Fatalf("second acquire at capacity 1: ok=%v err=%v, want rejection", ok, err)
	}
	// The crashed owner never releases; capacity returns by DB-clock expiry.
	time.Sleep(5500 * time.Millisecond)
	if depth, err := slots.Depth(ctx); err != nil || depth != 0 {
		t.Fatalf("depth=%d err=%v after slot lease expiry, want 0", depth, err)
	}
	if _, ok, _, err := slots.Acquire(ctx, other.Ownership); err != nil || !ok {
		t.Fatalf("acquire after crash expiry: ok=%v err=%v, want admitted", ok, err)
	}
}

// TestProviderSlotRejectsStaleOwnership: a worker that wakes up after its run
// was reaped cannot insert a capacity slot — the fence is checked from MySQL,
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

// TestProviderAdmissionRejectsStaleOwnershipBeforeReaper closes the window
// between DB-clock lease expiry and the next recovery sweep. A run can still
// be marked running in that interval, but its previous worker no longer owns
// it and must not acquire fresh provider capacity.
func TestProviderAdmissionRejectsStaleOwnershipBeforeReaper(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	provider := slotProvider(t)
	slots := newTestSlots(t, svc, provider, 1, time.Minute)

	claimed := claimForSlots(t, svc, provider)
	expireLease(t, svc, claimed.Run.ID, claimed.Ownership.WorkerID)

	if _, ok, _, err := slots.Acquire(ctx, claimed.Ownership); !errors.Is(err, execution.ErrLostOwnership) || ok {
		t.Fatalf("expired ownership acquired before reaper: ok=%v err=%v, want ErrLostOwnership", ok, err)
	}
	if got := activeSlotRows(t, svc, provider); got != 0 {
		t.Fatalf("expired ownership inserted %d provider slot(s) before reaper", got)
	}
}

// TestExpiredOwnershipCannotMutateBeforeReaper pins the complete long-pause
// matrix in the interval after the DB-clock lease expires but before recovery.
// Every worker-owned path must fail closed; an old worker cannot revive its
// lease/slot or commit provider attempts, retry or terminal state.
func TestExpiredOwnershipCannotMutateBeforeReaper(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	provider := slotProvider(t)
	slots := newTestSlots(t, svc, provider, 8, time.Minute)

	freshExpired := func(t *testing.T) *execution.ClaimedRun {
		t.Helper()
		claimed := claimForSlots(t, svc, provider)
		expireLease(t, svc, claimed.Run.ID, claimed.Ownership.WorkerID)
		return claimed
	}

	t.Run("heartbeat", func(t *testing.T) {
		claimed := freshExpired(t)
		if ok, err := svc.HeartbeatOwned(ctx, claimed.Ownership, time.Minute); err != nil || ok {
			t.Fatalf("expired heartbeat: ok=%v err=%v, want false/nil", ok, err)
		}
	})

	t.Run("merged heartbeat", func(t *testing.T) {
		claimed := claimForSlots(t, svc, provider)
		slot, ok, _, err := slots.Acquire(ctx, claimed.Ownership)
		if err != nil || !ok {
			t.Fatalf("acquire: ok=%v err=%v", ok, err)
		}
		expireLease(t, svc, claimed.Run.ID, claimed.Ownership.WorkerID)
		leaseOK, slotOK, err := svc.HeartbeatOwnedWithSlot(ctx, claimed.Ownership, time.Minute, slot, time.Minute)
		if err != nil || leaseOK || slotOK {
			t.Fatalf("expired merged heartbeat: leaseOK=%v slotOK=%v err=%v, want false/false/nil", leaseOK, slotOK, err)
		}
	})

	t.Run("provider slot renew", func(t *testing.T) {
		claimed := claimForSlots(t, svc, provider)
		slot, ok, _, err := slots.Acquire(ctx, claimed.Ownership)
		if err != nil || !ok {
			t.Fatalf("acquire: ok=%v err=%v", ok, err)
		}
		if _, err := svc.DB.ExecContext(ctx,
			`UPDATE provider_execution_slots SET expires_at = DATE_SUB(CURRENT_TIMESTAMP(3), INTERVAL 1 SECOND)
			 WHERE provider = ? AND run_id = ? AND lease_epoch = ?`,
			slot.Provider, slot.RunID.Bytes(), slot.LeaseEpoch); err != nil {
			t.Fatalf("expire provider slot: %v", err)
		}
		if err := slots.Renew(ctx, slot); !errors.Is(err, execution.ErrProviderSlotLost) {
			t.Fatalf("expired slot renew: err=%v, want ErrProviderSlotLost", err)
		}
	})

	t.Run("begin attempt", func(t *testing.T) {
		claimed := freshExpired(t)
		if _, err := svc.BeginProviderAttemptOwned(ctx, claimed.Ownership); !errors.Is(err, execution.ErrLostOwnership) {
			t.Fatalf("expired begin attempt: err=%v, want ErrLostOwnership", err)
		}
	})

	t.Run("retry", func(t *testing.T) {
		claimed := freshExpired(t)
		if err := svc.RetryOwnedRun(ctx, claimed.Run, claimed.Ownership, "expired"); !errors.Is(err, execution.ErrLostOwnership) {
			t.Fatalf("expired retry: err=%v, want ErrLostOwnership", err)
		}
	})

	t.Run("finalize", func(t *testing.T) {
		claimed := freshExpired(t)
		err := svc.FinalizeOwnedRun(ctx, claimed.Run, claimed.Ownership, &execution.FinishInput{Status: execution.StatusSucceeded})
		if !errors.Is(err, execution.ErrLostOwnership) {
			t.Fatalf("expired finalize: err=%v, want ErrLostOwnership", err)
		}
	})
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

// TestFinalizeAndRetryDeleteSlotAtomically pins both ownership transitions
// that free provider capacity: the terminal (finalize) and the non-terminal
// (retry) write delete the attempt's slot rows in the SAME MySQL transaction
// as the canonical state change. Capacity therefore never depends on the
// worker's deferred Release — "run stopped running ⇒ no provider slot"
// (invariant M) holds at commit time.
func TestFinalizeAndRetryDeleteSlotAtomically(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()

	t.Run("finalize", func(t *testing.T) {
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
		if got := slotRowsForRun(t, svc, provider, claimed.Run.ID); got != 0 {
			t.Fatalf("finalize committed with %d slot row(s) for the run, want 0", got)
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
	})

	t.Run("retry", func(t *testing.T) {
		provider := slotProvider(t)
		slots := newTestSlots(t, svc, provider, 1, time.Minute)
		claimed := claimForSlots(t, svc, provider)
		if _, ok, _, err := slots.Acquire(ctx, claimed.Ownership); err != nil || !ok {
			t.Fatalf("acquire: ok=%v err=%v", ok, err)
		}

		if err := svc.RetryOwnedRun(ctx, claimed.Run, claimed.Ownership, "provider_retry"); err != nil {
			t.Fatalf("retry: %v", err)
		}
		run, err := svc.GetRun(ctx, claimed.Run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != execution.StatusQueued {
			t.Fatalf("run status=%s after retry, want queued", run.Status)
		}
		if got := slotRowsForRun(t, svc, provider, claimed.Run.ID); got != 0 {
			t.Fatalf("retry committed with %d slot row(s) for the run, want 0", got)
		}
		if got := activeSlotRows(t, svc, provider); got != 0 {
			t.Fatalf("retry committed with %d active provider slot(s), want 0", got)
		}
		if _, err := svc.Querier().GetLease(ctx, claimed.Run.ID.Bytes()); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("retry committed with a lease: err=%v", err)
		}

		// Capacity is reusable by a fresh claim (new epoch) as soon as the
		// retry backoff passes, without resurrecting the old attempt's slot.
		var reclaimed *execution.ClaimedRun
		waitUntil(t, "retried run claimable again", 15*time.Second, func() bool {
			got, won, err := svc.ClaimRun(ctx, claimed.Run.ID, "retry-owner", time.Minute)
			if err != nil {
				t.Fatalf("re-claim after retry: %v", err)
			}
			if won {
				reclaimed = got
			}
			return won
		})
		if _, ok, _, err := slots.Acquire(ctx, reclaimed.Ownership); err != nil || !ok {
			t.Fatalf("acquire after retry: ok=%v err=%v", ok, err)
		}
		if got := slotRowsForRun(t, svc, provider, claimed.Run.ID); got != 1 {
			t.Fatalf("slot rows after re-claim = %d, want 1 (only the new epoch)", got)
		}
	})
}

// TestStaleEpochCannotMutateAfterLeaseRecovery is the ownership fencing
// matrix for a long worker pause. Worker A expires, the reaper recovers the
// run and worker B takes over; every stale A operation is then rejected, and
// A's idempotent slot release cannot disturb B's capacity reservation.
func TestStaleEpochCannotMutateAfterLeaseRecovery(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	provider := slotProvider(t)
	slots := newTestSlots(t, svc, provider, 1, time.Minute)

	claimedA := claimForSlots(t, svc, provider)
	slotA, ok, _, err := slots.Acquire(ctx, claimedA.Ownership)
	if err != nil || !ok {
		t.Fatalf("A acquire: ok=%v err=%v", ok, err)
	}
	expireLease(t, svc, claimedA.Run.ID, claimedA.Ownership.WorkerID)
	recoverRun(t, svc, claimedA.Run.ID)
	claimedB, won, err := svc.ClaimRun(ctx, claimedA.Run.ID, "slot-worker-b", time.Minute)
	if err != nil || !won {
		t.Fatalf("B claim: won=%v err=%v", won, err)
	}
	slotB, ok, _, err := slots.Acquire(ctx, claimedB.Ownership)
	if err != nil || !ok {
		t.Fatalf("B acquire: ok=%v err=%v", ok, err)
	}

	if leaseOK, _, err := svc.HeartbeatOwnedWithSlot(ctx, claimedA.Ownership, time.Minute, slotA, time.Minute); err != nil || leaseOK {
		t.Fatalf("stale heartbeat: leaseOK=%v err=%v, want false/nil", leaseOK, err)
	}
	if _, err := svc.BeginProviderAttemptOwned(ctx, claimedA.Ownership); !errors.Is(err, execution.ErrLostOwnership) {
		t.Fatalf("stale begin attempt: err=%v, want ErrLostOwnership", err)
	}
	if err := slots.Renew(ctx, slotA); !errors.Is(err, execution.ErrProviderSlotLost) {
		t.Fatalf("stale slot renew: err=%v, want ErrProviderSlotLost", err)
	}
	if err := slots.Release(ctx, slotA); err != nil {
		t.Fatalf("stale slot release: %v", err)
	}
	if got := activeSlotRows(t, svc, provider); got != 1 {
		t.Fatalf("stale release changed B's capacity row count to %d, want 1", got)
	}
	if err := svc.RetryOwnedRun(ctx, claimedA.Run, claimedA.Ownership, "stale"); !errors.Is(err, execution.ErrLostOwnership) {
		t.Fatalf("stale retry: err=%v, want ErrLostOwnership", err)
	}
	if err := svc.FinalizeOwnedRun(ctx, claimedA.Run, claimedA.Ownership, &execution.FinishInput{
		Status: execution.StatusSucceeded,
	}); !errors.Is(err, execution.ErrLostOwnership) {
		t.Fatalf("stale finalize: err=%v, want ErrLostOwnership", err)
	}
	if err := slots.Renew(ctx, slotB); err != nil {
		t.Fatalf("current owner slot renew: %v", err)
	}
	if err := svc.FinalizeOwnedRun(ctx, claimedB.Run, claimedB.Ownership, &execution.FinishInput{
		Status: execution.StatusSucceeded,
	}); err != nil {
		t.Fatalf("B finalize: %v", err)
	}
	if got := activeSlotRows(t, svc, provider); got != 0 {
		t.Fatalf("B finalize left %d active provider slot(s)", got)
	}
}

// blockingHandler holds the provider slot until released, so a second run
// provably arrives while capacity is exhausted. It also records the highest
// number of handler entries that were ever in flight simultaneously — the
// "remote handler entered count" bound of the Redis-loss proof. The tracked
// window ends BEFORE finalize (== the provider call), because finalize is what
// frees the slot: a run admitted right after that commit is legitimate even
// while the previous handler has not returned yet.
type blockingHandler struct {
	release chan struct{}
	svc     *execution.Service
	calls   *atomic.Int64

	mu        sync.Mutex
	seen      []string
	active    int
	maxActive int
}

func (h *blockingHandler) Execute(ctx context.Context, claimed *execution.ClaimedRun) error {
	h.calls.Add(1)
	h.mu.Lock()
	h.seen = append(h.seen, claimed.Run.ID.String())
	h.active++
	if h.active > h.maxActive {
		h.maxActive = h.active
	}
	h.mu.Unlock()
	leave := func() {
		h.mu.Lock()
		h.active--
		h.mu.Unlock()
	}
	select {
	case <-h.release:
	case <-ctx.Done():
		leave()
		return ctx.Err()
	}
	leave()
	return h.svc.FinalizeOwnedRun(ctx, claimed.Run, claimed.Ownership, &execution.FinishInput{
		Status: execution.StatusSucceeded,
		Output: map[string]any{"text": "done"},
	})
}

// entered reports the total number of handler entries so far.
func (h *blockingHandler) entered() int { return int(h.calls.Load()) }

// maxConcurrent reports the high-water mark of concurrent handler entries.
func (h *blockingHandler) maxConcurrent() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.maxActive
}

// activeEntries reports how many provider calls are in flight right now.
func (h *blockingHandler) activeEntries() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.active
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
	// Worker logs are the primary evidence for an admission anomaly, but CI
	// job logs require authentication; the buffer lets a failure re-emit them
	// through t.Logf, which the workflow's check annotation captures.
	logs := &syncLogBuffer{}
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
		Log:           slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn})),
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
		// Two overlapping provider executions with max_inflight=1 is a
		// capacity violation by construction — fail fast with the evidence
		// instead of waiting out the deadline.
		if handler.maxConcurrent() > 1 {
			diag := admissionFacts(t, svc, handler, provider, runA, runB)
			dumpWorkerLogs(t, logs)
			t.Fatalf("provider admission admitted %d concurrent executions with max_inflight=1: %s",
				handler.maxConcurrent(), diag)
		}
		if time.Now().After(deadline) {
			runAState, _ := svc.GetRun(ctx, runA)
			runBState, _ := svc.GetRun(ctx, runB)
			diag := admissionFacts(t, svc, handler, provider, runA, runB)
			dumpWorkerLogs(t, logs)
			t.Fatalf("neither run was held back for provider admission (A=%s B=%s, handler calls=%d): %s",
				runAState.Status, runBState.Status, handler.calls.Load(), diag)
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

// syncLogBuffer captures worker logs (slog sink) so a failing run can re-emit
// the tail through t.Logf.
type syncLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// tail returns up to n non-empty lines, newest last, each length-capped.
func (b *syncLogBuffer) tail(n int) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	lines := strings.Split(strings.TrimRight(b.buf.String(), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > 200 {
			line = line[len(line)-200:]
		}
		out = append(out, line)
	}
	return out
}

// dumpWorkerLogs re-emits the worker's recent warnings as test log lines: CI
// job logs need authentication, so the check annotation is the only window.
func dumpWorkerLogs(t *testing.T, logs *syncLogBuffer) {
	t.Helper()
	for _, line := range logs.tail(4) {
		t.Logf("worker log: %s", line)
	}
}

// admissionFacts renders the provider admission state as one compact line:
// handler counters, the durable slot rows (epoch + remaining lease) and each
// run's status/attempt/epoch/lease/events. A negative slot or lease remainder
// means the DB clock already considers that row expired.
func admissionFacts(t *testing.T, svc *execution.Service, handler *blockingHandler, provider string, runs ...ids.ID) string {
	t.Helper()
	ctx := context.Background()
	var b strings.Builder
	fmt.Fprintf(&b, "calls=%d act=%d max=%d", handler.entered(), handler.activeEntries(), handler.maxConcurrent())

	if rows, err := svc.DB.QueryContext(ctx,
		`SELECT lease_epoch, TIMESTAMPDIFF(MICROSECOND, CURRENT_TIMESTAMP(3), expires_at), run_id
		 FROM provider_execution_slots WHERE provider = ?`, provider); err == nil {
		slots := make([]string, 0, 4)
		for rows.Next() {
			var epoch uint64
			var remaining int64
			var runID []byte
			if err := rows.Scan(&epoch, &remaining, &runID); err != nil {
				continue
			}
			id := ids.ID{}
			_ = id.Scan(runID)
			slots = append(slots, fmt.Sprintf("%s@e%d%+dms", shortID(id), epoch, remaining/1000))
		}
		rows.Close()
		fmt.Fprintf(&b, " slots=[%s]", strings.Join(slots, ","))
	}

	for i, runID := range runs {
		status := "?"
		attempt := int64(-1)
		if run, err := svc.GetRun(ctx, runID); err == nil {
			status, attempt = run.Status, run.Attempt
		}
		var epoch uint64
		_ = svc.DB.QueryRowContext(ctx, `SELECT lease_epoch FROM runs WHERE id = ?`, runID.Bytes()).Scan(&epoch)
		var leaseRemaining sql.NullInt64
		_ = svc.DB.QueryRowContext(ctx,
			`SELECT TIMESTAMPDIFF(MICROSECOND, CURRENT_TIMESTAMP(3), expires_at) FROM run_leases WHERE run_id = ?`,
			runID.Bytes()).Scan(&leaseRemaining)
		lease := "none"
		if leaseRemaining.Valid {
			lease = fmt.Sprintf("%+dms", leaseRemaining.Int64/1000)
		}
		events, _ := svc.ListEventsAfter(ctx, runID, 0)
		kinds := make([]string, 0, len(events))
		for _, ev := range events {
			kind := ev.EventType
			if ev.EventType == execution.EventRunRetrying {
				if r, ok := ev.Payload["reason"].(string); ok {
					kind += ":" + r
				}
			}
			kinds = append(kinds, kind)
		}
		fmt.Fprintf(&b, " %c=%s/a%d/ep%d/l%s/%s[%s]",
			'A'+rune(i), status, attempt, epoch, lease, shortID(runID), strings.Join(kinds, ","))
	}
	return b.String()
}

// shortID is the first 8 characters of a UUID — enough to correlate runs with
// slot rows in a compact diagnostic line.
func shortID(id ids.ID) string {
	s := id.String()
	if len(s) > 8 {
		return s[:8]
	}
	return s
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

// slotRowsForRun counts every slot row of a run regardless of provider or
// expiry — the "no slot survives an ownership transition" probe.
func slotRowsForRun(t *testing.T, svc *execution.Service, provider string, runID ids.ID) int {
	t.Helper()
	var n int
	if err := svc.DB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM provider_execution_slots WHERE provider = ? AND run_id = ?`,
		provider, runID.Bytes()).Scan(&n); err != nil {
		t.Fatalf("count slot rows for run: %v", err)
	}
	return n
}

// slotExpiresAtMillis reads one ownership's stored expiry as absolute
// epoch-milliseconds: the same value on every read while nobody touches the
// row, so "unchanged" is a strict statement (unlike a remaining-TTL probe).
func slotExpiresAtMillis(t *testing.T, svc *execution.Service, slot *execution.ProviderSlot) int64 {
	t.Helper()
	var ms int64
	if err := svc.DB.QueryRowContext(context.Background(),
		`SELECT CAST(UNIX_TIMESTAMP(expires_at) * 1000 AS SIGNED) FROM provider_execution_slots
		 WHERE provider = ? AND run_id = ? AND lease_epoch = ? AND lease_token = ?`,
		slot.Provider, slot.RunID.Bytes(), slot.LeaseEpoch, slot.LeaseToken.Bytes()).Scan(&ms); err != nil {
		t.Fatalf("read slot expiry: %v", err)
	}
	return ms
}

// waitUntil polls cond until it holds or the timeout passes. The deadline is
// generous and the condition is deterministic — no fixed sleeps as assertions.
func waitUntil(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

// slotWorkerGroup is the consumer group shared by every worker of one test
// provider (a real fleet shares a group; only the consumers differ).
func slotWorkerGroup(provider string) string { return "itest-slotgroup-" + provider }

// enqueueForTest publishes run wakeups on the provider's interactive stream
// (the class the seeded fixtures use), mirroring the outbox relay.
func enqueueForTest(t *testing.T, rdb *redisx.Client, provider string, runIDs ...ids.ID) {
	t.Helper()
	ctx := context.Background()
	group := slotWorkerGroup(provider)
	stream := execution.PriorityStream(rdb, provider, execution.PriorityClassInteractive)
	if err := rdb.XGroupCreateMkStream(ctx, stream, group, "0").Err(); err != nil &&
		!strings.Contains(err.Error(), "BUSYGROUP") {
		t.Fatalf("create group %s: %v", group, err)
	}
	for _, runID := range runIDs {
		if err := rdb.XAdd(ctx, &goredis.XAddArgs{
			Stream: stream,
			Values: map[string]any{"run_id": runID.String()},
		}).Err(); err != nil {
			t.Fatalf("xadd: %v", err)
		}
	}
}

// newSlotWorker builds a real worker for one provider: Redis wakeups plus a
// one-second MySQL fallback scan, so even a flushed Redis cannot hide queued
// work (capacity decisions never come from Redis anyway).
func newSlotWorker(svc *execution.Service, rdb *redisx.Client, provider, workerID string, handler execution.Handler, slots *execution.ProviderSlots, concurrency int) *execution.Worker {
	return &execution.Worker{
		Svc:           svc,
		RDB:           rdb,
		Provider:      provider,
		WorkerID:      workerID,
		Group:         slotWorkerGroup(provider),
		Handler:       handler,
		Concurrency:   concurrency,
		Lease:         time.Minute,
		Heartbeat:     5 * time.Second,
		ScanEvery:     time.Second,
		ReclaimAfter:  time.Minute,
		Log:           silentLogger(),
		ProviderSlots: slots,
	}
}

// TestProviderSlotCannotBeRenewedByPreviousEpoch: after a re-claim the run has
// a new lease epoch, and the previous epoch's renewal must neither resurrect
// its own slot (gone with the recovery) nor extend the new owner's slot.
func TestProviderSlotCannotBeRenewedByPreviousEpoch(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	provider := slotProvider(t)
	slots := newTestSlots(t, svc, provider, 4, time.Minute)

	claimedA := claimForSlots(t, svc, provider)
	slotA, ok, _, err := slots.Acquire(ctx, claimedA.Ownership)
	if err != nil || !ok {
		t.Fatalf("A acquire: ok=%v err=%v", ok, err)
	}
	expireLease(t, svc, claimedA.Run.ID, claimedA.Ownership.WorkerID)
	recoverRun(t, svc, claimedA.Run.ID)
	claimedB, won, err := svc.ClaimRun(ctx, claimedA.Run.ID, "epoch-worker-b", time.Minute)
	if err != nil || !won {
		t.Fatalf("B claim: won=%v err=%v", won, err)
	}
	if claimedB.Ownership.LeaseEpoch <= claimedA.Ownership.LeaseEpoch {
		t.Fatalf("epoch did not advance: A=%d B=%d",
			claimedA.Ownership.LeaseEpoch, claimedB.Ownership.LeaseEpoch)
	}
	slotB, ok, _, err := slots.Acquire(ctx, claimedB.Ownership)
	if err != nil || !ok {
		t.Fatalf("B acquire: ok=%v err=%v", ok, err)
	}

	// The previous epoch's slot is gone (removed by the recovery transaction),
	// so its renewal reports a lost slot instead of resurrecting capacity.
	before := slotExpiresAtMillis(t, svc, slotB)
	if err := slots.Renew(ctx, slotA); !errors.Is(err, execution.ErrProviderSlotLost) {
		t.Fatalf("stale epoch renew: err=%v, want ErrProviderSlotLost", err)
	}
	if after := slotExpiresAtMillis(t, svc, slotB); after != before {
		t.Fatalf("stale epoch renew touched the new owner's slot: %d → %d", before, after)
	}
	if got := slotRowsForRun(t, svc, provider, claimedA.Run.ID); got != 1 {
		t.Fatalf("slot rows after stale renew = %d, want 1 (only the new epoch)", got)
	}
	// The current epoch is unaffected and still renewable.
	if err := slots.Renew(ctx, slotB); err != nil {
		t.Fatalf("current epoch renew: %v", err)
	}
}

// TestProviderSlotCannotBeReleasedByPreviousEpoch: the previous epoch's
// release may only ever delete its OWN row. The new owner's capacity
// reservation survives, and the stale release cannot disturb it.
func TestProviderSlotCannotBeReleasedByPreviousEpoch(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	provider := slotProvider(t)
	slots := newTestSlots(t, svc, provider, 4, time.Minute)

	claimedA := claimForSlots(t, svc, provider)
	slotA, ok, _, err := slots.Acquire(ctx, claimedA.Ownership)
	if err != nil || !ok {
		t.Fatalf("A acquire: ok=%v err=%v", ok, err)
	}
	expireLease(t, svc, claimedA.Run.ID, claimedA.Ownership.WorkerID)
	recoverRun(t, svc, claimedA.Run.ID)
	claimedB, won, err := svc.ClaimRun(ctx, claimedA.Run.ID, "release-worker-b", time.Minute)
	if err != nil || !won {
		t.Fatalf("B claim: won=%v err=%v", won, err)
	}
	slotB, ok, _, err := slots.Acquire(ctx, claimedB.Ownership)
	if err != nil || !ok {
		t.Fatalf("B acquire: ok=%v err=%v", ok, err)
	}
	expiresBefore := slotExpiresAtMillis(t, svc, slotB)

	if err := slots.Release(ctx, slotA); err != nil {
		t.Fatalf("stale epoch release: %v", err)
	}
	if got := activeSlotRows(t, svc, provider); got != 1 {
		t.Fatalf("stale release changed capacity to %d active slot(s), want 1 (B's)", got)
	}
	if got := slotRowsForRun(t, svc, provider, claimedA.Run.ID); got != 1 {
		t.Fatalf("stale release deleted the new owner's row: rows=%d, want 1", got)
	}
	if after := slotExpiresAtMillis(t, svc, slotB); after != expiresBefore {
		t.Fatalf("stale release modified the new owner's slot: %d → %d", expiresBefore, after)
	}
	if err := slots.Renew(ctx, slotB); err != nil {
		t.Fatalf("B renew after stale release: %v", err)
	}
	// B's own terminalization still frees the capacity.
	if err := svc.FinalizeOwnedRun(ctx, claimedB.Run, claimedB.Ownership, &execution.FinishInput{
		Status: execution.StatusSucceeded,
	}); err != nil {
		t.Fatalf("B finalize: %v", err)
	}
	if got := activeSlotRows(t, svc, provider); got != 0 {
		t.Fatalf("active slots after B finalize = %d, want 0", got)
	}
}

// TestReaperDeletesSlotsForRecoveredOwnership: the recovery transaction itself
// removes the crashed attempt's capacity rows (including stale epochs of the
// same run) — no worker code has to run for capacity to return.
func TestReaperDeletesSlotsForRecoveredOwnership(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	provider := slotProvider(t)
	slots := newTestSlots(t, svc, provider, 1, time.Minute)

	claimedA := claimForSlots(t, svc, provider)
	if _, ok, _, err := slots.Acquire(ctx, claimedA.Ownership); err != nil || !ok {
		t.Fatalf("A acquire: ok=%v err=%v", ok, err)
	}
	// Poison an older epoch's row for the same run (a crashed attempt whose
	// cleanup never ran): recovery must sweep those too.
	if _, err := svc.DB.ExecContext(ctx,
		`INSERT INTO provider_execution_slots
		     (provider, run_id, lease_epoch, lease_token, worker_id, acquired_at, heartbeat_at, expires_at)
		 VALUES (?, ?, ?, ?, 'stale-worker', CURRENT_TIMESTAMP(3), CURRENT_TIMESTAMP(3),
		         DATE_ADD(CURRENT_TIMESTAMP(3), INTERVAL 10 MINUTE))`,
		provider, claimedA.Run.ID.Bytes(), claimedA.Ownership.LeaseEpoch-1, ids.New().Bytes()); err != nil {
		t.Fatalf("poison stale slot: %v", err)
	}

	expireLease(t, svc, claimedA.Run.ID, claimedA.Ownership.WorkerID)
	recoverRun(t, svc, claimedA.Run.ID)

	if got := slotRowsForRun(t, svc, provider, claimedA.Run.ID); got != 0 {
		t.Fatalf("reaper left %d slot row(s) for the recovered run, want 0", got)
	}
	if got := activeSlotRows(t, svc, provider); got != 0 {
		t.Fatalf("reaper left %d active slot(s), want 0", got)
	}
	// Capacity is immediately reusable by the next owner.
	claimedB, won, err := svc.ClaimRun(ctx, claimedA.Run.ID, "reaper-worker-b", time.Minute)
	if err != nil || !won {
		t.Fatalf("B claim: won=%v err=%v", won, err)
	}
	if _, ok, _, err := slots.Acquire(ctx, claimedB.Ownership); err != nil || !ok {
		t.Fatalf("B acquire after reaper cleanup: ok=%v err=%v", ok, err)
	}
}

// TestRedisFlushDoesNotIncreaseProviderCapacity is the report's acceptance
// proof that Redis is no longer the provider capacity truth:
//
//	max_inflight = 2
//	Run A → active, Run B → active   (MySQL active slots = 2)
//	FLUSH the whole Redis logical DB
//	create Run C → still rejected by provider admission
//	remote handler entered concurrency NEVER exceeds 2
//
// The worker's Redis wakeups are destroyed by the flush on purpose: C is
// found by the MySQL fallback scan, and the admission decision is taken from
// the durable slot table.
func TestRedisFlushDoesNotIncreaseProviderCapacity(t *testing.T) {
	svc, rdb := testEnv(t)
	if rdb == nil {
		t.Skip("needs STUDIO_TEST_REDIS=1")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := slotProvider(t) + "_flush"
	const max = 2
	slots := newTestSlots(t, svc, provider, max, time.Minute)

	handler := &blockingHandler{release: make(chan struct{}), svc: svc, calls: &atomic.Int64{}}
	worker := newSlotWorker(svc, rdb, provider, "flush-worker", handler, slots, 3)
	go worker.Run(ctx)

	runA := seedRun(t, svc, provider)
	runB := seedRun(t, svc, provider)
	enqueueForTest(t, rdb, provider, runA, runB)

	// Phase 1: both runs occupy the provider; MySQL confirms capacity 2/2.
	waitUntil(t, "two runs admitted and blocked", 30*time.Second, func() bool {
		return handler.activeEntries() == max && activeSlotRows(t, svc, provider) == max
	})
	if got := handler.entered(); got != max {
		t.Fatalf("handler entered %d time(s) before capacity was exhausted, want %d", got, max)
	}

	// Redis state loss: the entire logical DB disappears (failover without
	// persistence / FLUSH as an operator would do). Only MySQL remains.
	if err := rdb.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("flush redis: %v", err)
	}
	streamKey := execution.PriorityStream(rdb, provider, execution.PriorityClassInteractive)
	if n, err := rdb.Exists(ctx, streamKey).Result(); err != nil || n != 0 {
		t.Fatalf("redis stream survived the flush: exists=%d err=%v", n, err)
	}

	// Phase 2: Run C is created after the flush. Its Redis wakeup is
	// deliberately NOT published — the flush destroyed the queue and the
	// stream stays gone, exactly like the production failure mode. The MySQL
	// fallback scan must still find C, and the durable capacity must still
	// hold it back.
	runC := seedRun(t, svc, provider)
	waitUntil(t, "run C held back for provider admission", 30*time.Second, func() bool {
		return runRetryingReason(t, svc, runC) == "provider_inflight_limit"
	})
	if got := handler.countFor(runC); got != 0 {
		t.Fatalf("run C entered the handler %d time(s) while capacity was exhausted", got)
	}
	if got := handler.entered(); got != max {
		t.Fatalf("handler entered %d time(s) after the Redis flush, want %d (A+B only)", got, max)
	}
	if got := handler.maxConcurrent(); got > max {
		t.Fatalf("concurrent handler entries reached %d after Redis loss, want <= %d", got, max)
	}
	if got := activeSlotRows(t, svc, provider); got != max {
		t.Fatalf("MySQL active slots = %d after the flush, want %d", got, max)
	}

	// Phase 3: releasing the provider frees the capacity; C then runs once.
	// Redis is never repopulated: every decision below comes from MySQL.
	close(handler.release)
	waitUntil(t, "run C executed after capacity returned", 60*time.Second, func() bool {
		run, err := svc.GetRun(ctx, runC)
		return err == nil && run.Status == execution.StatusSucceeded && handler.countFor(runC) == 1
	})
	if got := handler.entered(); got != max+1 {
		t.Fatalf("handler entered %d time(s) in total, want %d (A, B, C)", got, max+1)
	}
	if got := handler.maxConcurrent(); got > max {
		t.Fatalf("handler concurrency high-water mark = %d, want <= %d (invariant)", got, max)
	}
	if got := activeSlotRows(t, svc, provider); got != 0 {
		t.Fatalf("active slots after all runs finished = %d, want 0", got)
	}
	// The queue was never re-created: the entire post-flush outcome (claim,
	// rejection, requeue, execution) was driven by MySQL.
	if n, err := rdb.Exists(ctx, streamKey).Result(); err != nil || n != 0 {
		t.Fatalf("redis queue was repopulated after the flush: exists=%d err=%v", n, err)
	}
}

// TestWorkerRestartDoesNotIncreaseProviderCapacity: a second execution plane
// (fresh DB handle + fresh slot store + fresh worker id — the deploy / crash+
// respawn / extra replica scenario) observes the SAME durable accounting. It
// cannot admit a run while the first plane holds the provider, and it becomes
// usable the moment the first plane finishes.
func TestWorkerRestartDoesNotIncreaseProviderCapacity(t *testing.T) {
	svc, rdb := testEnv(t)
	if rdb == nil {
		t.Skip("needs STUDIO_TEST_REDIS=1")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := slotProvider(t) + "_restart"
	const max = 1
	slots := newTestSlots(t, svc, provider, max, time.Minute)

	handler := &blockingHandler{release: make(chan struct{}), svc: svc, calls: &atomic.Int64{}}
	first := newSlotWorker(svc, rdb, provider, "restart-worker-a", handler, slots, 1)
	go first.Run(ctx)

	runA := seedRun(t, svc, provider)
	enqueueForTest(t, rdb, provider, runA)
	waitUntil(t, "first worker admitted run A", 30*time.Second, func() bool {
		return handler.countFor(runA) == 1 && handler.activeEntries() == 1
	})

	// Restart: a brand-new DB handle, slot store and worker id. Nothing is
	// shared with the first plane except MySQL and Redis.
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	db2, err := database.Open(ctx, cfg.Database)
	if err != nil {
		t.Fatalf("second db handle: %v", err)
	}
	t.Cleanup(func() { _ = db2.Close() })
	svc2 := execution.NewService(db2, rdb, silentLogger(), nil)
	slots2 := execution.NewProviderSlots(db2, provider, max, time.Minute)
	second := newSlotWorker(svc2, rdb, provider, "restart-worker-b", handler, slots2, 1)
	go second.Run(ctx)

	runB := seedRun(t, svc, provider)
	enqueueForTest(t, rdb, provider, runB)
	waitUntil(t, "restarted worker held run B back", 30*time.Second, func() bool {
		return runRetryingReason(t, svc, runB) == "provider_inflight_limit"
	})
	if got := handler.countFor(runB); got != 0 {
		t.Fatalf("restarted worker entered the handler %d time(s) while capacity was full", got)
	}
	if got := handler.maxConcurrent(); got > max {
		t.Fatalf("handler concurrency reached %d across two workers, want <= %d", got, max)
	}
	if depth, err := slots2.Depth(ctx); err != nil || depth != max {
		t.Fatalf("restarted worker sees depth=%d err=%v, want %d (durable state)", depth, err, max)
	}

	// Capacity returns when the first plane finishes; B then runs once.
	close(handler.release)
	waitUntil(t, "run B executed after the first plane finished", 60*time.Second, func() bool {
		run, err := svc.GetRun(ctx, runB)
		return err == nil && run.Status == execution.StatusSucceeded && handler.countFor(runB) == 1
	})
	if got := handler.countFor(runA); got != 1 {
		t.Fatalf("run A executed %d times, want 1", got)
	}
	if got := handler.maxConcurrent(); got > max {
		t.Fatalf("handler concurrency high-water mark = %d, want <= %d", got, max)
	}
	if got := activeSlotRows(t, svc, provider); got != 0 {
		t.Fatalf("active slots after both runs = %d, want 0", got)
	}
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
