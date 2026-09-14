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

type occurrenceDeliveryExpectation struct {
	scheduleDeliveryID uint64
	targetType         string
	targetID           string
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
	expectations := make([]occurrenceDeliveryExpectation, 0)
	if occ.DeliverySnapshotAt.Valid {
		rows, err := q.ListOccurrenceDeliveryExpectations(ctx, occ.ID)
		if err != nil {
			return fmt.Errorf("delivery fan-out: list occurrence expectations: %w", err)
		}
		for _, row := range rows {
			expectations = append(expectations, occurrenceDeliveryExpectation{
				scheduleDeliveryID: row.ScheduleDeliveryID,
				targetType:         row.TargetType,
				targetID:           row.TargetID,
			})
		}
	} else {
		// Compatibility for occurrences committed before migration 0012.
		// Their historical policy cannot be reconstructed exactly, so preserve
		// the previous fan-out behavior; all newly created occurrences carry a
		// marker and use the immutable branch above.
		dels, err := q.ListEnabledDeliveriesBySchedule(ctx, occ.ScheduleID)
		if err != nil {
			return fmt.Errorf("delivery fan-out: list legacy deliveries: %w", err)
		}
		for _, del := range dels {
			expectations = append(expectations, occurrenceDeliveryExpectation{
				scheduleDeliveryID: del.ID,
				targetType:         del.TargetType,
				targetID:           del.TargetID,
			})
		}
	}
	if len(expectations) == 0 {
		return nil
	}

	for _, expected := range expectations {
		id := ids.New()
		res, err := q.CreateDeliveryExecution(ctx, db.CreateDeliveryExecutionParams{
			ID:                 id.Bytes(),
			OccurrenceID:       occ.ID,
			RunID:              run.ID.Bytes(),
			ScheduleDeliveryID: expected.scheduleDeliveryID,
			SenderUserID:       uint64(*run.UserID),
			TargetType:         expected.targetType,
			TargetID:           expected.targetID,
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
