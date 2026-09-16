package workerdispatch

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/creation-agent-studio/backend-go/internal/catalog"
	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
)

// ────────────────────────────────────────────────────────────── fakes ──

// fakeHandler counts Execute calls and can be told to fail.
type fakeHandler struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *fakeHandler) Execute(ctx context.Context, claimed *execution.ClaimedRun) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.err
}

func (f *fakeHandler) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeSink records Fail calls; err makes it fail (e.g. ErrLostOwnership).
type fakeSink struct {
	mu       sync.Mutex
	calls    int
	err      error
	lastCode string
	lastMsg  string
	lastRun  ids.ID
}

func (f *fakeSink) Fail(ctx context.Context, claimed *execution.ClaimedRun, errorCode string, message string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastCode = errorCode
	f.lastMsg = message
	if claimed != nil && claimed.Run != nil {
		f.lastRun = claimed.Run.ID
	}
	return f.err
}

func (f *fakeSink) state() (calls int, code, msg string, run ids.ID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.lastCode, f.lastMsg, f.lastRun
}

// ───────────────────────────────────────────────────────────── helpers ──

func newTestRegistry(t *testing.T, sink FailureSink) *Registry {
	t.Helper()
	return NewRegistry(sink, testLogger(), telemetry.NewMetrics("test"))
}

// testLogger discards the dispatcher's error-path chatter; these tests
// assert on state, not on logs.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// claimedRun builds a claimed run with the canonical columns (and optional
// snapshot) the dispatcher routes on.
func claimedRun(provider, runtimeType string, snapshot map[string]any) *execution.ClaimedRun {
	return &execution.ClaimedRun{
		Run: &execution.Run{
			ID:              ids.New(),
			Provider:        provider,
			RuntimeType:     runtimeType,
			RuntimeSnapshot: snapshot,
		},
	}
}

// registeredPlan registers spec and resolves it, failing the test on either
// error — every happy-path test starts here.
func registeredPlan(t *testing.T, r *Registry, spec ProviderSpec) *Plan {
	t.Helper()
	if err := r.RegisterProvider(spec); err != nil {
		t.Fatalf("RegisterProvider(%s): %v", spec.Key, err)
	}
	plan, err := r.ResolveProvider(spec.Key)
	if err != nil {
		t.Fatalf("ResolveProvider(%s): %v", spec.Key, err)
	}
	return plan
}

// ─────────────────────────────────────────────────────────────── tests ──

// TestRegisteredRuntimeIsDispatched: the one registered route receives the
// run, and the dispatcher itself touches nothing else (no terminal fail).
func TestRegisteredRuntimeIsDispatched(t *testing.T) {
	sink := &fakeSink{}
	r := newTestRegistry(t, sink)
	handler := &fakeHandler{}
	plan := registeredPlan(t, r, ProviderSpec{
		Key:    "feishu_aily",
		Routes: map[string]execution.Handler{catalog.RuntimeTypeAgent: handler},
	})

	run := claimedRun("feishu_aily", catalog.RuntimeTypeAgent, nil)
	if err := plan.Handler.Execute(context.Background(), run); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := handler.callCount(); got != 1 {
		t.Fatalf("agent handler calls = %d, want 1", got)
	}
	if calls, _, _, _ := sink.state(); calls != 0 {
		t.Fatalf("failure sink calls = %d, want 0 (a routed run must not be failed)", calls)
	}
}

