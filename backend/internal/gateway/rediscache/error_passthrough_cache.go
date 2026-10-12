package rediscache

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
)

const (
	errorPassthroughCacheKey  = "error_passthrough_rules"
	errorPassthroughPubSubKey = "error_passthrough_rules_updated"
	errorPassthroughCacheTTL  = 24 * time.Hour
)

type errorPassthroughCache struct {
	subscriptionMu      sync.Mutex
	subscriptionCancel  context.CancelFunc
	subscriptionStopped bool
	subscriptionWG      sync.WaitGroup
	rdb                 *redis.Client
	localCache          []*errorpolicy.ErrorPassthroughRule
	localMu             sync.RWMutex
}

// NewErrorPassthroughCache creates the Redis-backed rule cache.
func NewErrorPassthroughCache(rdb *redis.Client) errorpolicy.ErrorPassthroughCache {
	return &errorPassthroughCache{
		rdb: rdb,
	}
}

// Get returns independent rule copies from local cache or Redis; misses and read failures return false.
func (c *errorPassthroughCache) Get(ctx context.Context) ([]*errorpolicy.ErrorPassthroughRule, bool) {
	// The local cache avoids a Redis read when populated.
	c.localMu.RLock()
	if c.localCache != nil {
		rules := errorpolicy.CloneRules(c.localCache)
		c.localMu.RUnlock()
		return rules, true
	}
	c.localMu.RUnlock()

	// A local miss loads the shared Redis value.
	data, err := c.rdb.Get(ctx, errorPassthroughCacheKey).Bytes()
	if err != nil {
		if err != redis.Nil {
			log.Printf("[ErrorPassthroughCache] Failed to get from Redis: %v", err)
		}
		return nil, false
	}

	var rules []*errorpolicy.ErrorPassthroughRule
	if err := json.Unmarshal(data, &rules); err != nil {
		log.Printf("[ErrorPassthroughCache] Failed to unmarshal rules: %v", err)
		return nil, false
	}

	// Keep an independent local copy so callers can mutate their results.
	c.localMu.Lock()
	c.localCache = errorpolicy.CloneRules(rules)
	c.localMu.Unlock()

	return rules, true
}

// Set writes rules to Redis, then publishes an independent local copy.
func (c *errorPassthroughCache) Set(ctx context.Context, rules []*errorpolicy.ErrorPassthroughRule) error {
	rules = errorpolicy.CloneRules(rules)
	data, err := json.Marshal(rules)
	if err != nil {
		return err
	}

	if err := c.rdb.Set(ctx, errorPassthroughCacheKey, data, errorPassthroughCacheTTL).Err(); err != nil {
		return err
	}

	// Keep an independent local copy so callers can mutate their results.
	c.localMu.Lock()
	c.localCache = errorpolicy.CloneRules(rules)
	c.localMu.Unlock()

	return nil
}

// Invalidate clears local rules and deletes the shared Redis value.
func (c *errorPassthroughCache) Invalidate(ctx context.Context) error {
	// Clear local state even if the Redis deletion fails.
	c.localMu.Lock()
	c.localCache = nil
	c.localMu.Unlock()

	// Delete the shared cached value.
	return c.rdb.Del(ctx, errorPassthroughCacheKey).Err()
}

// NotifyUpdate asks other instances to refresh their caches.
func (c *errorPassthroughCache) NotifyUpdate(ctx context.Context) error {
	return c.rdb.Publish(ctx, errorPassthroughPubSubKey, "refresh").Err()
}

// SubscribeUpdates starts one update subscriber and rejects calls after StopSubscription.
func (c *errorPassthroughCache) SubscribeUpdates(ctx context.Context, handler func()) {
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
		sub := c.rdb.Subscribe(subscriberCtx, errorPassthroughPubSubKey)
		defer func() { _ = sub.Close() }()

		ch := sub.Channel()
		for {
			select {
			case <-subscriberCtx.Done():
				return
			case msg := <-ch:
				if msg == nil {
					return
				}
				// Clear the local value so subsequent reads reload rules.
				c.localMu.Lock()
				c.localCache = nil
				c.localMu.Unlock()

				// The handler can reload rules after invalidation.
				handler()
			}
		}
	}()
}

// StopSubscription cancels the subscriber and waits for any active callback to finish.
func (c *errorPassthroughCache) StopSubscription() {
	c.subscriptionMu.Lock()
	c.subscriptionStopped = true
	cancel := c.subscriptionCancel
	c.subscriptionMu.Unlock()
	if cancel != nil {
		cancel()
	}
	c.subscriptionWG.Wait()
}
