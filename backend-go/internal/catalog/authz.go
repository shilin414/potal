package catalog

import (
	"context"
	"database/sql"
	"errors"

	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
)

// Execution authorization errors (评测 P0-1).
//
// Regular callers get ONE opaque answer (ErrExecutionForbidden) so they
// cannot probe which applications exist, are private, or are disabled —
// exactly the same non-disclosure rule the run/conversation 404s follow.
// Staff callers (who already manage the app catalog) get precise reasons.
var (
	// ErrExecutionForbidden covers every rejected combination for a regular
	// user: unknown id, private app, disabled app, non-chat kind, missing
	// or disabled binding, disabled provider.
	ErrExecutionForbidden = errors.New("application is not executable")
	// ErrExecutionNotFound is returned to staff callers when the id simply
	// does not exist.
	ErrExecutionNotFound = errors.New("application not found")
	// ErrExecutionDisabled is returned to staff callers for a disabled app.
	ErrExecutionDisabled = errors.New("application is disabled")
	// ErrExecutionNotChat is returned to staff callers for a non-chat app.
	ErrExecutionNotChat = errors.New("application kind is not chat")
	// ErrExecutionProviderInactive is returned to staff callers when the
	// provider row exists but is not active.
	ErrExecutionProviderInactive = errors.New("provider is not active")
)

// Executable is the result of a successful AuthorizeExecution: the
// application facts plus the enabled binding that will serve the run.
type Executable struct {
	Application *Application
	Binding     *Binding
}

// AuthorizeExecution is the SINGLE gate every execution entry point must
// pass before creating a Run or admitting a Schedule (评测 P0-1):
//
//	CreateRun                    POST /api/v2/runs
//	CreateSchedule               POST /api/v2/schedules
//	UpdateSchedule(app change)   PATCH /api/v2/schedules/{id}
//	RunScheduleNow               POST /api/v2/schedules/{id}/run-now
//	Scheduler due-slot admission every fire, not only at creation
//
// It verifies, in one joined query:
//
//	application exists
//	application.enabled = 1        disabled apps NEVER execute (any caller)
//	application.kind = 'chat'
//	regular caller → is_public = 1 visibility == execution right
//	runtime binding enabled = 1
//	provider status = 'active'     (when the provider row exists)
//
// Staff bypass visibility only (they may execute private apps); a
// disabled application is not executable by anyone — that is the whole
// point of the admin kill switch.
func (s *Service) AuthorizeExecution(ctx context.Context, applicationID, userID int64, isStaff bool) (*Executable, error) {
	row, err := s.repo().q(ctx).GetExecutionAuthBundle(ctx, db.GetExecutionAuthBundleParams{
		ID:      uint64(applicationID),
		ShowAll: isStaff,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, s.explainRejection(ctx, applicationID, isStaff)
	}
	if err != nil {
		return nil, err
	}
	if row.ProviderStatus.Valid && row.ProviderStatus.String != "active" {
		if isStaff {
			return nil, ErrExecutionProviderInactive
		}
		return nil, ErrExecutionForbidden
	}
	app := &Application{
		ID:       int64(row.AppID),
		Slug:     row.AppSlug,
		Name:     row.AppName,
		Kind:     row.AppKind,
		IsPublic: row.AppIsPublic,
		Enabled:  row.AppEnabled,
	}
	binding := &Binding{
		ID:                 int64(row.BindingID),
		ApplicationID:      int64(row.AppID),
		ProviderKey:        row.BindingProviderKey,
		RuntimeType:        row.BindingRuntimeType,
		ExternalResourceID: row.BindingExternalResourceID,
		IdentityMode:       row.BindingIdentityMode,
		ExecutionMode:      row.BindingExecutionMode,
		SessionPolicy:      row.BindingSessionPolicy,
		ArtifactPolicy:     row.BindingArtifactPolicy,
		Enabled:            row.BindingEnabled,
	}
	if row.BindingProviderID.Valid {
		v := int64(row.BindingProviderID.Int64)
		binding.ProviderID = &v
	}
	return &Executable{Application: app, Binding: binding}, nil
}

// explainRejection turns a join miss into a precise error for staff and a
// deliberately opaque one for regular callers (no existence disclosure).
func (s *Service) explainRejection(ctx context.Context, applicationID int64, isStaff bool) error {
	if !isStaff {
		return ErrExecutionForbidden
	}
	app, err := s.ApplicationByID(ctx, applicationID)
	if err != nil || app == nil {
		return ErrExecutionNotFound
	}
	if !app.Enabled {
		return ErrExecutionDisabled
	}
	if app.Kind != "chat" {
		return ErrExecutionNotChat
	}
	if !app.IsPublic {
		return ErrExecutionForbidden
	}
	// Enabled + public + chat: the only remaining reason is the binding.
	b, err := s.EnabledBinding(ctx, applicationID)
	if err != nil || b == nil {
		return ErrNoBinding
	}
	return ErrExecutionForbidden
}
