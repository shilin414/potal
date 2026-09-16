// Package app assembles the full dependency graph from config so the
// three binaries (api / stream / worker) share identical wiring.
package app

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/platform/crypto"

	"github.com/creation-agent-studio/backend-go/internal/automation/schedule"
	"github.com/creation-agent-studio/backend-go/internal/automation/scheduler"
	"github.com/creation-agent-studio/backend-go/internal/catalog"
	"github.com/creation-agent-studio/backend-go/internal/delivery"
	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/identity"
	aily "github.com/creation-agent-studio/backend-go/internal/integrations/aily"
	"github.com/creation-agent-studio/backend-go/internal/integrations/aily/bridge"
	"github.com/creation-agent-studio/backend-go/internal/platform/config"
	"github.com/creation-agent-studio/backend-go/internal/platform/database"
	"github.com/creation-agent-studio/backend-go/internal/platform/httpclient"
	"github.com/creation-agent-studio/backend-go/internal/platform/redisx"
	"github.com/creation-agent-studio/backend-go/internal/platform/storage"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
	"github.com/creation-agent-studio/backend-go/internal/transport/sse"
)

// App holds every shared dependency.
type App struct {
	Cfg     *config.Config
	Log     *slog.Logger
	DB      *sql.DB
	Redis   *redisx.Client
	Metrics *telemetry.Metrics
	Storage storage.Storage

	IdentityRepo *identity.Repo
	Sessions     *identity.SessionStore
	StateCodec   *identity.StateCodec
	Oauth        *identity.ExchangeOrchestrator
	Feishu       *identity.FeishuClient

	Catalog     *catalog.Service
	CatalogRepo *catalog.Repo
	Registry    *catalog.RuntimeRegistry

	Runs         *execution.Service
	ArtifactsRL  *execution.RateLimiter
	AilyExecutor *aily.Executor
	// RunAdmissionRL is the long-lived per-user run-creation limiter
	// (评测 P1-7): one object, dynamic keys, per-key fallback state.
	RunAdmissionRL *execution.RateLimiter

	// Schedule automation (fourth role: studio-scheduler).
	Schedules        *schedule.Service
	Scheduler        *scheduler.Scheduler
	DeliveryDispatch *delivery.Dispatcher
	DeliverySender   delivery.Sender
	DeliveryLimiter  *execution.RateLimiter
	// ProviderSlots is the durable (MySQL) provider concurrency semaphore:
	// ownership-scoped slots, DB-clock expiry, Redis independent.
	ProviderSlots *execution.ProviderSlots

	// SSEHub is the process-local SSE fan-out registry (Batch 4 — SSE Hub).
	// It owns ONE Redis pub/sub subscription per run being streamed by this
	// instance, no matter how many HTTP connections are watching.
	//
	// It runs on its OWN runtime context (see sse.NewHubManager) and must be
	// closed BEFORE a.Redis — see App.Close.
	SSEHub *sse.HubManager
}

