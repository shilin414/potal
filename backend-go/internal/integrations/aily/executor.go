package aily

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/catalog"
	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
	"github.com/creation-agent-studio/backend-go/internal/platform/storage"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
)

// Executor runs claimed Aily agent runs on the worker plane.
//
// Behavior preserved from the validated reference implementation:
//   - Lazy session: AgentThread.remote_id stays empty until the first real
//     message; different user/agent/provider/auth-subject never share one.
//   - Streaming (interactive) drives content.delta events; transport end,
//     timeout or break NEVER finalize the run directly — Final
//     Reconciliation via GET chat result is the only authority (§26).
//   - Background (async/poll) submits then polls with backoff 1/2/3/5s.
//
// Correctness contract (Execution Correctness Closure):
//   - The executor holds ONLY the ownership-fenced WorkerOwnedService —
//     there is no unfenced canonical-write path reachable from here
//     (修复计划 §81: 编译期约束).
//   - Every canonical write goes through claimed.Ownership, which is
//     immutable for the attempt lifetime — a GetRun refresh can never
//     drop the lease token again (评测 §十).
type Executor struct {
	Owned       *execution.WorkerOwnedService
	Adapter     *AgentAdapter
	Auth        *AuthResolver
	ChatsL      *execution.RateLimiter
	PollsL      *execution.RateLimiter
	ArtifactsL  *execution.RateLimiter
	PollBackoff []time.Duration
	Log         *slog.Logger
	Metrics     *telemetry.Metrics
	// Storage reads the staged attachment bytes the browser uploaded
	// (第十一轮 P0-2). Request attachments are persisted as `pending`
	// runtime_attachments rows with an EMPTY provider id, so the worker is
	// the only plane that can turn them into real Aily agent_attachment_id
	// values — see prepareAttachments. Nil means this deployment cannot
	// carry attachments; a run that has them fails loudly rather than
	// silently dropping the user's files.
	Storage storage.Storage
	// Gate is the PRE-SUBMIT kill switch (第三轮 P1-B, Gate 2). The
	// worker's claim-time gate (Gate 1) runs before the handler, but the
	// run may still wait here for auth resolution and a limiter token
	// while an admin disables the application or the provider — a run
	// that has not been SUBMITTED yet must still obey the kill switch.
	//
	// 第四轮 P1-1: the checkpoint moved OUT of Execute into the two real
	// submit paths, AFTER every local preparation step (thread
	// resolution, payload build, local validation) and immediately
	// before the provider HTTP call. Nil disables the check (tests /
	// non-gated deployments).
	Gate execution.RunGate
}

// Execute implements execution.Handler.
func (e *Executor) Execute(ctx context.Context, claimed *execution.ClaimedRun) error {
	run := claimed.Run
	agentID, identityMode, err := parseAilySnapshot(run.RuntimeSnapshot)
	if err != nil {
		// Configuration problems must terminate as RUN_CONFIG_INVALID, not
		// enter the panic/retry machinery (评测 P1: typed snapshot).
		return e.failRun(ctx, claimed, "run_config_invalid", err.Error())
	}
	if run.UserID == nil {
		return e.failRun(ctx, claimed, "aily_auth_error", "run has no user identity")
	}
	authCtx, err := e.Auth.Build(ctx, *run.UserID, identityMode, e.Auth.AppID)
	if err != nil {
		return e.failRun(ctx, claimed, "aily_auth_error", err.Error())
	}
	auth := &catalog.ProviderAuthContext{
		Provider:      authCtx.Provider,
		IdentityMode:  authCtx.IdentityMode,
		SubjectUserID: authCtx.SubjectUserID,
		TenantID:      authCtx.TenantID,
		CredentialRef: authCtx.CredentialRef,
		Token:         authCtx.Token,
	}

	if err := e.ChatsL.Acquire(ctx); err != nil {
		if ctx.Err() != nil {
			return execution.ErrLostOwnership // cancelled by lease loss
		}
		return e.failRun(ctx, claimed, "aily_rate_limit", "waiting for provider rate limit cancelled")
	}

	// NOTE: the pre-submit gate and the attempt counter used to live
	// here. 第四轮 P1-1 moved them INTO executeStreaming /
	// executeBackground, after thread resolution and payload validation
	// and immediately before the provider HTTP call — see beginSubmit.
	if run.ExecutionMode() == "interactive" {
		if err := e.executeStreaming(ctx, claimed, auth, agentID); err != nil {
			return e.classifyError(ctx, claimed, err)
		}
		return nil
	}
	if err := e.executeBackground(ctx, claimed, auth, agentID); err != nil {
		return e.classifyError(ctx, claimed, err)
	}
	return nil
}

// parseAilySnapshot validates the runtime snapshot into typed fields.
// A malformed/missing snapshot is a configuration error (never a panic).
func parseAilySnapshot(snapshot map[string]any) (agentID, identityMode string, err error) {
	if snapshot == nil {
		return "", "", errors.New("empty runtime snapshot")
	}
	v, ok := snapshot["external_resource_id"].(string)
	if !ok {
		return "", "", errors.New("runtime snapshot: external_resource_id missing or not a string")
	}
	if v == "" {
		return "", "", errors.New("runtime snapshot: external_resource_id is empty")
	}
	mode, _ := snapshot["identity_mode"].(string) // absent/null → default
	if mode == "" {
		mode = "user"
	}
	return v, mode, nil
}

// preSubmitStop carries the outcome of the pre-submit checkpoint out of
// executeStreaming/executeBackground. The run was already resolved there
// (gate kill → cancelled, gate pause/infra → deferred, attempt budget
// exhausted → failed), so the provider-error classifier must NOT touch
// it: classifyError unwraps and returns the enclosed error verbatim.
type preSubmitStop struct{ err error }

func (p *preSubmitStop) Error() string { return "aily: run stopped before provider submit" }
func (p *preSubmitStop) Unwrap() error { return p.err }

// submitAction is beginSubmit's verdict on the provider submit.
type submitAction int

