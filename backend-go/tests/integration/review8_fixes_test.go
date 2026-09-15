package integration

// 第八轮复审整改验证 (P1: merged heartbeat 的 lease/slot 原子性).
//
// The merged heartbeat (HeartbeatOwnedWithSlot) claims "Run Ownership alive
// ⇔ Provider Slot alive", and max_inflight is a SAFETY bound on real provider
// concurrency. The pre-fix implementation broke that claim on its error
// paths: a failing slot renewal (SQL error, or 0 changed rows) was logged and
// then the RUN LEASE EXTENSION WAS COMMITTED ANYWAY, reporting
// leaseOK=true/slotOK=false.
//
// That produces exactly the state the capacity contract forbids:
//
//	run lease     = alive   (worker keeps executing, nothing cancels it)
//	provider call = alive
//	slot          = gone    (capacity accounting already freed it)
//
// The next admission counts the freed capacity, sees depth < max and admits
// a new run, so real provider concurrency becomes max_inflight + 1. The limit
// silently stops being an upper bound.
//
// The fix makes the operation BOTH OR NEITHER: any slot failure rolls the
// lease renewal back too, ErrProviderSlotLost is returned only for a PROVEN
// loss (a successful round-trip reporting 0 changed rows), and every other
// error stays inconclusive until the last confirmed lease TTL.
//
// These tests pin the two halves on the real MySQL 5.7:
//
//	Test 1  a proven slot loss does NOT extend run ownership
//	Test 3  a transient slot failure rolls the lease renewal back
//	        (the "BOTH" half of BOTH OR NEITHER, on a real lock timeout)
//	Test 4  a successful heartbeat still commits both renewals
//
// Test 2 (the worker's decision on ErrProviderSlotLost) is a pure unit test
// in internal/execution/worker_test.go, next to the function it covers.
//
// TestLiveRenewalReportsOneChangedRow (review7_fixes_test.go) is kept: the
// changed-rows semantics this round relies on ("0 rows means the row really
// is gone") is exactly what makes ErrProviderSlotLost strong enough to cancel
// on.

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/platform/config"
	"github.com/creation-agent-studio/backend-go/internal/platform/database"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
)

// Test 1 — a proven slot loss must not extend run ownership.
func TestMergedHeartbeatSlotLossRollsBackLeaseRenewal(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	provider := slotProvider(t)
	slots := newTestSlots(t, svc, provider, 2, time.Minute)

	claimed := claimForSlots(t, svc, provider)
	// The test ends with a leaked owner if it does not clean up (a running
	// run whose reservation is gone), so remove the fixture — without
	// marking it terminal, which would disturb the DATABASE-WIDE migration
	// sweeps (a terminal run with no canonical terminal event).
	deleteRunFixture(t, svc, claimed.Run.ID)
	slot, ok, _, err := slots.Acquire(ctx, claimed.Ownership)
	if err != nil || !ok {
		t.Fatalf("acquire: ok=%v err=%v", ok, err)
	}
	before := leaseTimestamps(t, svc, claimed.Run.ID)

	// The reservation disappears without the worker noticing: operator
	// cleanup, an expired slot reaped by a concurrent admission, ...
	if _, err := svc.DB.ExecContext(ctx,
		`DELETE FROM provider_execution_slots WHERE provider = ? AND run_id = ?`,
		provider, claimed.Run.ID.Bytes()); err != nil {
		t.Fatalf("drop slot: %v", err)
	}

	leaseOK, slotOK, err := svc.HeartbeatOwnedWithSlot(ctx, claimed.Ownership, time.Minute, slot, time.Minute)
	if !errors.Is(err, execution.ErrProviderSlotLost) {
		t.Fatalf("merged heartbeat over a lost slot: err=%v, want ErrProviderSlotLost "+
			"(the round-trip succeeded and reported 0 changed rows)", err)
	}
	if leaseOK || slotOK {
		t.Fatalf("merged heartbeat over a lost slot: leaseOK=%v slotOK=%v, want false/false", leaseOK, slotOK)
	}

	// The heart of the fix: the lease renewal that HeartbeatLeaseFenced
	// performed inside the same transaction must NOT be visible.
	if after := leaseTimestamps(t, svc, claimed.Run.ID); after != before {
		t.Fatalf("run lease moved from %+v to %+v: a proven slot loss must roll the lease "+
			"renewal back (committing it makes real provider concurrency exceed max_inflight)",
			before, after)
	}
	// The attempt still owns the run until its ORIGINAL expiry — the fix
	// removes the extension, not the existing lease (and never requeues: the
	// provider request may already be in flight).
	if !leaseRowAlive(t, svc, claimed.Run.ID.Bytes()) {
		t.Fatal("the fix dropped the attempt's existing lease instead of only the extension")
	}
	if got := activeSlotRows(t, svc, provider); got != 0 {
		t.Fatalf("heartbeat recreated the lost slot: rows=%d", got)
	}
}

