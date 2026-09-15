package aily

import (
	"errors"
	"testing"

	"github.com/creation-agent-studio/backend-go/internal/catalog"
	"github.com/creation-agent-studio/backend-go/internal/execution"
)

// TestClassifySubmitFailure is the table that pins the ONE safety decision at
// the provider submit boundary (第九轮 P0-2).
//
// The property under test is asymmetric and easy to get backwards:
//
//	missing a park  → a duplicate provider chat (a real external side effect)
//	an extra park   → a run that had to be resolved by hand
//
// so every case that is not a definitive answer from the provider must PARK
// for a provider that cannot deduplicate a resend.
func TestClassifySubmitFailure(t *testing.T) {
	timeout := &APIError{Kind: ErrTimeout, Msg: "context deadline exceeded"}
	server5xx := &APIError{Kind: ErrServer, Msg: "boom", HTTPStatus: 502}
	rateLimited := &APIError{Kind: ErrRateLimit, Msg: "slow down", HTTPStatus: 429}
	authFailed := &APIError{Kind: ErrAuth, Msg: "bad token", HTTPStatus: 401}
	badRequest := &APIError{Kind: ErrClient, Msg: "invalid content", HTTPStatus: 400}
	transport := errors.New("connection reset by peer")

	cases := []struct {
		name       string
		err        error
		class      catalog.IdempotencyClass
		wantPark   bool
		wantRecord string
	}{
		// ── no way to deduplicate a resend: only a refusal may retry ──
		{"timeout parks", timeout, catalog.IdempotencyNone, true, execution.SubmissionUnknown},
		{"5xx parks", server5xx, catalog.IdempotencyNone, true, execution.SubmissionUnknown},
		{"transport error parks", transport, catalog.IdempotencyNone, true, execution.SubmissionUnknown},
		{"429 keeps the retry policy", rateLimited, catalog.IdempotencyNone, false, execution.SubmissionRejected},
		{"401 keeps the failure policy", authFailed, catalog.IdempotencyNone, false, execution.SubmissionRejected},
		{"400 keeps the failure policy", badRequest, catalog.IdempotencyNone, false, execution.SubmissionRejected},

		// ── the provider collapses a resend on the stable key ──
		{"native: timeout may retry", timeout, catalog.IdempotencyNative, false, execution.SubmissionUnknown},
		{"native: 5xx may retry", server5xx, catalog.IdempotencyNative, false, execution.SubmissionUnknown},
		// A refusal is still recorded as a refusal: the state machine must
		// not lose the fact that the provider answered.
		{"native: 400 still records the refusal", badRequest, catalog.IdempotencyNative, false, execution.SubmissionRejected},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifySubmitFailure(tc.err, tc.class)
			if got.Park != tc.wantPark {
				t.Fatalf("Park = %v, want %v (a non-definitive outcome that is retried "+
					"creates a second provider chat)", got.Park, tc.wantPark)
			}
			if got.RecordOutcome != tc.wantRecord {
				t.Fatalf("RecordOutcome = %q, want %q", got.RecordOutcome, tc.wantRecord)
			}
			if got.Reason == "" {
				t.Fatal("Reason must be populated: it goes into the run event and the logs")
			}
		})
	}
}

// TestClassifySubmitFailureNeverResendsForTheWeakestClass states the safety
// property directly, over EVERY error shape the client can produce, so a new
// error kind added later cannot silently default to "retry".
func TestClassifySubmitFailureNeverResendsForTheWeakestClass(t *testing.T) {
	all := []error{
		&APIError{Kind: ErrTimeout},
		&APIError{Kind: ErrServer},
		&APIError{Kind: ErrRateLimit},
		&APIError{Kind: ErrAuth},
		&APIError{Kind: ErrClient},
		&APIError{Kind: ErrCapability},
		&APIError{}, // unclassified provider answer
		errors.New("dial tcp: refused"),
	}
	for _, err := range all {
		d := classifySubmitFailure(err, catalog.IdempotencyNone)
		// Either the run is parked, or the submission is recorded as
		// definitively refused — i.e. nothing here leaves a resend possible
		// without the provider having said "no".
		if !d.Park && d.RecordOutcome != execution.SubmissionRejected {
			t.Fatalf("%v (kind=%v) would resend without a definitive refusal "+
				"(park=%v record=%q)", err, err, d.Park, d.RecordOutcome)
		}
	}
}

