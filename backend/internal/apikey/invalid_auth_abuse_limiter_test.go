package apikey

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newInvalidAuthLimiterForTest constructs enabled tracking with a one-minute
// failure window and ten-second block. Callers supply positive threshold and
// capacity values and replace its clock before concurrent use when
// deterministic time is needed.
func newInvalidAuthLimiterForTest(threshold, capacity int) *KeyInvalidAuthAbuseLimiter {
	cfg := &Options{APIKeyAuth: APIKeyAuthCacheConfig{
		InvalidAbuse: InvalidAuthAbuseConfig{
			Enabled: true, Threshold: threshold, WindowSeconds: 60, BlockSeconds: 10, Capacity: capacity,
		},
	}}
	return KeyNewInvalidAuthAbuseLimiter(cfg)
}

// TestInvalidAuthAbuseLimiterBlocksAndExpires advances a fixed clock through
// threshold, block expiry and failure-window expiry. It checks retry duration,
// subsequent admission and final reclamation of the tracked client.
func TestInvalidAuthAbuseLimiterBlocksAndExpires(t *testing.T) {
	l := newInvalidAuthLimiterForTest(3, 16)
	now := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }

	for range 3 {
		l.KeyRecord("203.0.113.1")
	}
	retry, blocked := l.KeyCheck("203.0.113.1")
	require.True(t, blocked)
	require.Equal(t, 10*time.Second, retry)

	now = now.Add(11 * time.Second)
	_, blocked = l.KeyCheck("203.0.113.1")
	require.False(t, blocked)
	now = now.Add(61 * time.Second)
	_, blocked = l.KeyCheck("203.0.113.1")
	require.False(t, blocked)
	require.Zero(t, l.KeyHealth().Tracked)
}

// TestInvalidAuthAbuseLimiterCapacityUsesBoundedOverflowProtection fills two
// tracked positions and records failures from additional clients. It checks
// shared overflow rejection, tracked-client admission and the separate overflow
// health counters.
func TestInvalidAuthAbuseLimiterCapacityUsesBoundedOverflowProtection(t *testing.T) {
	l := newInvalidAuthLimiterForTest(2, 2)
	now := time.Now()
	l.now = func() time.Time { return now }
	l.KeyRecord("198.51.100.1")
	l.KeyRecord("198.51.100.2")
	l.KeyRecord("198.51.100.3")
	l.KeyRecord("198.51.100.4")

	_, blocked := l.KeyCheck("198.51.100.5")
	require.True(t, blocked)
	_, trackedBlocked := l.KeyCheck("198.51.100.1")
	require.False(t, trackedBlocked, "global overflow protection should spare existing tracked NATs")
	KeyHealth := l.KeyHealth()
	require.Equal(t, int64(2), KeyHealth.Tracked)
	require.Equal(t, uint64(2), KeyHealth.Overflowed)
	require.Equal(t, uint64(1), KeyHealth.GlobalBlocked)
}

// TestInvalidAuthAbuseLimiterConcurrentCapacityIsBounded records failures for
// many distinct clients concurrently against a smaller capacity. It checks
// retained entry count, all recorded failures and the expected overflow count
// after joining workers.
func TestInvalidAuthAbuseLimiterConcurrentCapacityIsBounded(t *testing.T) {
	// Use fewer tracked slots than concurrently recorded distinct clients.
	const capacity = 64
	l := newInvalidAuthLimiterForTest(1000, capacity)
	var wg sync.WaitGroup
	for i := range 1000 {
		wg.Add(1)
		// Distinct clients race for the shared reservation counter across shards;
		// join all recorders before comparing retained and overflow event counts.
		go func(i int) {
			defer wg.Done()
			l.KeyRecord(fmt.Sprintf("198.51.100.%d", i))
		}(i)
	}
	wg.Wait()
	KeyHealth := l.KeyHealth()
	require.LessOrEqual(t, KeyHealth.Tracked, int64(capacity))
	require.Equal(t, uint64(1000), KeyHealth.Recorded)
	require.Equal(t, uint64(1000-capacity), KeyHealth.Overflowed)
}

// TestInvalidAuthAbuseLimiterReclaimsExpiredCapacity fills tracking capacity,
// advances beyond the failure window and checks fresh client identities. It
// checks that some tracked entries are reclaimed and recording another client
// keeps the observed count within the configured limit.
func TestInvalidAuthAbuseLimiterReclaimsExpiredCapacity(t *testing.T) {
	// Fill a small tracker before exercising its rotating cleanup.
	const capacity = 16
	l := newInvalidAuthLimiterForTest(100, capacity)
	now := time.Now()
	l.now = func() time.Time { return now }
	for i := range capacity {
		l.KeyRecord(fmt.Sprintf("source-%d", i))
	}
	require.Equal(t, int64(capacity), l.KeyHealth().Tracked)

	now = now.Add(61 * time.Second)
	for i := range KeyInvalidAuthAbuseShardCount {
		l.KeyCheck(fmt.Sprintf("new-source-%d", i))
		// Permit the next shard scan after the shared cleanup deadline.
		now = now.Add(101 * time.Millisecond)
	}
	require.Less(t, l.KeyHealth().Tracked, int64(capacity))
	l.KeyRecord("fresh-source")
	require.LessOrEqual(t, l.KeyHealth().Tracked, int64(capacity))
}