// TestRuntimeTypeSelectsDifferentHandlers: THE Batch 5 test. Same provider,
// two runtime types — the runtime_type must really participate in routing,
// not decorate it.
func TestRuntimeTypeSelectsDifferentHandlers(t *testing.T) {
	sink := &fakeSink{}
	r := newTestRegistry(t, sink)
	agent := &fakeHandler{}
	workflow := &fakeHandler{}
	plan := registeredPlan(t, r, ProviderSpec{
		Key: "feishu_aily",
		Routes: map[string]execution.Handler{
			catalog.RuntimeTypeAgent:    agent,
			catalog.RuntimeTypeWorkflow: workflow,
		},
	})

	ctx := context.Background()
	agentRun := claimedRun("feishu_aily", catalog.RuntimeTypeAgent, nil)
	workflowRun := claimedRun("feishu_aily", catalog.RuntimeTypeWorkflow, nil)
	if err := plan.Handler.Execute(ctx, agentRun); err != nil {
		t.Fatalf("agent run: %v", err)
	}
	if err := plan.Handler.Execute(ctx, workflowRun); err != nil {
		t.Fatalf("workflow run: %v", err)
	}

	if got := agent.callCount(); got != 1 {
		t.Fatalf("agent handler calls = %d, want 1 (the agent run must reach only A)", got)
	}
	if got := workflow.callCount(); got != 1 {
		t.Fatalf("workflow handler calls = %d, want 1 (the workflow run must reach only B)", got)
	}
	if calls, _, _, _ := sink.state(); calls != 0 {
		t.Fatalf("failure sink calls = %d, want 0", calls)
	}
}

// TestProviderIsPartOfRouteIdentity: identical runtime_type under different
// providers must reach different handlers — route identity is the PAIR, so
// a provider-only registry cannot silently degrade into one.
func TestProviderIsPartOfRouteIdentity(t *testing.T) {
	sink := &fakeSink{}
	r := newTestRegistry(t, sink)
	handlerA := &fakeHandler{}
	handlerB := &fakeHandler{}
	registeredPlan(t, r, ProviderSpec{
		Key:    "provider-a",
		Routes: map[string]execution.Handler{catalog.RuntimeTypeAgent: handlerA},
	})
	planB := registeredPlan(t, r, ProviderSpec{
		Key:    "provider-b",
		Routes: map[string]execution.Handler{catalog.RuntimeTypeAgent: handlerB},
	})

	if err := planB.Handler.Execute(context.Background(),
		claimedRun("provider-b", catalog.RuntimeTypeAgent, nil)); err != nil {
		t.Fatalf("provider-b run: %v", err)
	}

	if got := handlerA.callCount(); got != 0 {
		t.Fatalf("provider-a handler calls = %d, want 0 (its worker never saw the run)", got)
	}
	if got := handlerB.callCount(); got != 1 {
		t.Fatalf("provider-b handler calls = %d, want 1", got)
	}
}

// TestCanonicalColumnsWinOverSnapshot: matching snapshot routes normally;
// a snapshot that disagrees with the canonical columns (present AND
// different) fails closed with runtime_route_snapshot_mismatch — the
// executor must never see a run whose own snapshot says it is something
// else.
func TestCanonicalColumnsWinOverSnapshot(t *testing.T) {
	sink := &fakeSink{}
	r := newTestRegistry(t, sink)
	handler := &fakeHandler{}
	plan := registeredPlan(t, r, ProviderSpec{
		Key:    "feishu_aily",
		Routes: map[string]execution.Handler{catalog.RuntimeTypeAgent: handler},
	})
	ctx := context.Background()

	// Phase 1: snapshot agrees → normal route.
	agree := claimedRun("feishu_aily", catalog.RuntimeTypeAgent, map[string]any{
		"provider_key": "feishu_aily",
		"runtime_type": catalog.RuntimeTypeAgent,
	})
	if err := plan.Handler.Execute(ctx, agree); err != nil {
		t.Fatalf("agreeing snapshot: %v", err)
	}
	if got := handler.callCount(); got != 1 {
		t.Fatalf("handler calls after agreeing snapshot = %d, want 1", got)
	}

	// Phase 2: snapshot says workflow while the canonical column says agent.
	// Executing it would burn a workflow resource id on the agent API.
	diverge := claimedRun("feishu_aily", catalog.RuntimeTypeAgent, map[string]any{
		"provider_key": "feishu_aily",
		"runtime_type": catalog.RuntimeTypeWorkflow,
	})
	if err := plan.Handler.Execute(ctx, diverge); err != nil {
		t.Fatalf("diverging snapshot must terminal-fail cleanly, got: %v", err)
	}
	if got := handler.callCount(); got != 1 {
		t.Fatalf("handler calls after diverging snapshot = %d, want 1 (the diverging run must not execute)", got)
	}
	calls, code, _, run := sink.state()
	if calls != 1 {
		t.Fatalf("failure sink calls = %d, want 1", calls)
	}
	if code != ErrCodeRuntimeRouteSnapshotMismatch {
		t.Fatalf("failure code = %q, want %q", code, ErrCodeRuntimeRouteSnapshotMismatch)
	}
	if run != diverge.Run.ID {
		t.Fatalf("failed run = %s, want the diverging run %s", run, diverge.Run.ID)
	}
}

