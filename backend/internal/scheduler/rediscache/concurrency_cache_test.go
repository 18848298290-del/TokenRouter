package rediscache

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// Live leases temporarily overlap regular reservations during session
// creation, then remain counted against ordinary provider/user admission.
// Unit scenarios check transfer and expiry, while an optional Redis benchmark
// compares counted sorted-set members with occurrences returned by SCAN at
// fixed fixture sizes.

// benchSlotTTLMinutes selects the ordinary activity window used to prune
// seeded members during timed counts; extended runs can outlive those
// fixtures.
const benchSlotTTLMinutes = 15

// benchSlotTTL supplies expiry for benchmark fixtures so seeded sorted sets
// and individual SCAN keys share the same configured retention interval.
var benchSlotTTL = time.Duration(benchSlotTTLMinutes) * time.Minute

// BenchmarkProviderConcurrency measures sorted-set counting and SCAN counting
// at three seeded slot sizes on TEST_REDIS_URL. Member seeding and deletion
// run with timers stopped; results describe that configured Redis workload.
func BenchmarkProviderConcurrency(b *testing.B) {
	rdb := newBenchmarkRedisClient(b)
	// Close the benchmark-owned client when its workload finishes; teardown
	// discards close errors so measured command failures remain the benchmark's
	// reported failure.
	defer func() {
		_ = rdb.Close()
	}()

	cache, _ := NewConcurrencyCache(rdb, benchSlotTTLMinutes, int(benchSlotTTL.Seconds())).(*concurrencyCache)
	ctx := context.Background()

	for _, size := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("zset/slots=%d", size), func(b *testing.B) {
			providerID := time.Now().UnixNano()
			key := providerSlotKey(providerID)

			// Seed the sorted-set members outside timing so the counted workload
			// starts with its requested number of existing ordinary request members.
			b.StopTimer()
			members := make([]redis.Z, 0, size)
			now := float64(time.Now().Unix())
			for i := range size {
				members = append(members, redis.Z{
					Score:  now,
					Member: fmt.Sprintf("req_%d", i),
				})
			}
			if err := rdb.ZAdd(ctx, key, members...).Err(); err != nil {
				b.Fatalf("初始化有序集合失败: %v", err)
			}
			if err := rdb.Expire(ctx, key, benchSlotTTL).Err(); err != nil {
				b.Fatalf("设置有序集合 TTL 失败: %v", err)
			}
			b.StartTimer()

			b.ReportAllocs()
			for range b.N {
				if _, err := cache.GetProviderConcurrency(ctx, providerID); err != nil {
					b.Fatalf("获取并发数量失败: %v", err)
				}
			}

			// Delete the seeded sorted set after stopping timing so cleanup of this
			// benchmark's member fixture does not contribute to the count
			// measurements.
			b.StopTimer()
			if err := rdb.Del(ctx, key).Err(); err != nil {
				b.Fatalf("清理有序集合失败: %v", err)
			}
		})

		b.Run(fmt.Sprintf("scan/slots=%d", size), func(b *testing.B) {
			providerID := time.Now().UnixNano()
			pattern := fmt.Sprintf("%s%d:*", providerSlotKeyPrefix, providerID)
			keys := make([]string, 0, size)

			// Populate one Redis key per request outside timing so the measured SCAN
			// traversal operates on the already-created fixture for this slot count.
			b.StopTimer()
			pipe := rdb.Pipeline()
			for i := range size {
				key := fmt.Sprintf("%s%d:req_%d", providerSlotKeyPrefix, providerID, i)
				keys = append(keys, key)
				pipe.Set(ctx, key, "1", benchSlotTTL)
			}
			if _, err := pipe.Exec(ctx); err != nil {
				b.Fatalf("初始化扫描键失败: %v", err)
			}
			b.StartTimer()

			b.ReportAllocs()
			for range b.N {
				if _, err := scanSlotCount(ctx, rdb, pattern); err != nil {
					b.Fatalf("SCAN 计数失败: %v", err)
				}
			}

			// Delete the exact seeded request keys after stopping timing so teardown
			// is separated from repeated traversal of this fixture's key pattern.
			b.StopTimer()
			if err := rdb.Del(ctx, keys...).Err(); err != nil {
				b.Fatalf("清理扫描键失败: %v", err)
			}
		})
	}
}

// scanSlotCount accumulates matching key occurrences returned by SCAN until
// its cursor completes. It propagates command errors; callers keep the
// benchmark key set stable because this traversal is not an atomic count.
func scanSlotCount(ctx context.Context, rdb *redis.Client, pattern string) (int, error) {
	var cursor uint64
	count := 0
	for {
		// SCAN's count argument is a cursor-work hint, not a returned-key limit.
		keys, nextCursor, err := rdb.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return 0, err
		}
		count += len(keys)
		if nextCursor == 0 {
			break
		}
		cursor = nextCursor
	}
	return count, nil
}

