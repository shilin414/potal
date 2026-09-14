package execution

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
	"github.com/creation-agent-studio/backend-go/internal/platform/redisx"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
)

// Handler executes a claimed run for one provider. The worker guarantees
// exactly-once *claiming*; handlers must still be safe to retry.
//
// The handler receives the ClaimedRun — run data plus the immutable
// ExecutionOwnership — and every canonical write it performs must go
// through that ownership (there is no unfenced path available to it).
type Handler interface {
	Execute(ctx context.Context, claimed *ClaimedRun) error
}

// Priority admission classes (P1-1): interactive runs outrank scheduled
// runs on the NORMAL Redis path, and scheduled runs can never starve —
// each class gets its own stream and the worker applies weighted fair
// scheduling across them.
const (
	PriorityClassInteractive = "interactive"
	PriorityClassRetry       = "retry"
	PriorityClassScheduled   = "scheduled"
)

// PriorityClasses lists the consumption order (highest first).
var PriorityClasses = []string{PriorityClassInteractive, PriorityClassRetry, PriorityClassScheduled}

// DefaultPriorityWeights: interactive 7 / retry 1 / scheduled 2 —
// configurable via RUN_PRIORITY_WEIGHTS.
var DefaultPriorityWeights = []int{7, 1, 2}

// AdmissionRequeueDelay is the pause before a run rejected by provider
// admission becomes claimable again. It does NOT consume a provider
// attempt (P0-2) and stays independent of the provider-retry backoff.
const AdmissionRequeueDelay = time.Second

// PriorityClassOf maps a run priority to its admission class.
func PriorityClassOf(priority string) string {
	switch priority {
	case "retry":
		return PriorityClassRetry
	case "scheduled", "scheduled_high", "scheduled_normal":
		return PriorityClassScheduled
	default:
		return PriorityClassInteractive
	}
}

// executionControl couples an in-flight run with its ownership fence, its
// ownership-scoped provider slot and a local cancellation handle. Losing
// the lease (heartbeat rejected by the fence) cancels the local execution
// context and releases the attempt's provider slot: the provider stream /
// poll loop stops and the handler must stop writing canonical state.
type executionControl struct {
	cancel         context.CancelFunc
	own            ExecutionOwnership
	slot           *ProviderSlot
	leaseRenewedAt time.Time
}

// Worker consumes provider queues: Redis Streams wake it up (fast), then
// the TiDB CAS claim makes it correct. A fallback scan (FallbackScan)
// covers messages lost to Redis restarts.
//
// Queue layout: one stream per priority class
// (queue:<provider>:interactive|retry|scheduled) consumed under weighted
// fair scheduling, so interactive traffic jumps ahead of a scheduled
// backlog while scheduled/retry work keeps a guaranteed share.
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
	// ProviderSlots caps provider-wide concurrent runs across worker
	// instances. Slots are durable in TiDB and ownership-scoped, so the cap
	// survives Redis restarts and worker clock skew (Phase 2).
	ProviderSlots *ProviderSlots
	// PriorityWeights per class [interactive, retry, scheduled]; zero
	// value falls back to DefaultPriorityWeights.
	PriorityWeights []int

	mu       sync.Mutex
	inflight map[ids.ID]*executionControl
}

// classStreams returns the per-priority streams in PriorityClasses order.
func (w *Worker) classStreams() []string {
	out := make([]string, len(PriorityClasses))
	for i, class := range PriorityClasses {
		out[i] = PriorityStream(w.RDB, w.Provider, class)
	}
	return out
}

func (w *Worker) priorityWeights() []int {
	if len(w.PriorityWeights) == len(PriorityClasses) {
		return w.PriorityWeights
	}
	return DefaultPriorityWeights
}

// PriorityStream returns the Redis Stream key of one priority class.
func PriorityStream(rdb *redisx.Client, provider, class string) string {
	return rdb.Key("queue", provider, class)
}