// Test 3 — a transient slot failure rolls the run-lease renewal back too.
//
// The report's Test 3: while the merged heartbeat renews the lease, a second
// connection holds an exclusive lock on the slot row. With a one-second
// session lock-wait timeout the lease renewal succeeds first and
// TouchProviderSlot then fails with InnoDB 1205 — the exact "slot SQL error"
// path. Before the fix that committed the lease extension (leaseOK=true,
// slotOK=false); now both must roll back.
//
// A plain infrastructure error means "the DB could not confirm the
// reservation", NOT "the reservation is gone", so nothing is cancelled here:
// the worker keeps its last confirmed TTL and retries next tick.
func TestMergedHeartbeatTransientSlotErrorRollsBackRunLease(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	provider := slotProvider(t)
	slots := newTestSlots(t, svc, provider, 2, time.Minute)

	claimed := claimForSlots(t, svc, provider)
	deleteRunFixture(t, svc, claimed.Run.ID)
	slot, ok, _, err := slots.Acquire(ctx, claimed.Ownership)
	if err != nil || !ok {
		t.Fatalf("acquire: ok=%v err=%v", ok, err)
	}
	before := leaseTimestamps(t, svc, claimed.Run.ID)

	// Connection A: hold an exclusive row lock on the slot for the whole
	// heartbeat, on its own dedicated session.
	holder, err := svc.DB.Conn(ctx)
	if err != nil {
		t.Fatalf("holder conn: %v", err)
	}
	defer func() { _ = holder.Close() }()
	if _, err := holder.ExecContext(ctx, "SET SESSION innodb_lock_wait_timeout = 1"); err != nil {
		// Loud, never skipped: without the timeout the heartbeat would block
		// for the server default (50s) and this test would prove nothing.
		t.Fatalf("holder session needs innodb_lock_wait_timeout control: %v", err)
	}
	holderTx, err := holder.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("holder tx: %v", err)
	}
	var locked int
	if err := holderTx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM provider_execution_slots
		 WHERE provider = ? AND run_id = ? AND lease_epoch = ? AND lease_token = ?
		 FOR UPDATE`,
		slot.Provider, slot.RunID.Bytes(), slot.LeaseEpoch, slot.LeaseToken.Bytes()).Scan(&locked); err != nil {
		t.Fatalf("lock slot row: %v", err)
	}
	if locked != 1 {
		_ = holderTx.Rollback()
		t.Fatalf("locked rows = %d, want 1 (the slot must exist when the heartbeat starts)", locked)
	}

	// The heartbeat runs on its OWN single-connection session, tuned to the
	// same one-second lock wait timeout.
	hb := newSessionTunedService(t, "SET SESSION innodb_lock_wait_timeout = 1")
	leaseOK, slotOK, err := hb.HeartbeatOwnedWithSlot(ctx, claimed.Ownership, time.Minute, slot, time.Minute)
	if err == nil {
		_ = holderTx.Rollback()
		t.Fatal("merged heartbeat succeeded although the slot row was locked by another transaction")
	}
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1205 {
		_ = holderTx.Rollback()
		t.Fatalf("merged heartbeat err=%v, want InnoDB lock wait timeout (1205) — the test's "+
			"injected condition was not the one exercised", err)
	}
	if errors.Is(err, execution.ErrProviderSlotLost) {
		_ = holderTx.Rollback()
		t.Fatal("a lock wait timeout was reported as a PROVEN slot loss; it only means the " +
			"reservation could not be confirmed")
	}
	if leaseOK || slotOK {
		_ = holderTx.Rollback()
		t.Fatalf("merged heartbeat under a slot lock timeout: leaseOK=%v slotOK=%v, want false/false "+
			"(BOTH OR NEITHER — a single-sided commit is the capacity bug)", leaseOK, slotOK)
	}

	if err := holderTx.Rollback(); err != nil {
		t.Fatalf("release holder lock: %v", err)
	}

	if after := leaseTimestamps(t, svc, claimed.Run.ID); after != before {
		t.Fatalf("run lease moved from %+v to %+v: a transient slot error must roll the lease "+
			"renewal back (only the last CONFIRMED TTL may stand)", before, after)
	}
	if !leaseRowAlive(t, svc, claimed.Run.ID.Bytes()) {
		t.Fatal("the transient slot error cost the attempt its confirmed lease")
	}
	// Capacity accounting is untouched: the reservation was never lost.
	if got := slotRowsForRun(t, svc, provider, claimed.Run.ID); got != 1 {
		t.Fatalf("slot rows after a transient error = %d, want 1", got)
	}
}

// Test 4 — the happy path still commits both renewals in one transaction, so
// the fix cannot have been "achieved" by never renewing the lease at all.
func TestMergedHeartbeatCommitsBothRenewals(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	provider := slotProvider(t)
	slots := newTestSlots(t, svc, provider, 2, time.Minute)

	claimed := claimForSlots(t, svc, provider)
	deleteRunFixture(t, svc, claimed.Run.ID)
	slot, ok, _, err := slots.Acquire(ctx, claimed.Ownership)
	if err != nil || !ok {
		t.Fatalf("acquire: ok=%v err=%v", ok, err)
	}
	before := leaseTimestamps(t, svc, claimed.Run.ID)
	beforeSlot := slotExpiresAtMillis(t, svc, slot)

	leaseOK, slotOK, err := svc.HeartbeatOwnedWithSlot(ctx, claimed.Ownership, time.Minute, slot, time.Minute)
	if err != nil || !leaseOK || !slotOK {
		t.Fatalf("merged heartbeat: leaseOK=%v slotOK=%v err=%v, want true/true/nil", leaseOK, slotOK, err)
	}
	if after := leaseTimestamps(t, svc, claimed.Run.ID); after == before {
		t.Fatalf("run lease was not renewed by a successful merged heartbeat (%+v)", before)
	}
	if after := slotExpiresAtMillis(t, svc, slot); after == beforeSlot {
		t.Fatalf("provider slot expiry was not refreshed by a successful merged heartbeat (%d)", beforeSlot)
	}
}

// ── helpers ──

// leaseStamp is a run lease's stored timestamps as exact strings: the same
// value on every read while nobody commits a renewal, so "unchanged" is a
// strict statement (unlike a remaining-TTL probe). heartbeat_at is written
// monotonically by HeartbeatLeaseFenced, so it is the most sensitive half —
// even a renewal that lands in the same millisecond as the previous write
// still advances it by one millisecond.
type leaseStamp struct {
	heartbeatAt string
	expiresAt   string
}

func leaseTimestamps(t *testing.T, svc *execution.Service, runID ids.ID) leaseStamp {
	t.Helper()
	var s leaseStamp
	if err := svc.DB.QueryRowContext(context.Background(),
		`SELECT CAST(heartbeat_at AS CHAR), CAST(expires_at AS CHAR) FROM run_leases WHERE run_id = ?`,
		runID.Bytes()).Scan(&s.heartbeatAt, &s.expiresAt); err != nil {
		t.Fatalf("read lease timestamps: %v", err)
	}
	return s
}

// newSessionTunedService opens a SECOND handle on the same database whose
// single connection carries one SESSION-scoped setting, so a test can inject
// a connection-level condition (a pinned clock, a one-second lock wait
// timeout) predictably. MySQL session variables are connection-scoped, hence
// MaxOpenConns(1): every query must go back to the one connection that has
// the setting.
func newSessionTunedService(t *testing.T, sessionStmt string, args ...any) *execution.Service {
	t.Helper()
	if os.Getenv("STUDIO_TEST_DB") != "1" {
		t.Skip("set STUDIO_TEST_DB=1 to run database integration tests")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	ctx := context.Background()
	d, err := database.Open(ctx, cfg.Database)
	if err != nil {
		t.Fatalf("database: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	d.SetMaxOpenConns(1)
	d.SetMaxIdleConns(1)

	conn, err := d.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer func() { _ = conn.Close() }() // back to the pool WITH the session variable set
	if _, err := conn.ExecContext(ctx, sessionStmt, args...); err != nil {
		t.Fatalf("this test needs session control (%s): %v", sessionStmt, err)
	}
	return execution.NewService(d, nil, silentLogger(), telemetry.NewMetrics("test"))
}
