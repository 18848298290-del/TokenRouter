package lifecycle

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Operation barriers keep dependency-stop hooks behind admitted work. One
// fixture exercises a normal release and duplicate cleanup, while another
// leaves an operation active through the stop deadline and checks that the
// first timeout result remains observable.

// TestOperationsWaitsBeforeClosingDependencies leaves one admitted operation
// active while shutdown starts. The later Redis hook must wait for release,
// duplicate release must be harmless, and stopped admission must reject new work.
func TestOperationsWaitsBeforeClosingDependencies(t *testing.T) {
	operations := NewOperations("fixture")
	finish, err := operations.Enter()
	require.NoError(t, err)
	manager := New()
	var closed atomic.Bool
	stopping := make(chan struct{})
	// Publish entry into the first stop hook before waiting for the active
	// operation, letting the test inspect the later dependency flag before release.
	manager.Register(Hook{Name: "platform", StopOrder: 15, Stop: func(ctx context.Context) error { close(stopping); return operations.StopContext(ctx) }})
	manager.Register(Hook{Name: "redis", StopOrder: 900, Stop: func(context.Context) error { closed.Store(true); return nil }})
	done := make(chan error, 1)
	// Run shutdown separately so the test can observe its first stop hook
	// before releasing admitted work and allowing the dependent Redis hook to run.
	go func() { done <- manager.Stop(context.Background()) }()
	<-stopping
	require.False(t, closed.Load())
	finish()
	finish()
	require.NoError(t, <-done)
	require.True(t, closed.Load())
	_, err = operations.Enter()
	require.ErrorContains(t, err, "stopped")
	require.NoError(t, operations.StopContext(context.Background()))
}

// TestOperationsTimeoutDoesNotCloseDependencies keeps an operation active
// through the shutdown deadline. The error must name that phase, the Redis
// hook must stay uncalled, and releasing later must retain the first timeout result.
func TestOperationsTimeoutDoesNotCloseDependencies(t *testing.T) {
	operations := NewOperations("QoderRequestsAndAttempts")
	finish, err := operations.Enter()
	require.NoError(t, err)
	defer finish()
	manager := New()
	var closed atomic.Bool
	// The stop callback can publish from another goroutine while the test
	// inspects timeout behavior. Use its atomic flag as the dependent-hook observation.
	manager.Register(Hook{Name: "QoderRequestsAndAttempts", StopOrder: 15, Stop: operations.StopContext})
	manager.Register(Hook{Name: "redis", StopOrder: 900, Stop: func(context.Context) error { closed.Store(true); return nil }})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err = manager.Stop(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Contains(t, err.Error(), "QoderRequestsAndAttempts")
	require.False(t, closed.Load())
	finish()
	require.ErrorIs(t, operations.StopContext(context.Background()), context.DeadlineExceeded)
}
