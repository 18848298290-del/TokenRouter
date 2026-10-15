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
// local copy. Subscription registration and terminal stopping share a lifecycle
// mutex so StopSubscription observes admitted work before waiting.
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
// localMu protects localCache publication and cloning. subscriptionMu protects
// subscriptionCancel and subscriptionStopped while admitting subscriptionWG
// work, so terminal stopping cannot miss a subscriber registered before it.
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

	subscriptionMu      sync.Mutex         // subscriber lifecycle registration lock
	subscriptionCancel  context.CancelFunc // retained subscriber cancellation function
	subscriptionWG      sync.WaitGroup     // admitted subscriber completion tracking
	subscriptionStopped bool               // terminal subscriber admission flag
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

// SubscribeUpdates admits one tracked subscriber for TLS router refresh
// messages and returns after launching it. Repeated registration or terminal
// stopping rejects another subscriber; handler needs to be nonnil and return to
// permit progress.
//
// Registration stores cancellation and increments the wait group under
// subscriptionMu before launch, so StopSubscription can observe the admitted
// work. Each message clears local state before invoking handler synchronously.
// Context cancellation or channel closure ends the subscriber; its retained
// registration prevents restarting this adapter, and handler code cannot
// synchronously call StopSubscription on itself.
func (c *tlsFingerprintRouterCache) SubscribeUpdates(ctx context.Context, handler func()) {
	subscriberCtx, cancel := context.WithCancel(ctx)
	c.subscriptionMu.Lock()
	if c.subscriptionCancel != nil || c.subscriptionStopped {
		c.subscriptionMu.Unlock()
		cancel()
		return
	}
	// Register cancellation and wait-group membership before releasing admission
	// state, so a concurrent terminal stop waits for this accepted goroutine.
	c.subscriptionCancel = cancel
	c.subscriptionWG.Add(1)
	c.subscriptionMu.Unlock()

	go func() {
		defer c.subscriptionWG.Done()
		sub := c.rdb.Subscribe(subscriberCtx, tlsFPRouterPubSubKey)
		defer func() { _ = sub.Close() }()
		ch := sub.Channel()
		for {
			select {
			case <-subscriberCtx.Done():
				slog.Debug("tls_fp_router_cache_subscriber_stopped", "reason", "context_done")
				return
			case msg, ok := <-ch:
				if !ok || msg == nil {
					if subscriberCtx.Err() != nil {
						slog.Debug("tls_fp_router_cache_subscriber_stopped", "reason", "context_done")
						return
					}
					slog.Warn("tls_fp_router_cache_subscriber_stopped", "reason", "channel_closed")
					return
				}
				// Clear the current collection before invoking the service callback so a
				// refresh can discover updated data. An in-flight publisher may install a
				// local collection again before the callback performs its lookup.
				c.localMu.Lock()
				c.localCache = nil
				c.localMu.Unlock()
				handler()
			}
		}
	}()
}

// StopSubscription seals new subscription admission, cancels retained
// subscriber work, and waits for completion. Repeated calls share that
// lifetime, and an unregistered adapter stops immediately. Call it before
// closing Redis.
//
// The lifecycle mutex is released before waiting, allowing subscription cleanup
// to progress. Cancellation ends receive waiting but does not interrupt a
// synchronous handler; that handler needs to return for the wait group to
// complete. Calling this method from the same subscriber handler waits on
// itself, so application shutdown invokes it from outside callback execution.
func (c *tlsFingerprintRouterCache) StopSubscription() {
	c.subscriptionMu.Lock()
	cancel := c.subscriptionCancel
	c.subscriptionStopped = true
	c.subscriptionMu.Unlock()
	if cancel == nil {
		return
	}
	// Wait outside lifecycle admission after cancellation. A callback already
	// running still owns completion and must return before Redis can be closed.
	cancel()
	c.subscriptionWG.Wait()
}
