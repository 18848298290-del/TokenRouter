package rediscache

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/egress"
)

// tls_router_cache.go caches TLS routers locally and in Redis and manages
// update subscriptions.
// TLS routers can be reused locally while Redis stores a serialized collection
// and delivers refresh notifications across instances. The Redis entry has a
// twenty-four-hour lifetime, but an installed local collection has no
// elapsed-time expiry and remains until replacement or invalidation.
//
// Reads return cloned values, and writes serialize before publishing a separate
// local copy. A tlsCacheSubscriber tracks notification work through terminal
// shutdown so StopSubscription waits for admitted callbacks and Pub/Sub cleanup.
// Notifications clear the local collection before invoking the handler
// synchronously. Handler work therefore delays subscription completion, and
// callers stop the subscriber before closing Redis; a handler cannot
// synchronously stop its own subscription without waiting on itself.

// TLS router cache keys distinguish serialized collection storage from refresh
// notifications, and the lifetime applies to the Redis entry.
const (
	tlsFPRouterCacheKey  = "tls_fingerprint_routers"
	tlsFPRouterPubSubKey = "tls_fingerprint_routers_updated"
	tlsFPRouterCacheTTL  = 24 * time.Hour
)

// tlsFingerprintRouterCache retains local TLS routers and one tracked Redis
// refresh subscription.
//
// localMu protects localCache publication and cloning. subscriber owns refresh
// registration and completion, while this cache clears its local collection
// before invoking the service handler.
type tlsFingerprintRouterCache struct {
	// rdb remains available for cache commands and subscriber callbacks until
	// StopSubscription finishes. The application owns closing the client after the
	// retained subscriber exits.
	rdb *redis.Client // borrowed application Redis client
	// localCache is cloned during reads and publication and has no time-based
	// local expiry. A nil value selects a Redis read; a present empty slice is a
	// local hit.
	localCache []*egress.TLSFingerprintRouter // owned local configuration collection
	localMu    sync.RWMutex                   // local value publication lock

	// subscriber owns refresh admission and completion for this adapter. Its zero
	// value accepts one registration, and StopSubscription seals admission
	// permanently even when no registration has occurred.
	subscriber tlsCacheSubscriber
}

// NewTLSFingerprintRouterCache constructs an empty adapter using the supplied
// Redis client. The adapter borrows that client, and the application stops its
// subscription before closing Redis; construction starts no subscriber.
func NewTLSFingerprintRouterCache(rdb *redis.Client) egress.TLSFingerprintRouterCache {
	return &tlsFingerprintRouterCache{rdb: rdb}
}

// Get returns separate cloned TLS routers from a present local collection or
// decoded Redis data and reports a hit. Redis absence, command failure, or
// malformed JSON returns false, with non-missing failures logged.
//
// A local collection has no TTL check; notification or explicit invalidation
// clears it. Redis decoding publishes a clone before returning the decoded
// values, so later caller mutations leave local storage unchanged. An
// in-progress Redis read can complete after another operation invalidates local
// state; this method serializes publication, not the entire remote-read
// lifecycle.
func (c *tlsFingerprintRouterCache) Get(ctx context.Context) ([]*egress.TLSFingerprintRouter, bool) {
	c.localMu.RLock()
	if c.localCache != nil {
		routers := egress.CloneTLSRouters(c.localCache)
		c.localMu.RUnlock()
		return routers, true
	}
	c.localMu.RUnlock()

	// Local absence requires a distributed read. Command and decoding failures
	// become misses so the service can use its authoritative repository path.
	data, err := c.rdb.Get(ctx, tlsFPRouterCacheKey).Bytes()
	if err != nil {
		if err != redis.Nil {
			slog.Warn("tls_fp_router_cache_get_failed", "error", err)
		}
		return nil, false
	}

	var routers []*egress.TLSFingerprintRouter
	if err := json.Unmarshal(data, &routers); err != nil {
		slog.Warn("tls_fp_router_cache_unmarshal_failed", "error", err)
		return nil, false
	}

	// Publish an owned clone of decoded values before returning the decoded
	// collection, keeping caller mutations separate from local cache storage.
	c.localMu.Lock()
	c.localCache = egress.CloneTLSRouters(routers)
	c.localMu.Unlock()
	return routers, true
}

