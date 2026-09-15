package execution

import (
	"context"
	"log/slog"
	"time"
)

// PreSubmitGate is the SECOND gate checkpoint (第三轮 P1-B, Gate 2): the
// pre-submit check inside the provider handler, executed AFTER every
// potentially-waiting step (auth resolution, rate limiter) and as close
// to BeginProviderAttempt as possible.
//
// Between the worker's claim-time gate (Gate 1) and the actual provider
// submit there is a real window: the run may wait for a user token or a
// limiter slot while an admin disables the application or the provider.
// The product rule says a run that has NOT been submitted yet must still
// obey the kill switch — so the handler re-checks the revocable facts
// immediately before BeginProviderAttempt:
//
//	Claim → Gate 1 → ProviderSlot → Handler → Auth → Limiter
//	      → ★ Gate 2 → BeginProviderAttempt → Provider Submit
//
// Semantics (same product decision as Gate 1):
//
//	application/binding missing or disabled → kill (cancel finalize)
//	provider missing/inactive               → defer (keep waiting)
//	gate query fails / unknown action       → defer, FAIL CLOSED
//
// Returns true when the run was gated and the caller MUST NOT submit;
// false means "allow" — proceed to BeginProviderAttempt.
func PreSubmitGate(ctx context.Context, owned *WorkerOwnedService, claimed *ClaimedRun, gate RunGate, log *slog.Logger) bool {
	if gate == nil {
		return false
	}
	action, err := gate.CheckRun(ctx, claimed.Run)
	switch {
	case err != nil:
		// Gate unavailable = infrastructure failure: defer briefly with
		// the original priority — never submit on an unreadable verdict.
		log.Warn("pre-submit gate unavailable; deferring run",
			"run_id", claimed.Run.ID.String(), "err", err)
		deferClaimed(ctx, owned, claimed, "run_gate_unavailable", AdmissionRequeueDelay, log)
		return true
	case action == GateKill:
		log.Info("pre-submit gate killed run (application/binding disabled)",
			"run_id", claimed.Run.ID.String())
		if ferr := owned.Finalize(ctx, claimed, &FinishInput{
			Status:       StatusCancelled,
			ErrorCode:    "execution_disabled",
			ErrorMessage: "application or runtime binding was disabled before execution",
		}); ferr != nil && ferr != ErrLostOwnership {
			log.Error("pre-submit gate kill finalize failed",
				"run_id", claimed.Run.ID.String(), "err", ferr)
		}
		return true
	case action == GatePause:
		// Provider paused between Gate 1 and submit: keep the run, defer
		// with its ORIGINAL priority, no attempt consumed (the submit
		// never happened).
		log.Info("pre-submit gate deferred run (provider inactive)",
			"run_id", claimed.Run.ID.String(), "delay", GatePauseRequeueDelay.String())
		deferClaimed(ctx, owned, claimed, "provider_disabled", GatePauseRequeueDelay, log)
		return true
	case action == GateAllow:
		return false
	default:
		// 第三轮 P2-E: an unknown action is a gate implementation bug.
		// The kill switch is a safety control — fail CLOSED (defer the
		// run), never continue to the provider on an unreadable verdict.
		log.Error("unknown pre-submit gate action; failing closed",
			"run_id", claimed.Run.ID.String(), "action", string(action))
		deferClaimed(ctx, owned, claimed, "run_gate_unknown", AdmissionRequeueDelay, log)
		return true
	}
}

// deferClaimed applies the priority-preserving defer; the write uses a
// detached context so a cancelled handler context cannot orphan the run —
// a failed defer is recovered by the lease/reaper machinery instead.
func deferClaimed(ctx context.Context, owned *WorkerOwnedService, claimed *ClaimedRun, reason string, delay time.Duration, log *slog.Logger) {
	// 第五轮 P2-5: detached from the handler context (a cancelled handler
	// must still be able to requeue) but bounded — a hung DB gives the
	// goroutine back after CleanupTimeout instead of blocking forever.
	cleanupCtx, cancel := NewCleanupContext(ctx)
	defer cancel()
	if err := owned.DeferAfter(cleanupCtx, claimed, reason, delay); err != nil && err != ErrLostOwnership {
		log.Error("pre-submit gate defer failed",
			"run_id", claimed.Run.ID.String(), "reason", reason, "err", err)
	}
}
