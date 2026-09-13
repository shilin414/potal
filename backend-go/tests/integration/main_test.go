package integration

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/dbtypes"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
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

// dbForceExpireParams rewrites the lease expiry into the past, simulating
// a dead worker.
func dbForceExpireParams(runID []byte) db.HeartbeatLeaseParams {
	past := time.Now().UTC().Add(-1 * time.Second)
	return db.HeartbeatLeaseParams{ExpiresAt: past, RunID: runID, WorkerID: "dead-worker"}
}

// heartbeatExpireParams pushes a specific worker's lease into the past.
func heartbeatExpireParams(past time.Time, runID []byte, workerID string) db.HeartbeatLeaseParams {
	return db.HeartbeatLeaseParams{ExpiresAt: past, RunID: runID, WorkerID: workerID}
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
