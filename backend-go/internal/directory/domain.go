package directory

import (
	"database/sql"
	"time"
)

type Department struct {
	ID                     int64      `json:"id"`
	OpenDepartmentID       string     `json:"open_department_id"`
	Name                   string     `json:"name"`
	ParentID               *int64     `json:"parent_id"`
	ParentOpenDepartmentID string     `json:"parent_open_department_id"`
	OrderWeight            string     `json:"order_weight"`
	IsActive               bool       `json:"is_active"`
	LastSyncedAt           *time.Time `json:"last_synced_at"`
}

type User struct {
	ID           int64           `json:"id"`
	OpenID       string          `json:"open_id"`
	Name         string          `json:"name"`
	AvatarURL    string          `json:"avatar_url"`
	ActiveStatus int             `json:"active_status"`
	IsResigned   bool            `json:"is_resigned"`
	LocalUserID  *int64          `json:"local_user_id"`
	IsActive     bool            `json:"is_active"`
	Departments  []DepartmentRef `json:"departments"`
	LastSyncedAt *time.Time      `json:"last_synced_at"`
}

type DepartmentRef struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	IsPrimary bool   `json:"is_primary"`
}

type Stats struct {
	DepartmentsTotal     int64 `json:"departments_total"`
	DepartmentsActive    int64 `json:"departments_active"`
	UsersTotal           int64 `json:"users_total"`
	UsersActive          int64 `json:"users_active"`
	UsersResigned        int64 `json:"users_resigned"`
	OAuthUsers           int64 `json:"oauth_users"`
	LinkedDirectoryUsers int64 `json:"linked_directory_users"`
}

type SyncConfig struct {
	Enabled         bool       `json:"enabled"`
	ScheduleType    string     `json:"schedule_type"`
	IntervalMinutes int        `json:"interval_minutes"`
	DailyTime       string     `json:"daily_time"`
	Timezone        string     `json:"timezone"`
	NextRunAt       *time.Time `json:"next_run_at"`
	LastRunAt       *time.Time `json:"last_run_at"`
	LastSuccessAt   *time.Time `json:"last_success_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type SyncRun struct {
	ID                     int64      `json:"id"`
	TriggerType            string     `json:"trigger_type"`
	Status                 string     `json:"status"`
	DepartmentsCount       int        `json:"departments_count"`
	UsersCount             int        `json:"users_count"`
	ActiveUsersCount       int        `json:"active_users_count"`
	MembershipsCount       int        `json:"memberships_count"`
	ActiveMembershipsCount int        `json:"active_memberships_count"`
	StartedAt              *time.Time `json:"started_at"`
	FinishedAt             *time.Time `json:"finished_at"`
	ErrorCode              string     `json:"error_code"`
	ErrorMessage           string     `json:"error_message"`
	CreatedBy              *int64     `json:"created_by"`
	CreatedAt              time.Time  `json:"created_at"`
}

type stagedDepartment struct {
	OpenID       string
	Name         string
	ParentOpenID string
	OrderWeight  string
	Active       bool
}

type stagedUser struct {
	OpenID       string
	Name         string
	AvatarURL    string
	ActiveStatus int
	Resigned     bool
	Departments  []string
}

type ClosureEdge struct {
	AncestorOpenID   string
	DescendantOpenID string
	Depth            int
}

func nullTimePtr(v sql.NullTime) *time.Time {
	if !v.Valid {
		return nil
	}
	t := v.Time
	return &t
}

func nullInt64Ptr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}
