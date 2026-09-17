// 第十一轮 P0 regression tests — the attachment bridge (opt-in:
// STUDIO_TEST_DB=1 + STUDIO_TEST_REDIS=1).
//
// The bug being closed: the run input carried STUDIO attachment ids under
// the key "agent_attachment_ids", and the executor forwarded them to Aily
// verbatim as if they were provider ids. Aily had never seen those UUIDs,
// so the chat request died before producing a chat identity — and the
// exactly-once submit boundary then parked the run in waiting_external,
// which the user saw as "请求结果待确认，正在等待外部响应…" forever.
//
// These tests drive the REAL executor over a REAL claimed run with only the
// provider HTTP transport faked, and assert the two things that make the
// bug impossible to reintroduce:
//
//	§38 invariant  every id in the chat payload comes from
//	               runtime_attachments.external_attachment_id, never
//	               from runtime_attachments.id
//	items 7/8      an attachment that already has an external id is never
//	               uploaded again
//	items 5/6      an upload failure fails the RUN — it must never park
package integration

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/execution"
	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/integrations/aily"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
	"github.com/creation-agent-studio/backend-go/internal/platform/storage"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
)

// attachmentFixture extends the submit fixture with a staged attachment and
// a real local blob store, mirroring what UploadApplicationAttachment does:
// bytes in storage, a `pending` row, an EMPTY provider id.
type attachmentFixture struct {
	*ailySubmitFixture
	studioID string
	store    *storage.LocalFS
	key      string
}

// newAttachmentFixture builds a claimed run whose input pins one STUDIO
// attachment id, with the bytes actually present in storage.
func newAttachmentFixture(t *testing.T, suffix, mode, attachmentType, filename string) *attachmentFixture {
	t.Helper()
	f := newAilySubmitFixture(t, suffix, mode, "看看这张图片")
	ctx := context.Background()
	// Requeue so the fixture's run is back in `queued`: the production
	// attachment API (UploadRunAttachment → AppendUserAttachmentToRunInput)
	// only writes while the run is queued, and this fixture attaches AFTER
	// the run was claimed. The existing lease must go first — a run may hold
	// only one (uniq_lease_run) and the re-claim below installs its own.
	// Returning to queued reproduces the real ordering instead of bypassing
	// the guard.
	if _, err := f.env.db.ExecContext(ctx,
		`DELETE FROM run_leases WHERE run_id = ?`, f.runID.Bytes()); err != nil {
		t.Fatalf("release fixture lease: %v", err)
	}
	if _, err := f.env.db.ExecContext(ctx,
		`UPDATE runs SET status = 'queued' WHERE id = ?`, f.runID.Bytes()); err != nil {
		t.Fatalf("requeue run for attachment binding: %v", err)
	}

	// Real storage, real bytes: the bridge must genuinely read the object.
	root := t.TempDir()
	fs, err := storage.NewLocalFS(root)
	if err != nil {
		t.Fatalf("local storage: %v", err)
	}
	key := "attachments/42/" + suffix + "/" + filename
	if _, err := fs.Put(ctx, key, strings.NewReader("PNGDATA-"+suffix), "application/octet-stream"); err != nil {
		t.Fatalf("put attachment: %v", err)
	}
	f.exec.Storage = fs

	attID := ids.New()
	if _, err := f.env.db.ExecContext(ctx,
		`INSERT INTO runtime_attachments (id, run_id, conversation_id, provider,
		 external_attachment_id, attachment_type, name, source_type, source_url,
		 storage_key, content_type, size_bytes, auth_mode, auth_subject_key, status, metadata, created_by)
		 VALUES (?, ?, ?, 'feishu_aily', '', ?, ?, 'upload', '', ?, 'application/octet-stream', 12,
		         'user', '42', 'pending', NULL, 42)`,
		attID.Bytes(), f.runID.Bytes(), f.convID, attachmentType, filename, key); err != nil {
		t.Fatalf("insert attachment: %v", err)
	}
	// Pin the STUDIO id on the run input THROUGH THE REAL QUERY, exactly as
	// the production path does. Binding the run's input by hand in a test
	// once hid a live bug: the query's JSON_ARRAY_APPEND silently did
	// nothing when the key was absent, so the id never reached the run at
	// all and the bridge saw no attachments to upload.
	if _, err := f.svc.Querier().AppendUserAttachmentToRunInput(ctx,
		db.AppendUserAttachmentToRunInputParams{
			JSONARRAYAPPEND: attID.String(),
			ID:              f.runID.Bytes(),
		}); err != nil {
		t.Fatalf("pin studio attachment id: %v", err)
	}
	// Now claim it, as the worker would.
	reclaimed, won, err := f.svc.ClaimRun(ctx, f.runID, "itest-att-"+suffix, time.Minute)
	if err != nil || !won {
		t.Fatalf("claim run with attachment: won=%v err=%v", won, err)
	}
	f.claimed = reclaimed

	// The claimed snapshot must genuinely carry the id — assert here so a
	// regression in the write path fails with a clear message instead of
	// showing up as "0 uploads" three assertions later.
	if got := f.claimed.Run.StudioAttachmentIDs(); len(got) != 1 || got[0] != attID.String() {
		var raw string
		_ = f.env.db.QueryRow(`SELECT input FROM runs WHERE id = ?`, f.runID.Bytes()).Scan(&raw)
		t.Fatalf("run input does not carry the studio attachment id: StudioAttachmentIDs()=%v, input=%s",
			got, raw)
	}

	return &attachmentFixture{ailySubmitFixture: f, studioID: attID.String(), store: fs, key: key}
}

