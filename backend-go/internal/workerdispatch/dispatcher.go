// Package workerdispatch routes a claimed Run to the concrete execution
// Handler that owns its (provider, runtime_type) pair.
//
// Batch 5 (第十轮). The execution kernel — CAS claim, lease, heartbeat,
// reaper, ProviderSlots, gate — is FROZEN and provider-neutral: it speaks to
// a single execution.Handler. Until Batch 5 that Handler was hard-wired to
// the Aily executor in cmd/worker, so EVERY provider would have been executed
// through the Aily state machine. The Registry closes that gap without
// touching the frozen kernel:
//
//	execution.Worker (claim / lease / slot / gate)
//	    ↓  one Handler
//	Registry.ResolveProvider(provider) → Plan
//	    ↓
//	providerScopedDispatcher  (worker provider guard, snapshot guard,
//	                           runtime_type route lookup)
//	    ↓
//	concrete Executor (Aily agent today; workflow / http / local_task later)
//
// Route identity is CANONICAL: Run.Provider + Run.RuntimeType — the columns
// frozen at CreateRun. The runtime snapshot is never the primary route
// value; its provider_key/runtime_type fields are only cross-checked
// (present AND different → fail closed), because a snapshot/canonical
// divergence means the executor would burn a workflow resource id on an
// agent API or vice versa.
//
// What the dispatcher does NOT own (Batch 5 §17): claim, lease, heartbeat,
// slots, priority, retry policy, the submission ledger, provider retries,
// the gates, timeouts, final reconciliation, SSE. It picks a Handler and it
// terminal-fails the runs IT cannot route — nothing else. In particular it
// has no recover(): the Worker already owns panic recovery, and a second
// layer would change the existing retry semantics.
package workerdispatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"sync"

	"github.com/creation-agent-studio/backend-go/internal/catalog"
	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
)

// isNilLike reports whether v is nil in a way that would panic when used —
// including Go's TYPED-NIL trap: a nil *T stored in an interface is NOT
// `== nil` (the interface carries the type), so a plain `handler == nil`
// check lets `var executor *SomeExecutor` through registration and only
// panics at the first business request. Registration must reject it at
// boot instead (Batch 5.1, P2-2).
func isNilLike(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan,
		reflect.Func,
		reflect.Interface,
		reflect.Map,
		reflect.Pointer,
		reflect.Slice,
		reflect.UnsafePointer:
		return rv.IsNil()
	default:
		return false
	}
}

// Dispatch result values — the closed `result` label set of
// studio_worker_dispatch_total. `routed` means the Handler was FOUND and
// CALLED; it says nothing about the run outcome (studio_runs_total owns
// that).
const (
	DispatchRouted           = "routed"
	DispatchProviderMismatch = "provider_mismatch"
	DispatchSnapshotMismatch = "snapshot_mismatch"
	DispatchRouteMissing     = "route_missing"
)

// Canonical error codes the dispatcher writes into terminal run failures.
// These surface to the user as run.error_code, so they are part of the
// product contract — never rename one without a migration story.
const (
	// ErrCodeWorkerProviderMismatch: the run was claimed by a worker binary
	// consuming a DIFFERENT provider's queue. Cannot happen through the
	// outbox routing; if it does, the wiring is wrong and nothing may be
	// executed.
	ErrCodeWorkerProviderMismatch = "worker_provider_mismatch"
	// ErrCodeRuntimeRouteSnapshotMismatch: the frozen runtime snapshot
	// disagrees with the canonical runs columns about which runtime this is.
	ErrCodeRuntimeRouteSnapshotMismatch = "runtime_route_snapshot_mismatch"
	// ErrCodeRuntimeHandlerUnavailable: this provider has no executor
	// registered for the run's runtime_type (e.g. the binary predates the
	// workflow executor). Terminal fail — NEVER a fallback to another route.
	ErrCodeRuntimeHandlerUnavailable = "runtime_handler_unavailable"
)

