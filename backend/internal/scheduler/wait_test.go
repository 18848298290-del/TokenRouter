package scheduler

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// waitOwnershipCache models a preexisting wait count and configurable
// increment ownership for cleanup tests. Admissions run sequentially; a mutex
// protects decrements performed by concurrent release callers.
type waitOwnershipCache struct {
	// count and mu support concurrent release observations. Admission setup
	// changes count before goroutines start, decrements use mu, and assertions
	// read count after all release callers join.
	count int // current fixture waiting count
	// allowed and err supply admission and uncertainty together.
	// IncrementWaitCount changes count only after a true allowed value with
	// nil err, reproducing confirmed versus unconfirmed ownership.
	allowed bool       // configured increment admission result
	err     error      // configured increment ownership failure
	mu      sync.Mutex // concurrent decrement serialization lock
}

// IncrementWaitCount increments the fixture count after confirmed admission
// and returns configured results. Tests finish configuring allowed and err
// before admission and run increment calls sequentially, while release-side
// decrements use their own synchronization.
func (c *waitOwnershipCache) IncrementWaitCount(context.Context, int64, int) (bool, error) {
	if c.err == nil && c.allowed {
		c.count++
	}
	return c.allowed, c.err
}

// IncrementProviderWaitCount delegates provider admission to the fixture's
// shared increment behavior. It uses the same configured ownership result so
// the test can compare user and provider cleanup rules with identical
// observations.
func (c *waitOwnershipCache) IncrementProviderWaitCount(ctx context.Context, id int64, limit int) (bool, error) {
	return c.IncrementWaitCount(ctx, id, limit)
}

// DecrementWaitCount serializes removal of one fixture wait count under its
// mutex. Release tests can call cleanup concurrently and inspect count after
// joining every caller, while increment setup remains sequential.
func (c *waitOwnershipCache) DecrementWaitCount(context.Context, int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.count--
	return nil
}

// DecrementProviderWaitCount delegates provider cleanup to the fixture's
// synchronized decrement method. Concurrent release callers share that
// counter; tests inspect its resulting count after those callers have joined.
func (c *waitOwnershipCache) DecrementProviderWaitCount(ctx context.Context, id int64) error {
	return c.DecrementWaitCount(ctx, id)
}

// TestWaitOwnershipFailOpenAndRepeatedRelease compares confirmed and uncertain
// wait increments for user and provider admission. Concurrent repeated release
// must preserve another request's starting count, with ownership classified as
// counted or uncertain.
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
			// Concurrent copies share the result's cleanup owner. Join every
			// release call before inspecting the count to distinguish one
			// confirmed decrement from repeated cleanup.
			for range 8 {
				wg.Go(func() { ; result.Release() })
			}
			wg.Wait()
			require.Equal(t, 1, cache.count)
		}
	}
}

// TestWaitQueueFullOwnsNothing attempts admission into a full fixture queue
// and then releases the denied result. It requires denied admission and the
// same existing count, protecting cleanup that owns no increment.
func TestWaitQueueFullOwnsNothing(t *testing.T) {
	cache := &waitOwnershipCache{count: 20}
	result, err := EnterUserWait(context.Background(), cache, 1, 20, Diagnostics{})
	require.NoError(t, err)
	require.False(t, result.Allowed)
	result.Release()
	require.Equal(t, 20, cache.count)
}
