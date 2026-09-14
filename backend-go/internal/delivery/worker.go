package delivery

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/creation-agent-studio/backend-go/internal/automation/schedule"
	"github.com/creation-agent-studio/backend-go/internal/execution"
	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
	"github.com/creation-agent-studio/backend-go/internal/platform/redisx"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
)

const (
	group        = "deliverers"
	leaseSeconds = 60 * time.Second // stuck 'sending' rows are reclaimed after this
	reclaimEvery = 20 * time.Second
	scanEvery    = time.Second
	// maxAttemptsV1: deliveries dead-end after this many attempts
	// (exponential backoff + jitter; 429-style errors cool down longer).
	maxAttemptsV1 = 5
)

// Worker consumes the delivery queue. At-least-once by design: the CAS on
// delivery_executions absorbs duplicates, the due-scan loop recovers any
// pending row whose stream message was lost (Redis restart, backlog loss).
type Worker struct {
	DB          *sql.DB
	RDB         *redisx.Client
	WorkerID    string
	Concurrency int
	Sender      Sender
	Limiter     *execution.RateLimiter
	Log         *slog.Logger
	Metrics     *telemetry.Metrics
}

func NewWorker(d *sql.DB, rdb *redisx.Client, workerID string, sender Sender, limiter *execution.RateLimiter, log *slog.Logger, m *telemetry.Metrics) *Worker {
	if log == nil {
		log = slog.Default()
	}
	return &Worker{DB: d, RDB: rdb, WorkerID: workerID, Concurrency: 4,
		Sender: sender, Limiter: limiter, Log: log, Metrics: m}
}

func (w *Worker) stream() string { return w.RDB.Key("queue", ProviderKey) }

// Run starts the consumer pool plus recovery loops until ctx is done.
func (w *Worker) Run(ctx context.Context) {
	if err := w.RDB.XGroupCreateMkStream(ctx, w.stream(), group, "0").Err(); err != nil {
		w.Log.Warn("delivery stream group create failed", "err", err)
	}
	var wg sync.WaitGroup
	n := w.Concurrency
	if n <= 0 {
		n = 4
	}
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w.loop(ctx, i)
		}(i)
	}
	wg.Add(1)
	go func() { defer wg.Done(); w.dueScanLoop(ctx) }()
	wg.Add(1)
	go func() { defer wg.Done(); w.reclaimLoop(ctx) }()
	wg.Wait()
}

func (w *Worker) q(ctx context.Context) db.Querier { return db.New(w.DB) }

