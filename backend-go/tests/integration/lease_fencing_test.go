// Fault-injection integration tests for the execution correctness
// hardening iteration (评测报告 P0-1/2/3 测试矩阵):
//
//  1. Outbox relay publishes canonical UUIDs (not raw BINARY(16) bytes).
//  2. Claim + lease are atomic — a lease INSERT failure rolls the claim
//     back; a running run without a lease can never exist.
//  3. Lease fencing chaos — a stale worker (lease expired, run reclaimed
//     by worker B) can neither append events, finish, requeue, nor drop
//     B's lease.
//  4. Heartbeats are fenced by lease token, not worker id.
package integration

import (
	"context"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
)

// expireLease pushes a worker's lease into the past (simulates a dead or
// paused worker) using that worker's identity.
func expireLease(t *testing.T, svc *execution.Service, runID ids.ID, workerID string) {
	t.Helper()
	past := time.Now().UTC().Add(-1 * time.Second)
	if _, err := svc.Querier().HeartbeatLease(context.Background(), heartbeatExpireParams(past, runID.Bytes(), workerID)); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
}

// TestOutboxRelayCanonicalUUID: the outbox relay must publish run ids as
// canonical UUID strings. With the old bug (string(raw BINARY(16))) every
// worker-side ids.Parse failed and the message was silently ACKed — the
// fast dispatch channel was effectively dead while tests stayed green.
func TestOutboxRelayCanonicalUUID(t *testing.T) {
	svc, rdb := testEnv(t)
	if rdb == nil {
		t.Skip("needs STUDIO_TEST_REDIS=1")
	}
	ctx := context.Background()
	const provider = "itest_relay"
	runID := seedRun(t, svc, provider)

	// Direct outbox row exactly like CreateRunInTx writes it.
	if _, err := svc.Querier().CreateOutboxEvent(ctx, outboxFixture("run", runID.Bytes(), provider)); err != nil {
		t.Fatalf("create outbox: %v", err)
	}

	relay := execution.NewRelay(svc, rdb, 10)
	published, err := relay.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if published != 1 {
		t.Fatalf("relay published %d, want 1", published)
	}

	entries, err := rdb.XRange(ctx, execution.QueueStream(rdb, provider), "-", "+").Result()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no entries in the dispatch stream")
	}
	raw, ok := entries[len(entries)-1].Values["run_id"].(string)
	if !ok {
		t.Fatalf("run_id field missing in stream entry: %v", entries[len(entries)-1].Values)
	}
	parsed, err := ids.Parse(raw)
	if err != nil {
		t.Fatalf("stream run_id %q is NOT a canonical UUID — the P0-1 serialization bug is back: %v", raw, err)
	}
	if parsed != runID {
		t.Fatalf("stream run_id %s != seeded %s", raw, runID.String())
	}
}

// TestClaimAndLeaseAtomicity: when the lease INSERT fails (here: a
// pre-existing lease row hits UNIQUE(run_id)), the whole claim rolls back
// — the run must remain queued. The "running without lease, no queue
// message, invisible to reaper" permanent-stuck state is impossible.
func TestClaimAndLeaseAtomicity(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	runID := seedRun(t, svc, "feishu_aily")

	// Poison the lease slot so the claim transaction's INSERT fails.
	if err := svc.AcquireLease(ctx, runID, "ghost-worker", 60*time.Second); err != nil {
		t.Fatalf("poison lease: %v", err)
	}

	_, won, err := svc.ClaimAndLease(ctx, runID, "victim-worker", 60*time.Second)
	if err == nil {
		t.Fatal("claim+lease unexpectedly succeeded with a poisoned lease slot")
	}
	if won {
		t.Fatal("claim reported won despite lease insert failure")
	}
	run, err := svc.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != execution.StatusQueued {
		t.Fatalf("run status = %s after failed claim+lease, want queued (atomic rollback)", run.Status)
	}
	if run.Attempt != 0 {
		t.Fatalf("attempt = %d after failed claim+lease, want 0", run.Attempt)
	}

	// With the slot cleared the same claim succeeds.
	if err := svc.Querier().DeleteLease(ctx, runID.Bytes()); err != nil {
		t.Fatal(err)
	}
	own, won, err := svc.ClaimAndLease(ctx, runID, "worker-ok", 60*time.Second)
	if err != nil || !won {
		t.Fatalf("claim after cleanup: won=%v err=%v", won, err)
	}
	if own.Epoch == 0 {
		t.Fatal("claim returned epoch 0 — fencing token must start at 1")
	}
	run, _ = svc.GetRun(ctx, runID)
	if run.Status != execution.StatusRunning || run.LeaseEpoch != own.Epoch {
		t.Fatalf("status=%s epoch=%d want running/%d", run.Status, run.LeaseEpoch, own.Epoch)
	}
}

