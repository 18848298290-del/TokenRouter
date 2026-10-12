package rediscache

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Search reservations increment a Redis counter keyed by supplier type, and
// configured proxy failures use separate expiring marker keys. Manager compares
// the returned counter against its limit and owns compensation after rejection
// or execution failure.
//
// The Lua increment applies expiration when its resulting count is one or the
// existing counter has no TTL, keeping that decision atomic with reservation.
// Decrement delegates directly to Redis and can recreate an expired key with a
// negative value. Usage maps a missing counter to zero, while proxy checks
// allow attempts on any storage read error.

// These key formats separate supplier call counters from configured-proxy
// failure markers shared by Managers across application instances.
const (
	quotaKeyPrefix      = "websearch:quota:"               // supplier counter key prefix
	proxyUnavailableKey = "websearch:proxy_unavailable:%d" // configured proxy marker key format
)

// quotaIncrScript increments usage and sets expiration when the resulting count
// is one or the existing counter lacks a TTL.
var quotaIncrScript = redis.NewScript(`
local val = redis.call('INCR', KEYS[1])
if val == 1 then
  redis.call('EXPIRE', KEYS[1], ARGV[1])
else
  local ttl = redis.call('TTL', KEYS[1])
  if ttl == -1 then
    redis.call('EXPIRE', KEYS[1], ARGV[1])
  end
end
return val
`)

// State implements search quota counters and configured-proxy markers over one
// Redis client.
type State struct {
	// rdb is the caller-owned Redis client used by every operation. Keep it open
	// until Manager searches and quota cleanup have finished. Command I/O deadline
	// enforcement follows the supplied client's ContextTimeoutEnabled option.
	rdb *redis.Client
}

// New retains the supplied Redis client for search counter and marker
// operations. The client must be nonnil and remain open until all searches and
// their cleanup finish.
func New(r *redis.Client) *State { return &State{rdb: r} }

// quotaRedisKey appends the literal supplier string to the shared counter
// prefix.
func quotaRedisKey(provider string) string { return quotaKeyPrefix + provider }

// Increment runs the atomic reservation script and returns its resulting
// supplier count. ttl is truncated to whole seconds, and storage errors leave
// the caller uncertain whether Redis applied the increment. Client retries can
// resend the command after a failed reply, so a successful count does not prove
// the script executed once.
func (s *State) Increment(ctx context.Context, provider string, ttl time.Duration) (int64, error) {
	// Pass whole-second expiration to Lua; subsecond durations become zero and can
	// expire the counter immediately after its reservation.
	return quotaIncrScript.Run(ctx, s.rdb, []string{quotaRedisKey(provider)}, int(ttl.Seconds())).Int64()
}

// Decrement sends Redis DECR for the literal supplier counter and returns its
// error. It can recreate a missing key at negative one; Manager decides whether
// this reservation is eligible for compensation. The client can resend DECR
// after a failed reply, allowing multiple applied decrements from this call.
func (s *State) Decrement(ctx context.Context, provider string) error {
	return s.rdb.Decr(ctx, quotaRedisKey(provider)).Err()
}

// Usage reads the literal supplier counter and reports missing keys as zero.
// Other read or numeric-conversion errors propagate to the caller, with their
// companion count determined by the Redis result.
func (s *State) Usage(ctx context.Context, provider string) (int64, error) {
	v, e := s.rdb.Get(ctx, quotaRedisKey(provider)).Int64()
	// Expose an expired or reset counter as zero usage so display and weighting
	// consumers can treat the supplier as unused.
	if e == redis.Nil {
		return 0, nil
	}
	return v, e
}

// Reset deletes the supplier's literal counter key and returns the Redis
// result. Deleting a missing key succeeds, and later reservations establish
// their own counter expiration.
func (s *State) Reset(ctx context.Context, provider string) error {
	return s.rdb.Del(ctx, quotaRedisKey(provider)).Err()
}

// MarkProxy stores an expiring nonempty marker for the configured proxy ID. A
// write error propagates so Manager can report that failure marking remains
// unconfirmed before trying another supplier.
func (s *State) MarkProxy(ctx context.Context, id int64, ttl time.Duration) error {
	return s.rdb.Set(ctx, fmt.Sprintf(proxyUnavailableKey, id), "1", ttl).Err()
}

// ProxyAvailable permits an attempt when marker reads fail or return an empty
// value. A successful nonempty marker read rejects the proxy; availability
// therefore follows a fail-open storage policy.
func (s *State) ProxyAvailable(ctx context.Context, id int64) bool {
	v, e := s.rdb.Get(ctx, fmt.Sprintf(proxyUnavailableKey, id)).Result()
	// Permit selection when Redis cannot establish a nonempty marker; storage
	// unavailability does not by itself remove a configured supplier proxy.
	return e != nil || v == ""
}
