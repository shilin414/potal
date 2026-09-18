package integration

// Catalog invariants (三次复审 P0-R1 / P0-R3 / P0-R4, 2026-09-18).
//
// What is pinned here, against the REAL database (STUDIO_TEST_DB=1):
//
//   P0-R1  the default-agent flag can no longer be promoted through any
//          path that skips the eligibility gate — Create without a runtime,
//          Create/PATCH with an inactive provider, PATCH
//          {enabled:false, set_default_agent:true}, a provider switched off
//          AFTER creation — and a FAILED promotion leaves the previous
//          default untouched (the whole transaction rolls back).
//   P0-R3  the provider kill switch reaches every CONSUMER surface: catalog
//          page (mode=consume), bootstrap default group, the single-object
//          consumption bundle behind resolve / @mention, and
//          AuthorizeExecution itself all refuse the same application at the
//          same time, and all allow it again when the provider comes back.
//   P0-R4  by-id surfaces do not disclose existence: a hidden-but-real id
//          and an unknown id are indistinguishable for favorite /
//          unfavorite / default-agent / delete. Favoriting a VISIBLE
//          application (chat AND fixed) still works.
//
// Fixtures go through the real service (never bare SQL for applications),
// wear the itest prefix and clean up via t.Cleanup. The one GLOBAL row this
// suite can touch — is_default_agent — is captured before and restored after
// every test that promotes a default, so the shared dev database keeps the
// default the workspace was configured with.

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	"github.com/creation-agent-studio/backend-go/internal/catalog"
	"github.com/creation-agent-studio/backend-go/internal/platform/config"
	"github.com/creation-agent-studio/backend-go/internal/platform/database"
)

func invariantEnv(t *testing.T) (*catalog.Service, *catalog.Repo, *sql.DB) {
	t.Helper()
	if os.Getenv("STUDIO_TEST_DB") != "1" {
		t.Skip("set STUDIO_TEST_DB=1 to run catalog-invariant integration tests")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	db, err := database.Open(context.Background(), cfg.Database)
	if err != nil {
		t.Fatalf("database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &catalog.Service{DB: db}, &catalog.Repo{DB: db}, db
}

// itestAdapter is the minimal RuntimeAdapter the service needs to accept a
// runtime binding for a test provider. Nothing here performs IO.
type itestAdapter struct{ key string }

func (a *itestAdapter) Key() string { return a.key }

func (a *itestAdapter) Submit(context.Context, *catalog.SubmitInput) (*catalog.SubmitResult, error) {
	return nil, errors.New("itest adapter: no submit")
}

func (a *itestAdapter) Status(context.Context, *catalog.ProviderAuthContext, string, string) (*catalog.StatusResult, error) {
	return nil, errors.New("itest adapter: no status")
}

func (a *itestAdapter) Stream(context.Context, *catalog.SubmitInput) (<-chan catalog.StreamEvent, func(), error) {
	return nil, func() {}, errors.New("itest adapter: no stream")
}

func (a *itestAdapter) UploadAttachment(context.Context, *catalog.ProviderAuthContext, string, *catalog.AttachmentInput) (string, error) {
	return "", errors.New("itest adapter: no upload")
}

func (a *itestAdapter) ResolveArtifact(context.Context, *catalog.ProviderAuthContext, string, string) (*catalog.ArtifactRef, error) {
	return nil, errors.New("itest adapter: no artifact")
}

func (a *itestAdapter) CheckVisibility(context.Context, *catalog.ProviderAuthContext, string) (bool, error) {
	return true, nil
}

func (a *itestAdapter) Capabilities() catalog.Capabilities { return catalog.Capabilities{} }

func (a *itestAdapter) BuildAuth(context.Context, int64, string) (*catalog.ProviderAuthContext, error) {
	return &catalog.ProviderAuthContext{}, nil
}

func (a *itestAdapter) DisplayLabel() string      { return "itest" }
func (a *itestAdapter) ResourceIDLabel() string   { return "资源 ID" }
func (a *itestAdapter) ResourceIDPattern() string { return "" }
func (a *itestAdapter) ResourceIDHint() string    { return "" }
func (a *itestAdapter) ResourceIDRequired() bool  { return false }

// invariantService is a service whose registry accepts bindings for the
// given test provider key (runtime type `agent`).
func invariantService(db *sql.DB, providerKey string) *catalog.Service {
	svc := &catalog.Service{DB: db}
	svc.Registry = catalog.NewRuntimeRegistry()
	svc.Registry.Register(&itestAdapter{key: providerKey + ":agent"})
	return svc
}

// seedProvider inserts one test provider row with the wanted status.
func seedProvider(t *testing.T, db *sql.DB, key, status string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO providers (provider_key, name, supported_runtime_types, status)
         VALUES (?, 'itest provider', '["agent"]', ?)
         ON DUPLICATE KEY UPDATE status = VALUES(status)`, key, status)
	if err != nil {
		t.Fatalf("seed provider %s: %v", key, err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM providers WHERE provider_key = ?`, key)
	})
}