// TestLeaseFencingStaleWorker — the report's chaos scenario:
//
//	Worker A claims → pauses → lease expires → reaper requeues →
//	Worker B reclaims (epoch bump) → Worker A wakes up.
//
// A must lose every canonical write; B keeps full ownership.
func TestLeaseFencingStaleWorker(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	runID := seedRun(t, svc, "feishu_aily")

	// ── Worker A claims (epoch 1). ──
	ownA, won, err := svc.ClaimAndLease(ctx, runID, "worker-a", 60*time.Second)
	if err != nil || !won {
		t.Fatalf("A claim: won=%v err=%v", won, err)
	}
	runA, _ := svc.GetRun(ctx, runID)
	runA.LeaseEpoch = ownA.Epoch
	runA.LeaseToken = ownA.Token

	// ── A "pauses": lease expires, reaper requeues. ──
	expireLease(t, svc, runID, "worker-a")
	if _, err := svc.RecoverExpiredLeases(ctx, 100); err != nil {
		t.Fatalf("reaper: %v", err)
	}
	// (Other tests may leave expired leases behind — the reaper sweeps
	// them all; this run's own recovery is asserted via status below.)
	run, _ := svc.GetRun(ctx, runID)
	if run.Status != execution.StatusQueued {
		t.Fatalf("after reaper status=%s, want queued", run.Status)
	}

	// ── Worker B reclaims (epoch 2, new token). ──
	ownB, won, err := svc.ClaimAndLease(ctx, runID, "worker-b", 60*time.Second)
	if err != nil || !won {
		t.Fatalf("B claim: won=%v err=%v", won, err)
	}
	if ownB.Epoch <= ownA.Epoch {
		t.Fatalf("epoch did not increase on reclaim: A=%d B=%d", ownA.Epoch, ownB.Epoch)
	}
	runB, _ := svc.GetRun(ctx, runID)
	runB.LeaseEpoch = ownB.Epoch
	runB.LeaseToken = ownB.Token

	// ── Worker A wakes up — every write must be fenced out. ──
	if err := svc.AppendEventFenced(ctx, runA, execution.EventContentDelta, map[string]any{"text": "stale"}); err != execution.ErrLostOwnership {
		t.Fatalf("stale A AppendEvent: err=%v, want ErrLostOwnership", err)
	}
	if err := svc.CheckOwnership(ctx, runID, ownA.Epoch); err != execution.ErrLostOwnership {
		t.Fatalf("stale A CheckOwnership: err=%v, want ErrLostOwnership", err)
	}
	if err := svc.UpdateExternalRunIDFenced(ctx, runA, "chat_stale"); err != execution.ErrLostOwnership {
		t.Fatalf("stale A UpdateExternalRunID: err=%v, want ErrLostOwnership", err)
	}
	// A's interrupted-release must NOT requeue (B is running) and must
	// NOT delete B's lease.
	if err := svc.ReleaseInterruptedFenced(ctx, runA, "stale worker"); err != nil {
		t.Logf("stale A ReleaseInterruptedFenced returned %v (acceptable: fenced no-ops)", err)
	}
	run, _ = svc.GetRun(ctx, runID)
	if run.Status != execution.StatusRunning {
		t.Fatalf("stale A requeued B's run: status=%s, want running", run.Status)
	}
	if _, err := svc.Querier().GetLease(ctx, runID.Bytes()); err != nil {
		t.Fatalf("B's lease was deleted by stale A: %v", err)
	}
	// A's terminal write must fail; the run stays running.
	if err := svc.Finish(ctx, runA, &execution.FinishInput{Status: execution.StatusSucceeded}); err != execution.ErrLostOwnership {
		t.Fatalf("stale A Finish: err=%v, want ErrLostOwnership", err)
	}
	run, _ = svc.GetRun(ctx, runID)
	if run.Status != execution.StatusRunning {
		t.Fatalf("stale A finished B's run: status=%s, want running", run.Status)
	}
	// A's heartbeat renewal must be rejected.
	if ok, err := svc.HeartbeatLeaseFenced(ctx, runID, ownA.Token, 60*time.Second); err != nil || ok {
		t.Fatalf("stale A heartbeat: ok=%v err=%v, want ok=false", ok, err)
	}

	// ── B keeps full ownership. ──
	if err := svc.AppendEventFenced(ctx, runB, execution.EventContentDelta, map[string]any{"text": "fresh"}); err != nil {
		t.Fatalf("B AppendEvent: %v", err)
	}
	if ok, err := svc.HeartbeatLeaseFenced(ctx, runID, ownB.Token, 60*time.Second); err != nil || !ok {
		t.Fatalf("B heartbeat: ok=%v err=%v", ok, err)
	}
	if err := svc.Finish(ctx, runB, &execution.FinishInput{
		Status: execution.StatusSucceeded,
		Output: map[string]any{"text": "done"},
	}); err != nil {
		t.Fatalf("B Finish: %v", err)
	}
	run, _ = svc.GetRun(ctx, runID)
	if run.Status != execution.StatusSucceeded {
		t.Fatalf("final status=%s, want succeeded (by B)", run.Status)
	}
	// Lease is cleaned up by B's fenced finish.
	if _, err := svc.Querier().GetLease(ctx, runID.Bytes()); err == nil {
		t.Fatal("lease still present after B's fenced finish")
	}

	// Exactly one content.delta event may exist (B's) — A's never landed.
	events, err := svc.ListEventsAfter(ctx, runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	deltas := 0
	for _, ev := range events {
		if ev.EventType == execution.EventContentDelta {
			deltas++
		}
	}
	if deltas != 1 {
		t.Fatalf("persisted deltas = %d, want exactly 1 (B's only)", deltas)
	}
}

// TestHeartbeatFencedByToken: even an identical recycled worker id cannot
// renew a lease it does not own — the fence is the token, not the id.
func TestHeartbeatFencedByToken(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	runID := seedRun(t, svc, "feishu_aily")

	own, won, err := svc.ClaimAndLease(ctx, runID, "same-id", 60*time.Second)
	if err != nil || !won {
		t.Fatalf("claim: won=%v err=%v", won, err)
	}
	wrong := ids.New()
	if ok, err := svc.HeartbeatLeaseFenced(ctx, runID, wrong, 60*time.Second); err != nil || ok {
		t.Fatalf("wrong token heartbeat: ok=%v err=%v, want ok=false", ok, err)
	}
	if ok, err := svc.HeartbeatLeaseFenced(ctx, runID, own.Token, 60*time.Second); err != nil || !ok {
		t.Fatalf("owner heartbeat: ok=%v err=%v, want ok=true", ok, err)
	}
}
