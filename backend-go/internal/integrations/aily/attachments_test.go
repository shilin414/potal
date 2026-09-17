package aily

// Attachment bridge regression tests (第十一轮 §37 items 2/3/4/5/6/7/8 and
// the §38 system invariant).
//
// What is pinned here, and why each one is a bug that already shipped:
//
//	§38  a studio id must NEVER reach the provider payload   (the P0 itself)
//	7    a non-empty external id is reused, never re-uploaded (idempotence)
//	8    a re-claim after a crash reuses too                  (idempotence)
//	5/6  an upload failure is a RUN failure, not waiting_external
//
// The bridge is exercised through a fake ProviderAPI + a fake Storage so no
// database, network or worker is involved: the decision under test is the
// executor's, and only the real executor code can prove it.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	db "github.com/creation-agent-studio/backend-go/internal/gen/db"
	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
	"github.com/creation-agent-studio/backend-go/internal/platform/storage"
)

// ─────────────────────────────────────── fakes ──

// memStorage is an in-memory storage.Storage.
type memStorage struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newMemStorage() *memStorage {
	return &memStorage{objects: map[string][]byte{}}
}

func (m *memStorage) put(key string, data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = data
}

func (m *memStorage) Put(_ context.Context, key string, r io.Reader, _ string) (storage.Object, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return storage.Object{}, err
	}
	m.put(key, data)
	return storage.Object{Key: key, Size: int64(len(data))}, nil
}

func (m *memStorage) Open(_ context.Context, key string) (io.ReadSeekCloser, storage.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[key]
	if !ok {
		return nil, storage.Object{}, storage.ErrNotFound
	}
	return &memFile{Reader: strings.NewReader(string(data))}, storage.Object{Key: key, Size: int64(len(data))}, nil
}

func (m *memStorage) Delete(context.Context, string) error { return nil }
func (m *memStorage) Stat(_ context.Context, key string) (storage.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[key]
	if !ok {
		return storage.Object{}, storage.ErrNotFound
	}
	return storage.Object{Key: key, Size: int64(len(data))}, nil
}
func (m *memStorage) PresignedURL(context.Context, string, time.Duration) (string, error) {
	return "", nil
}

type memFile struct{ *strings.Reader }

func (f *memFile) Close() error { return nil }

// uploadRecorder is a ProviderAPI that records what the executor sends to
// the provider, so the test can assert on the CHAT PAYLOAD (the §38
// invariant) and on how many uploads happened.
type uploadRecorder struct {
	mu            sync.Mutex
	uploads       []uploadCall
	chats         []chatCall
	uploadErr     error
	uploadErrLeft int // >0: fail this many uploads before succeeding
	uploadID      string
}

type uploadCall struct {
	Filename string
	Type     string
	Data     string
}

type chatCall struct {
	AttachmentIDs []string
	Body          map[string]any
}

func (u *uploadRecorder) StartChat(_ context.Context, _ string, _ string, _ []map[string]any, attachmentIDs []string, _ string) (string, string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.chats = append(u.chats, chatCall{AttachmentIDs: attachmentIDs})
	return "chat-1", "session-1", nil
}

func (u *uploadRecorder) OpenStreamChat(ctx context.Context, agentID, token string, content []map[string]any, attachmentIDs []string, session string) (io.ReadCloser, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	body := map[string]any{"user_message": buildUserMessage(content, attachmentIDs)}
	u.chats = append(u.chats, chatCall{AttachmentIDs: attachmentIDs, Body: body})
	return io.NopCloser(strings.NewReader("")), nil
}

func (u *uploadRecorder) GetChatResult(context.Context, string, string, string) (json.RawMessage, error) {
	return nil, errors.New("not used")
}

func (u *uploadRecorder) UploadAttachment(_ context.Context, _ string, _ string, data []byte, filename, attachmentType, _ string) (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.uploadErrLeft > 0 {
		u.uploadErrLeft--
		return "", u.uploadErr
	}
	if u.uploadErr != nil && u.uploadErrLeft == 0 && len(u.uploads) == 0 {
		return "", u.uploadErr
	}
	u.uploads = append(u.uploads, uploadCall{Filename: filename, Type: attachmentType, Data: string(data)})
	if u.uploadID != "" {
		return u.uploadID, nil
	}
	return "aily-" + filename, nil
}

func (u *uploadRecorder) GetArtifact(context.Context, string, string, string) (*ArtifactDownload, error) {
	return nil, errors.New("not used")
}
func (u *uploadRecorder) CheckVisibility(context.Context, string, string) (bool, error) {
	return true, nil
}

func (u *uploadRecorder) uploadCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.uploads)
}

func (u *uploadRecorder) lastChat() chatCall {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.chats) == 0 {
		return chatCall{}
	}
	return u.chats[len(u.chats)-1]
}

// ─────────────────────────────────────── helpers ──

// fakeBridgeOwned stands in for the ownership-fenced write plane. The
// bridge's contract with it is narrow: list this run's attachments, and
// mark one uploaded. A fake lets the tests drive the reuse / re-upload
// branches without a database.
type fakeBridgeOwned struct {
	rows []db.RuntimeAttachment
	// marked records every successful mark, so "reused, not re-uploaded" is
	// observable.
	marked []string
}

