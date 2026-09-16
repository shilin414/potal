package execution

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	mysql "github.com/go-sql-driver/mysql"

	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
)

// ErrProviderSlotLost is returned by Renew (and ownership-checked Acquire)
// when the caller's provider slot no longer exists (expired / released /
// superseded). Renew never recreates a missing slot — a stale attempt must
// not resurrect capacity accounting.
var ErrProviderSlotLost = errors.New("execution: provider inflight slot lost")

const maxProviderAdmissionRetries = 8

// DefaultProviderSlotLease bounds how long a crashed worker's slot can pin
// provider capacity when no explicit lease is configured.
const DefaultProviderSlotLease = 2 * time.Minute

// ProviderSlot is the ownership-scoped provider concurrency reservation
// (Admission Fairness & Distributed Lease Hardening, §35 terminology). It is
// derived from the immutable ExecutionOwnership and durable in MySQL: the row
// is unique on (provider, run_id, lease_epoch), so slots of different
// attempts (and different workers) of the same run are distinct — a stale
// worker releasing or renewing its slot can never touch (or delete) the new
// owner's slot.
type ProviderSlot struct {
	Provider   string
	RunID      ids.ID
	LeaseEpoch uint64
	LeaseToken ids.ID
	// Member is the log/metric readable ownership string
	// "{run_id}:{lease_epoch}:{lease_token}".
	Member string
}

func newProviderSlot(provider string, own ExecutionOwnership) *ProviderSlot {
	return &ProviderSlot{
		Provider:   provider,
		RunID:      own.RunID,
		LeaseEpoch: own.LeaseEpoch,
		LeaseToken: own.LeaseToken,
		Member: own.RunID.String() + ":" +
			strconv.FormatUint(own.LeaseEpoch, 10) + ":" + own.LeaseToken.String(),
	}
}

func (s *ProviderSlot) providerOr(def string) string {
	if s == nil || s.Provider == "" {
		return def
	}
	return s.Provider
}

// ProviderSlots is the provider-wide distributed concurrency semaphore.
//
// max_inflight is a SAFETY capacity state, so it lives in MySQL — the
// correctness plane — instead of a transient Redis semaphore: a Redis
// restart / flush / failover used to drop the whole inflight ZSET while the
// real provider calls kept running, and the next workers admitted a fresh
// full batch (real concurrency up to 2×limit). Every decision is a database
// transaction and every timestamp comes from the DB clock:
//
//	Acquire  — serialized per provider by a CONFLICTING WRITE on the shared
//	           admission-lock row, deletes expired slots, is idempotent for
//	           the same ownership, and rejects once the EFFECTIVE count
//	           reaches max
//	Renew    — XX-only: refreshes an existing slot, never recreates one
//	Release  — deletes exactly the caller's own ownership slot
//	expiry   — DB clock + slot lease; a crashed worker's slot self-heals
//
// Effective provider capacity (第九轮补丁 3.3-A) is the DISTINCT union of:
//
//  1. live ownership-scoped provider slots; and
//  2. non-settled runs whose provider submission is sending / unknown /
//     accepted.
//
// Within the configured unresolved-execution grace, this is the conservative
// upper bound used by admission.
//
// The second leg is what makes the bound hold. A provider execution is not
// bounded by its local slot: `provider_submissions` is the durable ledger of
// provider-side side effects (migration 0022), so a request that reached the
// provider and then lost its slot — worker crash after accept, an unknown
// submit outcome parked in waiting_external, an acceptance-persistence
// failure — still occupies real provider concurrency. Counting only the
// live slots under-counted exactly those cases, and the (max+1)-th run was
// then admitted against a provider already running `max` of them.
//
// It is a DISTINCT union over run_id, never a sum: a normally executing run
// holds a slot AND has a sending/accepted submission, and those are ONE
// execution. `rejected` (definitively refused — no external action exists)
// and settled runs (the ledger keeps state='accepted' forever as history)
// are excluded, otherwise capacity would leak on every 4xx and pin every
// completed run.
//
// The bound is conservative but not eternal: an unresolved run is settled
// (failed/provider_submit_unknown) once the unresolved-execution grace
// expires, so the reservation is held for the uncertainty window and then
// released. The invariant is therefore "a strict upper bound on every
// locally non-settled provider execution that may still exist", not "the
// provider is permanently bounded no matter what it does behind our back".
// Ownership alive ⇔ Slot alive still holds for the CONTROLLED portion.
type ProviderSlots struct {
	DB          *sql.DB
	Provider    string
	MaxInflight int
	Lease       time.Duration
}

