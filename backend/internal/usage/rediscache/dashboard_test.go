package rediscache

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// Dashboard cache keys separate installations through a normalized prefix.
// Constructor-only checks obtain the concrete adapter through its usage cache
// interface, so prefix setup can be verified with a nil Redis client. Both an
// absent trailing colon and an existing trailing colon are exercised.

// TestNewDashboardCacheKeyPrefix constructs cache adapters through the usage
// cache interface. A plain prefix gains its separator, while an existing
// trailing colon remains the stored separator.
func TestNewDashboardCacheKeyPrefix(t *testing.T) {
	var cache usage.DashboardStatsCache = NewDashboardCache(nil, "prod")
	impl, ok := cache.(*DashboardCache)
	require.True(t, ok)
	require.Equal(t, "prod:", impl.keyPrefix)

	cache = NewDashboardCache(nil, "staging:")
	impl, ok = cache.(*DashboardCache)
	require.True(t, ok)
	require.Equal(t, "staging:", impl.keyPrefix)
}