// Build constructs the graph; ctx bounds connection setup.
func Build(ctx context.Context, cfg *config.Config) (*App, error) {
	log := slog.Default()

	metrics := telemetry.NewMetrics("studio")

	dbh, err := database.Open(ctx, cfg.Database)
	if err != nil {
		return nil, err
	}
	rdb, err := redisx.Open(ctx, cfg.Redis)
	if err != nil {
		return nil, err
	}

	// Storage driver.
	st, err := storage.New(storage.StorageDriverConfig{
		Driver:    cfg.Storage.Driver,
		LocalRoot: cfg.Storage.LocalRoot,
		S3: storage.S3Config{
			Endpoint:  cfg.Storage.S3Endpoint,
			Region:    cfg.Storage.S3Region,
			Bucket:    cfg.Storage.S3Bucket,
			AccessKey: cfg.Storage.S3AccessKey,
			SecretKey: cfg.Storage.S3SecretKey,
			UseSSL:    cfg.Storage.S3UseSSL,
		},
	})
	if err != nil {
		return nil, err
	}

	gcm, err := crypto.NewAESGCM(cfg.Auth.TokenEncryptionKey)
	if err != nil {
		return nil, err
	}

	feishu := identity.NewFeishuClient(cfg.Feishu.BaseURL, cfg.Feishu.AppID, cfg.Feishu.AppSecret,
		httpclient.New(httpclient.Default(), 20*time.Second))

	identityRepo := identity.NewRepo(dbh)
	sessions := identity.NewSessionStore(rdb, cfg.Session.TTL, cfg.Session.CookieName, cfg.Auth.CSRFCookieName)
	stateCodec := identity.NewStateCodec(cfg.Feishu.AppSecret + ":state")
	oauth := &identity.ExchangeOrchestrator{
		Repo:        identityRepo,
		DB:          dbh,
		Feishu:      feishu,
		GCM:         gcm,
		RedirectURI: cfg.Feishu.RedirectURI,
	}

	registry := catalog.NewRuntimeRegistry()
	catalogRepo := &catalog.Repo{DB: dbh}
	catalogSvc := &catalog.Service{
		DB:       dbh,
		Registry: registry,
		Storage:  st,
	}

	runs := execution.NewService(dbh, rdb, log, metrics)
	// Retry timing has ONE source of truth: the run's available_at and the
	// dispatch outbox row share now+RequeueDelay (P1-2).
	runs.RequeueDelay = cfg.Runner.RequeueDelay

	// Aily provider wiring: adapter + executor + limiters.
	ailyAuth := &aily.AuthResolver{
		DB:     dbh,
		Redis:  rdb,
		Feishu: bridge.NewTokenAPI(feishu),
		GCM:    gcm,
		AppID:  cfg.Feishu.AppID,
		OnRotate: func(ctx context.Context, identityID int64, enc string, expires *time.Time) error {
			return oauth.RotateRefresh(ctx, identityID, enc, expires)
		},
	}
	ailyClient := aily.NewClient(cfg.Aily.BaseURL, httpclient.New(httpclient.Default(), cfg.Aily.RequestTimeout))
	ailyAdapter := aily.NewAgentAdapter(ailyClient, ailyAuth)
	registry.Register(ailyAdapter)

	ailyExecutor := &aily.Executor{
		// Owned: the ONLY write surface the executor holds — every
		// canonical write is ownership-fenced (Execution Correctness
		// Closure; there is no unfenced path reachable from the executor).
		Owned:   runs.WorkerOwned(),
		Adapter: ailyAdapter,
		Auth:    ailyAuth,
		// Pre-submit kill switch (第三轮 P1-B, Gate 2): same revocable-state
		// gate as the worker's claim-time check (Gate 1), re-run inside the
		// handler right before BeginProviderAttempt.
		Gate: NewExecutionGate(catalogSvc),
		ChatsL: execution.NewRateLimiter(rdb, rdb.Key("rate", "aily", "chats"),
			cfg.Aily.StartRateLimitPerSec, time.Second),
		PollsL:      execution.NewRateLimiter(rdb, rdb.Key("rate", "aily", "polls"), 10, time.Second),
		ArtifactsL:  execution.NewRateLimiter(rdb, rdb.Key("rate", "aily", "artifacts"), 50, time.Second),
		PollBackoff: cfg.Aily.PollBackoff,
		Log:         log,
		Metrics:     metrics,
	}

	// Schedule automation wiring: scheduler domain + delivery fan-out.
	// The Aily auth resolver doubles as the delivery UAT source — both
	// must run under the schedule owner's identity.
	disp := delivery.NewDispatcher(dbh, log, metrics)
	runs.CreateDeliveryExecutionsTx = disp.CreateInTx
	feishuSender := &delivery.FeishuSender{Client: feishu, Auth: ailyAuth}
	deliveryLimiter := execution.NewRateLimiter(rdb, rdb.Key("rate", "feishu", "im"), 20, time.Second)

	schedSvc := schedule.NewService(dbh, &schedulableChecker{Catalog: catalogSvc, Users: identityRepo}, log)
	schedSvc.MaxSchedules = cfg.Runner.UserMaxSchedules
	schedJob := scheduler.New(dbh, runs, &bindingResolver{Catalog: catalogSvc, Users: identityRepo}, log, metrics)
	// Run-now pending cap (复审 P1-3): bounded manual queue per schedule.
	schedJob.MaxPendingManual = cfg.Runner.UserMaxPendingManual

	// Provider concurrency cap: the provider catalog row is the source of
	// truth; AILY_MAX_INFLIGHT is only a bootstrap/default (and a
	// deliberate override when the catalog row is absent or zero).
	maxInflight := cfg.Aily.MaxInflight
	if p, err := catalogRepo.ProviderByKey(ctx, "feishu_aily"); err == nil && p != nil && p.MaxInflight > 0 {
		if p.MaxInflight != maxInflight {
			log.Info("provider max_inflight from catalog policy",
				"provider", p.Key, "policy", p.MaxInflight, "env_default", maxInflight)
		}
		maxInflight = p.MaxInflight
	} else {
		log.Info("provider max_inflight from env bootstrap",
			"provider", "feishu_aily", "value", maxInflight)
	}

	return &App{
		Cfg: cfg, Log: log, DB: dbh, Redis: rdb, Metrics: metrics, Storage: st,
		IdentityRepo: identityRepo, Sessions: sessions, StateCodec: stateCodec,
		Oauth: oauth, Feishu: feishu,
		Catalog: catalogSvc, CatalogRepo: catalogRepo, Registry: registry,
		Runs: runs, ArtifactsRL: ailyExecutor.ArtifactsL, AilyExecutor: ailyExecutor,
		RunAdmissionRL: execution.NewRateLimiter(rdb, rdb.Key("rate", "runs", "user"),
			cfg.Runner.UserRunQPS, time.Second),
		Schedules: schedSvc, Scheduler: schedJob,
		DeliveryDispatch: disp, DeliverySender: feishuSender, DeliveryLimiter: deliveryLimiter,
		ProviderSlots: execution.NewProviderSlots(dbh, "feishu_aily", maxInflight, cfg.Runner.LeaseSeconds),
		// SSE Hub (Batch 4). `ctx` here is the STARTUP context, and it is
		// passed only so the wiring records what must NOT become the hub's
		// runtime parent: sse.NewHubManager derives its own context from
		// context.Background() and lives until App.Close. Deriving it from
		// this context would cancel every live stream 30s after process
		// start.
		SSEHub: sse.NewHubManager(ctx, rdb, metrics, sse.HubOptions{
			CacheMaxEvents:      cfg.SSE.HubCacheEvents,
			CacheMaxBytes:       cfg.SSE.HubCacheBytes,
			SubscriberMaxEvents: cfg.SSE.SubscriberEvents,
			SubscriberMaxBytes:  cfg.SSE.SubscriberBytes,
			IdleTTL:             cfg.SSE.HubIdleTTL,
		}),
	}, nil
}

