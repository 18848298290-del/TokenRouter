package requestcontext

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

// TestDetachSeparatesCancellation checks values, parent deadlines, and server abort independently.
func TestDetachSeparatesCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
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
		time.Sleep(2 * time.Second)
		synctest.Wait()
		require.ErrorIs(t, request.Err(), context.DeadlineExceeded)
		require.NoError(t, nested.Err())
		require.False(t, called)
		abort()
		synctest.Wait()
		require.True(t, called)
		require.ErrorIs(t, nested.Err(), context.Canceled)
		require.ErrorIs(t, context.Cause(nested), context.Canceled)
		require.ErrorIs(t, Detach(request).Err(), context.Canceled)
	})
}

// TestDetachWithoutAbort checks detached contexts ignore parent cancellation.
func TestDetachWithoutAbort(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	detached := Detach(ctx)
	require.Nil(t, detached.Done())
	cancel()
	require.NoError(t, detached.Err())
	require.NoError(t, Detach(nil).Err()) //nolint:staticcheck // Exercise the supported nil input.
	require.True(t, AfterAbort(ctx, func() { t.Fatal("unexpected abort") })())
}