const (
	// submitStop: the run is already resolved (gate verdict, exhausted
	// budget, or an unconfirmable submission parked in waiting_external).
	// The caller must return &preSubmitStop{err} and touch nothing else.
	submitStop submitAction = iota
	// submitNow: transmit. The submission is durably recorded as in-flight.
	submitNow
	// submitResumeAccepted: this run already has an ACCEPTED provider chat
	// (第九轮 P0-2). Transmitting again would create a second one, so the
	// caller reconciles/polls the recorded external id instead.
	submitResumeAccepted
)

// beginSubmit is the FINAL checkpoint before the provider sees a request
// (第四轮 P1-1, extended by 第九轮 P0-2). It is called by both submit paths
// only after every local preparation step has succeeded:
//
//	thread() → build SubmitInput → ValidateSubmit → ★ Gate 2
//	        → BeginProviderSubmission → HTTP submit
//
// Anything that can fail locally (DB IO for the agent thread, payload
// validation) already happened, so after Gate 2 only the submission CAS and
// the network call remain — the kill switch window is as small as a
// level-triggered check can make it.
//
// The submission step is what closes the "provider accepted, we crashed
// before recording it" hole. It answers three cases:
//
//	no record            → record it as 'sending' and transmit
//	last outcome unknown → DO NOT transmit; park the run (submitStop)
//	already accepted     → DO NOT transmit; resume from the recorded chat id
//
// On submitStop the run is already resolved and err carries that outcome
// (nil = the gate handled it); the caller must return &preSubmitStop{err}.
func (e *Executor) beginSubmit(ctx context.Context, claimed *execution.ClaimedRun) (*execution.ProviderSubmission, submitAction, error) {
	// kill → cancelled, pause/infra/unknown → deferred with the ORIGINAL
	// priority, fail closed. No attempt is consumed on any gated path.
	if execution.PreSubmitGate(ctx, e.Owned, claimed, e.Gate, e.Log) {
		return nil, submitStop, nil
	}
	// A run that already carries an external id has ALREADY been submitted
	// and accepted: the id is set-once and written only from the provider's
	// own answer. Re-entering the submit path here (a re-claim after a crash
	// or a lease expiry) used to create a second provider chat.
	if claimed.Run.ExternalRunID != "" {
		e.noteSubmissionDedup()
		return &execution.ProviderSubmission{State: execution.SubmissionAccepted, ExternalRunID: claimed.Run.ExternalRunID}, submitResumeAccepted, nil
	}

	sub, err := e.Owned.BeginProviderSubmission(ctx, claimed, claimed.Run.Provider,
		submissionHash(claimed.Run), e.submissionResendPolicy())
	if err != nil {
		switch {
		case errors.Is(err, execution.ErrProviderAttemptsExhausted):
			return nil, submitStop, e.failRun(ctx, claimed, "aily_attempts_exhausted",
				"provider retry budget exhausted before submit")
		case errors.Is(err, execution.ErrProviderSubmitUnknown):
			// A previous attempt already put this submission on the wire and
			// its fate is unknown. Nothing local can undo that and the
			// provider cannot be asked, so this run is parked rather than
			// retried (at-most-once).
			e.noteSubmissionDedup()
			return nil, submitStop, e.parkUnconfirmedSubmit(ctx, claimed,
				"a previous attempt may already have submitted this request")
		}
		return nil, submitStop, err // ErrLostOwnership → stop writing
	}
	if sub.State == execution.SubmissionAccepted {
		e.noteSubmissionDedup()
		return sub, submitResumeAccepted, nil
	}
	return sub, submitNow, nil
}

// submissionResendPolicy translates the ADAPTER's declared idempotency into
// the single decision BeginProviderSubmission needs (第九轮复审 P2).
//
// Before this, classifySubmitFailure honoured IdempotencyNative ("may retry")
// while BeginProviderSubmission refused every 'unknown' submission, so the
// two halves disagreed and a native-idempotent provider could never actually
// resend. The capability must be read from the same place both times, which
// is why it is derived here rather than passed around.
func (e *Executor) submissionResendPolicy() execution.SubmissionResend {
	if catalog.SubmitIdempotencyOf(e.Adapter) == catalog.IdempotencyNative {
		return execution.ResendOnUnknownSubmission
	}
	// Fail-closed: anything that is not a proof of native idempotency
	// (including an adapter that does not implement the optional interface)
	// stays at-most-once.
	return execution.ResendForbidden
}

// submissionHash identifies the payload of this submission so a re-entry can
// tell "the same external action" from a genuinely different one.
func submissionHash(run *execution.Run) []byte {
	raw, err := json.Marshal(run.Input)
	if err != nil {
		// An unmarshalable run input cannot be transmitted either; hash the
		// empty payload so the outcome is deterministic rather than random.
		raw = nil
	}
	return execution.ProviderSubmissionHash(run.Provider, raw)
}

// acceptedChatID resolves the provider chat id of an already-accepted
// submission, preferring the SUBMISSION RECORD over the in-memory run: the
// submission carries the id that was written together with the 'accepted'
// state, while a claim snapshot can predate that write (第九轮 P0-2).
func acceptedChatID(sub *execution.ProviderSubmission, claimed *execution.ClaimedRun) string {
	if sub != nil && sub.ExternalRunID != "" {
		return sub.ExternalRunID
	}
	return claimed.Run.ExternalRunID
}

// submitDisposition is the DECISION the submit boundary makes about a failed
// provider POST (第九轮 P0-2). It is computed by a pure function so the
// safety-critical half of the executor can be table-tested without a
// database, a worker or a provider.
type submitDisposition struct {
	// Park: this submission must NEVER be transmitted again; the run is
	// parked in waiting_external.
	Park bool
	// RecordOutcome is the submission state to persist before acting
	// ("" = nothing to record).
	RecordOutcome string
	// Reason explains the outcome for the event payload and the logs.
	Reason string
}

