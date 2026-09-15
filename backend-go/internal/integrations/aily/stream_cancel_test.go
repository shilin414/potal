package aily

// 第五轮 P1-2: the stream producer must never perform a bare channel send.
//
//	The output channel is buffered (64). A producer parked on `out <- ev`
//	with a full buffer cannot be woken by closing the SSE body nor by
//	cancelling the context: only a `select { case out <- ev: case <-ctx.Done(): }`
//	can. Realistic triggers: the executor loses lease ownership mid-stream
//	(ErrLostOwnership) and returns early, or the worker shuts down. Without
//	the select the goroutine — and `defer close(out)` / `defer stop()` — is
//	stranded forever.
//
//	Regression shape: the stream used here is ENDLESS, so a producer that
//	survives cancel keeps emitting forever and `out` is never closed. That
//	makes "channel closed" a sound leak detector even though the test
//	drains the channel (draining would otherwise unblock a bare send and
//	hide the bug).
import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/catalog"
)

// floodingAPI opens a stream that emits far more events than the output
// buffer can hold.
type floodingAPI struct {
	// frames > 0 → a finite stream of that many SSE frames (happy path).
	// frames == 0 → an endless stream (leak reproducer).
	frames int

	mu     sync.Mutex
	closes int
}

func (f *floodingAPI) streamBody() io.ReadCloser {
	if f.frames > 0 {
		var sb strings.Builder
		for i := 0; i < f.frames; i++ {
			fmt.Fprintf(&sb, "event: message\ndata: {\"agent_chat_id\":\"chat-1\",\"session_id\":\"sess-1\",\"text\":\"tok-%d\"}\n\n", i)
		}
		return &trackedBody{Reader: strings.NewReader(sb.String()), owner: f}
	}
	return &trackedBody{Reader: &endlessSSE{}, owner: f}
}

func (f *floodingAPI) noteClose() {
	f.mu.Lock()
	f.closes++
	f.mu.Unlock()
}

func (f *floodingAPI) closed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closes > 0
}

func (f *floodingAPI) StartChat(ctx context.Context, agentID, token string, contentItems []map[string]any, attachmentIDs []string, sessionID string) (string, string, error) {
	return "chat-1", "sess-1", nil
}

func (f *floodingAPI) OpenStreamChat(ctx context.Context, agentID, token string, contentItems []map[string]any, attachmentIDs []string, sessionID string) (io.ReadCloser, error) {
	return f.streamBody(), nil
}

func (f *floodingAPI) GetChatResult(ctx context.Context, agentID, token, chatID string) (json.RawMessage, error) {
	return json.RawMessage(`{"status":"succeeded"}`), nil
}

func (f *floodingAPI) UploadAttachment(ctx context.Context, agentID, token string, data []byte, filename, attachmentType, docURL string) (string, error) {
	return "att-1", nil
}

func (f *floodingAPI) GetArtifact(ctx context.Context, agentID, token, artifactID string) (*ArtifactDownload, error) {
	return &ArtifactDownload{}, nil
}

func (f *floodingAPI) CheckVisibility(ctx context.Context, agentID, uat string) (bool, error) {
	return true, nil
}

// endlessSSE never reaches EOF: it keeps handing out SSE frames until the
// body is closed.
type endlessSSE struct {
	seq     int
	pending []byte
}

func (e *endlessSSE) Read(p []byte) (int, error) {
	if len(e.pending) == 0 {
		e.pending = []byte(fmt.Sprintf(
			"event: message\ndata: {\"agent_chat_id\":\"chat-1\",\"session_id\":\"sess-1\",\"text\":\"tok-%d\"}\n\n",
			e.seq))
		e.seq++
	}
	n := copy(p, e.pending)
	e.pending = e.pending[n:]
	return n, nil
}

type trackedBody struct {
	io.Reader
	owner *floodingAPI
}

func (b *trackedBody) Close() error {
	b.owner.noteClose()
	return nil
}

func streamInput() *catalog.SubmitInput {
	return &catalog.SubmitInput{
		ExternalResourceID: "agent-1",
		Auth:               &catalog.ProviderAuthContext{Token: "uat"},
		Payload: map[string]any{
			"content": []map[string]any{{"type": "text", "text": "hello"}},
		},
	}
}

// waitForFullBuffer parks until the producer has filled the 64-slot output
// buffer, i.e. it is blocked on a send.
func waitForFullBuffer(t *testing.T, out <-chan catalog.StreamEvent) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for len(out) < 64 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(out) < 64 {
		t.Fatalf("producer never filled the output buffer (len=%d)", len(out))
	}
}

// drainAssertClosed consumes the channel and fails if it is not closed.
// With an endless stream this only succeeds if the producer actually
// exited — a leaked producer keeps feeding the drain forever.
func drainAssertClosed(t *testing.T, out <-chan catalog.StreamEvent) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		for range out {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("output channel was never closed: producer goroutine leaked")
	}
}

// TestStreamPreparedCancelUnblocksFullOutputBuffer covers the realistic
// leak: the executor returns early (lost lease ownership / DB error), its
// deferred cancel fires, and nobody ever reads `out` again.
func TestStreamPreparedCancelUnblocksFullOutputBuffer(t *testing.T) {
	api := &floodingAPI{} // endless
	adapter := NewAgentAdapterWithAPI(api, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out, stop, err := adapter.StreamPrepared(ctx, streamInput())
	if err != nil {
		t.Fatalf("StreamPrepared: %v", err)
	}
	defer stop()

	waitForFullBuffer(t, out)

	// Nobody consumes from here on — only cancel().
	cancel()

	// The producer must exit on its own: `defer stop()` closes the body.
	deadline := time.Now().Add(2 * time.Second)
	for !api.closed() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !api.closed() {
		t.Fatal("producer goroutine leaked: cancel() did not unblock a full output buffer")
	}

	drainAssertClosed(t, out)
}

// TestStreamPreparedStopUnblocksConsumerExit covers the "consumer walks
// away" variant (workspace unmounted / worker shutdown) via the returned
// stop func.
func TestStreamPreparedStopUnblocksConsumerExit(t *testing.T) {
	api := &floodingAPI{} // endless
	adapter := NewAgentAdapterWithAPI(api, nil)

	out, stop, err := adapter.StreamPrepared(context.Background(), streamInput())
	if err != nil {
		t.Fatalf("StreamPrepared: %v", err)
	}

	waitForFullBuffer(t, out)
	stop()

	drainAssertClosed(t, out)

	if !api.closed() {
		t.Error("provider SSE body was not closed after stop()")
	}
}

// The happy path must still deliver every event: the select must not drop
// frames while a consumer is actively reading.
func TestStreamPreparedDeliversAllEventsWhenConsumed(t *testing.T) {
	api := &floodingAPI{frames: 20}
	adapter := NewAgentAdapterWithAPI(api, nil)

	out, stop, err := adapter.StreamPrepared(context.Background(), streamInput())
	if err != nil {
		t.Fatalf("StreamPrepared: %v", err)
	}
	defer stop()

	var got int
	timeout := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-out:
			if !ok {
				if got == 0 {
					t.Fatal("stream produced no events")
				}
				if !api.closed() {
					t.Error("body not closed at end of stream")
				}
				return
			}
			got++
		case <-timeout:
			t.Fatalf("stream did not finish (got %d events)", got)
		}
	}
}