// ensureGroup creates the consumer group idempotently on every class
// stream.
func (w *Worker) ensureGroup(ctx context.Context) {
	for _, stream := range w.classStreams() {
		err := w.RDB.XGroupCreateMkStream(ctx, stream, w.Group, "0").Err()
		if err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
			w.Log.Warn("create consumer group failed", "err", err, "stream", stream)
		}
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

type streamMessage struct {
	stream string
	msg    goredis.XMessage
}

func (w *Worker) loop(ctx context.Context, consumer int) {
	name := w.WorkerID + "-" + itoa(consumer)
	sched := newClassScheduler(w.priorityWeights())
	for {
		if ctx.Err() != nil {
			return
		}
		if sm, ok := w.readWeighted(ctx, name, sched); ok {
			w.process(ctx, sm)
			continue
		}
		// Every class is empty: brief pause before probing again.
		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// readWeighted applies weighted fair scheduling across the class streams
// (policy in classScheduler): credited classes are probed in weight order,
// and an EMPTY class immediately forfeits the rest of its round credit so
// an idle stream (e.g. retry with no failures) can never pin the round open
// and starve another class (Admission Fairness, Phase 1).
func (w *Worker) readWeighted(ctx context.Context, name string, sched *classScheduler) (streamMessage, bool) {
	streams := w.classStreams()
	for _, idx := range sched.order() {
		if idx >= len(streams) {
			continue
		}
		if sm, ok := w.readOne(ctx, name, streams[idx]); ok {
			sched.consume(idx)
			w.recordPriorityDispatch(idx)
			return sm, true
		}
		sched.markEmpty(idx)
	}
	return streamMessage{}, false
}

// recordPriorityDispatch counts the fair-dispatch decisions per class
// (studio_priority_dispatch_total{class}); a sustained skew between classes
// is the signal that the weights or the backlog are off.
func (w *Worker) recordPriorityDispatch(classIndex int) {
	if w.Svc == nil || w.Svc.Metrics == nil || classIndex < 0 || classIndex >= len(PriorityClasses) {
		return
	}
	w.Svc.Metrics.PriorityDispatchTotal.WithLabelValues(PriorityClasses[classIndex]).Inc()
}

// readOne performs a non-blocking XREADGROUP (Count 1) on one stream.
func (w *Worker) readOne(ctx context.Context, name, stream string) (streamMessage, bool) {
	res, err := w.RDB.XReadGroup(ctx, &goredis.XReadGroupArgs{
		Group:    w.Group,
		Consumer: name,
		Streams:  []string{stream, ">"},
		Count:    1,
		Block:    -1, // omit BLOCK → non-blocking
	}).Result()
	if err == goredis.Nil {
		return streamMessage{}, false
	}
	if err != nil {
		if ctx.Err() != nil {
			return streamMessage{}, false
		}
		w.Log.Warn("xreadgroup failed", "err", err, "stream", stream)
		return streamMessage{}, false
	}
	for _, st := range res {
		for _, msg := range st.Messages {
			return streamMessage{stream: st.Stream, msg: msg}, true
		}
	}
	return streamMessage{}, false
}

// process claims and executes one queued message. Duplicate or stale
// messages lose the CAS and are simply ACKed.
func (w *Worker) process(ctx context.Context, sm streamMessage) {
	msg := sm.msg
	runIDRaw := msg.Values["run_id"]
	runIDStr, _ := runIDRaw.(string)
	runID, err := ids.Parse(runIDStr)
	if err != nil {
		w.Log.Warn("unparseable run_id in queue message (acking)", "run_id", runIDStr)
		w.ack(ctx, sm.stream, msg.ID)
		return
	}
	defer func() {
		// A single bad run must never kill the execution plane. A panic
		// before the claim leaves the run queued (nothing happened); a
		// panic after the claim is handled inside execute with the
		// ownership fence. Either way the lease/reaper machinery bounds
		// the damage.
		if rec := recover(); rec != nil {
			w.Log.Error("handler panic recovered", "run_id", runIDStr, "panic", rec)
			w.ack(ctx, sm.stream, msg.ID)
		}
	}()
	w.claimAndExecute(ctx, runID, func() { w.ack(ctx, sm.stream, msg.ID) })
}

// claimAndExecute is the shared claim → execute path for both the Redis
// stream wakeup and the fallback scan. The claim and the lease commit in
// ONE transaction; only the winner (with its immutable ownership) runs.
func (w *Worker) claimAndExecute(ctx context.Context, runID ids.ID, ack func()) {
	// Conservative local monotonic baseline: the DB creates the lease after
	// this instant, so self-fencing from here can only stop early, never keep
	// executing after the confirmed DB TTL.
	claimStartedAt := time.Now()
	claimed, won, err := w.Svc.ClaimRun(ctx, runID, w.WorkerID, w.Lease)
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
	// Claim event: matches the reference protocol (SSE consumers render
	// the streaming bubble from run.started). Fenced: only the owner.
	if err := w.Svc.AppendOwnedEvent(ctx, claimed.Ownership, EventRunStarted, map[string]any{
		"worker_id": w.WorkerID,
		"attempt":   claimed.Run.Attempt,
	}); err != nil {
		if err == ErrLostOwnership {
			// Lost immediately (razor-thin expiry): the reaper/next claim
			// owns the run — stop without executing.
			if ack != nil {
				ack()
			}
			return
		}
		w.Log.Warn("append run.started failed", "run_id", runID.String(), "err", err)
	}
	w.execute(ctx, claimed, claimStartedAt)
	if ack != nil {
		ack()
	}
}

func (w *Worker) execute(ctx context.Context, claimed *ClaimedRun, leaseRenewedAt time.Time) {
	execCtx, cancel := context.WithCancel(ctx)
	// Heartbeat registration: losing the lease cancels execCtx.
	w.trackInflight(claimed.Ownership, cancel, leaseRenewedAt)
	defer w.trackInflight(claimed.Ownership, nil, time.Time{})

	if w.ProviderSlots != nil {
		// Ownership-scoped, durable acquire: the row is keyed by the
		// immutable ownership, so this attempt can never collide with or
		// delete another attempt's slot, and the cap survives Redis loss.
		slot, ok, depth, err := w.ProviderSlots.Acquire(ctx, claimed.Ownership)
		if errors.Is(err, ErrLostOwnership) {
			// The run was reclaimed between the claim and the acquire: this
			// worker must stop without requeueing — the reaper/new owner
			// already owns recovery.
			w.Log.Warn("provider slot acquire rejected: run ownership lost",
				"run_id", claimed.Run.ID.String())
			w.recordAdmission(telemetry.AdmissionLostOwnership)
			return
		}
		if err != nil {
			w.Log.Warn("provider slot store unavailable; requeueing run",
				"run_id", claimed.Run.ID.String(), "err", err)
			w.recordProviderAdmission("provider_inflight_unavailable")
			w.requeueForAdmission(claimed, "provider_inflight_unavailable")
			return
		}
		if !ok {
			w.Log.Warn("provider max_inflight reached; requeueing run",
				"run_id", claimed.Run.ID.String(), "depth", depth)
			w.recordProviderAdmission("provider_inflight_limit")
			w.recordAdmission(telemetry.AdmissionCapacityRejected)
			w.requeueForAdmission(claimed, "provider_inflight_limit")
			return
		}
		w.recordAdmission(telemetry.AdmissionAdmitted)
		w.attachProviderSlot(claimed.Ownership, slot)
		defer func() {
			if err := w.ProviderSlots.Release(context.Background(), slot); err != nil {
				w.Log.Warn("provider slot release failed", "run_id", claimed.Run.ID.String(), "err", err)
			}
		}()
	}

	defer func() {
		if rec := recover(); rec != nil {
			w.Log.Error("handler panic recovered (scan)", "run_id", claimed.Run.ID.String(), "panic", rec)
			// Fenced retry: the panicking worker may still own the run —
			// requeue it for another attempt. ErrLostOwnership (already
			// taken over) is the expected no-op.
			if err := w.Svc.RetryOwnedRun(context.Background(), claimed.Run, claimed.Ownership, "worker panic"); err != nil && err != ErrLostOwnership {
				w.Log.Error("retry after panic failed", "run_id", claimed.Run.ID.String(), "err", err)
			}
		}
	}()
	// Bound the handler by the configured runtime; the lease protects
	// against crashes.
	timeout := w.maxRuntime(claimed.Run)
	timer := time.AfterFunc(timeout, cancel)
	defer timer.Stop()

	if err := w.Handler.Execute(execCtx, claimed); err != nil {
		if err == ErrLostOwnership {
			// Lost the lease to a new owner: expected recovery path, not
			// an error — this worker must simply stop.
			w.Log.Warn("worker lost run ownership", "run_id", claimed.Run.ID.String())
		} else {
			w.Log.Warn("handler error", "run_id", claimed.Run.ID.String(), "err", err)
		}
	}
}

func (w *Worker) recordProviderAdmission(reason string) {
	if w.Svc != nil && w.Svc.Metrics != nil {
		w.Svc.Metrics.ProviderInflightRejected.WithLabelValues(w.Provider, reason).Inc()
	}
}

// recordAdmission classifies one provider admission decision (P3): the
// result label is a closed set so alerts can be defined per outcome.
func (w *Worker) recordAdmission(result string) {
	if w.Svc != nil && w.Svc.Metrics != nil {
		w.Svc.Metrics.ProviderAdmission.WithLabelValues(w.Provider, result).Inc()
	}
}

func (w *Worker) requeueForAdmission(claimed *ClaimedRun, reason string) {
	// Admission contention is a capacity problem, not a provider failure:
	// the run is made claimable again after a short pause (long enough to
	// avoid hot-looping against the saturated provider, short enough that
	// a freed slot is used immediately). Provider-failure retries keep
	// their own backoff (Service.RequeueDelay). The delay is applied by the
	// DB clock in the same transaction as the dispatch outbox row.
	if err := w.Svc.RetryOwnedRunAfter(context.Background(), claimed.Run, claimed.Ownership,
		reason, AdmissionRequeueDelay); err != nil && err != ErrLostOwnership {
		w.Log.Error("requeue after provider admission failed",
			"run_id", claimed.Run.ID.String(), "reason", reason, "err", err)
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

func (w *Worker) ack(ctx context.Context, stream, msgID string) {
	if err := w.RDB.XAck(ctx, stream, w.Group, msgID).Err(); err != nil {
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
			// Reaper: recover crashed workers' leases (atomic per run).
			if n, err := w.Svc.RecoverExpiredLeases(ctx, 100); err == nil && n > 0 {
				w.Log.Info("reaper recovered runs", "count", n)
			}
			// Provider slot hygiene: expired slots are already ignored on
			// every read; this bounds table growth.
			if w.ProviderSlots != nil {
				if n, err := w.ProviderSlots.CleanupExpired(ctx); err == nil && n > 0 {
					w.Log.Info("reaper cleaned expired provider slots", "count", n)
				}
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
	for {
		if ctx.Err() != nil {
			return
		}
		for _, stream := range w.classStreams() {
			w.reclaimStream(ctx, stream, reclaimAfter)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (w *Worker) reclaimStream(ctx context.Context, stream string, reclaimAfter time.Duration) {
	cursor := "0"
	for {
		if ctx.Err() != nil {
			return
		}
		res, next, err := w.RDB.XAutoClaim(ctx, &goredis.XAutoClaimArgs{
			Stream:   stream,
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
			w.Log.Warn("xautoclaim failed", "err", err, "stream", stream)
			return
		}
		for _, msg := range res {
			w.process(ctx, streamMessage{stream: stream, msg: msg})
		}
		// Sweep done: the cursor wrapped back to the start, nothing idle
		// was returned, or the cursor stalled (defensive: never spin).
		if len(res) == 0 || next == "0" || next == "0-0" || next == cursor {
			return
		}
		cursor = next
	}
}

// trackInflight registers an in-flight run with its cancellation handle.
// Passing cancel == nil removes it (deferred cleanup).
func (w *Worker) trackInflight(own ExecutionOwnership, cancel context.CancelFunc, leaseRenewedAt time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.inflight == nil {
		w.inflight = map[ids.ID]*executionControl{}
	}
	if cancel != nil {
		w.inflight[own.RunID] = &executionControl{
			cancel:         cancel,
			own:            own,
			leaseRenewedAt: leaseRenewedAt,
		}
	} else {
		delete(w.inflight, own.RunID)
	}
}

// attachProviderSlot records the attempt's provider slot on the control
// entry so the heartbeat loop renews exactly this slot (never a
// recreated one).
func (w *Worker) attachProviderSlot(own ExecutionOwnership, slot *ProviderSlot) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if ctl, ok := w.inflight[own.RunID]; ok {
		ctl.slot = slot
	}
}

// heartbeatOwned renews the run lease and (when the caller holds one) the
// provider slot. With a slot attached both happen in ONE TiDB transaction
// (§20), so "Run Ownership alive ⇔ Provider Slot alive" is an invariant
// rather than two independently drifting leases. The returned error is
// non-nil only when the run-lease heartbeat itself failed (nothing was
// committed); a lost slot merely reports slotOK=false.
func (w *Worker) heartbeatOwned(ctx context.Context, ctl *executionControl, slot *ProviderSlot) (leaseOK, slotOK bool, err error) {
	if w.ProviderSlots != nil && slot != nil {
		return w.Svc.HeartbeatOwnedWithSlot(ctx, ctl.own, w.Lease, slot, w.ProviderSlots.Lease)
	}
	leaseOK, err = w.Svc.HeartbeatOwned(ctx, ctl.own, w.Lease)
	return leaseOK, true, err
}

// inflightSnapshot is the lock-protected copy of one in-flight control: the
// slot pointer is read while w.mu is held (attachProviderSlot writes it
// under the same lock), so the heartbeat loop never races with the executor.
type inflightSnapshot struct {
	ctl            *executionControl
	slot           *ProviderSlot
	leaseRenewedAt time.Time
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
			snapshot := make(map[ids.ID]inflightSnapshot, len(w.inflight))
			for id, ctl := range w.inflight {
				snapshot[id] = inflightSnapshot{
					ctl:            ctl,
					slot:           ctl.slot,
					leaseRenewedAt: ctl.leaseRenewedAt,
				}
			}
			w.mu.Unlock()
			for id, snap := range snapshot {
				ctl := snap.ctl
				// A successful heartbeat establishes a lease at some point after
				// this conservative monotonic timestamp.
				heartbeatStartedAt := time.Now()
				leaseOK, slotOK, err := w.heartbeatOwned(ctx, ctl, snap.slot)
				deadlineElapsed := leaseRenewalDeadlineElapsed(
					snap.leaseRenewedAt, w.Lease, time.Now())
				cancelForLoss := shouldCancelAfterHeartbeat(leaseOK, err, deadlineElapsed)
				if err != nil && !cancelForLoss {
					// A transport/write-conflict failure does not prove ownership
					// loss before the last confirmed TTL. Keep the provider
					// execution and slot until the next tick; the local monotonic
					// deadline self-fences if the outage spans the full lease.
					w.Log.Warn("heartbeat failed — ownership unconfirmed; retrying",
						"run_id", id.String(), "err", err)
					continue
				}
				if cancelForLoss {
					// Ownership lost: stop the local execution immediately
					// (the provider call cannot be cancelled remotely, but
					// this worker loses the right to write canonical state —
					// fenced writes will reject it anyway; cancelling here
					// also stops pointless polling).
					reason := "heartbeat fence rejected ownership"
					if err != nil {
						reason = "heartbeat unavailable past local lease deadline"
					}
					w.Log.Warn("heartbeat lost lease — cancelling local execution",
						"run_id", id.String(), "reason", reason, "err", err)
					ctl.cancel()
					w.mu.Lock()
					// Only remove if the entry is still ours (not re-registered).
					if cur, ok := w.inflight[id]; ok && cur == ctl {
						delete(w.inflight, id)
					}
					w.mu.Unlock()
					// Release this attempt's own provider slot (never the
					// new owner's — the row is ownership-scoped).
					if snap.slot != nil && w.ProviderSlots != nil {
						if rerr := w.ProviderSlots.Release(context.Background(), snap.slot); rerr != nil && rerr != ErrProviderSlotLost {
							w.Log.Warn("provider slot release after lease loss failed",
								"run_id", id.String(), "err", rerr)
						}
					}
					if w.Svc.Metrics != nil {
						w.Svc.Metrics.LeaseExpired.Inc()
					}
					continue
				}
				w.recordHeartbeatSuccess(id, ctl, heartbeatStartedAt)
				if !slotOK {
					// The slot was released or expired (worker stalled longer
					// than the slot lease). The run ownership fence is
					// unaffected; capacity accounting self-heals when this
					// attempt finishes (release is idempotent).
					w.recordProviderAdmission("provider_slot_lost")
					w.recordAdmission(telemetry.AdmissionProviderSlotLost)
				}
			}
		}
	}
}

// recordHeartbeatSuccess advances the monotonic self-fencing deadline only
// for the same in-flight attempt. Using the pre-request instant is
// conservative relative to the DB timestamp written during the request.
func (w *Worker) recordHeartbeatSuccess(id ids.ID, ctl *executionControl, renewedAt time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if cur, ok := w.inflight[id]; ok && cur == ctl {
		cur.leaseRenewedAt = renewedAt
	}
}

// shouldCancelAfterHeartbeat distinguishes a confirmed fence rejection from
// an inconclusive infrastructure error. Only a successful DB round-trip that
// reports leaseOK=false proves this worker is stale. Infrastructure errors
// remain inconclusive only until the last confirmed lease TTL; after that
// local monotonic time fails closed so a prolonged outage cannot overlap a
// recovered owner indefinitely.
func shouldCancelAfterHeartbeat(leaseOK bool, err error, leaseDeadlineElapsed bool) bool {
	if err != nil {
		return leaseDeadlineElapsed
	}
	return !leaseOK
}

func leaseRenewalDeadlineElapsed(lastRenewedAt time.Time, lease time.Duration, now time.Time) bool {
	if lastRenewedAt.IsZero() || lease <= 0 {
		return true
	}
	return !now.Before(lastRenewedAt.Add(lease))
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [21]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