// classifySubmitFailure decides what a failed provider SUBMIT means.
//
// The default is "we do not know whether the provider received it", and for a
// provider that cannot deduplicate a resend that means PARK — never retry.
//
// Only a DEFINITIVE refusal keeps the ordinary retry policy, because only then
// is it certain the provider holds nothing:
//
//	4xx business error, auth failure, 429 → the provider looked at the
//	    request and refused it. Nothing was created, so a retry is a retry.
//	5xx                                  → NOT an answer. A 500 can be raised
//	    after the provider already accepted and started the work.
//	timeout / transport / unknown error  → the response never arrived; the
//	    only evidence is that we sent something.
//
// A provider declared IdempotencyNative collapses a resend on the stable
// submission key, so even an unknown outcome stays on the ordinary policy
// there — which is the entire reason the class exists.
func classifySubmitFailure(err error, class catalog.IdempotencyClass) submitDisposition {
	var apiErr *APIError
	definitive := errors.As(err, &apiErr) &&
		!errors.Is(apiErr.Kind, ErrTimeout) &&
		!errors.Is(apiErr.Kind, ErrServer)

	if definitive {
		// Recording 'rejected' BEFORE acting is what re-opens the submission
		// for the retry the caller may now legitimately perform.
		return submitDisposition{
			RecordOutcome: execution.SubmissionRejected,
			Reason:        "provider definitively refused the submission: " + apiErr.Error(),
		}
	}
	reason := submitUnknownReason(err)
	if class == catalog.IdempotencyNative {
		// The provider deduplicates a resend on the stable key, so the
		// pre-existing policy stays safe.
		return submitDisposition{RecordOutcome: execution.SubmissionUnknown, Reason: reason}
	}
	return submitDisposition{Park: true, RecordOutcome: execution.SubmissionUnknown, Reason: reason}
}

// onSubmitFailure applies classifySubmitFailure. A wrapped outcome has already
// been resolved by the disposition (a run parked, failed or requeued), so the
// caller returns it as a preSubmitStop and classifyError must not re-classify
// it.
func (e *Executor) onSubmitFailure(ctx context.Context, claimed *execution.ClaimedRun, sub *execution.ProviderSubmission, err error) error {
	if errors.Is(err, execution.ErrLostOwnership) {
		return err
	}
	// A cancellation is not a provider answer: the run is being wound down
	// (lease loss, shutdown) and nothing about the submission changed.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	d := classifySubmitFailure(err, catalog.SubmitIdempotencyOf(e.Adapter))
	e.recordSubmissionOutcome(ctx, claimed, sub, d)
	if d.Park {
		return e.parkUnconfirmedSubmit(ctx, claimed, d.Reason)
	}
	// Not parked: the outcome is durable and the ordinary provider policy
	// takes over — retry for a rate limit or a native-idempotent provider,
	// terminal failure for an auth or capability error. That policy is only
	// safe here because the submission was recorded as definitively refused
	// (or the provider can collapse a resend itself).
	return e.classifyError(ctx, claimed, err)
}

// recordSubmissionOutcome persists the submission state the disposition asks
// for. Failure to write it is reported but never fatal:
//
//   - a submission left in 'sending' is ALREADY treated as unknown by the next
//     attempt, so the safety property (never resend after an ambiguous
//     outcome) holds either way;
//   - 'rejected' is the only outcome that must be durable for a retry to
//     happen, and its absence makes the next attempt PARK the run instead of
//     retrying — a safe (if less available) direction to fail in.
func (e *Executor) recordSubmissionOutcome(ctx context.Context, claimed *execution.ClaimedRun, sub *execution.ProviderSubmission, d submitDisposition) {
	if d.RecordOutcome == "" {
		return
	}
	if err := e.Owned.MarkSubmissionState(ctx, claimed, sub, d.RecordOutcome, d.Reason); err != nil {
		e.Log.Warn("mark provider submission state failed",
			"run_id", claimed.Run.ID.String(), "state", d.RecordOutcome, "err", err)
	}
}

// parkUnconfirmedSubmit moves the run to waiting_external: the provider may
// hold this request, so the run must not be retried.
func (e *Executor) parkUnconfirmedSubmit(ctx context.Context, claimed *execution.ClaimedRun, reason string) error {
	if err := e.Owned.AwaitExternal(ctx, claimed, reason); err != nil {
		return err
	}
	e.Log.Warn("run parked in waiting_external",
		"run_id", claimed.Run.ID.String(), "provider", claimed.Run.Provider, "reason", reason)
	return nil
}

func (e *Executor) noteSubmissionDedup() {
	if e.Metrics != nil {
		e.Metrics.ProviderSubmissionDedupTotal.Inc()
	}
}

// submitUnknownReason renders a submit failure for the run event payload.
func submitUnknownReason(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return "provider submit outcome unknown: " + apiErr.Error()
	}
	return "provider submit outcome unknown: " + err.Error()
}

func (e *Executor) classifyError(ctx context.Context, claimed *execution.ClaimedRun, err error) error {
	// A gated / budget-exhausted run is already resolved by the
	// checkpoint: never re-classify it as a provider failure.
	var stopped *preSubmitStop
	if errors.As(err, &stopped) {
		return stopped.err
	}
	if errors.Is(err, execution.ErrLostOwnership) {
		return err // fence verdict: stop writing, never retry from here
	}
	// 第九轮补丁 3.2-B: the provider already holds the action; its acceptance
	// just could not be recorded. failRun would declare a run failed that is
	// still executing upstream, and RetryOwnedRun would transmit a second
	// chat — both are forbidden. Return verbatim: the worker exits, the run
	// stays running, the lease expires and the reaper converges it (the new
	// owner sees a 'sending' submission → waiting_external).
	if errors.Is(err, ErrProviderAcceptancePersistence) {
		return err
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if errors.Is(apiErr.Kind, ErrRateLimit) {
			return e.retryOrFail(ctx, claimed, "aily_rate_limit")
		}
		if errors.Is(apiErr.Kind, ErrTimeout) {
			return e.reconcile(ctx, claimed, "")
		}
		if apiErr.Retryable() {
			return e.retryOrFail(ctx, claimed, "aily_server_error")
		}
		return e.failRun(ctx, claimed, "aily_"+kindName(apiErr.Kind), apiErr.Msg)
	}
	if errors.Is(err, ErrCapability) {
		// Local input/capability rejection (content limits, attachment
		// count, unsupported file type): the provider never saw the
		// request, so this is a customer-input failure — NOT an internal
		// error, and (第四轮 P1-1) not a consumed attempt.
		return e.failRun(ctx, claimed, "aily_capability_error", err.Error())
	}
	return e.failRun(ctx, claimed, "aily_internal_error", err.Error())
}

