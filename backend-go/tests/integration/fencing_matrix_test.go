// Execution Fencing Matrix (剩余问题开发执行报告 P2-2): one table that pins,
// for every ownership-fenced operation, what each ownership state may do.
//
//	operation     | current owner | expired lease | stale owner
//	--------------+---------------+---------------+----------------------------
//	heartbeat     | PASS          | REJECT        | REJECT
//	begin attempt | PASS          | REJECT        | REJECT
//	retry         | PASS          | REJECT        | REJECT
//	finalize      | PASS          | REJECT        | REJECT
//	slot acquire  | PASS          | REJECT        | REJECT
//	slot renew    | PASS          | REJECT        | REJECT
//	slot release  | PASS          | own row only  | never the new owner's row
//
// REJECT means ErrLostOwnership (ErrProviderSlotLost for the slot plane).
// "Expired lease" is the long-pause window after the DB-clock lease lapsed
// but before the reaper ran; "stale owner" is the same worker after recovery
// handed the run to a new epoch. Every stale case additionally proves the
// successor's run and provider reservation are untouched.
package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/execution"
)

const (
	legCurrent = "current_owner"
	legExpired = "expired_lease"
	legStale   = "stale_owner"
)

// fenceFixture is one ownership state: the run, the caller's immutable
// ownership plus its slot, and (stale leg) the successor that took over.
type fenceFixture struct {
	provider string
	slots    *execution.ProviderSlots
	own      execution.ExecutionOwnership
	run      *execution.Run
	slot     *execution.ProviderSlot

	succOwn  execution.ExecutionOwnership
	succSlot *execution.ProviderSlot
}

// expireOwnedSlot pushes one attempt's provider slot into the past: after a
// long worker pause the slot lease lapses exactly like the run lease it is
// renewed with.
func expireOwnedSlot(t *testing.T, svc *execution.Service, slot *execution.ProviderSlot) {
	t.Helper()
	if _, err := svc.DB.ExecContext(context.Background(),
		`UPDATE provider_execution_slots SET expires_at = DATE_SUB(CURRENT_TIMESTAMP(3), INTERVAL 1 SECOND)
		 WHERE provider = ? AND run_id = ? AND lease_epoch = ? AND lease_token = ?`,
		slot.Provider, slot.RunID.Bytes(), slot.LeaseEpoch, slot.LeaseToken.Bytes()); err != nil {
		t.Fatalf("expire provider slot: %v", err)
	}
}

// assertSuccessorUntouched proves a rejected stale operation did not leak
// into the recovered ownership: the run still runs at the successor's epoch
// and the successor's capacity reservation still exists.
func (f *fenceFixture) assertSuccessorUntouched(t *testing.T, svc *execution.Service) {
	t.Helper()
	ctx := context.Background()
	run, err := svc.GetRun(ctx, f.run.ID)
	if err != nil {
		t.Fatalf("load run after stale op: %v", err)
	}
	var epoch uint64
	if err := svc.DB.QueryRowContext(ctx,
		`SELECT lease_epoch FROM runs WHERE id = ?`, f.run.ID.Bytes()).Scan(&epoch); err != nil {
		t.Fatalf("read run lease epoch: %v", err)
	}
	if run.Status != execution.StatusRunning || epoch != f.succOwn.LeaseEpoch {
		t.Fatalf("stale operation leaked into the successor's run: status=%s epoch=%d (want running/%d)",
			run.Status, epoch, f.succOwn.LeaseEpoch)
	}
	if got := slotRowsForRun(t, svc, f.provider, f.run.ID); got != 1 {
		t.Fatalf("provider slots for the run = %d after stale op, want 1 (the successor's)", got)
	}
	if err := f.slots.Renew(ctx, f.succSlot); err != nil {
		t.Fatalf("successor slot not renewable after stale op: %v", err)
	}
}

