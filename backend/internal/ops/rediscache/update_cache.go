package rediscache

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/ops"
)

// Release checks share a Redis value containing the latest version, release
// metadata, and write timestamp. Redis expiration limits stored availability,
// while ReleaseQuery also checks timestamp age when decoding the cached
// response for its own running version.

// updateCacheKey names the shared latest-release value read by all ReleaseQuery
// instances using this Redis database and retained for their supplied
// expiration lifetime.
const updateCacheKey = "update:latest"

// updateCache stores the serialized latest-release response in the application
// Redis database.
type updateCache struct {
	// rdb is borrowed from app and remains open while release queries use this
	// adapter. Application shutdown closes the shared client after its consumers
	// stop.
	rdb *redis.Client
}

// NewUpdateCache creates a release-cache adapter using the supplied Redis
// client. The client must be nonnil before operations run, and app retains
// responsibility for its lifetime.
func NewUpdateCache(rdb *redis.Client) ops.UpdateCache {
	return &updateCache{rdb: rdb}
}

// GetUpdateInfo reads the shared serialized latest-release value for
// ReleaseQuery. A missing key returns redis.Nil; the query service selects its
// network or warning response path after a failed read and checks timestamp age
// after decoding a stored value.
func (c *updateCache) GetUpdateInfo(ctx context.Context) (string, error) {
	return c.rdb.Get(ctx, updateCacheKey).Result()
}

// SetUpdateInfo replaces the shared release-cache value using ttl as its Redis
// lifetime. Zero ttl stores the value indefinitely, while ReleaseQuery
// separately checks the serialized timestamp when reading.
func (c *updateCache) SetUpdateInfo(ctx context.Context, data string, ttl time.Duration) error {
	return c.rdb.Set(ctx, updateCacheKey, data, ttl).Err()
}
