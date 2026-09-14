package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/automation/schedule"
	"github.com/creation-agent-studio/backend-go/internal/delivery"
	"github.com/creation-agent-studio/backend-go/internal/execution"
	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
)

func seedScheduledRun(t *testing.T, svc *execution.Service) (ids.ID, db.ScheduleOccurrence) {
	t.Helper()
	ctx := context.Background()
	runID := seedRun(t, svc, "itest_hardening")
	occ, err := svc.Querier().CreateScheduleOccurrence(ctx, db.CreateScheduleOccurrenceParams{
		ScheduleID:  seedHardeningSchedule(t, svc),
		ScheduledAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create occurrence: %v", err)
	}
	occID, _ := occ.LastInsertId()
	row, err := svc.Querier().GetScheduleOccurrenceByID(ctx, uint64(occID))
	if err != nil {
		t.Fatalf("load occurrence: %v", err)
	}
	if _, err := svc.Querier().MarkOccurrenceQueued(ctx, db.MarkOccurrenceQueuedParams{
		RunID:      sql.NullString{String: string(runID.Bytes()), Valid: true},
		AdmittedAt: sql.NullTime{Time: time.Now().UTC(), Valid: true},
		ID:         uint64(occID),
	}); err != nil {
		t.Fatalf("queue occurrence: %v", err)
	}
	if _, err := svc.DB.ExecContext(ctx,
		`UPDATE runs SET trigger_type='scheduled', trigger_id=?, user_id=42 WHERE id=?`, occID, runID.Bytes()); err != nil {
		t.Fatalf("tag run: %v", err)
	}
	row, err = svc.Querier().GetScheduleOccurrenceByID(ctx, uint64(occID))
	if err != nil {
		t.Fatalf("reload occurrence: %v", err)
	}
	return runID, row
}

func seedHardeningSchedule(t *testing.T, svc *execution.Service) uint64 {
	t.Helper()
	ctx := context.Background()
	payload, _ := json.Marshal(map[string]any{"prompt": "test"})
	res, err := svc.Querier().CreateSchedule(ctx, db.CreateScheduleParams{
		OwnerUserID:   42,
		ApplicationID: 1,
		Name:          fmt.Sprintf("hardening-%d", time.Now().UnixNano()),
		InputPayload:  payload,
		ScheduleType:  "once",
		RunAt:         sql.NullTime{Time: time.Now().UTC(), Valid: true},
		Timezone:      "UTC",
		OverlapPolicy: "queue",
		NextRunAt:     sql.NullTime{Time: time.Now().UTC(), Valid: true},
	})
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	id, _ := res.LastInsertId()
	return uint64(id)
}

func TestTerminalRunRejectsAllStaleWorkerWrites(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	runID, occ := seedScheduledRun(t, svc)

	claimedA, _, err := svc.ClaimRun(ctx, runID, "worker-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	expireLease(t, svc, runID, "worker-a")
	recoverRun(t, svc, runID)
	claimedB, _, err := svc.ClaimRun(ctx, runID, "worker-b", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.UpdateExternalRunIDOwned(ctx, claimedB.Ownership, "chat-b"); err != nil {
		t.Fatalf("owner external id: %v", err)
	}
	if err := svc.UpdateExternalRunIDOwned(ctx, claimedB.Ownership, "chat-b"); err != nil {
		t.Fatalf("idempotent external id: %v", err)
	}
	if err := svc.UpdateExternalRunIDOwned(ctx, claimedB.Ownership, "chat-other"); !errors.Is(err, execution.ErrExternalRunIDConflict) {
		t.Fatalf("external id conflict: err=%v", err)
	}
	if err := svc.FinalizeOwnedRun(ctx, claimedB.Run, claimedB.Ownership, &execution.FinishInput{
		Status: execution.StatusSucceeded,
		Output: map[string]any{"text": "done"},
	}); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	if _, err := svc.PersistArtifactOwned(ctx, claimedA.Ownership, execution.ArtifactInput{ExternalID: "late"}); !errors.Is(err, execution.ErrLostOwnership) {
		t.Fatalf("terminal stale artifact: %v", err)
	}
	if err := svc.BindProviderSessionOwned(ctx, claimedA.Ownership, ids.New(), "late"); !errors.Is(err, execution.ErrLostOwnership) {
		t.Fatalf("terminal stale session: %v", err)
	}
	if err := svc.UpdateExternalRunIDOwned(ctx, claimedA.Ownership, "chat-a"); !errors.Is(err, execution.ErrLostOwnership) {
		t.Fatalf("terminal stale external id: %v", err)
	}
	if err := svc.RetryOwnedRun(ctx, claimedA.Run, claimedA.Ownership, "late"); !errors.Is(err, execution.ErrLostOwnership) {
		t.Fatalf("terminal stale retry: %v", err)
	}

	finalOcc, err := svc.Querier().GetScheduleOccurrenceByID(ctx, occ.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finalOcc.Status != "succeeded" {
		t.Fatalf("occurrence status=%s, want succeeded", finalOcc.Status)
	}
}

func TestFinalizeCreatesDurableDeliveryExecutions(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	runID, occ := seedScheduledRun(t, svc)

	res, err := svc.Querier().UpsertScheduleDelivery(ctx, db.UpsertScheduleDeliveryParams{
		ScheduleID:         occ.ScheduleID,
		Channel:            delivery.ChannelFeishu,
		SenderIdentityMode: delivery.SenderOwnerUser,
		TargetType:         delivery.TargetChat,
		TargetID:           "oc_test",
		TargetName:         "hardening target",
		ContentMode:        delivery.ContentSummary,
		Enabled:            true,
	})
	if err != nil {
		t.Fatal(err)
	}
	delID, _ := res.LastInsertId()

	disp := delivery.NewDispatcher(svc.DB, testLogger(), telemetry.NewMetrics("test"))
	svc.CreateDeliveryExecutionsTx = disp.CreateInTx
	claimed, _, err := svc.ClaimRun(ctx, runID, "delivery-owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.FinalizeOwnedRun(ctx, claimed.Run, claimed.Ownership, &execution.FinishInput{
		Status: execution.StatusSucceeded,
		Output: map[string]any{"text": "deliver me"},
	}); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	rows, err := svc.Querier().ListDeliveryExecutionsByOccurrence(ctx, occ.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ScheduleDeliveryID != uint64(delID) || rows[0].Status != "pending" {
		t.Fatalf("delivery rows=%+v, want one pending row for target", rows)
	}
	var outbox int
	if err := svc.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM outbox_events WHERE aggregate='delivery' AND aggregate_id=? AND event_type='delivery.dispatch'`,
		rows[0].ID).Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if outbox != 1 {
		t.Fatalf("delivery outbox rows=%d, want 1", outbox)
	}
}

// TestOccurrenceDeliveryExpectationDoesNotFollowLaterScheduleChanges proves
// that an occurrence owns a snapshot of the delivery policy that existed
// when it was created. Editing the schedule while the run is executing must
// neither redirect that historical result nor change Invariant L.
func TestOccurrenceDeliveryExpectationDoesNotFollowLaterScheduleChanges(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()
	due := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	scheduleID := env.seedSchedule(t, due, schedule.OverlapQueue)

	original, err := db.New(env.db).UpsertScheduleDelivery(ctx, db.UpsertScheduleDeliveryParams{
		ScheduleID:         uint64(scheduleID),
		Channel:            delivery.ChannelFeishu,
		SenderIdentityMode: delivery.SenderOwnerUser,
		TargetType:         delivery.TargetChat,
		TargetID:           "oc_original",
		TargetName:         "original target",
		ContentMode:        delivery.ContentSummary,
		Enabled:            true,
	})
	if err != nil {
		t.Fatal(err)
	}
	originalID, _ := original.LastInsertId()

	env.schd.ProcessDue(ctx)
	occ, err := db.New(env.db).LatestOccurrenceBySchedule(ctx, uint64(scheduleID))
	if err != nil {
		t.Fatalf("load occurrence: %v", err)
	}
	if !occ.RunID.Valid {
		t.Fatal("created occurrence has no run")
	}

	// Mutate the live schedule policy after the occurrence exists: disable
	// the original destination and add a different one.
	if _, err := env.db.ExecContext(ctx,
		`UPDATE schedule_deliveries SET enabled = 0 WHERE id = ?`, originalID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.New(env.db).UpsertScheduleDelivery(ctx, db.UpsertScheduleDeliveryParams{
		ScheduleID:         uint64(scheduleID),
		Channel:            delivery.ChannelFeishu,
		SenderIdentityMode: delivery.SenderOwnerUser,
		TargetType:         delivery.TargetChat,
		TargetID:           "oc_replacement",
		TargetName:         "replacement target",
		ContentMode:        delivery.ContentSummary,
		Enabled:            true,
	}); err != nil {
		t.Fatal(err)
	}

	runID := ids.ID{}
	if err := runID.Scan([]byte(occ.RunID.String)); err != nil {
		t.Fatalf("parse occurrence run id: %v", err)
	}
	runs := execution.NewService(env.db, nil, testLogger(), telemetry.NewMetrics("test"))
	dispatcher := delivery.NewDispatcher(env.db, testLogger(), telemetry.NewMetrics("test"))
	runs.CreateDeliveryExecutionsTx = dispatcher.CreateInTx
	claimed, won, err := runs.ClaimRun(ctx, runID, "delivery-snapshot-owner", time.Minute)
	if err != nil || !won {
		t.Fatalf("claim occurrence run: won=%v err=%v", won, err)
	}
	if err := runs.FinalizeOwnedRun(ctx, claimed.Run, claimed.Ownership, &execution.FinishInput{
		Status: execution.StatusSucceeded,
		Output: map[string]any{"text": "snapshot result"},
	}); err != nil {
		t.Fatalf("finalize occurrence: %v", err)
	}

	rows, err := db.New(env.db).ListDeliveryExecutionsByOccurrence(ctx, occ.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ScheduleDeliveryID != uint64(originalID) || rows[0].TargetID != "oc_original" {
		t.Fatalf("delivery executions=%+v, want the original occurrence snapshot", rows)
	}
	var missing int
	err = env.db.QueryRowContext(ctx,
		`SELECT COUNT(*)
		 FROM occurrence_delivery_expectations e
		 LEFT JOIN delivery_executions d
		   ON d.occurrence_id = e.occurrence_id
		  AND d.schedule_delivery_id = e.schedule_delivery_id
		 WHERE e.occurrence_id = ? AND d.id IS NULL`, occ.ID).Scan(&missing)
	if err != nil || missing != 0 {
		t.Fatalf("Invariant L after schedule edit: missing=%d err=%v, want 0", missing, err)
	}
}

func TestOccurrenceWithEmptyDeliverySnapshotDoesNotGainLaterTarget(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()
	due := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	scheduleID := env.seedSchedule(t, due, schedule.OverlapQueue)

	// Create the occurrence while the schedule has no delivery targets. The
	// marker must still record an intentional, immutable empty snapshot.
	env.schd.ProcessDue(ctx)
	occ, err := db.New(env.db).LatestOccurrenceBySchedule(ctx, uint64(scheduleID))
	if err != nil {
		t.Fatalf("load occurrence: %v", err)
	}
	if !occ.DeliverySnapshotAt.Valid {
		t.Fatal("occurrence with zero targets did not record an empty delivery snapshot")
	}
	if !occ.RunID.Valid {
		t.Fatal("created occurrence has no run")
	}

	if _, err := db.New(env.db).UpsertScheduleDelivery(ctx, db.UpsertScheduleDeliveryParams{
		ScheduleID:         uint64(scheduleID),
		Channel:            delivery.ChannelFeishu,
		SenderIdentityMode: delivery.SenderOwnerUser,
		TargetType:         delivery.TargetChat,
		TargetID:           "oc_added_later",
		TargetName:         "late target",
		ContentMode:        delivery.ContentSummary,
		Enabled:            true,
	}); err != nil {
		t.Fatal(err)
	}

	runID := ids.ID{}
	if err := runID.Scan([]byte(occ.RunID.String)); err != nil {
		t.Fatalf("parse occurrence run id: %v", err)
	}
	runs := execution.NewService(env.db, nil, testLogger(), telemetry.NewMetrics("test"))
	dispatcher := delivery.NewDispatcher(env.db, testLogger(), telemetry.NewMetrics("test"))
	runs.CreateDeliveryExecutionsTx = dispatcher.CreateInTx
	claimed, won, err := runs.ClaimRun(ctx, runID, "empty-snapshot-owner", time.Minute)
	if err != nil || !won {
		t.Fatalf("claim occurrence run: won=%v err=%v", won, err)
	}
	if err := runs.FinalizeOwnedRun(ctx, claimed.Run, claimed.Ownership, &execution.FinishInput{
		Status: execution.StatusSucceeded,
		Output: map[string]any{"text": "no delivery expected"},
	}); err != nil {
		t.Fatalf("finalize occurrence: %v", err)
	}
	rows, err := db.New(env.db).ListDeliveryExecutionsByOccurrence(ctx, occ.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("late schedule target created %d delivery execution(s) for an empty snapshot", len(rows))
	}
}

func TestReaperFinalFailureConvergesOccurrence(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	runID, occ := seedScheduledRun(t, svc)
	if _, err := svc.DB.ExecContext(ctx,
		`UPDATE runs SET attempt=max_attempts WHERE id=?`, runID.Bytes()); err != nil {
		t.Fatal(err)
	}
	claimed, _, err := svc.ClaimRun(ctx, runID, "dead-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_ = claimed
	if _, err := svc.Querier().HeartbeatLease(ctx, dbForceExpireParams(runID.Bytes())); err != nil {
		t.Fatal(err)
	}
	recoverRun(t, svc, runID)
	row, err := svc.Querier().GetScheduleOccurrenceByID(ctx, occ.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != "failed" {
		t.Fatalf("occurrence status=%s, want failed", row.Status)
	}
}
