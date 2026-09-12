package execution

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/platform/config"
	"github.com/creation-agent-studio/backend-go/internal/platform/redisx"
)

// TestGCRAAgainstRealRedis is an opt-in integration test (STUDIO_TEST_REDIS=1).
// It validates the anti-burst property: two adjacent windows may not emit
// 2×limit the way a fixed-window limiter can.
func TestGCRAAgainstRealRedis(t *testing.T) {
	if os.Getenv("STUDIO_TEST_REDIS") != "1" {
		t.Skip("set STUDIO_TEST_REDIS=1 to run against real Redis")
	}
	cfg := &config.RedisConfig{
		Host: os.Getenv("REDIS_HOST"), Port: 6380,
		Password: os.Getenv("REDIS_PASSWORD"), DB: 2, KeyPrefix: "xiaoan3",
	}
	if cfg.Host == "" {
		cfg.Host = "192.168.211.239"
	}
	rdb, err := redisx.Open(context.Background(), *cfg)
	if err != nil {
		t.Fatalf("redis: %v", err)
	}
	defer rdb.Close()

	ctx := context.Background()
	key := rdb.Key("rate", "test", "gcra", time.Now().Format(time.RFC3339Nano))
	limiter := NewRateLimiter(rdb, key, 5, time.Second)

	allowed := atomic.Int64{}
	denied := atomic.Int64{}
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, _, err := limiter.Allow(ctx)
			if err != nil {
				return
			}
			if ok {
				allowed.Add(1)
			} else {
				denied.Add(1)
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	// GCRA with burst capacity 5: the first burst of 20 concurrent
	// attempts admits at most 5 immediately.
	if allowed.Load() > 5 {
		t.Fatalf("burst leaked: %d admitted instantly (capacity 5)", allowed.Load())
	}
	if allowed.Load() == 0 {
		t.Fatal("nothing admitted")
	}
	t.Logf("allowed=%d denied=%d elapsed=%s", allowed.Load(), denied.Load(), elapsed)
}

func TestQueueStreamKeyNamespacing(t *testing.T) {
	cfg := config.RedisConfig{KeyPrefix: "xiaoan3"}
	rdb := redisx.NewWithPrefix(cfg.KeyPrefix)
	if got := QueueStream(rdb, "feishu_aily"); got != "xiaoan3:queue:feishu_aily" {
		t.Fatalf("queue key = %q", got)
	}
	if got := rdb.RunEventsChannel("abc"); got != "xiaoan3:run:abc:events" {
		t.Fatalf("pubsub channel = %q (must match the reference implementation)", got)
	}
}