// Registration and resolution failures. All are boot-time fatal by
// contract: app.Build propagates them, so a wiring bug can never survive to
// serve traffic (Batch 5 §22 — startup error, not a warning).
var (
	ErrEmptyProviderKey     = errors.New("workerdispatch: provider key must not be empty")
	ErrNoRoutes             = errors.New("workerdispatch: provider must register at least one runtime route")
	ErrEmptyRuntimeType     = errors.New("workerdispatch: runtime_type must not be empty")
	ErrRuntimeTypeNone      = errors.New("workerdispatch: runtime_type \"none\" must not enter the worker (fixed-page apps never create runs)")
	ErrNilHandler           = errors.New("workerdispatch: runtime route handler must not be nil")
	ErrProviderSlotMismatch = errors.New("workerdispatch: ProviderSlots belong to a different provider")
	ErrDuplicateProvider    = errors.New("workerdispatch: provider already registered (duplicate registration is a wiring bug, not a replacement)")
	ErrUnknownProvider      = errors.New("workerdispatch: provider is not registered")
	// ErrNilFailureSink: the registry cannot terminal-fail unroutable runs
	// without a working FailureSink, so a nil (or typed-nil) one is a boot
	// wiring error, not a defer-to-first-failure one (Batch 5.1, P2-2).
	ErrNilFailureSink = errors.New("workerdispatch: failure sink must not be nil")
	// ErrNilHealthProbe: HealthFunc(nil) inside the interface is non-nil
	// and would panic the worker's metrics goroutine on its first tick —
	// a panic in a plain goroutine kills the whole process (Batch 5.1).
	ErrNilHealthProbe = errors.New("workerdispatch: health probe must not be a nil function")
)

// HealthProbe reports whether the provider's rate limiters are running on
// their local (Redis-unreachable) fallback. Provider-scoped on purpose: one
// worker process serves ONE provider, so one degraded verdict per plan is
// the whole story (Batch 5 §20).
type HealthProbe interface {
	LimiterDegraded() bool
}

// HealthFunc adapts a plain function to HealthProbe.
type HealthFunc func() bool

// LimiterDegraded implements HealthProbe.
func (f HealthFunc) LimiterDegraded() bool { return f() }

// FailureSink is the MINIMAL write surface the dispatcher needs: an
// ownership-fenced terminal fail. Production passes runs.WorkerOwned();
// tests pass a fake without a database. The dispatcher deliberately does
// NOT hold *execution.Service — least-privilege wiring (Batch 5 §8).
type FailureSink interface {
	Fail(ctx context.Context, claimed *execution.ClaimedRun, errorCode string, message string) error
}

// ProviderSpec registers one provider and every runtime it can execute.
//
// Slots are provider-WIDE: all runtime routes of one provider share the
// same semaphore, because the external provider only sees aggregate
// concurrency (Batch 5 §7 — per-runtime slots would silently multiply the
// real limit).
type ProviderSpec struct {
	Key string

	// Slots is the provider-wide concurrency semaphore. Optional at the
	// type level (a provider without durable capacity), but when present it
	// MUST belong to Key.
	Slots *execution.ProviderSlots

	// Routes maps runtime_type → executor. At least one entry, none nil,
	// never the "none" runtime.
	Routes map[string]execution.Handler

	// Health aggregates the provider's limiter state for the worker
	// metrics goroutine. Optional.
	Health HealthProbe
}

// Plan is what a worker binary gets for its --provider flag: everything
// needed to build the frozen execution.Worker, plus the dispatcher that
// picks the concrete executor per run.
type Plan struct {
	Provider string
	Handler  execution.Handler // a providerScopedDispatcher, never a bare executor
	Slots    *execution.ProviderSlots
	Health   HealthProbe

	// RuntimeTypes lists the registered runtime types (sorted). Startup
	// logs it; tests pin it.
	RuntimeTypes []string
}

// providerEntry is the immutable registry record. routes is a defensive
// copy — the caller keeping its own map alive afterwards must not be able
// to re-route live traffic (Batch 5 §34, TestRegisteredRoutesAreImmutable).
type providerEntry struct {
	key    string
	slots  *execution.ProviderSlots
	health HealthProbe
	routes map[string]execution.Handler
}

// Registry owns the provider → executor wiring. Registration happens at
// boot and is validated HARD; after startup the registry is read-only, so
// concurrent Execute needs only the read path (Batch 5 §35).
type Registry struct {
	mu sync.RWMutex

	providers map[string]*providerEntry

	failureSink FailureSink
	log         *slog.Logger
	metrics     *telemetry.Metrics
}

// NewRegistry builds the dispatch registry. failureSink receives every
// terminal fail the dispatcher itself produces (production:
// runs.WorkerOwned(), which stays behind the ownership fence).
func NewRegistry(failureSink FailureSink, log *slog.Logger, metrics *telemetry.Metrics) *Registry {
	if log == nil {
		log = slog.Default()
	}
	return &Registry{
		providers:   make(map[string]*providerEntry),
		failureSink: failureSink,
		log:         log,
		metrics:     metrics,
	}
}

