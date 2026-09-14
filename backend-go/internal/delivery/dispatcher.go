package delivery

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/creation-agent-studio/backend-go/internal/execution"
	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/dbtypes"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
)

func marshalJSON(v any) ([]byte, error) { return json.Marshal(v) }

// Dispatcher turns succeeded scheduled runs into durable delivery
// executions. It runs inside the finalize transaction; no post-commit
// in-memory hook can lose a delivery request.
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

// CreateInTx inserts one execution and one domain outbox row for every
// enabled target. A duplicate (occurrence, target) is idempotent.
func (d *Dispatcher) CreateInTx(ctx context.Context, tx *sql.Tx, run *execution.Run) error {
	if run == nil || run.TriggerID == nil || run.UserID == nil || *run.TriggerID == 0 || *run.UserID == 0 {
		return nil
	}
	q := db.New(tx)
	occ, err := q.GetScheduleOccurrenceByID(ctx, uint64(*run.TriggerID))
	if err != nil {
		return fmt.Errorf("delivery fan-out: occurrence missing: %w", err)
	}
	dels, err := q.ListEnabledDeliveriesBySchedule(ctx, occ.ScheduleID)
	if err != nil {
		return fmt.Errorf("delivery fan-out: list deliveries: %w", err)
	}
	if len(dels) == 0 {
		return nil
	}

	for _, del := range dels {
		id := ids.New()
		res, err := q.CreateDeliveryExecution(ctx, db.CreateDeliveryExecutionParams{
			ID:                 id.Bytes(),
			OccurrenceID:       occ.ID,
			RunID:              run.ID.Bytes(),
			ScheduleDeliveryID: del.ID,
			SenderUserID:       uint64(*run.UserID),
			TargetType:         del.TargetType,
			TargetID:           del.TargetID,
		})
		if err != nil {
			return fmt.Errorf("delivery fan-out: insert execution: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue // duplicate (occurrence, delivery) — already fanned out
		}
		payload, _ := marshalJSON(map[string]any{
			"delivery_id": id.Hex(),
			"provider":    ProviderKey,
			"channel":     ChannelFeishu,
		})
		if _, err := q.CreateOutboxEvent(ctx, db.CreateOutboxEventParams{
			Aggregate:   "delivery",
			AggregateID: id.Bytes(),
			EventType:   "delivery.dispatch",
			Payload:     dbtypes.JSONText(payload),
		}); err != nil {
			return fmt.Errorf("delivery fan-out: insert outbox: %w", err)
		}
		d.Log.Info("delivery request durable", "occurrence_id", occ.ID, "delivery_id", id.Hex())
	}
	return nil
}
