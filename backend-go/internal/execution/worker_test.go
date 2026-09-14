package execution

import (
	"errors"
	"testing"
	"time"
)

func TestHeartbeatDecisionKeepsExecutionOnTransientError(t *testing.T) {
	if shouldCancelAfterHeartbeat(false, errors.New("temporary database error"), false) {
		t.Fatal("transient heartbeat error cancelled an execution whose lease loss was not confirmed")
	}
}

func TestHeartbeatDecisionCancelsAfterLocalLeaseDeadline(t *testing.T) {
	if !shouldCancelAfterHeartbeat(false, errors.New("database unavailable"), true) {
		t.Fatal("heartbeat outage beyond the local lease deadline did not cancel execution")
	}
}

func TestHeartbeatDecisionCancelsConfirmedOwnershipLoss(t *testing.T) {
	if !shouldCancelAfterHeartbeat(false, nil, false) {
		t.Fatal("confirmed ownership loss did not cancel the stale execution")
	}
	if shouldCancelAfterHeartbeat(true, nil, false) {
		t.Fatal("successful heartbeat cancelled a live execution")
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
