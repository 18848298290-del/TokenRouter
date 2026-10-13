//go:build integration

package subscriptions_test

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	egressredis "github.com/TokenFlux/TokenRouter/internal/egress/rediscache"
	gatewayredis "github.com/TokenFlux/TokenRouter/internal/gateway/rediscache"
	"github.com/TokenFlux/TokenRouter/internal/testutil/rediscontainer"
)

// Redis cache subscription shutdown must wait for a synchronous update callback
// that is already running. The fixture blocks that callback for error rules,
// TLS profiles, and TLS routers, then samples whether StopSubscription returns
// before release and waits for its completion afterward.
//
// A buffered nonblocking arrival signal tells the publisher loop when one
// callback has entered, while a shared release channel keeps it active during
// the twenty-millisecond stop observation. Closing release lets subscriber work
// finish before the one-second join deadline. A repeated stop call checks the
// completed lifecycle path. The passing path releases and joins its callback;
// this fixture registers no separate release cleanup for earlier failures.

// stoppableTLSFingerprintCache supplies the synchronous subscription lifecycle
// exercised by the cache adapter matrix.
type stoppableTLSFingerprintCache interface {
	// SubscribeUpdates starts the adapter's first tracked Redis subscriber
	// with a nonnil context and synchronous handler. The handler must
	// return for stopped subscriber work to finish.
	SubscribeUpdates(context.Context, func())
	// StopSubscription stops new admission, cancels subscriber receiving,
	// and waits for an active callback. Invoke it outside that callback and
	// before closing the borrowed Redis client.
	StopSubscription()
}

// TestSubscriptionsWaitForInFlightCallback blocks an observed Redis callback
// for each selected cache adapter while requesting shutdown. Stop stays
// incomplete during the sample interval, finishes after callback release, and
// tolerates a repeated call.
func TestSubscriptionsWaitForInFlightCallback(t *testing.T) {
	for _, factory := range []struct {
		name, channel string // adapter case name; Redis update topic
		// make returns a new adapter borrowing the supplied Redis
		// client. The case owns subscription stop before Redis fixture
		// cleanup closes that shared client.
		make func(*redis.Client) stoppableTLSFingerprintCache
	}{
		{"error", "error_passthrough_rules_updated", func(c *redis.Client) stoppableTLSFingerprintCache {
			value, ok := gatewayredis.NewErrorPassthroughCache(c).(stoppableTLSFingerprintCache)
			require.True(t, ok)
			return value
		}},
		{"profile", "tls_fingerprint_profiles_updated", func(c *redis.Client) stoppableTLSFingerprintCache {
			value, ok := egressredis.NewTLSFingerprintProfileCache(c).(stoppableTLSFingerprintCache)
			require.True(t, ok)
			return value
		}},
		{"router", "tls_fingerprint_routers_updated", func(c *redis.Client) stoppableTLSFingerprintCache {
			value, ok := egressredis.NewTLSFingerprintRouterCache(c).(stoppableTLSFingerprintCache)
			require.True(t, ok)
			return value
		}},
	} {
		t.Run(factory.name, func(t *testing.T) {
			client := rediscontainer.New(t)
			cache := factory.make(client)
			// Signal one callback entry without blocking on
			// repeated notifications, then keep callback execution
			// parked until release opens during the normal shutdown
			// sequence.
			entered, release := make(chan struct{}, 1), make(chan struct{})
			cache.SubscribeUpdates(context.Background(), func() {
				select {
				case entered <- struct{}{}:
				default:
				}
				<-release
			})
			// Publish until callback entry is observed because
			// Redis subscription setup proceeds asynchronously; the
			// polling budget bounds this fixture's readiness
			// observation.
			require.Eventually(t, func() bool {
				require.NoError(t, client.Publish(context.Background(), factory.channel, "refresh").Err())
				select {
				case <-entered:
					return true
				default:
					return false
				}
			}, 2*time.Second, 10*time.Millisecond)
			// Request stop on another goroutine so the test can
			// observe its pending result while the synchronous
			// callback remains blocked on the release channel.
			stopped := make(chan struct{})
			go func() { cache.StopSubscription(); close(stopped) }()
			select {
			case <-stopped:
				t.Fatal("订阅回调尚未完成")
			case <-time.After(20 * time.Millisecond):
			}
			// Let the active callback return before joining stop,
			// demonstrating that receiving cancellation alone
			// cannot finish shutdown while callback work is still
			// parked.
			close(release)
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("订阅没有退出")
			}
			cache.StopSubscription()
		})
	}
}
