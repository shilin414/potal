package execution

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/creation-agent-studio/backend-go/internal/platform/ids"
	"github.com/creation-agent-studio/backend-go/internal/platform/redisx"
)

// ErrProviderSlotLost is returned by Renew when the caller's provider slot
// no longer exists (expired / released). Renew never recreates a missing
// slot — a stale attempt must not resurrect capacity accounting.
var ErrProviderSlotLost = errors.New("execution: provider inflight slot lost")

// inflightAcquireLua reserves (or refreshes) one attempt-scoped slot.
// ARGV: [nowMs, max, expiryScore, member]
//
//  1. drop expired slots (crashed workers cannot pin capacity forever)
//  2. same member already present → refresh its expiry (idempotent
//     re-acquire by the SAME attempt) and admit
//  3. ZCARD >= max → reject
//  4. ZADD the member
//
// The member is attempt-scoped ({run}:{epoch}:{token}), so a stale worker
// can never collide with, extend or delete the new owner's slot.
var inflightAcquireLua = goredis.NewScript(`
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
if redis.call('ZSCORE', KEYS[1], ARGV[4]) then
  redis.call('ZADD', KEYS[1], ARGV[3], ARGV[4])
  return {1, tostring(redis.call('ZCARD', KEYS[1]))}
end
local count = redis.call('ZCARD', KEYS[1])
if count >= tonumber(ARGV[2]) then
  return {0, tostring(count)}
end
redis.call('ZADD', KEYS[1], ARGV[3], ARGV[4])
return {1, tostring(count + 1)}
`)

// inflightRenewLua extends the expiry of an EXISTING slot only (XX).
// ARGV: [expiryScore, member]. Returns 0 when the member is absent — the
// caller must treat that as ErrProviderSlotLost and must NOT re-add the
// member.
var inflightRenewLua = goredis.NewScript(`
if redis.call('ZSCORE', KEYS[1], ARGV[2]) then
  redis.call('ZADD', KEYS[1], ARGV[1], ARGV[2])
  return 1
end
return 0
`)

// ProviderSlot is the attempt-scoped provider concurrency reservation.
// It is derived from the immutable ExecutionOwnership: the Redis ZSET
// member is "{run_id}:{lease_epoch}:{lease_token}", so slots of different
// attempts (and different workers) of the same run are distinct — a stale
// worker releasing its slot can never delete the new owner's slot, and a
// stale renewal can never extend it (P0-1 fencing).
type ProviderSlot struct {
	RunID      ids.ID
	LeaseEpoch uint64
	LeaseToken ids.ID
	Member     string
}

func newProviderSlot(own ExecutionOwnership) *ProviderSlot {
	return &ProviderSlot{
		RunID:      own.RunID,
		LeaseEpoch: own.LeaseEpoch,
		LeaseToken: own.LeaseToken,
		Member:     own.RunID.String() + ":" + strconv.FormatUint(own.LeaseEpoch, 10) + ":" + own.LeaseToken.String(),
	}
}

// InflightLimiter is a provider-wide distributed semaphore. Each slot is
// leased to one run ATTEMPT (ownership-scoped) and expires with the
// worker lease, so a crashed worker cannot permanently consume provider
// capacity.
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

// Acquire reserves one provider slot for the given attempt. Re-acquiring
// with the SAME ownership is idempotent (refreshes the expiry); a run
// re-claimed by a new attempt gets a distinct member and must fit within
// the limit independently. Returns the slot, whether it was admitted and
// the post-decision depth (for rejection metrics).
func (l *InflightLimiter) Acquire(ctx context.Context, own ExecutionOwnership) (*ProviderSlot, bool, int, error) {
	if l == nil || l.MaxInflight <= 0 {
		return nil, true, 0, nil
	}
	slot := newProviderSlot(own)
	now := time.Now().UTC()
	score := float64(now.Add(l.Lease).UnixMilli())
	res, err := inflightAcquireLua.Run(ctx, l.RDB.Client, []string{l.key()},
		now.UnixMilli(), l.MaxInflight, score, slot.Member).Slice()
	if err != nil {
		return nil, false, 0, err
	}
	allowed, _ := res[0].(int64)
	count, _ := res[1].(string)
	if len(count) == 0 {
		if n, ok := res[1].(int64); ok {
			count = itoa64(n)
		}
	}
	return slot, allowed == 1, parseIntCount(count), nil
}

// Renew extends the slot's lease. XX-only semantics: a missing member is
// ErrProviderSlotLost and is NEVER recreated here — renewing a lost slot
// would let a stale attempt inflate the semaphore's capacity accounting.
func (l *InflightLimiter) Renew(ctx context.Context, slot *ProviderSlot) error {
	if l == nil || l.MaxInflight <= 0 || slot == nil {
		return nil
	}
	res, err := inflightRenewLua.Run(ctx, l.RDB.Client, []string{l.key()},
		float64(time.Now().UTC().Add(l.Lease).UnixMilli()), slot.Member).Int64()
	if err != nil {
		return err
	}
	if res != 1 {
		return ErrProviderSlotLost
	}
	return nil
}

// Release removes exactly this attempt's slot after terminalization. The
// ZREM targets the full member string, so a stale worker's deferred
// release can only remove its OWN (already-irrelevant) slot, never the
// new owner's.
func (l *InflightLimiter) Release(ctx context.Context, slot *ProviderSlot) error {
	if l == nil || l.MaxInflight <= 0 || slot == nil {
		return nil
	}
	return l.RDB.ZRem(ctx, l.key(), slot.Member).Err()
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
	return strconv.FormatInt(n, 10)
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
