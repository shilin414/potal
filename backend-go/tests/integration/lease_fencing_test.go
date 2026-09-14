// Fault-injection integration tests for the execution correctness
// closure iteration (评测复核 + 修复计划):
//
//  1. Outbox relay publishes canonical UUIDs (not raw BINARY(16) bytes).
//  2. Claim + lease are atomic — a lease INSERT failure rolls the claim
//     back; a running run without a lease can never exist.
//  3. Lease fencing chaos — a stale worker (lease expired, run reclaimed
//     by worker B) can neither append events, finalize, retry, persist
//     artifacts, bind sessions, nor drop B's lease.
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
// paused worker) using that worker's identity. The negative delay is applied
// by the DB clock, so the lease is provably expired for the reaper.
func expireLease(t *testing.T, svc *execution.Service, runID ids.ID, workerID string) {
	t.Helper()
	if _, err := svc.Querier().HeartbeatLease(context.Background(), heartbeatExpireParams(runID.Bytes(), workerID)); err != nil {
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

	// Flush pending outbox rows left by earlier tests on the shared dev
	// database so the relay's batch contains exactly our fixture.
	if _, err := svc.DB.ExecContext(ctx,
		`UPDATE outbox_events SET status='published' WHERE status='pending'`); err != nil {
		t.Fatalf("flush outbox: %v", err)
	}

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

// TestClaimRunAtomicity: when the lease INSERT fails (here: a
// pre-existing lease row hits UNIQUE(run_id)), the whole claim rolls back
// — the run must remain queued. The "running without lease, no queue
// message, invisible to reaper" permanent-stuck state is impossible.
func TestClaimRunAtomicity(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	runID := seedRun(t, svc, "itest_closure")

	// Poison the lease slot so the claim transaction's INSERT fails.
	if err := svc.Querier().CreateRunLease(ctx, leaseRowParams(runID, "ghost-worker", 999)); err != nil {
		t.Fatalf("poison lease: %v", err)
	}

	_, won, err := svc.ClaimRun(ctx, runID, "victim-worker", 60*time.Second)
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
	claimed, won, err := svc.ClaimRun(ctx, runID, "worker-ok", 60*time.Second)
	if err != nil || !won {
		t.Fatalf("claim after cleanup: won=%v err=%v", won, err)
	}
	if claimed.Ownership.LeaseEpoch == 0 {
		t.Fatal("claim returned epoch 0 — fencing token must start at 1")
	}
	if claimed.Run.ID != runID {
		t.Fatalf("claimed run id mismatch: %s != %s", claimed.Run.ID, runID)
	}
	run, _ = svc.GetRun(ctx, runID)
	if run.Status != execution.StatusRunning {
		t.Fatalf("status=%s, want running", run.Status)
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
	runID := seedRun(t, svc, "itest_closure")

	// ── Worker A claims (epoch 1). ──
	claimedA, won, err := svc.ClaimRun(ctx, runID, "worker-a", 60*time.Second)
	if err != nil || !won {
		t.Fatalf("A claim: won=%v err=%v", won, err)
	}
	ownA := claimedA.Ownership

	// ── A "pauses": lease expires, reaper requeues. ──
	expireLease(t, svc, runID, "worker-a")
	// Other tests may leave expired leases behind — the reaper sweeps them
	// all until this run's own recovery is observed.
	_ = recoverRun(t, svc, runID)
	run, _ := svc.GetRun(ctx, runID)
	if run.Status != execution.StatusQueued {
		t.Fatalf("after reaper status=%s, want queued", run.Status)
	}

	// ── Worker B reclaims (epoch 2, new token). ──
	claimedB, won, err := svc.ClaimRun(ctx, runID, "worker-b", 60*time.Second)
	if err != nil || !won {
		t.Fatalf("B claim: won=%v err=%v", won, err)
	}
	ownB := claimedB.Ownership
	if ownB.LeaseEpoch <= ownA.LeaseEpoch {
		t.Fatalf("epoch did not increase on reclaim: A=%d B=%d", ownA.LeaseEpoch, ownB.LeaseEpoch)
	}

	// ── Worker A wakes up — every write must be fenced out. ──
	if err := svc.AppendOwnedEvent(ctx, ownA, execution.EventContentDelta, map[string]any{"text": "stale"}); err != execution.ErrLostOwnership {
		t.Fatalf("stale A AppendEvent: err=%v, want ErrLostOwnership", err)
	}
	if err := svc.CheckOwnership(ctx, runID, ownA.LeaseEpoch); err != execution.ErrLostOwnership {
		t.Fatalf("stale A CheckOwnership: err=%v, want ErrLostOwnership", err)
	}
	if err := svc.UpdateExternalRunIDOwned(ctx, ownA, "chat_stale"); err != execution.ErrLostOwnership {
		t.Fatalf("stale A UpdateExternalRunID: err=%v, want ErrLostOwnership", err)
	}
	// A's retry must NOT requeue (B is running), must NOT create a retry
	// outbox and must NOT delete B's lease (T2).
	if err := svc.RetryOwnedRun(ctx, claimedA.Run, ownA, "stale worker"); err != execution.ErrLostOwnership {
		t.Fatalf("stale A RetryOwnedRun: err=%v, want ErrLostOwnership", err)
	}
	run, _ = svc.GetRun(ctx, runID)
	if run.Status != execution.StatusRunning {
		t.Fatalf("stale A requeued B's run: status=%s, want running", run.Status)
	}
	if _, err := svc.Querier().GetLease(ctx, runID.Bytes()); err != nil {
		t.Fatalf("B's lease was deleted by stale A: %v", err)
	}
	// A's terminal write must fail; the run stays running.
	if err := svc.FinalizeOwnedRun(ctx, claimedA.Run, ownA, &execution.FinishInput{Status: execution.StatusSucceeded}); err != execution.ErrLostOwnership {
		t.Fatalf("stale A Finalize: err=%v, want ErrLostOwnership", err)
	}
	run, _ = svc.GetRun(ctx, runID)
	if run.Status != execution.StatusRunning {
		t.Fatalf("stale A finished B's run: status=%s, want running", run.Status)
	}
	// A's heartbeat renewal must be rejected.
	if ok, err := svc.HeartbeatOwned(ctx, ownA, 60*time.Second); err != nil || ok {
		t.Fatalf("stale A heartbeat: ok=%v err=%v, want ok=false", ok, err)
	}

	// ── B keeps full ownership. ──
	if err := svc.AppendOwnedEvent(ctx, ownB, execution.EventContentDelta, map[string]any{"text": "fresh"}); err != nil {
		t.Fatalf("B AppendEvent: %v", err)
	}
	if ok, err := svc.HeartbeatOwned(ctx, ownB, 60*time.Second); err != nil || !ok {
		t.Fatalf("B heartbeat: ok=%v err=%v, want ok=true", ok, err)
	}
	if err := svc.FinalizeOwnedRun(ctx, claimedB.Run, ownB, &execution.FinishInput{
		Status: execution.StatusSucceeded,
		Output: map[string]any{"text": "done"},
	}); err != nil {
		t.Fatalf("B Finalize: %v", err)
	}
	run, _ = svc.GetRun(ctx, runID)
	if run.Status != execution.StatusSucceeded {
		t.Fatalf("final status=%s, want succeeded (by B)", run.Status)
	}
	// Lease is cleaned up inside B's finalize transaction.
	if _, err := svc.Querier().GetLease(ctx, runID.Bytes()); err == nil {
		t.Fatal("lease still present after B's finalize")
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
	runID := seedRun(t, svc, "itest_closure")

	claimed, won, err := svc.ClaimRun(ctx, runID, "same-id", 60*time.Second)
	if err != nil || !won {
		t.Fatalf("claim: won=%v err=%v", won, err)
	}
	wrong := execution.ExecutionOwnership{
		RunID:      runID,
		WorkerID:   "same-id",
		LeaseEpoch: claimed.Ownership.LeaseEpoch,
		LeaseToken: ids.New(),
	}
	if ok, err := svc.HeartbeatOwned(ctx, wrong, 60*time.Second); err != nil || ok {
		t.Fatalf("wrong token heartbeat: ok=%v err=%v, want ok=false", ok, err)
	}
	if ok, err := svc.HeartbeatOwned(ctx, claimed.Ownership, 60*time.Second); err != nil || !ok {
		t.Fatalf("owner heartbeat: ok=%v err=%v, want ok=true", ok, err)
	}
}
