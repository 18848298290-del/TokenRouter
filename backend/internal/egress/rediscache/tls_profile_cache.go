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
	tlsFPProfileCacheKey  = "tls_fingerprint_profiles"
	tlsFPProfilePubSubKey = "tls_fingerprint_profiles_updated"
	tlsFPProfileCacheTTL  = 24 * time.Hour
)

type tlsFingerprintProfileCache struct {
	rdb        *redis.Client
	localCache []*egress.TLSFingerprintProfile
	localMu    sync.RWMutex

	// The cache owns subscriber cancellation; stop the subscriber before closing Redis.
	subscriptionMu      sync.Mutex
	subscriptionCancel  context.CancelFunc
	subscriptionWG      sync.WaitGroup
	subscriptionStopped bool
}

// NewTLSFingerprintProfileCache creates a TLS profile cache.
func NewTLSFingerprintProfileCache(rdb *redis.Client) egress.TLSFingerprintProfileCache {
	return &tlsFingerprintProfileCache{
		rdb: rdb,
	}
}

// Get returns independent profile copies from local cache or Redis and reports a cache hit.
func (c *tlsFingerprintProfileCache) Get(ctx context.Context) ([]*egress.TLSFingerprintProfile, bool) {
	c.localMu.RLock()
	if c.localCache != nil {
		profiles := egress.CloneTLSProfiles(c.localCache)
		c.localMu.RUnlock()
		return profiles, true
	}
	c.localMu.RUnlock()

	data, err := c.rdb.Get(ctx, tlsFPProfileCacheKey).Bytes()
	if err != nil {
		if err != redis.Nil {
			slog.Warn("tls_fp_profile_cache_get_failed", "error", err)
		}
		return nil, false
	}

	var profiles []*egress.TLSFingerprintProfile
	if err := json.Unmarshal(data, &profiles); err != nil {
		slog.Warn("tls_fp_profile_cache_unmarshal_failed", "error", err)
		return nil, false
	}

	c.localMu.Lock()
	c.localCache = egress.CloneTLSProfiles(profiles)
	c.localMu.Unlock()

	return profiles, true
}

// Set writes profiles to Redis and stores an independent local copy.
func (c *tlsFingerprintProfileCache) Set(ctx context.Context, profiles []*egress.TLSFingerprintProfile) error {
	data, err := json.Marshal(profiles)
	if err != nil {
		return err
	}

	if err := c.rdb.Set(ctx, tlsFPProfileCacheKey, data, tlsFPProfileCacheTTL).Err(); err != nil {
		return err
	}

	c.localMu.Lock()
	c.localCache = egress.CloneTLSProfiles(profiles)
	c.localMu.Unlock()

	return nil
}

// Invalidate clears the local and Redis profile caches.
func (c *tlsFingerprintProfileCache) Invalidate(ctx context.Context) error {
	c.localMu.Lock()
	c.localCache = nil
	c.localMu.Unlock()

	return c.rdb.Del(ctx, tlsFPProfileCacheKey).Err()
}

// NotifyUpdate publishes a profile refresh notification to subscribed instances.
func (c *tlsFingerprintProfileCache) NotifyUpdate(ctx context.Context) error {
	return c.rdb.Publish(ctx, tlsFPProfilePubSubKey, "refresh").Err()
}

// SubscribeUpdates starts one subscriber that clears local profiles before calling handler.
func (c *tlsFingerprintProfileCache) SubscribeUpdates(ctx context.Context, handler func()) {
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
		sub := c.rdb.Subscribe(subscriberCtx, tlsFPProfilePubSubKey)
		defer func() { _ = sub.Close() }()

		ch := sub.Channel()
		for {
			select {
			case <-subscriberCtx.Done():
				slog.Debug("tls_fp_profile_cache_subscriber_stopped", "reason", "context_done")
				return
			case msg, ok := <-ch:
				if !ok || msg == nil {
					if subscriberCtx.Err() != nil {
						slog.Debug("tls_fp_profile_cache_subscriber_stopped", "reason", "context_done")
						return
					}
					slog.Warn("tls_fp_profile_cache_subscriber_stopped", "reason", "channel_closed")
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
func (c *tlsFingerprintProfileCache) StopSubscription() {
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
