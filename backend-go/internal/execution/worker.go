package execution

import (
	"context"
	"log/slog"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
	"github.com/creation-agent-studio/backend-go/internal/platform/redisx"
)

// Handler executes a claimed run for one provider. The worker guarantees
// exactly-once *claiming*; handlers must still be safe to retry.
type Handler interface {
	Execute(ctx context.Context, run *Run) error
}

// Worker consumes provider queues: Redis Streams wake it up (fast), then
// the TiDB CAS claim makes it correct. A fallback scan (FallbackScan)
// covers messages lost to Redis restarts.
type Worker struct {
	Svc         *Service
	RDB         *redisx.Client
	Provider    string
	WorkerID    string
	Group       string
	Handler     Handler
	Concurrency int
	Lease       time.Duration
	Heartbeat   time.Duration
	ScanEvery   time.Duration
	// ReclaimAfter: pending queue entries idle longer than this are
	// re-assigned to this worker (crashed-worker recovery; CAS absorbs
	// duplicates). Zero defaults to 60s.
	ReclaimAfter time.Duration
	Log          *slog.Logger

	mu       sync.Mutex
	inflight map[ids.ID]bool
}

func (w *Worker) stream() string { return QueueStream(w.RDB, w.Provider) }

// ensureGroup creates the consumer group idempotently.
func (w *Worker) ensureGroup(ctx context.Context) {
	err := w.RDB.XGroupCreateMkStream(ctx, w.stream(), w.Group, "0").Err()
	if err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
		w.Log.Warn("create consumer group failed", "err", err)
	}
}

// Run blocks until ctx is cancelled, fanning out to concurrency goroutines.
func (w *Worker) Run(ctx context.Context) {
	w.ensureGroup(ctx)
	var wg sync.WaitGroup
	for i := 0; i < w.Concurrency; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			w.loop(ctx, n)
		}(i)
	}
	// Fallback scan + pending-claim sweep.
	if w.ScanEvery > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.scanLoop(ctx)
		}()
	}
	// Reclaim pending queue entries (messages delivered to a worker that
	// died before ACKing).
	wg.Add(1)
	go func() {
		defer wg.Done()
		w.reclaimLoop(ctx)
	}()
	// Heartbeats for in-flight runs.
	wg.Add(1)
	go func() {
		defer wg.Done()
		w.heartbeatLoop(ctx)
	}()
	wg.Wait()
}

