package http

// 第五轮 P2-3: a cancelled request context must abort admission, not pass
// it. The HTTP layer used to be written as `if err != nil || ok { return
// nil }`, which turned AllowKey's ctx error into "admitted" — the handler
// then ran on until some later DB call reported the cancellation anyway,
// and a client that had already hung up was told 429 when it was really
// gone.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/execution"
	"github.com/creation-agent-studio/backend-go/internal/platform/config"
)

func admissionServer() *Server {
	return &Server{
		Config: &config.Config{Redis: config.RedisConfig{KeyPrefix: "studio"}},
		// limit=1/s: the second call in the same second would be denied,
		// so a "returned nil" outcome can only come from swallowing the
		// ctx error.
		RunAdmission: execution.NewRateLimiter(nil, "test:rate:runs", 1, time.Second),
	}
}

func TestAdmitUserRunPropagatesCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := admissionServer().admitUserRun(ctx, 7)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("admitUserRun err = %v, want context.Canceled", err)
	}
}

// A live context is still admitted (the happy path must not regress).
func TestAdmitUserRunAllowsLiveContext(t *testing.T) {
	if err := admissionServer().admitUserRun(context.Background(), 7); err != nil {
		t.Fatalf("admitUserRun on a live ctx = %v, want nil", err)
	}
}

// Exhausting the budget still returns the rate-limit error (not nil).
func TestAdmitUserRunReportsRateLimit(t *testing.T) {
	srv := admissionServer() // limit 1/s, shared key
	if err := srv.admitUserRun(context.Background(), 99); err != nil {
		t.Fatalf("first call: %v", err)
	}
	err := srv.admitUserRun(context.Background(), 99)
	if err == nil {
		t.Fatal("second call succeeded: the per-user budget is not enforced")
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("rate limit reported as a ctx error (%v): the two outcomes must stay distinct", err)
	}
}
