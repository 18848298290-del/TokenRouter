package querycache

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// contextResult carries a snapshot and error from an asynchronous cache-call
// fixture.
type contextResult struct {
	entry Entry // asynchronous snapshot result
	err   error // asynchronous call error
}

// awaitContextWaiters waits until a registered query flight has the expected
// waiter count. It inspects the registry under loadMu for up to one second,
// failing the test when cancellation setup cannot reach that state.
func awaitContextWaiters(t *testing.T, c *Cache, key string, count int) *contextLoad {
	t.Helper()
	var flight *contextLoad
	// Poll registry state before cancelling callers so the test establishes how
	// many waiters are sharing the flight at the cancellation decision being asserted.
	require.Eventually(t, func() bool {
		c.loadMu.Lock()
		defer c.loadMu.Unlock()
		flight = c.loads[key]
		return flight != nil && flight.waiters == count
	}, time.Second, time.Millisecond)
	return flight
}

// startContextLoad starts one context-aware cache call and returns its result
// channel. A single buffered publication lets the goroutine finish before the
// test receives the result; the channel remains open afterward.
func startContextLoad(c *Cache, ctx context.Context, key string, load func(context.Context) (any, error)) <-chan contextResult {
	// Buffer the sole result so completion does not depend on the test receiving
	// immediately; callers can coordinate registry state before reading this outcome.
	done := make(chan contextResult, 1)
	go func() { entry, _, err := c.GetOrLoadContext(ctx, key, load); done <- contextResult{entry, err} }()
	return done
}

// TestContextLoadCallerCancellationKeepsWaiters registers two callers and
// cancels either the first or later waiter. The shared context must remain live
// for the survivor, execute one load, and return independently mutable payload
// data.
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
		// Register both waiters before cancellation so either caller can leave while
		// a confirmed peer still needs the flight and keeps its shared context alive.
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

// TestContextLoadLastWaiterCancelsAndDiscardsLateValue abandons an
// uncooperative loader before registering a replacement for its key. The
// abandoned context must cancel, and its late value must leave the replacement
// cached.
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
	// Release the abandoned loader after the replacement is cached, then wait
	// for its publication path so the next cache read observes the late-result decision.
	close(release)
	<-flight.done
	entry, ok := c.Get("key")
	require.True(t, ok)
	require.Equal(t, "new", entry.Payload)
}

// TestContextLoadDeadlineAndErrors supplies an expired caller, a
// deadline-limited waiter, a failing loader, and a panicking loader.
// Cancellation must respect live peers, failures must stay uncached, and a
// later call must recover by loading successfully.
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
	// Give the second registered call a short deadline while the first stays
	// active, making its timeout exercise waiter departure from an existing flight.
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
