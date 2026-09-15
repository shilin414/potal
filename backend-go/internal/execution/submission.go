package execution

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"

	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
)

// Provider submission states (migration 0022).
//
//	sending   the intent to transmit is durable; the outcome is not known yet
//	accepted  the provider answered with an external id (recorded here AND on
//	          runs.external_run_id, in one transaction)
//	rejected  the provider definitively refused — the request does NOT exist
//	          on the provider side, so a resubmission is safe
//	unknown   the request may have been delivered and the provider cannot be
//	          asked about it → the run is parked, never retried
const (
	SubmissionSending  = "sending"
	SubmissionAccepted = "accepted"
	SubmissionRejected = "rejected"
	SubmissionUnknown  = "unknown"
)

// ErrProviderSubmitUnknown marks a submission that must NOT be transmitted
// again (第九轮 P0-2). It is not a retryable provider failure: for a provider
// that offers neither a native idempotency key nor a lookup by request key,
// sending again can create a SECOND real provider execution, so the run is
// parked in waiting_external until the sweep resolves it.
var ErrProviderSubmitUnknown = errors.New("execution: provider submit outcome unknown")

// DefaultWaitingExternalGrace bounds how long a parked run may stay
// unresolved. A run parked in waiting_external holds its conversation and its
// per-user outstanding slot, so an unbounded park would permanently block the
// conversation with 409s. After the grace the run is resolved as
// failed/provider_submit_unknown — the honest report for "we cannot know".
const DefaultWaitingExternalGrace = 10 * time.Minute

// ProviderSubmission is the durable identity of one provider-side action.
type ProviderSubmission struct {
	RunID          ids.ID
	SubmissionNo   uint32
	Provider       string
	IdempotencyKey string
	State          string
	Attempt        int64
	ExternalRunID  string
}

// ProviderSubmissionKey builds the STABLE provider-facing idempotency key:
//
//	potal:run:<run_uuid>:submit:<submission_no>
//
// It deliberately does NOT contain the attempt counter (第九轮 P0-2 §5).
// Several HTTP transport retries are still ONE external business action, and a
// per-attempt key would tell the provider "these are different requests" —
// which is precisely the second-provider-chat bug this table exists to
// prevent. submission_no only changes when the PAYLOAD changes.
func ProviderSubmissionKey(runID ids.ID, submissionNo uint32) string {
	return fmt.Sprintf("potal:run:%s:submit:%d", runID.String(), submissionNo)
}

// ProviderSubmissionHash identifies the PAYLOAD of a submission; it does not
// include the submission number. Same idea as RunRequestHash, one layer down:
// it lets a re-entry tell "the same external action" from "a genuinely
// different one" without trusting the attempt counter.
//
// Excluding the submission number is what makes the comparison meaningful: a
// caller about to submit does not yet know which submission_no the service
// will pick, while the number is already part of the idempotency KEY.
//
// payloadJSON is the run's marshalled input, produced by the caller (the
// executor already has the run loaded). A marshalling failure there yields
// nil, which still hashes deterministically — an unreadable payload must
// neither make two different submissions look identical nor make one
// submission look brand new.
func ProviderSubmissionHash(provider string, payloadJSON []byte) []byte {
	h := sha256.New()
	hashString(h, provider)
	hashString(h, string(payloadJSON))
	return h.Sum(nil)
}

func (s *Service) waitingExternalGrace() time.Duration {
	if s.WaitingExternalGrace > 0 {
		return s.WaitingExternalGrace
	}
	return DefaultWaitingExternalGrace
}

