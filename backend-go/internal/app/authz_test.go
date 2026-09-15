// Unit tests for the scheduler resolver error classification (复审 P1-1)
// and the identity-lookup error contract of authorizeForOwner.
// The DB-backed cases are opt-in (STUDIO_TEST_DB=1).
package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/go-sql-driver/mysql"

	"github.com/creation-agent-studio/backend-go/internal/catalog"
	"github.com/creation-agent-studio/backend-go/internal/identity"
	"github.com/creation-agent-studio/backend-go/internal/platform/config"
	"github.com/creation-agent-studio/backend-go/internal/platform/database"
)

// TestExecutionDeniedClassifiesPolicyErrors: exactly the seven policy
// sentinels (and their wrapped forms) count as "denied"; everything else —
// MySQL driver errors, connection refused, context deadlines — must NOT
// be classified as a denial, or the scheduler would permanently fail
// occurrences on transient outages.
func TestExecutionDeniedClassifiesPolicyErrors(t *testing.T) {
	policy := []error{
		nil,
		catalog.ErrExecutionForbidden,
		catalog.ErrExecutionNotFound,
		catalog.ErrExecutionDisabled,
		catalog.ErrExecutionNotChat,
		catalog.ErrNoBinding,
		catalog.ErrExecutionProviderInactive,
		catalog.ErrExecutionProviderMissing,
		fmt.Errorf("wrapped: %w", catalog.ErrExecutionForbidden),
	}
	for _, err := range policy {
		if !executionDenied(err) {
			t.Fatalf("executionDenied(%v) = false, want true", err)
		}
	}

	infra := []error{
		errors.New("driver: bad connection"),
		fmt.Errorf("authorize: %w", &mysql.MySQLError{Number: 1213, Message: "Deadlock found"}),
		context.DeadlineExceeded,
	}
	for _, err := range infra {
		if executionDenied(err) {
			t.Fatalf("executionDenied(%v) = true, want false (infra error must retry the slot)", err)
		}
	}
}

// brokenDB returns an opened-but-unreachable MySQL handle: every query
// fails with a connection error, exactly like a transient outage.
func brokenDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sql.Open("mysql", "itest:noreach@tcp(127.0.0.1:1)/none")
	if err != nil {
		t.Fatalf("open broken db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func testCatalogDB(t *testing.T) *catalog.Service {
	t.Helper()
	if os.Getenv("STUDIO_TEST_DB") != "1" {
		t.Skip("set STUDIO_TEST_DB=1 to run database-backed unit tests")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	d, err := database.Open(context.Background(), cfg.Database)
	if err != nil {
		t.Fatalf("database: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return &catalog.Service{DB: d}
}

// TestAuthorizeForOwnerPropagatesIdentityInfraError (复审 P1-1): when the
// identity lookup FAILS (DB outage), authorizeForOwner must return the
// error — the old code silently demoted the owner to non-staff, turning
// an outage into a fake "private application" policy denial.
func TestAuthorizeForOwnerPropagatesIdentityInfraError(t *testing.T) {
	catalogSvc := testCatalogDB(t)
	users := identity.NewRepo(brokenDB(t))

	res := &bindingResolver{Catalog: catalogSvc, Users: users}
	binding, err := res.EnabledBindingFor(context.Background(), 1, 42)
	if err == nil {
		t.Fatalf("identity outage swallowed: binding=%v err=%v, want error", binding != nil, err)
	}
	if binding != nil {
		t.Fatal("an infra failure must not resolve a binding")
	}
}

// TestEnabledBindingForUnknownOwnerIsPolicyDenial (control): an owner that
// does not exist is a POLICY fact (strictest non-staff rules), not an
// error — the resolver must report (nil, nil) so the scheduler records a
// failed occurrence instead of retrying forever.
func TestEnabledBindingForUnknownOwnerIsPolicyDenial(t *testing.T) {
	catalogSvc := testCatalogDB(t)
	users := identity.NewRepo(catalogSvc.DB)

	res := &bindingResolver{Catalog: catalogSvc, Users: users}
	binding, err := res.EnabledBindingFor(context.Background(), 1<<40 /* nonexistent app */, 999_999_999 /* unknown owner */)
	if err != nil {
		t.Fatalf("unknown owner must be a policy denial, got err=%v", err)
	}
	if binding != nil {
		t.Fatal("unknown owner + unknown app must not resolve a binding")
	}
}
