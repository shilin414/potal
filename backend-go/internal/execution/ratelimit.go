package execution

import (
	"context"
	"fmt"
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
var gcraLua = goredis.NewScript(`
local tat = redis.call('GET', KEYS[1])
local now = ARGV[1]
local emission = ARGV[2]
local burst_offset = ARGV[3]
local ttl = ARGV[4]
if not tat then
  tat = 0
else
  tat = tonumber(tat)
end
now = tonumber(now)
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
type RateLimiter struct {
	rdb    *redisx.Client
	key    string
	limit  int           // events per period
	period time.Duration // e.g. 1s
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
	nowMicro := time.Now().UnixMicro()
	emissionMicro := int64(l.period / time.Duration(l.limit) / time.Microsecond)
	if emissionMicro <= 0 {
		emissionMicro = 1
	}
	// Burst capacity in time units: (limit-1) additional emissions may
	// pile up instantly — the GCRA contract that prevents the fixed-window
	// boundary burst (10 at t=0.99s + 10 at t=1.01s).
	burstOffset := emissionMicro * int64(l.limit-1)
	ttlMillis := int64(l.period/time.Millisecond)*int64(l.limit) + 1000

	res, err := gcraLua.Run(ctx, l.rdb.Client, []string{l.key},
		nowMicro, emissionMicro, burstOffset, ttlMillis).Slice()
	if err != nil {
		// Fail open on Redis errors: a limiter outage must not stop runs;
		// the provider's own 429 + retry policy is the backstop.
		return true, 0, err
	}
	allowed, _ := res[0].(int64)
	if allowed == 1 {
		return true, 0, nil
	}
	waitUS, _ := res[1].(string)
	var waitMicro int64
	_, _ = fmt.Sscanf(waitUS, "%d", &waitMicro)
	return false, time.Duration(waitMicro) * time.Microsecond, nil
}

// Acquire blocks until a token is available or ctx is done.
func (l *RateLimiter) Acquire(ctx context.Context) error {
	for {
		ok, wait, err := l.Allow(ctx)
		if err != nil {
			return nil // fail open (see Allow)
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
