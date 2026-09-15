package execution

import (
	"errors"
	"testing"
	"time"
)

func TestHeartbeatDecisionKeepsExecutionOnTransientError(t *testing.T) {
	if shouldCancelAfterHeartbeat(false, false, errors.New("temporary database error"), false) {
		t.Fatal("transient heartbeat error cancelled an execution whose lease loss was not confirmed")
	}
}

func TestHeartbeatDecisionCancelsAfterLocalLeaseDeadline(t *testing.T) {
	if !shouldCancelAfterHeartbeat(false, false, errors.New("database unavailable"), true) {
		t.Fatal("heartbeat outage beyond the local lease deadline did not cancel execution")
	}
}

func TestHeartbeatDecisionCancelsConfirmedOwnershipLoss(t *testing.T) {
	if !shouldCancelAfterHeartbeat(false, false, nil, false) {
		t.Fatal("confirmed ownership loss did not cancel the stale execution")
	}
	if shouldCancelAfterHeartbeat(true, true, nil, false) {
		t.Fatal("successful heartbeat cancelled a live execution")
	}
}

// TestHeartbeatDecisionCancelsConfirmedProviderSlotLoss (第八轮 P1): a
// confirmed provider-capacity loss must cancel at once, NOT at the run-lease
// deadline. The run lease renewal was rolled back with the lost slot, so
// every further provider call this worker makes is invisible to
// max_inflight — waiting for the lease TTL would let real concurrency
// exceed the limit that is supposed to be a safety bound.
func TestHeartbeatDecisionCancelsConfirmedProviderSlotLoss(t *testing.T) {
	// leaseOK is false because the merged heartbeat rolls BOTH renewals
	// back; the deadline has NOT elapsed, so the pre-P1 decision returned
	// false and kept executing.
	if !shouldCancelAfterHeartbeat(true, false, ErrProviderSlotLost, false) {
		t.Fatal("confirmed provider slot loss did not cancel the execution before the lease deadline")
	}
	if !shouldCancelAfterHeartbeat(false, false, ErrProviderSlotLost, false) {
		t.Fatal("confirmed provider slot loss with a rolled-back lease did not cancel the execution")
	}
	// The same shape WITHOUT the sentinel (an inconclusive infrastructure
	// error) must still respect the last confirmed TTL.
	if shouldCancelAfterHeartbeat(false, false, errors.New("lock wait timeout"), false) {
		t.Fatal("an inconclusive slot error cancelled the execution before the confirmed lease TTL")
	}
	if !shouldCancelAfterHeartbeat(false, false, errors.New("lock wait timeout"), true) {
		t.Fatal("an inconclusive slot error past the confirmed lease TTL did not cancel execution")
	}
}

// TestConfirmedProviderSlotLossNeedsProof: only a successful round-trip can
// prove the reservation is gone; an infrastructure error proves nothing, and
// a rejected LEASE fence is not a capacity loss.
func TestConfirmedProviderSlotLossNeedsProof(t *testing.T) {
	if !confirmedProviderSlotLoss(true, false, nil) {
		t.Fatal("a round-trip that renewed the lease but reported the slot missing " +
			"was not treated as a confirmed capacity loss")
	}
	if !confirmedProviderSlotLoss(true, true, ErrProviderSlotLost) {
		t.Fatal("ErrProviderSlotLost was not treated as a confirmed loss")
	}
	if confirmedProviderSlotLoss(false, false, errors.New("lock wait timeout")) {
		t.Fatal("an inconclusive infrastructure error was treated as a confirmed capacity loss")
	}
	// The lease fence rejecting reports slotOK=false too (the slot was never
	// touched): that is an ownership loss, not a capacity loss.
	if confirmedProviderSlotLoss(false, false, nil) {
		t.Fatal("a rejected lease fence was misattributed to provider capacity")
	}
	if confirmedProviderSlotLoss(true, true, nil) {
		t.Fatal("a fully successful heartbeat was treated as a capacity loss")
	}
}

// TestHeartbeatCancelReasonNamesTheActualLoss keeps the operator-facing label
// honest: a lease-fence rejection reports slotOK=false as well, and calling
// that "capacity lost" would send an operator to the wrong subsystem.
func TestHeartbeatCancelReasonNamesTheActualLoss(t *testing.T) {
	cases := []struct {
		name            string
		leaseOK, slotOK bool
		err             error
		want            string
	}{
		{"proven slot loss", true, false, ErrProviderSlotLost, "provider capacity reservation lost"},
		{"successful round-trip reporting no slot", true, false, nil, "provider capacity reservation lost"},
		{"fence rejected the lease", false, false, nil, "heartbeat fence rejected ownership"},
		{"outage past the confirmed TTL", false, false, errors.New("lock wait timeout"), "heartbeat unavailable past local lease deadline"},
	}
	for _, tc := range cases {
		if got := heartbeatCancelReason(tc.leaseOK, tc.slotOK, tc.err); got != tc.want {
			t.Errorf("%s: reason = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestLeaseRenewalDeadlineUsesLastConfirmedRenewal(t *testing.T) {
	lastRenewed := time.Unix(100, 0)
	if leaseRenewalDeadlineElapsed(lastRenewed, 30*time.Second, lastRenewed.Add(29*time.Second)) {
		t.Fatal("lease deadline elapsed before the confirmed TTL")
	}
	if !leaseRenewalDeadlineElapsed(lastRenewed, 30*time.Second, lastRenewed.Add(30*time.Second)) {
		t.Fatal("lease deadline remained open at the confirmed TTL")
	}
}
