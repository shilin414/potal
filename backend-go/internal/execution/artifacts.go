package execution

import (
	"context"

	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
)

// ArtifactInput describes one discovered provider artifact. The
// normalized type is provider-mapping output (the aily mapper), so the
// execution layer stays provider-agnostic.
type ArtifactInput struct {
	ExternalID     string
	Provider       string
	ProviderType   string
	Name           string
	NormalizedType string
}

// PersistArtifactOwned upserts a RunArtifact and emits artifact.discovered
// with the ACTUAL stored row id, all under the ownership fence and in ONE
// transaction (修复计划 §24-26, Phase 5):
//
//  1. verify ownership (run row locked FOR UPDATE, epoch checked)
//  2. UPSERT run_artifacts (unique run_id + external_artifact_id)
//  3. read the ACTUAL row back — its id is the stable local artifact id
//     across re-discovery (streaming + final reconciliation both see the
//     same id; the pre-generated id is never sent to clients)
//  4. INSERT artifact.discovered with the actual id
//     COMMIT
//
// A stale worker can neither write the artifact row nor emit its event
// (评测 §十一).
func (s *Service) PersistArtifactOwned(ctx context.Context, own ExecutionOwnership, art ArtifactInput) (ids.ID, error) {
	if art.ExternalID == "" {
		return ids.ID{}, nil
	}
	if !own.Valid() {
		return ids.ID{}, ErrLostOwnership
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ids.ID{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := db.New(tx)

	// 1. Fence check under the run row lock — before any write.
	if _, err := verifyActiveOwnershipTx(ctx, tx, own); err != nil {
		return ids.ID{}, err
	}

	// 2. Upsert (idempotent on run_id + external_artifact_id).
	candidate := ids.New()
	if err := q.UpsertRunArtifact(ctx, db.UpsertRunArtifactParams{
		ID:                   candidate.Bytes(),
		RunID:                own.RunID.Bytes(),
		Provider:             art.Provider,
		ExternalArtifactID:   art.ExternalID,
		ProviderArtifactType: art.ProviderType,
		Name:                 art.Name,
		NormalizedType:       art.NormalizedType,
	}); err != nil {
		return ids.ID{}, err
	}

	// 3. Read the actual row back: on a duplicate discovery the stored id
	// is the FIRST insert's id — never the freshly generated candidate
	// (评测 §十一 artifact ID bug).
	row, err := q.ListRunArtifactsByExternalID(ctx, db.ListRunArtifactsByExternalIDParams{
		RunID: own.RunID.Bytes(), ExternalArtifactID: art.ExternalID,
	})
	if err != nil {
		return ids.ID{}, err
	}
	storedID := ids.ID{}
	if err := storedID.Scan(row.ID); err != nil {
		return ids.ID{}, err
	}

	// 4. Discovery event carries the actual stored id.
	sequence, err := appendEventTx(ctx, tx, own.RunID, own.LeaseEpoch, EventArtifactDiscovered, map[string]any{
		"artifact_id":            storedID.String(),
		"external_artifact_id":   art.ExternalID,
		"provider_artifact_type": art.ProviderType,
		"name":                   art.Name,
	})
	if err != nil {
		return ids.ID{}, err
	}

	if err := tx.Commit(); err != nil {
		return ids.ID{}, err
	}
	s.publishLive(ctx, own.RunID, sequence, EventArtifactDiscovered, map[string]any{
		"artifact_id":            storedID.String(),
		"external_artifact_id":   art.ExternalID,
		"provider_artifact_type": art.ProviderType,
		"name":                   art.Name,
	})
	return storedID, nil
}

// BindProviderSessionOwned binds the provider session id onto the
// conversation's agent thread under the run ownership fence (修复计划
// §27-28, Phase 5). Semantics:
//
//	ownership lost (stale worker) → ErrLostOwnership — a stale worker can
//	                                  NEVER touch the session (评测 §十二)
//	remote_id empty                → bind (first writer wins)
//	remote_id == value             → idempotent success
//	remote_id != value, owner      → rebind: the CURRENT owner's provider
//	                                  state is canonical; the old session
//	                                  belongs to a dead attempt
func (s *Service) BindProviderSessionOwned(ctx context.Context, own ExecutionOwnership, threadID ids.ID, sessionID string) error {
	if sessionID == "" || threadID.IsZero() {
		return nil
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

	// Fence check under the run row lock — a stale worker must not even
	// touch the conversation's provider session.
	if _, err := verifyActiveOwnershipTx(ctx, tx, own); err != nil {
		return err
	}

	res, err := q.BindAgentThreadSessionOwned(ctx, db.BindAgentThreadSessionOwnedParams{
		RemoteID:   sessionID,
		ID:         threadID.Bytes(),
		RemoteID_2: sessionID,
	})
	if err != nil {
		return err
	}
	n, raErr := res.RowsAffected()
	if raErr != nil {
		return raErr
	}
	if n == 0 {
		// Distinguish idempotent re-bind (same session) from an owner
		// re-binding a NEW session (the old one belongs to a dead
		// attempt — e.g. the previous worker's lease expired mid-flight).
		row, gerr := q.GetAgentThreadByID(ctx, threadID.Bytes())
		if gerr != nil {
			return gerr
		}
		if row.RemoteID != sessionID {
			// Owner rebind: allowed. Non-owners never reach here — the
			// fence above already rejected them.
			if uerr := q.BindAgentThreadSession(ctx, db.BindAgentThreadSessionParams{
				RemoteID: sessionID, ID: threadID.Bytes(),
			}); uerr != nil {
				return uerr
			}
		}
		// Same value: MySQL reports 0 changed rows — idempotent success.
	}
	return tx.Commit()
}
