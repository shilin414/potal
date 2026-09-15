package execution

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"sort"
	"time"

	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
)

// ErrIdempotencyKeyReused marks a submit that reuses a client_request_id with
// a DIFFERENT payload (第九轮 P0-1). It is a client error (HTTP 409), never a
// retry: the identity the caller chose is already bound to another request.
var ErrIdempotencyKeyReused = errors.New("execution: client_request_id already used with a different payload")

// errRunRequestDuplicate is the INTERNAL signal that the reservation could
// not be taken inside the run-creation transaction. It never reaches a
// caller: CreateRunIdempotent converts it into a replay of the winner's run
// (or into ErrIdempotencyKeyReused when the hash disagrees).
var errRunRequestDuplicate = errors.New("execution: run request already reserved")

// MaxClientRequestIDLen mirrors run_requests.client_request_id VARCHAR(64).
// An over-long id is rejected rather than silently truncated: truncation
// would fuse two distinct request identities into one.
const MaxClientRequestIDLen = 64

// RunRequestHash is the canonical identity of a submit request: SHA-256 over
// the NORMALIZED payload, so a transport retry of the same user action hashes
// identically and a genuinely different request does not.
//
// Normalization rules:
//   - application_id / conversation_id are hashed as fixed-width integers;
//     conversationID == 0 means "lazy" (the conversation does not exist yet),
//     which is exactly what the first attempt of a new conversation sends.
//   - content is hashed verbatim.
//   - attachment ids are treated as a SET: deduplicated and sorted. The wire
//     order of attachment_ids carries no meaning, so reordering them in a
//     retry must replay instead of raising a spurious 409.
//
// Every field is length-prefixed before it is hashed. Without that, the
// concatenation of ("ab", "c") and ("a", "bc") would collide, and two
// different requests could be accepted as each other's replay.
func RunRequestHash(applicationID, conversationID int64, content string, attachmentIDs []string) []byte {
	h := sha256.New()
	hashInt64(h, applicationID)
	hashInt64(h, conversationID)
	hashString(h, content)
	atts := normalizeAttachmentIDs(attachmentIDs)
	hashInt64(h, int64(len(atts)))
	for _, a := range atts {
		hashString(h, a)
	}
	return h.Sum(nil)
}

// normalizeAttachmentIDs returns the attachment ids as a deduplicated,
// sorted set. Empty ids are dropped: an absent attachment cannot
// distinguish two requests.
func normalizeAttachmentIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func hashString(h hash.Hash, s string) {
	hashInt64(h, int64(len(s)))
	_, _ = h.Write([]byte(s))
}

func hashInt64(h hash.Hash, v int64) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(v))
	_, _ = h.Write(buf[:])
}

