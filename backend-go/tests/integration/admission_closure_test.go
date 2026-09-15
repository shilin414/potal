// Admission-closure integration tests (opt-in: STUDIO_TEST_DB=1).
//
// They prove the round's new invariants on the real MySQL 5.7:
//
//	P0-1 execution authorization (disabled/private/non-chat/app-less binding)
//	P0-2 one conversation = one active turn (serialized, concurrent-safe)
//	P1-1 tick + run-now share ONE admission (no parallel active occurrence)
//	P1-5 attachment claim is atomic (one run wins)
//	§十二 schedule soft delete keeps pending deliveries resolvable
package integration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/automation/schedule"
	"github.com/creation-agent-studio/backend-go/internal/automation/scheduler"
	"github.com/creation-agent-studio/backend-go/internal/catalog"
	"github.com/creation-agent-studio/backend-go/internal/execution"
	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
)

// ── P0-2: conversation serialization ──

// TestConversationAdmissionSerializesTurns: a second submit to a
// conversation with an in-flight (queued) run is rejected with
// ErrConversationBusy — the provider session can never be addressed by two
// concurrent runs.
func TestConversationAdmissionSerializesTurns(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	convID := seedConversation(t, svc)

	in := func() *execution.CreateRunInput {
		return &execution.CreateRunInput{
			UserID:         42,
			ApplicationID:  1,
			ConversationID: convID,
			Provider:       "itest_admission",
			RuntimeType:    "agent",
			ExecutionMode:  "interactive",
			Content:        "hello",
		}
	}
	if _, err := svc.CreateRun(ctx, in()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if _, err := svc.CreateRun(ctx, in()); !errors.Is(err, execution.ErrConversationBusy) {
		t.Fatalf("second run err=%v, want ErrConversationBusy", err)
	}
}

// TestConversationConcurrentSubmitSingleTurn: N concurrent submits to one
// conversation produce exactly ONE run; every loser is rejected, none
// silently creates a second active turn.
func TestConversationConcurrentSubmitSingleTurn(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()
	convID := seedConversation(t, svc)

	const n = 12
	var ok, busy, other atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.CreateRun(ctx, &execution.CreateRunInput{
				UserID:         42,
				ApplicationID:  1,
				ConversationID: convID,
				Provider:       "itest_admission",
				RuntimeType:    "agent",
				ExecutionMode:  "interactive",
				Content:        "burst",
			})
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, execution.ErrConversationBusy):
				busy.Add(1)
			default:
				other.Add(1)
				t.Logf("unexpected create error: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 1 {
		t.Fatalf("concurrent submits: %d runs created, want exactly 1", ok.Load())
	}
	if busy.Load() != n-1 {
		t.Fatalf("concurrent submits: %d busy, want %d (other=%d)", busy.Load(), n-1, other.Load())
	}
}

// ── P1-5: attachment claim ──