// TestAilyAdapterDeclaresTheWeakestIdempotencyClass: the Aily chat API has no
// idempotency key and no lookup-by-request-key, so a resend after an ambiguous
// outcome cannot be deduplicated. Declaring anything stronger here would turn
// the park above into a blind retry.
func TestAilyAdapterDeclaresTheWeakestIdempotencyClass(t *testing.T) {
	a := &AgentAdapter{}
	if got := a.SubmitIdempotency(); got != catalog.IdempotencyNone {
		t.Fatalf("SubmitIdempotency() = %q, want %q", got, catalog.IdempotencyNone)
	}
	var adapter catalog.RuntimeAdapter = a
	if got := catalog.SubmitIdempotencyOf(adapter); got != catalog.IdempotencyNone {
		t.Fatalf("SubmitIdempotencyOf(adapter) = %q, want %q", got, catalog.IdempotencyNone)
	}
}

// TestSubmitIdempotencyOfIsFailClosed: an adapter that does not implement the
// optional interface must inherit the SAFE policy, so a provider added later
// cannot silently get "resend is fine".
func TestSubmitIdempotencyOfIsFailClosed(t *testing.T) {
	if got := catalog.SubmitIdempotencyOf(nil); got != catalog.IdempotencyNone {
		t.Fatalf("SubmitIdempotencyOf(nil) = %q, want %q", got, catalog.IdempotencyNone)
	}
}

// TestDeltaChunkIsIncremental is the 第九轮 P1-4 guard: a durable content.chunk
// must carry only the NEW text and its end offset.
//
// Carrying the cumulative answer on every chunk made a run's event payload
// volume quadratic in the output length (a 1 MB answer wrote ~250 MB of
// snapshots), which is invisible in small tests and only shows up on long
// answers — exactly the case this assertion pins.
func TestDeltaChunkIsIncremental(t *testing.T) {
	c := newDeltaCoalescer()
	for _, part := range []string{"alpha", "beta", "gamma"} {
		c.buf = append(c.buf, part...)
	}
	payload, ok := c.chunk()
	if !ok {
		t.Fatal("a non-empty buffer must produce a chunk")
	}
	if _, hasSnapshot := payload["snapshot"]; hasSnapshot {
		t.Fatal("content.chunk carries a cumulative snapshot: event storage becomes " +
			"quadratic in the answer length (P1-4)")
	}
	if len(payload) != 2 {
		t.Fatalf("chunk payload keys = %v, want exactly text + offset", payload)
	}
	if got := payload["text"]; got != "alphabetagamma" {
		t.Fatalf("text = %v, want the incremental buffer", got)
	}
	if got := payload["offset"]; got != len("alphabetagamma") {
		t.Fatalf("offset = %v, want %d (bytes emitted so far)", got, len("alphabetagamma"))
	}
	// A second chunk continues from the first: incremental, never cumulative.
	c.buf = append(c.buf, "delta"...)
	next, ok := c.chunk()
	if !ok {
		t.Fatal("second chunk missing")
	}
	if got := next["text"]; got != "delta" {
		t.Fatalf("second text = %v, want only the new bytes", got)
	}
	if got := next["offset"]; got != len("alphabetagammadelta") {
		t.Fatalf("second offset = %v, want %d (cumulative byte count)", got, len("alphabetagammadelta"))
	}
}

// TestDeltaChunkSkipsEmptyBuffer: a flush timer firing on an empty buffer must
// not write a zero-length event.
func TestDeltaChunkSkipsEmptyBuffer(t *testing.T) {
	c := newDeltaCoalescer()
	if _, ok := c.chunk(); ok {
		t.Fatal("an empty buffer produced a chunk")
	}
}
