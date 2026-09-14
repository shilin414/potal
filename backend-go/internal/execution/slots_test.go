package execution

import (
	"context"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
)

// TestProviderSlotIsOwnershipScoped pins the slot identity: the member is
// derived from the immutable ownership (run + claim epoch + token), so two
// attempts of the SAME run are distinct slots and a stale worker can never
// address the new owner's row.
func TestProviderSlotIsOwnershipScoped(t *testing.T) {
	runID := ids.New()
	ownA := ExecutionOwnership{RunID: runID, WorkerID: "a", LeaseEpoch: 1, LeaseToken: ids.New()}
	ownB := ExecutionOwnership{RunID: runID, WorkerID: "b", LeaseEpoch: 2, LeaseToken: ids.New()}

	slotA := newProviderSlot("feishu_aily", ownA)
	slotB := newProviderSlot("feishu_aily", ownB)

	if slotA.Member == slotB.Member {
		t.Fatalf("ownership-scoped members collided: %q", slotA.Member)
	}
	if slotA.Provider != "feishu_aily" || slotA.RunID != runID ||
		slotA.LeaseEpoch != 1 || slotA.LeaseToken != ownA.LeaseToken {
		t.Fatalf("slot A = %+v, want the full ownership", slotA)
	}
	want := runID.String() + ":2:" + ownB.LeaseToken.String()
	if slotB.Member != want {
		t.Fatalf("member = %q, want %q", slotB.Member, want)
	}
}

// TestProviderSlotsUnlimitedIsTransparent: with max_inflight <= 0 (limiter
// disabled) acquire admits immediately and every op is a no-op — the
// execution plane behaves exactly as if no limiter were configured.
func TestProviderSlotsUnlimitedIsTransparent(t *testing.T) {
	l := NewProviderSlots(nil, "feishu_aily", 0, time.Minute)
	own := ownershipFixture(1)
	slot, ok, depth, err := l.Acquire(context.Background(), own)
	if err != nil || !ok || slot != nil || depth != 0 {
		t.Fatalf("unlimited acquire: slot=%v ok=%v depth=%d err=%v", slot, ok, depth, err)
	}
	if err := l.Renew(context.Background(), nil); err != nil {
		t.Fatalf("renew with nil slot: %v", err)
	}
	if err := l.Release(context.Background(), nil); err != nil {
		t.Fatalf("release with nil slot: %v", err)
	}
	if n, err := l.Depth(context.Background()); err != nil || n != 0 {
		t.Fatalf("depth=%d err=%v, want 0/nil", n, err)
	}
}

func ownershipFixture(epoch uint64) ExecutionOwnership {
	return ExecutionOwnership{
		RunID:      ids.New(),
		WorkerID:   "unit-worker",
		LeaseEpoch: epoch,
		LeaseToken: ids.New(),
	}
}
