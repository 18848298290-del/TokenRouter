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

const (
	tlsFPRouterCacheKey  = "tls_fingerprint_routers"
	tlsFPRouterPubSubKey = "tls_fingerprint_routers_updated"
	tlsFPRouterCacheTTL  = 24 * time.Hour
)

type tlsFingerprintRouterCache struct {
	rdb        *redis.Client
	localCache []*egress.TLSFingerprintRouter
	localMu    sync.RWMutex

	// The cache owns subscriber cancellation; stop the subscriber before closing Redis.
	subscriptionMu      sync.Mutex
	subscriptionCancel  context.CancelFunc
	subscriptionWG      sync.WaitGroup
	subscriptionStopped bool
}

// NewTLSFingerprintRouterCache creates a TLS router cache.
func NewTLSFingerprintRouterCache(rdb *redis.Client) egress.TLSFingerprintRouterCache {
	return &tlsFingerprintRouterCache{rdb: rdb}
}

// Get returns independent router copies from local cache or Redis and reports a cache hit.
func (c *tlsFingerprintRouterCache) Get(ctx context.Context) ([]*egress.TLSFingerprintRouter, bool) {
	c.localMu.RLock()
	if c.localCache != nil {
		routers := egress.CloneTLSRouters(c.localCache)
		c.localMu.RUnlock()
		return routers, true
	}
	c.localMu.RUnlock()

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

	c.localMu.Lock()
	c.localCache = egress.CloneTLSRouters(routers)
	c.localMu.Unlock()
	return routers, true
}

// Set writes routers to Redis and stores an independent local copy.
func (c *tlsFingerprintRouterCache) Set(ctx context.Context, routers []*egress.TLSFingerprintRouter) error {
	data, err := json.Marshal(routers)
	if err != nil {
		return err
	}
	if err := c.rdb.Set(ctx, tlsFPRouterCacheKey, data, tlsFPRouterCacheTTL).Err(); err != nil {
		return err
	}
	c.localMu.Lock()
	c.localCache = egress.CloneTLSRouters(routers)
	c.localMu.Unlock()
	return nil
}

// Invalidate clears the local and Redis router caches.
func (c *tlsFingerprintRouterCache) Invalidate(ctx context.Context) error {
	c.localMu.Lock()
	c.localCache = nil
	c.localMu.Unlock()
	return c.rdb.Del(ctx, tlsFPRouterCacheKey).Err()
}

// NotifyUpdate publishes a router refresh notification to subscribed instances.
func (c *tlsFingerprintRouterCache) NotifyUpdate(ctx context.Context) error {
	return c.rdb.Publish(ctx, tlsFPRouterPubSubKey, "refresh").Err()
}

// SubscribeUpdates starts one subscriber that clears local routers before calling handler.
func (c *tlsFingerprintRouterCache) SubscribeUpdates(ctx context.Context, handler func()) {
	subscriberCtx, cancel := context.WithCancel(ctx)
	c.subscriptionMu.Lock()
	if c.subscriptionCancel != nil || c.subscriptionStopped {
		c.subscriptionMu.Unlock()
		cancel()
		return
	}
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
				c.localMu.Lock()
				c.localCache = nil
				c.localMu.Unlock()
				handler()
			}
		}
	}()
}

// StopSubscription prevents new subscriptions, cancels the subscriber, and waits for its exit.
func (c *tlsFingerprintRouterCache) StopSubscription() {
	c.subscriptionMu.Lock()
	cancel := c.subscriptionCancel
	c.subscriptionStopped = true
	c.subscriptionMu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	c.subscriptionWG.Wait()
}
