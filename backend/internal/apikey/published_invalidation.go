package apikey

import (
	"context"
)

// PublishedAuthCacheInvalidator deletes the shared cache entry, then publishes its key for local invalidation.
type PublishedAuthCacheInvalidator struct{ Cache APIKeyCache }

func (p PublishedAuthCacheInvalidator) InvalidateAuthCacheByKey(ctx context.Context, key string) {
	cacheKey := AuthCacheKey(key)
	_ = p.Cache.DeleteAuthCache(ctx, cacheKey)
	_ = p.Cache.PublishAuthCacheInvalidation(ctx, cacheKey)
}