// RegisterProvider validates the spec strictly and registers it. Any error
// here is a wiring bug; app.Build must fail the process, not log a warning.
//
// Validation contract (Batch 5 §9, hardened by 5.1 P2-2):
//   - the registry's FailureSink must be usable (nil or typed-nil refused —
//     the dispatcher needs it to terminal-fail unroutable runs);
//   - key must not be empty;
//   - at least one route — a provider with no executor must not start a worker;
//   - runtime_type must not be empty;
//   - runtime_type "none" is refused — fixed-page apps never create runs,
//     so no worker may ever claim to execute them;
//   - handlers must not be nil (including TYPED nil — a nil *T inside the
//     interface passes `== nil` and would panic at the first request);
//   - Health, when present, must be a real probe (HealthFunc(nil) inside
//     the interface would panic the worker metrics goroutine);
//   - Slots, when present, must belong to THIS provider — otherwise
//     other_agent executions would silently burn feishu_aily capacity;
//   - duplicate registration is refused (last-write-wins would hide a
//     double wiring of the same provider).
func (r *Registry) RegisterProvider(spec ProviderSpec) error {
	if isNilLike(r.failureSink) {
		return ErrNilFailureSink
	}
	if spec.Key == "" {
		return ErrEmptyProviderKey
	}
	if len(spec.Routes) == 0 {
		return fmt.Errorf("%w: provider %q", ErrNoRoutes, spec.Key)
	}
	for rt, handler := range spec.Routes {
		if rt == "" {
			return fmt.Errorf("%w: provider %q", ErrEmptyRuntimeType, spec.Key)
		}
		if rt == catalog.RuntimeTypeNone {
			return fmt.Errorf("%w: provider %q", ErrRuntimeTypeNone, spec.Key)
		}
		if isNilLike(handler) {
			return fmt.Errorf("%w: provider %q runtime %q", ErrNilHandler, spec.Key, rt)
		}
	}
	if spec.Health != nil && isNilLike(spec.Health) {
		return fmt.Errorf("%w: provider %q", ErrNilHealthProbe, spec.Key)
	}
	if spec.Slots != nil && spec.Slots.Provider != spec.Key {
		return fmt.Errorf("%w: spec key %q, slots provider %q",
			ErrProviderSlotMismatch, spec.Key, spec.Slots.Provider)
	}

	routes := make(map[string]execution.Handler, len(spec.Routes))
	for rt, handler := range spec.Routes {
		routes[rt] = handler
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.providers[spec.Key]; exists {
		return fmt.Errorf("%w: %q", ErrDuplicateProvider, spec.Key)
	}
	r.providers[spec.Key] = &providerEntry{
		key:    spec.Key,
		slots:  spec.Slots,
		health: spec.Health,
		routes: routes,
	}
	return nil
}

// ResolveProvider returns the dispatch plan for one provider. Unknown
// providers are an ERROR — never a fallback to whatever happens to be
// registered first (Batch 5 §26/§34).
func (r *Registry) ResolveProvider(provider string) (*Plan, error) {
	r.mu.RLock()
	entry, ok := r.providers[provider]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownProvider, provider)
	}

	types := make([]string, 0, len(entry.routes))
	for rt := range entry.routes {
		types = append(types, rt)
	}
	sort.Strings(types)

	return &Plan{
		Provider: entry.key,
		Handler: &providerScopedDispatcher{
			expectedProvider: entry.key,
			routes:           entry.routes,
			failureSink:      r.failureSink,
			log:              r.log,
			metrics:          r.metrics,
		},
		Slots:        entry.slots,
		Health:       entry.health,
		RuntimeTypes: types,
	}, nil
}

// providerScopedDispatcher implements execution.Handler for ONE provider:
// it guards the claim against worker/snapshot drift, then hands the run to
// the executor registered for its canonical (provider, runtime_type) pair.
type providerScopedDispatcher struct {
	expectedProvider string
	routes           map[string]execution.Handler

	failureSink FailureSink
	log         *slog.Logger
	metrics     *telemetry.Metrics
}