func NewProviderSlots(d *sql.DB, provider string, max int, lease time.Duration) *ProviderSlots {
	if lease <= 0 {
		lease = DefaultProviderSlotLease
	}
	return &ProviderSlots{DB: d, Provider: provider, MaxInflight: max, Lease: lease}
}

func (l *ProviderSlots) leaseMicros() int64 {
	lease := l.Lease
	if lease <= 0 {
		lease = DefaultProviderSlotLease
	}
	micros := lease.Microseconds()
	if micros <= 0 {
		micros = 1
	}
	return micros
}

// Acquire reserves one provider slot for the given ownership.
//
// It refuses stale workers: the run must still be running at the caller's
// lease epoch, otherwise ErrLostOwnership is returned and no slot is taken.
// Re-acquiring with the SAME ownership is idempotent (refreshes the expiry);
// a run re-claimed by a new attempt gets a distinct row and must fit within
// the limit independently. Returns the slot, whether it was admitted and the
// post-decision EFFECTIVE depth (for rejection metrics) — the count of other
// runs' capacity plus this one, never a raw slot count.
func (l *ProviderSlots) Acquire(ctx context.Context, own ExecutionOwnership) (*ProviderSlot, bool, int, error) {
	if l == nil || l.DB == nil || l.MaxInflight <= 0 {
		return nil, true, 0, nil
	}
	if !own.Valid() {
		return nil, false, 0, ErrLostOwnership
	}
	slot := newProviderSlot(l.Provider, own)

	// Concurrent admission decisions serialize on the shared lock row
	// (migration 0013) and losers are retried with a FRESH snapshot by
	// Acquire. Retrying only COMMIT would reuse the stale count and could
	// over-admit, so the whole decision is replayed.
	for attempt := 0; ; attempt++ {
		got, ok, depth, err := l.acquireOnce(ctx, own, slot)
		if err == nil || !isRetryableAdmissionConflict(err) || attempt >= maxProviderAdmissionRetries-1 {
			return got, ok, depth, err
		}
		if ctx.Err() != nil {
			return nil, false, 0, ctx.Err()
		}
	}
}