// schedulableChecker adapts the catalog to the schedule ApplicationChecker.
type schedulableChecker struct {
	Catalog *catalog.Service
	Users   *identity.Repo
}

// SchedulableApplication enforces the SAME execution gate as CreateRun
// (评测 P0-1): the schedule owner must be allowed to execute the
// application — public + enabled for regular owners, anything for staff.
// The ownerUserID parameter is authoritative (schedule.Service passes the
// real owner on create; update passes the caller).
//
// Error contract (复审 P1-1, 第三轮 P0-A): only policy refusals are
// reported as schedule.ErrApplicationNotSchedulable (→ 400);
// infrastructure errors propagate raw so a DB outage cannot masquerade as
// invalid user input. The SUCCESS path is checked explicitly first —
// even if executionDenied() were ever regressed, an authorized
// application can never be classified as a refusal again.
func (c *schedulableChecker) SchedulableApplication(ctx context.Context, appID, ownerUserID int64) error {
	_, err := authorizeForOwner(ctx, c.Catalog, c.Users, appID, ownerUserID)
	if err == nil {
		return nil // authorized — never re-classify success
	}
	if executionDenied(err) {
		return schedule.ErrApplicationNotSchedulable
	}
	return err
}

// bindingResolver adapts the catalog to the scheduler RuntimeResolver.
// Every due-slot / admission fire re-validates the schedule owner's right
// to execute the application (评测 P0-1): a private or disabled
// application stops producing runs even for schedules that already
// exist. Staff owners keep their staff rights at fire time.
type bindingResolver struct {
	Catalog *catalog.Service
	Users   *identity.Repo
}

