package requestcontext

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

// TestDetachSeparatesCancellation advances a parent deadline inside a synctest
// bubble while retaining detached request values. Parent expiry must leave
// detached work and callbacks live, and server abort must cancel nested
// detachment and invoke its callback.
func TestDetachSeparatesCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// valueKey identifies the fixture value used to check nested context lookup.
		type valueKey struct{}
		parent, cancel := context.WithTimeout(context.WithValue(context.Background(), valueKey{}, "value"), time.Second)
		defer cancel()
		request, abort := WithAbort(parent)
		defer abort()
		detached := Detach(request)
		nested := Detach(detached)
		require.Equal(t, "value", nested.Value(valueKey{}))
		_, deadline := nested.Deadline()
		require.False(t, deadline)
		called := false
		stop := AfterAbort(request, func() { called = true })
		defer stop()
		// Advance the simulated clock past the parent deadline, then wait for
		// blocked work so the cancellation and callback assertions observe settled state.
		time.Sleep(2 * time.Second)
		synctest.Wait()
		require.ErrorIs(t, request.Err(), context.DeadlineExceeded)
		require.NoError(t, nested.Err())
		require.False(t, called)
		// End server-owned work and let the asynchronous AfterAbort callback finish
		// before reading its result and the nested context cancellation state.
		abort()
		synctest.Wait()
		require.True(t, called)
		require.ErrorIs(t, nested.Err(), context.Canceled)
		require.ErrorIs(t, context.Cause(nested), context.Canceled)
		require.ErrorIs(t, Detach(request).Err(), context.Canceled)
	})
}

// TestDetachWithoutAbort detaches a context that has no server stop signal and
// then cancels its parent. The detached context must remain live, nil input
// must be supported, and callback registration must return a successful
// stopper.
func TestDetachWithoutAbort(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	detached := Detach(ctx)
	require.Nil(t, detached.Done())
	cancel()
	require.NoError(t, detached.Err())
	require.NoError(t, Detach(nil).Err()) //nolint:staticcheck // Exercise the supported nil input.
	require.True(t, AfterAbort(ctx, func() { t.Fatal("unexpected abort") })())
}
