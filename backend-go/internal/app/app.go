// Package app assembles the full dependency graph from config so the
// three binaries (api / stream / worker) share identical wiring.
package app

import (
	"context"
	"database/sql"
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

	// Schedule automation (fourth role: studio-scheduler).
	Schedules        *schedule.Service
	Scheduler        *scheduler.Scheduler
	DeliveryDispatch *delivery.Dispatcher
	DeliverySender   delivery.Sender
	DeliveryLimiter  *execution.RateLimiter
	// ProviderSlots is the durable (MySQL) provider concurrency semaphore:
	// ownership-scoped slots, DB-clock expiry, Redis independent.
	ProviderSlots *execution.ProviderSlots
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
		Schedules: schedSvc, Scheduler: schedJob,
		DeliveryDispatch: disp, DeliverySender: feishuSender, DeliveryLimiter: deliveryLimiter,
		ProviderSlots: execution.NewProviderSlots(dbh, "feishu_aily", maxInflight, cfg.Runner.LeaseSeconds),
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
func (c *schedulableChecker) SchedulableApplication(ctx context.Context, appID, ownerUserID int64) error {
	_, err := authorizeForOwner(ctx, c.Catalog, c.Users, appID, ownerUserID)
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
	if err != nil {
		return nil, nil // not executable → scheduler treats as not schedulable
	}
	return bindingViewOf(exe), nil
}

// EnabledBindingFor resolves the binding under the schedule owner's
// identity (used by the scheduler's per-fire authorization).
func (r *bindingResolver) EnabledBindingFor(ctx context.Context, appID, ownerUserID int64) (*scheduler.BindingView, error) {
	exe, err := authorizeForOwner(ctx, r.Catalog, r.Users, appID, ownerUserID)
	if err != nil {
		return nil, nil
	}
	return bindingViewOf(exe), nil
}

// authorizeForOwner runs the unified execution gate for one owner,
// resolving the owner's staff flag through the identity repo (unknown
// owner → strictest non-staff rules).
func authorizeForOwner(ctx context.Context, svc *catalog.Service, users *identity.Repo, appID, ownerUserID int64) (*catalog.Executable, error) {
	isStaff := false
	if ownerUserID > 0 && users != nil {
		if u, _, err := users.UserWithIdentity(ctx, ownerUserID); err == nil && u != nil {
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

// Close releases shared resources.
func (a *App) Close() {
	_ = a.Redis.Close()
	_ = a.DB.Close()
}

// HTTPClient exposes a tuned client for handlers that need outbound HTTP.
func HTTPClient(timeout time.Duration) *http.Client {
	return httpclient.New(httpclient.Default(), timeout)
}
