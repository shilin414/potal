package integration

import (
	"context"
	"os"
	"testing"
	"time"

	studioapp "github.com/creation-agent-studio/backend-go/internal/app"
	"github.com/creation-agent-studio/backend-go/internal/platform/config"
)

func TestAppBuildWiresEnterpriseACLFlagToRepoAndService(t *testing.T) {
	if os.Getenv("STUDIO_TEST_DB") != "1" || os.Getenv("STUDIO_TEST_REDIS") != "1" {
		t.Skip("set STUDIO_TEST_DB=1 and STUDIO_TEST_REDIS=1")
	}
	t.Setenv("ENTERPRISE_ACL_ENABLED", "true")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	a, err := studioapp.Build(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if !a.CatalogRepo.ACLEnabled || !a.Catalog.ACLEnabled {
		t.Fatalf("repo=%v service=%v", a.CatalogRepo.ACLEnabled, a.Catalog.ACLEnabled)
	}
}