// BeginProviderSubmissionOwned is the FINAL durable checkpoint before the
// provider sees a request (第九轮 P0-2). It replaces BeginProviderAttempt as
// the attempt consumer and adds the submission record, in ONE transaction
// under the run row lock:
//
//	verify ownership (running + epoch)
//	attempt >= max_attempts      → ErrProviderAttemptsExhausted
//	attempt++                    → the returned submission carries the new count
//	create or re-open the submission at state 'sending'
//
// The returned decision is one of:
//
//	(*ProviderSubmission{State: sending}, nil)   the caller MAY transmit
//	(*ProviderSubmission{State: accepted}, nil)  already accepted: the caller
//	                                             must reconcile with
//	                                             ExternalRunID, NOT transmit
//	(nil, ErrProviderSubmitUnknown)              must not transmit; park the run
//
// The rules behind those three outcomes:
//
//   - a submission in 'sending' or 'unknown' means a previous attempt may
//     already have put THIS action on the wire. Nothing local can undo that,
//     and the provider cannot be asked, so transmitting again is forbidden —
//     this is where the old code created a second provider chat;
//   - 'rejected' is a definitive refusal: the provider has nothing, so the
//     same key may be transmitted again;
//   - a different payload hash gets a NEW submission_no (and therefore a new
//     key) — but only from a non-sending state, so a possibly-delivered
//     action is never duplicated.
func (s *Service) BeginProviderSubmissionOwned(ctx context.Context, own ExecutionOwnership, provider string, requestHash []byte) (*ProviderSubmission, error) {
	if !own.Valid() {
		return nil, ErrLostOwnership
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	row, err := verifyActiveOwnershipTx(ctx, tx, own)
	if err != nil {
		return nil, err
	}
	if int64(row.Attempt) >= int64(row.MaxAttempts) {
		return nil, ErrProviderAttemptsExhausted
	}
	q := db.New(tx)

	latest, err := q.GetLatestProviderSubmission(ctx, own.RunID.Bytes())
	switch {
	case errors.Is(err, sql.ErrNoRows):
		attempt, err := consumeAttemptTx(ctx, tx, own)
		if err != nil {
			return nil, err
		}
		sub, err := createSubmissionTx(ctx, q, own, provider, 1, requestHash, attempt)
		if err != nil {
			return nil, err
		}
		return sub, tx.Commit()
	case err != nil:
		return nil, err
	}

	// A previous attempt exists. Whether we may transmit again depends on
	// ITS outcome, never on ours.
	switch latest.State {
	case SubmissionAccepted:
		// The external id is already durable (written with the same
		// transaction as this state). Transmitting again would create a
		// second provider chat — resume from the recorded id instead.
		return submissionFromRow(latest), tx.Commit()
	case SubmissionSending, SubmissionUnknown:
		return nil, ErrProviderSubmitUnknown
	}

	// latest.State == rejected: definitively not on the provider side.
	attempt, err := consumeAttemptTx(ctx, tx, own)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(latest.RequestHash, requestHash) {
		// The payload changed, so this is a different external action and
		// gets its own identity/key. Safe here precisely because the
		// predecessor is in 'rejected'.
		sub, err := createSubmissionTx(ctx, q, own, provider, latest.SubmissionNo+1, requestHash, attempt)
		if err != nil {
			return nil, err
		}
		return sub, tx.Commit()
	}
	// Same payload, previously refused: reuse the identity (and the key).
	if _, err := q.MarkProviderSubmissionState(ctx, db.MarkProviderSubmissionStateParams{
		State:        SubmissionSending,
		LastError:    sql.NullString{},
		RunID:        own.RunID.Bytes(),
		SubmissionNo: latest.SubmissionNo,
	}); err != nil {
		return nil, err
	}
	out := submissionFromRow(latest)
	out.State = SubmissionSending
	out.Attempt = attempt
	return out, tx.Commit()
}

// consumeAttemptTx burns one provider-execution attempt (P0-2): attempt counts
// PROVIDER EXECUTIONS, so only a path that is about to reach the provider may
// call this.
func consumeAttemptTx(ctx context.Context, tx *sql.Tx, own ExecutionOwnership) (int64, error) {
	row, err := verifyActiveOwnershipTx(ctx, tx, own)
	if err != nil {
		return 0, err
	}
	if _, err := db.New(tx).BeginProviderAttemptFenced(ctx, db.BeginProviderAttemptFencedParams{
		ID:         own.RunID.Bytes(),
		LeaseEpoch: own.LeaseEpoch,
	}); err != nil {
		return 0, err
	}
	return int64(row.Attempt) + 1, nil
}

func createSubmissionTx(ctx context.Context, q db.Querier, own ExecutionOwnership, provider string, submissionNo uint32, requestHash []byte, attempt int64) (*ProviderSubmission, error) {
	key := ProviderSubmissionKey(own.RunID, submissionNo)
	res, err := q.CreateProviderSubmission(ctx, db.CreateProviderSubmissionParams{
		RunID:          own.RunID.Bytes(),
		SubmissionNo:   submissionNo,
		Provider:       provider,
		IdempotencyKey: key,
		RequestHash:    requestHash,
		Attempt:        uint32(attempt),
	})
	if err != nil {
		return nil, err
	}
	// 0 = this (run, submission_no) already exists, i.e. two workers raced
	// the first submission of the same run. The row that exists is the
	// authoritative one, and its state decides; the caller must NOT assume
	// it may transmit.
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, ErrProviderSubmitUnknown
	}
	return &ProviderSubmission{
		RunID:          own.RunID,
		SubmissionNo:   submissionNo,
		Provider:       provider,
		IdempotencyKey: key,
		State:          SubmissionSending,
		Attempt:        attempt,
	}, nil
}