// externalAttachmentID reads what the bridge persisted.
func (a *attachmentFixture) externalAttachmentID(t *testing.T) string {
	t.Helper()
	var ext string
	if err := a.env.db.QueryRow(
		`SELECT external_attachment_id FROM runtime_attachments WHERE id = ?`,
		mustParseID(t, a.studioID).Bytes()).Scan(&ext); err != nil {
		t.Fatalf("read external attachment id: %v", err)
	}
	return ext
}

func mustParseID(t *testing.T, s string) ids.ID {
	t.Helper()
	id, err := ids.Parse(s)
	if err != nil {
		t.Fatalf("parse id %q: %v", s, err)
	}
	return id
}

// ── §38: the invariant itself ────────────────────────────────────────────

// TestChatNeverReceivesAStudioAttachmentID is the acceptance assertion of
// §38. The chat payload must reference the id that /attachments returned,
// and the studio id must appear nowhere on the wire.
func TestChatNeverReceivesAStudioAttachmentID(t *testing.T) {
	for _, mode := range []string{"background", "interactive"} {
		t.Run(mode, func(t *testing.T) {
			a := newAttachmentFixture(t, "r11inv"+mode, mode, "image", "shot.png")
			providerID := "3d058789-6952-4697-bf9c-1add1ebc206e"
			a.rec.uploadID = providerID

			if err := a.exec.Execute(context.Background(), a.claimed); err != nil {
				t.Fatalf("execute: %v", err)
			}

			// 1. The bytes were uploaded exactly once, and the provider id
			//    was persisted.
			if ups := a.rec.uploadsMade(); len(ups) != 1 {
				t.Fatalf("provider uploads = %d, want 1", len(ups))
			}
			if got := a.externalAttachmentID(t); got != providerID {
				t.Fatalf("persisted external_attachment_id = %q, want %q", got, providerID)
			}

			// 2. THE INVARIANT: the chat carried the provider id.
			got := a.rec.chatAttachmentIDs()
			if len(got) != 1 {
				t.Fatalf("chat attachment ids = %v, want exactly the provider id", got)
			}
			if got[0] != providerID {
				t.Fatalf("chat attachment id = %q, want the PROVIDER id %q", got[0], providerID)
			}

			// 3. ...and never the studio id.
			for _, id := range got {
				if id == a.studioID {
					t.Fatalf("chat payload leaked the STUDIO id %q — Aily has never seen this value", id)
				}
			}
		})
	}
}

// ── items 7/8: reuse instead of re-upload ────────────────────────────────

// TestAlreadyUploadedAttachmentIsNotUploadedAgain pins §37 item 7: a row
// that already carries an external id is reused verbatim.
func TestAlreadyUploadedAttachmentIsNotUploadedAgain(t *testing.T) {
	a := newAttachmentFixture(t, "r11reuse", "background", "image", "shot.png")
	existing := "3d058789-1111-4697-bf9c-aaaaaaaaaaaa"
	if _, err := a.env.db.ExecContext(context.Background(),
		`UPDATE runtime_attachments SET external_attachment_id = ?, status = 'uploaded' WHERE id = ?`,
		existing, mustParseID(t, a.studioID).Bytes()); err != nil {
		t.Fatalf("pre-mark uploaded: %v", err)
	}
	a.rec.uploadID = "should-never-be-used"

	if err := a.exec.Execute(context.Background(), a.claimed); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if ups := a.rec.uploadsMade(); len(ups) != 0 {
		t.Fatalf("re-uploaded an attachment that already had a provider id: %d uploads", len(ups))
	}
	if got := a.rec.chatAttachmentIDs(); len(got) != 1 || got[0] != existing {
		t.Fatalf("chat attachment ids = %v, want the persisted %q", got, existing)
	}
}

