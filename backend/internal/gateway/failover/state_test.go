package failover

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

// TestSleepWithContext checks elapsed time and cancellation results.
func TestSleepWithContext(t *testing.T) {
	t.Run("零时长立即返回true", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			start := time.Now()
			ok := SleepWithContext(context.Background(), 0)
			require.True(t, ok)
			require.Less(t, time.Since(start), 50*time.Millisecond)
		})
	})

	t.Run("负时长立即返回true", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			start := time.Now()
			ok := SleepWithContext(context.Background(), -1*time.Second)
			require.True(t, ok)
			require.Less(t, time.Since(start), 50*time.Millisecond)
		})
	})

	t.Run("正常等待后返回true", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			start := time.Now()
			ok := SleepWithContext(context.Background(), 50*time.Millisecond)
			elapsed := time.Since(start)
			require.True(t, ok)
			require.GreaterOrEqual(t, elapsed, 40*time.Millisecond)
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
			require.Less(t, time.Since(start), 50*time.Millisecond)
		})
	})

	t.Run("等待期间context取消返回false", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			go func() {
				time.Sleep(30 * time.Millisecond)
				cancel()
			}()

			start := time.Now()
			ok := SleepWithContext(ctx, 5*time.Second)
			elapsed := time.Since(start)
			require.False(t, ok)
			require.Less(t, elapsed, 500*time.Millisecond)
		})
	})
}
