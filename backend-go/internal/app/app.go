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

	"github.com/creation-agent-studio/backend-go/internal/catalog"
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
		Svc:     runs,
		Adapter: ailyAdapter,
		Auth:    ailyAuth,
		ChatsL: execution.NewRateLimiter(rdb, rdb.Key("rate", "aily", "chats"),
			cfg.Aily.StartRateLimitPerSec, time.Second),
		PollsL:      execution.NewRateLimiter(rdb, rdb.Key("rate", "aily", "polls"), 10, time.Second),
		ArtifactsL:  execution.NewRateLimiter(rdb, rdb.Key("rate", "aily", "artifacts"), 50, time.Second),
		PollBackoff: cfg.Aily.PollBackoff,
		Log:         log,
		Metrics:     metrics,
		DB:          dbh,
	}

	return &App{
		Cfg: cfg, Log: log, DB: dbh, Redis: rdb, Metrics: metrics, Storage: st,
		IdentityRepo: identityRepo, Sessions: sessions, StateCodec: stateCodec,
		Oauth: oauth, Feishu: feishu,
		Catalog: catalogSvc, CatalogRepo: catalogRepo, Registry: registry,
		Runs: runs, ArtifactsRL: ailyExecutor.ArtifactsL, AilyExecutor: ailyExecutor,
	}, nil
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
