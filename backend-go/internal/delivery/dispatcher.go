package delivery

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"

	"github.com/creation-agent-studio/backend-go/internal/automation/schedule"
	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/dbtypes"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
)

func marshalJSON(v any) ([]byte, error) { return json.Marshal(v) }

// Dispatcher fans a succeeded scheduled run out to delivery executions.
// It runs inside the worker process right after the winning terminal CAS;
// the UNIQUE (occurrence_id, schedule_delivery_id) barrier keeps repeated
// fan-out (worker crashes, duplicate events) harmless.
type Dispatcher struct {
	DB      *sql.DB
	Log     *slog.Logger
	Metrics *telemetry.Metrics
}

func NewDispatcher(d *sql.DB, log *slog.Logger, m *telemetry.Metrics) *Dispatcher {
	if log == nil {
		log = slog.Default()
	}
	return &Dispatcher{DB: d, Log: log, Metrics: m}
}

// OnRunSucceeded is the execution.Service hook. Never returns an error:
// delivery scheduling failure is logged, it must not fail the AI run.
func (d *Dispatcher) OnRunSucceeded(ctx context.Context, runIDStr string, triggerType string, triggerID int64, senderUserID int64, _ map[string]any) {
	if triggerType != schedule.TriggerTypeScheduled || triggerID == 0 {
		return
	}
	q := db.New(d.DB)
	occ, err := q.GetScheduleOccurrenceByID(ctx, uint64(triggerID))
	if err != nil {
		d.Log.Warn("delivery fan-out: occurrence missing", "occurrence_id", triggerID, "err", err)
		return
	}
	dels, err := q.ListEnabledDeliveriesBySchedule(ctx, occ.ScheduleID)
	if err != nil {
		d.Log.Warn("delivery fan-out: list deliveries failed", "err", err)
		return
	}
	if len(dels) == 0 {
		return
	}

	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		d.Log.Warn("delivery fan-out: begin tx", "err", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	tq := db.New(tx)

	var runID ids.ID
	_ = runID.Scan([]byte(runIDStr))
	runIDBytes := runID.Bytes()

	created := 0
	for _, del := range dels {
		id := ids.New()
		res, err := tq.CreateDeliveryExecution(ctx, db.CreateDeliveryExecutionParams{
			ID:                 id.Bytes(),
			OccurrenceID:       occ.ID,
			RunID:              runIDBytes,
			ScheduleDeliveryID: del.ID,
			SenderUserID:       uint64(senderUserID),
			TargetType:         del.TargetType,
			TargetID:           del.TargetID,
		})
		if err != nil {
			d.Log.Warn("delivery fan-out: insert execution failed",
				"occurrence_id", occ.ID, "delivery", del.ID, "err", err)
			continue
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue // duplicate (occurrence, delivery) — already fanned out
		}
		payload, _ := marshalJSON(map[string]any{
			"delivery_id": id.Hex(),
			"provider":    ProviderKey,
			"channel":     ChannelFeishu,
		})
		if _, err := tq.CreateOutboxEvent(ctx, db.CreateOutboxEventParams{
			Aggregate:   "delivery",
			AggregateID: id.Bytes(),
			EventType:   "delivery.dispatch",
			Payload:     dbtypes.JSONText(payload),
		}); err != nil {
			d.Log.Warn("delivery fan-out: outbox insert failed", "delivery_id", id.Hex(), "err", err)
			continue
		}
		created++
	}
	if err := tx.Commit(); err != nil {
		d.Log.Warn("delivery fan-out: commit failed", "err", err)
		return
	}
	if created > 0 {
		d.Log.Info("delivery fan-out", "occurrence_id", occ.ID, "created", created)
	}
}
