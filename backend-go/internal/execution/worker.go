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

// executionControl couples an in-flight run with its ownership fence and
// a local cancellation handle. Losing the lease (heartbeat rejected by
// the fence) cancels the local execution context: the provider stream /
// poll loop stops and the handler must stop writing canonical state.
type executionControl struct {
	cancel context.CancelFunc
	token  ids.ID
	epoch  uint64
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
	inflight map[ids.ID]*executionControl
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
		w.Log.Warn("unparseable run_id in queue message (acking)", "run_id", runIDStr)
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
	w.claimAndExecute(ctx, runID, func() { w.ack(ctx, msg.ID) })
}

// claimAndExecute is the shared claim → execute path for both the Redis
// stream wakeup and the fallback scan. The claim and the lease commit in
// ONE transaction; only the winner (with ownership fence) executes.
func (w *Worker) claimAndExecute(ctx context.Context, runID ids.ID, ack func()) {
	ownership, won, err := w.Svc.ClaimAndLease(ctx, runID, w.WorkerID, w.Lease)
	if err != nil {
		w.Log.Error("claim+lease failed", "run_id", runID.String(), "err", err)
		// Do NOT ack on transient DB errors: leave the entry pending so
		// the reclaim loop re-delivers it once the database recovers.
		return
	}
	if !won {
		// Someone else claimed it (or it moved on): the message is done.
		if ack != nil {
			ack()
		}
		return
	}
	run, err := w.Svc.GetRun(ctx, runID)
	if err != nil {
		if ack != nil {
			ack()
		}
		return
	}
	// Claim-time fence capture: every canonical write from here on must
	// match this epoch/token (stale-worker fencing, 评测 P0-3).
	run.LeaseEpoch = ownership.Epoch
	run.LeaseToken = ownership.Token
	// Claim event: matches the reference protocol (SSE consumers render
	// the streaming bubble from run.started).
	_ = w.Svc.AppendEventFenced(ctx, run, EventRunStarted, map[string]any{
		"worker_id": w.WorkerID,
		"attempt":   run.Attempt,
	})
	w.execute(ctx, run)
	if ack != nil {
		ack()
	}
}

func (w *Worker) execute(ctx context.Context, run *Run) {
	execCtx, cancel := context.WithCancel(ctx)
	// Heartbeat registration: losing the lease cancels execCtx.
	w.trackInflight(run, cancel)
	defer w.trackInflight(run, nil)

	defer func() {
		if rec := recover(); rec != nil {
			w.Log.Error("handler panic recovered (scan)", "run_id", run.ID.String(), "panic", rec)
			_ = w.Svc.ReleaseInterruptedFenced(context.Background(), run, "worker panic")
		}
	}()
	// Bound the handler by the configured runtime; the lease protects
	// against crashes.
	timeout := w.maxRuntime(run)
	timer := time.AfterFunc(timeout, cancel)
	defer timer.Stop()

	if err := w.Handler.Execute(execCtx, run); err != nil {
		if err == ErrLostOwnership {
			// Lost the lease to a new owner: expected recovery path, not
			// an error — this worker must simply stop.
			w.Log.Warn("worker lost run ownership", "run_id", run.ID.String())
		} else {
			w.Log.Warn("handler error", "run_id", run.ID.String(), "err", err)
		}
	}
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
				w.claimAndExecute(ctx, runID, nil)
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

// trackInflight registers an in-flight run with its cancellation handle.
// Passing cancel == nil removes it (deferred cleanup).
func (w *Worker) trackInflight(run *Run, cancel context.CancelFunc) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.inflight == nil {
		w.inflight = map[ids.ID]*executionControl{}
	}
	if cancel != nil {
		w.inflight[run.ID] = &executionControl{cancel: cancel, token: run.LeaseToken, epoch: run.LeaseEpoch}
	} else {
		delete(w.inflight, run.ID)
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
			snapshot := make(map[ids.ID]*executionControl, len(w.inflight))
			for id, ctl := range w.inflight {
				snapshot[id] = ctl
			}
			w.mu.Unlock()
			for id, ctl := range snapshot {
				ok, err := w.Svc.HeartbeatLeaseFenced(ctx, id, ctl.token, w.Lease)
				if err != nil || !ok {
					// Ownership lost: stop the local execution immediately
					// (the provider call cannot be cancelled remotely, but
					// this worker loses the right to write canonical state —
					// fenced writes will reject it anyway; cancelling here
					// also stops pointless polling).
					w.Log.Warn("heartbeat lost lease — cancelling local execution",
						"run_id", id.String(), "err", err)
					ctl.cancel()
					w.mu.Lock()
					// Only remove if the entry is still ours (not re-registered).
					if cur, ok := w.inflight[id]; ok && cur == ctl {
						delete(w.inflight, id)
					}
					w.mu.Unlock()
					if w.Svc.Metrics != nil {
						w.Svc.Metrics.LeaseExpired.Inc()
					}
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
