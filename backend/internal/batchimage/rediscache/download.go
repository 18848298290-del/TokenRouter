package rediscache

import (
	"context"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/batchimage"
)

const (
	defaultBatchImageDownloadActivePrefix = "batch_image:download:active:"
	defaultBatchImageDownloadActiveTTL    = 10 * time.Minute
	defaultBatchImageDownloadConcurrency  = 2
)

var (
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

	batchImageDownloadReleaseScript = redis.NewScript(`
local current = tonumber(redis.call("GET", KEYS[1]) or "0")
if current <= 1 then
  redis.call("DEL", KEYS[1])
  return 0
end
return redis.call("DECR", KEYS[1])
`)

	_ batchimage.BatchImageDownloadLimiter = (*batchImageDownloadLimiter)(nil)
	_ batchimage.BatchImageDownloadPermit  = (*batchImageDownloadPermit)(nil)
)

type batchImageDownloadLimiter struct {
	rdb          *redis.Client
	activePrefix string
	maxActive    int
	ttl          time.Duration
}

type batchImageDownloadPermit struct {
	rdb  *redis.Client
	key  string
	once sync.Once
	err  error
}

// DownloadOptions configures per-user concurrency and the active-counter expiry.
type DownloadOptions struct {
	MaxDownloadConcurrencyPerUser int
	MaxDownloadDurationSeconds    int
}

func NewBatchImageDownloadLimiter(rdb *redis.Client, cfg *DownloadOptions) batchimage.BatchImageDownloadLimiter {
	maxActive := defaultBatchImageDownloadConcurrency
	ttl := defaultBatchImageDownloadActiveTTL
	if cfg != nil {
		if cfg.MaxDownloadConcurrencyPerUser > 0 {
			maxActive = cfg.MaxDownloadConcurrencyPerUser
		}
		if cfg.MaxDownloadDurationSeconds > 0 {
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

func (l *batchImageDownloadLimiter) Acquire(ctx context.Context, userID string, kind string) (batchimage.BatchImageDownloadPermit, error) {
	if l == nil || l.rdb == nil {
		return nil, batchimage.ErrBatchImageDownloadLimited
	}
	key := l.activeKey(userID)
	ok, err := batchImageDownloadAcquireScript.Run(ctx, l.rdb, []string{key}, l.maxActive, int(l.ttl.Seconds())).Int()
	if err != nil {
		return nil, err
	}
	if ok != 1 {
		return nil, batchimage.ErrBatchImageDownloadLimited
	}
	return &batchImageDownloadPermit{rdb: l.rdb, key: key}, nil
}

func (l *batchImageDownloadLimiter) activeKey(userID string) string {
	return l.activePrefix + userID
}

func (p *batchImageDownloadPermit) Release(ctx context.Context) error {
	if p == nil || p.rdb == nil || p.key == "" {
		return nil
	}
	p.once.Do(func() {
		_, p.err = batchImageDownloadReleaseScript.Run(ctx, p.rdb, []string{p.key}).Result()
	})
	return p.err
}