func (w *Worker) loop(ctx context.Context, consumer int) {
	name := w.WorkerID + "-" + itoa(consumer)
	for {
		if ctx.Err() != nil {
			return
		}
		res, err := w.RDB.XReadGroup(ctx, &goredis.XReadGroupArgs{
			Group:    w.Group,
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
			w.Log.Warn("xreadgroup failed", "err", err)
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

// process claims and executes one queued message. Duplicate or stale
// messages lose the CAS and are simply ACKed.
func (w *Worker) process(ctx context.Context, msg goredis.XMessage) {
	runIDRaw := msg.Values["run_id"]
	runIDStr, _ := runIDRaw.(string)
	runID, err := ids.Parse(runIDStr)
	if err != nil {
		w.ack(ctx, msg.ID)
		return
	}
	defer func() {
		// A single bad run must never kill the execution plane.
		if rec := recover(); rec != nil {
			w.Log.Error("handler panic recovered", "run_id", runIDStr, "panic", rec)
			w.ack(ctx, msg.ID)
			if run, err := w.Svc.GetRun(context.Background(), runID); err == nil {
				if err := w.Svc.ReleaseInterrupted(context.Background(), run, "worker panic"); err != nil {
					w.Log.Error("release after panic failed", "run_id", runIDStr, "err", err)
				}
			}
		}
	}()
	won, err := w.Svc.CASClaim(ctx, runID)
	if err != nil {
		w.Log.Error("cas claim failed", "run_id", runIDStr, "err", err)
		// Do not ACK on transient DB errors; let pending entries retry.
		return
	}
	if !won {
		// Someone else claimed it (or it moved on): the message is done.
		w.ack(ctx, msg.ID)
		return
	}
	if err := w.Svc.AcquireLease(ctx, runID, w.WorkerID, w.Lease); err != nil {
		w.Log.Error("acquire lease failed", "run_id", runIDStr, "err", err)
		w.ack(ctx, msg.ID)
		return
	}
	run, err := w.Svc.GetRun(ctx, runID)
	if err != nil {
		w.ack(ctx, msg.ID)
		return
	}
	// Claim event: matches the reference protocol (SSE consumers render
	// the streaming bubble from run.started).
	_ = w.Svc.AppendEvent(ctx, runID, EventRunStarted, map[string]any{
		"worker_id": w.WorkerID,
		"attempt":   run.Attempt,
	})
	// Heartbeat registration.
	w.trackInflight(runID, true)
	defer w.trackInflight(runID, false)

	execCtx, cancel := context.WithTimeout(ctx, w.maxRuntime(run))
	defer cancel()

	if err := w.Handler.Execute(execCtx, run); err != nil {
		w.Log.Warn("handler error", "run_id", runIDStr, "err", err)
	}
	w.ack(ctx, msg.ID)
}

func (w *Worker) maxRuntime(run *Run) time.Duration {
	timeout := run.SnapshotInt("timeout_seconds", 300)
	// Give the handler generous room; the lease protects against crashes.
	d := time.Duration(timeout) * time.Second
	if d < w.Lease {
		d = w.Lease
	}
	return d + 30*time.Second
}

func (w *Worker) ack(ctx context.Context, msgID string) {
	if err := w.RDB.XAck(ctx, w.stream(), w.Group, msgID).Err(); err != nil {
		w.Log.Warn("xack failed", "err", err)
	}
}

// scanLoop sweeps queued runs directly from TiDB (recovery path) and
// claims any that have waited too long without a queue message.
func (w *Worker) scanLoop(ctx context.Context) {
	ticker := time.NewTicker(w.ScanEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ids0, err := w.Svc.ClaimCandidates(ctx, w.Provider, 10)
			if err != nil {
				continue
			}
			for _, runID := range ids0 {
				won, err := w.Svc.CASClaim(ctx, runID)
				if err != nil || !won {
					continue
				}
				if err := w.Svc.AcquireLease(ctx, runID, w.WorkerID, w.Lease); err != nil {
					continue
				}
				run, err := w.Svc.GetRun(ctx, runID)
				if err != nil {
					continue
				}
				_ = w.Svc.AppendEvent(ctx, runID, EventRunStarted, map[string]any{
					"worker_id": w.WorkerID,
					"attempt":   run.Attempt,
				})
				w.trackInflight(runID, true)
				func(run *Run) {
					defer w.trackInflight(run.ID, false)
					defer func() {
						if rec := recover(); rec != nil {
							w.Log.Error("handler panic recovered (scan)", "run_id", run.ID.String(), "panic", rec)
							_ = w.Svc.ReleaseInterrupted(context.Background(), run, "worker panic")
						}
					}()
					execCtx, cancel := context.WithTimeout(ctx, w.maxRuntime(run))
					defer cancel()
					if err := w.Handler.Execute(execCtx, run); err != nil {
						w.Log.Warn("handler error (scan)", "run_id", run.ID.String(), "err", err)
					}
				}(run)
			}
			// Reaper: recover crashed workers' leases.
			if n, err := w.Svc.RecoverExpiredLeases(ctx, 100); err == nil && n > 0 {
				w.Log.Info("reaper recovered runs", "count", n)
			}
		}
	}
}

// reclaimLoop re-assigns long-pending stream entries. Messages published
// before the consumer group existed (or delivered to a crashed worker)
// would otherwise sit in the PEL forever — ">" only hands out new ones.
// Re-delivery is safe: the TiDB CAS claim absorbs duplicates.
func (w *Worker) reclaimLoop(ctx context.Context) {
	reclaimAfter := w.ReclaimAfter
	if reclaimAfter <= 0 {
		reclaimAfter = time.Minute
	}
	var cursor string
	for {
		if ctx.Err() != nil {
			return
		}
		res, next, err := w.RDB.XAutoClaim(ctx, &goredis.XAutoClaimArgs{
			Stream:   w.stream(),
			Group:    w.Group,
			Consumer: w.WorkerID + "-reclaim",
			MinIdle:  reclaimAfter,
			Start:    cursor,
			Count:    10,
		}).Result()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		cursor = next
		for _, msg := range res {
			w.process(ctx, msg)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

// inflight tracks leases owned by this process for heartbeat renewal.
func (w *Worker) trackInflight(runID ids.ID, on bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.inflight == nil {
		w.inflight = map[ids.ID]bool{}
	}
	if on {
		w.inflight[runID] = true
	} else {
		delete(w.inflight, runID)
	}
}

func (w *Worker) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(w.Heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.mu.Lock()
			idsSnapshot := make([]ids.ID, 0, len(w.inflight))
			for id := range w.inflight {
				idsSnapshot = append(idsSnapshot, id)
			}
			w.mu.Unlock()
			for _, id := range idsSnapshot {
				ok, err := w.Svc.HeartbeatLease(ctx, id, w.WorkerID, w.Lease)
				if err != nil || !ok {
					w.Log.Warn("heartbeat lost lease", "run_id", id.String(), "err", err)
				}
			}
		}
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}
