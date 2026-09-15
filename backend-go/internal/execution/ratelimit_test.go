package execution

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

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
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	// config.Load reads .env.local, so the shared dev Redis (host, port AND
	// password) is used exactly as the binaries use it.
	rdb, err := redisx.Open(context.Background(), cfg.Redis)
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

// TestLimiterRedisOutageLocalFallback (T13, 修复计划 §65): with Redis
// unreachable the limiter must degrade to the in-process GCRA — never
// fail open — and report degraded=true.
func TestLimiterRedisOutageLocalFallback(t *testing.T) {
	// A client pointed at an address with (almost certainly) nothing
	// listening on it. Built without the Open() ping so the outage is
	// discovered lazily by the limiter itself — exactly the production
	// failure shape (Redis dies mid-flight).
	dead := goredis.NewClient(&goredis.Options{
		Addr:        "127.0.0.1:1",
		DialTimeout: 100 * time.Millisecond,
		MaxRetries:  1,
	})
	defer func() { _ = dead.Close() }()
	rdb := redisx.NewWithPrefix("itest_outage")
	rdb.Client = dead

	limiter := NewRateLimiter(rdb, "itest:outage:gcra", 5, time.Second)
	// 20 CONCURRENT Allow() calls (T13 spec): they all fail their Redis
	// dial at (nearly) the same instant and fall back to the in-process
	// GCRA — which must admit at most the burst capacity of 5.
	const attempts = 20
	var allowed atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok, _, err := limiter.Allow(context.Background())
			if err != nil {
				t.Errorf("Allow must not error on Redis outage (local fallback): %v", err)
				return
			}
			if ok {
				allowed.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if n := allowed.Load(); n > 5 {
		t.Fatalf("local fallback admitted %d/%d instantly, want <= 5 (fail-open regression)", n, attempts)
	}
	if n := allowed.Load(); n == 0 {
		t.Fatal("local fallback admitted nothing — GCRA burst capacity broken")
	}
	if !limiter.Degraded() {
		t.Fatal("Degraded() = false during a Redis outage")
	}
}

// TestUserAdmissionRedisDownStillBounded (评测 P1): the per-user limiter is
// long-lived and meters per key, so a Redis outage must NOT fail open.
//
// A nil Redis client makes every AllowKey take the degraded in-process
// path. A per-request limiter (the previous design) would have reset the
// fallback state on every call and admitted everything.
func TestUserAdmissionRedisDownStillBounded(t *testing.T) {
	limiter := NewRateLimiter(nil, "rate:runs:user", 3, time.Second)
	ctx := context.Background()

	allowed := 0
	for i := 0; i < 20; i++ {
		ok, _, err := limiter.AllowKey(ctx, "rate:runs:user:1")
		if err != nil {
			t.Fatalf("AllowKey: %v", err)
		}
		if ok {
			allowed++
		}
	}
	if allowed == 0 {
		t.Fatal("degraded limiter admitted nothing; the local fallback is broken")
	}
	if allowed > 3 {
		t.Fatalf("degraded limiter admitted %d of 20 with limit 3 (fail-open)", allowed)
	}
	if !limiter.Degraded() {
		t.Fatal("limiter must report degraded while Redis is unreachable")
	}

	// Keys are metered independently: a different user has its own budget.
	if ok, _, err := limiter.AllowKey(ctx, "rate:runs:user:2"); err != nil || !ok {
		t.Fatalf("second key must have its own budget (ok=%v err=%v)", ok, err)
	}
}