// EnabledBinding resolves without an owner (legacy signature used by
// transports that have no user context): the strictest regular-user
// rules apply.
func (r *bindingResolver) EnabledBinding(ctx context.Context, appID int64) (*scheduler.BindingView, error) {
	exe, err := authorizeForOwner(ctx, r.Catalog, r.Users, appID, 0)
	if err == nil {
		return bindingViewOf(exe), nil // authorized — never re-classify success
	}
	if executionDenied(err) {
		return nil, nil // not executable → scheduler treats as not schedulable
	}
	return nil, err // infrastructure failure: never disguised as a denial
}

// EnabledBindingFor resolves the binding under the schedule owner's
// identity (used by the scheduler's per-fire authorization).
func (r *bindingResolver) EnabledBindingFor(ctx context.Context, appID, ownerUserID int64) (*scheduler.BindingView, error) {
	exe, err := authorizeForOwner(ctx, r.Catalog, r.Users, appID, ownerUserID)
	if err == nil {
		return bindingViewOf(exe), nil // authorized — never re-classify success
	}
	if executionDenied(err) {
		return nil, nil // not executable → scheduler treats as not schedulable
	}
	return nil, err // infrastructure failure: never disguised as a denial
}

// NewBindingResolver exposes the real catalog-backed resolver for wiring
// and tests (第三轮 P0-A: happy-path E2E must run the REAL
// resolver → authorizeForOwner → executionDenied chain, not a stub).
func NewBindingResolver(svc *catalog.Service, users *identity.Repo) *bindingResolver {
	return &bindingResolver{Catalog: svc, Users: users}
}

// NewSchedulableChecker exposes the real schedule application checker for
// wiring and tests (same rationale as NewBindingResolver).
func NewSchedulableChecker(svc *catalog.Service, users *identity.Repo) *schedulableChecker {
	return &schedulableChecker{Catalog: svc, Users: users}
}

// executionDenied reports whether err is a POLICY rejection — the
// application/binding/provider state itself refuses execution — as
// opposed to an infrastructure failure (MySQL down, connection pool
// exhausted, context deadline). The scheduler treats only the former as
// "not schedulable" (failed occurrence, slot advances); the latter must
// abort the current tick so the SAME slot is retried on the next scan
// (复审 P1-1). Swallowing a transient DB error used to permanently fail
// the occurrence and lose the scheduled slot.
//
// 第三轮 P0-A: nil is SUCCESS, never a denial. The previous
// `err == nil ||` shortcut inverted the classifier — every successful
// authorization was treated as a policy refusal, which blocked schedule
// create/update and silently failed every automatic fire and run-now.
// Callers additionally guard the success path with an explicit
// `err == nil` check so this helper can never break authorized flow
// on its own again.
func executionDenied(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, catalog.ErrExecutionForbidden) ||
		errors.Is(err, catalog.ErrExecutionNotFound) ||
		errors.Is(err, catalog.ErrExecutionDisabled) ||
		errors.Is(err, catalog.ErrExecutionNotChat) ||
		errors.Is(err, catalog.ErrNoBinding) ||
		errors.Is(err, catalog.ErrExecutionProviderInactive) ||
		errors.Is(err, catalog.ErrExecutionProviderMissing)
}