// newBenchmarkRedisClient creates and pings the client configured by
// TEST_REDIS_URL, skipping when that value is empty. Invalid URLs and failed
// pings fail the benchmark; callers close a successfully returned client.
func newBenchmarkRedisClient(b *testing.B) *redis.Client {
	b.Helper()

	redisURL := os.Getenv("TEST_REDIS_URL")
	// An absent endpoint leaves this fixture's Redis workload unconfigured. Skip
	// the benchmark so results identify the server selected by TEST_REDIS_URL.
	if redisURL == "" {
		b.Skip("未设置 TEST_REDIS_URL，跳过 Redis 基准测试")
	}

	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		b.Fatalf("解析 TEST_REDIS_URL 失败: %v", err)
	}

	client := redis.NewClient(opt)
	// Supply a short context deadline for the endpoint probe; socket operations
	// still use the parsed client's configured read and write timeouts.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		b.Fatalf("Redis 连接失败: %v", err)
	}

	return client
}

// TestLiveLeaseReplacesRegularSlotsAndCountsTowardLimits reserves ordinary
// provider/user slots before acquiring a replacement Live lease. It checks
// counted Live occupancy, ordinary-provider rejection, successful renewal and
// provider admission after Live release.
func TestLiveLeaseReplacesRegularSlotsAndCountsTowardLimits(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	regular := NewConcurrencyCache(client, 15, 900)
	live, ok := regular.(scheduler.LiveConcurrencyCache)
	require.True(t, ok)
	ctx := context.Background()

	providerAcquired, err := regular.AcquireProviderSlot(ctx, 10, 1, "regular-provider")
	require.NoError(t, err)
	require.True(t, providerAcquired)
	userAcquired, err := regular.AcquireUserSlot(ctx, 20, 1, "regular-user")
	require.NoError(t, err)
	require.True(t, userAcquired)

	acquired, err := live.AcquireLiveLease(ctx, 10, 1, 20, 1, 30, "live-lease", true)
	require.NoError(t, err)
	require.True(t, acquired)
	// Release the ordinary reservations after successful replacement so
	// subsequent count assertions observe the Live lease alone rather than the
	// transfer overlap.
	require.NoError(t, regular.ReleaseProviderSlot(ctx, 10, "regular-provider"))
	require.NoError(t, regular.ReleaseUserSlot(ctx, 20, "regular-user"))

	providerCount, err := regular.GetProviderConcurrency(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, 1, providerCount)
	userCount, err := regular.GetUserConcurrency(ctx, 20)
	require.NoError(t, err)
	require.Equal(t, 1, userCount)
	providerAcquired, err = regular.AcquireProviderSlot(ctx, 10, 1, "ordinary-blocked")
	require.NoError(t, err)
	require.False(t, providerAcquired)

	refreshed, err := live.RefreshLiveLease(ctx, 10, 20, 30, "live-lease")
	require.NoError(t, err)
	require.True(t, refreshed)
	require.NoError(t, live.ReleaseLiveLease(ctx, 10, 20, 30, "live-lease"))
	providerAcquired, err = regular.AcquireProviderSlot(ctx, 10, 1, "ordinary-allowed")
	require.NoError(t, err)
	require.True(t, providerAcquired)
}

// TestLiveLeaseExpiresWithoutRefresh acquires a Live lease and expires the
// fixture keys beyond its lifetime. It checks ordinary provider admission and
// failure to renew the expired Live identity.
func TestLiveLeaseExpiresWithoutRefresh(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	regular := NewConcurrencyCache(client, 15, 900)
	live, ok := regular.(scheduler.LiveConcurrencyCache)
	require.True(t, ok)
	ctx := context.Background()

	acquired, err := live.AcquireLiveLease(ctx, 10, 1, 20, 1, 30, "expired-live", false)
	require.NoError(t, err)
	require.True(t, acquired)

	// Expire the unrenewed fixture keys beyond their sixty-second lifetime
	// before checking that ordinary admission can reuse capacity and Live
	// renewal detects absence.
	redisServer.FastForward(61 * time.Second)
	acquired, err = regular.AcquireProviderSlot(ctx, 10, 1, "ordinary-after-expiry")
	require.NoError(t, err)
	require.True(t, acquired)
	refreshed, err := live.RefreshLiveLease(ctx, 10, 20, 30, "expired-live")
	require.NoError(t, err)
	require.False(t, refreshed)
}
