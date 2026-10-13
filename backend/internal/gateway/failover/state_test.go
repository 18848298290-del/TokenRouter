package failover

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

// TestSleepWithContext covers nonpositive durations, elapsed waits, and
// cancellation before or during a wait. The returned flag and measured delay
// must distinguish successful waiting from interrupted recovery.
func TestSleepWithContext(t *testing.T) {
	t.Run("零时长立即返回true", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			start := time.Now()
			ok := SleepWithContext(context.Background(), 0)
			require.True(t, ok)
			// Bound immediate returns to a short observation window.
			require.Less(t, time.Since(start), 50*time.Millisecond)
		})
	})

	t.Run("负时长立即返回true", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			start := time.Now()
			ok := SleepWithContext(context.Background(), -1*time.Second)
			require.True(t, ok)
			// Bound immediate returns to a short observation window.
			require.Less(t, time.Since(start), 50*time.Millisecond)
		})
	})

	t.Run("正常等待后返回true", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			start := time.Now()
			ok := SleepWithContext(context.Background(), 50*time.Millisecond)
			elapsed := time.Since(start)
			require.True(t, ok)
			// Allow a broad observation window around the intended wait.
			require.GreaterOrEqual(t, elapsed, 40*time.Millisecond)
			// Detect stalls beyond the requested fifty-millisecond wait.
			require.Less(t, elapsed, 500*time.Millisecond)
		})
	})

	t.Run("已取消context立即返回false", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			start := time.Now()
			ok := SleepWithContext(ctx, 5*time.Second)
			require.False(t, ok)
			// Bound immediate returns to a short observation window.
			require.Less(t, time.Since(start), 50*time.Millisecond)
		})
	})

	t.Run("等待期间context取消返回false", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			// Cancel after the wait begins so the long timer cannot complete first;
			// this verifies recovery responds to mid-wait request cancellation.
			go func() {
				time.Sleep(30 * time.Millisecond)
				cancel()
			}()

			start := time.Now()
			ok := SleepWithContext(ctx, 5*time.Second)
			elapsed := time.Since(start)
			require.False(t, ok)
			// Detect a prolonged wait beyond the modeled cancellation budget.
			require.Less(t, elapsed, 500*time.Millisecond)
		})
	})
}