// TestProviderMismatchFailsClosed: a run from another provider claimed by
// this worker must reach NO executor and be failed with
// worker_provider_mismatch.
func TestProviderMismatchFailsClosed(t *testing.T) {
	sink := &fakeSink{}
	r := newTestRegistry(t, sink)
	handler := &fakeHandler{}
	plan := registeredPlan(t, r, ProviderSpec{
		Key:    "feishu_aily",
		Routes: map[string]execution.Handler{catalog.RuntimeTypeAgent: handler},
	})

	foreign := claimedRun("other_provider", catalog.RuntimeTypeAgent, nil)
	if err := plan.Handler.Execute(context.Background(), foreign); err != nil {
		t.Fatalf("provider mismatch must terminal-fail cleanly, got: %v", err)
	}
	if got := handler.callCount(); got != 0 {
		t.Fatalf("handler calls = %d, want 0 (a foreign run must never execute here)", got)
	}
	calls, code, _, _ := sink.state()
	if calls != 1 {
		t.Fatalf("failure sink calls = %d, want 1", calls)
	}
	if code != ErrCodeWorkerProviderMismatch {
		t.Fatalf("failure code = %q, want %q", code, ErrCodeWorkerProviderMismatch)
	}
}

// TestMissingRuntimeFailsClosed: a runtime this binary does not carry must
// terminal-fail with runtime_handler_unavailable — never fall back to the
// agent route, never ride the lease/reaper loop as a generic error.
func TestMissingRuntimeFailsClosed(t *testing.T) {
	sink := &fakeSink{}
	r := newTestRegistry(t, sink)
	handler := &fakeHandler{}
	plan := registeredPlan(t, r, ProviderSpec{
		Key:    "feishu_aily",
		Routes: map[string]execution.Handler{catalog.RuntimeTypeAgent: handler},
	})

	run := claimedRun("feishu_aily", catalog.RuntimeTypeWorkflow, nil)
	if err := plan.Handler.Execute(context.Background(), run); err != nil {
		t.Fatalf("missing route must terminal-fail cleanly, got: %v", err)
	}
	if got := handler.callCount(); got != 0 {
		t.Fatalf("handler calls = %d, want 0 (a missing route must not fall back to agent)", got)
	}
	calls, code, msg, _ := sink.state()
	if calls != 1 {
		t.Fatalf("failure sink calls = %d, want 1", calls)
	}
	if code != ErrCodeRuntimeHandlerUnavailable {
		t.Fatalf("failure code = %q, want %q", code, ErrCodeRuntimeHandlerUnavailable)
	}
	if !strings.Contains(msg, "provider=feishu_aily") || !strings.Contains(msg, "runtime_type=workflow") {
		t.Fatalf("failure message %q must name provider and runtime_type", msg)
	}
}

// TestMissingRuntimeFailureLostOwnershipPropagates: when the terminal fail
// itself loses the ownership fence, the dispatcher must surface
// ErrLostOwnership raw — the Worker distinguishes ownership loss from
// ordinary failures, and a disguised error would corrupt that decision.
func TestMissingRuntimeFailureLostOwnershipPropagates(t *testing.T) {
	sink := &fakeSink{err: execution.ErrLostOwnership}
	r := newTestRegistry(t, sink)
	handler := &fakeHandler{}
	plan := registeredPlan(t, r, ProviderSpec{
		Key:    "feishu_aily",
		Routes: map[string]execution.Handler{catalog.RuntimeTypeAgent: handler},
	})

	run := claimedRun("feishu_aily", catalog.RuntimeTypeWorkflow, nil)
	err := plan.Handler.Execute(context.Background(), run)
	if !errors.Is(err, execution.ErrLostOwnership) {
		t.Fatalf("Execute err = %v, want ErrLostOwnership raw (not %v)", err, err)
	}
	if got := handler.callCount(); got != 0 {
		t.Fatalf("handler calls = %d, want 0", got)
	}
}

