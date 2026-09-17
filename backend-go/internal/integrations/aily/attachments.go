package aily

// Worker attachment bridge (第十一轮 P0-2).
//
// The browser-side upload endpoint only STAGES a file: it writes the bytes
// to studio storage and creates a `pending` runtime_attachments row whose
// external id is empty. Aily cannot be handed that row's id — it has never
// seen it. The provider id (`agent_attachment_id`) only exists after the
// bytes are POSTed to
//
//	POST /aily/v1/agents/:agent_id/attachments
//
// so that call has to happen HERE, on the worker plane, between "the run
// was claimed" and "the chat is submitted":
//
//	studio AttachmentIDs (runtime_attachments.id)
//	  → load rows, verify they belong to THIS run + provider
//	  → reuse a non-empty external_attachment_id (crash / re-claim)
//	  → else Storage.Open(storage_key) → Adapter.UploadAttachment
//	  → persist external_attachment_id (fenced, set-once)
//	  → return the provider ids, which are what the chat payload carries
//
// ORDERING IS LOAD-BEARING. This runs BEFORE beginSubmit, because
// beginSubmit is what marks the submission 'sending' — i.e. "a chat may
// now exist upstream". If the upload happened after it, a failed upload
// would leave a submission recorded as possibly-on-the-wire for a chat
// that was never POSTed, and the next attempt would park the run in
// waiting_external for no reason. An upload failure is therefore a plain
// run failure (error_code=aily_attachment_upload_failed), never an unknown
// provider outcome.
//
// The upload API is also a SEPARATE external action from POST /chats and
// must never be recorded in provider_submissions: that ledger tracks the
// chat submit, which is the only action with a duplicate-execution risk.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/catalog"
	"github.com/creation-agent-studio/backend-go/internal/execution"
	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
)

// ErrAttachmentUpload marks an attachment-preparation failure: the bytes
// never reached (or were refused by) the provider, so NO chat was created
// and the run may be failed outright. It is deliberately distinct from a
// provider submit failure, whose outcome is unknown.
var ErrAttachmentUpload = errors.New("aily: attachment preparation failed")

// attachmentUploadWaits is the bounded local retry budget for a SINGLE
// upload (第十一轮 §26). Retrying an upload is safe — the worst case is an
// orphaned duplicate attachment on the provider, never a duplicate agent
// conversation — so it gets a small budget where the chat submit gets none.
// Only retryable classes are retried (5xx / timeout / 429); a 4xx or auth
// refusal is definitive and stops immediately.
var attachmentUploadWaits = []time.Duration{0, 300 * time.Millisecond, 900 * time.Millisecond}

// preparedAttachments is the bridge result: the provider ids the chat must
// reference, in the order the run pinned them.
type preparedAttachments struct {
	// ExternalIDs are REAL Aily agent_attachment_id values.
	ExternalIDs []string
	// Count is how many studio attachments the run pinned (for logging).
	Count int
}