// TestReClaimDoesNotReUpload pins §37 item 8 end-to-end: a second claim of
// the same run (the shape of a worker restart / lease expiry) must find the
// id the first attempt persisted and upload nothing.
func TestReClaimDoesNotReUpload(t *testing.T) {
	a := newAttachmentFixture(t, "r11reclaim", "background", "image", "shot.png")
	providerID := "3d058789-2222-4697-bf9c-bbbbbbbbbbbb"
	a.rec.uploadID = providerID

	if err := a.exec.Execute(context.Background(), a.claimed); err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if ups := a.rec.uploadsMade(); len(ups) != 1 {
		t.Fatalf("first execute uploads = %d, want 1", len(ups))
	}
	if got := a.externalAttachmentID(t); got != providerID {
		t.Fatalf("persisted external id = %q, want %q", got, providerID)
	}

	// Simulate a re-claim of the same run by a fresh worker.
	ctx := context.Background()
	if _, err := a.env.db.ExecContext(ctx,
		`UPDATE runs SET status = 'queued' WHERE id = ?`, a.runID.Bytes()); err != nil {
		t.Fatalf("requeue run: %v", err)
	}
	reclaimed, won, err := a.svc.ClaimRun(ctx, a.runID, "itest-reclaim", time.Minute)
	if err != nil || !won {
		t.Fatalf("re-claim: won=%v err=%v", won, err)
	}
	second := &aily.Executor{
		Owned:       a.svc.WorkerOwned(),
		Adapter:     aily.NewAgentAdapterWithAPI(a.rec, nil),
		Auth:        a.exec.Auth,
		Storage:     a.store,
		ChatsL:      a.exec.ChatsL,
		PollsL:      a.exec.PollsL,
		PollBackoff: a.exec.PollBackoff,
		Log:         a.exec.Log,
		Metrics:     telemetry.NewMetrics("test"),
		Gate:        a.exec.Gate,
	}
	if err := second.Execute(ctx, reclaimed); err != nil {
		t.Fatalf("second execute: %v", err)
	}

	if ups := a.rec.uploadsMade(); len(ups) != 1 {
		t.Fatalf("re-claim uploaded again: total uploads = %d, want 1 (the provider object "+
			"would be duplicated)", len(ups))
	}
	if got := a.rec.chatAttachmentIDs(); got[0] != providerID {
		t.Fatalf("second chat attachment id = %q, want %q", got[0], providerID)
	}
}

// ── items 5/6: upload failure never parks the run ────────────────────────

// TestUploadFailureFailsTheRun is the direct regression for the reported
// symptom: before this batch, an attachment problem surfaced as
// waiting_external ("请求结果待确认…"). It must be a plain run failure with
// a code that names the attachment stage, because POST /chats was never
// sent and the provider therefore holds nothing.
func TestUploadFailureFailsTheRun(t *testing.T) {
	a := newAttachmentFixture(t, "r11fail", "background", "image", "shot.png")
	a.rec.uploadErr = &aily.APIError{Kind: aily.ErrClient, Msg: "param is invalid", HTTPStatus: 400}

	if err := a.exec.Execute(context.Background(), a.claimed); err != nil {
		t.Fatalf("execute: %v", err)
	}

	st := readRunState(t, a.env.db, a.runID)
	if st.status == "waiting_external" {
		t.Fatalf("upload failure parked the run in waiting_external — the exact bug this batch fixes")
	}
	if st.status != "failed" {
		t.Fatalf("status = %q, want failed", st.status)
	}
	if st.errorCode != "aily_attachment_upload_failed" {
		t.Fatalf("error_code = %q, want aily_attachment_upload_failed", st.errorCode)
	}
	// No chat may be attempted, and no submission may exist: the failure is
	// strictly before the submit boundary.
	if starts, opens := a.rec.counted(); starts != 0 || opens != 0 {
		t.Fatalf("provider chat was attempted after an upload failure: StartChat=%d OpenStreamChat=%d",
			starts, opens)
	}
	if n := a.env.count(t,
		`SELECT COUNT(*) FROM provider_submissions WHERE run_id = ?`, a.runID.Bytes()); n != 0 {
		t.Fatalf("upload failure created %d provider submission rows, want 0 — the upload is not "+
			"a chat submit and must never enter the exactly-once ledger", n)
	}
}