// TestHandlerErrorPassesThrough: the executor's own error is returned
// untouched — no retry, no finalize, no dispatcher-side Fail.
func TestHandlerErrorPassesThrough(t *testing.T) {
	sentinel := errors.New("aily: executor exploded")
	sink := &fakeSink{}
	r := newTestRegistry(t, sink)
	handler := &fakeHandler{err: sentinel}
	plan := registeredPlan(t, r, ProviderSpec{
		Key:    "feishu_aily",
		Routes: map[string]execution.Handler{catalog.RuntimeTypeAgent: handler},
	})

	run := claimedRun("feishu_aily", catalog.RuntimeTypeAgent, nil)
	err := plan.Handler.Execute(context.Background(), run)
	if !errors.Is(err, sentinel) {
		t.Fatalf("Execute err = %v, want the handler's own sentinel %v", err, sentinel)
	}
	if calls, _, _, _ := sink.state(); calls != 0 {
		t.Fatalf("failure sink calls = %d, want 0 (the executor owns its state machine)", calls)
	}
}

// TestSnapshotProviderMismatchFailsClosed: the provider half of the frozen
// snapshot guard gets its OWN deterministic proof (Batch 5.1, P2-3). The
// runtime_type column agrees on purpose, so the only way this test fails is
// a diverging snapshot provider_key — deleting the runtime guard (or the
// lookup) cannot make it pass or fail.
func TestSnapshotProviderMismatchFailsClosed(t *testing.T) {
	sink := &fakeSink{}
	r := newTestRegistry(t, sink)
	handler := &fakeHandler{}
	plan := registeredPlan(t, r, ProviderSpec{
		Key:    "feishu_aily",
		Routes: map[string]execution.Handler{catalog.RuntimeTypeAgent: handler},
	})

	run := claimedRun("feishu_aily", catalog.RuntimeTypeAgent, map[string]any{
		"provider_key": "other_provider",
		"runtime_type": catalog.RuntimeTypeAgent,
	})
	if err := plan.Handler.Execute(context.Background(), run); err != nil {
		t.Fatalf("provider snapshot mismatch must terminal-fail cleanly, got: %v", err)
	}
	if got := handler.callCount(); got != 0 {
		t.Fatalf("handler calls = %d, want 0", got)
	}
	calls, code, _, _ := sink.state()
	if calls != 1 {
		t.Fatalf("failure sink calls = %d, want 1", calls)
	}
	if code != ErrCodeRuntimeRouteSnapshotMismatch {
		t.Fatalf("failure code = %q, want %q", code, ErrCodeRuntimeRouteSnapshotMismatch)
	}
}

// TestDuplicateProviderRegistrationRejected: the second registration of the
// same provider must be an error — last-write-wins would hide a double
// wiring.
func TestDuplicateProviderRegistrationRejected(t *testing.T) {
	sink := &fakeSink{}
	r := newTestRegistry(t, sink)
	spec := ProviderSpec{
		Key:    "feishu_aily",
		Routes: map[string]execution.Handler{catalog.RuntimeTypeAgent: &fakeHandler{}},
	}
	if err := r.RegisterProvider(spec); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	if err := r.RegisterProvider(spec); !errors.Is(err, ErrDuplicateProvider) {
		t.Fatalf("second registration err = %v, want ErrDuplicateProvider", err)
	}
}

// TestNilHandlerRegistrationRejected: a nil route must die at boot, not at
// the first business request.
func TestNilHandlerRegistrationRejected(t *testing.T) {
	sink := &fakeSink{}
	r := newTestRegistry(t, sink)
	err := r.RegisterProvider(ProviderSpec{
		Key:    "feishu_aily",
		Routes: map[string]execution.Handler{catalog.RuntimeTypeAgent: nil},
	})
	if !errors.Is(err, ErrNilHandler) {
		t.Fatalf("err = %v, want ErrNilHandler", err)
	}
	if _, err := r.ResolveProvider("feishu_aily"); !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("a rejected spec must not be registered; ResolveProvider err = %v", err)
	}
}

