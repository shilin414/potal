package app

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/catalog"
)

// TestBuildRegistersFeishuAilyAgentDispatchPlan pins the Batch 5 worker
// dispatcher wiring end to end (Batch 5 §36): Build must have registered
// the feishu_aily provider plan with exactly the agent route, the slots
// must belong to feishu_aily, and the Aily WORKFLOW route must NOT exist
// yet — a test that "finds" a workflow route would mean Batch 5 silently
// grew beyond its scope (the workflow executor is Batch 6 work).
//
// Needs the real database and Redis: the registry is wired inside
// App.Build. Opt in with STUDIO_TEST_DB=1 and STUDIO_TEST_REDIS=1.
func TestBuildRegistersFeishuAilyAgentDispatchPlan(t *testing.T) {
	if os.Getenv("STUDIO_TEST_DB") != "1" || os.Getenv("STUDIO_TEST_REDIS") != "1" {
		t.Skip("set STUDIO_TEST_DB=1 and STUDIO_TEST_REDIS=1 to run the dispatch wiring integration test")
	}

	// The CI integration job provides ONLY database/Redis env vars — the
	// first 5.1 CI run died in Build on crypto.NewAESGCM("") because no
	// TOKEN_ENCRYPTION_KEY existed there (no .env.local in CI). Supply a
	// throwaway key: this test proves WIRING, not key material. APP_ENV is
	// pinned to development so a future CI-side production setting cannot
	// drag validateProduction (FEISHU_APP_SECRET / DB_PASSWORD checks) into
	// a wiring test.
	t.Setenv("APP_ENV", "development")
	t.Setenv("TOKEN_ENCRYPTION_KEY", "batch5-1-app-wiring-integration-key")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	a, err := Build(ctx, cfg)
	if err != nil {
		t.Fatalf("build app: %v", err)
	}
	defer a.Close()

	if a.WorkerDispatch == nil {
		t.Fatal("App.Build produced no worker dispatch registry (Batch 5 wiring missing)")
	}

	plan, err := a.WorkerDispatch.ResolveProvider("feishu_aily")
	if err != nil {
		t.Fatalf("resolve feishu_aily plan: %v", err)
	}
	if plan.Provider != "feishu_aily" {
		t.Fatalf("plan provider = %q, want feishu_aily", plan.Provider)
	}
	if plan.Handler == nil {
		t.Fatal("plan has no dispatch handler")
	}
	if plan.Slots == nil || plan.Slots.Provider != "feishu_aily" {
		t.Fatalf("plan slots = %+v, want slots owned by feishu_aily", plan.Slots)
	}

	// Batch 5 registers exactly ONE route: agent. Aily Workflow is
	// deliberately NOT implemented in this batch.
	if len(plan.RuntimeTypes) != 1 || plan.RuntimeTypes[0] != catalog.RuntimeTypeAgent {
		t.Fatalf("runtime types = %v, want exactly [agent] — Batch 5 must not pretend the Aily workflow route exists",
			plan.RuntimeTypes)
	}

	// The compatibility alias and the plan must share the ONE provider-wide
	// semaphore: a second slots object would silently double the real limit.
	if a.ProviderSlots == nil || a.ProviderSlots.Provider != "feishu_aily" {
		t.Fatalf("compat alias ProviderSlots = %+v, want the feishu_aily slots", a.ProviderSlots)
	}
	if plan.Slots != a.ProviderSlots {
		t.Fatal("the plan and the App.ProviderSlots alias hold DIFFERENT slots objects — " +
			"per-consumer capacity would no longer be provider-wide")
	}

	// An unknown provider must not resolve: a worker started with a typo
	// would otherwise silently inherit whatever happens to be registered.
	if _, err := a.WorkerDispatch.ResolveProvider("definitely_not_a_provider"); err == nil {
		t.Fatal("resolving an unregistered provider must fail")
	}
}
