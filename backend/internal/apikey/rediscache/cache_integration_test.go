//go:build integration

package rediscache

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/TokenFlux/TokenRouter/internal/testutil/rediscontainer"
)

// ApiKeyCacheSuite hosts isolated Redis-backed creation and daily-counter
// adapter scenarios.
type ApiKeyCacheSuite struct {
	rediscontainer.Suite // Redis fixture lifetime owner
}

// TestCreateAttemptCount exercises absent user counters, repeated increments
// and deletion against isolated Redis clients. It checks zero reads, recorded
// counts and a positive lifetime within the configured counter duration.
func (s *ApiKeyCacheSuite) TestCreateAttemptCount() {
	tests := []struct {
		name string                                                           // Redis counter scenario label
		fn   func(ctx context.Context, rdb *redis.Client, cache *ApiKeyCache) // scenario counter assertion callback
	}{
		{
			name: "missing_key_returns_zero_nil",
			fn: func(ctx context.Context, rdb *redis.Client, cache *ApiKeyCache) {
				userID := int64(1)

				count, err := cache.GetCreateAttemptCount(ctx, userID)

				require.NoError(s.T(), err, "expected nil error for missing key")
				require.Equal(s.T(), 0, count, "expected zero count for missing key")
			},
		},
		{
			name: "increment_increases_count_and_sets_ttl",
			fn: func(ctx context.Context, rdb *redis.Client, cache *ApiKeyCache) {
				userID := int64(1)
				key := fmt.Sprintf("%s%d", ApiKeyRateLimitKeyPrefix, userID)

				require.NoError(s.T(), cache.IncrementCreateAttemptCount(ctx, userID), "IncrementCreateAttemptCount")
				require.NoError(s.T(), cache.IncrementCreateAttemptCount(ctx, userID), "IncrementCreateAttemptCount 2")

				count, err := cache.GetCreateAttemptCount(ctx, userID)
				require.NoError(s.T(), err, "GetCreateAttemptCount")
				require.Equal(s.T(), 2, count, "count mismatch")

				ttl, err := rdb.TTL(ctx, key).Result()
				require.NoError(s.T(), err, "TTL")
				s.AssertTTLWithin(ttl, 1*time.Second, ApiKeyRateLimitDuration)
			},
		},
		{
			name: "delete_removes_key",
			fn: func(ctx context.Context, rdb *redis.Client, cache *ApiKeyCache) {
				userID := int64(1)

				require.NoError(s.T(), cache.IncrementCreateAttemptCount(ctx, userID))
				require.NoError(s.T(), cache.DeleteCreateAttemptCount(ctx, userID), "DeleteCreateAttemptCount")

				count, err := cache.GetCreateAttemptCount(ctx, userID)
				require.NoError(s.T(), err, "expected nil error after delete")
				require.Equal(s.T(), 0, count, "expected zero count after delete")
			},
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			// Each suite client isolates this scenario's counter names, so earlier
			// increments cannot affect the missing-key and exact-count assertions in later cases.
			rdb := s.Client(s.T())
			cache, ok := NewAPIKeyCache(rdb).(*ApiKeyCache)
			require.True(s.T(), ok, "Key Redis adapter type")
			ctx := context.Background()

			tt.fn(ctx, rdb, cache)
		})
	}
}

// TestDailyUsage increments an explicitly named daily counter and separately
// applies an expiry. It checks retained count and positive TTL through the
// Redis adapter using isolated resources per scenario.
func (s *ApiKeyCacheSuite) TestDailyUsage() {
	tests := []struct {
		name string                                                           // Redis counter scenario label
		fn   func(ctx context.Context, rdb *redis.Client, cache *ApiKeyCache) // scenario counter assertion callback
	}{
		{
			name: "increment_increases_count",
			fn: func(ctx context.Context, rdb *redis.Client, cache *ApiKeyCache) {
				dailyKey := "daily:sk-test"

				require.NoError(s.T(), cache.IncrementDailyUsage(ctx, dailyKey), "IncrementDailyUsage")
				require.NoError(s.T(), cache.IncrementDailyUsage(ctx, dailyKey), "IncrementDailyUsage 2")

				n, err := rdb.Get(ctx, dailyKey).Int()
				require.NoError(s.T(), err, "Get dailyKey")
				require.Equal(s.T(), 2, n, "expected daily usage=2")
			},
		},
		{
			name: "set_expiry_sets_ttl",
			fn: func(ctx context.Context, rdb *redis.Client, cache *ApiKeyCache) {
				dailyKey := "daily:sk-test-expiry"

				require.NoError(s.T(), cache.IncrementDailyUsage(ctx, dailyKey))
				require.NoError(s.T(), cache.SetDailyUsageExpiry(ctx, dailyKey, 1*time.Hour), "SetDailyUsageExpiry")

				ttl, err := rdb.TTL(ctx, dailyKey).Result()
				require.NoError(s.T(), err, "TTL dailyKey")
				require.Greater(s.T(), ttl, time.Duration(0), "expected ttl > 0")
			},
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			rdb := s.Client(s.T())
			cache, ok := NewAPIKeyCache(rdb).(*ApiKeyCache)
			require.True(s.T(), ok, "Key Redis adapter type")
			ctx := context.Background()

			tt.fn(ctx, rdb, cache)
		})
	}
}

// TestApiKeyCacheSuite runs the creation and daily-counter scenarios through
// the shared Redis container suite. Its suite owns database isolation and
// client cleanup for each case.
func TestApiKeyCacheSuite(t *testing.T) {
	suite.Run(t, new(ApiKeyCacheSuite))
}
