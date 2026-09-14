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
// point shares: public+enabled+chat+binding for regular users; disabled
// applications are not executable by anyone.
func TestAuthorizeExecutionGates(t *testing.T) {
	env := newScheduleEnv(t)
	ctx := context.Background()
	svc := &catalog.Service{DB: env.db}
	uniq := time.Now().UnixNano()

	pub := seedAuthzApp(t, env.db, uniq, "pub", "chat", true, true, true)
	if _, err := svc.AuthorizeExecution(ctx, pub, 42, false); err != nil {
		t.Fatalf("public app must be executable by a regular user: %v", err)
	}

	priv := seedAuthzApp(t, env.db, uniq, "priv", "chat", false, true, true)
	if _, err := svc.AuthorizeExecution(ctx, priv, 42, false); !errors.Is(err, catalog.ErrExecutionForbidden) {
		t.Fatalf("private app: regular user err=%v, want ErrExecutionForbidden", err)
	}
	if _, err := svc.AuthorizeExecution(ctx, priv, 42, true); err != nil {
		t.Fatalf("private app: staff must be allowed: %v", err)
	}

	dis := seedAuthzApp(t, env.db, uniq, "dis", "chat", true, false, true)
	if _, err := svc.AuthorizeExecution(ctx, dis, 42, false); !errors.Is(err, catalog.ErrExecutionForbidden) {
		t.Fatalf("disabled app: regular user err=%v, want ErrExecutionForbidden", err)
	}
	if _, err := svc.AuthorizeExecution(ctx, dis, 42, true); !errors.Is(err, catalog.ErrExecutionDisabled) {
		t.Fatalf("disabled app: staff err=%v, want ErrExecutionDisabled (kill switch applies to everyone)", err)
	}

	nonChat := seedAuthzApp(t, env.db, uniq, "flow", "workflow", true, true, true)
	if _, err := svc.AuthorizeExecution(ctx, nonChat, 42, true); !errors.Is(err, catalog.ErrExecutionNotChat) {
		t.Fatalf("non-chat app err=%v, want ErrExecutionNotChat", err)
	}

	noBinding := seedAuthzApp(t, env.db, uniq, "nobind", "chat", true, true, false)
	if _, err := svc.AuthorizeExecution(ctx, noBinding, 42, true); err == nil {
		t.Fatal("app without an enabled binding must be rejected")
	}

	if _, err := svc.AuthorizeExecution(ctx, 1<<40, 42, true); !errors.Is(err, catalog.ErrExecutionNotFound) {
		t.Fatalf("unknown id err=%v, want ErrExecutionNotFound", err)
	}
}

// seedAuthzApp inserts an application plus (optionally) its enabled
// runtime binding. provider_id stays NULL so no provider fixture is
// needed — the authorization allows an absent provider row.
func seedAuthzApp(t *testing.T, d *sql.DB, uniq int64, tag, kind string, isPublic, enabled, withBinding bool) int64 {
	t.Helper()
	ctx := context.Background()
	slug := fmt.Sprintf("itest-authz-%d-%s", uniq, tag)
	res, err := d.ExecContext(ctx,
		`INSERT INTO applications (slug, name, kind, is_public, enabled) VALUES (?, ?, ?, ?, ?)`,
		slug, slug, kind, isPublic, enabled)
	if err != nil {
		t.Fatalf("seed application %s: %v", tag, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("application id: %v", err)
	}
	if withBinding {
		if _, err := d.ExecContext(ctx,
			`INSERT INTO runtime_bindings (application_id, provider_key, runtime_type, enabled) VALUES (?, ?, 'agent', 1)`,
			id, "itest_authz"); err != nil {
			t.Fatalf("seed binding %s: %v", tag, err)
		}
	}
	return id
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