func (l *ProviderSlots) acquireOnce(ctx context.Context, own ExecutionOwnership, slot *ProviderSlot) (*ProviderSlot, bool, int, error) {
	// Materialize the serialization row before opening the explicit
	// admission transaction. This avoids coupling first-provider creation to
	// the capacity decision; any remaining write conflict is handled by
	// Acquire's full-transaction retry above.
	if err := db.New(l.DB).EnsureProviderAdmissionLock(ctx, l.Provider); err != nil {
		return nil, false, 0, fmt.Errorf("ensure provider admission lock: %w", err)
	}

	tx, err := l.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	q := db.New(tx)

	// 1. Serialize the admission decision per provider with a conflicting
	// WRITE on the shared lock row, so "delete expired → count active →
	// insert" is atomic. A plain locking read is not enough: the read must
	// CONFLICT, otherwise two concurrent decisions both observe the same
	// pre-insert depth and over-admit (migration 0013 documents the CI
	// evidence).
	res, err := q.LockProviderAdmission(ctx, l.Provider)
	if err != nil {
		return nil, false, 0, fmt.Errorf("lock provider admission: %w", err)
	}
	if n, nerr := res.RowsAffected(); nerr != nil {
		return nil, false, 0, nerr
	} else if n != 1 {
		// The serialization row vanished (operator cleanup): fail closed
		// instead of deciding without serialization. The next Acquire
		// re-materializes it through EnsureProviderAdmissionLock.
		return nil, false, 0, fmt.Errorf("lock provider admission: no row for provider %q", l.Provider)
	}

	// 2. Fence under locks: provider -> run -> lease. Recovery uses
	// run -> lease and never takes the provider admission lock, so there is
	// no lock cycle. A concurrent reaper/heartbeat either commits first or
	// conflicts this whole admission transaction, which Acquire retries with
	// a fresh snapshot.
	if _, err := verifyActiveOwnershipTx(ctx, tx, own); err != nil {
		return nil, false, 0, err
	}

	// 3. Expired slots must not pin capacity (DB clock decides expiry).
	if _, err := q.DeleteExpiredProviderSlots(ctx, l.Provider); err != nil {
		return nil, false, 0, err
	}

	// 4. Same ownership already holds a slot → refresh instead of consuming
	// a second one (a worker retrying its own acquire must not leak).
	if _, err := q.GetProviderSlotForUpdate(ctx, db.GetProviderSlotForUpdateParams{
		Provider:   l.Provider,
		RunID:      own.RunID.Bytes(),
		LeaseEpoch: own.LeaseEpoch,
		LeaseToken: own.LeaseToken.Bytes(),
	}); err == nil {
		res, terr := q.TouchProviderSlot(ctx, db.TouchProviderSlotParams{
			LeaseMicros: l.leaseMicros(),
			Provider:    l.Provider,
			RunID:       own.RunID.Bytes(),
			LeaseEpoch:  own.LeaseEpoch,
			LeaseToken:  own.LeaseToken.Bytes(),
		})
		if terr != nil {
			return nil, false, 0, terr
		}
		if n, terr := res.RowsAffected(); terr != nil {
			return nil, false, 0, terr
		} else if n != 1 {
			// The row is locked, so this is unreachable outside data
			// corruption; fail closed rather than over-admit.
			return nil, false, 0, ErrProviderSlotLost
		}
		// Depth is the OTHER runs' effective capacity plus this one: the
		// count excludes this run_id on purpose (the slot just refreshed is
		// this run's own capacity, and its unresolved submission is the same
		// execution). There is deliberately NO max check here — refreshing a
		// slot that already exists consumes no capacity, so a same-ownership
		// re-acquire must never fail; rejecting it would strand a run whose
		// capacity it is already holding.
		other, cerr := q.CountProviderEffectiveInflightExcludingRun(ctx,
			db.CountProviderEffectiveInflightExcludingRunParams{
				Provider:   l.Provider,
				RunID:      own.RunID.Bytes(),
				Provider_2: l.Provider,
				RunID_2:    own.RunID.Bytes(),
			})
		if cerr != nil {
			return nil, false, 0, cerr
		}
		if err := tx.Commit(); err != nil {
			return nil, false, 0, err
		}
		return slot, true, int(other) + 1, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, 0, err
	}

	// 5. Capacity check — the conservative bound over real provider work, as
	// seen by the OTHER runs.
	//
	// Excluding this run is what keeps recovery possible: a run whose
	// submission is still sending/unknown/accepted but whose slot has expired
	// (worker crash, reaper requeue) is ITSELF part of the effective depth. A
	// global count would reject it (1 >= max with max=1) and it could never
	// re-enter the executor to consume the queue entry that parks it in
	// waiting_external — a self-deadlock no timeout can break (§十).
	other, err := q.CountProviderEffectiveInflightExcludingRun(ctx,
		db.CountProviderEffectiveInflightExcludingRunParams{
			Provider:   l.Provider,
			RunID:      own.RunID.Bytes(),
			Provider_2: l.Provider,
			RunID_2:    own.RunID.Bytes(),
		})
	if err != nil {
		return nil, false, 0, err
	}
	if int(other) >= l.MaxInflight {
		// Rejection writes nothing: the deferred rollback discards the
		// serialization write, so concurrent rejections cannot conflict with
		// each other at commit and cannot exhaust the retry budget.
		return nil, false, int(other), nil
	}

	// 6. Admit.
	if err := q.CreateProviderSlot(ctx, db.CreateProviderSlotParams{
		Provider:    l.Provider,
		RunID:       own.RunID.Bytes(),
		LeaseEpoch:  own.LeaseEpoch,
		LeaseToken:  own.LeaseToken.Bytes(),
		WorkerID:    own.WorkerID,
		LeaseMicros: l.leaseMicros(),
	}); err != nil {
		return nil, false, 0, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, 0, err
	}
	return slot, true, int(other) + 1, nil
}

// isRetryableAdmissionConflict reports whether err is an InnoDB
// transaction conflict that a full-decision replay can safely absorb:
//
//	1213 — deadlock victim
//	1205 — lock wait timeout
//
// Both are transient and leave the transaction rolled back, so replaying
// the whole admission decision (never just the commit) is safe.
func isRetryableAdmissionConflict(err error) bool {
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) {
		return false
	}
	switch mysqlErr.Number {
	case 1213, // deadlock victim
		1205: // lock wait timeout
		return true
	default:
		return false
	}
}

