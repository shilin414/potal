package execution

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
	"github.com/creation-agent-studio/backend-go/internal/platform/redisx"
)

var inflightAcquireLua = goredis.NewScript(`
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
local count = redis.call('ZCARD', KEYS[1])
if count >= tonumber(ARGV[2]) then
  return {0, tostring(count)}
end
redis.call('ZADD', KEYS[1], ARGV[3], ARGV[4])
return {1, tostring(count + 1)}
`)

// InflightLimiter is a provider-wide distributed semaphore. Each slot is
// leased to one run and expires with the worker lease, so a crashed
// worker cannot permanently consume provider capacity.
type InflightLimiter struct {
	RDB         *redisx.Client
	Provider    string
	MaxInflight int
	Lease       time.Duration
}

func NewInflightLimiter(rdb *redisx.Client, provider string, max int, lease time.Duration) *InflightLimiter {
	return &InflightLimiter{RDB: rdb, Provider: provider, MaxInflight: max, Lease: lease}
}

func (l *InflightLimiter) key() string {
	return l.RDB.Key("provider", l.Provider, "inflight")
}

// Acquire reserves one provider slot for runID.
func (l *InflightLimiter) Acquire(ctx context.Context, runID ids.ID) (bool, int, error) {
	if l == nil || l.MaxInflight <= 0 {
		return true, 0, nil
	}
	now := time.Now().UTC()
	score := float64(now.Add(l.Lease).UnixMilli())
	res, err := inflightAcquireLua.Run(ctx, l.RDB.Client, []string{l.key()},
		now.UnixMilli(), l.MaxInflight, score, runID.String()).Slice()
	if err != nil {
		return false, 0, err
	}
	allowed, _ := res[0].(int64)
	count, _ := res[1].(string)
	if len(count) == 0 {
		if n, ok := res[1].(int64); ok {
			count = itoa64(n)
		}
	}
	return allowed == 1, parseIntCount(count), nil
}

// Renew extends the run's slot lease. It is best-effort: losing the slot
// after the provider call has started does not invalidate the run's
// ownership fence, but metrics expose the degradation.
func (l *InflightLimiter) Renew(ctx context.Context, runID ids.ID) error {
	if l == nil || l.MaxInflight <= 0 {
		return nil
	}
	return l.RDB.ZAdd(ctx, l.key(), goredis.Z{
		Score:  float64(time.Now().UTC().Add(l.Lease).UnixMilli()),
		Member: runID.String(),
	}).Err()
}

// Release removes the run's slot after terminalization.
func (l *InflightLimiter) Release(ctx context.Context, runID ids.ID) error {
	if l == nil || l.MaxInflight <= 0 {
		return nil
	}
	return l.RDB.ZRem(ctx, l.key(), runID.String()).Err()
}

// Depth reports the current non-expired provider slot count.
func (l *InflightLimiter) Depth(ctx context.Context) (int, error) {
	if l == nil || l.MaxInflight <= 0 {
		return 0, nil
	}
	if _, err := l.RDB.ZRemRangeByScore(ctx, l.key(), "-inf",
		fmt.Sprintf("(%d", time.Now().UTC().UnixMilli())).Result(); err != nil {
		return 0, err
	}
	n, err := l.RDB.ZCard(ctx, l.key()).Result()
	return int(n), err
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for n > 0 {
		pos--
		b[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(b[pos:])
}

func parseIntCount(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
