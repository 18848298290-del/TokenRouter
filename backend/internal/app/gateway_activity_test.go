package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
)

// Gateway activity must finish before completion queues and Redis shut down.
// Two entered operations reproduce a drain timeout, and a completed operation
// exercises successful shutdown ordering so both retained dependencies and
// normal cleanup remain observable to the application.

// TestGatewayRequestActivityTimeoutKeepsCompletionAndStorageOpen enters two
// operations that remain active during shutdown. A deadline must leave
// completion and Redis stop hooks uncalled, reject new activity, and remain
// the manager's repeated Stop result after the operations finish.
func TestGatewayRequestActivityTimeoutKeepsCompletionAndStorageOpen(t *testing.T) {
	manager := lifecycle.New()
	activity := provideGatewayRequestActivity(manager)
	requestDone, err := activity.Enter()
	require.NoError(t, err)
	defer requestDone()
	attemptDone, err := activity.Enter()
	require.NoError(t, err)
	defer attemptDone()
	var completionStopped, redisClosed atomic.Bool
	// Stop hooks can publish observations during shutdown. Atomic flags let the
	// test inspect completion and storage cleanup safely after the drain deadline.
	manager.Register(lifecycle.Hook{Name: "completion", StopOrder: 40, Stop: func(context.Context) error { completionStopped.Store(true); return nil }})
	manager.Register(lifecycle.Hook{Name: "redis", StopOrder: 900, Stop: func(context.Context) error { redisClosed.Store(true); return nil }})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err = manager.Stop(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	// Manager reports the unfinished phase; the activity tracker retains the unfinished operation count.
	require.ErrorContains(t, err, "GatewayRequestsAndAttempts")
	detailCtx, detailCancel := context.WithTimeout(context.Background(), time.Second)
	defer detailCancel()
	require.ErrorContains(t, activity.StopContext(detailCtx), "2 operations unfinished")
	require.False(t, completionStopped.Load())
	require.False(t, redisClosed.Load())
	_, err = activity.Enter()
	require.Error(t, err)
	attemptDone()
	requestDone()
	require.ErrorIs(t, manager.Stop(context.Background()), context.DeadlineExceeded)
}

// TestGatewayRequestActivityFinishesBeforeCompletionAndStorage releases its
// entered operation before stopping the manager. Shutdown must succeed and run
// the completion hook before the Redis hook in their configured stop order.
func TestGatewayRequestActivityFinishesBeforeCompletionAndStorage(t *testing.T) {
	manager := lifecycle.New()
	activity := provideGatewayRequestActivity(manager)
	done, err := activity.Enter()
	require.NoError(t, err)
	var order []string
	manager.Register(lifecycle.Hook{Name: "completion", StopOrder: 40, Stop: func(context.Context) error { order = append(order, "completion"); return nil }})
	manager.Register(lifecycle.Hook{Name: "redis", StopOrder: 900, Stop: func(context.Context) error { order = append(order, "redis"); return nil }})
	done()
	require.NoError(t, manager.Stop(context.Background()))
	require.Equal(t, []string{"completion", "redis"}, order)
}