// TestTypedNilHandlerRegistrationRejected: Go's typed-nil trap — a nil *T
// stored in the execution.Handler interface is NOT `== nil`. The plain-nil
// check would register the wiring and only panic at the first business
// request; registration must refuse it at boot instead (Batch 5.1, P2-2).
func TestTypedNilHandlerRegistrationRejected(t *testing.T) {
	sink := &fakeSink{}
	r := newTestRegistry(t, sink)
	var handler *fakeHandler // typed nil: non-nil interface, nil pointer
	err := r.RegisterProvider(ProviderSpec{
		Key:    "feishu_aily",
		Routes: map[string]execution.Handler{catalog.RuntimeTypeAgent: handler},
	})
	if !errors.Is(err, ErrNilHandler) {
		t.Fatalf("err = %v, want ErrNilHandler", err)
	}
	if _, err := r.ResolveProvider("feishu_aily"); !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("a rejected spec must not be registered; ResolveProvider err = %v", err)
	}
}

// TestNilFailureSinkRegistrationRejected: without a working sink the
// dispatcher cannot terminal-fail unroutable runs — that is a boot wiring
// error, not something to discover mid-traffic.
func TestNilFailureSinkRegistrationRejected(t *testing.T) {
	r := NewRegistry(nil, testLogger(), telemetry.NewMetrics("test"))
	err := r.RegisterProvider(ProviderSpec{
		Key:    "feishu_aily",
		Routes: map[string]execution.Handler{catalog.RuntimeTypeAgent: &fakeHandler{}},
	})
	if !errors.Is(err, ErrNilFailureSink) {
		t.Fatalf("err = %v, want ErrNilFailureSink", err)
	}
}

// TestTypedNilFailureSinkRegistrationRejected: the same typed-nil trap on
// the sink interface (Batch 5.1, P2-2).
func TestTypedNilFailureSinkRegistrationRejected(t *testing.T) {
	var sink *fakeSink // typed nil: non-nil FailureSink interface, nil pointer
	r := NewRegistry(sink, testLogger(), telemetry.NewMetrics("test"))
	err := r.RegisterProvider(ProviderSpec{
		Key:    "feishu_aily",
		Routes: map[string]execution.Handler{catalog.RuntimeTypeAgent: &fakeHandler{}},
	})
	if !errors.Is(err, ErrNilFailureSink) {
		t.Fatalf("err = %v, want ErrNilFailureSink", err)
	}
}

// TestTypedNilHealthProbeRejected: HealthFunc(nil) inside the HealthProbe
// interface is non-nil and would panic the worker's metrics goroutine on
// its first tick — a panic in a plain goroutine kills the whole process.
// Registration must refuse it (Batch 5.1, P2-2).
func TestTypedNilHealthProbeRejected(t *testing.T) {
	sink := &fakeSink{}
	r := newTestRegistry(t, sink)
	var health HealthFunc // nil function inside a non-nil interface
	err := r.RegisterProvider(ProviderSpec{
		Key:    "feishu_aily",
		Routes: map[string]execution.Handler{catalog.RuntimeTypeAgent: &fakeHandler{}},
		Health: health,
	})
	if !errors.Is(err, ErrNilHealthProbe) {
		t.Fatalf("err = %v, want ErrNilHealthProbe", err)
	}
	if _, err := r.ResolveProvider("feishu_aily"); !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("a rejected spec must not be registered; ResolveProvider err = %v", err)
	}
}

// TestNoneRuntimeCannotBeRegistered: "none" means "no worker execution" —
// a route claiming to execute it is a contradiction that must be refused.
func TestNoneRuntimeCannotBeRegistered(t *testing.T) {
	sink := &fakeSink{}
	r := newTestRegistry(t, sink)
	err := r.RegisterProvider(ProviderSpec{
		Key:    "feishu_aily",
		Routes: map[string]execution.Handler{catalog.RuntimeTypeNone: &fakeHandler{}},
	})
	if !errors.Is(err, ErrRuntimeTypeNone) {
		t.Fatalf("err = %v, want ErrRuntimeTypeNone", err)
	}
}

