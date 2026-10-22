package rediscache

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/egress"
)

// tls_cache_subscriber_scenario_test.go exercises tls_profile_cache.go and
// tls_router_cache.go through the lifecycle implemented in tls_cache_subscriber.go.
// The scenarios keep Redis available through callbacks and subscription cleanup,
// observing local invalidation, registration admission, and shutdown ordering.

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

// tlsCacheSubscriberScenario pairs each cache adapter with its notification channel.
type tlsCacheSubscriberScenario struct {
	name    string // cache lifecycle scenario name
	channel string // expected refresh publication channel
	// newCache seeds a local collection and returns a synchronized presence probe
	// so callbacks can observe local invalidation before executing service work.
	newCache func(*redis.Client) (stoppableTLSFingerprintCache, func() bool)
}

// TestTLSFingerprintCacheSubscriberStopsBeforeRedisClose registers profile and
// router subscribers against miniredis and observes a delivered callback. It
// then calls terminal stop twice before deferred Redis closure to protect
// admitted subscriber cleanup ordering.
func TestTLSFingerprintCacheSubscriberStopsBeforeRedisClose(t *testing.T) {
	for _, tt := range tlsCacheSubscriberScenarios() {
		t.Run(tt.name, func(t *testing.T) {
			server := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: server.Addr()})
			defer func() { _ = client.Close() }()

			cache, _ := tt.newCache(client)
			defer cache.StopSubscription()
			ctx := context.Background()
			notified := make(chan struct{}, 1)
			cache.SubscribeUpdates(ctx, func() { notified <- struct{}{} })

			// Wait for asynchronous subscription setup before publishing one refresh,
			// so callback delivery establishes the starting point for shutdown checks.
			require.Eventually(t, func() bool {
				return server.PubSubNumSub(tt.channel)[tt.channel] == 1
			}, time.Second, time.Millisecond)
			require.NoError(t, client.Publish(ctx, tt.channel, "refresh").Err())
			select {
			case <-notified:
			case <-time.After(time.Second):
				t.Fatal("callback did not receive the notification")
			}

			// Stop outside the callback while Redis remains available, then repeat
			// the wait to verify terminal shutdown can be requested more than once.
			cache.StopSubscription()
			cache.StopSubscription()
		})
	}
}

// TestTLSFingerprintCacheSubscriberStopBeforeStart seals an unused cache before
// registration is attempted. Repeated registration and stopping must complete
// without opening a Redis connection or admitting callback work.
func TestTLSFingerprintCacheSubscriberStopBeforeStart(t *testing.T) {
	for _, tt := range tlsCacheSubscriberScenarios() {
		t.Run(tt.name, func(t *testing.T) {
			server := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: server.Addr()})
			defer func() { _ = client.Close() }()
			cache, _ := tt.newCache(client)
			cache.StopSubscription()
			for range 3 {
				cache.SubscribeUpdates(context.Background(), func() {
					t.Error("stopped cache admitted a subscriber callback")
				})
				cache.StopSubscription()
			}
			require.Zero(t, server.TotalConnectionCount())
		})
	}
}

// TestTLSFingerprintCacheSubscriberNotifications checks duplicate registration
// while two refreshes reach the accepted callback. The first callback observes
// local invalidation, and the second callback waits for the first to return.
func TestTLSFingerprintCacheSubscriberNotifications(t *testing.T) {
	for _, tt := range tlsCacheSubscriberScenarios() {
		t.Run(tt.name, func(t *testing.T) {
			server := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: server.Addr()})
			defer func() { _ = client.Close() }()
			cache, localPresent := tt.newCache(client)
			require.True(t, localPresent())
			defer cache.StopSubscription()
			ctx := context.Background()
			entered := make(chan bool, 2)
			duplicate := make(chan struct{}, 2)
			release := make(chan struct{})
			var releaseOnce sync.Once
			defer releaseOnce.Do(func() { close(release) })

			// Hold the first callback after observing local state, allowing a second
			// notification to arrive while service refresh work is still in progress.
			cache.SubscribeUpdates(ctx, func() {
				entered <- localPresent()
				<-release
			})
			cache.SubscribeUpdates(ctx, func() { duplicate <- struct{}{} })
			require.Eventually(t, func() bool {
				return server.PubSubNumSub(tt.channel)[tt.channel] == 1
			}, time.Second, time.Millisecond)
			require.NoError(t, client.Publish(ctx, tt.channel, "first").Err())
			select {
			case present := <-entered:
				require.False(t, present, "local collection survived notification")
			case <-time.After(time.Second):
				t.Fatal("first callback did not start")
			}
			require.NoError(t, client.Publish(ctx, tt.channel, "second").Err())
			select {
			case <-entered:
				t.Fatal("callbacks overlapped")
			case <-time.After(30 * time.Millisecond):
			}
			releaseOnce.Do(func() { close(release) })
			select {
			case present := <-entered:
				require.False(t, present)
			case <-time.After(time.Second):
				t.Fatal("second callback did not start")
			}
			cache.StopSubscription()
			require.Empty(t, duplicate)
		})
	}
}

