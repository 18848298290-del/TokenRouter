package querycache

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// contextResult carries an asynchronous load result to the test goroutine.
type contextResult struct {
	entry Entry
	err   error
}

// awaitContextWaiters waits for registration so cancellation ordering is deterministic.
func awaitContextWaiters(t *testing.T, c *Cache, key string, count int) *contextLoad {
	t.Helper()
	var flight *contextLoad
	require.Eventually(t, func() bool {
		c.loadMu.Lock()
		defer c.loadMu.Unlock()
		flight = c.loads[key]
		return flight != nil && flight.waiters == count
	}, time.Second, time.Millisecond)
	return flight
}

// startContextLoad returns an asynchronous load result through a buffered channel.
func startContextLoad(c *Cache, ctx context.Context, key string, load func(context.Context) (any, error)) <-chan contextResult {
	done := make(chan contextResult, 1)
	go func() { entry, _, err := c.GetOrLoadContext(ctx, key, load); done <- contextResult{entry, err} }()
	return done
}

// TestContextLoadCallerCancellationKeepsWaiters checks independent cancellation of early and late waiters.
func TestContextLoadCallerCancellationKeepsWaiters(t *testing.T) {
	for _, cancelFirst := range []bool{true, false} {
		c := NewCache(time.Minute)
		first, cancelA := context.WithCancel(context.Background())
		second, cancelB := context.WithCancel(context.Background())
		t.Cleanup(cancelA)
		t.Cleanup(cancelB)
		release := make(chan struct{})
		calls := make(chan struct{}, 2)
		load := func(ctx context.Context) (any, error) {
			calls <- struct{}{}
			select {
			case <-release:
				return map[string][]int{"value": {7}}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		a := startContextLoad(c, first, "key", load)
		awaitContextWaiters(t, c, "key", 1)
		b := startContextLoad(c, second, "key", load)
		flight := awaitContextWaiters(t, c, "key", 2)
		canceled, live := a, b
		if cancelFirst {
			cancelA()
		} else {
			cancelB()
			canceled, live = b, a
		}
		require.ErrorIs(t, (<-canceled).err, context.Canceled)
		require.NoError(t, flight.ctx.Err())
		close(release)
		result := <-live
		require.NoError(t, result.err)
		require.Len(t, calls, 1)
		payload, typed := result.entry.Payload.(map[string][]int)
		require.True(t, typed)
		payload["value"][0] = 99
		stored, ok := c.Get("key")
		require.True(t, ok)
		storedPayload, typed := stored.Payload.(map[string][]int)
		require.True(t, typed)
		require.Equal(t, 7, storedPayload["value"][0])
	}
}

// TestContextLoadLastWaiterCancelsAndDiscardsLateValue checks new callers do not join an abandoned load.
func TestContextLoadLastWaiterCancelsAndDiscardsLateValue(t *testing.T) {
	c := NewCache(time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	old := startContextLoad(c, ctx, "key", func(context.Context) (any, error) { <-release; return "old", nil })
	flight := awaitContextWaiters(t, c, "key", 1)
	cancel()
	require.ErrorIs(t, (<-old).err, context.Canceled)
	require.ErrorIs(t, flight.ctx.Err(), context.Canceled)
	entry, _, err := c.GetOrLoadContext(context.Background(), "key", func(context.Context) (any, error) { return "new", nil })
	require.NoError(t, err)
	require.Equal(t, "new", entry.Payload)
	close(release)
	<-flight.done
	entry, ok := c.Get("key")
	require.True(t, ok)
	require.Equal(t, "new", entry.Payload)
}

// TestContextLoadDeadlineAndErrors covers expired callers, independent deadlines, and reloading after failure.
func TestContextLoadDeadlineAndErrors(t *testing.T) {
	c := NewCache(time.Minute)
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, _, err := c.GetOrLoadContext(expired, "key", func(context.Context) (any, error) { t.Fatal("已过期请求不应启动查询"); return nil, nil })
	require.ErrorIs(t, err, context.DeadlineExceeded)
	release := make(chan struct{})
	a := startContextLoad(c, context.Background(), "deadline", func(ctx context.Context) (any, error) {
		select {
		case <-release:
			return 7, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	flight := awaitContextWaiters(t, c, "deadline", 1)
	short, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	_, _, err = c.GetOrLoadContext(short, "deadline", func(context.Context) (any, error) { t.Error("重复启动查询"); return nil, nil })
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NoError(t, flight.ctx.Err())
	close(release)
	require.NoError(t, (<-a).err)
	failed := errors.New("unavailable")
	_, _, err = c.GetOrLoadContext(context.Background(), "error", func(context.Context) (any, error) { return nil, failed })
	require.ErrorIs(t, err, failed)
	_, ok := c.Get("error")
	require.False(t, ok)
	_, _, err = c.GetOrLoadContext(context.Background(), "panic", func(context.Context) (any, error) { panic("load failed") })
	require.ErrorContains(t, err, "load failed")
	entry, _, err := c.GetOrLoadContext(context.Background(), "panic", func(context.Context) (any, error) { return 9, nil })
	require.NoError(t, err)
	require.Equal(t, 9, entry.Payload)
}
