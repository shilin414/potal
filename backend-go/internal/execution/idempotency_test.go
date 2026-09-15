package execution

import (
	"bytes"
	"testing"

	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
)

// TestRunRequestHashIsStableAcrossAttachmentOrdering pins the normalization
// rule that makes a retry a replay instead of a conflict.
//
// attachment_ids is a SET on the wire: the same three attachments listed in a
// different order are the same request. Hashing the raw slice would bind the
// client to the exact byte order of its first attempt and answer 409 to a
// perfectly ordinary resend.
func TestRunRequestHashIsStableAcrossAttachmentOrdering(t *testing.T) {
	a := RunRequestHash(7, 0, "hello", []string{"att-1", "att-2", "att-3"})
	b := RunRequestHash(7, 0, "hello", []string{"att-3", "att-1", "att-2"})
	if !bytes.Equal(a, b) {
		t.Fatal("attachment order changed the request identity: a reordered resend would be " +
			"rejected as a different payload (409) instead of replayed")
	}
	// Duplicates are dropped for the same reason: a client that repeats an
	// id must not create a second identity.
	c := RunRequestHash(7, 0, "hello", []string{"att-1", "att-1", "att-2", "att-3"})
	if !bytes.Equal(a, c) {
		t.Fatal("a duplicated attachment id changed the request identity")
	}
	if len(a) != 32 {
		t.Fatalf("hash length = %d, want 32 (SHA-256, matching BINARY(32))", len(a))
	}
}

// TestRunRequestHashDistinguishesFields is the length-prefix guard.
//
// Without per-field framing, the concatenation of ("ab","c") and ("a","bc")
// is identical, so two DIFFERENT requests would be accepted as each other's
// replay — silently returning the wrong run and never creating the second
// turn the user asked for.
func TestRunRequestHashDistinguishesFields(t *testing.T) {
	base := RunRequestHash(1, 2, "ab", nil)
	if bytes.Equal(base, RunRequestHash(1, 2, "a", nil)) {
		t.Fatal("different content produced the same identity")
	}
	// The classic concatenation collision, across the content/attachment
	// boundary and the id boundary.
	if bytes.Equal(RunRequestHash(1, 2, "x", []string{"y"}), RunRequestHash(1, 2, "xy", nil)) {
		t.Fatal("content and attachment id concatenated ambiguously")
	}
	if bytes.Equal(RunRequestHash(12, 0, "x", nil), RunRequestHash(1, 20, "x", nil)) {
		t.Fatal("application_id and conversation_id concatenated ambiguously")
	}
	// An empty attachment id is normalized away: it is not a real attachment,
	// so it cannot make a request different from the one that omits it. It
	// also cannot collide with a real id, because a real id is non-empty.
	if !bytes.Equal(RunRequestHash(1, 2, "x", nil), RunRequestHash(1, 2, "x", []string{""})) {
		t.Fatal("an empty attachment id must normalize to 'absent'")
	}
	if bytes.Equal(RunRequestHash(1, 2, "x", nil), RunRequestHash(1, 2, "x", []string{"a"})) {
		t.Fatal("a real attachment id must change the identity")
	}
}

// TestRunRequestHashSeparatesApplicationAndConversation: a lazy request and a
// request against an existing conversation are different intents and must not
// collide, otherwise the retry of a first message could be answered with a run
// that belongs to another conversation.
func TestRunRequestHashSeparatesApplicationAndConversation(t *testing.T) {
	lazy := RunRequestHash(5, 0, "hi", nil)
	explicit := RunRequestHash(5, 99, "hi", nil)
	otherApp := RunRequestHash(6, 0, "hi", nil)
	if bytes.Equal(lazy, explicit) || bytes.Equal(lazy, otherApp) || bytes.Equal(explicit, otherApp) {
		t.Fatal("lazy / explicit / other-application requests are not distinguishable")
	}
}

// TestProviderSubmissionKeyIsStableAndNumbered pins the key format the review
// prescribes (第九轮 P0-2 §5):
//
//	potal:run:<run_uuid>:submit:<n>
//
// The important property is what the key does NOT contain: the attempt
// counter. Several HTTP retries of one submission must present the SAME key to
// the provider, or a provider that honours idempotency keys would treat them
// as distinct requests — the exact second-chat bug the table exists to close.
func TestProviderSubmissionKeyIsStableAndNumbered(t *testing.T) {
	runID := mustIDFromString(t, "3f0d9b1e-6a2c-4f57-9a1e-2b0c7d4e5f60")
	first := ProviderSubmissionKey(runID, 1)
	if first != "potal:run:3f0d9b1e-6a2c-4f57-9a1e-2b0c7d4e5f60:submit:1" {
		t.Fatalf("key = %q, want the documented stable form", first)
	}
	if again := ProviderSubmissionKey(runID, 1); again != first {
		t.Fatalf("key is not stable across calls: %q vs %q", first, again)
	}
	if second := ProviderSubmissionKey(runID, 2); second == first {
		t.Fatal("a new submission_no must get a new key (a changed payload is a different action)")
	}
}

// TestProviderSubmissionHashExcludesSubmissionNumber: the hash answers "same
// payload?", and a caller about to submit cannot know which submission_no the
// service will pick — so the number must not be part of the payload identity.
func TestProviderSubmissionHashExcludesSubmissionNumber(t *testing.T) {
	a := ProviderSubmissionHash("aily", []byte(`{"content":[{"type":"text","text":"hi"}]}`))
	b := ProviderSubmissionHash("aily", []byte(`{"content":[{"type":"text","text":"hi"}]}`))
	if !bytes.Equal(a, b) {
		t.Fatal("the same provider + payload hashed differently")
	}
	if bytes.Equal(a, ProviderSubmissionHash("aily", []byte(`{"content":[{"type":"text","text":"bye"}]}`))) {
		t.Fatal("a different payload hashed identically")
	}
	if bytes.Equal(a, ProviderSubmissionHash("other", []byte(`{"content":[{"type":"text","text":"hi"}]}`))) {
		t.Fatal("a different provider hashed identically")
	}
	if !bytes.Equal(ProviderSubmissionHash("aily", nil), ProviderSubmissionHash("aily", nil)) {
		t.Fatal("a nil payload must still hash deterministically")
	}
}

func mustIDFromString(t *testing.T, s string) ids.ID {
	t.Helper()
	var out ids.ID
	if err := out.Scan(s); err != nil {
		t.Fatalf("parse id %q: %v", s, err)
	}
	return out
}
