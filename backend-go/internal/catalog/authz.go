package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
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
	// ErrExecutionProviderMissing is returned to staff callers when the
	// binding's provider_key has no providers row at all.
	ErrExecutionProviderMissing = errors.New("provider is not registered")
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
//	provider ACTIVE                matched by provider_key (fail closed)
//
// Staff bypass visibility only (they may execute private apps); a
// disabled application is not executable by anyone — that is the whole
// point of the admin kill switch.
//
// The provider check is deliberately keyed on `provider_key`, not on the
// nullable `provider_id` (评测 P1): bindings created through the API did
// not populate provider_id, so a NULL join let a disabled provider keep
// executing. provider_key is the business key that is always present.
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
	// Fail closed: no providers row, or a row that is not active, blocks
	// execution for everyone (the kill switch must be absolute).
	if !row.ProviderStatus.Valid {
		if isStaff {
			return nil, ErrExecutionProviderMissing
		}
		return nil, ErrExecutionForbidden
	}
	if row.ProviderStatus.String != "active" {
		if isStaff {
			return nil, ErrExecutionProviderInactive
		}
		return nil, ErrExecutionForbidden
	}
	return &Executable{
		Application: &Application{
			ID:       int64(row.AppID),
			Slug:     row.AppSlug,
			Name:     row.AppName,
			Kind:     row.AppKind,
			IsPublic: row.AppIsPublic,
			Enabled:  row.AppEnabled,
		},
		Binding: bindingFromExecutionAuthRow(row),
	}, nil
}

// bindingFromExecutionAuthRow converts the authorization row into the
// SAME Binding shape bindingFromRow produces.
//
// This is load-bearing (评测 P0): Binding.Snapshot() is frozen onto every
// Run, and it writes timeout_seconds / config / capabilities
// unconditionally. Dropping any of them silently zeroes the run's runtime
// budget — a background run with timeout_seconds=0 fails immediately and
// an interactive run loses its extended deadline.
func bindingFromExecutionAuthRow(row db.GetExecutionAuthBundleRow) *Binding {
	b := &Binding{
		ID:                 int64(row.BindingID),
		ApplicationID:      int64(row.AppID),
		ProviderKey:        row.BindingProviderKey,
		RuntimeType:        row.BindingRuntimeType,
		ExternalResourceID: row.BindingExternalResourceID,
		IdentityMode:       row.BindingIdentityMode,
		ExecutionMode:      row.BindingExecutionMode,
		SessionPolicy:      row.BindingSessionPolicy,
		ArtifactPolicy:     row.BindingArtifactPolicy,
		TimeoutSeconds:     int64(row.BindingTimeoutSeconds),
		Enabled:            row.BindingEnabled,
	}
	if row.BindingProviderID.Valid {
		v := int64(row.BindingProviderID.Int64)
		b.ProviderID = &v
	}
	_ = json.Unmarshal(row.BindingCapabilities, &b.Capabilities)
	_ = json.Unmarshal(row.BindingConfig, &b.Config)
	return b
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