func kindName(k error) string {
	switch k {
	case ErrAuth:
		return "auth_error"
	case ErrServer:
		return "server_error"
	case ErrClient:
		return "client_error"
	case ErrCapability:
		return "capability_error"
	default:
		return "error"
	}
}

// classifyAttachmentError resolves an attachment-preparation failure
// (第十一轮 P0-4).
//
// This is deliberately NOT classifyError's job, and deliberately not
// onSubmitFailure's either: the upload happens BEFORE the submit boundary,
// so POST /chats was never sent and the provider cannot be holding anything.
// Parking the run in waiting_external here would be a lie — the run would
// wait forever for an external call that never happened, which is exactly
// the symptom this batch fixes. The run fails, with a code that names the
// attachment stage so operators can tell it apart from a chat failure.
//
// ErrLostOwnership still wins: a fenced-out worker must stop writing.
func (e *Executor) classifyAttachmentError(ctx context.Context, claimed *execution.ClaimedRun, err error) error {
	if errors.Is(err, execution.ErrLostOwnership) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	// Wrap so the caller's provider classifier cannot reinterpret this as a
	// submit failure: the enclosed error carries the real cause.
	return &preSubmitStop{err: e.failRun(ctx, claimed, "aily_attachment_upload_failed", err.Error())}
}

// thread loads (or lazily creates) the conversation's AgentThread via the
// owned service, enforcing the identity binding invariant: one
// user/agent/mode per thread.
func (e *Executor) thread(ctx context.Context, claimed *execution.ClaimedRun) (threadID ids.ID, remoteID string, err error) {
	run := claimed.Run
	if run.ConversationID == nil {
		return ids.ID{}, "", nil
	}
	expectedMode := run.SnapshotString("identity_mode")
	if expectedMode == "" {
		expectedMode = "user"
	}
	expectedSubject := fmt.Sprintf("%d", *run.UserID)
	return e.Owned.EnsureAgentThread(ctx, *run.ConversationID, ProviderKey, expectedMode, expectedSubject)
}

// ErrProviderAcceptancePersistence (第九轮补丁 3.2-B): the provider answered
// with a chat id, but the ONE transaction that records "accepted + external
// id" could not be written. This error must NEVER be classified into a
// failRun / retry / re-submit: the provider already holds the action, and the
// only safe convergence is to stop, let the lease expire and let the reaper
// hand the run — with its still-'sending' submission — to a new owner, which
// will then park it in waiting_external.
var ErrProviderAcceptancePersistence = errors.New("aily: provider acceptance could not be persisted")

// acceptancePersistWaits is the short LOCAL DB retry budget for recording
// the provider's acceptance (第九轮补丁 3.2-B). What is retried is the local
// persistence write — NEVER the provider submit. If the write lands inside
// this window (a transient lock wait, a brief failover), the ordinary flow
// continues; if not, the executor returns ErrProviderAcceptancePersistence
// and the run converges through lease expiry instead.
var acceptancePersistWaits = []time.Duration{0, 50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond}

// persistProviderAcceptance records the provider's acceptance (submission
// state 'accepted' + runs.external_run_id, one transaction) — the canonical
// correctness half of the old bindThreadAndRun (第九轮补丁 3.2-B).
//
// Failure here is NOT log-and-continue: without this row the run has no
// durable proof of WHICH provider chat belongs to it, so polling would poll
// "" and a successful finalize would leave a submission stuck in 'sending'
// with no external id — both break the provider exactly-once ledger. The
// caller must stop the provider execution chain (no poll, no reconcile, no
// finalize, no second submit) and let the lease/reaper path converge.
func (e *Executor) persistProviderAcceptance(ctx context.Context, claimed *execution.ClaimedRun, sub *execution.ProviderSubmission, externalID string) error {
	if externalID == "" || claimed.Run.ExternalRunID != "" {
		return nil
	}
	var err error
	for i, wait := range acceptancePersistWaits {
		if wait > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}
		err = e.Owned.MarkSubmissionAccepted(ctx, claimed, sub, externalID)
		if err == nil {
			claimed.Run.ExternalRunID = externalID
			return nil
		}
		if errors.Is(err, execution.ErrLostOwnership) {
			return err
		}
		if i == 0 {
			e.Log.Warn("persisting provider acceptance failed; retrying the local write",
				"run_id", claimed.Run.ID.String(), "attempt", i+1, "err", err)
		}
	}
	e.Log.Error("provider acceptance could not be persisted after retries",
		"run_id", claimed.Run.ID.String(), "external_id", externalID, "err", err)
	return fmt.Errorf("%w: %w", ErrProviderAcceptancePersistence, err)
}

// bindProviderSessionBestEffort is the CONVERSATION-OPTIMIZATION half of the
// old bindThreadAndRun (第九轮补丁 3.2-B): a session conflict or a transient
// bind failure does not affect the run — the session is set-once and the
// next turn retries it. Only a lost ownership is propagated, because a
// fenced-out worker must stop touching state at all.
func (e *Executor) bindProviderSessionBestEffort(ctx context.Context, claimed *execution.ClaimedRun, threadID ids.ID, sessionID string) error {
	if threadID.IsZero() || sessionID == "" {
		return nil
	}
	if err := e.Owned.BindProviderSession(ctx, claimed, threadID, sessionID); err != nil {
		if errors.Is(err, execution.ErrLostOwnership) {
			return err
		}
		// Session conflict / transient bind failure: the run itself is
		// unaffected; log and continue (session binding is set-once).
		e.Log.Warn("bind provider session failed",
			"run_id", claimed.Run.ID.String(), "err", err)
	}
	return nil
}

