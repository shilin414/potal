package enterpriseaccess

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

const (
	ModeAll       = "all"
	ModeAssigned  = "assigned"
	ModeAdminOnly = "admin_only"
)

var ErrNotFound = errors.New("enterprise access: not found")
var ErrInvalidGrant = errors.New("enterprise access: invalid grant")

type DepartmentGrant struct {
	DepartmentID    int64  `json:"department_id"`
	Name            string `json:"name"`
	IncludeChildren bool   `json:"include_children"`
	CoveredUsers    int    `json:"covered_users"`
}
type UserGrant struct {
	DirectoryUserID int64    `json:"directory_user_id"`
	Name            string   `json:"name"`
	AvatarURL       string   `json:"avatar_url"`
	Departments     []string `json:"departments"`
}
type Policy struct {
	ApplicationID int64             `json:"application_id"`
	AccessMode    string            `json:"access_mode"`
	Departments   []DepartmentGrant `json:"departments"`
	Users         []UserGrant       `json:"users"`
}
type Update struct {
	AccessMode       string            `json:"access_mode"`
	DepartmentGrants []DepartmentGrant `json:"department_grants"`
	UserGrants       []int64           `json:"user_grants"`
}

type Service struct{ DB *sql.DB }

func (s *Service) Get(ctx context.Context, appID int64) (*Policy, error) {
	var mode string
	if err := s.DB.QueryRowContext(ctx, `SELECT access_mode FROM applications WHERE id=?`, appID).Scan(&mode); errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	p := &Policy{ApplicationID: appID, AccessMode: mode, Departments: []DepartmentGrant{}, Users: []UserGrant{}}
	rows, err := s.DB.QueryContext(ctx, `SELECT d.id,d.name,g.include_children,(SELECT COUNT(DISTINCT dud.directory_user_id) FROM directory_department_closure c JOIN directory_user_departments dud ON dud.department_id=c.descendant_id JOIN directory_users du ON du.id=dud.directory_user_id AND du.is_active=1 AND du.is_resigned=0 WHERE c.ancestor_id=d.id AND (g.include_children=1 OR c.depth=0)) FROM application_department_grants g JOIN directory_departments d ON d.id=g.department_id WHERE g.application_id=? ORDER BY d.name,d.id`, appID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var g DepartmentGrant
		if err = rows.Scan(&g.DepartmentID, &g.Name, &g.IncludeChildren, &g.CoveredUsers); err != nil {
			rows.Close()
			return nil, err
		}
		p.Departments = append(p.Departments, g)
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	rows, err = s.DB.QueryContext(ctx, `SELECT du.id,du.name,du.avatar_url FROM application_user_grants g JOIN directory_users du ON du.id=g.directory_user_id WHERE g.application_id=? ORDER BY du.name,du.id`, appID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var g UserGrant
		if err = rows.Scan(&g.DirectoryUserID, &g.Name, &g.AvatarURL); err != nil {
			rows.Close()
			return nil, err
		}
		depRows, e := s.DB.QueryContext(ctx, `SELECT d.name FROM directory_user_departments dud JOIN directory_departments d ON d.id=dud.department_id WHERE dud.directory_user_id=? ORDER BY dud.is_primary DESC,d.name`, g.DirectoryUserID)
		if e != nil {
			rows.Close()
			return nil, e
		}
		for depRows.Next() {
			var n string
			if e = depRows.Scan(&n); e != nil {
				depRows.Close()
				rows.Close()
				return nil, e
			}
			g.Departments = append(g.Departments, n)
		}
		depRows.Close()
		p.Users = append(p.Users, g)
	}
	return p, rows.Close()
}

func (s *Service) Replace(ctx context.Context, appID, actorID int64, in Update) (*Policy, error) {
	if !validMode(in.AccessMode) {
		return nil, fmt.Errorf("%w: invalid access_mode", ErrInvalidGrant)
	}
	depMap := map[int64]bool{}
	for _, g := range in.DepartmentGrants {
		if g.DepartmentID <= 0 {
			return nil, fmt.Errorf("%w: invalid department", ErrInvalidGrant)
		}
		depMap[g.DepartmentID] = g.IncludeChildren
	}
	userMap := map[int64]struct{}{}
	for _, id := range in.UserGrants {
		if id <= 0 {
			return nil, fmt.Errorf("%w: invalid user", ErrInvalidGrant)
		}
		userMap[id] = struct{}{}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var before string
	if err = tx.QueryRowContext(ctx, `SELECT access_mode FROM applications WHERE id=? FOR UPDATE`, appID).Scan(&before); errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	for id := range depMap {
		var n int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM directory_departments WHERE id=? AND is_active=1`, id).Scan(&n); err != nil {
			return nil, err
		}
		if n != 1 {
			return nil, fmt.Errorf("%w: department %d is not active", ErrInvalidGrant, id)
		}
	}
	for id := range userMap {
		var n int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM directory_users WHERE id=?`, id).Scan(&n); err != nil {
			return nil, err
		}
		if n != 1 {
			return nil, fmt.Errorf("%w: user %d not found", ErrInvalidGrant, id)
		}
	}
	isPublic := in.AccessMode == ModeAll
	if _, err = tx.ExecContext(ctx, `UPDATE applications SET access_mode=?,is_public=? WHERE id=?`, in.AccessMode, isPublic, appID); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM application_department_grants WHERE application_id=?`, appID); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM application_user_grants WHERE application_id=?`, appID); err != nil {
		return nil, err
	}
	for id, children := range depMap {
		if _, err = tx.ExecContext(ctx, `INSERT INTO application_department_grants(application_id,department_id,include_children,created_by) VALUES(?,?,?,?)`, appID, id, children, actorID); err != nil {
			return nil, err
		}
	}
	for id := range userMap {
		if _, err = tx.ExecContext(ctx, `INSERT INTO application_user_grants(application_id,directory_user_id,created_by) VALUES(?,?,?)`, appID, id, actorID); err != nil {
			return nil, err
		}
	}
	detail, _ := json.Marshal(map[string]any{"application_id": appID, "before": map[string]any{"access_mode": before}, "after": map[string]any{"access_mode": in.AccessMode, "departments": depMap, "users": keys(userMap)}})
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_logs(user_id,action,resource,resource_id,detail) VALUES(?,'application.access.update','application',?,?)`, actorID, strconv.FormatInt(appID, 10), detail); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.Get(ctx, appID)
}

func (s *Service) Allowed(ctx context.Context, appID, userID int64, isStaff bool) (bool, error) {
	if isStaff {
		return true, nil
	}
	var allowed bool
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM applications a JOIN directory_users du ON du.local_user_id=? AND du.is_active=1 AND du.is_resigned=0 AND du.active_status=2 WHERE a.id=? AND a.enabled=1 AND (a.access_mode='all' OR (a.access_mode='assigned' AND (EXISTS(SELECT 1 FROM application_user_grants ug WHERE ug.application_id=a.id AND ug.directory_user_id=du.id) OR EXISTS(SELECT 1 FROM directory_user_departments dud JOIN directory_department_closure dc ON dc.descendant_id=dud.department_id JOIN application_department_grants dg ON dg.department_id=dc.ancestor_id AND (dg.include_children=1 OR dc.depth=0) WHERE dud.directory_user_id=du.id AND dg.application_id=a.id)))))`, userID, appID).Scan(&allowed)
	return allowed, err
}
func validMode(v string) bool { return v == ModeAll || v == ModeAssigned || v == ModeAdminOnly }
func keys(m map[int64]struct{}) []int64 {
	out := make([]int64, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	return out
}
