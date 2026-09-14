package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

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
	_, _ = svc.RecoverExpiredLeases(ctx, 10)
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
	if _, err := svc.RecoverExpiredLeases(ctx, 10); err != nil {
		t.Fatal(err)
	}
	row, err := svc.Querier().GetScheduleOccurrenceByID(ctx, occ.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != "failed" {
		t.Fatalf("occurrence status=%s, want failed", row.Status)
	}
}
