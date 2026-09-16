// 第十轮 Batch 4.1 端到端验证：replay cache 的字节上界是不是真的上界
// （复审报告 §37）。
//
// The unit tests assert the ring's own invariant (Bytes() <= maxBytes at every
// instant). This one asserts the property an operator actually cares about,
// over real MySQL and real Redis:
//
//	delivered in full      (not cached ≠ not sent)
//	cache within its bound (the limit is memory safety, not a target)
//	recoverable afterwards (not cached ≠ lost: the log still has it)
//
// The three are inseparable. A cache rule that satisfied the bound by
// truncating the event, or by never sending it, would pass a bound-only test
// and break the product.
package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/transport/sse"
)

// TestSSEHubOversizedTerminalIsDeliveredButNotCached is §37 end to end, with the
// real hub cache and the real byte limit.
//
// A terminal event carries the run's WHOLE answer in `payload.text`, so it is
// the frame most likely to exceed SSE_HUB_CACHE_BYTES — and the pre-4.1 ring
// kept the newest entry whatever its size, which made that limit advisory.
func TestSSEHubOversizedTerminalIsDeliveredButNotCached(t *testing.T) {
	svc, rdb, runID := streamRun(t, "feishu_aily")
	run, err := svc.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("load run: %v", err)
	}

	opts := sse.DefaultHubOptions()
	opts.CacheMaxBytes = 1024
	opts.SubscriberMaxBytes = 64 << 10
	ts, hub := startHubSSEServerWithOptions(t, svc, rdb, run, opts)
	t.Cleanup(ts.Close)

	s := openStreamAt(t, runURL(ts, runID, "after=0"))
	syncStream(t, rdb, runID, s)

	answer := strings.Repeat("x", 4096)
	terminalSeq := appendLiveEvent(t, svc, runID, execution.EventRunCompleted,
		map[string]any{"status": execution.StatusSucceeded, "text": answer})

	if !s.await(func(f sseFrame) bool { return f.Sequence == terminalSeq }, 5*time.Second) {
		t.Fatalf("the oversized terminal never reached the subscriber; transcript=%v", frameSummary(s.all))
	}
	select {
	case <-s.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream stayed open after the terminal event")
	}

	terminal, ok := s.first(func(f sseFrame) bool { return f.Sequence == terminalSeq })
	if !ok {
		t.Fatal("the oversized terminal is missing from the transcript")
	}
	if got, _ := terminal.Payload["text"].(string); got != answer {
		t.Fatalf("the delivered terminal carried %d bytes of text, want %d: an event too large to "+
			"cache is still delivered in full — the cache is not the transport", len(got), len(answer))
	}

	// The hub survives the terminal (its cache is retained for the reuse
	// window), so its bound can be read after the fact.
	runHub := hub.Lookup(runID.String())
	if runHub == nil {
		t.Fatal("the hub was evicted before its cache bound could be read")
	}
	if got := runHub.CacheBytes(); got > opts.CacheMaxBytes {
		t.Fatalf("hub cache holds %d bytes with SSE_HUB_CACHE_BYTES = %d: the byte bound is a "+
			"memory-safety limit, not a target", got, opts.CacheMaxBytes)
	}
	if got := runHub.CacheLen(); got != 0 {
		t.Fatalf("hub cache still holds %d events after a terminal larger than the whole budget, want 0",
			got)
	}

	// A reconnect is served the same terminal, in full, from the log: a cache
	// miss costs one read, and costs the client nothing.
	again := openStreamAt(t, runURL(ts, runID, "after=0"))
	if !again.await(func(f sseFrame) bool { return f.Sequence == terminalSeq }, 5*time.Second) {
		t.Fatalf("the reconnect never received the terminal; transcript=%v", frameSummary(again.all))
	}
	replayed, _ := again.first(func(f sseFrame) bool { return f.Sequence == terminalSeq })
	if got, _ := replayed.Payload["text"].(string); got != answer {
		t.Fatalf("the reconnect got %d bytes of terminal text, want %d: the cache is an optimisation, "+
			"so a miss must fall back to the log where the event is complete", len(got), len(answer))
	}
}
