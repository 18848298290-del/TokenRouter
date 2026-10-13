package provider

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestGrokProbeDetachedWorkHasStopOwner cancels a waiter after its shared probe
// enters a blocking callback. It asserts the waiter returns cancellation,
// shared work remains active, and service shutdown cancels and joins that
// callback.
func TestGrokProbeDetachedWorkHasStopOwner(t *testing.T) {
	svc := NewGrokQuotaService(GrokQuotaOptions{}, &ProbeRuntime{})
	entered, exited, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Start the waiter before cancelling it so the entry barrier proves shared
	// work has its own registered operation when caller cancellation is
	// delivered.
	go func() {
		_, err := svc.RunProbeFlight(ctx, "local-owner", func(shared context.Context) (*GrokQuotaProbeResult, error) {
			// Keep entry and exit observable separately; the test cancels only the
			// waiter first, then requires shutdown to finish this still-active
			// shared callback.
			close(entered)
			defer close(exited)
			select {
			case <-shared.Done():
				return nil, shared.Err()
			case <-release:
				return &GrokQuotaProbeResult{Source: "local"}, nil
			}
		})
		done <- err
	}()
	// Release a surviving callback during cleanup and await its exit with a
	// diagnostic timeout. Failure is reported if this release does not finish
	// the shared probe.
	t.Cleanup(func() {
		close(release)
		select {
		case <-exited:
		case <-time.After(time.Second):
			t.Error("共享探测未结束")
		}
	})
	<-entered
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	select {
	case <-exited:
		t.Fatal("调用方取消不应结束共享探测")
	default:
	}
	if owner, ok := any(svc).(interface {
		// StopContext cancels the shared probe owner and waits for accepted
		// callbacks within the supplied budget. Unfinished work reports a
		// deadline error so this fixture can observe shutdown ownership.
		StopContext(context.Context) error
	}); ok {
		// Give shutdown one second to cancel and join the shared callback,
		// making a failure to finish observable to this lifecycle fixture.
		budget, stopCancel := context.WithTimeout(context.Background(), time.Second)
		defer stopCancel()
		require.NoError(t, owner.StopContext(budget))
	}
	select {
	case <-exited:
	default:
		t.Error("共享探测没有可停止的拥有者")
	}
}