// deltaCoalescer batches high-frequency provider deltas into durable
// content.chunk events (评测 P1: run_events write amplification). Live
// consumers still see every delta via the transient Redis channel; MySQL
// only receives a chunk every flushInterval / flushBytes.
//
// Each durable chunk is INCREMENTAL (第九轮 P1-4): text plus the end offset,
// never the cumulative answer. Storing the cumulative text on every chunk
// made a run's event payload volume quadratic in the answer length.
type deltaCoalescer struct {
	buf          []byte
	flushBytes   int
	lastFlush    time.Time
	totalFlushed int
	// totalReceived is the ABSOLUTE UTF-8 byte position after every delta
	// ever seen (第九轮补丁 3.2-A). Transient deltas and durable chunks must
	// share ONE coordinate system: a buffered delta that the SSE gateway
	// drains AFTER its coalesced chunk replayed can only be deduped by
	// comparing end offsets. len(text) on a Go string IS its UTF-8 byte
	// length, so this counter and the chunk offsets can never disagree.
	totalReceived int
}

func newDeltaCoalescer() *deltaCoalescer {
	return &deltaCoalescer{flushBytes: 2000, lastFlush: time.Now()}
}

// add buffers one delta and returns (absoluteEndOffset, shouldFlush).
// shouldFlush reports whether the buffer crossed a threshold (2000 bytes or
// 500ms since the last flush). The offset is the end of THIS delta in the
// answer's byte coordinate system — the same number the coalesced chunk
// that eventually covers it will carry.
func (c *deltaCoalescer) add(text string) (int, bool) {
	c.buf = append(c.buf, text...)
	c.totalReceived += len(text)
	return c.totalReceived,
		len(c.buf) >= c.flushBytes || time.Since(c.lastFlush) >= 500*time.Millisecond
}

// chunk drains the buffer into a persisted-chunk payload: the incremental
// text and the number of bytes emitted so far. Consumers concatenate chunks
// in ascending sequence; `offset` lets them detect a gap without holding the
// whole answer in memory.
func (c *deltaCoalescer) chunk() (payload map[string]any, ok bool) {
	if len(c.buf) == 0 {
		return nil, false
	}
	incremental := string(c.buf)
	c.totalFlushed += len(c.buf)
	c.buf = c.buf[:0]
	c.lastFlush = time.Now()
	return map[string]any{
		"text":   incremental,
		"offset": c.totalFlushed,
	}, true
}

