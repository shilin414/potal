package integration

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/execution"
	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/dbtypes"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// recoverRun drives the reaper until the given run is no longer running, i.e.
// until the expired lease that belongs to it has been recovered. The reaper
// works in bounded batches and the shared dev database accumulates expired
// leases from earlier runs, so a single RecoverExpiredLeases call does not
// deterministically reach one specific run. Returns the total number of runs
// recovered along the way.
func recoverRun(t *testing.T, svc *execution.Service, runID ids.ID) int {
	t.Helper()
	ctx := context.Background()
	recovered := 0
	for i := 0; i < 20; i++ {
		n, err := svc.RecoverExpiredLeases(ctx, 100)
		if err != nil {
			t.Fatalf("reaper: %v", err)
		}
		recovered += n
		run, err := svc.GetRun(ctx, runID)
		if err != nil {
			t.Fatalf("load run %s: %v", runID, err)
		}
		if run.Status != execution.StatusRunning {
			return recovered
		}
	}
	t.Fatalf("run %s was never recovered by the reaper", runID)
	return recovered
}

// seedRun inserts a queued run directly (test fixture).
func dbCreateRunFixture(id []byte, provider string, input, snapshot []byte) db.CreateRunParams {
	return db.CreateRunParams{
		ID:              id,
		Provider:        provider,
		RuntimeType:     "agent",
		Input:           dbtypes.JSONText(input),
		RuntimeSnapshot: dbtypes.JSONText(snapshot),
		MaxAttempts:     3,
	}
}

// dbForceExpireParams moves the lease expiry one second into the past
// (relative to the DB clock), simulating a dead worker.
func dbForceExpireParams(runID []byte) db.HeartbeatLeaseParams {
	return db.HeartbeatLeaseParams{LeaseMicros: -int64(time.Second / time.Microsecond), RunID: runID, WorkerID: "dead-worker"}
}

// leaseRowParams builds a raw lease row (fixture for poisoning the
// UNIQUE(run_id) slot); the epoch is arbitrary for that purpose. Expiry is
// a DB-clock + 60s derived value.
func leaseRowParams(runID ids.ID, workerID string, epoch uint64) db.CreateRunLeaseParams {
	return db.CreateRunLeaseParams{
		RunID:       runID.Bytes(),
		WorkerID:    workerID,
		LeaseToken:  ids.New().Bytes(),
		LeaseEpoch:  epoch,
		LeaseMicros: int64(60 * time.Second / time.Microsecond),
	}
}

// heartbeatExpireParams moves a specific worker's lease into the past
// (relative to the DB clock).
func heartbeatExpireParams(runID []byte, workerID string) db.HeartbeatLeaseParams {
	return db.HeartbeatLeaseParams{
		LeaseMicros: -int64(time.Second / time.Microsecond),
		RunID:       runID,
		WorkerID:    workerID,
	}
}

// outboxFixture builds a run.dispatch outbox row for the relay test.
func outboxFixture(aggregate string, aggregateID []byte, provider string) db.CreateOutboxEventParams {
	payload := fmt.Sprintf(`{"run_id":%q,"provider":%q}`, idsFromBytes(aggregateID), provider)
	return db.CreateOutboxEventParams{
		Aggregate:   aggregate,
		AggregateID: aggregateID,
		EventType:   "run.dispatch",
		Payload:     dbtypes.JSONText([]byte(payload)),
	}
}

// idsFromBytes renders BINARY(16) as a canonical UUID string.
func idsFromBytes(b []byte) string {
	id := ids.ID{}
	_ = id.Scan(b)
	return id.String()
}

// dbFinishRunParams marks a fixture run terminal directly.
func dbFinishRunParams(runID []byte, status, output string) db.CASFinishRunParams {
	return db.CASFinishRunParams{
		Status:         status,
		Output:         dbtypes.JSONText(output),
		ProviderStatus: "Completed",
		ID:             runID,
	}
}

var _ = context.Background
