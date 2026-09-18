package directory

import (
	"context"
	"errors"
	"testing"

	"github.com/creation-agent-studio/backend-go/internal/identity"
)

type fakeDirectoryClient struct{ failEmployees bool }

func (f *fakeDirectoryClient) TenantToken(context.Context) (*identity.TenantTokenResult, error) {
	return &identity.TenantTokenResult{TenantAccessToken: "t"}, nil
}
func (f *fakeDirectoryClient) ListDirectoryDepartments(_ context.Context, _ string, parent, page string) (*identity.DirectoryDepartmentPage, error) {
	if page != "" {
		t := &identity.DirectoryDepartmentPage{}
		return t, nil
	}
	switch parent {
	case "0":
		return &identity.DirectoryDepartmentPage{Departments: []identity.DirectoryDepartment{{DepartmentID: "a", Name: identity.DirectoryI18nText{DefaultValue: "A"}, ParentDepartmentID: "0"}}}, nil
	case "a":
		return &identity.DirectoryDepartmentPage{Departments: []identity.DirectoryDepartment{{DepartmentID: "b", Name: identity.DirectoryI18nText{DefaultValue: "B"}, ParentDepartmentID: "a"}}}, nil
	default:
		return &identity.DirectoryDepartmentPage{}, nil
	}
}
func (f *fakeDirectoryClient) ListDirectoryEmployees(_ context.Context, _ string, _ []string, status int, _ string) (*identity.DirectoryEmployeePage, error) {
	if f.failEmployees {
		return nil, errors.New("page 2 failed")
	}
	if status != 1 {
		return &identity.DirectoryEmployeePage{}, nil
	}
	return &identity.DirectoryEmployeePage{Employees: []identity.DirectoryEmployee{{BaseInfo: identity.DirectoryEmployeeBaseInfo{EmployeeID: "ou-1", Name: identity.DirectoryEmployeeName{Name: identity.DirectoryI18nText{DefaultValue: "张三"}}, Departments: []identity.DirectoryEmployeeDepartment{{DepartmentID: "b"}}, ActiveStatus: 2, IsResigned: boolPtr(false)}}}}, nil
}
func TestFetchTraversesDepartmentTreeAndMergesEmployees(t *testing.T) {
	svc := NewService(nil, &fakeDirectoryClient{}, nil)
	deps, users, err := svc.fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 2 || deps[1].ParentOpenID != "a" {
		t.Fatalf("deps=%#v", deps)
	}
	if len(users) != 1 || users[0].OpenID != "ou-1" || len(users[0].Departments) != 1 {
		t.Fatalf("users=%#v", users)
	}
}
func TestFetchPageFailureReturnsNoSnapshot(t *testing.T) {
	svc := NewService(nil, &fakeDirectoryClient{failEmployees: true}, nil)
	deps, users, err := svc.fetch(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if deps != nil || users != nil {
		t.Fatalf("partial snapshot leaked deps=%v users=%v", deps, users)
	}
}

func TestFetchRejectsFieldlessEmployeeRows(t *testing.T) {
	client := &fieldlessEmployeeClient{fakeDirectoryClient: fakeDirectoryClient{}}
	svc := NewService(nil, client, nil)
	if _, _, err := svc.fetch(context.Background()); err == nil {
		t.Fatal("expected missing field error")
	}
}

type fieldlessEmployeeClient struct{ fakeDirectoryClient }

func (f *fieldlessEmployeeClient) ListDirectoryEmployees(context.Context, string, []string, int, string) (*identity.DirectoryEmployeePage, error) {
	return &identity.DirectoryEmployeePage{Employees: []identity.DirectoryEmployee{{BaseInfo: identity.DirectoryEmployeeBaseInfo{EmployeeID: "ou-empty"}}}}, nil
}

func boolPtr(v bool) *bool { return &v }