// TestAttachmentClaimSingleWinner: two runs referencing the same pending
// attachment — exactly one claims it; the other rolls back entirely.
func TestAttachmentClaimSingleWinner(t *testing.T) {
	svc, _ := testEnv(t)
	ctx := context.Background()

	attID := ids.New()
	if _, err := svc.Querier().CreateAttachment(ctx, db.CreateAttachmentParams{
		ID: attID.Bytes(), Provider: "itest_admission", AttachmentType: "file",
		Name: "a.txt", SourceType: "upload", ContentType: "text/plain",
		Status: "pending", AuthMode: "user", AuthSubjectKey: "42",
		CreatedBy: sql.NullInt64{Int64: 42, Valid: true},
	}); err != nil {
		t.Fatalf("seed attachment: %v", err)
	}

	convs := []int64{seedConversation(t, svc), seedConversation(t, svc)}
	var ok, claimed, other atomic.Int64
	var wg sync.WaitGroup
	for _, conv := range convs {
		wg.Add(1)
		go func(convID int64) {
			defer wg.Done()
			_, err := svc.CreateRun(ctx, &execution.CreateRunInput{
				UserID:         42,
				ApplicationID:  1,
				ConversationID: convID,
				Provider:       "itest_admission",
				RuntimeType:    "agent",
				ExecutionMode:  "interactive",
				Content:        "with attachment",
				AttachmentIDs:  []string{attID.String()},
			})
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, execution.ErrAttachmentClaimed):
				claimed.Add(1)
			default:
				other.Add(1)
				t.Logf("unexpected create error: %v", err)
			}
		}(conv)
	}
	wg.Wait()
	if ok.Load() != 1 || claimed.Load() != 1 {
		t.Fatalf("attachment race: ok=%d claimed=%d other=%d, want 1/1/0", ok.Load(), claimed.Load(), other.Load())
	}

	// The winning run really owns it (no half-bound state).
	var bound int64
	if err := svc.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM runtime_attachments WHERE id = ? AND run_id IS NOT NULL`, attID.Bytes()).Scan(&bound); err != nil {
		t.Fatalf("count bound: %v", err)
	}
	if bound != 1 {
		t.Fatalf("attachment bound count=%d, want 1", bound)
	}
	// The losing run must not exist at all: exactly ONE run lives in the
	// two conversations (the winner), and it is the one holding the claim.
	var total int64
	if err := svc.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM runs WHERE conversation_id IN (?, ?)`, convs[0], convs[1]).Scan(&total); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if total != 1 {
		t.Fatalf("runs in the two conversations=%d, want exactly 1 (the loser must roll back)", total)
	}
	var claimedBy int64
	if err := svc.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM runs r JOIN runtime_attachments a ON a.run_id = r.id
		  WHERE a.id = ? AND r.conversation_id IN (?, ?)`, attID.Bytes(), convs[0], convs[1]).Scan(&claimedBy); err != nil {
		t.Fatalf("count claim owner: %v", err)
	}
	if claimedBy != 1 {
		t.Fatalf("runs owning the attachment=%d, want 1", claimedBy)
	}
}

// ── P0-1: execution authorization ──

// TestAuthorizeExecutionGates pins the ONE gate every execution entry
// point shares: public+enabled+chat+binding+active-provider for regular
// users; disabled applications are not executable by anyone.
func TestAuthorizeExecutionGates(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()
	svc := &catalog.Service{DB: env.db}
	uniq := time.Now().UnixNano()
	provKey := fmt.Sprintf("itest_authzprov_%d", uniq)
	provID := seedAuthzProvider(t, env.db, provKey, "active")

	pub := seedAuthzFixture(t, env.db, authzFixture{
		slug: fmt.Sprintf("itest-authz-%d-pub", uniq), kind: "chat",
		isPublic: true, enabled: true, provider: provKey, providerID: provID,
		resource: "agent_pub", timeout: 300,
	})
	if _, err := svc.AuthorizeExecution(ctx, pub, 42, false); err != nil {
		t.Fatalf("public app must be executable by a regular user: %v", err)
	}

	priv := seedAuthzFixture(t, env.db, authzFixture{
		slug: fmt.Sprintf("itest-authz-%d-priv", uniq), kind: "chat",
		isPublic: false, enabled: true, provider: provKey, providerID: provID,
	})
	if _, err := svc.AuthorizeExecution(ctx, priv, 42, false); !errors.Is(err, catalog.ErrExecutionForbidden) {
		t.Fatalf("private app: regular user err=%v, want ErrExecutionForbidden", err)
	}
	if _, err := svc.AuthorizeExecution(ctx, priv, 42, true); err != nil {
		t.Fatalf("private app: staff must be allowed: %v", err)
	}

	dis := seedAuthzFixture(t, env.db, authzFixture{
		slug: fmt.Sprintf("itest-authz-%d-dis", uniq), kind: "chat",
		isPublic: true, enabled: false, provider: provKey, providerID: provID,
	})
	if _, err := svc.AuthorizeExecution(ctx, dis, 42, false); !errors.Is(err, catalog.ErrExecutionForbidden) {
		t.Fatalf("disabled app: regular user err=%v, want ErrExecutionForbidden", err)
	}
	if _, err := svc.AuthorizeExecution(ctx, dis, 42, true); !errors.Is(err, catalog.ErrExecutionDisabled) {
		t.Fatalf("disabled app: staff err=%v, want ErrExecutionDisabled (kill switch applies to everyone)", err)
	}

	nonChat := seedAuthzFixture(t, env.db, authzFixture{
		slug: fmt.Sprintf("itest-authz-%d-flow", uniq), kind: "workflow",
		isPublic: true, enabled: true, provider: provKey, providerID: provID,
	})
	if _, err := svc.AuthorizeExecution(ctx, nonChat, 42, true); !errors.Is(err, catalog.ErrExecutionNotChat) {
		t.Fatalf("non-chat app err=%v, want ErrExecutionNotChat", err)
	}

	noBinding := seedAuthzFixture(t, env.db, authzFixture{
		slug: fmt.Sprintf("itest-authz-%d-nobind", uniq), kind: "chat",
		isPublic: true, enabled: true,
	})
	if _, err := svc.AuthorizeExecution(ctx, noBinding, 42, true); err == nil {
		t.Fatal("app without an enabled binding must be rejected")
	}

	if _, err := svc.AuthorizeExecution(ctx, 1<<40, 42, true); !errors.Is(err, catalog.ErrExecutionNotFound) {
		t.Fatalf("unknown id err=%v, want ErrExecutionNotFound", err)
	}
}

// TestInactiveProviderRejectedEvenWithNullProviderID (评测 P1): the provider
// kill switch must not depend on the nullable runtime_bindings.provider_id.
// The gate joins on provider_key, so a binding created before the FK
// backfill (or by an older API path) still stops executing.
func TestInactiveProviderRejectedEvenWithNullProviderID(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()
	svc := &catalog.Service{DB: env.db}
	uniq := time.Now().UnixNano()
	provKey := fmt.Sprintf("itest_authzoff_%d", uniq)
	seedAuthzProvider(t, env.db, provKey, "inactive")

	// provider_id deliberately left NULL — exactly the old/broken shape.
	appID := seedAuthzFixture(t, env.db, authzFixture{
		slug: fmt.Sprintf("itest-authz-%d-nullpid", uniq), kind: "chat",
		isPublic: true, enabled: true, provider: provKey, providerID: 0,
		resource: "agent_nullpid",
	})
	if _, err := svc.AuthorizeExecution(ctx, appID, 42, false); !errors.Is(err, catalog.ErrExecutionForbidden) {
		t.Fatalf("inactive provider (provider_id NULL): regular err=%v, want ErrExecutionForbidden", err)
	}
	if _, err := svc.AuthorizeExecution(ctx, appID, 42, true); !errors.Is(err, catalog.ErrExecutionProviderInactive) {
		t.Fatalf("inactive provider (provider_id NULL): staff err=%v, want ErrExecutionProviderInactive", err)
	}

	// A binding whose provider_key has no providers row at all fails closed.
	orphan := seedAuthzFixture(t, env.db, authzFixture{
		slug: fmt.Sprintf("itest-authz-%d-orphanprov", uniq), kind: "chat",
		isPublic: true, enabled: true, provider: fmt.Sprintf("itest_noprov_%d", uniq), providerID: 0,
	})
	if _, err := svc.AuthorizeExecution(ctx, orphan, 42, true); !errors.Is(err, catalog.ErrExecutionProviderMissing) {
		t.Fatalf("unregistered provider: staff err=%v, want ErrExecutionProviderMissing", err)
	}

	// Flipping the provider back to active re-enables execution.
	if _, err := env.db.ExecContext(ctx,
		`UPDATE providers SET status = 'active' WHERE provider_key = ?`, provKey); err != nil {
		t.Fatalf("reactivate provider: %v", err)
	}
	if _, err := svc.AuthorizeExecution(ctx, appID, 42, false); err != nil {
		t.Fatalf("reactivated provider must allow execution again: %v", err)
	}
}

// TestAuthorizeExecutionPreservesRuntimeSnapshot (评测 P0): Binding.Snapshot()
// is frozen onto every Run. AuthorizeExecution used to rebuild the Binding
// without timeout/config/capabilities, which zeroed the run's runtime budget
// (background runs failed immediately, interactive runs lost their extended
// deadline).
func TestAuthorizeExecutionPreservesRuntimeSnapshot(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()
	svc := &catalog.Service{DB: env.db}
	uniq := time.Now().UnixNano()
	provKey := fmt.Sprintf("itest_authzsnap_%d", uniq)
	provID := seedAuthzProvider(t, env.db, provKey, "active")

	appID := seedAuthzFixture(t, env.db, authzFixture{
		slug: fmt.Sprintf("itest-authz-%d-snap", uniq), kind: "chat",
		isPublic: true, enabled: true, provider: provKey, providerID: provID,
		resource: "agent_snap", timeout: 321, config: `{"foo":"bar"}`,
	})

	exe, err := svc.AuthorizeExecution(ctx, appID, 42, false)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	snap := exe.Binding.Snapshot()
	if got, _ := snap["timeout_seconds"].(int64); got != 321 {
		t.Fatalf("snapshot timeout_seconds=%v, want 321 (binding field dropped from AuthorizeExecution)", snap["timeout_seconds"])
	}
	cfg, ok := snap["config"].(map[string]any)
	if !ok || cfg["foo"] != "bar" {
		t.Fatalf("snapshot config=%v, want {foo:bar}", snap["config"])
	}
	if snap["external_resource_id"] != "agent_snap" {
		t.Fatalf("snapshot external_resource_id=%v, want agent_snap", snap["external_resource_id"])
	}

	// End to end: the snapshot frozen onto the run must match the binding.
	runsSvc, _ := testEnv(t)
	convID := seedConversation(t, runsSvc)
	run, err := runsSvc.CreateRun(ctx, &execution.CreateRunInput{
		UserID:           42,
		ApplicationID:    appID,
		ConversationID:   convID,
		RuntimeBindingID: exe.Binding.ID,
		Provider:         exe.Binding.ProviderKey,
		RuntimeType:      exe.Binding.RuntimeType,
		ExecutionMode:    exe.Binding.ExecutionMode,
		Content:          "snapshot check",
		RuntimeSnapshot:  snap,
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if got := run.SnapshotInt("timeout_seconds", 300); got != 321 {
		t.Fatalf("run snapshot timeout_seconds=%d, want 321", got)
	}
	if got := run.SnapshotString("external_resource_id"); got != "agent_snap" {
		t.Fatalf("run snapshot external_resource_id=%q, want agent_snap", got)
	}
}

// seedAuthzProvider inserts (or refreshes) a provider row.
func seedAuthzProvider(t *testing.T, d *sql.DB, key, status string) int64 {
	t.Helper()
	ctx := context.Background()
	if _, err := d.ExecContext(ctx,
		`INSERT INTO providers (provider_key, name, supported_runtime_types, status)
		 VALUES (?, ?, '["agent"]', ?)
		 ON DUPLICATE KEY UPDATE status = VALUES(status)`,
		key, key, status); err != nil {
		t.Fatalf("seed provider %s: %v", key, err)
	}
	var id int64
	if err := d.QueryRowContext(ctx, `SELECT id FROM providers WHERE provider_key = ?`, key).Scan(&id); err != nil {
		t.Fatalf("load provider %s: %v", key, err)
	}
	return id
}

// authzFixture describes an application (+ optional runtime binding).
type authzFixture struct {
	slug       string
	kind       string
	isPublic   bool
	enabled    bool
	provider   string // provider_key; "" → no binding
	providerID int64  // 0 → leave runtime_bindings.provider_id NULL
	resource   string
	timeout    int64
	config     string
}

// seedAuthzFixture inserts the application and (when provider != "")
// its enabled binding, returning the application id.
func seedAuthzFixture(t *testing.T, d *sql.DB, f authzFixture) int64 {
	t.Helper()
	ctx := context.Background()
	kind := f.kind
	if kind == "" {
		kind = "chat"
	}
	res, err := d.ExecContext(ctx,
		`INSERT INTO applications (slug, name, kind, is_public, enabled) VALUES (?, ?, ?, ?, ?)`,
		f.slug, f.slug, kind, f.isPublic, f.enabled)
	if err != nil {
		t.Fatalf("seed application %s: %v", f.slug, err)
	}
	appID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("application id: %v", err)
	}
	if f.provider == "" {
		return appID
	}
	timeout := f.timeout
	if timeout == 0 {
		timeout = 300
	}
	config := f.config
	if config == "" {
		config = "{}"
	}
	var pidArg sql.NullInt64
	if f.providerID != 0 {
		pidArg = sql.NullInt64{Int64: f.providerID, Valid: true}
	}
	if _, err := d.ExecContext(ctx,
		`INSERT INTO runtime_bindings
		   (application_id, provider_id, provider_key, runtime_type, external_resource_id,
		    enabled, timeout_seconds, config)
		 VALUES (?, ?, ?, 'agent', ?, 1, ?, ?)`,
		appID, pidArg, f.provider, f.resource, timeout, config); err != nil {
		t.Fatalf("seed binding %s: %v", f.slug, err)
	}
	return appID
}

// ── P0-1 (fire time) + P1-1: one admission path ──

// TestSchedulerRecordsFailureWhenApplicationNotExecutable: at FIRE time the
// scheduler re-authorizes the owner. A resolver that refuses (disabled /
// private application) must record ONE failed occurrence and create ZERO
// runs — the schedule does not keep running an application the owner lost
// access to.
func TestSchedulerRecordsFailureWhenApplicationNotExecutable(t *testing.T) {
	env := newScheduleEnv(t)
	env.schd.Binding = &fakeResolver{binding: nil} // authorization refuses
	ctx := context.Background()
	id := env.seedSchedule(t, time.Now().UTC().Add(-time.Second), schedule.OverlapQueue)

	env.schd.ProcessDue(ctx)

	var status string
	var runID sql.NullString
	if err := env.db.QueryRowContext(ctx,
		`SELECT status, run_id FROM schedule_occurrences WHERE schedule_id = ? ORDER BY id DESC LIMIT 1`,
		id).Scan(&status, &runID); err != nil {
		t.Fatalf("load occurrence: %v", err)
	}
	if status != schedule.OccFailed {
		t.Fatalf("occurrence status=%s, want failed", status)
	}
	if runID.Valid {
		t.Fatal("a non-executable application must not produce a run")
	}
}

// TestTickAndRunNowShareOneAdmission: once a run is active for a schedule,
// neither the scan tick nor run-now may create a SECOND active occurrence.
// With overlap=skip the loser records a skipped occurrence instead.
func TestTickAndRunNowShareOneAdmission(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()
	id := env.seedSchedule(t, time.Now().UTC().Add(-time.Second), schedule.OverlapSkip)

	// run-now wins the admission and leaves an active (queued) run.
	if _, err := env.schd.TriggerNow(ctx, id, 42, false); err != nil {
		t.Fatalf("run-now: %v", err)
	}
	// the tick must observe the active run under the same lock.
	env.schd.ProcessDue(ctx)

	active := env.count(t, `SELECT COUNT(*) FROM schedule_occurrences
		WHERE schedule_id = ? AND status IN ('pending','queued','running')`, id)
	if active != 1 {
		t.Fatalf("active occurrences=%d, want exactly 1", active)
	}
	// run-now with skip policy must now be rejected outright.
	if _, err := env.schd.TriggerNow(ctx, id, 42, false); err == nil {
		t.Fatal("run-now while a run is active must be rejected under overlap=skip")
	}
}

// ── §十二: schedule soft delete ──

// TestScheduleSoftDeleteKeepsDeliveryLookup: deleting a schedule hides it
// from the scan and the owner list, but the row (and its name, which a
// pending Feishu delivery still needs) survives.
func TestScheduleSoftDeleteKeepsDeliveryLookup(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()
	id := env.seedSchedule(t, time.Now().UTC().Add(-time.Minute), schedule.OverlapQueue)

	if err := env.svc.Delete(ctx, id, 42, false); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	if _, err := env.svc.Get(ctx, id, 42, false); !errors.Is(err, schedule.ErrNotFound) {
		t.Fatalf("deleted schedule Get err=%v, want ErrNotFound", err)
	}

	// The delivery worker resolves the name through the raw row — it must
	// still exist (otherwise pending sends retry into oblivion).
	row, err := db.New(env.db).GetScheduleByID(ctx, uint64(id))
	if err != nil {
		t.Fatalf("delivery lookup after delete: %v", err)
	}
	if row.Name == "" {
		t.Fatal("schedule name must survive a soft delete")
	}

	due, err := db.New(env.db).ListDueSchedules(ctx, db.ListDueSchedulesParams{
		NextRunAt: sql.NullTime{Time: time.Now().UTC().Add(time.Hour), Valid: true},
		Limit:     200,
	})
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	for _, r := range due {
		if int64(r.ID) == id {
			t.Fatal("a soft-deleted schedule must never be due")
		}
	}

	items, err := env.svc.List(ctx, 42, schedule.ListFilter{Status: "all", Limit: 100})
	if err != nil {
		t.Fatalf("list owner: %v", err)
	}
	for _, it := range items {
		if it.ID == id {
			t.Fatal("a soft-deleted schedule must not appear in the owner list")
		}
	}
}

// ── 评测 P1: pending admission re-authorizes under the schedule lock ──

// authzResolver resolves bindings through the REAL catalog authorization
// gate, so the scheduler's per-fire re-authorization is exercised against
// the database instead of a fixed fake.
type authzResolver struct{ svc *catalog.Service }

func (r *authzResolver) EnabledBinding(ctx context.Context, appID int64) (*scheduler.BindingView, error) {
	return r.EnabledBindingFor(ctx, appID, 0)
}

func (r *authzResolver) EnabledBindingFor(ctx context.Context, appID, ownerUserID int64) (*scheduler.BindingView, error) {
	exe, err := r.svc.AuthorizeExecution(ctx, appID, ownerUserID, false)
	if err != nil {
		return nil, nil
	}
	b := exe.Binding
	return &scheduler.BindingView{
		ID:            b.ID,
		ProviderKey:   b.ProviderKey,
		RuntimeType:   b.RuntimeType,
		ExecutionMode: b.ExecutionMode,
		Snapshot:      b.Snapshot(),
	}, nil
}

func bindingIDOf(t *testing.T, d *sql.DB, appID int64) int64 {
	t.Helper()
	var id int64
	if err := d.QueryRow(`SELECT id FROM runtime_bindings WHERE application_id = ? AND enabled = 1`, appID).Scan(&id); err != nil {
		t.Fatalf("binding id for app %d: %v", appID, err)
	}
	return id
}

// seedPendingOccurrenceForApp seeds a schedule for appID whose next_run_at
// is far in the future plus one PENDING occurrence (the overlap=queue
// backlog shape), so ProcessDue only exercises the pending-admission path.
func seedPendingOccurrenceForApp(t *testing.T, env *scheduleEnv, appID int64) int64 {
	t.Helper()
	ctx := context.Background()
	payload := []byte(`{"prompt":"pending"}`)
	trigger := []byte(`{"time":"09:00"}`)
	res, err := env.db.ExecContext(ctx,
		`INSERT INTO schedules (owner_user_id, name, application_id, input_payload, schedule_type,
		    cron_expression, trigger_config, timezone, enabled, conversation_policy,
		    overlap_policy, misfire_policy, deadline_policy, next_run_at)
		 VALUES (42, ?, ?, ?, 'daily', '0 9 * * *', ?, 'Asia/Shanghai', 1, 'new_each_run',
		    'queue', 'fire_once', 'execute_anyway', ?)`,
		fmt.Sprintf("itest-pending-%d", time.Now().UnixNano()), appID, payload, trigger,
		time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatalf("seed schedule: %v", err)
	}
	id, _ := res.LastInsertId()
	if _, err := env.db.ExecContext(ctx,
		`INSERT INTO schedule_occurrences (schedule_id, scheduled_at, status) VALUES (?, ?, 'pending')`,
		id, time.Now().UTC().Add(-time.Minute)); err != nil {
		t.Fatalf("seed pending occurrence: %v", err)
	}
	return id
}

// TestPendingOccurrenceAdmissionReauthorizesApplicationChange (评测 P1): a
// pending occurrence admitted AFTER a concurrent schedule PATCH must use
// the NEW application for every field of the run — application, binding and
// runtime snapshot. Resolving the binding before taking the schedule row
// lock produced an internally inconsistent run.
func TestPendingOccurrenceAdmissionReauthorizesApplicationChange(t *testing.T) {
	env := newScheduleEnv(t)
	runsSvc, _ := testEnv(t)
	ctx := context.Background()
	uniq := time.Now().UnixNano()
	provKey := fmt.Sprintf("itest_pending_%d", uniq)
	provID := seedAuthzProvider(t, env.db, provKey, "active")

	appA := seedAuthzFixture(t, env.db, authzFixture{
		slug: fmt.Sprintf("itest-pending-%d-a", uniq), kind: "chat",
		isPublic: true, enabled: true, provider: provKey, providerID: provID, resource: "agent_A",
	})
	appB := seedAuthzFixture(t, env.db, authzFixture{
		slug: fmt.Sprintf("itest-pending-%d-b", uniq), kind: "chat",
		isPublic: true, enabled: true, provider: provKey, providerID: provID, resource: "agent_B",
	})
	env.schd.Binding = &authzResolver{svc: &catalog.Service{DB: env.db}}

	scheduleID := seedPendingOccurrenceForApp(t, env, appA)
	// Concurrent PATCH: A → B, committed BEFORE the admission runs.
	if _, err := env.db.ExecContext(ctx,
		`UPDATE schedules SET application_id = ? WHERE id = ?`, appB, scheduleID); err != nil {
		t.Fatalf("patch schedule: %v", err)
	}

	env.schd.ProcessDue(ctx)

	var runIDBytes []byte
	if err := env.db.QueryRowContext(ctx,
		`SELECT run_id FROM schedule_occurrences WHERE schedule_id = ? AND status = 'queued'`,
		scheduleID).Scan(&runIDBytes); err != nil {
		t.Fatalf("pending occurrence was not admitted: %v", err)
	}
	var rid ids.ID
	if err := rid.Scan(runIDBytes); err != nil {
		t.Fatalf("run id: %v", err)
	}
	run, err := runsSvc.GetRun(ctx, rid)
	if err != nil {
		t.Fatalf("load admitted run: %v", err)
	}
	if run.ApplicationID == nil || *run.ApplicationID != appB {
		t.Fatalf("run.application_id=%v, want %d (B)", run.ApplicationID, appB)
	}
	if want := bindingIDOf(t, env.db, appB); run.RuntimeBindingID == nil || *run.RuntimeBindingID != want {
		t.Fatalf("run.runtime_binding_id=%v, want B's binding %d", run.RuntimeBindingID, want)
	}
	if got := run.SnapshotString("external_resource_id"); got != "agent_B" {
		t.Fatalf("run snapshot external_resource_id=%q, want agent_B (stale binding leaked)", got)
	}
}

// TestDisabledApplicationStopsPendingAdmission (评测 P1): disabling the
// application after the occurrence was queued must stop the admission — the
// authorization is re-run inside the schedule lock.
func TestDisabledApplicationStopsPendingAdmission(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()
	uniq := time.Now().UnixNano()
	provKey := fmt.Sprintf("itest_disab_%d", uniq)
	provID := seedAuthzProvider(t, env.db, provKey, "active")
	appID := seedAuthzFixture(t, env.db, authzFixture{
		slug: fmt.Sprintf("itest-disab-%d", uniq), kind: "chat",
		isPublic: true, enabled: true, provider: provKey, providerID: provID, resource: "agent_dis",
	})
	env.schd.Binding = &authzResolver{svc: &catalog.Service{DB: env.db}}

	scheduleID := seedPendingOccurrenceForApp(t, env, appID)
	if _, err := env.db.ExecContext(ctx, `UPDATE applications SET enabled = 0 WHERE id = ?`, appID); err != nil {
		t.Fatalf("disable application: %v", err)
	}

	env.schd.ProcessDue(ctx)

	var status string
	var runID sql.NullString
	if err := env.db.QueryRowContext(ctx,
		`SELECT status, run_id FROM schedule_occurrences WHERE schedule_id = ? ORDER BY id DESC LIMIT 1`,
		scheduleID).Scan(&status, &runID); err != nil {
		t.Fatalf("load occurrence: %v", err)
	}
	if status != schedule.OccFailed {
		t.Fatalf("occurrence status=%s, want failed", status)
	}
	if runID.Valid {
		t.Fatal("a disabled application must not admit a pending occurrence")
	}
}

// ── 评测 P1: conversation lifetime vs scheduled runs / deliveries ──

// TestDeleteConversationBlockedByScheduledRuns (评测 P1): a conversation
// holding a scheduler-created run must not be hard-deleted — that run is
// referenced by its occurrence and by the delivery worker (run.Output).
func TestDeleteConversationBlockedByScheduledRuns(t *testing.T) {
	runsSvc, _ := testEnv(t)
	ctx := context.Background()
	convID := seedConversation(t, runsSvc)
	runID := seedRunWithConversation(t, runsSvc, "itest_delguard", convID)

	// Terminal + scheduler-owned: the active-run guard must NOT be what
	// stops the delete — the scheduled-run lifetime guard is.
	if _, err := runsSvc.DB.ExecContext(ctx,
		`UPDATE runs SET trigger_type = 'scheduled', status = 'succeeded' WHERE id = ?`, runID.Bytes()); err != nil {
		t.Fatalf("tag scheduled: %v", err)
	}
	if err := runsSvc.DeleteConversationCascade(ctx, convID, 42); !errors.Is(err, execution.ErrConversationHasScheduledRuns) {
		t.Fatalf("delete with scheduled run err=%v, want ErrConversationHasScheduledRuns", err)
	}

	// Once the run is no longer scheduler-owned the delete succeeds and the
	// conversation really disappears.
	if _, err := runsSvc.DB.ExecContext(ctx,
		`UPDATE runs SET trigger_type = 'interactive_user' WHERE id = ?`, runID.Bytes()); err != nil {
		t.Fatalf("untag: %v", err)
	}
	if err := runsSvc.DeleteConversationCascade(ctx, convID, 42); err != nil {
		t.Fatalf("delete after clearing scheduled runs: %v", err)
	}
	var n int64
	if err := runsSvc.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversations WHERE id = ?`, convID).Scan(&n); err != nil {
		t.Fatalf("count conversations: %v", err)
	}
	if n != 0 {
		t.Fatalf("conversation still present after delete (n=%d)", n)
	}
}

