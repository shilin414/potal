package integration

// 第七轮复审整改验证 (P2-1).
//
// The sixth round moved studio_run_duration onto the DB clock, but left the
// timestamp read INSIDE the finalize transaction and fatal:
//
//	CASFinishRunFenced
//	→ GetRunForUpdate   ← observation only
//	→ if err != nil { return err }   ← rolls the terminal transition back
//
// So a metrics read that cannot complete could leave a finished run
// `running` with a live lease and no terminal event — the exact opposite of
// the file header's own promise ("metrics are post-commit side effects,
// never correctness"). 第七轮 P2-1 moves the read after the commit and makes
// every failure a warning. These tests prove both halves on the real
// database.

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/creation-agent-studio/backend-go/internal/execution"
	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
)

// ── P2-1: a failing metric read can never roll back terminalization ──

func TestDurationMetricReadFailureDoesNotRollbackFinalize(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	provider := "itest_metric_fail"

	convID := seedConversation(t, svc)
	runID := seedRunWithConversation(t, svc, provider, convID)
	deleteConversationFixture(t, svc, convID)
	deleteRunFixture(t, svc, runID)

	claimed, _, err := svc.ClaimRun(ctx, runID, "metric-worker", time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := svc.MarkRunStartedOwned(ctx, claimed.Ownership); err != nil {
		t.Fatalf("mark started: %v", err)
	}
	// A live provider slot: finalize must clean it even when the metric
	// read explodes (slot cleanup lives in the correctness transaction).
	if err := svc.Querier().CreateProviderSlot(ctx, db.CreateProviderSlotParams{
		Provider:    provider,
		RunID:       runID.Bytes(),
		LeaseEpoch:  claimed.Ownership.LeaseEpoch,
		LeaseToken:  claimed.Ownership.LeaseToken.Bytes(),
		WorkerID:    "metric-worker",
		LeaseMicros: int64(time.Minute / time.Microsecond),
	}); err != nil {
		t.Fatalf("create provider slot: %v", err)
	}

	// The injected failure hits ONLY the duration observation.
	injected := errors.New("injected duration read failure")
	svc.RunDurationTimestamps = func(context.Context, ids.ID) (sql.NullTime, sql.NullTime, error) {
		return sql.NullTime{}, sql.NullTime{}, injected
	}

	err = svc.FinalizeOwnedRun(ctx, claimed.Run, claimed.Ownership, &execution.FinishInput{
		Status:            execution.StatusSucceeded,
		Output:            map[string]any{"text": "answer"},
		ProviderStatus:    "Completed",
		AssistantText:     "answer",
		AssistantMetadata: mustMeta(runID),
	})
	if err != nil {
		t.Fatalf("finalize returned %v — a metrics read must never be able to fail "+
			"terminalization (第七轮 P2-1)", err)
	}

	// Correctness is fully intact — the same invariants a clean finalize
	// establishes, all from the one committed transaction.
	run, err := svc.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != execution.StatusSucceeded {
		t.Fatalf("run.status = %q, want %q: the metric read rolled the terminal "+
			"transition back", run.Status, execution.StatusSucceeded)
	}
	if n := countEvents(t, svc, runID, execution.EventRunCompleted); n != 1 {
		t.Fatalf("run.completed events = %d, want exactly 1", n)
	}
	if leaseExists(t, svc, runID) {
		t.Fatal("terminal run kept its lease")
	}
	if n := countProviderSlots(t, svc, runID); n != 0 {
		t.Fatalf("provider_execution_slots = %d, want 0 (slot cleanup is part of the "+
			"correctness transaction)", n)
	}
	var assistantMessages int64
	if err := svc.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE conversation_id = ? AND role = 'assistant'`,
		convID).Scan(&assistantMessages); err != nil {
		t.Fatal(err)
	}
	if assistantMessages != 1 {
		t.Fatalf("assistant messages = %d, want 1", assistantMessages)
	}

	// The ONLY permitted casualty: the duration sample never happened.
	if n := histogramSampleCount(t, svc, "studio_run_execution_seconds",
		map[string]string{"provider": provider, "status": execution.StatusSucceeded}); n != 0 {
		t.Fatalf("duration samples = %d, want 0: the failed read must be dropped, "+
			"not replaced by a bogus sample", n)
	}
}

// ── P2-1: the observation reads COMMITTED, DB-clock timestamps ──

func TestDurationMetricUsesPersistedDBTimestamps(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	provider := "itest_metric_db_clock"

	convID := seedConversation(t, svc)
	runID := seedRunWithConversation(t, svc, provider, convID)
	deleteConversationFixture(t, svc, convID)
	deleteRunFixture(t, svc, runID)

	claimed, _, err := svc.ClaimRun(ctx, runID, "metric-worker", time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := svc.MarkRunStartedOwned(ctx, claimed.Ownership); err != nil {
		t.Fatalf("mark started: %v", err)
	}

	// The observer runs on its OWN connection, so whatever it reads is the
	// state a concurrent reader sees. Reading `running` would prove the
	// observation still happens inside the (uncommitted) finalize
	// transaction — the 第六轮 shape this round removes.
	var observedStatus string
	var observedErr error
	observed := 0
	svc.RunDurationTimestamps = func(callCtx context.Context, id ids.ID) (sql.NullTime, sql.NullTime, error) {
		observed++
		if err := svc.DB.QueryRowContext(callCtx,
			`SELECT status FROM runs WHERE id = ?`, id.Bytes()).Scan(&observedStatus); err != nil {
			observedErr = err
			return sql.NullTime{}, sql.NullTime{}, err
		}
		row, err := svc.Querier().GetRunTimestamps(callCtx, id.Bytes())
		if err != nil {
			observedErr = err
			return sql.NullTime{}, sql.NullTime{}, err
		}
		if !row.StartedAt.Valid || !row.FinishedAt.Valid {
			observedErr = errors.New("duration bounds are NULL after finalize")
			return sql.NullTime{}, sql.NullTime{}, observedErr
		}
		return row.StartedAt, row.FinishedAt, nil
	}

	if err := svc.FinalizeOwnedRun(ctx, claimed.Run, claimed.Ownership, &execution.FinishInput{
		Status:         execution.StatusSucceeded,
		Output:         map[string]any{"text": "answer"},
		ProviderStatus: "Completed",
	}); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	if observed != 1 {
		t.Fatalf("duration observation calls = %d, want 1", observed)
	}
	if observedErr != nil {
		t.Fatalf("observation failed: %v", observedErr)
	}
	if observedStatus != execution.StatusSucceeded {
		t.Fatalf("run.status seen by the observer = %q, want %q: the duration read "+
			"happens BEFORE the commit (第七轮 P2-1)", observedStatus, execution.StatusSucceeded)
	}
	if n := histogramSampleCount(t, svc, "studio_run_execution_seconds",
		map[string]string{"provider": provider, "status": execution.StatusSucceeded}); n != 1 {
		t.Fatalf("duration samples = %d, want 1 (the DB-clock pair is observable)", n)
	}
}

// ── helpers ──

func countProviderSlots(t *testing.T, svc *execution.Service, runID ids.ID) int64 {
	t.Helper()
	var n int64
	if err := svc.DB.QueryRow(`SELECT COUNT(*) FROM provider_execution_slots WHERE run_id = ?`,
		runID.Bytes()).Scan(&n); err != nil {
		t.Fatalf("count provider slots: %v", err)
	}
	return n
}

// histogramSampleCount reads one label set's sample count straight off the
// registry. prometheus/testutil is a separate Go module, and the whole
// assertion is a gather + filter, so the dependency is not worth adding.
func histogramSampleCount(t *testing.T, svc *execution.Service, name string, labels map[string]string) uint64 {
	t.Helper()
	if svc.Metrics == nil || svc.Metrics.Registry == nil {
		t.Fatal("test env has no metrics registry")
	}
	families, err := svc.Metrics.Registry.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, fam := range families {
		if fam.GetName() != name {
			continue
		}
		for _, m := range fam.GetMetric() {
			if metricHasLabels(m.GetLabel(), labels) {
				return m.GetHistogram().GetSampleCount()
			}
		}
	}
	return 0
}

func metricHasLabels(pairs []*dto.LabelPair, want map[string]string) bool {
	got := make(map[string]string, len(pairs))
	for _, p := range pairs {
		got[p.GetName()] = p.GetValue()
	}
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}