// TestTLSFingerprintCacheSubscriberWaitsForHandler blocks callback completion
// while two callers stop the subscription. Redis must remain usable by the
// callback until both stop callers can finish and client cleanup begins.
func TestTLSFingerprintCacheSubscriberWaitsForHandler(t *testing.T) {
	for _, tt := range tlsCacheSubscriberScenarios() {
		t.Run(tt.name, func(t *testing.T) {
			server := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: server.Addr()})
			defer func() { _ = client.Close() }()
			cache, _ := tt.newCache(client)
			defer cache.StopSubscription()
			ctx := context.Background()
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			var releaseOnce sync.Once
			defer releaseOnce.Do(func() { close(release) })
			handlerResult := make(chan error, 1)
			cache.SubscribeUpdates(ctx, func() {
				entered <- struct{}{}
				<-release
				handlerResult <- client.Ping(ctx).Err()
			})
			require.Eventually(t, func() bool {
				return server.PubSubNumSub(tt.channel)[tt.channel] == 1
			}, time.Second, time.Millisecond)
			require.NoError(t, client.Publish(ctx, tt.channel, "refresh").Err())
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("callback did not start")
			}

			// Model application cleanup after StopSubscription and a concurrent stop
			// caller, keeping both completion signals pending until callback release.
			closed := make(chan error, 1)
			stopped := make(chan struct{})
			go func() {
				cache.StopSubscription()
				closed <- client.Close()
			}()
			go func() {
				cache.StopSubscription()
				close(stopped)
			}()
			select {
			case <-closed:
				t.Fatal("Redis closed before callback completion")
			case <-stopped:
				t.Fatal("concurrent stop returned before callback completion")
			case <-time.After(30 * time.Millisecond):
			}
			releaseOnce.Do(func() { close(release) })
			select {
			case err := <-handlerResult:
				require.NoError(t, err)
			case <-time.After(time.Second):
				t.Fatal("callback did not finish")
			}
			select {
			case err := <-closed:
				require.NoError(t, err)
			case <-time.After(time.Second):
				t.Fatal("Redis cleanup did not finish")
			}
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("concurrent stop did not finish")
			}
			require.Eventually(t, func() bool {
				return server.PubSubNumSub(tt.channel)[tt.channel] == 0
			}, time.Second, time.Millisecond)
		})
	}
}

// TestTLSFingerprintCacheSubscriberConcurrentStartStop races registration with
// terminal stopping for both adapters. Concurrent stop callers account for
// admitted work, and final shutdown leaves no active Redis subscription.
func TestTLSFingerprintCacheSubscriberConcurrentStartStop(t *testing.T) {
	for _, tt := range tlsCacheSubscriberScenarios() {
		t.Run(tt.name, func(t *testing.T) {
			server := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: server.Addr()})
			defer func() { _ = client.Close() }()
			for range 20 {
				cache, _ := tt.newCache(client)
				start := make(chan struct{})
				var callers sync.WaitGroup
				// Release registration and shutdown together to exercise admission
				// ordering across many schedules while the Redis client remains open.
				for range 3 {
					callers.Go(func() {
						<-start
						cache.SubscribeUpdates(context.Background(), func() {})
					})
					callers.Go(func() {
						<-start
						cache.StopSubscription()
					})
				}
				close(start)
				callers.Wait()
				cache.StopSubscription()
			}
			require.Eventually(t, func() bool {
				return server.PubSubNumSub(tt.channel)[tt.channel] == 0
			}, time.Second, time.Millisecond)
		})
	}
}

// tlsCacheSubscriberScenarios constructs the profile and router lifecycle cases.
// Each constructor seeds a present empty local collection and supplies a probe
// that reads its presence under the adapter's local lock.
func tlsCacheSubscriberScenarios() []tlsCacheSubscriberScenario {
	return []tlsCacheSubscriberScenario{
		{
			name:    "profile",
			channel: tlsFPProfilePubSubKey,
			newCache: func(client *redis.Client) (stoppableTLSFingerprintCache, func() bool) {
				cache := &tlsFingerprintProfileCache{
					rdb:        client,
					localCache: []*egress.TLSFingerprintProfile{},
				}
				return cache, func() bool {
					cache.localMu.RLock()
					defer cache.localMu.RUnlock()
					return cache.localCache != nil
				}
			},
		},
		{
			name:    "router",
			channel: tlsFPRouterPubSubKey,
			newCache: func(client *redis.Client) (stoppableTLSFingerprintCache, func() bool) {
				cache := &tlsFingerprintRouterCache{
					rdb:        client,
					localCache: []*egress.TLSFingerprintRouter{},
				}
				return cache, func() bool {
					cache.localMu.RLock()
					defer cache.localMu.RUnlock()
					return cache.localCache != nil
				}
			},
		},
	}
}
