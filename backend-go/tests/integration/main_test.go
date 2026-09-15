package integration

import (
	"context"
	"fmt"
	"io"
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

// silentLogger discards worker chatter. Fault-injection tests deliberately
// break Redis under a live worker, which logs every retry — the test asserts
// on state, not on logs, so the noise is dropped.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
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

// ───────────────────────── provider submission fixtures (第九轮 P0-2) ──
//
// Since 第九轮 the ONLY path that consumes a provider-execution attempt is
// BeginProviderSubmissionOwned, and it answers "may I transmit?" as well as
// "how much budget is left?". Tests must go through it rather than through a
// bare attempt counter, because the extra answer is the safety property under
// test: a submission whose fate is unknown must NOT be transmitted again.
//
// A test that models "the worker reached the provider and then died" has to
// say WHICH outcome the provider gave, so the helpers below make the two
// cases explicit:
//
//	beginSubmission          the outcome is still unknown (in flight / died)
//	beginSubmissionRefused   the provider DEFINITIVELY refused, so a later
//	                         attempt may transmit again

// submissionFixtureHash is the payload identity a fixture submission uses.
func submissionFixtureHash(provider string) []byte {
	return execution.ProviderSubmissionHash(provider, []byte(`{"content":[{"type":"text","text":"itest"}]}`))
}

// beginSubmission consumes one provider-execution attempt and leaves the
// submission IN FLIGHT (state 'sending').
func beginSubmission(t *testing.T, svc *execution.Service, own execution.ExecutionOwnership, provider string) *execution.ProviderSubmission {
	t.Helper()
	sub, err := svc.BeginProviderSubmissionOwned(context.Background(), own, provider, submissionFixtureHash(provider), execution.ResendForbidden)
	if err != nil {
		t.Fatalf("begin provider submission: %v", err)
	}
	return sub
}

// beginSubmissionRefused consumes one attempt and records a DEFINITIVE
// provider refusal, which is what legitimately re-opens the submission for a
// retry (see BeginProviderSubmissionOwned). Tests that need "attempts are
// retained across reclaims" use this, because an unresolved submission can
// never be re-transmitted by design.
func beginSubmissionRefused(t *testing.T, svc *execution.Service, own execution.ExecutionOwnership, provider string) *execution.ProviderSubmission {
	t.Helper()
	sub := beginSubmission(t, svc, own, provider)
	if err := svc.MarkSubmissionStateOwned(context.Background(), own, sub, execution.SubmissionRejected, "itest: refused"); err != nil {
		t.Fatalf("mark submission rejected: %v", err)
	}
	return sub
}

// listAllEvents reads one maximum-size page; the fixtures here never come
// close to the page bound.
func listAllEvents(t *testing.T, svc *execution.Service, runID ids.ID) []execution.EventRecord {
	t.Helper()
	events, err := svc.ListEventsAfter(context.Background(), runID, 0, execution.MaxEventPageSize)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	return events
}
