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

// benchSlotTTLMinutes sets the benchmark slot lifetime in minutes.
const benchSlotTTLMinutes = 15

var benchSlotTTL = time.Duration(benchSlotTTLMinutes) * time.Minute

// BenchmarkProviderConcurrency compares SCAN and sorted-set counting at different slot counts.
func BenchmarkProviderConcurrency(b *testing.B) {
	rdb := newBenchmarkRedisClient(b)
	defer func() {
		_ = rdb.Close()
	}()

	cache, _ := NewConcurrencyCache(rdb, benchSlotTTLMinutes, int(benchSlotTTL.Seconds())).(*concurrencyCache)
	ctx := context.Background()

	for _, size := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("zset/slots=%d", size), func(b *testing.B) {
			providerID := time.Now().UnixNano()
			key := providerSlotKey(providerID)

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

			b.StopTimer()
			if err := rdb.Del(ctx, key).Err(); err != nil {
				b.Fatalf("清理有序集合失败: %v", err)
			}
		})

		b.Run(fmt.Sprintf("scan/slots=%d", size), func(b *testing.B) {
			providerID := time.Now().UnixNano()
			pattern := fmt.Sprintf("%s%d:*", providerSlotKeyPrefix, providerID)
			keys := make([]string, 0, size)

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

			b.StopTimer()
			if err := rdb.Del(ctx, keys...).Err(); err != nil {
				b.Fatalf("清理扫描键失败: %v", err)
			}
		})
	}
}

func scanSlotCount(ctx context.Context, rdb *redis.Client, pattern string) (int, error) {
	var cursor uint64
	count := 0
	for {
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

func newBenchmarkRedisClient(b *testing.B) *redis.Client {
	b.Helper()

	redisURL := os.Getenv("TEST_REDIS_URL")
	if redisURL == "" {
		b.Skip("未设置 TEST_REDIS_URL，跳过 Redis 基准测试")
	}

	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		b.Fatalf("解析 TEST_REDIS_URL 失败: %v", err)
	}

	client := redis.NewClient(opt)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		b.Fatalf("Redis 连接失败: %v", err)
	}

	return client
}

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

	redisServer.FastForward(61 * time.Second)
	acquired, err = regular.AcquireProviderSlot(ctx, 10, 1, "ordinary-after-expiry")
	require.NoError(t, err)
	require.True(t, acquired)
	refreshed, err := live.RefreshLiveLease(ctx, 10, 20, 30, "expired-live")
	require.NoError(t, err)
	require.False(t, refreshed)
}