// ResolveRunRequest answers "has this client request already been served?"
// (第九轮 P0-1). Three outcomes:
//
//	(run, true, nil)                    the SAME request was already served →
//	                                    return the original run (idempotent
//	                                    replay, HTTP 200)
//	(nil, false, ErrIdempotencyKeyReused) the id is bound to another payload
//	(nil, false, nil)                   the request is new
//
// It is deliberately a PRE-TRANSACTION read so the handler can answer a
// replay BEFORE authorization, rate limiting and attachment validation:
// every one of those would otherwise misreport a legitimate replay — the
// attachments of the first attempt are already claimed by its run, and the
// first attempt's own run counts against the per-user outstanding cap, so a
// retry could be rejected with 400/409/429 for a request that SUCCEEDED.
func (s *Service) ResolveRunRequest(ctx context.Context, userID int64, clientRequestID string, requestHash []byte) (*Run, bool, error) {
	if clientRequestID == "" {
		return nil, false, nil
	}
	row, err := s.q(ctx).GetRunRequest(ctx, db.GetRunRequestParams{
		UserID:          uint64(userID),
		ClientRequestID: clientRequestID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !bytes.Equal(row.RequestHash, requestHash) {
		return nil, false, ErrIdempotencyKeyReused
	}
	run, err := s.GetRun(ctx, mustID(row.RunID))
	if err != nil {
		return nil, false, err
	}
	return run, true, nil
}

// ResolveRunRequestWait bounds how long a caller may wait for an in-flight
// duplicate to become visible (第九轮复审 P1).
//
// The window it exists for: request A has passed admission and is inside its
// creation transaction, so its reservation is not committed yet. Request B
// (the same client_request_id) resolves BEFORE that commit, sees nothing, and
// walks on into the per-user QPS limiter — where it can be rejected with 429
// for a request that actually SUCCEEDED. A single re-read is not enough
// either, because A may still be a few milliseconds from committing.
//
// The wait is bounded and short by design: it only has to cover a commit, not
// a slow request. A caller whose own request really is new pays the whole
// budget before being told 429 — which is why the budget is a few hundred
// milliseconds and not a second.
const (
	DefaultResolveRunRequestWait = 400 * time.Millisecond
	resolveRunRequestPollEvery   = 20 * time.Millisecond
)

// ResolveRunRequestWithWait polls ResolveRunRequest until the identity
// appears or the budget runs out. It answers the same three ways as
// ResolveRunRequest; (nil, false, nil) simply means "still new after waiting".
//
// A losing duplicate uses it AFTER an admission refusal, so the required
// semantics hold:
//
//	same id + same payload + winner eventually commits → the ORIGINAL run
//	same id + winner rolled back                      → continue as a new
//	                                                    request (and 429 is
//	                                                    then the right answer)
func (s *Service) ResolveRunRequestWithWait(ctx context.Context, userID int64, clientRequestID string, requestHash []byte, wait time.Duration) (*Run, bool, error) {
	if clientRequestID == "" {
		return nil, false, nil
	}
	deadline := time.Now().Add(wait)
	for {
		run, found, err := s.ResolveRunRequest(ctx, userID, clientRequestID, requestHash)
		if err != nil || found {
			return run, found, err
		}
		if !time.Now().Before(deadline) {
			return nil, false, nil
		}
		select {
		case <-ctx.Done():
			// The client is gone; there is no answer to give.
			return nil, false, ctx.Err()
		case <-time.After(resolveRunRequestPollEvery):
		}
	}
}

// CreateRunIdempotent is the submit path for requests that carry a
// client_request_id. It returns replayed=true when the returned Run is the
// ORIGINAL run of an identical earlier request, in which case nothing new was
// written — no conversation, no user message, no run, no outbox row.
//
// The three steps are:
//
//  1. resolve BEFORE admission — a replay must not be turned into 429/409 by
//     limits that the original request already consumed;
//  2. create under the ordinary admission path, whose transaction also TAKES
//     the reservation, so "reserved" and "run exists" commit together;
//  3. resolve AGAIN after any failure — the loser of a concurrent duplicate
//     loses the reservation race (and may also have lost the user-row or
//     conversation admission race to its own original), and must report the
//     winner's run rather than that failure.
func (s *Service) CreateRunIdempotent(ctx context.Context, in *CreateRunInput, maxOutstanding int) (*Run, bool, error) {
	if in.ClientRequestID == "" {
		run, err := s.CreateRunAdmitted(ctx, in, maxOutstanding)
		return run, false, err
	}
	if run, found, err := s.ResolveRunRequest(ctx, in.UserID, in.ClientRequestID, in.RequestHash); err != nil || found {
		return run, found, err
	}

	run, err := s.CreateRunAdmitted(ctx, in, maxOutstanding)
	if err == nil {
		return run, false, nil
	}

	// The failure may be this request's own earlier outcome arriving late:
	// the winner commits while the loser is blocked on the user-row or
	// conversation lock, so the loser sees the winner's run in the
	// outstanding count / active-run count and fails admission. A committed
	// reservation is proof that this request was already served.
	replay, found, rerr := s.ResolveRunRequest(ctx, in.UserID, in.ClientRequestID, in.RequestHash)
	if rerr != nil {
		return nil, false, rerr
	}
	if found {
		return replay, true, nil
	}
	return nil, false, err
}

// reserveRunRequestTx takes the client request identity inside the run
// creation transaction. Callers must treat errRunRequestDuplicate as "someone
// else owns this identity" and resolve it, never as a raw failure.
func reserveRunRequestTx(ctx context.Context, q db.Querier, in *CreateRunInput, runID ids.ID) error {
	if in.ClientRequestID == "" {
		return nil
	}
	res, err := q.ReserveRunRequest(ctx, db.ReserveRunRequestParams{
		UserID:          uint64(in.UserID),
		ClientRequestID: in.ClientRequestID,
		RunID:           runID.Bytes(),
		RequestHash:     in.RequestHash,
	})
	if err != nil {
		return fmt.Errorf("reserve run request: %w", err)
	}
	// 1 = the identity was free and is now ours; 0 = it already exists (see
	// ReserveRunRequest). There is no third outcome: the statement updates
	// the row to its own value, so MySQL can never report 2.
	if n, _ := res.RowsAffected(); n != 1 {
		return errRunRequestDuplicate
	}
	return nil
}