func TestExecutionFencingMatrix(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()

	build := func(t *testing.T, leg string) *fenceFixture {
		t.Helper()
		provider := slotProvider(t) + "_" + leg
		slots := newTestSlots(t, svc, provider, 4, time.Minute)
		claimed := claimForSlots(t, svc, provider)
		slot, ok, _, err := slots.Acquire(ctx, claimed.Ownership)
		if err != nil || !ok {
			t.Fatalf("fixture acquire: ok=%v err=%v", ok, err)
		}
		f := &fenceFixture{
			provider: provider,
			slots:    slots,
			own:      claimed.Ownership,
			run:      claimed.Run,
			slot:     slot,
		}
		switch leg {
		case legCurrent:
		case legExpired:
			expireLease(t, svc, f.run.ID, f.own.WorkerID)
			expireOwnedSlot(t, svc, slot)
		case legStale:
			expireLease(t, svc, f.run.ID, f.own.WorkerID)
			recoverRun(t, svc, f.run.ID)
			succ, won, err := svc.ClaimRun(ctx, f.run.ID, "matrix-successor", time.Minute)
			if err != nil || !won {
				t.Fatalf("successor claim: won=%v err=%v", won, err)
			}
			succSlot, ok, _, err := slots.Acquire(ctx, succ.Ownership)
			if err != nil || !ok {
				t.Fatalf("successor acquire: ok=%v err=%v", ok, err)
			}
			if succ.Ownership.LeaseEpoch <= f.own.LeaseEpoch {
				t.Fatalf("epoch did not advance: %d → %d", f.own.LeaseEpoch, succ.Ownership.LeaseEpoch)
			}
			f.succOwn, f.succSlot = succ.Ownership, succSlot
		default:
			t.Fatalf("unknown leg %q", leg)
		}
		return f
	}

	cases := []struct {
		name string
		run  func(t *testing.T, leg string, f *fenceFixture)
	}{
		{"heartbeat", func(t *testing.T, leg string, f *fenceFixture) {
			ok, err := svc.HeartbeatOwned(ctx, f.own, time.Minute)
			if err != nil {
				t.Fatalf("heartbeat: %v", err)
			}
			if leg == legCurrent && !ok {
				t.Fatal("current owner heartbeat was rejected")
			}
			if leg != legCurrent && ok {
				t.Fatalf("%s heartbeat renewed a dead ownership", leg)
			}
		}},

		{"begin_attempt", func(t *testing.T, leg string, f *fenceFixture) {
			_, err := svc.BeginProviderAttemptOwned(ctx, f.own)
			if leg == legCurrent {
				if err != nil {
					t.Fatalf("current owner begin attempt: %v", err)
				}
				return
			}
			if !errors.Is(err, execution.ErrLostOwnership) {
				t.Fatalf("%s begin attempt: err=%v, want ErrLostOwnership", leg, err)
			}
		}},

		{"retry", func(t *testing.T, leg string, f *fenceFixture) {
			err := svc.RetryOwnedRun(ctx, f.run, f.own, "matrix")
			if leg == legCurrent {
				if err != nil {
					t.Fatalf("current owner retry: %v", err)
				}
				run, err := svc.GetRun(ctx, f.run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if run.Status != execution.StatusQueued {
					t.Fatalf("current owner retry left run=%s, want queued", run.Status)
				}
				return
			}
			if !errors.Is(err, execution.ErrLostOwnership) {
				t.Fatalf("%s retry: err=%v, want ErrLostOwnership", leg, err)
			}
		}},

		{"finalize", func(t *testing.T, leg string, f *fenceFixture) {
			err := svc.FinalizeOwnedRun(ctx, f.run, f.own, &execution.FinishInput{
				Status: execution.StatusSucceeded,
			})
			if leg == legCurrent {
				if err != nil {
					t.Fatalf("current owner finalize: %v", err)
				}
				run, err := svc.GetRun(ctx, f.run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if run.Status != execution.StatusSucceeded {
					t.Fatalf("current owner finalize left run=%s, want succeeded", run.Status)
				}
				return
			}
			if !errors.Is(err, execution.ErrLostOwnership) {
				t.Fatalf("%s finalize: err=%v, want ErrLostOwnership", leg, err)
			}
		}},

		{"slot_acquire", func(t *testing.T, leg string, f *fenceFixture) {
			_, ok, _, err := f.slots.Acquire(ctx, f.own)
			if leg == legCurrent {
				if err != nil || !ok {
					t.Fatalf("current owner slot acquire: ok=%v err=%v", ok, err)
				}
				return
			}
			if !errors.Is(err, execution.ErrLostOwnership) || ok {
				t.Fatalf("%s slot acquire: ok=%v err=%v, want ErrLostOwnership", leg, ok, err)
			}
		}},

		{"slot_renew", func(t *testing.T, leg string, f *fenceFixture) {
			err := f.slots.Renew(ctx, f.slot)
			if leg == legCurrent {
				if err != nil {
					t.Fatalf("current owner slot renew: %v", err)
				}
				return
			}
			if !errors.Is(err, execution.ErrProviderSlotLost) {
				t.Fatalf("%s slot renew: err=%v, want ErrProviderSlotLost", leg, err)
			}
		}},

		{"slot_release", func(t *testing.T, leg string, f *fenceFixture) {
			if err := f.slots.Release(ctx, f.slot); err != nil {
				t.Fatalf("%s slot release: %v", leg, err)
			}
			if leg == legStale {
				// Only its own (already irrelevant) row may disappear.
				if got := slotRowsForRun(t, svc, f.provider, f.run.ID); got != 1 {
					t.Fatalf("stale release touched the successor's reservation: rows=%d, want 1", got)
				}
				return
			}
			if got := slotRowsForRun(t, svc, f.provider, f.run.ID); got != 0 {
				t.Fatalf("%s release left its own row behind: rows=%d, want 0", leg, got)
			}
		}},
	}

	for _, c := range cases {
		for _, leg := range []string{legCurrent, legExpired, legStale} {
			t.Run(c.name+"/"+leg, func(t *testing.T) {
				f := build(t, leg)
				c.run(t, leg, f)
				if leg == legStale {
					f.assertSuccessorUntouched(t, svc)
				}
			})
		}
	}
}