// executeStreaming drives the interactive SSE path.
//
// Execution order (第十一轮 P0-4 — the attachment bridge sits strictly
// BEFORE the submit boundary):
//
//  1. thread()                     — conversation/provider session binding
//  2. attachment preflight gate     — kill switch, because the uploads below
//     are provider IO
//  3. prepareAttachments()          — studio ids → real Aily ids
//  4. build SubmitInput             — carries the PROVIDER ids
//  5. ValidateSubmit()              — local, no IO, no attempt consumed
//  6. beginSubmit()                 — final gate + 'sending' + attempt CAS
//  7. StreamPrepared()              — POST /chats
//
// Step 2 exists as a separate checkpoint because step 3 is the first thing
// in the run that talks to the provider. Step 6 stays where it was: it is
// the boundary that means "a chat may now exist upstream", so nothing that
// can fail may sit between it and the POST.
func (e *Executor) executeStreaming(ctx context.Context, claimed *execution.ClaimedRun, auth *catalog.ProviderAuthContext, agentID string) error {
	run := claimed.Run
	threadID, sessionID, err := e.thread(ctx, claimed)
	if err != nil {
		return err
	}

	// Attachment preflight gate (第十一轮 P0-4). Uploading is provider IO, so
	// a run that has not been submitted yet must still obey the kill switch
	// before its bytes reach Aily. Gated → the run is already resolved.
	if len(run.StudioAttachmentIDs()) > 0 && execution.PreSubmitGate(ctx, e.Owned, claimed, e.Gate, e.Log) {
		return &preSubmitStop{}
	}
	prepared, err := e.prepareAttachments(ctx, claimed, auth, agentID)
	if err != nil {
		return e.classifyAttachmentError(ctx, claimed, err)
	}

	submit := &catalog.SubmitInput{
		RunID:                 run.ID.String(),
		Auth:                  auth,
		ExternalResourceID:    agentID,
		Payload:               run.Input,
		SessionID:             sessionID,
		ExternalAttachmentIDs: prepared.ExternalIDs,
		Stream:                true,
		TimeoutSeconds:        run.SnapshotInt("timeout_seconds", 300),
	}
	// Local validation BEFORE the gate: an invalid payload is not a
	// provider execution and must not consume an attempt (第四轮 P1-1).
	if err := e.Adapter.ValidateSubmit(submit); err != nil {
		return err
	}
	sub, action, err := e.beginSubmit(ctx, claimed)
	if err != nil || action == submitStop {
		return &preSubmitStop{err: err}
	}
	if action == submitResumeAccepted {
		// This run was already accepted by the provider, so the stream must
		// NOT be re-opened — that would create a second provider chat. The
		// result API is the authority anyway (§26), so converge from it.
		//
		// The chat id comes from the SUBMISSION RECORD when it is available,
		// not from the in-memory run: the run snapshot was loaded at claim
		// time and can predate the acceptance, while provider_submissions
		// carries the id written together with that state (第九轮 P0-2).
		return e.reconcile(ctx, claimed, acceptedChatID(sub, claimed))
	}
	submit.ProviderIdempotencyKey = sub.IdempotencyKey

	// StreamPrepared opens the HTTP POST synchronously on this
	// goroutine — the gate above is the last checkpoint before it.
	events, cancel, err := e.Adapter.StreamPrepared(ctx, submit)
	if err != nil {
		// The POST itself failed. Whether the provider received the request
		// is unknowable from here, so the outcome is classified at the
		// SUBMIT boundary rather than by the generic provider policy
		// (第九轮 P0-2).
		return &preSubmitStop{err: e.onSubmitFailure(ctx, claimed, sub, err)}
	}
	defer cancel()

	externalRunID := ""
	coalescer := newDeltaCoalescer()
	// parkIfUnconfirmed is the boundary the review found missing (第九轮
	// 复审 P1). Once OpenStreamChat has succeeded the request has crossed
	// the submit boundary, so "we never learned the chat id" is NOT proof
	// that the provider holds nothing — the provider may already be running
	// the agent. Any outcome that leaves externalRunID empty (transport
	// error before the first frame, a first frame with no id, EOF, context
	// timeout) is therefore UNKNOWN and must park the run rather than fail
	// it. Before this, reconcile("") reported aily_no_chat_id and declared
	// terminal-failed a run that was still executing upstream.
	//
	// The boolean is the whole point: after a park the run is RESOLVED, so
	// the caller must return instead of falling through to reconciliation
	// (which would try to fail a run that is already parked).
	parkIfUnconfirmed := func(reason string) (bool, error) {
		if externalRunID != "" {
			return false, nil
		}
		e.recordSubmissionOutcome(ctx, claimed, sub, submitDisposition{
			RecordOutcome: execution.SubmissionUnknown,
			Reason:        reason,
		})
		return true, e.parkUnconfirmedSubmit(ctx, claimed, reason)
	}
	// NOTE (第九轮 P1-4): the durable chunk carries ONLY the incremental
	// text plus its end offset. It used to also carry a cumulative
	// `snapshot` of the whole answer, which made the run's event data
	// quadratic in the output length: a 1 MB answer stored
	// 2KB + 4KB + ... + 1MB ≈ 250 MB of snapshots.
	//
	// Deployment order matters. A client that reconnects and replays from
	// sequence 0 reconstructs the text by APPENDING incremental chunks, so
	// the durable cursor (and the frontend's local dedupe guard) must be in
	// place before this stops being written; the two ship together.
	flushChunk := func() error {
		payload, ok := coalescer.chunk()
		if !ok {
			return nil
		}
		return e.Owned.AppendEvent(ctx, claimed, execution.EventContentChunk, payload)
	}
	for ev := range events {
		switch ev.EventType {
		case "aily.stream.started":
			chatID, _ := ev.Payload["agent_chat_id"].(string)
			newSession, _ := ev.Payload["session_id"].(string)
			externalRunID = chatID
			if newSession == "" {
				newSession = sessionID
			}
			if chatID != "" {
				externalRunID = chatID
			}
			// 第九轮补丁 3.2-B: the acceptance ledger write is CANONICAL —
			// if it cannot be persisted the stream consumption stops here.
			// Continuing would let a succeeded reconciliation finalize a
			// run whose submission ledger still says 'sending' with no
			// external id. The lease expires, the reaper requeues, and the
			// next owner parks the 'sending' submission in waiting_external.
			if err := e.persistProviderAcceptance(ctx, claimed, sub, chatID); err != nil {
				return err
			}
			if err := e.bindProviderSessionBestEffort(ctx, claimed, threadID, newSession); err != nil {
				return err
			}
		case "aily.stream.transport_error":
			// Break out; reconciliation decides the terminal state — but
			// only when the provider actually told us which chat it
			// created.
			e.Log.Warn("aily stream transport error", "run_id", run.ID.String(), "err", ev.Payload["error"])
			if parked, perr := parkIfUnconfirmed("stream transport failed before the provider reported a chat id"); parked {
				return perr
			}
			return e.reconcile(ctx, claimed, externalRunID)
		case execution.EventContentDelta:
			// Transient: live SSE consumers only — never persisted as-is.
			// 第九轮补丁 3.2-A: the transient delta now carries the SAME
			// absolute UTF-8 end offset the durable chunk will carry. The
			// SSE gateway deliberately drains buffered live frames AFTER
			// replaying durable events, so the wire order is genuinely
			// "chunk ABC, then delta A, delta B, delta C" — a client that
			// appended both duplicated the answer. With an offset on both
			// event kinds, the client's byte-range reconciliation drops
			// every buffered delta the chunk already covered, whichever
			// order the two arrive in.
			text := strOf(ev.Payload["text"], "")
			endOffset, flush := coalescer.add(text)
			livePayload := make(map[string]any, len(ev.Payload)+1)
			for k, v := range ev.Payload {
				livePayload[k] = v
			}
			livePayload["offset"] = endOffset
			e.Owned.PublishTransient(ctx, claimed, execution.EventContentDelta, livePayload)
			if flush {
				if err := flushChunk(); err != nil {
					if errors.Is(err, execution.ErrLostOwnership) {
						return err
					}
					e.Log.Warn("append chunk failed", "err", err)
				}
			}
		case execution.EventArtifactDiscovered:
			if err := e.recordArtifact(ctx, claimed, ev.Payload); err != nil {
				if errors.Is(err, execution.ErrLostOwnership) {
					return err
				}
				e.Log.Warn("record artifact failed", "err", err)
			}
		case execution.EventRunFailed:
			return e.failRun(ctx, claimed,
				strOf(ev.Payload["error_code"], "aily_stream_error"),
				strOf(ev.Payload["error_message"], ""))
		}
	}
	// Flush any tail delta before the final reconciliation.
	if err := flushChunk(); err != nil && !errors.Is(err, execution.ErrLostOwnership) {
		e.Log.Warn("flush tail chunk failed", "err", err)
	}
	// Stream ended (normally or broken): final authority is the result API.
	// A stream that closed without ever reporting a chat id is the same
	// unknown as a transport error, not a failure we are entitled to
	// declare (第九轮复审 P1).
	if parked, perr := parkIfUnconfirmed("stream ended before the provider reported a chat id"); parked {
		return perr
	}
	return e.reconcile(ctx, claimed, externalRunID)
}