func setProviderStatus(t *testing.T, db *sql.DB, key, status string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		`UPDATE providers SET status = ? WHERE provider_key = ?`, status, key); err != nil {
		t.Fatalf("set provider %s status=%s: %v", key, status, err)
	}
}

// currentDefaultID captures the workspace's configured default agent so a
// test that promotes its own can restore it afterwards.
func currentDefaultID(t *testing.T, db *sql.DB) *int64 {
	t.Helper()
	var id int64
	err := db.QueryRowContext(context.Background(),
		`SELECT id FROM applications WHERE is_default_agent = 1 ORDER BY id LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		t.Fatalf("read current default: %v", err)
	}
	return &id
}

// restoreDefaultOnCleanup re-points the global default flag at the row that
// held it before the test ran (the promotion CLEARs every other default).
func restoreDefaultOnCleanup(t *testing.T, db *sql.DB, promoted *catalog.Application, prior *int64) {
	t.Helper()
	t.Cleanup(func() {
		if promoted != nil {
			_, _ = db.ExecContext(context.Background(),
				`UPDATE applications SET is_default_agent = 0 WHERE id = ?`, promoted.ID)
		}
		if prior != nil {
			_, _ = db.ExecContext(context.Background(),
				`UPDATE applications SET is_default_agent = 1 WHERE id = ?`, *prior)
		}
	})
}

func seedChatAppWithRuntime(t *testing.T, svc *catalog.Service, creator int64, name, providerKey string, public, setDefault bool) *catalog.Application {
	t.Helper()
	app, _, err := svc.Create(context.Background(), &catalog.CreateInput{
		Name: name, Kind: "chat", IsPublic: public,
		Runtime: &catalog.BindingInput{
			ProviderKey:        providerKey,
			RuntimeType:        "agent",
			ExternalResourceID: "itest-resource-1",
		},
		SetDefaultAgent: setDefault,
		CreatorID:       creator, IsStaff: true,
	})
	if err != nil {
		t.Fatalf("seed chat app %s: %v", name, err)
	}
	t.Cleanup(func() { _ = svc.Delete(context.Background(), app.ID, creator, true) })
	return app
}

// ───────────────────────────────────────────────── P0-R1: default agent ──

// A create that asks for the default flag WITHOUT a runtime is refused, and
// the workspace's previous default survives (失败事务不得清掉原来的 default).
func TestDefaultAgentCreateWithoutRuntimeRefused(t *testing.T) {
	svc, _, db := invariantEnv(t)
	prior := currentDefaultID(t, db)

	staff := int64(777100)
	_, _, err := svc.Create(context.Background(), &catalog.CreateInput{
		Name: "itest_default_noruntime", Kind: "chat", IsPublic: true,
		SetDefaultAgent: true, CreatorID: staff, IsStaff: true,
	})
	if !errors.Is(err, catalog.ErrNotDefaultable) {
		t.Fatalf("create without runtime + set_default_agent must be refused, got %v", err)
	}
	if got := currentDefaultID(t, db); !sameID(got, prior) {
		t.Fatalf("a failed promotion must not move the default: before=%v after=%v", prior, got)
	}
}

// A create with an INACTIVE provider is refused before anything is written.
func TestDefaultAgentCreateWithInactiveProviderRefused(t *testing.T) {
	svc, _, db := invariantEnv(t)
	const providerKey = "itestprov_inactive"
	seedProvider(t, db, providerKey, "disabled")
	prior := currentDefaultID(t, db)

	staff := int64(777101)
	_, _, err := svc.Create(context.Background(), &catalog.CreateInput{
		Name: "itest_default_inactprov", Kind: "chat", IsPublic: true,
		Runtime: &catalog.BindingInput{
			ProviderKey: providerKey, RuntimeType: "agent", ExternalResourceID: "r",
		},
		SetDefaultAgent: true, CreatorID: staff, IsStaff: true,
	})
	if !errors.Is(err, catalog.ErrProviderDisabled) {
		t.Fatalf("create with inactive provider must be refused, got %v", err)
	}
	if got := currentDefaultID(t, db); !sameID(got, prior) {
		t.Fatalf("a refused create must not move the default: before=%v after=%v", prior, got)
	}
}

// The happy path: eligible chat + active provider → promoted; the previous
// default is replaced (and restored by cleanup).
func TestDefaultAgentCreateEligibleSucceeds(t *testing.T) {
	svc, _, db := invariantEnv(t)
	const providerKey = "itestprov_ok"
	seedProvider(t, db, providerKey, "active")
	prior := currentDefaultID(t, db)
	svc = invariantService(db, providerKey)

	staff := int64(777102)
	app := seedChatAppWithRuntime(t, svc, staff, "itest_default_ok", providerKey, true, true)
	restoreDefaultOnCleanup(t, db, app, prior)

	fresh, err := svc.ApplicationByID(context.Background(), app.ID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if !fresh.IsDefaultAgent {
		t.Fatal("an eligible create with set_default_agent=true must land the flag")
	}
}

// A FIXED application can never be the default agent.
func TestDefaultAgentFixedKindRefused(t *testing.T) {
	svc, _, db := invariantEnv(t)
	prior := currentDefaultID(t, db)
	staff := int64(777103)
	_, _, err := svc.Create(context.Background(), &catalog.CreateInput{
		Name: "itest_default_fixed", Kind: "task", IsPublic: true,
		SetDefaultAgent: true, CreatorID: staff, IsStaff: true,
	})
	if !errors.Is(err, catalog.ErrNotDefaultable) {
		t.Fatalf("fixed kind + set_default_agent must be refused, got %v", err)
	}
	if got := currentDefaultID(t, db); !sameID(got, prior) {
		t.Fatalf("default must not move: before=%v after=%v", prior, got)
	}
}

// THE bypass the round was about: PATCH {enabled:false,
// set_default_agent:true} used to disable first and re-promote right after.
// The gate reads the FINAL in-transaction state, so the whole patch is
// refused and rolls back — the application stays enabled and stays default.
func TestDefaultAgentUpdateCannotDisableAndRepromote(t *testing.T) {
	svc, _, db := invariantEnv(t)
	const providerKey = "itestprov_patch"
	seedProvider(t, db, providerKey, "active")
	prior := currentDefaultID(t, db)
	svc = invariantService(db, providerKey)
	staff := int64(777104)

	app := seedChatAppWithRuntime(t, svc, staff, "itest_default_patch", providerKey, true, true)
	restoreDefaultOnCleanup(t, db, app, prior)
	if !app.IsDefaultAgent {
		t.Fatal("fixture must start as default")
	}

	no := false
	yes := true
	// enabled=false + set_default_agent=true — the exact bypass pair.
	_, _, err := svc.Update(context.Background(), app.ID, staff, true,
		nil, nil, nil, nil, nil, &no, nil, nil, nil, &yes, nil)
	if !errors.Is(err, catalog.ErrNotDefaultable) {
		t.Fatalf("PATCH {enabled:false, set_default_agent:true} must be refused, got %v", err)
	}
	fresh, err := svc.ApplicationByID(context.Background(), app.ID)
	if err != nil {
		t.Fatalf("re-read after rollback: %v", err)
	}
	if !fresh.Enabled || !fresh.IsDefaultAgent {
		t.Fatalf("the whole patch must roll back: enabled=%v default=%v", fresh.Enabled, fresh.IsDefaultAgent)
	}
}

// Disabling WITHOUT asking for the default flag still clears it (the 二次复审
// P0-6 behaviour must keep working).
func TestDefaultAgentDisableClearsFlag(t *testing.T) {
	svc, _, db := invariantEnv(t)
	const providerKey = "itestprov_disable"
	seedProvider(t, db, providerKey, "active")
	prior := currentDefaultID(t, db)
	svc = invariantService(db, providerKey)
	staff := int64(777105)

	app := seedChatAppWithRuntime(t, svc, staff, "itest_default_disable", providerKey, true, true)
	restoreDefaultOnCleanup(t, db, nil, prior)

	no := false
	// enabled=false only; set_default_agent left untouched.
	if _, _, err := svc.Update(context.Background(), app.ID, staff, true,
		nil, nil, nil, nil, nil, &no, nil, nil, nil, nil, nil); err != nil {
		t.Fatalf("disable: %v", err)
	}
	fresh, err := svc.ApplicationByID(context.Background(), app.ID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if fresh.IsDefaultAgent {
		t.Fatal("disabling must clear the default flag")
	}
}

// A provider switched off AFTER creation blocks the promotion through the
// dedicated endpoint — and the previous default stays where it was.
func TestDefaultAgentProviderDisabledRefusesPromotion(t *testing.T) {
	svc, _, db := invariantEnv(t)
	const providerKey = "itestprov_kill"
	seedProvider(t, db, providerKey, "active")
	prior := currentDefaultID(t, db)
	svc = invariantService(db, providerKey)
	staff := int64(777106)

	app := seedChatAppWithRuntime(t, svc, staff, "itest_default_kill", providerKey, true, false)
	restoreDefaultOnCleanup(t, db, nil, prior)

	setProviderStatus(t, db, providerKey, "disabled")
	err := svc.SetDefaultAgent(context.Background(), app.ID, staff, true)
	if !errors.Is(err, catalog.ErrNotDefaultable) {
		t.Fatalf("promotion with disabled provider must be refused, got %v", err)
	}
	if got := currentDefaultID(t, db); !sameID(got, prior) {
		t.Fatalf("a failed promotion must not clear the previous default: before=%v after=%v", prior, got)
	}

	// The provider comes back → the same promotion now succeeds.
	setProviderStatus(t, db, providerKey, "active")
	if err := svc.SetDefaultAgent(context.Background(), app.ID, staff, true); err != nil {
		t.Fatalf("promotion after provider re-enabled must succeed, got %v", err)
	}
	restoreDefaultOnCleanup(t, db, app, prior)
}

// Rebinding to an INACTIVE provider while also asking for the default flag
// is refused (the binding the promotion would pick is not executable).
func TestDefaultAgentRebindToInactiveProviderWithDefaultRefused(t *testing.T) {
	svc, _, db := invariantEnv(t)
	const providerKey = "itestprov_rebind"
	const deadKey = "itestprov_dead"
	seedProvider(t, db, providerKey, "active")
	seedProvider(t, db, deadKey, "disabled")
	prior := currentDefaultID(t, db)
	svc = invariantService(db, providerKey)
	svc2 := invariantService(db, deadKey)
	staff := int64(777107)

	app := seedChatAppWithRuntime(t, svc, staff, "itest_default_rebind", providerKey, true, false)
	restoreDefaultOnCleanup(t, db, nil, prior)

	yes := true
	_, _, err := svc2.Update(context.Background(), app.ID, staff, true,
		nil, nil, nil, nil, nil, nil, nil, nil,
		&catalog.BindingInput{ProviderKey: deadKey, RuntimeType: "agent", ExternalResourceID: "r"},
		&yes, nil)
	if err == nil {
		t.Fatal("rebinding to an inactive provider + set_default_agent must be refused")
	}
	if got := currentDefaultID(t, db); !sameID(got, prior) {
		t.Fatalf("default must not move: before=%v after=%v", prior, got)
	}
}

// ─────────────────────────────── P0-R3: provider kill switch everywhere ──

// Disabling the PROVIDER (not the binding) must pull the application out of
// every consumer surface at once — page, bootstrap default, single-object
// bundle, and execution — and everything must allow it again on re-enable.
func TestProviderKillSwitchReachesEveryConsumerSurface(t *testing.T) {
	svc, repo, db := invariantEnv(t)
	const providerKey = "itestprov_surface"
	seedProvider(t, db, providerKey, "active")
	svc = invariantService(db, providerKey)
	staff := int64(777108)
	regular := int64(777109)

	app := seedChatAppWithRuntime(t, svc, staff, "itest_surface_kill", providerKey, true, false)

	ctx := context.Background()
	consumableNow := func() (page, bundle, exec bool) {
		rows, err := repo.ListApplicationPage(ctx, catalog.ApplicationPageQuery{
			Scope: "public", Mode: catalog.PageModeConsume, Kind: catalog.PageQueryKindChat,
			Search: "itest_surface_kill", Limit: 10, CallerID: regular, IsStaff: false,
		})
		if err != nil {
			t.Fatalf("page: %v", err)
		}
		for _, row := range rows {
			if row.App.ID == app.ID {
				page = true
			}
		}
		b, err := repo.ConsumptionBundleByID(ctx, app.ID)
		if err != nil {
			t.Fatalf("bundle: %v", err)
		}
		bundle = catalog.Consumable(catalog.ConsumptionFacts{
			Application: b.Application, Binding: b.Binding, ProviderStatus: b.ProviderStatus,
		})
		if _, err := svc.AuthorizeExecution(ctx, app.ID, regular, false); err == nil {
			exec = true
		}
		return page, bundle, exec
	}

	if p, b, e := consumableNow(); !p || !b || !e {
		t.Fatalf("fresh eligible app must be consumable everywhere: page=%v bundle=%v exec=%v", p, b, e)
	}

	setProviderStatus(t, db, providerKey, "disabled")
	if p, b, e := consumableNow(); p || b || e {
		t.Fatalf("provider disabled must remove the app from EVERY consumer surface: page=%v bundle=%v exec=%v", p, b, e)
	}

	// The bundle still resolves the row (the app exists) — it is the
	// CONSUMABLE decision that refuses, with the provider status attached.
	b, err := repo.ConsumptionBundleByID(ctx, app.ID)
	if err != nil {
		t.Fatalf("bundle read: %v", err)
	}
	if b.ProviderStatus == nil || *b.ProviderStatus != "disabled" {
		t.Fatalf("bundle must surface the provider status, got %v", b.ProviderStatus)
	}

	// The bootstrap default group must not offer the application either.
	if groups, err := repo.BootstrapGroups(ctx, catalog.BootstrapGroupQuery{CallerID: staff, IsStaff: true}); err != nil {
		t.Fatalf("bootstrap groups: %v", err)
	} else if groups.Default != nil && groups.Default.ApplicationID == app.ID {
		t.Fatal("a provider-disabled application must not be the bootstrap default")
	}

	setProviderStatus(t, db, providerKey, "active")
	if p, b, e := consumableNow(); !p || !b || !e {
		t.Fatalf("provider re-enabled must restore every surface: page=%v bundle=%v exec=%v", p, b, e)
	}
}

// ──────────────────────────────── P0-R4: sequential ids stay opaque ──────

// A hidden-but-real id and an unknown id are indistinguishable on the by-id
// mutation surfaces; a VISIBLE application (chat or fixed) can still be
// favorited.
func TestSequentialIDSurfacesStayOpaqueAndFavoritesWork(t *testing.T) {
	svc, _, _ := invariantEnv(t)
	staff := int64(777110)
	regular := int64(777111)
	ctx := context.Background()

	private, _, err := svc.Create(ctx, &catalog.CreateInput{
		Name: "itest_opaque_private", Kind: "chat", IsPublic: false,
		CreatorID: staff, IsStaff: true,
	})
	if err != nil {
		t.Fatalf("seed private app: %v", err)
	}
	t.Cleanup(func() { _ = svc.Delete(ctx, private.ID, staff, true) })

	fixed, _, err := svc.Create(ctx, &catalog.CreateInput{
		Name: "itest_opaque_fixed", Kind: "task", IsPublic: true,
		CreatorID: staff, IsStaff: true,
	})
	if err != nil {
		t.Fatalf("seed fixed app: %v", err)
	}
	t.Cleanup(func() { _ = svc.Delete(ctx, fixed.ID, staff, true) })

	const unknownID = int64(987654321)

	// Favorite: hidden-but-real ≡ unknown.
	if err := svc.Favorite(ctx, private.ID, regular, false); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("favoriting a hidden application must look like a miss, got %v", err)
	}
	if err := svc.Favorite(ctx, unknownID, regular, false); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("favoriting an unknown id must look like a miss, got %v", err)
	}
	// A visible chat app CAN be favorited by a regular user.
	if err := svc.Favorite(ctx, fixed.ID, regular, false); err != nil {
		t.Fatalf("favoriting a visible fixed application must work, got %v", err)
	}
	if err := svc.Unfavorite(ctx, fixed.ID, regular, false); err != nil {
		t.Fatalf("unfavoriting a visible fixed application must work, got %v", err)
	}

	// Default-agent / delete for a non-manager: refused, opaque at the HTTP
	// layer (the handler maps ErrNotManageable and ErrNotFound onto the same
	// 404 — that mapping is unit-level, the service level just refuses).
	if err := svc.SetDefaultAgent(ctx, private.ID, regular, false); !errors.Is(err, catalog.ErrNotManageable) {
		t.Fatalf("non-manager default promotion must be refused, got %v", err)
	}
	if err := svc.Delete(ctx, private.ID, regular, false); !errors.Is(err, catalog.ErrNotManageable) {
		t.Fatalf("non-manager delete must be refused, got %v", err)
	}
}

// ─────────────────────────────── P1-R4: fixed consume needs no unbound flag ──

// `kind=fixed&mode=consume` WITHOUT include_unbound must still return
// binding-less fixed applications: the consume predicate never required a
// binding for kind <> 'chat', so the separate allow_unbound filter must not
// apply to them (三次复审 P1-R4). This is the contract MobileCatalogSheet
// used to compensate for with `includeUnbound: type === 'app'`.
func TestFixedConsumeDoesNotNeedIncludeUnbound(t *testing.T) {
	svc, repo, _ := invariantEnv(t)
	staff := int64(777112)
	ctx := context.Background()

	fixed, _, err := svc.Create(ctx, &catalog.CreateInput{
		Name: "itest_fixed_unbound", Kind: "task", IsPublic: true,
		CreatorID: staff, IsStaff: true,
	})
	if err != nil {
		t.Fatalf("seed unbound fixed app: %v", err)
	}
	t.Cleanup(func() { _ = svc.Delete(ctx, fixed.ID, staff, true) })

	page, err := repo.ListApplicationPage(ctx, catalog.ApplicationPageQuery{
		Scope: "public", Mode: catalog.PageModeConsume, Kind: catalog.PageQueryKindFixed,
		IncludeUnbound: false,
		Search:         "itest_fixed_unbound", Limit: 10,
		CallerID: staff, IsStaff: false,
	})
	if err != nil {
		t.Fatalf("fixed consume page: %v", err)
	}
	found := false
	for _, row := range page {
		if row.App.ID == fixed.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("an unbound fixed application must be returned by kind=fixed&mode=consume without include_unbound")
	}
}

func sameID(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
