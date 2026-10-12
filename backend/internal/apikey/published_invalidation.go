package apikey

import (
	"context"
)

// App-level callers can invalidate an authentication credential through the
// shared cache even when they do not own an APIKeyService operation. The hashed
// credential is deleted before publication, and each attempted cache call
// returns no error through this invalidator interface.

// PublishedAuthCacheInvalidator deletes shared authentication entries and
// publishes their invalidation identities.
type PublishedAuthCacheInvalidator struct {
	// Cache supplies a usable shared cache. The invalidator retains this
	// dependency, and its caller coordinates use with cache resource shutdown.
	Cache APIKeyCache
}

// InvalidateAuthCacheByKey hashes key, deletes its shared entry, and publishes
// that cache identity. Callers supply a usable Cache and coordinate its
// lifetime; deletion failure still permits a publication attempt, and both
// errors are suppressed.
func (p PublishedAuthCacheInvalidator) InvalidateAuthCacheByKey(ctx context.Context, key string) {
	cacheKey := AuthCacheKey(key)
	// Attempt both operations independently because subscribers can clear L1
	// even when this shared deletion fails; durable delivery retries separate work.
	_ = p.Cache.DeleteAuthCache(ctx, cacheKey)
	_ = p.Cache.PublishAuthCacheInvalidation(ctx, cacheKey)
}