// executeBackground submits async and polls until terminal. The execution
// order matches executeStreaming (第十一轮 P0-4): the attachment bridge runs
// before the submit boundary, behind its own preflight gate.
func (e *Executor) executeBackground(ctx context.Context, claimed *execution.ClaimedRun, auth *catalog.ProviderAuthContext, agentID string) error {
	run := claimed.Run
	threadID, sessionID, err := e.thread(ctx, claimed)
	if err != nil {
		return err
	}
	if len(run.StudioAttachmentIDs()) > 0 && execution.PreSubmitGate(ctx, e.Owned, claimed, e.Gate, e.Log) {
		return &preSubmitStop{}
	}
	prepared, err := e.prepareAttachments(ctx, claimed, auth, agentID)
	if err != nil {
		return e.classifyAttachmentError(ctx, claimed, err)
	}
	submit := &catalog.SubmitInput{
		RunID:                 run.ID.String(),
		Auth:                  auth,
		ExternalResourceID:    agentID,
		Payload:               run.Input,
		SessionID:             sessionID,
		ExternalAttachmentIDs: prepared.ExternalIDs,
		TimeoutSeconds:        run.SnapshotInt("timeout_seconds", 300),
	}
	// Local validation BEFORE the gate (第四轮 P1-1): no attempt, no
	// provider call for a locally invalid payload.
	if err := e.Adapter.ValidateSubmit(submit); err != nil {
		return err
	}
	sub, action, err := e.beginSubmit(ctx, claimed)
	if err != nil || action == submitStop {
		return &preSubmitStop{err: err}
	}
	if action == submitResumeAccepted {
		// Already submitted and accepted earlier: poll the recorded chat
		// instead of creating a second one (第九轮 P0-2). The run data is
		// refreshed first so chatIDForResult and the terminal checks see the
		// current row; ownership stays on claimed.Ownership.
		if refreshed, rerr := e.Owned.GetRun(ctx, run.ID); rerr == nil {
			claimed.RefreshRun(refreshed)
		}
		chatID := acceptedChatID(sub, claimed)
		if chatID == "" {
			return e.failRun(ctx, claimed, "aily_no_chat_id",
				"submission is accepted but no external id could be resolved")
		}
		return e.pollUntilTerminal(ctx, claimed, auth, agentID, chatID)
	}
	submit.ProviderIdempotencyKey = sub.IdempotencyKey
	result, err := e.Adapter.SubmitPrepared(ctx, submit)
	if err != nil {
		return &preSubmitStop{err: e.onSubmitFailure(ctx, claimed, sub, err)}
	}
	// Defense-in-depth (第九轮补丁 3.2-B): SubmitPrepared returning WITHOUT an
	// external id after a successful POST means the provider crossed the
	// submit boundary while the response body is unusable (malformed JSON,
	// missing agent_chat_id). That is the same unknown as a streaming POST
	// whose first frame carries no chat id — NOT an ordinary provider
	// failure. ErrServer keeps Aily (IdempotencyNone) on the park path, and
	// a future native-idempotent provider on the safe-retry path.
	if result == nil || result.ExternalRunID == "" {
		err := &APIError{
			Kind:       ErrServer,
			Msg:        "provider accepted submit without an external run id",
			HTTPStatus: 200,
		}
		return &preSubmitStop{err: e.onSubmitFailure(ctx, claimed, sub, err)}
	}
	// The acceptance ledger write is the canonical correctness step: if it
	// cannot be persisted, do NOT poll — the run would poll "" and could be
	// declared failed while the provider is still executing (第九轮补丁 3.2-B).
	if err := e.persistProviderAcceptance(ctx, claimed, sub, result.ExternalRunID); err != nil {
		return err
	}
	if err := e.bindProviderSessionBestEffort(ctx, claimed, threadID, result.SessionID); err != nil {
		return err
	}
	// Refresh the run data WITHOUT touching the ownership: the fence
	// lives on claimed.Ownership and cannot be clobbered by a reload
	// (评测 §十: the old code overwrote the lease token here).
	if refreshed, err := e.Owned.GetRun(ctx, run.ID); err == nil {
		claimed.RefreshRun(refreshed)
	}
	return e.pollUntilTerminal(ctx, claimed, auth, agentID, claimed.Run.ExternalRunID)
}

func (e *Executor) pollUntilTerminal(ctx context.Context, claimed *execution.ClaimedRun, auth *catalog.ProviderAuthContext, agentID, chatID string) error {
	run := claimed.Run
	backoff := e.PollBackoff
	if len(backoff) == 0 {
		backoff = []time.Duration{time.Second, 2 * time.Second, 3 * time.Second, 5 * time.Second}
	}
	timeout := time.Duration(run.SnapshotInt("timeout_seconds", 300)) * time.Second
	deadline := time.Now().Add(timeout)
	attempt := 0
	for {
		if time.Now().After(deadline) {
			return e.failRun(ctx, claimed, "aily_poll_timeout",
				fmt.Sprintf("chat did not finish within %ds", int(timeout.Seconds())))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff[min(attempt, len(backoff)-1)]):
		}
		attempt++
		if err := e.PollsL.Acquire(ctx); err != nil {
			return err
		}
		status, err := e.Adapter.Status(ctx, auth, agentID, chatID)
		if err != nil {
			var apiErr *APIError
			if errors.As(err, &apiErr) && apiErr.HTTPStatus == 404 {
				return e.failRun(ctx, claimed, "aily_chat_not_found", apiErr.Msg)
			}
			continue
		}
		// Poll events are owned writes: a stale background worker must
		// STOP polling the moment the fence is lost (修复计划 §22 —
		// log-and-continue is forbidden).
		if err := e.Owned.AppendEvent(ctx, claimed, execution.EventRunPoll, map[string]any{"provider_status": status.ProviderStatus}); err != nil {
			return err
		}
		switch e.Adapter.mapper().MapProviderStatus(status.ProviderStatus) {
		case "succeeded", "failed", "cancelled":
			return e.finalizeFromResult(ctx, claimed, status.Raw)
		}
	}
}

func chatIDForResult(run *execution.Run, streamingChatID string) string {
	if run.ExternalRunID != "" {
		return run.ExternalRunID
	}
	return streamingChatID
}

