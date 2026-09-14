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
// derived from the immutable ExecutionOwnership and durable in TiDB: the row
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
// max_inflight is a SAFETY capacity state, so it lives in TiDB — the
// correctness plane — instead of a transient Redis semaphore: a Redis
// restart / flush / failover used to drop the whole inflight ZSET while the
// real provider calls kept running, and the next workers admitted a fresh
// full batch (real concurrency up to 2×limit). Every decision is a TiDB
// transaction and every timestamp comes from the DB clock:
//
//	Acquire  — serialized per provider (admission lock row), deletes expired
//	           slots, is idempotent for the same ownership, and rejects once
//	           the active count reaches max
//	Renew    — XX-only: refreshes an existing slot, never recreates one
//	Release  — deletes exactly the caller's own ownership slot
//	expiry   — DB clock + slot lease; a crashed worker's slot self-heals
//
// Invariant (with the merged heartbeat): Run Ownership alive ⇔ Provider Slot
// alive, and the count of active slots is a STRICT upper bound on real
// provider executions — Redis state loss cannot raise it.
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
// post-decision active depth (for rejection metrics).
func (l *ProviderSlots) Acquire(ctx context.Context, own ExecutionOwnership) (*ProviderSlot, bool, int, error) {
	if l == nil || l.DB == nil || l.MaxInflight <= 0 {
		return nil, true, 0, nil
	}
	if !own.Valid() {
		return nil, false, 0, ErrLostOwnership
	}
	slot := newProviderSlot(l.Provider, own)

	// TiDB may run explicit transactions optimistically. Concurrent admission
	// decisions then serialize at commit and losers receive Error 9007. Retry
	// the WHOLE decision with a fresh snapshot; retrying only COMMIT would
	// reuse the stale count and could over-admit. MySQL deadlock/lock-timeout
	// victims use the same safe transaction-boundary retry.
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
	// the capacity decision; any remaining optimistic conflict is handled by
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

	// 1. Serialize the admission decision per provider. The lock row makes
	// "delete expired → count active → insert" atomic on TiDB and MySQL 5.7.
	if _, err := q.LockProviderAdmission(ctx, l.Provider); err != nil {
		return nil, false, 0, fmt.Errorf("lock provider admission: %w", err)
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
		count, cerr := q.CountActiveProviderSlots(ctx, l.Provider)
		if cerr != nil {
			return nil, false, 0, cerr
		}
		if err := tx.Commit(); err != nil {
			return nil, false, 0, err
		}
		return slot, true, int(count), nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, 0, err
	}

	// 5. Capacity check — the strict upper bound.
	count, err := q.CountActiveProviderSlots(ctx, l.Provider)
	if err != nil {
		return nil, false, 0, err
	}
	if int(count) >= l.MaxInflight {
		if err := tx.Commit(); err != nil {
			return nil, false, 0, err
		}
		return nil, false, int(count), nil
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
	return slot, true, int(count) + 1, nil
}

func isRetryableAdmissionConflict(err error) bool {
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) {
		return false
	}
	switch mysqlErr.Number {
	case 9007, // TiDB optimistic write conflict
		1213, // MySQL deadlock victim
		1205: // MySQL lock wait timeout
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

// Depth reports the current active (non-expired, DB-clock) slot count.
func (l *ProviderSlots) Depth(ctx context.Context) (int, error) {
	if l == nil || l.DB == nil || l.MaxInflight <= 0 {
		return 0, nil
	}
	n, err := db.New(l.DB).CountActiveProviderSlots(ctx, l.Provider)
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