func submissionFromRow(row db.ProviderSubmission) *ProviderSubmission {
	id := ids.ID{}
	_ = id.Scan(row.RunID)
	return &ProviderSubmission{
		RunID:          id,
		SubmissionNo:   row.SubmissionNo,
		Provider:       row.Provider,
		IdempotencyKey: row.IdempotencyKey,
		State:          row.State,
		Attempt:        int64(row.Attempt),
		ExternalRunID:  row.ExternalRunID,
	}
}

// MarkSubmissionAcceptedOwned records the provider's answer and the run's
// external id in ONE transaction (第九轮 P0-2). Splitting them would recreate
// the crash window from the other side: a submission marked accepted whose
// external id was lost cannot be resumed, and an external id without the
// submission state cannot be told apart from a partially written attempt.
//
// This is also the write that makes the "already accepted" branch above
// reachable, i.e. it is what turns a re-claim of a crashed run into a
// reconcile instead of a resubmit.
func (s *Service) MarkSubmissionAcceptedOwned(ctx context.Context, own ExecutionOwnership, sub *ProviderSubmission, externalRunID string) error {
	if externalRunID == "" {
		return nil
	}
	if !own.Valid() {
		return ErrLostOwnership
	}
	if sub == nil {
		return errors.New("execution: no provider submission to accept")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := verifyActiveOwnershipTx(ctx, tx, own)
	if err != nil {
		return err
	}
	q := db.New(tx)
	if _, err := q.MarkProviderSubmissionAccepted(ctx, db.MarkProviderSubmissionAcceptedParams{
		ExternalRunID: externalRunID,
		RunID:         own.RunID.Bytes(),
		SubmissionNo:  sub.SubmissionNo,
	}); err != nil {
		return err
	}
	if err := updateExternalRunIDTx(ctx, tx, row, own, externalRunID); err != nil {
		return err
	}
	return tx.Commit()
}

// MarkSubmissionStateOwned records a terminal-for-this-attempt outcome:
// 'rejected' (the provider definitively refused) or 'unknown' (it may have
// been delivered and cannot be confirmed).
//
// A failure to write this must not be swallowed: 'unknown' is exactly the
// fact that stops the next attempt from resubmitting, so the caller treats an
// error here as a failure to park the run safely and fails closed.
func (s *Service) MarkSubmissionStateOwned(ctx context.Context, own ExecutionOwnership, sub *ProviderSubmission, state, lastError string) error {
	if !own.Valid() {
		return ErrLostOwnership
	}
	if sub == nil {
		return errors.New("execution: no provider submission to mark")
	}
	res, err := s.q(ctx).MarkProviderSubmissionState(ctx, db.MarkProviderSubmissionStateParams{
		State:        state,
		LastError:    sql.NullString{String: lastError, Valid: lastError != ""},
		RunID:        own.RunID.Bytes(),
		SubmissionNo: sub.SubmissionNo,
	})
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		// The row must exist; 0 changed rows means either it is missing or
		// the values were already identical (MySQL reports CHANGED rows).
		// Distinguish "already in that state" (fine, idempotent re-entry)
		// from "missing" with a read, because a missing row would silently
		// re-enable a resubmit on the next attempt.
		cur, rerr := s.q(ctx).GetLatestProviderSubmission(ctx, own.RunID.Bytes())
		if rerr != nil || cur.State != state {
			return fmt.Errorf("provider submission state not recorded (want %q): %w", state, rerr)
		}
	}
	if state == SubmissionUnknown && s.Metrics != nil {
		s.Metrics.ProviderSubmissionUnknownTotal.Inc()
	}
	return nil
}

