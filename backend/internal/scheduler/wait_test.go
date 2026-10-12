package scheduler

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// waitOwnershipCache starts with another request's count to detect release of an unconfirmed increment.
type waitOwnershipCache struct {
	count   int
	allowed bool
	err     error
	mu      sync.Mutex
}

func (c *waitOwnershipCache) IncrementWaitCount(context.Context, int64, int) (bool, error) {
	if c.err == nil && c.allowed {
		c.count++
	}
	return c.allowed, c.err
}

func (c *waitOwnershipCache) IncrementProviderWaitCount(ctx context.Context, id int64, limit int) (bool, error) {
	return c.IncrementWaitCount(ctx, id, limit)
}

func (c *waitOwnershipCache) DecrementWaitCount(context.Context, int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.count--
	return nil
}

func (c *waitOwnershipCache) DecrementProviderWaitCount(ctx context.Context, id int64) error {
	return c.DecrementWaitCount(ctx, id)
}

func TestWaitOwnershipFailOpenAndRepeatedRelease(t *testing.T) {
	for _, provider := range []bool{false, true} {
		for _, failure := range []bool{false, true} {
			cache := &waitOwnershipCache{count: 1, allowed: true}
			if failure {
				cache.err = errors.New("未确认增加计数")
			}
			enter := EnterUserWait
			if provider {
				enter = EnterProviderWait
			}
			result, err := enter(context.Background(), cache, 1, 20, Diagnostics{})
			require.NoError(t, err)
			require.True(t, result.Allowed)
			if failure {
				require.Equal(t, WaitUncertain, result.Ownership)
			} else {
				require.Equal(t, WaitCounted, result.Ownership)
			}
			var wg sync.WaitGroup
			for range 8 {
				wg.Go(func() { ; result.Release() })
			}
			wg.Wait()
			require.Equal(t, 1, cache.count)
		}
	}
}

func TestWaitQueueFullOwnsNothing(t *testing.T) {
	cache := &waitOwnershipCache{count: 20}
	result, err := EnterUserWait(context.Background(), cache, 1, 20, Diagnostics{})
	require.NoError(t, err)
	require.False(t, result.Allowed)
	result.Release()
	require.Equal(t, 20, cache.count)
}
