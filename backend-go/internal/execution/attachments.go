package execution

// Attachment bridge writes (第十一轮 P0-2 / P0-3).
//
// The worker plane needs exactly two attachment capabilities: read the
// attachments a claimed run owns, and persist the provider id that an
// upload produced. Both are canonical writes, so both go through the
// ownership fence and live behind WorkerOwnedService — a provider
// executor never touches *sql.DB (修复计划 §81).
//
// Why the fence matters here specifically:
//
//	A worker that lost its lease mid-run must not stamp an external id
//	onto an attachment row. The lease may already belong to a new owner
//	whose provider upload produced a DIFFERENT id; the fenced predicates
//	(id AND run_id AND empty external id) make the stale write a no-op
//	instead of a silent overwrite that would attach the wrong provider
//	object to the next chat.

import (
	"context"
	"errors"

	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
)

// ListClaimedAttachments returns the runtime_attachments rows this claimed
// run owns — the input to the worker's provider-upload bridge.
//
// Scope comes from the OWNERSHIP fence, never from a request value: the
// run id is taken from own.RunID, so a worker can only ever see the
// attachments of the run it actually holds.
func (s *Service) ListClaimedAttachments(ctx context.Context, own ExecutionOwnership) ([]db.RuntimeAttachment, error) {
	if !own.Valid() {
		return nil, ErrLostOwnership
	}
	return s.q(ctx).ListClaimedAttachmentsByRun(ctx, own.RunID.Bytes())
}

// MarkAttachmentUploadedOwned persists "this provider object belongs to
// this attachment" under the ownership fence (第十一轮 P0-3).
//
// Transaction shape (mirrors every other owned write in this package):
//
//	BEGIN
//	  lock runs row FOR UPDATE + verify epoch/lease token   → fence
//	  UPDATE runtime_attachments SET external_attachment_id, status='uploaded'
//	    WHERE id = ? AND run_id = ? AND external_attachment_id = ''
//	COMMIT
//
// The write is deliberately conditional on external_attachment_id = `”` so
// the column stays SET-ONCE: a second worker that re-uploads (or a retry
// that raced the first write) cannot replace an id the chat payload may
// already reference.
//
// RowsAffected == 0 means the attachment is not this run's, was deleted, or
// already carries an id. None of those is fatal — the caller re-reads the
// row and proceeds with whatever id is persisted — but they are reported so
// the caller can tell "already uploaded" from "nothing was written".
func (s *Service) MarkAttachmentUploadedOwned(ctx context.Context, own ExecutionOwnership, attachmentID ids.ID, externalID string) error {
	if externalID == "" {
		return errors.New("execution: attachment external id must not be empty")
	}
	if attachmentID.IsZero() {
		return errors.New("execution: attachment id must not be zero")
	}
	if !own.Valid() {
		return ErrLostOwnership
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := db.New(tx)

	// Fence check under the run row lock — before any write.
	if _, err := verifyActiveOwnershipTx(ctx, tx, own); err != nil {
		return err
	}

	res, err := q.MarkAttachmentUploadedFenced(ctx, db.MarkAttachmentUploadedFencedParams{
		ExternalAttachmentID: externalID,
		ID:                   attachmentID.Bytes(),
		RunID:                own.RunID.Bytes(),
	})
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		// Either a concurrent writer already recorded an id (fine — the
		// chat will reference that one) or the row is not ours (also
		// fine to ignore, but never silently: the caller logs it). The
		// transaction still commits so the fence check's lock is released
		// normally.
		if err := tx.Commit(); err != nil {
			return err
		}
		return ErrAttachmentNotClaimed
	}
	return tx.Commit()
}

// ErrAttachmentNotClaimed reports that a fenced attachment write matched no
// row: the attachment is not owned by this run, no longer exists, or already
// carries a provider id. It is NOT a lost lease — the caller may continue
// (after re-reading the row) — which is why it is a distinct sentinel rather
// than ErrLostOwnership.
var ErrAttachmentNotClaimed = errors.New("execution: attachment not claimed by this run")
