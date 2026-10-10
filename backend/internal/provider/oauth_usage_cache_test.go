package provider

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOAuthUsageCacheLoadCopies(t *testing.T) {
	now := time.Now()
	t.Run("api", func(t *testing.T) {
		cache := NewOAuthUsageCache()
		original := &OAuthAPIUsageCache{Identity: "identity", Timestamp: now, Response: &ClaudeUsageResponse{FiveHour: ClaudeUsageWindow{Utilization: 12}}}
		cache.StoreAPI(1, original)
		original.Response.FiveHour.Utilization = 99
		first, ok := cache.LoadAPI(1)
		require.True(t, ok)
		require.Equal(t, float64(12), first.Response.FiveHour.Utilization)
		first.Identity = "caller"
		first.Response.FiveHour.Utilization = 0
		second, ok := cache.LoadAPI(1)
		require.True(t, ok)
		require.Equal(t, "identity", second.Identity)
		require.Equal(t, now, second.Timestamp)
		require.Equal(t, float64(12), second.Response.FiveHour.Utilization)
	})
	t.Run("window", func(t *testing.T) {
		cache := NewOAuthUsageCache()
		original := &OAuthWindowStatsCache{Timestamp: now, Stats: &WindowStats{Requests: 12}}
		cache.StoreWindow(1, original)
		original.Stats.Requests = 99
		first, ok := cache.LoadWindow(1)
		require.True(t, ok)
		require.Equal(t, int64(12), first.Stats.Requests)
		first.Stats.Requests = 0
		second, ok := cache.LoadWindow(1)
		require.True(t, ok)
		require.Equal(t, now, second.Timestamp)
		require.Equal(t, int64(12), second.Stats.Requests)
	})
	for _, platform := range []string{"antigravity", "qoder"} {
		t.Run(platform, func(t *testing.T) {
			cache := NewOAuthUsageCache()
			original := &UsageInfo{QoderQuota: &QoderQuotaInfo{UserQuota: &QoderQuotaProgress{Remaining: 12}}}
			read := func() *UsageInfo {
				if platform == "antigravity" {
					value, ok := cache.LoadAntigravity(1)
					require.True(t, ok)
					require.Equal(t, "identity", value.Identity)
					require.Equal(t, now, value.Timestamp)
					return value.UsageInfo
				}
				value, ok := cache.LoadQoder(1)
				require.True(t, ok)
				require.Equal(t, "identity", value.Identity)
				require.Equal(t, now, value.Timestamp)
				return value.UsageInfo
			}
			if platform == "antigravity" {
				cache.StoreAntigravity(1, &OAuthAntigravityUsageCache{Identity: "identity", Timestamp: now, UsageInfo: original})
			} else {
				cache.StoreQoder(1, &OAuthQoderUsageCache{Identity: "identity", Timestamp: now, UsageInfo: original})
			}
			original.QoderQuota.UserQuota.Remaining = 99
			first := read()
			require.Equal(t, float64(12), first.QoderQuota.UserQuota.Remaining)
			first.QoderQuota.UserQuota.Remaining = 0
			require.Equal(t, float64(12), read().QoderQuota.UserQuota.Remaining)
		})
	}
}

func TestOAuthUsageCacheLoadMisses(t *testing.T) {
	cache := NewOAuthUsageCache()
	cache.StoreAPI(1, nil)
	cache.StoreWindow(1, nil)
	cache.StoreAntigravity(1, nil)
	cache.StoreQoder(1, nil)
	for _, key := range []int64{1, 2} {
		api, ok := cache.LoadAPI(key)
		require.False(t, ok)
		require.Nil(t, api)
		window, ok := cache.LoadWindow(key)
		require.False(t, ok)
		require.Nil(t, window)
		antigravity, ok := cache.LoadAntigravity(key)
		require.False(t, ok)
		require.Nil(t, antigravity)
		qoder, ok := cache.LoadQoder(key)
		require.False(t, ok)
		require.Nil(t, qoder)
	}
}
