package execution

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/creation-agent-studio/backend-go/internal/platform/redisx"
)

// GCRA implements a distributed leaky-bucket rate limiter (Generic Cell
// Rate Algorithm) in Redis Lua. Unlike a fixed window it cannot burst
// 2×limit across a window boundary — the exact failure mode the reference
// implementation had.
//
// Keys: one per scope (e.g. aily:chats). Values:
//
//	tat = theoretical arrival time (microseconds) of the next free token.
//
// burst = limit (capacity); rate = limit per period.
// Emission interval = period / limit.
//
// The reference clock is Redis TIME (Clock Authority, Phase 3): the shared
// limiter state must not be expired or advanced by any single worker's local
// clock — a fast worker clock would otherwise consume other workers' tokens
// and a slow one would admit a burst. ARGV carries only the policy
// parameters; "now" is owned by Redis.
var gcraLua = goredis.NewScript(`
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000000 + tonumber(t[2])
local tat = redis.call('GET', KEYS[1])
local emission = ARGV[1]
local burst_offset = ARGV[2]
local ttl = ARGV[3]
if not tat then
  tat = 0
else
  tat = tonumber(tat)
end
emission = tonumber(emission)
burst_offset = tonumber(burst_offset)
if now < tat - burst_offset then
  local wait_us = tat - burst_offset - now
  return {0, tostring(wait_us)}
end
local new_tat = math.max(tat, now) + emission
redis.call('SET', KEYS[1], tostring(new_tat), 'PX', ttl)
return {1, '0'}
`)

// RateLimiter is a shared, distributed limiter for provider calls.
//
// Degradation policy (评测 P1): a Redis outage must NOT fail open to
// "unlimited" — with a run backlog every worker would simultaneously
// storm the provider (429 storm). On Redis errors the limiter falls back
// to an in-process GCRA with the same emission/burst parameters (the
// aggregate rate then bounds at limit × worker-count instead of the
// shared limit, which is still far safer than unbounded), and reports
// degraded=true for monitoring.
type RateLimiter struct {
	rdb    *redisx.Client
	key    string
	limit  int           // events per period
	period time.Duration // e.g. 1s

	// local fallback state (GCRA tat in microseconds, same math as the
	// Lua script) + degraded flag for monitoring.
	localMu  sync.Mutex
	localTat atomic.Int64
	degraded atomic.Bool
}

// NewRateLimiter builds a GCRA limiter: `limit` events per `period`,
// shared across every worker instance via Redis.
func NewRateLimiter(rdb *redisx.Client, key string, limit int, period time.Duration) *RateLimiter {
	return &RateLimiter{rdb: rdb, key: key, limit: limit, period: period}
}

// Allow attempts to consume one token. When denied it returns how long
// the caller should wait.
func (l *RateLimiter) Allow(ctx context.Context) (ok bool, wait time.Duration, err error) {
	if l.limit <= 0 {
		return true, 0, nil
	}
	emissionMicro := int64(l.period / time.Duration(l.limit) / time.Microsecond)
	if emissionMicro <= 0 {
		emissionMicro = 1
	}
	// Burst capacity in time units: (limit-1) additional emissions may
	// pile up instantly — the GCRA contract that prevents the fixed-window
	// boundary burst (10 at t=0.99s + 10 at t=1.01s).
	burstOffset := emissionMicro * int64(l.limit-1)
	ttlMillis := int64(l.period/time.Millisecond)*int64(l.limit) + 1000

	// Redis owns "now" for the shared window (Phase 3).
	res, err := gcraLua.Run(ctx, l.rdb.Client, []string{l.key},
		emissionMicro, burstOffset, ttlMillis).Slice()
	if err != nil {
		// Degraded mode: fall back to the in-process limiter with the
		// same GCRA parameters. Never fail open (评测 P1). The local
		// fallback necessarily uses the local clock — it only guards this
		// one worker while Redis is unreachable, and is reported through
		// Degraded().
		l.degraded.Store(true)
		return l.allowLocal(time.Now().UnixMicro(), emissionMicro, burstOffset)
	}
	l.degraded.Store(false)
	allowed, _ := res[0].(int64)
	if allowed == 1 {
		return true, 0, nil
	}
	waitUS, _ := res[1].(string)
	var waitMicro int64
	_, _ = fmt.Sscanf(waitUS, "%d", &waitMicro)
	return false, time.Duration(waitMicro) * time.Microsecond, nil
}

// Degraded reports whether the limiter is running on the local fallback
// (Redis unreachable). Expose as provider_limiter_degraded in metrics.
func (l *RateLimiter) Degraded() bool { return l.degraded.Load() }

// allowLocal runs the same GCRA decision in-process (per-worker bound).
func (l *RateLimiter) allowLocal(nowMicro, emissionMicro, burstOffset int64) (bool, time.Duration, error) {
	l.localMu.Lock()
	defer l.localMu.Unlock()
	tat := l.localTat.Load()
	if nowMicro < tat-burstOffset {
		return false, time.Duration(tat-burstOffset-nowMicro) * time.Microsecond, nil
	}
	newTat := tat
	if nowMicro > newTat {
		newTat = nowMicro
	}
	l.localTat.Store(newTat + emissionMicro)
	return true, 0, nil
}

// Acquire blocks until a token is available or ctx is done.
func (l *RateLimiter) Acquire(ctx context.Context) error {
	for {
		ok, wait, err := l.Allow(ctx)
		if err != nil {
			// Allow already fell back to the local limiter on Redis
			// errors; only ctx cancellation bubbles up here.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		if ok {
			return nil
		}
		if wait < 5*time.Millisecond {
			wait = 5 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}
