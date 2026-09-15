package http

// 第九轮 P1: an idempotent replay must outrank every admission limit the
// ORIGINAL request already consumed — including the per-user QPS budget.
//
// The race this closes is invisible in single-request tests: request A has
// passed admission and is inside its creation transaction (reservation not
// yet committed); request B carries the same client_request_id, resolves
// BEFORE that commit, and therefore walks into the QPS limiter, where it can
// be told 429 for a request that actually succeeded. The client's "retry"
// then creates a second turn.
//
// These tests pin tryServeIdempotentReplay — the layer that decides whether a
// rejected request gets the original run instead of its own error — without
// needing a database, so the ORDERING (and only the ordering) is what is
// under test. 第九轮补丁 3.2-C extended it: a same-key/different-payload
// conflict must surface as 409 (never 429), and a resolver infrastructure
// error must surface as 500 (never pretend the client is rate limited).

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
	"github.com/creation-agent-studio/backend-go/internal/platform/telemetry"
)

func replayServer() *Server {
	return &Server{Metric: telemetry.NewMetrics("test")}
}

func mustRunID(t *testing.T, s string) ids.ID {
	t.Helper()
	id, err := ids.Parse(s)
	if err != nil {
		t.Fatalf("parse id %q: %v", s, err)
	}
	return id
}

func TestServeReplayAfterRefusalReturnsTheOriginalRun(t *testing.T) {
	srv := replayServer()
	rec := httptest.NewRecorder()
	// The resolver stands in for the whole (bounded) wait: by the time it
	// answers, the winner's reservation has become visible. That waiting
	// lives in execution.ResolveRunRequestWithWait and is pinned against a
	// real uncommitted transaction there.
	ok := srv.tryServeIdempotentReplay(context.Background(), rec, "req-1",
		func(context.Context) (*execution.Run, bool, error) {
			return &execution.Run{ID: mustRunID(t, "8b6f0d1a0f0e4b3f9d2c5a7e1b4d6c80")}, true, nil
		})
	if !ok {
		t.Fatal("replay not served although the request identity resolved")
	}
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 (a replay creates nothing, so it is not 201)", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, rec.Body.String())
	}
	if body["client_request_id"] != "req-1" {
		t.Fatalf("client_request_id = %v, want req-1", body["client_request_id"])
	}
	if body["idempotency_replayed"] != true {
		t.Fatalf("idempotency_replayed = %v, want true (the client must be able to tell "+
			"a replay from a newly created run)", body["idempotency_replayed"])
	}
	if body["id"] != "8b6f0d1a-0f0e-4b3f-9d2c-5a7e1b4d6c80" {
		t.Fatalf("id = %v, want the ORIGINAL run, not a new one", body["id"])
	}
}

// A request that is genuinely new must NOT be answered here — the caller has
// to be free to write its 429.
func TestServeReplayAfterRefusalStaysSilentForANewRequest(t *testing.T) {
	srv := replayServer()
	rec := httptest.NewRecorder()
	ok := srv.tryServeIdempotentReplay(context.Background(), rec, "req-new",
		func(context.Context) (*execution.Run, bool, error) {
			return nil, false, nil // never resolved within the budget
		})
	if ok {
		t.Fatal("a NEW request was answered as a replay: every new request does get here " +
			"(the limiter refused it), and it must still see its 429")
	}
	if rec.Code != 200 {
		t.Fatalf("recorder default code = %d, want 200 (nothing must have been written)", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body = %q, want empty", rec.Body.String())
	}
}

// Without a client_request_id there is no identity to replay, and the
// admission path must remain untouched for ordinary requests.
func TestTryServeIdempotentReplayRequiresAClientRequestID(t *testing.T) {
	srv := replayServer()
	rec := httptest.NewRecorder()
	called := false
	ok := srv.tryServeIdempotentReplay(context.Background(), rec, "",
		func(context.Context) (*execution.Run, bool, error) {
			called = true
			return &execution.Run{}, true, nil
		})
	if ok || called {
		t.Fatalf("served=%v resolver-called=%v, want false/false "+
			"(an ordinary request must not pay a resolve round-trip)", ok, called)
	}
}

// 第九轮补丁 3.2-C 12.1: the wait resolver surfaced a SAME-ID/DIFFERENT-PAYLOAD
// conflict. Swallowing it into the caller's 429 mislabeled a client error as
// rate limiting AND hid the key reuse; the wire answer is 409.
func TestTryServeIdempotentReplaySurfacesAKeyConflictAs409(t *testing.T) {
	srv := replayServer()
	rec := httptest.NewRecorder()
	ok := srv.tryServeIdempotentReplay(context.Background(), rec, "req-dup",
		func(context.Context) (*execution.Run, bool, error) {
			return nil, false, execution.ErrIdempotencyKeyReused
		})
	if !ok {
		t.Fatal("the conflict was not handled: the caller would have written a 429")
	}
	if rec.Code != 409 {
		t.Fatalf("status = %d, want 409 — a key reuse is a conflict, never rate limiting", rec.Code)
	}
	if body := rec.Body.String(); body == "" || !json.Valid([]byte(body)) {
		t.Fatalf("body = %q, want a JSON error envelope", body)
	}
}

// 第九轮补丁 3.2-C 12.2: a resolver INFRASTRUCTURE error (DB outage) must be
// a 500, not the caller's 429 — an outage disguised as rate limiting sends
// the client into retry storms while the real problem is on our side.
func TestTryServeIdempotentReplaySurfacesAResolverErrorAs500(t *testing.T) {
	srv := replayServer()
	rec := httptest.NewRecorder()
	ok := srv.tryServeIdempotentReplay(context.Background(), rec, "req-err",
		func(context.Context) (*execution.Run, bool, error) {
			return nil, false, context.DeadlineExceeded
		})
	if !ok {
		t.Fatal("the resolver error was not handled: the caller would have written a 429")
	}
	if rec.Code != 500 {
		t.Fatalf("status = %d, want 500 — an infrastructure failure must not masquerade "+
			"as client-side rate limiting", rec.Code)
	}
}
