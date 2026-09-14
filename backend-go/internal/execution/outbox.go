package execution

import (
	"context"
	"encoding/json"
	"time"

	goredis "github.com/redis/go-redis/v9"

	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/dbtypes"
	"github.com/creation-agent-studio/backend-go/internal/platform/redisx"
)

// QueueStream returns the Redis Stream key for a provider dispatch queue.
// It is the LEGACY / default stream: run dispatches that carry no
// priority_class (pre-upgrade rows, delivery dispatches) land here.
func QueueStream(rdb *redisx.Client, provider string) string {
	return rdb.Key("queue", provider)
}

// dispatchTarget resolves the stream a pending outbox row must be
// published to. Run dispatches carry priority_class and are routed to the
// matching per-class stream (P1-1: weighted fair scheduling needs the
// classes separated in Redis, not only in the fallback SQL scan);
// everything else (delivery.dispatch, legacy rows) keeps the single
// provider stream.
func dispatchTarget(rdb *redisx.Client, eventType string, payload dbtypes.JSONText) string {
	var p struct {
		Provider      string `json:"provider"`
		PriorityClass string `json:"priority_class"`
	}
	_ = json.Unmarshal(payload, &p)
	provider := p.Provider
	if provider == "" {
		provider = "unknown"
	}
	if eventType == "run.dispatch" && p.PriorityClass != "" {
		for _, class := range PriorityClasses {
			if class == p.PriorityClass {
				return PriorityStream(rdb, provider, class)
			}
		}
		// Unknown class: fall back to the default stream rather than
		// dropping the dispatch (the fallback scan still covers it).
	}
	return QueueStream(rdb, provider)
}

// Relay publishes pending outbox events to Redis Streams.
//
// Outbox rows and their aggregates commit in one database transaction; this
// relay is the only component allowed to turn them into queue messages.
// It is at-least-once: workers must stay idempotent (they are — CAS).
type Relay struct {
	svc     *Service
	rdb     *redisx.Client
	batch   int
	metrics interface {
		SetOutboxBacklog(float64)
	}
}

func NewRelay(svc *Service, rdb *redisx.Client, batch int) *Relay {
	return &Relay{svc: svc, rdb: rdb, batch: batch}
}

// RunOnce drains up to batch pending outbox events.
func (r *Relay) RunOnce(ctx context.Context) (int, error) {
	rows, err := r.svc.q(ctx).ListPendingOutbox(ctx, int32(r.batch))
	if err != nil {
		return 0, err
	}
	published := 0
	for _, row := range rows {
		stream := dispatchTarget(r.rdb, row.EventType, dbtypes.JSONText(row.Payload))
		// ID boundary (§评测 P0-1): aggregate_id is BINARY(16) — it must
		// cross the transport boundary as a canonical UUID string, never
		// as raw bytes coerced into a Go string (workers ids.Parse it).
		fields := map[string]any{
			"outbox_id": row.ID,
			"run_id":    mustID(row.AggregateID).String(),
			"event":     row.EventType,
		}
		if err := r.rdb.XAdd(ctx, &goredis.XAddArgs{
			Stream: stream,
			MaxLen: 100000,
			Approx: true,
			Values: fields,
		}).Err(); err != nil {
			// Leave pending; retry with backoff on the next tick.
			_ = r.svc.q(ctx).FailOutboxEvent(ctx, db.FailOutboxEventParams{
				LastError: nullText(err.Error()),
				ID:        row.ID,
			})
			continue
		}
		if err := r.svc.q(ctx).MarkOutboxPublished(ctx, row.ID); err != nil {
			return published, err
		}
		published++
	}
	return published, nil
}

// Run loops until the context is cancelled.
func (r *Relay) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			published, err := r.RunOnce(ctx)
			if err != nil {
				r.svc.Log.Warn("outbox relay tick failed", "err", err)
			}
			if r.svc.Metrics != nil {
				if n, err := r.svc.q(ctx).CountPendingOutbox(ctx); err == nil {
					r.svc.Metrics.OutboxBacklog.Set(float64(n))
				}
			}
			_ = published
		}
	}
}
