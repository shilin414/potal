// Review-round-4 fix regression tests (opt-in: STUDIO_TEST_DB=1 +
// STUDIO_TEST_REDIS=1) — P1-1: the pre-submit kill switch must sit
// IMMEDIATELY before the REAL provider submit.
//
//	P1-1  Gate 2 moved out of Executor.Execute into the two real submit
//	      paths, AFTER thread resolution and payload validation and
//	      immediately before the HTTP call. The round-3 suite only proved
//	      that the GATE HELPER works (a mocked handler incrementing a
//	      "submits" counter); it never proved the provider was actually
//	      unreachable, because the real Aily executor was never in the
//	      loop. These tests inject a fake ProviderAPI and count the real
//	      StartChat / OpenStreamChat calls.
//	P1-1  Streaming now opens the HTTP POST synchronously
//	      (OpenStreamChat), so there is no goroutine scheduling window
//	      between the gate verdict and http.Do.
//	P1-1  The provider attempt is consumed only after every local
//	      preparation step succeeded — a thread/validation failure leaves
//	      the retry budget untouched.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/app"
	"github.com/creation-agent-studio/backend-go/internal/catalog"
	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/integrations/aily"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
	"github.com/creation-agent-studio/backend-go/internal/platform/redisx"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
)

// ── fake provider transport ─────────────────────────────────────────────

// submitRecorder is an aily.ProviderAPI that counts the two calls that
// actually reach the provider. Everything else is a stub: the tests below
// are about the SUBMIT boundary, not about reconciliation.
type submitRecorder struct {
	mu          sync.Mutex
	startChats  int
	openStreams int

	streamBody string
	// onSubmit runs INSIDE the provider call (still on the executor's
	// goroutine) so the test can observe the run state at the exact
	// moment the provider receives the request.
	onSubmit func(kind string)
}

// cannedSSE is a minimal stream: it carries the chat identity and then
// ends, so the executor reconciles via GetChatResult (stubbed below).
const cannedSSE = "event: message\ndata: {\"agent_chat_id\":\"chat-1\",\"session_id\":\"sess-1\"}\n\n"

func (r *submitRecorder) hook(kind string) func() {
	r.mu.Lock()
	defer r.mu.Unlock()
	h := r.onSubmit
	if h == nil {
		return nil
	}
	return func() { h(kind) }
}

func (r *submitRecorder) counted() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.startChats, r.openStreams
}

func (r *submitRecorder) StartChat(ctx context.Context, agentID, token string, contentItems []map[string]any, attachmentIDs []string, sessionID string) (string, string, error) {
	r.mu.Lock()
	r.startChats++
	r.mu.Unlock()
	if h := r.hook("start_chat"); h != nil {
		h()
	}
	return "chat-1", "sess-1", nil
}

func (r *submitRecorder) OpenStreamChat(ctx context.Context, agentID, token string, contentItems []map[string]any, attachmentIDs []string, sessionID string) (io.ReadCloser, error) {
	r.mu.Lock()
	r.openStreams++
	body := r.streamBody
	r.mu.Unlock()
	if h := r.hook("open_stream"); h != nil {
		h()
	}
	return io.NopCloser(strings.NewReader(body)), nil
}

func (r *submitRecorder) GetChatResult(ctx context.Context, agentID, token, chatID string) (json.RawMessage, error) {
	return json.RawMessage(`{"status":"succeeded","finish_reason":"stop","msg":"ok"}`), nil
}

func (r *submitRecorder) UploadAttachment(ctx context.Context, agentID, token string, data []byte, filename, attachmentType, docURL string) (string, error) {
	return "att-1", nil
}

func (r *submitRecorder) GetArtifact(ctx context.Context, agentID, token, artifactID string) (*aily.ArtifactDownload, error) {
	return nil, errors.New("artifacts are not used by these tests")
}

func (r *submitRecorder) CheckVisibility(ctx context.Context, agentID, uat string) (bool, error) {
	return true, nil
}

// ── fixture ─────────────────────────────────────────────────────────────

type ailySubmitFixture struct {
	env     *scheduleEnv
	rdb     *redisx.Client
	svc     *execution.Service
	rec     *submitRecorder
	exec    *aily.Executor
	claimed *execution.ClaimedRun
	runID   ids.ID
	appID   int64
	provKey string
	convID  int64
}