// Execute runs the fixed dispatch order (Batch 5 §12):
//
//  1. validate claimed/run
//  2. worker provider consistency
//  3. frozen snapshot route consistency
//  4. runtime route lookup
//  5. dispatch metric
//  6. handler.Execute
//
// It NEVER re-resolves the current binding: the run's columns were frozen
// at CreateRun and stay authoritative for the run's whole life.
func (d *providerScopedDispatcher) Execute(ctx context.Context, claimed *execution.ClaimedRun) error {
	// 1. A nil claim is a scheduler bug, not a run state — there is no
	// ownership to fence a terminal fail against, so it surfaces as a plain
	// error for the Worker's existing failure path.
	if claimed == nil || claimed.Run == nil {
		return errors.New("workerdispatch: claimed run or its run is nil")
	}
	run := claimed.Run

	// 2. Worker provider consistency (§15). The outbox routes by provider,
	// so a cross-provider claim means broken wiring — nothing may execute.
	if run.Provider != d.expectedProvider {
		d.observe(run, DispatchProviderMismatch)
		d.log.Error("worker provider mismatch",
			"expected_provider", d.expectedProvider,
			"actual_provider", run.Provider,
			"runtime_type", run.RuntimeType)
		return d.failTerminal(ctx, claimed, ErrCodeWorkerProviderMismatch,
			fmt.Sprintf("claimed run provider %q does not match this worker's provider %q",
				run.Provider, d.expectedProvider))
	}

	// 3. Frozen snapshot route consistency (§14). Only "present AND
	// different" fails — older rows without the snapshot fields route on.
	if snapProvider := run.SnapshotString("provider_key"); snapProvider != "" && snapProvider != run.Provider {
		d.observe(run, DispatchSnapshotMismatch)
		d.log.Error("worker runtime route snapshot mismatch",
			"provider", run.Provider,
			"runtime_type", run.RuntimeType,
			"snapshot_provider", snapProvider)
		return d.failTerminal(ctx, claimed, ErrCodeRuntimeRouteSnapshotMismatch,
			fmt.Sprintf("runtime snapshot provider_key %q diverges from canonical runs.provider %q",
				snapProvider, run.Provider))
	}
	if snapRuntime := run.SnapshotString("runtime_type"); snapRuntime != "" && snapRuntime != run.RuntimeType {
		d.observe(run, DispatchSnapshotMismatch)
		d.log.Error("worker runtime route snapshot mismatch",
			"provider", run.Provider,
			"runtime_type", run.RuntimeType,
			"snapshot_runtime_type", snapRuntime)
		return d.failTerminal(ctx, claimed, ErrCodeRuntimeRouteSnapshotMismatch,
			fmt.Sprintf("runtime snapshot runtime_type %q diverges from canonical runs.runtime_type %q",
				snapRuntime, run.RuntimeType))
	}

	// 4. Runtime route lookup (§16). A missing route terminal-fails the run
	// through the ownership fence — never a fallback to another runtime
	// (that would run a workflow id through the agent API), and never a
	// generic error (that would ride the lease/reaper loop forever).
	handler, ok := d.routes[run.RuntimeType]
	if !ok || handler == nil {
		d.observe(run, DispatchRouteMissing)
		d.log.Error("worker runtime route unavailable",
			"provider", run.Provider,
			"runtime_type", run.RuntimeType)
		return d.failTerminal(ctx, claimed, ErrCodeRuntimeHandlerUnavailable,
			fmt.Sprintf("no worker runtime handler is registered for provider=%s runtime_type=%s",
				run.Provider, run.RuntimeType))
	}

	// 5. Dispatch metric: routed = found and called. The run's own outcome
	// stays with studio_runs_total.
	d.observe(run, DispatchRouted)

	// 6. Handler passthrough (§18): the concrete executor owns its
	// execution state machine; the dispatcher never retries, never
	// finalizes, never rewrites the error into a fail.
	return handler.Execute(ctx, claimed)
}

// failTerminal drives the run to failed through the ownership fence.
//
// The sink's error propagates RAW: ErrLostOwnership must reach the Worker
// as ownership loss, not be disguised as an ordinary dispatch error. A nil
// sink is a wiring bug that refuses to stay silent — the run would
// otherwise ride its lease into the reaper with no trace of why.
func (d *providerScopedDispatcher) failTerminal(ctx context.Context, claimed *execution.ClaimedRun, errorCode, message string) error {
	if d.failureSink == nil {
		return fmt.Errorf("workerdispatch: failure sink is nil; cannot terminal-fail run with %s (%s)",
			errorCode, message)
	}
	return d.failureSink.Fail(ctx, claimed, errorCode, message)
}

// observe increments the bounded dispatch counter. Labels are
// (provider, runtime_type, result) — a closed result enum, no run/user/
// conversation dimensions: this metric answers "how are claims being
// routed", never "what happened to run X" (Batch 5 §19).
func (d *providerScopedDispatcher) observe(run *execution.Run, result string) {
	if d.metrics == nil {
		return
	}
	d.metrics.WorkerDispatchTotal.
		WithLabelValues(run.Provider, run.RuntimeType, result).Inc()
}