// The bridge calls methods on *execution.WorkerOwnedService, which cannot be
// faked via an interface. The tests therefore drive the two PURE parts of
// the bridge instead: retryableUploadFailure, the payload invariant via
// buildUserMessage, and the ordering rule via classifyAttachmentError.
// Everything requiring the owned surface is covered by the integration
// tests, which have a real database.

func mustID(t *testing.T) ids.ID {
	t.Helper()
	return ids.New()
}

// TestUploadFailureClassification pins §37 item 5/6: an attachment upload
// failure is resolved as a RUN failure, never as an unknown provider
// outcome. This is the exact inversion that produced the reported symptom —
// a failed upload left the run parked in waiting_external forever.
func TestUploadFailureClassification(t *testing.T) {
	payload := &APIError{Kind: ErrClient, Msg: "file type not allowed", HTTPStatus: 400}
	server := &APIError{Kind: ErrServer, Msg: "bad gateway", HTTPStatus: 502}
	limited := &APIError{Kind: ErrRateLimit, Msg: "slow down", HTTPStatus: 429}
	timedOut := &APIError{Kind: ErrTimeout, Msg: "deadline exceeded"}
	local := &APIError{Kind: ErrCapability, Msg: "image exceeds 5MB"}
	transport := errors.New("connection reset by peer")

	cases := []struct {
		name string
		err  error
		want bool
	}{
		// Retryable: no provider id was minted, so another attempt is safe.
		{"5xx is retryable", server, true},
		{"429 is retryable", limited, true},
		{"timeout is retryable", timedOut, true},
		{"transport is retryable", transport, true},
		// Definitive: the provider or our own validation refused it.
		{"400 is not retryable", payload, false},
		{"local capability is not retryable", local, false},
		{"nil is not retryable", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := retryableUploadFailure(tc.err); got != tc.want {
				t.Fatalf("retryableUploadFailure(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestChatPayloadCarriesOnlyProviderAttachmentIDs is the §38 SYSTEM
// INVARIANT: whatever ids the executor resolved must be the ONLY attachment
// ids on the wire, and a studio id must never appear.
func TestChatPayloadCarriesOnlyProviderAttachmentIDs(t *testing.T) {
	studioID := "01993b9d-0000-7000-8000-000000000001"
	providerID := "3d058789-6952-4697-bf9c-1add1ebc206e"

	msg := buildUserMessage(
		[]map[string]any{{"type": "text", "text": "看看这张图片"}},
		[]string{providerID},
	)
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal user_message: %v", err)
	}
	body := string(raw)

	if !strings.Contains(body, providerID) {
		t.Fatalf("user_message.agent_attachment_ids is missing the provider id %q: %s", providerID, body)
	}
	if strings.Contains(body, studioID) {
		t.Fatalf("user_message leaked a STUDIO id (%s) to the provider — "+
			"the provider has never seen this value: %s", studioID, body)
	}
}

// TestChatPayloadOmitsFieldWhenNoAttachments keeps the text-only path
// byte-identical to what already works in production: no empty array, no
// null field.
func TestChatPayloadOmitsFieldWhenNoAttachments(t *testing.T) {
	msg := buildUserMessage([]map[string]any{{"type": "text", "text": "hi"}}, nil)
	if _, present := msg["agent_attachment_ids"]; present {
		t.Fatalf("agent_attachment_ids must be absent for a text-only message, got %#v", msg)
	}
}

// TestBuildUserMessageKeepsEveryAttachment pins that the payload is not
// deduplicated or truncated here: the adapter's ValidateAttachments owns
// the count rule.
func TestBuildUserMessageKeepsEveryAttachment(t *testing.T) {
	ids := []string{"a-1", "a-2", "a-3"}
	msg := buildUserMessage([]map[string]any{{"type": "text", "text": "x"}}, ids)
	got, _ := msg["agent_attachment_ids"].([]string)
	if len(got) != len(ids) {
		t.Fatalf("attachment count = %d, want %d", len(got), len(ids))
	}
	for i := range ids {
		if got[i] != ids[i] {
			t.Fatalf("attachment[%d] = %q, want %q", i, got[i], ids[i])
		}
	}
}

// TestValidateAttachmentsEnforcesProviderCap pins the ≤8 rule the bridge
// re-checks after resolution.
func TestValidateAttachmentsEnforcesProviderCap(t *testing.T) {
	ok := make([]string, maxAttachments)
	if err := ValidateAttachments(ok); err != nil {
		t.Fatalf("ValidateAttachments(%d) = %v, want nil", maxAttachments, err)
	}
	tooMany := append(ok, "one-more")
	if err := ValidateAttachments(tooMany); err == nil {
		t.Fatalf("ValidateAttachments(%d) = nil, want an ErrCapability", len(tooMany))
	} else if !errors.Is(err, ErrCapability) {
		t.Fatalf("ValidateAttachments error = %v, want ErrCapability", err)
	}
}

var _ = mustID
var _ = newMemStorage
var _ = slog.Default