// reconcile implements Final Reconciliation: after any stream outcome the
// result API decides status/text/finish_reason/artifacts.
func (e *Executor) reconcile(ctx context.Context, claimed *execution.ClaimedRun, externalRunID string) error {
	run := claimed.Run
	chatID := chatIDForResult(run, externalRunID)
	if chatID == "" {
		return e.failRun(ctx, claimed, "aily_no_chat_id", "stream ended without agent_chat_id")
	}
	if refreshed, err := e.Owned.GetRun(ctx, run.ID); err == nil {
		if execution.IsSettled(refreshed.Status) {
			return nil // someone else finished it (reaper/cancel)
		}
		// Ownership is immutable on the ClaimedRun — the refresh only
		// updates execution data.
		claimed.RefreshRun(refreshed)
	}
	agentID := run.SnapshotString("external_resource_id")
	if run.UserID == nil {
		return e.failRun(ctx, claimed, "aily_auth_error", "run has no user identity")
	}
	authCtx, err := e.Auth.Build(ctx, *run.UserID, modeOrDefault(run), e.Auth.AppID)
	if err != nil {
		return e.failRun(ctx, claimed, "aily_auth_error", err.Error())
	}
	auth := &catalog.ProviderAuthContext{Token: authCtx.Token, IdentityMode: authCtx.IdentityMode}

	if err := e.PollsL.Acquire(ctx); err != nil {
		return err
	}
	status, err := e.Adapter.Status(ctx, auth, agentID, chatID)
	if err != nil {
		return err
	}
	if e.Metrics != nil {
		e.Metrics.Reconciles.Inc()
	}
	switch e.Adapter.mapper().MapProviderStatus(status.ProviderStatus) {
	case "succeeded", "failed", "cancelled":
		return e.finalizeFromResult(ctx, claimed, status.Raw)
	default:
		return e.pollUntilTerminal(ctx, claimed, auth, agentID, chatID)
	}
}

func modeOrDefault(run *execution.Run) string {
	m := run.SnapshotString("identity_mode")
	if m == "" {
		return "user"
	}
	return m
}

// finalizeFromResult converges the run from the chat-result payload:
// artifacts recorded, then the terminal finalize transaction (run CAS +
// terminal event + assistant message + occurrence + lease cleanup).
func (e *Executor) finalizeFromResult(ctx context.Context, claimed *execution.ClaimedRun, chatResult map[string]any) error {
	run := claimed.Run
	mapper := e.Adapter.mapper()
	finalText := mapper.ExtractFinalText(chatResult)
	for _, art := range mapper.ExtractArtifacts(chatResult) {
		if err := e.recordArtifact(ctx, claimed, map[string]any{
			"external_artifact_id":   art.ExternalID,
			"provider_artifact_type": art.ProviderType,
			"name":                   art.Name,
		}); err != nil && !errors.Is(err, execution.ErrLostOwnership) {
			e.Log.Warn("record artifact failed", "run_id", run.ID.String(), "err", err)
		}
	}

	providerStatus, _ := chatResult["status"].(string)
	finishReason, _ := chatResult["finish_reason"].(string)
	mapped := mapper.MapProviderStatus(providerStatus)

	in := &execution.FinishInput{
		ProviderStatus: providerStatus,
		FinishReason:   finishReason,
	}
	switch mapped {
	case "failed":
		msg, _ := chatResult["msg"].(string)
		in.Status = execution.StatusFailed
		in.ErrorCode = "aily_provider_failed"
		in.ErrorMessage = msg
	case "cancelled":
		in.Status = execution.StatusCancelled
		in.Output = map[string]any{"text": finalText, "status": providerStatus}
	default:
		in.Status = execution.StatusSucceeded
		in.Output = map[string]any{"text": finalText, "status": providerStatus}
	}
	// Assistant message durability (修复计划 §32): the message is part of
	// the finalize transaction — a succeeded run can never lose its answer.
	if finalText != "" && run.ConversationID != nil {
		in.AssistantText = finalText
		meta, err := e.assistantMetadata(ctx, run)
		if err == nil {
			in.AssistantMetadata = meta
		}
	}
	return e.Owned.Finalize(ctx, claimed, in)
}

// assistantMetadata builds the assistant message metadata with artifact
// summaries for history replay (same shape as before the closure, but the
// message itself is now written inside the finalize transaction).
func (e *Executor) assistantMetadata(ctx context.Context, run *execution.Run) (json.RawMessage, error) {
	artifacts, err := e.Owned.ListRunArtifacts(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	meta := map[string]any{
		"run_id":   run.ID.String(),
		"provider": ProviderKey,
	}
	if len(artifacts) > 0 {
		summaries := make([]map[string]any, 0, len(artifacts))
		for _, a := range artifacts {
			summaries = append(summaries, map[string]any{
				"artifact_id":     idsMustString(a.ID),
				"name":            a.Name,
				"normalized_type": a.NormalizedType,
			})
		}
		meta["artifacts"] = summaries
	}
	return json.Marshal(meta)
}

// recordArtifact persists a RunArtifact and emits the discovery event
// with the LOCAL artifact id (the clickable handle) + external reference
// — both fenced and idempotent (stable id across re-discovery).
func (e *Executor) recordArtifact(ctx context.Context, claimed *execution.ClaimedRun, payload map[string]any) error {
	externalID, _ := payload["external_artifact_id"].(string)
	if externalID == "" {
		return nil
	}
	providerType, _ := payload["provider_artifact_type"].(string)
	name, _ := payload["name"].(string)
	mapper := e.Adapter.mapper()
	_, err := e.Owned.PersistArtifact(ctx, claimed, execution.ArtifactInput{
		ExternalID:     externalID,
		Provider:       ProviderKey,
		ProviderType:   providerType,
		Name:           name,
		NormalizedType: mapper.NormalizeArtifactType(providerType),
	})
	return err
}

// retryOrFail: a retryable provider failure either requeues (run.retrying
// — non-terminal) through the OWNED path, or fails the run for good once
// the attempts are exhausted (修复计划 §23: the old code called the
// unfenced ReleaseInterrupted here — a stale worker could delete the new
// owner's lease).
func (e *Executor) retryOrFail(ctx context.Context, claimed *execution.ClaimedRun, code string) error {
	if claimed.Run.Attempt < claimed.Run.MaxAttempts {
		return e.Owned.Retry(ctx, claimed, code)
	}
	return e.Owned.Fail(ctx, claimed, code, "rate limited after retries")
}

func (e *Executor) failRun(ctx context.Context, claimed *execution.ClaimedRun, code, message string) error {
	e.Log.Warn("aily run failed", "run_id", claimed.Run.ID.String(), "code", code, "message", message)
	return e.Owned.Fail(ctx, claimed, code, message)
}

func strOf(v any, def string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return def
}

func idsMustString(b []byte) string {
	id := ids.ID{}
	_ = id.Scan(b)
	return id.String()
}