// AwaitExternalOwned parks a running run in waiting_external — NON-terminal
// (第九轮 P0-2). It is the honest alternative to a blind retry: the run keeps
// its conversation and its outstanding slot, keeps its SSE stream open (the
// event is not terminal), and is resolved later by ExpireParkedExternalRuns
// or by an operator.
//
// Mirrors RetryOwnedRunAfter's transaction shape: ownership verified under
// the run row lock, status CAS, ONE event, provider capacity released, lease
// dropped, all in one commit. Attempt is deliberately NOT restored — the
// provider may have been reached, so this attempt is spent.
//
// The submission is resolved inside the transaction rather than passed in:
// the idempotency key that goes into the event payload must be the one
// actually used on the wire, and only the database knows it.
func (s *Service) AwaitExternalOwned(ctx context.Context, own ExecutionOwnership, reason string) error {
	if !own.Valid() {
		return ErrLostOwnership
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := db.New(tx)

	if _, err := verifyActiveOwnershipTx(ctx, tx, own); err != nil {
		return err
	}
	res, err := q.AwaitExternalRunFenced(ctx, db.AwaitExternalRunFencedParams{
		ID:         own.RunID.Bytes(),
		LeaseEpoch: own.LeaseEpoch,
	})
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrLostOwnership
	}

	payload := map[string]any{
		"status":     StatusWaitingExternal,
		"reason":     reason,
		"error_code": "provider_submit_unknown",
		"worker_id":  own.WorkerID,
	}
	if sub, serr := q.GetLatestProviderSubmission(ctx, own.RunID.Bytes()); serr == nil {
		payload["submission_no"] = sub.SubmissionNo
		payload["idempotency_key"] = sub.IdempotencyKey
		payload["submission_state"] = sub.State
	}
	sequence, err := allocEventSequenceTx(ctx, tx, own.RunID, 0)
	if err != nil {
		return err
	}
	if err := insertRunEventTx(ctx, tx, own.RunID, sequence, EventRunWaitingExternal, payload); err != nil {
		return err
	}

	// Provider capacity is released in the same transaction that ends the
	// execution, exactly like retry/recovery: a parked run must not hold a
	// provider slot.
	if _, err := q.DeleteProviderSlotsUpToEpoch(ctx, db.DeleteProviderSlotsUpToEpochParams{
		RunID:      own.RunID.Bytes(),
		LeaseEpoch: own.LeaseEpoch,
	}); err != nil {
		return err
	}
	if err := deleteOwnedLeaseTx(ctx, q, own); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if s.Metrics != nil {
		s.Metrics.ProviderSubmissionUnknownTotal.Inc()
	}
	s.publishLive(ctx, own.RunID, sequence, EventRunWaitingExternal, payload)
	return nil
}

// ExpireParkedExternalRuns is the resolution half of the parked-submission
// lifecycle: runs that have stayed in waiting_external past the grace window
// are resolved as failed/provider_submit_unknown, in ONE transaction each.
//
// Without it a parked run is a zombie: waiting_external is a non-settled
// status, so it counts against the run-per-conversation rule (the conversation
// answers 409 forever) and against RUN_USER_MAX_OUTSTANDING. The grace is
// measured from the DB clock, and a run is only swept once its row has been
// quiet for the whole window.
func (s *Service) ExpireParkedExternalRuns(ctx context.Context, limit int) (int, error) {
	// Clock Authority: the cutoff derives from the DATABASE clock. A local
	// fallback here would silently shorten or lengthen the grace window on a
	// drifted host, so a failure to read the clock skips the sweep instead.
	dbNow, err := s.q(ctx).CurrentDBTime(ctx)
	if err != nil {
		return 0, err
	}
	runIDs, err := s.q(ctx).ListParkedWaitingExternalRunIDs(ctx, db.ListParkedWaitingExternalRunIDsParams{
		QuietBefore: dbNow.Add(-s.waitingExternalGrace()),
		Limit:       int32(limit),
	})
	if err != nil {
		return 0, err
	}
	expired := 0
	for _, raw := range runIDs {
		runID := mustID(raw)
		ok, err := s.expireParkedExternalRunTx(ctx, runID)
		if err != nil {
			s.Log.Error("parked-submission sweep failed", "run_id", runID.String(), slogKey("err"), err)
			continue
		}
		if ok {
			expired++
		}
	}
	return expired, nil
}

func (s *Service) expireParkedExternalRunTx(ctx context.Context, runID ids.ID) (bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	q := db.New(tx)

	row, err := q.GetRunForUpdate(ctx, runID.Bytes())
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if row.Status != StatusWaitingExternal {
		// Resolved in the meantime (cancel, a reconciler): the winner owns
		// the transition and this sweep must not overwrite it.
		return false, nil
	}
	res, err := q.FailParkedExternalRun(ctx, db.FailParkedExternalRunParams{
		ErrorMessage: nullText("provider submit outcome could not be confirmed; the provider may have received the request"),
		ID:           runID.Bytes(),
	})
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return false, nil
	}

	payload := map[string]any{
		"status":     StatusFailed,
		"error_code": "provider_submit_unknown",
		"reason":     "provider submit outcome could not be confirmed within the grace window",
	}
	sequence, err := allocEventSequenceTx(ctx, tx, runID, 0)
	if err != nil {
		return false, err
	}
	if err := insertRunEventTx(ctx, tx, runID, sequence, EventRunFailed, payload); err != nil {
		return false, err
	}
	// Invariant H: a terminal scheduled run and its occurrence converge.
	if err := finishOccurrenceTx(ctx, tx, row, runID, StatusFailed); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	s.publishLive(ctx, runID, sequence, EventRunFailed, payload)
	return true, nil
}
