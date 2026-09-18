package directory

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var ErrNotFound = errors.New("directory: not found")
var ErrLeaseLost = errors.New("directory: sync lease lost")

type Repo struct{ DB *sql.DB }

func (r *Repo) ListDepartments(ctx context.Context, query string, parentID *int64, includeInactive bool) ([]Department, error) {
	args := []any{}
	where := []string{"1=1"}
	if !includeInactive {
		where = append(where, "d.is_active = 1")
	}
	if strings.TrimSpace(query) != "" {
		where = append(where, "d.name LIKE ?")
		args = append(args, "%"+escapeLike(strings.TrimSpace(query))+"%")
	}
	if parentID != nil {
		where = append(where, "d.parent_id = ?")
		args = append(args, *parentID)
	}
	rows, err := r.DB.QueryContext(ctx, `SELECT d.id,d.open_department_id,d.name,d.parent_id,d.parent_open_department_id,d.order_weight,d.is_active,d.last_synced_at FROM directory_departments d WHERE `+strings.Join(where, " AND ")+` ORDER BY d.parent_id IS NOT NULL,d.order_weight,d.name,d.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Department{}
	for rows.Next() {
		var d Department
		var parent sql.NullInt64
		var last sql.NullTime
		if err := rows.Scan(&d.ID, &d.OpenDepartmentID, &d.Name, &parent, &d.ParentOpenDepartmentID, &d.OrderWeight, &d.IsActive, &last); err != nil {
			return nil, err
		}
		d.ParentID = nullInt64Ptr(parent)
		d.LastSyncedAt = nullTimePtr(last)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *Repo) ListUsers(ctx context.Context, query string, departmentID *int64, includeInactive bool, cursor string, limit int) ([]User, string, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	lastID := int64(0)
	if cursor != "" {
		if raw, err := base64.RawURLEncoding.DecodeString(cursor); err == nil {
			lastID, _ = strconv.ParseInt(string(raw), 10, 64)
		}
	}
	args := []any{lastID}
	where := []string{"du.id > ?"}
	if !includeInactive {
		where = append(where, "du.is_active = 1")
	}
	if strings.TrimSpace(query) != "" {
		where = append(where, "du.name LIKE ?")
		args = append(args, "%"+escapeLike(strings.TrimSpace(query))+"%")
	}
	join := ""
	if departmentID != nil {
		join = " JOIN directory_user_departments filter_dud ON filter_dud.directory_user_id=du.id AND filter_dud.department_id=?"
		args = append([]any{*departmentID}, args...)
	}
	args = append(args, limit+1)
	rows, err := r.DB.QueryContext(ctx, `SELECT DISTINCT du.id,du.open_id,du.name,du.avatar_url,du.active_status,du.is_resigned,du.local_user_id,du.is_active,du.last_synced_at FROM directory_users du`+join+` WHERE `+strings.Join(where, " AND ")+` ORDER BY du.id LIMIT ?`, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		var local sql.NullInt64
		var last sql.NullTime
		if err := rows.Scan(&u.ID, &u.OpenID, &u.Name, &u.AvatarURL, &u.ActiveStatus, &u.IsResigned, &local, &u.IsActive, &last); err != nil {
			return nil, "", err
		}
		u.LocalUserID = nullInt64Ptr(local)
		u.LastSyncedAt = nullTimePtr(last)
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(out[len(out)-1].ID, 10)))
	}
	for i := range out {
		deps, err := r.userDepartments(ctx, out[i].ID)
		if err != nil {
			return nil, "", err
		}
		out[i].Departments = deps
	}
	return out, next, nil
}

func (r *Repo) userDepartments(ctx context.Context, userID int64) ([]DepartmentRef, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT d.id,d.name,dud.is_primary FROM directory_user_departments dud JOIN directory_departments d ON d.id=dud.department_id WHERE dud.directory_user_id=? ORDER BY dud.is_primary DESC,d.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DepartmentRef{}
	for rows.Next() {
		var d DepartmentRef
		if err := rows.Scan(&d.ID, &d.Name, &d.IsPrimary); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *Repo) Stats(ctx context.Context) (*Stats, error) {
	var out Stats
	err := r.DB.QueryRowContext(ctx, `SELECT
	 (SELECT COUNT(*) FROM directory_departments),
	 (SELECT COUNT(*) FROM directory_departments WHERE is_active=1),
	 (SELECT COUNT(*) FROM directory_users),
	 (SELECT COUNT(*) FROM directory_users WHERE is_active=1),
	 (SELECT COUNT(*) FROM directory_users WHERE is_resigned=1),
	 (SELECT COUNT(*) FROM feishu_identities WHERE open_id IS NOT NULL AND open_id<>''),
	 (SELECT COUNT(DISTINCT du.id) FROM directory_users du JOIN feishu_identities fi ON fi.user_id=du.local_user_id AND fi.open_id=du.open_id WHERE du.is_active=1)`).Scan(&out.DepartmentsTotal, &out.DepartmentsActive, &out.UsersTotal, &out.UsersActive, &out.UsersResigned, &out.OAuthUsers, &out.LinkedDirectoryUsers)
	return &out, err
}

func (r *Repo) GetSyncConfig(ctx context.Context) (*SyncConfig, error) {
	row := r.DB.QueryRowContext(ctx, `SELECT enabled,schedule_type,interval_minutes,daily_time,timezone,next_run_at,last_run_at,last_success_at,updated_at FROM directory_sync_configs WHERE id=1`)
	var c SyncConfig
	var next, last, success sql.NullTime
	if err := row.Scan(&c.Enabled, &c.ScheduleType, &c.IntervalMinutes, &c.DailyTime, &c.Timezone, &next, &last, &success, &c.UpdatedAt); err != nil {
		return nil, err
	}
	c.NextRunAt = nullTimePtr(next)
	c.LastRunAt = nullTimePtr(last)
	c.LastSuccessAt = nullTimePtr(success)
	return &c, nil
}

func (r *Repo) UpdateSyncConfig(ctx context.Context, c SyncConfig, updatedBy int64, next time.Time) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var before SyncConfig
	var beforeNext, beforeLast, beforeSuccess sql.NullTime
	if err = tx.QueryRowContext(ctx, `SELECT enabled,schedule_type,interval_minutes,daily_time,timezone,next_run_at,last_run_at,last_success_at,updated_at FROM directory_sync_configs WHERE id=1 FOR UPDATE`).Scan(&before.Enabled, &before.ScheduleType, &before.IntervalMinutes, &before.DailyTime, &before.Timezone, &beforeNext, &beforeLast, &beforeSuccess, &before.UpdatedAt); err != nil {
		return err
	}
	before.NextRunAt = nullTimePtr(beforeNext)
	before.LastRunAt = nullTimePtr(beforeLast)
	before.LastSuccessAt = nullTimePtr(beforeSuccess)
	if _, err = tx.ExecContext(ctx, `UPDATE directory_sync_configs SET enabled=?,schedule_type=?,interval_minutes=?,daily_time=?,timezone=?,next_run_at=?,updated_by=? WHERE id=1`, c.Enabled, c.ScheduleType, c.IntervalMinutes, c.DailyTime, c.Timezone, next, updatedBy); err != nil {
		return err
	}
	after := c
	after.NextRunAt = &next
	detail, err := json.Marshal(map[string]any{"before": before, "after": after})
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_logs(user_id,action,resource,resource_id,detail) VALUES(?,'directory.sync_config.update','enterprise','1',?)`, updatedBy, detail); err != nil {
		return err
	}
	return tx.Commit()
}
func (r *Repo) CreateSyncRun(ctx context.Context, trigger string, createdBy *int64) (*SyncRun, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var by any
	if createdBy != nil {
		by = *createdBy
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO directory_sync_runs(trigger_type,status,created_by) VALUES (?,'pending',?)`, trigger, by)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	if createdBy != nil {
		detail, _ := json.Marshal(map[string]any{"run_id": id})
		if _, err = tx.ExecContext(ctx, `INSERT INTO audit_logs(user_id,action,resource,resource_id,detail) VALUES(?,'directory.sync.manual','enterprise',?,?)`, *createdBy, strconv.FormatInt(id, 10), detail); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return r.GetSyncRun(ctx, id)
}
func (r *Repo) GetSyncRun(ctx context.Context, id int64) (*SyncRun, error) {
	row := r.DB.QueryRowContext(ctx, `SELECT id,trigger_type,status,departments_count,users_count,active_users_count,memberships_count,active_memberships_count,started_at,finished_at,error_code,error_message,created_by,created_at FROM directory_sync_runs WHERE id=?`, id)
	var v SyncRun
	var st, ft sql.NullTime
	var by sql.NullInt64
	if err := row.Scan(&v.ID, &v.TriggerType, &v.Status, &v.DepartmentsCount, &v.UsersCount, &v.ActiveUsersCount, &v.MembershipsCount, &v.ActiveMembershipsCount, &st, &ft, &v.ErrorCode, &v.ErrorMessage, &by, &v.CreatedAt); errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	v.StartedAt = nullTimePtr(st)
	v.FinishedAt = nullTimePtr(ft)
	v.CreatedBy = nullInt64Ptr(by)
	return &v, nil
}
func (r *Repo) ListSyncRuns(ctx context.Context, limit int) ([]SyncRun, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	rows, err := r.DB.QueryContext(ctx, `SELECT id,trigger_type,status,departments_count,users_count,active_users_count,memberships_count,active_memberships_count,started_at,finished_at,error_code,error_message,created_by,created_at FROM directory_sync_runs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SyncRun{}
	for rows.Next() {
		var v SyncRun
		var st, ft sql.NullTime
		var by sql.NullInt64
		if err := rows.Scan(&v.ID, &v.TriggerType, &v.Status, &v.DepartmentsCount, &v.UsersCount, &v.ActiveUsersCount, &v.MembershipsCount, &v.ActiveMembershipsCount, &st, &ft, &v.ErrorCode, &v.ErrorMessage, &by, &v.CreatedAt); err != nil {
			return nil, err
		}
		v.StartedAt = nullTimePtr(st)
		v.FinishedAt = nullTimePtr(ft)
		v.CreatedBy = nullInt64Ptr(by)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (r *Repo) RecoverStaleRuns(ctx context.Context) error {
	_, err := r.DB.ExecContext(ctx, `UPDATE directory_sync_runs SET status='failed',error_code='stale_running_recovered',error_message='scheduler recovered a stale running directory sync',finished_at=CURRENT_TIMESTAMP(3) WHERE status='running' AND started_at<DATE_SUB(CURRENT_TIMESTAMP(3),INTERVAL 24 HOUR)`)
	return err
}

func (r *Repo) ClaimPendingRun(ctx context.Context) (*SyncRun, error) {
	var id int64
	err := r.DB.QueryRowContext(ctx, `SELECT id FROM directory_sync_runs WHERE status='pending' ORDER BY id LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	res, err := r.DB.ExecContext(ctx, `UPDATE directory_sync_runs SET status='running',started_at=CURRENT_TIMESTAMP(3),error_code='',error_message='' WHERE id=? AND status='pending'`, id)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, nil
	}
	return r.GetSyncRun(ctx, id)
}
func (r *Repo) FinishRun(ctx context.Context, id int64, status string, dc, uc, auc, mc, amc int, code, msg string) error {
	res, err := r.DB.ExecContext(ctx, `UPDATE directory_sync_runs SET status=?,departments_count=?,users_count=?,active_users_count=?,memberships_count=?,active_memberships_count=?,error_code=?,error_message=?,finished_at=CURRENT_TIMESTAMP(3) WHERE id=? AND status='running'`, status, dc, uc, auc, mc, amc, code, left(msg, 1000), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("directory sync run %d is no longer running", id)
	}
	return nil
}
func (r *Repo) AcquireLease(ctx context.Context, owner string, d time.Duration) (bool, error) {
	res, err := r.DB.ExecContext(ctx, `UPDATE directory_sync_configs SET lease_owner=?,lease_until=DATE_ADD(CURRENT_TIMESTAMP(3),INTERVAL ? SECOND) WHERE id=1 AND (lease_until IS NULL OR lease_until<CURRENT_TIMESTAMP(3) OR lease_owner=?)`, owner, int(d.Seconds()), owner)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
func (r *Repo) RenewLease(ctx context.Context, owner string, d time.Duration) error {
	res, err := r.DB.ExecContext(ctx, `UPDATE directory_sync_configs SET lease_until=DATE_ADD(CURRENT_TIMESTAMP(3),INTERVAL ? SECOND) WHERE id=1 AND lease_owner=? AND lease_until>=CURRENT_TIMESTAMP(3)`, int(d.Seconds()), owner)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrLeaseLost
	}
	return nil
}
func (r *Repo) ReleaseLease(ctx context.Context, owner string) {
	_, _ = r.DB.ExecContext(ctx, `UPDATE directory_sync_configs SET lease_owner=NULL,lease_until=NULL WHERE id=1 AND lease_owner=?`, owner)
}
func (r *Repo) MarkScheduleStarted(ctx context.Context, next time.Time) error {
	_, err := r.DB.ExecContext(ctx, `UPDATE directory_sync_configs SET last_run_at=CURRENT_TIMESTAMP(3),next_run_at=? WHERE id=1`, next)
	return err
}
func (r *Repo) ValidateSnapshotSize(ctx context.Context, departments, totalUsers, totalMemberships, activeUsers, activeMemberships int) error {
	var prevDepartments, prevActiveUsers, prevActiveMemberships int
	err := r.DB.QueryRowContext(ctx, `SELECT departments_count,active_users_count,active_memberships_count FROM directory_sync_runs WHERE status='success' ORDER BY id DESC LIMIT 1`).Scan(&prevDepartments, &prevActiveUsers, &prevActiveMemberships)
	if errors.Is(err, sql.ErrNoRows) {
		err = r.DB.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM directory_departments),(SELECT COUNT(*) FROM directory_users),(SELECT COUNT(*) FROM directory_user_departments)`).Scan(&prevDepartments, &prevActiveUsers, &prevActiveMemberships)
		if err != nil {
			return err
		}
		return validateSnapshotShrink(prevDepartments, prevActiveUsers, prevActiveMemberships, departments, totalUsers, totalMemberships)
	}
	if err != nil {
		return err
	}
	return validateSnapshotShrink(prevDepartments, prevActiveUsers, prevActiveMemberships, departments, activeUsers, activeMemberships)
}
func validateSnapshotShrink(prevDepartments, prevUsers, prevMemberships, departments, users, memberships int) error {
	type metric struct {
		name       string
		prev, next int
	}
	for _, m := range []metric{{"departments", prevDepartments, departments}, {"users", prevUsers, users}, {"memberships", prevMemberships, memberships}} {
		if m.prev > 0 && m.next*100 < m.prev*70 {
			return fmt.Errorf("suspicious directory shrink: %s %d -> %d (below 70%%); refusing publication", m.name, m.prev, m.next)
		}
	}
	return nil
}

func (r *Repo) WriteStage(ctx context.Context, runID int64, deps []stagedDepartment, users []stagedUser) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM directory_sync_department_stage WHERE run_id=?`, runID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM directory_sync_user_stage WHERE run_id=?`, runID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM directory_sync_user_department_stage WHERE run_id=?`, runID); err != nil {
		return err
	}
	for _, d := range deps {
		if _, err = tx.ExecContext(ctx, `INSERT INTO directory_sync_department_stage(run_id,open_department_id,name,parent_open_department_id,order_weight) VALUES(?,?,?,?,?)`, runID, d.OpenID, d.Name, d.ParentOpenID, d.OrderWeight); err != nil {
			return err
		}
	}
	for _, u := range users {
		if _, err = tx.ExecContext(ctx, `INSERT INTO directory_sync_user_stage(run_id,open_id,name,avatar_url,active_status,is_resigned) VALUES(?,?,?,?,?,?)`, runID, u.OpenID, u.Name, u.AvatarURL, u.ActiveStatus, u.Resigned); err != nil {
			return err
		}
		for i, dep := range u.Departments {
			if _, err = tx.ExecContext(ctx, `INSERT INTO directory_sync_user_department_stage(run_id,user_open_id,department_open_id,is_primary) VALUES(?,?,?,?)`, runID, u.OpenID, dep, i == 0); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (r *Repo) CleanupStage(ctx context.Context, runID int64) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"directory_sync_user_department_stage", "directory_sync_user_stage", "directory_sync_department_stage"} {
		if _, err = tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE run_id=?", runID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *Repo) Publish(ctx context.Context, runID int64, deps []stagedDepartment, users []stagedUser, edges []ClosureEdge, leaseOwner string, activeUsers, activeMemberships int) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if leaseOwner != "" {
		var owner sql.NullString
		var until sql.NullTime
		var now time.Time
		if err = tx.QueryRowContext(ctx, `SELECT lease_owner,lease_until,CURRENT_TIMESTAMP(3) FROM directory_sync_configs WHERE id=1 FOR UPDATE`).Scan(&owner, &until, &now); err != nil {
			return err
		}
		if !owner.Valid || owner.String != leaseOwner || !until.Valid || until.Time.Before(now) {
			return ErrLeaseLost
		}
	}
	// Set-based publication keeps the live-snapshot transaction bounded even
	// for 20k+ employees. Per-row round trips previously exceeded the driver's
	// read timeout while holding the transaction open.
	if _, err = tx.ExecContext(ctx, `INSERT INTO directory_departments(open_department_id,name,parent_open_department_id,order_weight,is_active,sync_generation,last_synced_at)
		SELECT open_department_id,name,parent_open_department_id,order_weight,1,run_id,CURRENT_TIMESTAMP(3)
		FROM directory_sync_department_stage WHERE run_id=?
		ON DUPLICATE KEY UPDATE name=VALUES(name),parent_open_department_id=VALUES(parent_open_department_id),order_weight=VALUES(order_weight),is_active=1,sync_generation=VALUES(sync_generation),last_synced_at=VALUES(last_synced_at)`, runID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE directory_departments child LEFT JOIN directory_departments parent ON parent.open_department_id=child.parent_open_department_id SET child.parent_id=CASE WHEN child.parent_open_department_id IN ('','0') THEN NULL ELSE parent.id END WHERE child.sync_generation=?`, runID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE directory_departments SET is_active=0 WHERE sync_generation IS NULL OR sync_generation<>?`, runID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO directory_users(open_id,name,avatar_url,active_status,is_resigned,is_active,sync_generation,last_synced_at)
		SELECT open_id,name,avatar_url,active_status,is_resigned,(active_status=2 AND is_resigned=0),run_id,CURRENT_TIMESTAMP(3)
		FROM directory_sync_user_stage WHERE run_id=?
		ON DUPLICATE KEY UPDATE name=VALUES(name),avatar_url=VALUES(avatar_url),active_status=VALUES(active_status),is_resigned=VALUES(is_resigned),is_active=VALUES(is_active),sync_generation=VALUES(sync_generation),last_synced_at=VALUES(last_synced_at)`, runID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE directory_users du JOIN feishu_identities fi ON fi.open_id=du.open_id SET du.local_user_id=fi.user_id WHERE du.sync_generation=?`, runID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE directory_users SET is_active=0 WHERE sync_generation IS NULL OR sync_generation<>?`, runID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM directory_user_departments`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO directory_user_departments(directory_user_id,department_id,is_primary) SELECT u.id,d.id,s.is_primary FROM directory_sync_user_department_stage s JOIN directory_users u ON u.open_id=s.user_open_id JOIN directory_departments d ON d.open_department_id=s.department_open_id WHERE s.run_id=?`, runID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM directory_department_closure`); err != nil {
		return err
	}
	if err = insertClosureEdgesTx(ctx, tx, edges); err != nil {
		return err
	}
	memberships := 0
	for _, u := range users {
		memberships += len(u.Departments)
	}
	result, err := tx.ExecContext(ctx, `UPDATE directory_sync_runs SET status='success',departments_count=?,users_count=?,active_users_count=?,memberships_count=?,active_memberships_count=?,error_code='',error_message='',finished_at=CURRENT_TIMESTAMP(3) WHERE id=? AND status='running'`, len(deps), len(users), activeUsers, memberships, activeMemberships, runID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("directory sync run %d is no longer running", runID)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE directory_sync_configs SET last_success_at=CURRENT_TIMESTAMP(3) WHERE id=1`); err != nil {
		return err
	}
	detail, err := json.Marshal(map[string]any{"departments": len(deps), "users": len(users), "active_users": activeUsers, "memberships": memberships, "active_memberships": activeMemberships})
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_logs(user_id,action,resource,resource_id,detail) SELECT created_by,'directory.sync.success','enterprise',?,? FROM directory_sync_runs WHERE id=?`, strconv.FormatInt(runID, 10), detail, runID); err != nil {
		return err
	}
	return tx.Commit()
}

func insertClosureEdgesTx(ctx context.Context, tx *sql.Tx, edges []ClosureEdge) error {
	const batch = 300
	for start := 0; start < len(edges); start += batch {
		end := start + batch
		if end > len(edges) {
			end = len(edges)
		}
		var b strings.Builder
		b.WriteString(`INSERT INTO directory_department_closure(ancestor_id,descendant_id,depth) SELECT a.id,d.id,e.depth FROM (`)
		args := make([]any, 0, (end-start)*3)
		for i, e := range edges[start:end] {
			if i > 0 {
				b.WriteString(` UNION ALL `)
			}
			if i == 0 {
				b.WriteString(`SELECT ? AS ancestor_open_id, ? AS descendant_open_id, ? AS depth`)
			} else {
				b.WriteString(`SELECT ?, ?, ?`)
			}
			args = append(args, e.AncestorOpenID, e.DescendantOpenID, e.Depth)
		}
		b.WriteString(`) e JOIN directory_departments a ON a.open_department_id=e.ancestor_open_id JOIN directory_departments d ON d.open_department_id=e.descendant_open_id`)
		if _, err := tx.ExecContext(ctx, b.String(), args...); err != nil {
			return err
		}
	}
	return nil
}

func WriteAudit(ctx context.Context, db *sql.DB, userID *int64, action, resourceID string, detail any) error {
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	var uid any
	if userID != nil {
		uid = *userID
	}
	_, err = db.ExecContext(ctx, `INSERT INTO audit_logs(user_id,action,resource,resource_id,detail) VALUES(?,?,'enterprise',?,?)`, uid, action, resourceID, raw)
	return err
}
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
func left(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
func ValidateSyncConfig(c SyncConfig) error {
	if c.ScheduleType != "interval" && c.ScheduleType != "daily" {
		return fmt.Errorf("schedule_type must be interval or daily")
	}
	if c.IntervalMinutes < 15 || c.IntervalMinutes > 10080 {
		return fmt.Errorf("interval_minutes must be between 15 and 10080")
	}
	parts := strings.Split(c.DailyTime, ":")
	if len(parts) != 2 {
		return fmt.Errorf("daily_time must be HH:MM")
	}
	h, e1 := strconv.Atoi(parts[0])
	m, e2 := strconv.Atoi(parts[1])
	if e1 != nil || e2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return fmt.Errorf("daily_time must be HH:MM")
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return fmt.Errorf("invalid timezone")
	}
	return nil
}
