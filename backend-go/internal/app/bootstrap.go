package app

import (
	"github.com/creation-agent-studio/backend-go/internal/platform/config"
	transporthttp "github.com/creation-agent-studio/backend-go/internal/transport/http"
	"github.com/creation-agent-studio/backend-go/internal/transport/sse"
)

// LoadConfig loads configuration from the environment / .env files.
func LoadConfig() (*config.Config, error) { return config.Load() }

// NewServer bridges the assembled App into the HTTP transport Server.
func NewServer(cfg *config.Config, a *App) *transporthttp.Server {
	return &transporthttp.Server{
		Config: cfg,
		DB:     a.DB,
		Log:    a.Log,
		Metric: a.Metrics,
		Redis:  a.Redis,

		IdentityRepo: a.IdentityRepo,
		Store:        a.Sessions,
		StateCodec:   a.StateCodec,
		Oauth:        a.Oauth,
		Feishu:       a.Feishu,
		FeishuAuth:   a.AilyExecutor.Auth,

		Catalog:     a.Catalog,
		CatalogRepo: a.CatalogRepo,
		Registry:    a.Registry,

		Runs:               a.Runs,
		Storage:            a.Storage,
		RateLimitArtifacts: a.ArtifactsRL,
		RunAdmission:       a.RunAdmissionRL,

		Schedules: a.Schedules,
		Scheduler: a.Scheduler,

		SSE: &sse.Gateway{Runs: a.Runs, Redis: a.Redis},
	}
}
