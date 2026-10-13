package provider

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Concurrent snapshot attempts for one provider share a single reservation
// interval, while another provider has its own timestamp. The test starts many
// callers at the same supplied time and then checks admission immediately
// before and exactly at the interval's end.

// TestWriteThrottleSharedProviderWindow sends thirty-two concurrent attempts
// for one provider at one timestamp. Exactly one is admitted; a different
// provider passes independently, and the first provider passes at the interval
// boundary.
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