// Renew extends the slot's DB-clock lease. XX-only semantics: a missing
// member is ErrProviderSlotLost and is NEVER recreated here — renewing a
// lost slot would let a stale attempt inflate the semaphore's capacity
// accounting.
func (l *ProviderSlots) Renew(ctx context.Context, slot *ProviderSlot) error {
	if l == nil || l.DB == nil || l.MaxInflight <= 0 || slot == nil {
		return nil
	}
	res, err := db.New(l.DB).TouchProviderSlot(ctx, db.TouchProviderSlotParams{
		LeaseMicros: l.leaseMicros(),
		Provider:    slot.providerOr(l.Provider),
		RunID:       slot.RunID.Bytes(),
		LeaseEpoch:  slot.LeaseEpoch,
		LeaseToken:  slot.LeaseToken.Bytes(),
	})
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrProviderSlotLost
	}
	return nil
}

// Release removes exactly this ownership's slot after terminalization. The
// DELETE targets (provider, run_id, epoch, token), so a stale worker's
// deferred release can only remove its OWN (already-irrelevant) slot, never
// the new owner's. Idempotent: releasing twice is a no-op.
func (l *ProviderSlots) Release(ctx context.Context, slot *ProviderSlot) error {
	if l == nil || l.DB == nil || l.MaxInflight <= 0 || slot == nil {
		return nil
	}
	_, err := db.New(l.DB).DeleteProviderSlotByToken(ctx, db.DeleteProviderSlotByTokenParams{
		Provider:   slot.providerOr(l.Provider),
		RunID:      slot.RunID.Bytes(),
		LeaseEpoch: slot.LeaseEpoch,
		LeaseToken: slot.LeaseToken.Bytes(),
	})
	return err
}

// Depth reports the provider's EFFECTIVE inflight: the distinct count of real
// provider executions admission believes exist — live slots union non-settled
// sending/unknown/accepted submissions (see the ProviderSlots doc comment).
//
// It answers "how much of max_inflight does the admission plane consider
// used", which is what a dashboard comparing inflight against real provider
// concurrency needs. ControlledDepth is the narrower "how many of those does
// a live worker own" question.
func (l *ProviderSlots) Depth(ctx context.Context) (int, error) {
	if l == nil || l.DB == nil || l.MaxInflight <= 0 {
		return 0, nil
	}
	n, err := db.New(l.DB).CountProviderEffectiveInflight(ctx,
		db.CountProviderEffectiveInflightParams{
			Provider:   l.Provider,
			Provider_2: l.Provider,
		})
	return int(n), err
}

// ControlledDepth reports the CONTROLLED portion of provider capacity: the
// live (non-expired, DB-clock) ownership-scoped slots a worker currently
// holds. It is the original CountActiveProviderSlots semantics, kept for
// diagnostics — orphan-slot detection and the controlled leg of the capacity
// metrics.
//
// It is NOT a safe admission bound: a provider execution outlives its slot
// whenever a worker crashes after accept or a run parks in waiting_external.
func (l *ProviderSlots) ControlledDepth(ctx context.Context) (int, error) {
	if l == nil || l.DB == nil || l.MaxInflight <= 0 {
		return 0, nil
	}
	n, err := db.New(l.DB).CountActiveProviderSlots(ctx, l.Provider)
	return int(n), err
}

// UncontrolledDepth reports the UNCONTROLLED portion of provider capacity:
// non-settled runs with a sending/unknown/accepted submission but no live
// slot. Diagnostics only, never an admission bound.
//
// Healthy operation keeps this at ~0 (effective ≈ controlled). A sustained
// non-zero value means real provider work is accumulating outside any
// worker's control: a crash between accept and release, waiting_external
// build-up, an acceptance-persistence failure, or reconciliation backlog.
// This is the series to alert on — the effective count stays correct either
// way, but the uncontrolled share is what grows before an incident does.
func (l *ProviderSlots) UncontrolledDepth(ctx context.Context) (int, error) {
	if l == nil || l.DB == nil || l.MaxInflight <= 0 {
		return 0, nil
	}
	n, err := db.New(l.DB).CountProviderUncontrolledInflight(ctx, l.Provider)
	return int(n), err
}

// CleanupExpired removes slots that outlived their DB-clock lease (crashed
// workers). Expiry is already enforced on every read; this only bounds table
// growth.
func (l *ProviderSlots) CleanupExpired(ctx context.Context) (int64, error) {
	if l == nil || l.DB == nil {
		return 0, nil
	}
	res, err := db.New(l.DB).DeleteExpiredProviderSlotsAll(ctx)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return n, err
}