// authorizeForOwner runs the unified execution gate for one owner,
// resolving the owner's staff flag through the identity repo.
//
// Error classification (复审 P1-1): an unknown owner is a policy fact
// (strictest non-staff rules apply), but a FAILED identity lookup is an
// infrastructure error that must propagate — silently demoting a staff
// owner to non-staff used to turn a DB outage into a fake "private app"
// rejection, which the scheduler then treated as a permanent denial.
func authorizeForOwner(ctx context.Context, svc *catalog.Service, users *identity.Repo, appID, ownerUserID int64) (*catalog.Executable, error) {
	isStaff := false
	if ownerUserID > 0 && users != nil {
		u, _, err := users.UserWithIdentity(ctx, ownerUserID)
		switch {
		case errors.Is(err, identity.ErrNotFound):
			// Owner gone → strictest non-staff rules (policy, not failure).
		case err != nil:
			return nil, err
		case u != nil:
			isStaff = u.IsStaff
		}
	}
	return svc.AuthorizeExecution(ctx, appID, ownerUserID, isStaff)
}

func bindingViewOf(exe *catalog.Executable) *scheduler.BindingView {
	b := exe.Binding
	return &scheduler.BindingView{
		ID:            b.ID,
		ProviderKey:   b.ProviderKey,
		RuntimeType:   b.RuntimeType,
		ExecutionMode: b.ExecutionMode,
		Snapshot:      b.Snapshot(),
	}
}

// ExecutionGate adapts the catalog to the worker's execution-time kill
// switch (复审 P1-2). It reads ONLY the mutable revocable state — one
// indexed join — and never touches the frozen runtime snapshot.
//
// Classification (product decision 2026-09-15):
//
//	application missing/disabled          → GateKill (cancel)
//	binding missing/disabled              → GateKill (cancel)
//	provider missing/not active           → GatePause (requeue, keep waiting)
//	gate query itself fails               → raw error (worker requeues as
//	                                       an infrastructure outage)
type ExecutionGate struct {
	Catalog *catalog.Service
}

func NewExecutionGate(svc *catalog.Service) *ExecutionGate { return &ExecutionGate{Catalog: svc} }

func (g *ExecutionGate) CheckRun(ctx context.Context, run *execution.Run) (execution.GateAction, error) {
	st, err := g.Catalog.RunGateState(ctx, run.ID.Bytes())
	if err != nil {
		return "", err
	}
	if !st.AppEnabled.Valid || !st.AppEnabled.Bool {
		return execution.GateKill, nil
	}
	if !st.BindingEnabled.Valid || !st.BindingEnabled.Bool {
		return execution.GateKill, nil
	}
	if !st.ProviderStatus.Valid || st.ProviderStatus.String != "active" {
		return execution.GatePause, nil
	}
	return execution.GateAllow, nil
}

// Close releases shared resources.
//
// The order is load-bearing (Batch 4 AC-12). The SSE Hub owns Redis pub/sub
// subscriptions, so it is closed FIRST: closing the Redis client underneath
// live hubs would tear their upstreams down in a way that reports a Redis
// outage that never happened, and would leave subscriber goroutines waiting on
// a pool that is gone.
func (a *App) Close() {
	if a.SSEHub != nil {
		a.SSEHub.Close()
	}
	_ = a.Redis.Close()
	_ = a.DB.Close()
}

// HTTPClient exposes a tuned client for handlers that need outbound HTTP.
func HTTPClient(timeout time.Duration) *http.Client {
	return httpclient.New(httpclient.Default(), timeout)
}