// Set serializes supplied TLS routers, writes their Redis collection, and
// publishes a separate cloned local value after Redis succeeds. Serialization
// and Redis failures return before local replacement. Callers keep input nested
// values stable throughout serialization and cloning.
//
// Redis TTL governs the distributed entry; the installed local collection has no
// elapsed-time expiry. The local lock covers replacement, not the remote write
// lifecycle, so competing reads, writes, or invalidations can complete between
// those phases. Successful return establishes this write outcome without
// enforcing latest-version ordering across concurrent publishers.
func (c *tlsFingerprintRouterCache) Set(ctx context.Context, routers []*egress.TLSFingerprintRouter) error {
	data, err := json.Marshal(routers)
	if err != nil {
		return err
	}
	if err := c.rdb.Set(ctx, tlsFPRouterCacheKey, data, tlsFPRouterCacheTTL).Err(); err != nil {
		return err
	}
	// Publish only after the distributed write succeeds, so this local update
	// does not advertise a collection whose Set returned a Redis failure.
	c.localMu.Lock()
	c.localCache = egress.CloneTLSRouters(routers)
	c.localMu.Unlock()
	return nil
}

// Invalidate clears local TLS routers before deleting the Redis collection.
// Local removal occurs under localMu, then the remote command runs after that
// lock is released. Its error is returned even though that local removal has
// already happened.
//
// A failed remote delete can leave distributed values available for later reads.
// In-flight remote reads or writes can also publish their results after local
// clearing, so this method does not promise that local state remains absent
// under competing publishers. Service refresh and notification use their own
// subsequent operations.
func (c *tlsFingerprintRouterCache) Invalidate(ctx context.Context) error {
	c.localMu.Lock()
	// Clear local reuse before the Redis command. This local invalidation is
	// recorded even when distributed deletion fails; in-flight publishers acquire
	// the same local lock separately.
	c.localCache = nil
	c.localMu.Unlock()
	return c.rdb.Del(ctx, tlsFPRouterCacheKey).Err()
}

// NotifyUpdate publishes a TLS router refresh message to the configured Redis
// channel.
func (c *tlsFingerprintRouterCache) NotifyUpdate(ctx context.Context) error {
	return c.rdb.Publish(ctx, tlsFPRouterPubSubKey, "refresh").Err()
}

// SubscribeUpdates registers TLS router refresh notifications and returns after
// launching the subscriber. Each notification clears the local collection
// before invoking handler synchronously; handler must be nonnil and return for
// later notifications and shutdown to proceed. Duplicate registrations and
// registrations after StopSubscription are ignored.
func (c *tlsFingerprintRouterCache) SubscribeUpdates(ctx context.Context, handler func()) {
	c.subscriber.start(ctx, c.rdb, tlsFPRouterPubSubKey, "tls_fp_router_cache_subscriber_stopped", func() {
		// Clear the current collection before invoking the service callback so a
		// refresh can discover updated data. An in-flight publisher may install a
		// local collection again before the callback performs its lookup.
		c.localMu.Lock()
		c.localCache = nil
		c.localMu.Unlock()
		handler()
	})
}

// StopSubscription cancels the retained subscriber and waits for callback and
// Pub/Sub completion. Call it before closing Redis and outside callback
// execution, since a callback that stops itself waits on its own completion.
// Repeated calls wait for the same subscriber lifetime, and stopping an unused
// cache permanently rejects later registration.
func (c *tlsFingerprintRouterCache) StopSubscription() {
	c.subscriber.stop()
}
