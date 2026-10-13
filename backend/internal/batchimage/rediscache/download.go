package rediscache

import (
	"context"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/batchimage"
)

// Image and ZIP downloads consume the same active counter for one user.
// Acquiring a permit checks the limit, increments the counter, and refreshes
// its expiry in one Redis script, so simultaneous requests cannot each admit
// themselves from the same observed count.
//
// The counter expiry recovers capacity after a caller disappears. A permit uses
// sync.Once to start one Script.Run call and retains its result for repeated
// Release calls. Go-redis can repeat server execution after an uncertain reply,
// so that one invocation can apply more than one decrement. Counter expiry and
// permit release also have separate lifetimes: a permit carries a user key,
// and its first release after expiry acts on the counter then occupying that key.

// Download defaults select the per-user counter namespace, its recovery
// expiry, and the concurrency limit used when configuration supplies no
// positive overrides.
const (
	defaultBatchImageDownloadActivePrefix = "batch_image:download:active:"
	defaultBatchImageDownloadActiveTTL    = 10 * time.Minute
	defaultBatchImageDownloadConcurrency  = 2
)

var (
	// batchImageDownloadAcquireScript admits a user when its counter is below the
	// limit, then increments and renews expiry within that same Redis operation.
	batchImageDownloadAcquireScript = redis.NewScript(`
local current = tonumber(redis.call("GET", KEYS[1]) or "0")
local max = tonumber(ARGV[1])
if current >= max then
  return 0
end
redis.call("INCR", KEYS[1])
redis.call("EXPIRE", KEYS[1], ARGV[2])
return 1
`)

	// batchImageDownloadReleaseScript decrements the current user counter or
	// removes its final entry. Client retries can repeat this mutation after an
	// uncertain reply, independently of the permit's sync.Once guard.
	batchImageDownloadReleaseScript = redis.NewScript(`
local current = tonumber(redis.call("GET", KEYS[1]) or "0")
if current <= 1 then
  redis.call("DEL", KEYS[1])
  return 0
end
return redis.call("DECR", KEYS[1])
`)

	// These assertions keep the Redis limiter and its permits compatible with
	// the download use case's admission and release interfaces.
	_ batchimage.BatchImageDownloadLimiter = (*batchImageDownloadLimiter)(nil)
	_ batchimage.BatchImageDownloadPermit  = (*batchImageDownloadPermit)(nil)
)

// batchImageDownloadLimiter counts concurrent downloads per user through an
// expiring Redis counter.
type batchImageDownloadLimiter struct {
	// rdb remains owned by the constructor's caller. Keep that client available
	// until limiter operations and the permits returned from them have finished.
	rdb          *redis.Client // borrowed Redis connection client
	activePrefix string        // per-user counter key prefix
	maxActive    int           // maximum simultaneous user downloads
	ttl          time.Duration // counter recovery expiry duration
}

// batchImageDownloadPermit invokes its release script once through the Redis
// client and shares the returned result with repeated callers.
type batchImageDownloadPermit struct {
	// rdb shares the limiter's borrowed client. A first Release still needs that
	// client after counter expiry, so shutdown follows outstanding permit use.
	rdb *redis.Client // borrowed Redis connection client
	key string        // admitted user counter key
	// once and err retain the first Script.Run invocation and its returned result.
	// Repeated Release calls share that error; retries inside the Redis client
	// remain governed by the supplied client's command policy.
	once sync.Once // single release attempt guard
	err  error     // first Redis release error
}

// DownloadOptions supplies positive per-user concurrency and counter expiry
// overrides when constructing the limiter.
type DownloadOptions struct {
	// MaxDownloadConcurrencyPerUser overrides the default when positive. Acquire
	// applies that limit to the combined image and ZIP counter for each user.
	MaxDownloadConcurrencyPerUser int // positive active-download limit override
	// MaxDownloadDurationSeconds overrides counter expiry when positive.
	// The limiter uses it for Redis recovery; download stream deadlines belong
	// to the download use case.
	MaxDownloadDurationSeconds int // seconds
}

// NewBatchImageDownloadLimiter retains the supplied Redis client and configures
// per-user download counters. Nil or nonpositive overrides select two active
// downloads and a ten-minute expiry; the client owner controls its shutdown.
func NewBatchImageDownloadLimiter(rdb *redis.Client, cfg *DownloadOptions) batchimage.BatchImageDownloadLimiter {
	maxActive := defaultBatchImageDownloadConcurrency
	ttl := defaultBatchImageDownloadActiveTTL
	if cfg != nil {
		if cfg.MaxDownloadConcurrencyPerUser > 0 {
			maxActive = cfg.MaxDownloadConcurrencyPerUser
		}
		if cfg.MaxDownloadDurationSeconds > 0 {
			// Convert configured seconds to the counter's expiry duration.
			ttl = time.Duration(cfg.MaxDownloadDurationSeconds) * time.Second
		}
	}
	return &batchImageDownloadLimiter{
		rdb:          rdb,
		activePrefix: defaultBatchImageDownloadActivePrefix,
		maxActive:    maxActive,
		ttl:          ttl,
	}
}

// Acquire admits a download against the user's current Redis counter and
// returns a permit for releasing capacity. Download kinds share that counter;
// a missing client or exhausted limit returns ErrBatchImageDownloadLimited.
//
// Admission tests the count and renews expiry together with incrementing it.
// Redis errors propagate before a permit is returned, but an uncertain reply
// can leave increments applied or replayed by the client's retry policy.
// Callers release a returned permit after consuming or closing the output;
// counter expiry recovers capacity when an increment has no returned permit.
func (l *batchImageDownloadLimiter) Acquire(ctx context.Context, userID string, kind string) (batchimage.BatchImageDownloadPermit, error) {
	if l == nil || l.rdb == nil {
		return nil, batchimage.ErrBatchImageDownloadLimited
	}
	key := l.activeKey(userID)
	// Check admission and increment the counter together so concurrent callers
	// cannot both consume its final free slot. Each accepted request renews the
	// recovery expiry, passed as whole seconds for Redis EXPIRE.
	ok, err := batchImageDownloadAcquireScript.Run(ctx, l.rdb, []string{key}, l.maxActive, int(l.ttl.Seconds())).Int()
	if err != nil {
		return nil, err
	}
	if ok != 1 {
		return nil, batchimage.ErrBatchImageDownloadLimited
	}
	return &batchImageDownloadPermit{rdb: l.rdb, key: key}, nil
}

// activeKey appends the user identifier to the download counter namespace.
func (l *batchImageDownloadLimiter) activeKey(userID string) string {
	return l.activePrefix + userID
}

// Release invokes this permit's release script once through its Redis client
// and returns that result on later calls. Incomplete permits are no-ops; a failed
// invocation is cached, leaving counter expiry to recover retained capacity.
// The client's own retry policy can repeat server execution after an uncertain
// reply, so sync.Once limits invocations from this permit.
func (p *batchImageDownloadPermit) Release(ctx context.Context) error {
	if p == nil || p.rdb == nil || p.key == "" {
		return nil
	}
	// Reuse this invocation's result on later Release calls so repeated stream
	// closure does not start another Script.Run. The Redis client's internal retry
	// policy still applies within that first invocation.
	p.once.Do(func() {
		_, p.err = batchImageDownloadReleaseScript.Run(ctx, p.rdb, []string{p.key}).Result()
	})
	return p.err
}