// TestUploadFailureLeavesAttachmentPending pins that a failed upload does
// NOT stamp an external id: the row must stay re-uploadable.
func TestUploadFailureLeavesAttachmentPending(t *testing.T) {
	a := newAttachmentFixture(t, "r11pend", "background", "image", "shot.png")
	a.rec.uploadErr = &aily.APIError{Kind: aily.ErrAuth, Msg: "bad token", HTTPStatus: 401}

	if err := a.exec.Execute(context.Background(), a.claimed); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := a.externalAttachmentID(t); got != "" {
		t.Fatalf("external_attachment_id = %q after a failed upload, want empty", got)
	}
	var status string
	if err := a.env.db.QueryRow(
		`SELECT status FROM runtime_attachments WHERE id = ?`,
		mustParseID(t, a.studioID).Bytes()).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "pending" {
		t.Fatalf("attachment status = %q, want pending (re-uploadable)", status)
	}
}

// TestTransientUploadFailureIsRetriedThenSucceeds pins §37 item 6: a 5xx
// upload is retried, and the retry's id is what the chat uses.
func TestTransientUploadFailureIsRetriedThenSucceeds(t *testing.T) {
	a := newAttachmentFixture(t, "r11retry", "background", "image", "shot.png")
	providerID := "3d058789-3333-4697-bf9c-cccccccccccc"
	a.rec.uploadID = providerID

	// The wrapper fails the FIRST /attachments call with a 5xx and delegates
	// everything else, so the retry path is exercised for real.
	fake := &oneShotUploadFailure{rec: a.rec}
	a.exec.Adapter = aily.NewAgentAdapterWithAPI(fake, nil)
	a.exec.Storage = a.store

	if err := a.exec.Execute(context.Background(), a.claimed); err != nil {
		t.Fatalf("execute: %v", err)
	}

	st := readRunState(t, a.env.db, a.runID)
	if st.status == "failed" {
		t.Fatalf("a retryable upload failure ended the run instead of retrying (error_code=%q)", st.errorCode)
	}
	if got := a.externalAttachmentID(t); got != providerID {
		t.Fatalf("persisted external id = %q, want %q", got, providerID)
	}
	if got := a.rec.chatAttachmentIDs(); len(got) != 1 || got[0] != providerID {
		t.Fatalf("chat attachment ids = %v, want [%s]", got, providerID)
	}
}

// oneShotUploadFailure delegates everything to the recorder but lets the
// FIRST /attachments call fail with a 5xx, exercising the retry path.
type oneShotUploadFailure struct {
	rec  *submitRecorder
	once bool
}

func (o *oneShotUploadFailure) StartChat(ctx context.Context, agentID, token string, content []map[string]any, attachmentIDs []string, session string) (string, string, error) {
	return o.rec.StartChat(ctx, agentID, token, content, attachmentIDs, session)
}

func (o *oneShotUploadFailure) OpenStreamChat(ctx context.Context, agentID, token string, content []map[string]any, attachmentIDs []string, session string) (io.ReadCloser, error) {
	return o.rec.OpenStreamChat(ctx, agentID, token, content, attachmentIDs, session)
}

func (o *oneShotUploadFailure) GetChatResult(ctx context.Context, agentID, token, chatID string) (json.RawMessage, error) {
	return o.rec.GetChatResult(ctx, agentID, token, chatID)
}

func (o *oneShotUploadFailure) UploadAttachment(ctx context.Context, agentID, token string, data []byte, filename, attachmentType, docURL string) (string, error) {
	if !o.once {
		o.once = true
		return "", &aily.APIError{Kind: aily.ErrServer, Msg: "bad gateway", HTTPStatus: 502}
	}
	return o.rec.UploadAttachment(ctx, agentID, token, data, filename, attachmentType, docURL)
}

func (o *oneShotUploadFailure) GetArtifact(ctx context.Context, agentID, token, artifactID string) (*aily.ArtifactDownload, error) {
	return o.rec.GetArtifact(ctx, agentID, token, artifactID)
}

func (o *oneShotUploadFailure) CheckVisibility(ctx context.Context, agentID, token string) (bool, error) {
	return o.rec.CheckVisibility(ctx, agentID, token)
}

// TestTextOnlyRunStillHasNoAttachmentField keeps the working path intact:
// a run with no attachments must not upload anything and must not grow an
// agent_attachment_ids field.
func TestTextOnlyRunStillHasNoAttachmentField(t *testing.T) {
	f := newAilySubmitFixture(t, "r11text", "background", "hello")
	f.exec.Storage = nil // must not be needed at all

	if err := f.exec.Execute(context.Background(), f.claimed); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if ups := f.rec.uploadsMade(); len(ups) != 0 {
		t.Fatalf("text-only run performed %d uploads, want 0", len(ups))
	}
	if got := f.rec.chatAttachmentIDs(); len(got) != 0 {
		t.Fatalf("text-only chat carried attachment ids %v, want none", got)
	}
	if st := readRunState(t, f.env.db, f.runID); st.status != "succeeded" {
		t.Fatalf("text-only run status = %q, want succeeded", st.status)
	}
}

var _ = execution.DefaultPriority