// TestClearConversationResetsAgentThread (评测 P1-3): clearing removes the
// messages AND the agent thread, so the next turn starts a fresh provider
// session; an active run blocks the clear.
func TestClearConversationResetsAgentThread(t *testing.T) {
	runsSvc, _ := testEnv(t)
	ctx := context.Background()
	convID := seedConversation(t, runsSvc)

	if _, err := runsSvc.DB.ExecContext(ctx,
		`INSERT INTO messages (conversation_id, role, content) VALUES (?, 'user', 'hello')`, convID); err != nil {
		t.Fatalf("seed message: %v", err)
	}
	if _, err := runsSvc.DB.ExecContext(ctx,
		`INSERT INTO agent_threads (id, conversation_id, provider, remote_id, status, auth_mode, auth_subject_key)
		 VALUES (?, ?, 'feishu_aily', 'sess-1', 'active', 'user', '42')`, ids.New().Bytes(), convID); err != nil {
		t.Fatalf("seed thread: %v", err)
	}

	if err := runsSvc.ClearConversation(ctx, convID, 42); err != nil {
		t.Fatalf("clear: %v", err)
	}
	var messages, threads int64
	_ = runsSvc.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE conversation_id = ?`, convID).Scan(&messages)
	_ = runsSvc.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_threads WHERE conversation_id = ?`, convID).Scan(&threads)
	if messages != 0 || threads != 0 {
		t.Fatalf("after clear: messages=%d threads=%d, want 0/0", messages, threads)
	}

	// A live run blocks the clear (the in-flight answer must not land in an
	// emptied conversation).
	liveConv := seedConversation(t, runsSvc)
	_ = seedRunWithConversation(t, runsSvc, "itest_clearguard", liveConv)
	if err := runsSvc.ClearConversation(ctx, liveConv, 42); !errors.Is(err, execution.ErrConversationHasActiveRun) {
		t.Fatalf("clear with active run err=%v, want ErrConversationHasActiveRun", err)
	}
}

// ── real lock contention: tick vs run-now ──

// TestTickAndRunNowConcurrent (评测 §十二): real goroutines racing the scan
// tick against run-now for the SAME schedule must still leave at most one
// active occurrence — the earlier test proved the rule, this one creates
// the contention.
func TestTickAndRunNowConcurrent(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()
	scheduleID := env.seedSchedule(t, time.Now().UTC().Add(-time.Second), schedule.OverlapSkip)

	const workers = 10
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if i%2 == 0 {
				env.schd.ProcessDue(ctx)
				return
			}
			_, _ = env.schd.TriggerNow(ctx, scheduleID, 42, false)
		}(i)
	}
	close(start)
	wg.Wait()

	active := env.count(t, `SELECT COUNT(*) FROM schedule_occurrences
		WHERE schedule_id = ? AND status IN ('pending','queued','running')`, scheduleID)
	if active != 1 {
		t.Fatalf("active occurrences after concurrent tick/run-now=%d, want exactly 1", active)
	}
}