// TestProviderSlotMustBelongToProvider: slots wired to another provider
// would let its executions burn THIS provider's capacity — refuse at boot.
func TestProviderSlotMustBelongToProvider(t *testing.T) {
	sink := &fakeSink{}
	r := newTestRegistry(t, sink)
	err := r.RegisterProvider(ProviderSpec{
		Key: "other_agent",
		Slots: &execution.ProviderSlots{
			Provider: "feishu_aily",
		},
		Routes: map[string]execution.Handler{catalog.RuntimeTypeAgent: &fakeHandler{}},
	})
	if !errors.Is(err, ErrProviderSlotMismatch) {
		t.Fatalf("err = %v, want ErrProviderSlotMismatch", err)
	}
}

// TestUnknownProviderCannotResolve: resolving an unregistered provider is
// an error — never a fallback to the first (or only) registered provider.
func TestUnknownProviderCannotResolve(t *testing.T) {
	sink := &fakeSink{}
	r := newTestRegistry(t, sink)
	registeredPlan(t, r, ProviderSpec{
		Key:    "feishu_aily",
		Routes: map[string]execution.Handler{catalog.RuntimeTypeAgent: &fakeHandler{}},
	})

	plan, err := r.ResolveProvider("missing")
	if !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("err = %v, want ErrUnknownProvider", err)
	}
	if plan != nil {
		t.Fatalf("plan = %+v, want nil", plan)
	}
}

// TestRegisteredRoutesAreImmutable: mutating the caller's map after
// registration must not re-route live traffic — the registry keeps its own
// copy.
func TestRegisteredRoutesAreImmutable(t *testing.T) {
	sink := &fakeSink{}
	r := newTestRegistry(t, sink)
	handlerA := &fakeHandler{}
	handlerB := &fakeHandler{}
	routes := map[string]execution.Handler{catalog.RuntimeTypeAgent: handlerA}
	plan := registeredPlan(t, r, ProviderSpec{
		Key:    "feishu_aily",
		Routes: routes,
	})

	routes[catalog.RuntimeTypeAgent] = handlerB // the caller rewires its map

	if err := plan.Handler.Execute(context.Background(),
		claimedRun("feishu_aily", catalog.RuntimeTypeAgent, nil)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := handlerA.callCount(); got != 1 {
		t.Fatalf("original handler calls = %d, want 1 (registry routes must be a defensive copy)", got)
	}
	if got := handlerB.callCount(); got != 0 {
		t.Fatalf("replacement handler calls = %d, want 0", got)
	}
}

// TestDispatcherConcurrentExecuteRaceSafe: after boot the registry is
// read-only; concurrent Execute across resolved plans must be race-free
// (run under `go test -race`).
func TestDispatcherConcurrentExecuteRaceSafe(t *testing.T) {
	sink := &fakeSink{}
	r := newTestRegistry(t, sink)
	agent := &fakeHandler{}
	workflow := &fakeHandler{}
	plan := registeredPlan(t, r, ProviderSpec{
		Key: "feishu_aily",
		Routes: map[string]execution.Handler{
			catalog.RuntimeTypeAgent:    agent,
			catalog.RuntimeTypeWorkflow: workflow,
		},
	})

	const goroutines = 100
	const perGoroutine = 10
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			ctx := context.Background()
			var run *execution.ClaimedRun
			for i := 0; i < perGoroutine; i++ {
				if g%2 == 0 {
					run = claimedRun("feishu_aily", catalog.RuntimeTypeAgent, nil)
				} else {
					run = claimedRun("feishu_aily", catalog.RuntimeTypeWorkflow, nil)
				}
				if err := plan.Handler.Execute(ctx, run); err != nil {
					t.Errorf("concurrent Execute: %v", err)
					return
				}
			}
		}(g)
	}
	wg.Wait()

	total := agent.callCount() + workflow.callCount()
	if total != goroutines*perGoroutine {
		t.Fatalf("handler calls = %d, want %d", total, goroutines*perGoroutine)
	}
	if calls, _, _, _ := sink.state(); calls != 0 {
		t.Fatalf("failure sink calls = %d, want 0", calls)
	}
}
