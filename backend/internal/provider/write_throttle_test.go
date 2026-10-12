package provider

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestWriteThrottleSharedProviderWindow checks concurrent admission, independent providers, and interval expiry.
func TestWriteThrottleSharedProviderWindow(t *testing.T) {
	throttle := NewWriteThrottle(30 * time.Second)
	now := time.Unix(1_800_000_000, 0)
	var accepted atomic.Int64
	var workers sync.WaitGroup
	for range 32 {
		workers.Go(func() {
			if throttle.Allow(17, now) {
				accepted.Add(1)
			}
		})
	}
	workers.Wait()
	require.Equal(t, int64(1), accepted.Load())
	require.True(t, throttle.Allow(18, now))
	require.False(t, throttle.Allow(17, now.Add(30*time.Second-time.Nanosecond)))
	require.True(t, throttle.Allow(17, now.Add(30*time.Second)))
}
