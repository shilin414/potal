package integration

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/catalog"
	"github.com/creation-agent-studio/backend-go/internal/enterpriseaccess"
	"github.com/creation-agent-studio/backend-go/internal/platform/config"
	"github.com/creation-agent-studio/backend-go/internal/platform/database"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
)

func TestEnterpriseACLDepartmentUserAndEmploymentLifecycle(t *testing.T) {
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
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	rootOpen := "itest-acl-root-" + suffix
	childOpen := "itest-acl-child-" + suffix
	userOpen := "itest-acl-user-" + suffix
	staff := int64(777900)
	localUsername := "itest-acl-local-" + suffix
	userRes, err := db.ExecContext(ctx, `INSERT INTO users(username,password_hash,display_name,display_id,email,role,auth_source,is_staff,is_active) VALUES(?,'',?,'','', 'creator','feishu',0,1)`, localUsername, localUsername)
	if err != nil {
		t.Fatal(err)
	}
	localUser, _ := userRes.LastInsertId()
	res, err := db.ExecContext(ctx, `INSERT INTO directory_departments(open_department_id,name,parent_open_department_id,is_active) VALUES(?,?,'0',1)`, rootOpen, "itest ACL Root")
	if err != nil {
		t.Fatal(err)
	}
	rootID, _ := res.LastInsertId()
	res, err = db.ExecContext(ctx, `INSERT INTO directory_departments(open_department_id,name,parent_id,parent_open_department_id,is_active) VALUES(?,?,?,?,1)`, childOpen, "itest ACL Child", rootID, rootOpen)
	if err != nil {
		t.Fatal(err)
	}
	childID, _ := res.LastInsertId()
	res, err = db.ExecContext(ctx, `INSERT INTO directory_users(open_id,name,active_status,is_resigned,local_user_id,is_active) VALUES(?,?,2,0,?,1)`, userOpen, "itest ACL User", localUser)
	if err != nil {
		t.Fatal(err)
	}
	directoryUserID, _ := res.LastInsertId()
	_, err = db.ExecContext(ctx, `INSERT INTO directory_user_departments(directory_user_id,department_id,is_primary) VALUES(?,?,1)`, directoryUserID, childID)
	if err != nil {
		t.Fatal(err)
	}
	for _, edge := range [][3]int64{{rootID, rootID, 0}, {rootID, childID, 1}, {childID, childID, 0}} {
		if _, err = db.ExecContext(ctx, `INSERT INTO directory_department_closure(ancestor_id,descendant_id,depth) VALUES(?,?,?)`, edge[0], edge[1], edge[2]); err != nil {
			t.Fatal(err)
		}
	}
	svc := &catalog.Service{DB: db, ACLEnabled: true}
	app, _, err := svc.Create(ctx, &catalog.CreateInput{Name: "itest ACL App " + suffix, Kind: "custom", RendererKey: "itest-acl", CreatorID: staff, IsStaff: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `UPDATE applications SET enabled=1 WHERE id=?`, app.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM application_user_grants WHERE application_id=?`, app.ID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM application_department_grants WHERE application_id=?`, app.ID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM audit_logs WHERE resource_id=?`, fmt.Sprint(app.ID))
		_ = svc.Delete(context.Background(), app.ID, staff, true)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM directory_user_departments WHERE directory_user_id=?`, directoryUserID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM directory_department_closure WHERE ancestor_id IN (?,?) OR descendant_id IN (?,?)`, rootID, childID, rootID, childID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM directory_users WHERE id=?`, directoryUserID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM users WHERE id=?`, localUser)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM directory_departments WHERE id IN (?,?)`, rootID, childID)
	})
	access := &enterpriseaccess.Service{DB: db}
	repo := &catalog.Repo{DB: db, ACLEnabled: true}
	_, err = access.Replace(ctx, app.ID, staff, enterpriseaccess.Update{AccessMode: enterpriseaccess.ModeAssigned, DepartmentGrants: []enterpriseaccess.DepartmentGrant{{DepartmentID: rootID, IncludeChildren: true}}})
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := access.Allowed(ctx, app.ID, localUser, false)
	if err != nil || !allowed {
		t.Fatalf("child grant allow=%v err=%v", allowed, err)
	}
	page, err := repo.ListApplicationPage(ctx, catalog.ApplicationPageQuery{Scope: "accessible", Mode: catalog.PageModeConsume, Kind: catalog.PageQueryKindFixed, Search: suffix, Limit: 10, CallerID: localUser})
	if err != nil || len(page) != 1 {
		t.Fatalf("page=%d err=%v", len(page), err)
	}
	_, err = access.Replace(ctx, app.ID, staff, enterpriseaccess.Update{AccessMode: enterpriseaccess.ModeAssigned, DepartmentGrants: []enterpriseaccess.DepartmentGrant{{DepartmentID: rootID, IncludeChildren: false}}})
	if err != nil {
		t.Fatal(err)
	}
	allowed, err = access.Allowed(ctx, app.ID, localUser, false)
	if err != nil || allowed {
		t.Fatalf("direct-only parent allow=%v err=%v", allowed, err)
	}
	_, err = access.Replace(ctx, app.ID, staff, enterpriseaccess.Update{AccessMode: enterpriseaccess.ModeAssigned, UserGrants: []int64{directoryUserID}})
	if err != nil {
		t.Fatal(err)
	}
	allowed, err = access.Allowed(ctx, app.ID, localUser, false)
	if err != nil || !allowed {
		t.Fatalf("user grant allow=%v err=%v", allowed, err)
	}
	// Turn the fixture into a bound chat application for bootstrap + worker checks.
	if _, err = db.ExecContext(ctx, `UPDATE applications SET kind='chat',renderer_key='chat' WHERE id=?`, app.ID); err != nil {
		t.Fatal(err)
	}
	bindingRes, err := db.ExecContext(ctx, `INSERT INTO runtime_bindings(application_id,provider_key,runtime_type,external_resource_id,enabled) VALUES(?,'feishu_aily','agent','itest-acl-gate',1)`, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	bindingID, _ := bindingRes.LastInsertId()
	gateSvc := &catalog.Service{DB: db, ACLEnabled: true}
	if _, err = gateSvc.AuthorizeExecution(ctx, app.ID, localUser, false); err != nil {
		t.Fatalf("assigned execution: %v", err)
	}
	if _, err = gateSvc.AuthorizeExecution(ctx, app.ID, localUser+1, false); err != catalog.ErrExecutionForbidden {
		t.Fatalf("unlinked execution err=%v", err)
	}
	if err = svc.Favorite(ctx, app.ID, localUser, false); err != nil {
		t.Fatalf("favorite through ACL: %v", err)
	}
	groups, err := repo.BootstrapGroups(ctx, catalog.BootstrapGroupQuery{CallerID: localUser})
	if err != nil {
		t.Fatal(err)
	}
	foundFavorite := false
	for _, row := range groups.Favorites {
		if row.ApplicationID == app.ID {
			foundFavorite = true
		}
	}
	if !foundFavorite {
		t.Fatal("assigned favorite missing from bootstrap")
	}
	if err = svc.Unfavorite(ctx, app.ID, localUser, false); err != nil {
		t.Fatalf("unfavorite through ACL: %v", err)
	}
	runID := ids.New()
	if _, err = db.ExecContext(ctx, `INSERT INTO runs(id,user_id,application_id,runtime_binding_id,provider,runtime_type,status) VALUES(?,?,?,?, 'feishu_aily','agent','queued')`, runID.Bytes(), localUser, app.ID, bindingID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM runs WHERE id=?`, runID.Bytes())
		_, _ = db.ExecContext(context.Background(), `DELETE FROM runtime_bindings WHERE id=?`, bindingID)
	})
	gate, err := gateSvc.RunGateState(ctx, runID.Bytes())
	if err != nil || !gate.ACLAllowed.Bool {
		t.Fatalf("initial gate=%#v err=%v", gate, err)
	}
	if _, err = access.Replace(ctx, app.ID, staff, enterpriseaccess.Update{AccessMode: enterpriseaccess.ModeAdminOnly}); err != nil {
		t.Fatal(err)
	}
	if err = svc.Favorite(ctx, app.ID, localUser, false); err != catalog.ErrNotFound {
		t.Fatalf("revoked favorite err=%v", err)
	}
	if _, err = gateSvc.AuthorizeExecution(ctx, app.ID, localUser, false); err != catalog.ErrExecutionForbidden {
		t.Fatalf("revoked execution err=%v", err)
	}
	if _, err = gateSvc.AuthorizeExecution(ctx, app.ID, staff, true); err != nil {
		t.Fatalf("staff admin_only execution: %v", err)
	}
	gate, err = gateSvc.RunGateState(ctx, runID.Bytes())
	if err != nil || gate.ACLAllowed.Bool {
		t.Fatalf("revoked gate=%#v err=%v", gate, err)
	}
	staffName := "itest-acl-staff-" + suffix
	res, err = db.ExecContext(ctx, `INSERT INTO users(username,password_hash,display_name,display_id,email,role,auth_source,is_staff) VALUES(?,'',?,'','', 'admin','local',1)`, staffName, staffName)
	if err != nil {
		t.Fatal(err)
	}
	staffUserID, _ := res.LastInsertId()
	staffRunID := ids.New()
	if _, err = db.ExecContext(ctx, `INSERT INTO runs(id,user_id,application_id,runtime_binding_id,provider,runtime_type,status) VALUES(?,?,?,?, 'feishu_aily','agent','queued')`, staffRunID.Bytes(), staffUserID, app.ID, bindingID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM runs WHERE id=?`, staffRunID.Bytes())
		_, _ = db.ExecContext(context.Background(), `DELETE FROM users WHERE id=?`, staffUserID)
	})
	staffGate, err := gateSvc.RunGateState(ctx, staffRunID.Bytes())
	if err != nil || !staffGate.ACLAllowed.Bool {
		t.Fatalf("staff admin_only gate=%#v err=%v", staffGate, err)
	}
	if _, err = db.ExecContext(ctx, `UPDATE users SET is_active=0 WHERE id=?`, staffUserID); err != nil {
		t.Fatal(err)
	}
	staffGate, err = gateSvc.RunGateState(ctx, staffRunID.Bytes())
	if err != nil || staffGate.ACLAllowed.Bool {
		t.Fatalf("disabled staff gate=%#v err=%v", staffGate, err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO directory_users(open_id,name,active_status,is_resigned,local_user_id,is_active) VALUES(?,?,2,0,?,1)`, "itest-disabled-staff-"+suffix, staffName, staffUserID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM directory_users WHERE local_user_id=?`, staffUserID)
	})
	if _, err = db.ExecContext(ctx, `UPDATE applications SET access_mode='all',is_public=1 WHERE id=?`, app.ID); err != nil {
		t.Fatal(err)
	}
	staffGate, err = gateSvc.RunGateState(ctx, staffRunID.Bytes())
	if err != nil || staffGate.ACLAllowed.Bool {
		t.Fatalf("disabled linked staff gate=%#v err=%v", staffGate, err)
	}
	legacyGate := &catalog.Service{DB: db, ACLEnabled: false}
	staffGate, err = legacyGate.RunGateState(ctx, staffRunID.Bytes())
	if err != nil || staffGate.ACLAllowed.Bool {
		t.Fatalf("disabled legacy gate=%#v err=%v", staffGate, err)
	}
	if _, err = access.Replace(ctx, app.ID, staff, enterpriseaccess.Update{AccessMode: enterpriseaccess.ModeAssigned, UserGrants: []int64{directoryUserID}}); err != nil {
		t.Fatal(err)
	}

	if _, err = db.ExecContext(ctx, `UPDATE directory_users SET is_resigned=1,is_active=0 WHERE id=?`, directoryUserID); err != nil {
		t.Fatal(err)
	}
	allowed, err = access.Allowed(ctx, app.ID, localUser, false)
	if err != nil || allowed {
		t.Fatalf("resigned allow=%v err=%v", allowed, err)
	}
	allowed, err = access.Allowed(ctx, app.ID, staff, true)
	if err != nil || !allowed {
		t.Fatalf("staff bypass allow=%v err=%v", allowed, err)
	}
}
