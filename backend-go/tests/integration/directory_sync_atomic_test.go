package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/directory"
	"github.com/creation-agent-studio/backend-go/internal/identity"
	"github.com/creation-agent-studio/backend-go/internal/platform/config"
	"github.com/creation-agent-studio/backend-go/internal/platform/database"
)

type cyclicDirectoryClient struct{}

func (*cyclicDirectoryClient) TenantToken(context.Context) (*identity.TenantTokenResult, error) {
	return &identity.TenantTokenResult{TenantAccessToken: "test"}, nil
}
func (*cyclicDirectoryClient) ListDirectoryDepartments(_ context.Context, _ string, parent, page string) (*identity.DirectoryDepartmentPage, error) {
	if page != "" {
		return &identity.DirectoryDepartmentPage{}, nil
	}
	switch parent {
	case "0":
		return &identity.DirectoryDepartmentPage{Departments: []identity.DirectoryDepartment{{DepartmentID: "cycle-a", Name: identity.DirectoryI18nText{DefaultValue: "A"}, ParentDepartmentID: "cycle-b"}}}, nil
	case "cycle-a":
		return &identity.DirectoryDepartmentPage{Departments: []identity.DirectoryDepartment{{DepartmentID: "cycle-b", Name: identity.DirectoryI18nText{DefaultValue: "B"}, ParentDepartmentID: "cycle-a"}}}, nil
	default:
		return &identity.DirectoryDepartmentPage{}, nil
	}
}
func (*cyclicDirectoryClient) ListDirectoryEmployees(_ context.Context, _ string, _ []string, status int, _ string) (*identity.DirectoryEmployeePage, error) {
	if status != 1 {
		return &identity.DirectoryEmployeePage{}, nil
	}
	return &identity.DirectoryEmployeePage{Employees: []identity.DirectoryEmployee{{BaseInfo: identity.DirectoryEmployeeBaseInfo{EmployeeID: "cycle-user", Name: identity.DirectoryEmployeeName{Name: identity.DirectoryI18nText{DefaultValue: "User"}}, Departments: []identity.DirectoryEmployeeDepartment{{DepartmentID: "cycle-a"}}, ActiveStatus: 2, IsResigned: integrationBoolPtr(false)}}}}, nil
}

func TestDirectoryStageValidationFailureKeepsLiveSnapshot(t *testing.T) {
	if os.Getenv("STUDIO_TEST_DB") != "1" {
		t.Skip("set STUDIO_TEST_DB=1")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(context.Background(), cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	suffix := fmt.Sprint(time.Now().UnixNano())
	openID := "itest-live-snapshot-" + suffix
	if _, err = db.ExecContext(ctx, `INSERT INTO directory_departments(open_department_id,name,parent_open_department_id,is_active) VALUES(?,?,'0',1)`, openID, "keep-me"); err != nil {
		t.Fatal(err)
	}
	repo := &directory.Repo{DB: db}
	_, err = repo.CreateSyncRun(ctx, "manual", nil)
	if err != nil {
		t.Fatal(err)
	}
	run, err := repo.ClaimPendingRun(ctx)
	if err != nil || run == nil {
		t.Fatalf("claim run=%v err=%v", run, err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM directory_sync_user_department_stage WHERE run_id=?`, run.ID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM directory_sync_user_stage WHERE run_id=?`, run.ID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM directory_sync_department_stage WHERE run_id=?`, run.ID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM directory_sync_runs WHERE id=?`, run.ID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM directory_departments WHERE open_department_id=?`, openID)
	})
	svc := directory.NewService(repo, &cyclicDirectoryClient{}, nil)
	if err = svc.Run(ctx, run); err == nil {
		t.Fatal("expected cycle validation failure")
	}
	var name string
	var active bool
	if err = db.QueryRowContext(ctx, `SELECT name,is_active FROM directory_departments WHERE open_department_id=?`, openID).Scan(&name, &active); err != nil {
		t.Fatal(err)
	}
	if name != "keep-me" || !active {
		t.Fatalf("live snapshot changed name=%q active=%v", name, active)
	}
	var staged int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM directory_sync_department_stage WHERE run_id=?`, run.ID).Scan(&staged); err != nil {
		t.Fatal(err)
	}
	if staged != 2 {
		t.Fatalf("staged=%d want=2", staged)
	}
}

func TestDirectoryLeaseLossIsFenced(t *testing.T) {
	if os.Getenv("STUDIO_TEST_DB") != "1" {
		t.Skip("set STUDIO_TEST_DB=1")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(context.Background(), cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &directory.Repo{DB: db}
	owner := fmt.Sprintf("definitely-not-owner-%d", time.Now().UnixNano())
	if err = repo.RenewLease(context.Background(), owner, time.Minute); !errors.Is(err, directory.ErrLeaseLost) {
		t.Fatalf("renew err=%v", err)
	}
	if err = repo.Publish(context.Background(), 0, nil, nil, nil, owner, 0, 0); !errors.Is(err, directory.ErrLeaseLost) {
		t.Fatalf("publish err=%v", err)
	}
}

type cancelledDirectoryClient struct{}

func (*cancelledDirectoryClient) TenantToken(ctx context.Context) (*identity.TenantTokenResult, error) {
	return nil, ctx.Err()
}
func (*cancelledDirectoryClient) ListDirectoryDepartments(context.Context, string, string, string) (*identity.DirectoryDepartmentPage, error) {
	return nil, context.Canceled
}
func (*cancelledDirectoryClient) ListDirectoryEmployees(context.Context, string, []string, int, string) (*identity.DirectoryEmployeePage, error) {
	return nil, context.Canceled
}

func TestDirectoryCancelledContextStillFinalizesRun(t *testing.T) {
	if os.Getenv("STUDIO_TEST_DB") != "1" {
		t.Skip("set STUDIO_TEST_DB=1")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(context.Background(), cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &directory.Repo{DB: db}
	_, err = repo.CreateSyncRun(context.Background(), "manual", nil)
	if err != nil {
		t.Fatal(err)
	}
	run, err := repo.ClaimPendingRun(context.Background())
	if err != nil || run == nil {
		t.Fatalf("claim=%v err=%v", run, err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM directory_sync_runs WHERE id=?`, run.ID)
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc := directory.NewService(repo, &cancelledDirectoryClient{}, nil)
	if err = svc.Run(ctx, run); err == nil {
		t.Fatal("expected cancellation")
	}
	stored, err := repo.GetSyncRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "failed" {
		t.Fatalf("status=%s", stored.Status)
	}
}

func integrationBoolPtr(v bool) *bool { return &v }