// prepareAttachments resolves the run's STUDIO attachment ids into real
// Aily agent_attachment_id values, uploading whatever has not been uploaded
// yet. It is the ONLY place a provider attachment id is minted on the worker
// plane.
//
// Failure modes and their meaning:
//
//	ErrLostOwnership              → the fence is gone; stop, write nothing
//	ErrAttachmentUpload (wrapped) → definitive preparation failure; the
//	                                caller fails the run, never parks it
//
// A missing local file is treated as a definitive failure too: the studio
// cannot produce the bytes, so no amount of provider retrying helps.
func (e *Executor) prepareAttachments(
	ctx context.Context,
	claimed *execution.ClaimedRun,
	auth *catalog.ProviderAuthContext,
	agentID string,
) (*preparedAttachments, error) {
	studioIDs := claimed.Run.StudioAttachmentIDs()
	out := &preparedAttachments{ExternalIDs: make([]string, 0, len(studioIDs)), Count: len(studioIDs)}
	if len(studioIDs) == 0 {
		return out, nil
	}
	if e.Storage == nil {
		// A deployment that can carry attachments must wire Storage; a nil
		// one means the run would silently lose its files. Fail loudly.
		e.Log.Error("attachment bridge has no storage configured",
			"run_id", claimed.Run.ID.String(), "attachments", len(studioIDs))
		return nil, fmt.Errorf("%w: storage is not configured", ErrAttachmentUpload)
	}

	rows, err := e.Owned.ListClaimedAttachments(ctx, claimed)
	if err != nil {
		if errors.Is(err, execution.ErrLostOwnership) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: load attachments: %v", ErrAttachmentUpload, err)
	}
	byID := make(map[string]int, len(rows))
	for i := range rows {
		id := ids.ID{}
		if err := id.Scan(rows[i].ID); err != nil {
			continue
		}
		byID[id.String()] = i
	}

	for _, studioID := range studioIDs {
		// Ownership scope: the id must resolve to a row THIS run owns and
		// this provider serves. Anything else is a tampered/stale run input
		// and is rejected rather than uploaded.
		idx, ok := byID[studioID]
		if !ok {
			return nil, fmt.Errorf("%w: attachment %s is not owned by this run", ErrAttachmentUpload, studioID)
		}
		row := rows[idx]
		if row.Provider != "" && row.Provider != ProviderKey {
			return nil, fmt.Errorf("%w: attachment %s belongs to provider %s", ErrAttachmentUpload, studioID, row.Provider)
		}

		// Reuse: a previous attempt (or a re-claim after a crash) may already
		// have uploaded this attachment. The row is the source of truth, so
		// the provider object is never duplicated.
		if ext := strings.TrimSpace(row.ExternalAttachmentID); ext != "" {
			out.ExternalIDs = append(out.ExternalIDs, ext)
			continue
		}

		externalID, err := e.uploadStudioAttachment(ctx, claimed, auth, agentID, studioID, row)
		if err != nil {
			return nil, err
		}
		out.ExternalIDs = append(out.ExternalIDs, externalID)
	}
	// Aily caps a chat at 8 attachment ids (adapter validation).
	if err := ValidateAttachments(out.ExternalIDs); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAttachmentUpload, err)
	}
	e.Log.Info("attachments prepared for provider submit",
		"run_id", claimed.Run.ID.String(), "attachments", len(out.ExternalIDs))
	return out, nil
}

