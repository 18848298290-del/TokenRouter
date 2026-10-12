package rediscache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// Profile and router subscribers from tls_profile_cache.go and tls_router_cache.go
// receive a notification while Redis remains available. The scenarios stop each
// subscriber from outside callback execution before client cleanup, checking the
// lifecycle ordering shared by those adapters.

// stoppableTLSFingerprintCache exposes tracked subscription registration and
// terminal shutdown for lifecycle scenarios.
type stoppableTLSFingerprintCache interface {
	// SubscribeUpdates registers the scenario callback for cache update messages
	// using its context. Implementations retain the subscriber until cancellation
	// or terminal shutdown, and the fixture keeps Redis available during that
	// lifetime.
	SubscribeUpdates(context.Context, func())
	// StopSubscription cancels the retained subscriber and waits for callback and
	// goroutine completion. The scenario calls it outside callback execution and
	// before its deferred Redis client close.
	StopSubscription()
}

// TestTLSFingerprintCacheSubscriberStopsBeforeRedisClose registers profile and
// router subscribers against miniredis and observes a delivered callback. It
// then calls terminal stop twice before deferred Redis closure to protect
// admitted subscriber cleanup ordering.
func TestTLSFingerprintCacheSubscriberStopsBeforeRedisClose(t *testing.T) {
	tests := []struct {
		name    string // cache lifecycle scenario name
		channel string // expected refresh publication channel
		// selected cache adapter constructor
		newCache func(*redis.Client) stoppableTLSFingerprintCache
	}{
		{
			name:    "profile",
			channel: tlsFPProfilePubSubKey,
			newCache: func(client *redis.Client) stoppableTLSFingerprintCache {
				return &tlsFingerprintProfileCache{rdb: client}
			},
		},
		{
			name:    "router",
			channel: tlsFPRouterPubSubKey,
			newCache: func(client *redis.Client) stoppableTLSFingerprintCache {
				return &tlsFingerprintRouterCache{rdb: client}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: server.Addr()})
			defer func() { _ = client.Close() }()

			cache := tt.newCache(client)
			ctx := context.Background()
			notified := make(chan struct{}, 1)
			cache.SubscribeUpdates(ctx, func() { notified <- struct{}{} })

			// Subscription registration launches asynchronously, so publish until
			// one message reaches the callback before testing shutdown completion.
			require.Eventually(t, func() bool {
				if err := client.Publish(ctx, tt.channel, "refresh").Err(); err != nil {
					return false
				}
				select {
				case <-notified:
					return true
				default:
					return false
				}
			}, time.Second, 10*time.Millisecond)

			// Stop outside the callback while Redis remains available, then repeat
			// the wait to verify terminal shutdown can be requested more than once.
			cache.StopSubscription()
			cache.StopSubscription()
		})
	}
}
