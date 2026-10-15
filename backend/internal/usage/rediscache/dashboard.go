package rediscache

import (
	"context"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// dashboard.go stores and retrieves serialized dashboard snapshots in Redis.
// Dashboard snapshots share one versioned Redis key within the configured
// prefix. The adapter stores serialized text verbatim, leaving freshness
// metadata to DashboardService and translating a missing key into the usage
// cache-miss sentinel used by its refresh path.

// dashboardStatsCacheKey identifies the serialized dashboard envelope shared
// by cache reads, refresh writes, and invalidation within each configured prefix.
const dashboardStatsCacheKey = "dashboard:stats:v2"

// DashboardCache stores serialized dashboard snapshots through a shared Redis
// client.
type DashboardCache struct {
	// rdb is supplied by app and remains owned by its lifecycle; constructing this
	// adapter does not open or close a client.
	rdb       *redis.Client // caller-owned shared Redis client
	keyPrefix string        // normalized dashboard key prefix
}

// NewDashboardCache binds a Redis client and normalizes the key prefix for
// dashboard snapshots. Prefix whitespace is trimmed, nonempty prefixes receive
// a trailing colon, and the caller owns client shutdown.
func NewDashboardCache(rdb *redis.Client, keyPrefix string) *DashboardCache {
	prefix := strings.TrimSpace(keyPrefix)
	if prefix != "" && !strings.HasSuffix(prefix, ":") {
		prefix += ":"
	}
	return &DashboardCache{
		rdb:       rdb,
		keyPrefix: prefix,
	}
}

// GetDashboardStats returns the serialized snapshot at this adapter's
// configured key. A Redis missing-key result becomes ErrDashboardStatsCacheMiss
// so DashboardService can load a fresh value; other read errors propagate.
func (c *DashboardCache) GetDashboardStats(ctx context.Context) (string, error) {
	val, err := c.rdb.Get(ctx, c.buildKey()).Result()
	if err != nil {
		// Absence is an ordinary cache miss for DashboardService. Translate that
		// sentinel separately so connection or server errors remain distinguishable.
		if err == redis.Nil {
			return "", usage.ErrDashboardStatsCacheMiss
		}
		return "", err
	}
	return val, nil
}

// SetDashboardStats writes the serialized dashboard text with the supplied
// Redis expiration. The adapter forwards ttl unchanged, leaving zero or
// negative expiration behavior to the Redis client and server.
func (c *DashboardCache) SetDashboardStats(ctx context.Context, data string, ttl time.Duration) error {
	return c.rdb.Set(ctx, c.buildKey(), data, ttl).Err()
}

// buildKey returns the versioned dashboard key with its normalized prefix.
func (c *DashboardCache) buildKey() string {
	if c.keyPrefix == "" {
		return dashboardStatsCacheKey
	}
	return c.keyPrefix + dashboardStatsCacheKey
}

// DeleteDashboardStats removes the shared snapshot at this adapter's configured
// key. Redis deletion is successful when the key is already absent, allowing
// settings changes and malformed-cache recovery to invalidate it repeatedly.
func (c *DashboardCache) DeleteDashboardStats(ctx context.Context) error {
	return c.rdb.Del(ctx, c.buildKey()).Err()
}
