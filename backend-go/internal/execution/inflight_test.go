package execution

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/platform/config"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
	"github.com/creation-agent-studio/backend-go/internal/platform/redisx"
)

// testInflightRedis opens the shared dev Redis (opt-in via
// STUDIO_TEST_REDIS=1) with the same credentials the binaries use.
func testInflightRedis(t *testing.T) *redisx.Client {
	t.Helper()
	if os.Getenv("STUDIO_TEST_REDIS") != "1" {
		t.Skip("set STUDIO_TEST_REDIS=1 to run provider slot tests against real Redis")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	rdb, err := redisx.Open(context.Background(), cfg.Redis)
	if err != nil {
		t.Fatalf("redis: %v", err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

// newTestLimiter builds a limiter on a per-test stream key so tests on the
// shared dev Redis never interfere with each other.
func newTestLimiter(t *testing.T, rdb *redisx.Client, max int, lease time.Duration) *InflightLimiter {
	t.Helper()
	provider := "itest_inflight_" + t.Name() + "_" + time.Now().Format("150405.000000000")
	l := NewInflightLimiter(rdb, provider, max, lease)
	t.Cleanup(func() { _ = rdb.Del(context.Background(), l.key()).Err() })
	return l
}

// ownershipFor builds an ownership value of the shape ClaimRun produces.
func ownershipFor(epoch uint64) ExecutionOwnership {
	return ExecutionOwnership{
		RunID:      ids.New(),
		WorkerID:   "itest-worker",
		LeaseEpoch: epoch,
		LeaseToken: ids.New(),
	}
}

// TestProviderSlotStaleReleaseCannotDeleteNewOwnerSlot — P0-1 core: worker
// A (epoch 1) loses its lease, worker B re-claims the SAME run (epoch 2)
// and acquires its own slot; A's deferred release must not delete B's
// slot. With the old runID-keyed member both attempts shared one entry.
func TestProviderSlotStaleReleaseCannotDeleteNewOwnerSlot(t *testing.T) {
	rdb := testInflightRedis(t)
	ctx := context.Background()
	l := newTestLimiter(t, rdb, 8, time.Minute)

	runID := ids.New()
	ownA := ExecutionOwnership{RunID: runID, WorkerID: "a", LeaseEpoch: 1, LeaseToken: ids.New()}
	ownB := ExecutionOwnership{RunID: runID, WorkerID: "b", LeaseEpoch: 2, LeaseToken: ids.New()}

	slotA, ok, _, err := l.Acquire(ctx, ownA)
	if err != nil || !ok {
		t.Fatalf("A acquire: ok=%v err=%v", ok, err)
	}
	slotB, ok, _, err := l.Acquire(ctx, ownB)
	if err != nil || !ok {
		t.Fatalf("B acquire: ok=%v err=%v", ok, err)
	}
	if slotA.Member == slotB.Member {
		t.Fatalf("attempt-scoped members collided: %q", slotA.Member)
	}

	// A finally exits and releases — B's slot must survive.
	if err := l.Release(ctx, slotA); err != nil {
		t.Fatalf("A release: %v", err)
	}
	if depth, err := l.Depth(ctx); err != nil || depth != 1 {
		t.Fatalf("after A's stale release depth=%d err=%v, want 1 (B still holds its slot)", depth, err)
	}
	if err := l.Renew(ctx, slotB); err != nil {
		t.Fatalf("B renew after A's stale release: %v", err)
	}
	// A's release must never be able to touch B's member directly.
	if err := l.Release(ctx, slotB); err != nil {
		t.Fatalf("B release: %v", err)
	}
	if depth, _ := l.Depth(ctx); depth != 0 {
		t.Fatalf("depth=%d after both releases, want 0", depth)
	}
}

// TestProviderSlotStaleRenewCannotExtendNewOwnerSlot: a stale attempt's
// renewal must not touch (or extend) the new owner's slot.
func TestProviderSlotStaleRenewCannotExtendNewOwnerSlot(t *testing.T) {
	rdb := testInflightRedis(t)
	ctx := context.Background()
	l := newTestLimiter(t, rdb, 8, 200*time.Millisecond)

	runID := ids.New()
	ownA := ExecutionOwnership{RunID: runID, WorkerID: "a", LeaseEpoch: 1, LeaseToken: ids.New()}
	slotA, _, _, err := l.Acquire(ctx, ownA)
	if err != nil {
		t.Fatal(err)
	}
	// A's slot expires (stale worker paused beyond the slot lease).
	time.Sleep(300 * time.Millisecond)

	ownB := ExecutionOwnership{RunID: runID, WorkerID: "b", LeaseEpoch: 2, LeaseToken: ids.New()}
	slotB, ok, _, err := l.Acquire(ctx, ownB)
	if err != nil || !ok {
		t.Fatalf("B acquire after A's slot expired: ok=%v err=%v", ok, err)
	}
	// A wakes up and renews: its slot is gone → ErrProviderSlotLost, and
	// B's slot must remain untouched (A's renew cannot extend it).
	if err := l.Renew(ctx, slotA); err != ErrProviderSlotLost {
		t.Fatalf("stale renew: err=%v, want ErrProviderSlotLost", err)
	}
	// B's slot keeps its own (fresh) expiry: it must still be renewable.
	if err := l.Renew(ctx, slotB); err != nil {
		t.Fatalf("B renew after stale A renew: %v", err)
	}
	if depth, _ := l.Depth(ctx); depth != 1 {
		t.Fatalf("depth=%d, want 1 (only B holds a slot)", depth)
	}
}

// TestProviderSlotRenewMissingDoesNotRecreate: renewing a slot that does
// not exist must fail instead of resurrecting capacity accounting.
func TestProviderSlotRenewMissingDoesNotRecreate(t *testing.T) {
	rdb := testInflightRedis(t)
	ctx := context.Background()
	l := newTestLimiter(t, rdb, 2, time.Minute)

	own := ownershipFor(1)
	slot, _, _, err := l.Acquire(ctx, own)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Release(ctx, slot); err != nil {
		t.Fatal(err)
	}
	if depth, _ := l.Depth(ctx); depth != 0 {
		t.Fatalf("depth=%d after release, want 0", depth)
	}
	if err := l.Renew(ctx, slot); err != ErrProviderSlotLost {
		t.Fatalf("renew of a released slot: err=%v, want ErrProviderSlotLost", err)
	}
	if depth, _ := l.Depth(ctx); depth != 0 {
		t.Fatalf("renew recreated the slot: depth=%d, want 0", depth)
	}
}

// TestProviderInflightGlobalMaxAcrossWorkers: the semaphore is global per
// provider — the (max+1)-th distinct attempt is rejected across workers.
func TestProviderInflightGlobalMaxAcrossWorkers(t *testing.T) {
	rdb := testInflightRedis(t)
	ctx := context.Background()
	const max = 3
	l := newTestLimiter(t, rdb, max, time.Minute)

	slots := make([]*ProviderSlot, 0, max)
	for i := 0; i < max; i++ {
		slot, ok, _, err := l.Acquire(ctx, ownershipFor(1))
		if err != nil || !ok {
			t.Fatalf("acquire %d: ok=%v err=%v", i, ok, err)
		}
		slots = append(slots, slot)
	}
	if _, ok, depth, err := l.Acquire(ctx, ownershipFor(1)); err != nil || ok {
		t.Fatalf("acquire beyond max: ok=%v depth=%d err=%v, want rejection", ok, depth, err)
	}
	// Releasing one frees exactly one slot.
	if err := l.Release(ctx, slots[0]); err != nil {
		t.Fatal(err)
	}
	if _, ok, _, err := l.Acquire(ctx, ownershipFor(1)); err != nil || !ok {
		t.Fatalf("acquire after release: ok=%v err=%v, want admitted", ok, err)
	}
}

// TestProviderSlotAcquireIsIdempotentForSameAttempt: re-acquiring with the
// same ownership refreshes the existing slot instead of consuming a
// second one (a worker that retries its own acquire must not leak
// capacity).
func TestProviderSlotAcquireIsIdempotentForSameAttempt(t *testing.T) {
	rdb := testInflightRedis(t)
	ctx := context.Background()
	l := newTestLimiter(t, rdb, 2, time.Minute)

	own := ownershipFor(1)
	if _, ok, _, err := l.Acquire(ctx, own); err != nil || !ok {
		t.Fatalf("first acquire: ok=%v err=%v", ok, err)
	}
	if _, ok, _, err := l.Acquire(ctx, own); err != nil || !ok {
		t.Fatalf("re-acquire same attempt: ok=%v err=%v, want admitted", ok, err)
	}
	if depth, _ := l.Depth(ctx); depth != 1 {
		t.Fatalf("depth=%d after re-acquire, want 1 (idempotent)", depth)
	}
}

// TestRejectedAttemptNeverHoldsSlotEvenIfRenewed: a run rejected by the
// provider limit holds no slot; even if its heartbeat wrongly attempted a
// renewal, Renew cannot create one (XX-only) — so a rejected attempt can
// never inflate the semaphore.
func TestRejectedAttemptNeverHoldsSlotEvenIfRenewed(t *testing.T) {
	rdb := testInflightRedis(t)
	ctx := context.Background()
	l := newTestLimiter(t, rdb, 1, time.Minute)

	if _, ok, _, err := l.Acquire(ctx, ownershipFor(1)); err != nil || !ok {
		t.Fatalf("first acquire: ok=%v err=%v", ok, err)
	}
	// Rejected: the limiter reports "no slot" but returns a usable slot
	// descriptor for the caller's release path.
	slot, ok, _, err := l.Acquire(ctx, ownershipFor(1))
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("acquire beyond max was admitted")
	}
	if slot == nil {
		t.Fatal("rejected acquire returned a nil slot descriptor")
	}
	if err := l.Renew(ctx, slot); err != ErrProviderSlotLost {
		t.Fatalf("renew of a rejected slot: err=%v, want ErrProviderSlotLost", err)
	}
	if depth, _ := l.Depth(ctx); depth != 1 {
		t.Fatalf("depth=%d after rejected renew, want 1 (no slot created)", depth)
	}
	// Release is safe too: it cannot touch the admitted attempt's slot.
	if err := l.Release(ctx, slot); err != nil {
		t.Fatalf("release of a rejected slot: %v", err)
	}
	if depth, _ := l.Depth(ctx); depth != 1 {
		t.Fatalf("depth=%d after rejected release, want 1", depth)
	}
}

// TestProviderSlotCrashExpiresSlot: a crashed worker's slot expires with
// the slot lease instead of pinning provider capacity forever.
func TestProviderSlotCrashExpiresSlot(t *testing.T) {
	rdb := testInflightRedis(t)
	ctx := context.Background()
	l := newTestLimiter(t, rdb, 1, 150*time.Millisecond)

	if _, ok, _, err := l.Acquire(ctx, ownershipFor(1)); err != nil || !ok {
		t.Fatalf("first acquire: ok=%v err=%v", ok, err)
	}
	// Immediately full: the same window must reject (capacity is 1).
	if _, ok, _, err := l.Acquire(ctx, ownershipFor(1)); err != nil || ok {
		t.Fatalf("second acquire: ok=%v err=%v, want rejection at capacity 1", ok, err)
	}
	// The crashed worker never releases; the slot expires and capacity
	// returns.
	time.Sleep(250 * time.Millisecond)
	if depth, err := l.Depth(ctx); err != nil || depth != 0 {
		t.Fatalf("depth=%d err=%v after slot lease expiry, want 0", depth, err)
	}
	if _, ok, _, err := l.Acquire(ctx, ownershipFor(2)); err != nil || !ok {
		t.Fatalf("acquire after crash expiry: ok=%v err=%v, want admitted", ok, err)
	}
}