func (w *Worker) loop(ctx context.Context, consumer int) {
	name := w.WorkerID + "-delivery-" + strconv.Itoa(consumer)
	for {
		if ctx.Err() != nil {
			return
		}
		res, err := w.RDB.XReadGroup(ctx, &goredis.XReadGroupArgs{
			Group:    group,
			Consumer: name,
			Streams:  []string{w.stream(), ">"},
			Count:    1,
			Block:    2 * time.Second,
		}).Result()
		if err == goredis.Nil {
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			w.Log.Warn("delivery xreadgroup failed", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		for _, stream := range res {
			for _, msg := range stream.Messages {
				w.process(ctx, msg)
			}
		}
	}
}

// process claims and sends one delivery. Duplicate/stale messages lose the
// CAS and are ACKed; send results are always persisted before ACK.
func (w *Worker) process(ctx context.Context, msg goredis.XMessage) {
	raw, _ := msg.Values["run_id"].(string) // outbox relay maps aggregate_id → run_id
	id, err := ids.Parse(raw)
	if err != nil {
		w.ack(ctx, msg.ID)
		return
	}
	defer func() {
		if rec := recover(); rec != nil {
			w.Log.Error("delivery panic recovered", "delivery_id", raw, "panic", rec)
			w.ack(ctx, msg.ID)
		}
	}()

	q := w.q(ctx)
	row, err := q.GetDeliveryExecutionByID(ctx, id.Bytes())
	if err != nil {
		w.ack(ctx, msg.ID) // unknown execution — nothing to do
		return
	}
	if row.Status == schedule.DeliverySucceeded || row.Status == schedule.DeliveryFailed {
		w.ack(ctx, msg.ID)
		return
	}
	res, err := q.CASClaimDelivery(ctx, id.Bytes())
	if err != nil {
		w.Log.Warn("delivery claim failed", "delivery_id", raw, "err", err)
		return // leave unacked; reclaimed later
	}
	if n, _ := res.RowsAffected(); n == 0 {
		w.ack(ctx, msg.ID) // someone else owns it
		return
	}
	row.Attempt++ // CASClaimDelivery incremented attempt in DB

	started := time.Now()
	if err := w.send(ctx, row); err != nil {
		w.handleFailure(ctx, row, err)
	} else if _, err := q.CASFinishDelivery(ctx, db.CASFinishDeliveryParams{
		Status: schedule.DeliverySucceeded,
		ID:     id.Bytes(),
	}); err != nil {
		w.Log.Error("delivery finish failed", "delivery_id", raw, "err", err)
	} else {
		if w.Metrics != nil {
			w.Metrics.DeliveryDuration.WithLabelValues(ChannelFeishu, schedule.DeliverySucceeded).
				Observe(time.Since(started).Seconds())
			w.Metrics.DeliverySendsTotal.
				WithLabelValues(ChannelFeishu, "keyed").Inc()
		}
	}
	w.ack(ctx, msg.ID)
}

// send builds the message then delivers with owner UAT under the
// shared Feishu IM rate limit.
func (w *Worker) send(ctx context.Context, row db.DeliveryExecution) error {
	if w.Limiter != nil {
		if err := w.Limiter.Acquire(ctx); err != nil {
			return err
		}
	}
	occ, err := w.q(ctx).GetScheduleOccurrenceByID(ctx, row.OccurrenceID)
	if err != nil {
		return err
	}
	sch, err := w.q(ctx).GetScheduleByID(ctx, occ.ScheduleID)
	if err != nil {
		return err
	}
	run, err := w.q(ctx).GetRunByID(ctx, row.RunID)
	if err != nil {
		return err
	}
	var output map[string]any
	_ = json.Unmarshal(run.Output, &output)
	answer, _ := output["text"].(string)
	text := BuildDeliveryText(sch.Name, answer)
	// Idempotency key = the DeliveryExecution id: stable across every
	// retry of this delivery, so adapters can dedupe natively where the
	// provider supports it and ops can correlate duplicate sends where it
	// does not (at-least-once external side effect).
	execID := ids.ID(row.ID)
	return w.Sender.Send(ctx, DeliveryRequest{
		ExecutionID:    execID,
		SenderUserID:   int64(row.SenderUserID),
		Target:         Target{Type: row.TargetType, ID: row.TargetID, Content: text},
		IdempotencyKey: execID.String(),
	})
}

// handleFailure requeues with exponential backoff + jitter, or dead-ends
// the delivery after max attempts. 429-style rate errors cool down longer;
// all state lands in TiDB before the message is ACKed.
func (w *Worker) handleFailure(ctx context.Context, row db.DeliveryExecution, sendErr error) {
	attempt := int(row.Attempt)
	code := "send_failed"
	msg := sendErr.Error()
	if strings.Contains(msg, "99991400") || strings.Contains(strings.ToLower(msg), "rate limit") ||
		strings.Contains(strings.ToLower(msg), "too many") {
		code = "rate_limited"
	}
	q := w.q(ctx)
	if attempt >= maxAttemptsV1 {
		if _, err := q.CASFinishDelivery(ctx, db.CASFinishDeliveryParams{
			Status:       schedule.DeliveryFailed,
			ErrorCode:    code,
			ErrorMessage: sqlNullString(msg),
			ID:           row.ID,
		}); err != nil {
			w.Log.Error("delivery dead-end failed", "err", err)
			return
		}
		if w.Metrics != nil {
			w.Metrics.DeliveryFailuresTotal.WithLabelValues(ChannelFeishu, code).Inc()
			w.Metrics.DeliveryDuration.WithLabelValues(ChannelFeishu, schedule.DeliveryFailed).
				Observe(0)
		}
		w.Log.Warn("delivery failed permanently", "delivery_id", hexID(row.ID), "code", code)
		return
	}
	backoff := time.Duration(1<<attempt) * time.Second
	if code == "rate_limited" {
		backoff *= 2
	}
	backoff += time.Duration(rand.Int63n(int64(backoff/4) + 1))
	if backoff > 5*time.Minute {
		backoff = 5 * time.Minute
	}
	// The delay is applied by the DB clock inside RequeueDelivery: the
	// worker only contributes the duration, never an absolute instant.
	if _, err := q.RequeueDelivery(ctx, db.RequeueDeliveryParams{
		BackoffMicros: backoff.Microseconds(),
		ErrorCode:     code,
		ErrorMessage:  sqlNullString(msg),
		ID:            row.ID,
	}); err != nil {
		w.Log.Error("delivery requeue failed", "err", err)
		return
	}
	if w.Metrics != nil {
		w.Metrics.DeliveryRetryTotal.Inc()
	}
	w.Log.Warn("delivery retry scheduled", "delivery_id", hexID(row.ID),
		"attempt", attempt, "code", code, "backoff", backoff.String())
}

// dueScanLoop re-enqueues pending due deliveries whose stream message was
// lost; CAS makes double sends impossible.
func (w *Worker) dueScanLoop(ctx context.Context) {
	ticker := time.NewTicker(scanEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rows, err := w.q(ctx).ListDueDeliveries(ctx, 50)
			if err != nil {
				continue
			}
			for _, row := range rows {
				w.enqueue(ctx, row.ID)
			}
		}
	}
}

// reclaimLoop returns rows stuck in 'sending' (crashed worker) to pending,
// or dead-ends those that exhausted their attempts.
func (w *Worker) reclaimLoop(ctx context.Context) {
	ticker := time.NewTicker(reclaimEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			q := w.q(ctx)
			// "Stuck" is measured from the DB clock: the worker passes only
			// the lease duration (Phase 3).
			if _, err := q.ReclaimStuckDeliveries(ctx, leaseSeconds.Microseconds()); err != nil {
				w.Log.Warn("delivery reclaim failed", "err", err)
			}
			if _, err := q.FailStuckDeliveries(ctx, db.FailStuckDeliveriesParams{
				ErrorMessage: sqlNullString("worker crashed before delivery completed"),
				LeaseMicros:  leaseSeconds.Microseconds(),
			}); err != nil {
				w.Log.Warn("delivery stuck fail failed", "err", err)
			}
		}
	}
}

func (w *Worker) enqueue(ctx context.Context, id []byte) {
	if err := w.RDB.XAdd(ctx, &goredis.XAddArgs{
		Stream: w.stream(),
		MaxLen: 100000,
		Approx: true,
		Values: map[string]any{
			"run_id": ids.ID(id).Hex(),
			"event":  "delivery.dispatch",
		},
	}).Err(); err != nil {
		w.Log.Warn("delivery enqueue failed", "err", err)
	}
}

func (w *Worker) ack(ctx context.Context, msgID string) {
	if err := w.RDB.XAck(ctx, w.stream(), group, msgID).Err(); err != nil {
		w.Log.Warn("delivery ack failed", "msg", msgID, "err", err)
	}
}

func hexID(b []byte) string { return ids.ID(b).Hex() }

func sqlNullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
