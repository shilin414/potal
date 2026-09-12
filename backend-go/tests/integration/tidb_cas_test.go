// Package integration contains opt-in tests against the real TiDB and
// Redis (STUDIO_TEST_TIDB=1, STUDIO_TEST_REDIS=1).
//
// These prove the correctness core of the execution plane on the real
// database — the exact class of bugs SQLite-style unit tests hide.
package integration

import (
	"context"
	"fmt"
	"os"
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
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
)

func testEnv(t *testing.T) (*execution.Service, *redisx.Client) {
	t.Helper()
	if os.Getenv("STUDIO_TEST_TIDB") != "1" {
		t.Skip("set STUDIO_TEST_TIDB=1 (and STUDIO_TEST_REDIS=1) to run TiDB integration tests")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	db, err := database.Open(context.Background(), cfg.Database)
	if err != nil {
		t.Fatalf("tidb: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var rdb *redisx.Client
	if os.Getenv("STUDIO_TEST_REDIS") == "1" {
		rdb, err = redisx.Open(context.Background(), cfg.Redis)
		if err != nil {
			t.Fatalf("redis: %v", err)
		}
		t.Cleanup(func() { _ = rdb.Close() })
	}
	log := testLogger()
	svc := execution.NewService(db, rdb, log, telemetry.NewMetrics("test"))
	return svc, rdb
}

// seedRun inserts a queued run directly (test fixture).
func seedRun(t *testing.T, svc *execution.Service, provider string) ids.ID {
	t.Helper()
	ctx := context.Background()
	runID := ids.New()
	inputJSON := []byte(`{"content":[{"type":"text","text":"t"}],"mode":"interactive"}`)
	snapshotJSON := []byte(`{"external_resource_id":"agent_test"}`)
	_, err := svc.Querier().CreateRun(ctx, dbCreateRunFixture(runID.Bytes(), provider, inputJSON, snapshotJSON))
	if err != nil {
		t.Fatalf("seed run: %v", err)
	}
	return runID
}

// TestCASRaceSingleWinner: 100 concurrent workers CAS-claim the same
// queued run — exactly one may win (no double execution, ever).
func TestCASRaceSingleWinner(t *testing.T) {
	svc, _ := testEnv(t)
	runID := seedRun(t, svc, "feishu_aily")

	const workers = 100
	var wins atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			won, err := svc.CASClaim(context.Background(), runID)
			if err != nil {
				return
			}
			if won {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("CAS race: %d workers won, want exactly 1", wins.Load())
	}
	run, err := svc.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != execution.StatusRunning || run.Attempt != 1 {
		t.Fatalf("status=%s attempt=%d, want running/1", run.Status, run.Attempt)
	}
}

// TestDuplicateQueueMessagesNoDoubleExecution: the handler runs at most
// once even when the same run id is dispatched twice (at-least-once
// outbox/queue delivery must be absorbed by the CAS).
func TestDuplicateQueueMessagesNoDoubleExecution(t *testing.T) {
	svc, rdb := testEnv(t)
	if rdb == nil {
		t.Skip("needs STUDIO_TEST_REDIS=1")
	}
	// A dedicated provider keeps the test hermetic: a live studio-worker
	// (same queue stream, fallback scan) would otherwise claim the run.
	const provider = "itest_provider"
	runID := seedRun(t, svc, provider)

	execCalls := atomic.Int64{}
	group := fmt.Sprintf("itest-%d", time.Now().UnixNano())
	stream := execution.QueueStream(rdb, provider)
	// Pre-create the group so the published messages are readable by ">".
	setupCtx, cancelSetup := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelSetup()
	if err := rdb.XGroupCreateMkStream(setupCtx, stream, group, "0").Err(); err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
		t.Fatalf("create group: %v", err)
	}
	w := &execution.Worker{
		Svc:          svc,
		RDB:          rdb,
		Provider:     provider,
		WorkerID:     "test-dup-worker",
		Group:        group,
		Handler:      countingHandler{calls: &execCalls, svc: svc},
		Concurrency:  4,
		Lease:        30 * time.Second,
		Heartbeat:    5 * time.Second,
		ScanEvery:    0, // no fallback scan: keep this test queue-only
		ReclaimAfter: 2 * time.Second,
		Log:          testLogger(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	// Dispatch the same run twice through the queue.
	for i := 0; i < 2; i++ {
		if err := rdb.XAdd(ctx, &goredis.XAddArgs{
			Stream: stream,
			Values: map[string]any{"run_id": runID.String()},
		}).Err(); err != nil {
			t.Fatalf("xadd: %v", err)
		}
	}
	// Probe: a manual read must see the messages (stream/group sanity).
	// A throwaway group — reading from the test group would steal them.
	if err := rdb.XGroupCreateMkStream(ctx, stream, group+"-probe", "0").Err(); err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
		t.Fatalf("create probe group: %v", err)
	}
	probeRes, err := rdb.XReadGroup(ctx, &goredis.XReadGroupArgs{
		Group:    group + "-probe",
		Consumer: "probe",
		Streams:  []string{stream, ">"},
		Count:    2,
	}).Result()
	if err != nil {
		t.Fatalf("probe xreadgroup: %v", err)
	}
	probeCount := 0
	for _, st := range probeRes {
		probeCount += len(st.Messages)
	}
	if probeCount < 2 {
		t.Fatalf("probe read %d messages, want 2", probeCount)
	}

	// Diagnose: group state + consumers after a beat.
	time.Sleep(1 * time.Second)
	if groups, err := rdb.XInfoGroups(ctx, stream).Result(); err == nil {
		for _, g := range groups {
			t.Logf("group=%s last-delivered=%s pending=%d consumers=%d", g.Name, g.LastDeliveredID, g.Pending, g.Consumers)
		}
	}
	if consumers, err := rdb.XInfoConsumers(ctx, stream, group).Result(); err == nil {
		for _, c := range consumers {
			t.Logf("consumer=%s pending=%d idle=%s", c.Name, c.Pending, c.Idle)
		}
	}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if execCalls.Load() >= 1 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Allow the second delivery to be processed (and CAS-rejected).
	time.Sleep(3 * time.Second)
	if got := execCalls.Load(); got != 1 {
		run, _ := svc.GetRun(ctx, runID)
		pendingSummary, _ := rdb.XPending(ctx, stream, group).Result()
		status := "?"
		if run != nil {
			status = run.Status
		}
		t.Fatalf("handler executed %d times for one run, want exactly 1 (run status=%s, pending=%v)", got, status, pendingSummary.Count)
	}
}

type countingHandler struct {
	calls *atomic.Int64
	svc   *execution.Service
}

func (h countingHandler) Execute(ctx context.Context, run *execution.Run) error {
	h.calls.Add(1)
	return h.svc.Finish(ctx, run, &execution.FinishInput{
		Status: execution.StatusSucceeded,
		Output: map[string]any{"text": "done"},
	})
}

// TestLeaseExpiryAndReaper: a claimed run whose worker dies (lease never
// renewed) is requeued by the reaper with attempts retained, and fails
// once attempts are exhausted.
func TestLeaseExpiryAndReaper(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	runID := seedRun(t, svc, "feishu_aily")

	if won, err := svc.CASClaim(ctx, runID); err != nil || !won {
		t.Fatalf("claim: won=%v err=%v", won, err)
	}
	if err := svc.AcquireLease(ctx, runID, "dead-worker", 120*time.Second); err != nil {
		t.Fatal(err)
	}
	// Simulate the worker dying: force the lease into the past.
	if _, err := svc.Querier().HeartbeatLease(ctx, dbForceExpireParams(runID.Bytes())); err != nil {
		t.Fatalf("force expire: %v", err)
	}

	recovered, err := svc.RecoverExpiredLeases(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 1 {
		t.Fatalf("reaper recovered %d, want 1", recovered)
	}
	run, err := svc.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != execution.StatusQueued {
		t.Fatalf("status after reaper = %s, want queued (retry)", run.Status)
	}
	if run.Attempt != 1 {
		t.Fatalf("attempt = %d, want retained 1", run.Attempt)
	}

	// Exhaust attempts: claim + expire twice more → third expiry fails it.
	for i := 0; i < 2; i++ {
		won, err := svc.CASClaim(ctx, runID)
		if err != nil || !won {
			t.Fatalf("reclaim %d: won=%v err=%v", i, won, err)
		}
		if err := svc.AcquireLease(ctx, runID, "dead-worker", 120*time.Second); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Querier().HeartbeatLease(ctx, dbForceExpireParams(runID.Bytes())); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.RecoverExpiredLeases(ctx, 10); err != nil {
			t.Fatal(err)
		}
	}
	run, err = svc.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != execution.StatusFailed || run.ErrorCode != "lease_expired" {
		t.Fatalf("final status=%s code=%s, want failed/lease_expired", run.Status, run.ErrorCode)
	}
}