// uploadStudioAttachment uploads ONE staged attachment and persists the
// provider id under the ownership fence.
func (e *Executor) uploadStudioAttachment(
	ctx context.Context,
	claimed *execution.ClaimedRun,
	auth *catalog.ProviderAuthContext,
	agentID string,
	studioID string,
	row db.RuntimeAttachment,
) (string, error) {
	if strings.TrimSpace(row.StorageKey) == "" {
		// doc_url attachments carry no bytes; they are referenced by URL
		// rather than uploaded. Without a storage key we cannot produce a
		// file body, so this is a capability failure, not a retry.
		return "", fmt.Errorf("%w: attachment %s has no stored file", ErrAttachmentUpload, studioID)
	}

	f, _, err := e.Storage.Open(ctx, row.StorageKey)
	if err != nil {
		return "", fmt.Errorf("%w: open %s: %v", ErrAttachmentUpload, row.StorageKey, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxAttachmentBytes+1))
	if err != nil {
		return "", fmt.Errorf("%w: read %s: %v", ErrAttachmentUpload, row.StorageKey, err)
	}
	if int64(len(data)) > maxAttachmentBytes {
		return "", fmt.Errorf("%w: attachment %s exceeds %d bytes",
			ErrAttachmentUpload, studioID, maxAttachmentBytes)
	}

	attachmentType := row.AttachmentType
	if attachmentType != "image" && attachmentType != "file" {
		// Only image/file carry bytes to /attachments; feishu_doc/bitable
		// are doc_url references the worker cannot upload.
		return "", fmt.Errorf("%w: attachment %s has unsupported type %q",
			ErrAttachmentUpload, studioID, attachmentType)
	}
	if len(data) == 0 {
		return "", fmt.Errorf("%w: attachment %s is empty", ErrAttachmentUpload, studioID)
	}

	var (
		externalID string
		lastErr    error
	)
	for i, wait := range attachmentUploadWaits {
		if wait > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(wait):
			}
		}
		externalID, lastErr = e.Adapter.UploadAttachment(ctx, auth, agentID, &catalog.AttachmentInput{
			Data:           data,
			Filename:       row.Name,
			ContentType:    row.ContentType,
			AttachmentType: attachmentType,
		})
		if lastErr == nil && externalID != "" {
			break
		}
		// Retry only what can be retried: a definitive refusal (4xx / auth)
		// or a local capability rejection never becomes true on a retry.
		if !retryableUploadFailure(lastErr) {
			break
		}
		if i < len(attachmentUploadWaits)-1 {
			e.Log.Warn("attachment upload failed; retrying",
				"run_id", claimed.Run.ID.String(), "attachment_id", studioID,
				"attempt", i+1, "err", lastErr)
		}
	}
	if lastErr != nil {
		return "", fmt.Errorf("%w: %s: %v", ErrAttachmentUpload, studioID, lastErr)
	}
	if strings.TrimSpace(externalID) == "" {
		// The client already refuses an empty id, so this is defense in
		// depth: never persist, never reference, an empty provider id.
		return "", fmt.Errorf("%w: %s: provider returned an empty attachment id",
			ErrAttachmentUpload, studioID)
	}

	attID, err := ids.Parse(studioID)
	if err != nil {
		return "", fmt.Errorf("%w: invalid attachment id %s", ErrAttachmentUpload, studioID)
	}
	if err := e.Owned.MarkAttachmentUploaded(ctx, claimed, attID, externalID); err != nil {
		if errors.Is(err, execution.ErrLostOwnership) {
			return "", err
		}
		if errors.Is(err, execution.ErrAttachmentNotClaimed) {
			// The row was already stamped by a concurrent writer or is no
			// longer ours. Re-reading is the honest move: the chat must
			// reference the id the DATABASE holds, not the one this upload
			// just produced (they can differ if an earlier attempt's upload
			// landed after this one started).
			if persisted := e.persistedExternalID(ctx, claimed, studioID); persisted != "" {
				e.Log.Warn("attachment already carried an external id; reusing the persisted one",
					"run_id", claimed.Run.ID.String(), "attachment_id", studioID)
				return persisted, nil
			}
		}
		return "", fmt.Errorf("%w: persist %s: %v", ErrAttachmentUpload, studioID, err)
	}
	return externalID, nil
}

// persistedExternalID re-reads an attachment's provider id. Returns "" when
// the row is gone or still has none.
func (e *Executor) persistedExternalID(ctx context.Context, claimed *execution.ClaimedRun, studioID string) string {
	rows, err := e.Owned.ListClaimedAttachments(ctx, claimed)
	if err != nil {
		return ""
	}
	for i := range rows {
		id := ids.ID{}
		if err := id.Scan(rows[i].ID); err != nil {
			continue
		}
		if id.String() == studioID {
			return strings.TrimSpace(rows[i].ExternalAttachmentID)
		}
	}
	return ""
}

// retryableUploadFailure reports whether an upload failure is worth another
// attempt. Only classes that mean "the provider produced no id and might
// succeed later" qualify: 5xx, timeout and rate limits. A 4xx or an auth
// refusal is the provider's considered answer, and a local capability error
// (bad type, too large) is ours — neither changes on a retry.
//
// NOTE this is deliberately WIDER than APIError.Retryable(), which exists
// for the CHAT submit and therefore excludes timeouts: a timed-out chat may
// already have been accepted upstream, so retrying it could duplicate an
// agent execution. An upload has no such risk — the worst case is an
// orphaned duplicate attachment — so a timeout belongs on the retry list
// here and nowhere else.
func retryableUploadFailure(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Retryable() || errors.Is(apiErr.Kind, ErrTimeout)
	}
	// Unknown error kinds are treated as transport-level: no id was
	// produced, so a retry cannot duplicate anything. Duplicating a provider
	// ATTACHMENT is harmless — duplicating a CHAT is not, which is why the
	// chat submit keeps the opposite default.
	return !errors.Is(err, ErrCapability)
}

// maxAttachmentBytes mirrors the adapter's file ceiling (40MB) with headroom
// for the multipart envelope, so a mis-declared row cannot stream an
// unbounded object into memory.
const maxAttachmentBytes = 41 << 20