// newAilySubmitFixture builds the REAL Aily executor over a real claimed
// run (real DB, real application/binding/provider rows, real gate) with
// only the provider HTTP transport faked.
func newAilySubmitFixture(t *testing.T, suffix, mode, content string) *ailySubmitFixture {
	t.Helper()
	env := newScheduleEnv(t)
	rdb := testRedis(t)
	if rdb == nil {
		t.Skip("set STUDIO_TEST_REDIS=1 to run the aily submit-boundary tests")
	}
	ctx := context.Background()
	provKey, appID, bindingID := gateFixture(t, env.db, suffix)
	svc := execution.NewService(env.db, rdb, silentLogger(), telemetry.NewMetrics("test"))
	convID := seedConversation(t, svc)

	// A cached UAT keeps AuthResolver away from the Feishu token
	// endpoint without weakening anything the tests assert.
	uat, _ := json.Marshal(map[string]any{
		"token":      "itest-uat",
		"expires_at": time.Now().Unix() + 3600,
	})
	if err := rdb.Set(ctx, rdb.Key("provider", "aily", "uat", "42"), uat, time.Hour).Err(); err != nil {
		t.Fatalf("seed uat cache: %v", err)
	}

	run, err := svc.CreateRun(ctx, &execution.CreateRunInput{
		UserID:           42,
		ApplicationID:    appID,
		ConversationID:   convID,
		RuntimeBindingID: bindingID,
		Provider:         provKey,
		RuntimeType:      "agent",
		ExecutionMode:    mode,
		Content:          content,
		RuntimeSnapshot: map[string]any{
			"external_resource_id": "agent_itest_" + suffix,
			"identity_mode":        "user",
			"execution_mode":       mode,
			"timeout_seconds":      60,
		},
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	claimed, won, err := svc.ClaimRun(ctx, run.ID, "itest-"+suffix, time.Minute)
	if err != nil || !won {
		t.Fatalf("claim run: won=%v err=%v", won, err)
	}

	rec := &submitRecorder{streamBody: cannedSSE}
	exec := &aily.Executor{
		Owned:   svc.WorkerOwned(),
		Adapter: aily.NewAgentAdapterWithAPI(rec, nil),
		Auth:    &aily.AuthResolver{DB: env.db, Redis: rdb, AppID: "itest-app"},
		ChatsL:  execution.NewRateLimiter(rdb, "itest:chats:"+suffix, 100, time.Second),
		PollsL:  execution.NewRateLimiter(rdb, "itest:polls:"+suffix, 100, time.Second),
		// Short backoff: the background path polls once before the stub
		// result API returns succeeded.
		PollBackoff: []time.Duration{10 * time.Millisecond},
		Log:         silentLogger(),
		Metrics:     telemetry.NewMetrics("test"),
		Gate:        &app.ExecutionGate{Catalog: &catalog.Service{DB: env.db}},
	}
	return &ailySubmitFixture{
		env: env, rdb: rdb, svc: svc, rec: rec, exec: exec,
		claimed: claimed, runID: run.ID, appID: appID, provKey: provKey, convID: convID,
	}
}

func countAgentThreads(t *testing.T, f *ailySubmitFixture) int64 {
	t.Helper()
	return f.env.count(t, `SELECT COUNT(*) FROM agent_threads WHERE conversation_id = ?`, f.convID)
}

// ── P1-1: a disabled application must never reach the provider ───────────

// TestDisabledApplicationNeverReachesStartChat (第四轮 P1-1): the REAL
// executor, with the application disabled before the final checkpoint,
// must not call StartChat (background) or OpenStreamChat (interactive),
// must not consume an attempt, and must cancel with execution_disabled.
func TestDisabledApplicationNeverReachesStartChat(t *testing.T) {
	for _, mode := range []string{"background", "interactive"} {
		t.Run(mode, func(t *testing.T) {
			f := newAilySubmitFixture(t, "r4kill"+mode, mode, "round4 kill")
			ctx := context.Background()
			if _, err := f.env.db.ExecContext(ctx,
				`UPDATE applications SET enabled = 0 WHERE id = ?`, f.appID); err != nil {
				t.Fatalf("disable application: %v", err)
			}

			if err := f.exec.Execute(ctx, f.claimed); err != nil {
				t.Fatalf("execute: %v", err)
			}
			starts, opens := f.rec.counted()
			if starts != 0 || opens != 0 {
				t.Fatalf("provider reached after kill: StartChat=%d OpenStreamChat=%d, want 0/0", starts, opens)
			}
			st := readRunState(t, f.env.db, f.runID)
			if st.status != "cancelled" {
				t.Fatalf("status=%q, want cancelled", st.status)
			}
			if st.errorCode != "execution_disabled" {
				t.Fatalf("error_code=%q, want execution_disabled", st.errorCode)
			}
			if st.attempt != 0 {
				t.Fatalf("killed run consumed %d attempts, want 0", st.attempt)
			}
		})
	}
}

// TestInactiveProviderNeverReachesStartChat (第四轮 P1-1): a provider
// deactivated before the final checkpoint defers the run with its ORIGINAL
// priority — no provider call, no consumed attempt.
func TestInactiveProviderNeverReachesStartChat(t *testing.T) {
	for _, mode := range []string{"background", "interactive"} {
		t.Run(mode, func(t *testing.T) {
			f := newAilySubmitFixture(t, "r4pause"+mode, mode, "round4 pause")
			ctx := context.Background()
			if _, err := f.env.db.ExecContext(ctx,
				`UPDATE providers SET status = 'inactive' WHERE provider_key = ?`, f.provKey); err != nil {
				t.Fatalf("deactivate provider: %v", err)
			}

			if err := f.exec.Execute(ctx, f.claimed); err != nil {
				t.Fatalf("execute: %v", err)
			}
			starts, opens := f.rec.counted()
			if starts != 0 || opens != 0 {
				t.Fatalf("provider reached while paused: StartChat=%d OpenStreamChat=%d, want 0/0", starts, opens)
			}
			st := readRunState(t, f.env.db, f.runID)
			if st.status != "queued" {
				t.Fatalf("status=%q, want queued", st.status)
			}
			if st.priority != execution.DefaultPriority {
				t.Fatalf("priority=%q, want the original %q", st.priority, execution.DefaultPriority)
			}
			if st.attempt != 0 {
				t.Fatalf("deferred run consumed %d attempts, want 0", st.attempt)
			}
			if !st.avail.Valid || !st.avail.Time.After(time.Now().UTC()) {
				t.Fatalf("deferred run available_at=%v, want a future requeue time", st.avail)
			}
		})
	}
}

// TestAilyBackgroundGateRunsAfterThreadPreparation (第四轮 P1-1): the gate
// must run AFTER the local preparation (agent-thread resolution), not
// before it — otherwise a disabled application would skip the thread and
// the ordering would be indistinguishable from "the gate ran but nothing
// was prepared".
func TestAilyBackgroundGateRunsAfterThreadPreparation(t *testing.T) {
	f := newAilySubmitFixture(t, "r4thread", "background", "round4 thread")
	ctx := context.Background()
	if _, err := f.env.db.ExecContext(ctx,
		`UPDATE applications SET enabled = 0 WHERE id = ?`, f.appID); err != nil {
		t.Fatalf("disable application: %v", err)
	}

	if err := f.exec.Execute(ctx, f.claimed); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := countAgentThreads(t, f); got != 1 {
		t.Fatalf("agent threads=%d, want 1 (thread resolution must precede the gate)", got)
	}
	if starts, _ := f.rec.counted(); starts != 0 {
		t.Fatalf("StartChat=%d after gate kill, want 0", starts)
	}
}

// TestAilyStreamingGateRunsImmediatelyBeforeOpenStream (第四轮 P1-1): the
// attempt is consumed and the thread is prepared BEFORE the provider
// receives the streaming request — asserted from inside OpenStreamChat,
// i.e. at the exact moment of the HTTP open.
func TestAilyStreamingGateRunsImmediatelyBeforeOpenStream(t *testing.T) {
	f := newAilySubmitFixture(t, "r4order", "interactive", "round4 ordering")
	ctx := context.Background()

	var (
		attemptAtOpen int64
		threadsAtOpen int64
	)
	f.rec.onSubmit = func(kind string) {
		attemptAtOpen = readRunState(t, f.env.db, f.runID).attempt
		threadsAtOpen = countAgentThreads(t, f)
	}

	if err := f.exec.Execute(ctx, f.claimed); err != nil {
		t.Fatalf("execute: %v", err)
	}
	starts, opens := f.rec.counted()
	if starts != 0 || opens != 1 {
		t.Fatalf("StartChat=%d OpenStreamChat=%d, want 0/1", starts, opens)
	}
	// The attempt was consumed BEFORE the provider saw the request: the
	// only steps left after the gate are the attempt CAS and the network
	// call (第四轮 P1-1 target flow).
	if attemptAtOpen != 1 {
		t.Fatalf("attempt at HTTP open=%d, want 1 (attempt must be consumed before the submit)", attemptAtOpen)
	}
	if threadsAtOpen != 1 {
		t.Fatalf("agent threads at HTTP open=%d, want 1", threadsAtOpen)
	}
	waitUntil(t, "run succeeded", 10*time.Second, func() bool {
		return readRunState(t, f.env.db, f.runID).status == "succeeded"
	})
}

// TestAilySubmitHappyPathWithGateAllowed (control / 第四轮 P1-1): moving
// the gate deeper into the submit paths must not break the normal path —
// a background run with an enabled application and an active provider
// really reaches StartChat and converges to succeeded.
func TestAilySubmitHappyPathWithGateAllowed(t *testing.T) {
	f := newAilySubmitFixture(t, "r4happy", "background", "round4 happy")
	ctx := context.Background()

	if err := f.exec.Execute(ctx, f.claimed); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if starts, _ := f.rec.counted(); starts != 1 {
		t.Fatalf("StartChat=%d, want 1 (gate must not block an allowed run)", starts)
	}
	waitUntil(t, "run succeeded", 10*time.Second, func() bool {
		return readRunState(t, f.env.db, f.runID).status == "succeeded"
	})
}

// ── P2: staff execution diagnostics ─────────────────────────────────────

// TestStaffDiagnosesPrivateApplicationMissingBinding (第四轮 P2): staff
// bypass VISIBILITY only. A private application with no binding used to be
// reported as a vague forbidden/404 — hiding the real cause from exactly
// the people who can fix it.
func TestStaffDiagnosesPrivateApplicationMissingBinding(t *testing.T) {
	env := newScheduleEnv(t)
	uniq := fmt.Sprintf("%d", time.Now().UnixNano())
	appID := seedAuthzFixture(t, env.db, authzFixture{
		slug: "itest-r4-private-" + uniq, kind: "chat", isPublic: false, enabled: true,
	})
	svc := &catalog.Service{DB: env.db}

	if _, err := svc.AuthorizeExecution(context.Background(), appID, 42, true); !errors.Is(err, catalog.ErrNoBinding) {
		t.Fatalf("staff diagnosis: err=%v, want ErrNoBinding", err)
	}
	// A regular caller still gets the opaque, non-disclosing refusal.
	if _, err := svc.AuthorizeExecution(context.Background(), appID, 42, false); !errors.Is(err, catalog.ErrExecutionForbidden) {
		t.Fatalf("regular caller: err=%v, want ErrExecutionForbidden", err)
	}
}

// ── P2: non-terminal run predicate ──────────────────────────────────────

// TestNonTerminalStatusBlocksSecondTurn (第四轮 P2): the conversation
// admission guard used to count only ('queued','running'), so as soon as a
// provider reaches waiting_input / waiting_external / cancelling /
// interrupted the conversation looked idle and a SECOND run was admitted —
// breaking the one-turn-at-a-time invariant. "Active" must mean
// "non-terminal".
func TestNonTerminalStatusBlocksSecondTurn(t *testing.T) {
	env := newScheduleEnv(t)
	svc := execution.NewService(env.db, nil, silentLogger(), telemetry.NewMetrics("test"))
	ctx := context.Background()

	nonTerminal := []string{
		execution.StatusQueued, execution.StatusRunning,
		execution.StatusWaitingInput, execution.StatusWaitingExternal,
		execution.StatusCancelling, execution.StatusInterrupted,
	}
	for _, st := range nonTerminal {
		t.Run(st, func(t *testing.T) {
			convID := seedConversation(t, svc)
			first, err := svc.CreateRun(ctx, &execution.CreateRunInput{
				UserID: 42, ApplicationID: 1, ConversationID: convID,
				Provider: "itest_pred", RuntimeType: "agent",
				ExecutionMode: "interactive", Content: "first turn",
			})
			if err != nil {
				t.Fatalf("create first run: %v", err)
			}
			if _, err := env.db.ExecContext(ctx, `UPDATE runs SET status = ? WHERE id = ?`, st, first.ID.Bytes()); err != nil {
				t.Fatalf("force status %s: %v", st, err)
			}
			_, err = svc.CreateRun(ctx, &execution.CreateRunInput{
				UserID: 42, ApplicationID: 1, ConversationID: convID,
				Provider: "itest_pred", RuntimeType: "agent",
				ExecutionMode: "interactive", Content: "second turn",
			})
			if !errors.Is(err, execution.ErrConversationBusy) {
				t.Fatalf("second turn with a %s run: err=%v, want ErrConversationBusy", st, err)
			}
			// The delete guard shares the same predicate.
			if err := svc.ClearConversation(ctx, convID, 42); !errors.Is(err, execution.ErrConversationHasActiveRun) {
				t.Fatalf("clear with a %s run: err=%v, want ErrConversationHasActiveRun", st, err)
			}
		})
	}
}

// TestTerminalStatusFreesTheConversation (control): a terminal run must
// still allow the next turn — the widened predicate must not freeze
// conversations forever.
func TestTerminalStatusFreesTheConversation(t *testing.T) {
	env := newScheduleEnv(t)
	svc := execution.NewService(env.db, nil, silentLogger(), telemetry.NewMetrics("test"))
	ctx := context.Background()
	convID := seedConversation(t, svc)

	first, err := svc.CreateRun(ctx, &execution.CreateRunInput{
		UserID: 42, ApplicationID: 1, ConversationID: convID,
		Provider: "itest_pred", RuntimeType: "agent",
		ExecutionMode: "interactive", Content: "first turn",
	})
	if err != nil {
		t.Fatalf("create first run: %v", err)
	}
	if _, err := env.db.ExecContext(ctx, `UPDATE runs SET status = 'succeeded' WHERE id = ?`, first.ID.Bytes()); err != nil {
		t.Fatalf("force terminal status: %v", err)
	}
	if _, err := svc.CreateRun(ctx, &execution.CreateRunInput{
		UserID: 42, ApplicationID: 1, ConversationID: convID,
		Provider: "itest_pred", RuntimeType: "agent",
		ExecutionMode: "interactive", Content: "second turn",
	}); err != nil {
		t.Fatalf("second turn after a succeeded run: %v", err)
	}
}

// TestNonTerminalRunCountsAgainstUserOutstandingCap (第四轮 P2): the
// per-user cap is a bound on live work, so a waiting_input run must count
// against it too (it used to be invisible because the query only counted
// queued+running).
func TestNonTerminalRunCountsAgainstUserOutstandingCap(t *testing.T) {
	env := newScheduleEnv(t)
	svc := execution.NewService(env.db, nil, silentLogger(), telemetry.NewMetrics("test"))
	ctx := context.Background()
	// The cap is enforced under a users row lock, so the user must exist.
	userID := seedUser(t, env.db)
	convID := seedConversation(t, svc)

	first, err := svc.CreateRun(ctx, &execution.CreateRunInput{
		UserID: userID, ApplicationID: 1, ConversationID: convID,
		Provider: "itest_pred", RuntimeType: "agent",
		ExecutionMode: "interactive", Content: "first turn",
	})
	if err != nil {
		t.Fatalf("create first run: %v", err)
	}
	if _, err := env.db.ExecContext(ctx,
		`UPDATE runs SET status = ? WHERE id = ?`, execution.StatusWaitingInput, first.ID.Bytes()); err != nil {
		t.Fatalf("force waiting_input: %v", err)
	}
	_, err = svc.CreateRunAdmitted(ctx, &execution.CreateRunInput{
		UserID: userID, ApplicationID: 1,
		Provider: "itest_pred", RuntimeType: "agent",
		ExecutionMode: "interactive", Content: "another conversation",
	}, 1)
	if !errors.Is(err, execution.ErrUserOutstandingExceeded) {
		t.Fatalf("cap with a waiting_input run: err=%v, want ErrUserOutstandingExceeded", err)
	}
}

// TestLocalPreparationFailureDoesNotConsumeAttempt (第四轮 P1-1): a payload
// rejected by LOCAL validation (content over the provider limit) never
// reaches the provider and never consumes an attempt.
func TestLocalPreparationFailureDoesNotConsumeAttempt(t *testing.T) {
	overLimit := strings.Repeat("x", 10001) // maxTextChars = 10000
	f := newAilySubmitFixture(t, "r4validate", "background", overLimit)
	ctx := context.Background()

	if err := f.exec.Execute(ctx, f.claimed); err != nil {
		t.Fatalf("execute: %v", err)
	}
	starts, opens := f.rec.counted()
	if starts != 0 || opens != 0 {
		t.Fatalf("provider reached with invalid payload: StartChat=%d OpenStreamChat=%d, want 0/0", starts, opens)
	}
	st := readRunState(t, f.env.db, f.runID)
	if st.status != "failed" {
		t.Fatalf("status=%q, want failed", st.status)
	}
	if st.attempt != 0 {
		t.Fatalf("invalid payload consumed %d attempts, want 0", st.attempt)
	}
	if st.errorCode != "aily_capability_error" {
		t.Fatalf("error_code=%q, want aily_capability_error (input rejection, not internal)", st.errorCode)
	}
}
