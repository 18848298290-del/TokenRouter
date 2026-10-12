package notification

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestNotificationLockCancellationAndIsolation holds one key while acquiring a different key.
// It verifies a same-key waiter can cancel, repeated release is harmless,
// and the coordinator removes its entries after all registered callers finish.
func TestNotificationLockCancellationAndIsolation(t *testing.T) {
	var locks keyCoordinator
	release, err := locks.acquire(context.Background(), "one")
	require.NoError(t, err)
	other, err := locks.acquire(context.Background(), "two")
	require.NoError(t, err)
	other()
	ctx, cancel := context.WithCancel(context.Background())
	// Keep the first key held while the new goroutine registers as its waiter.
	// Waiting for refs before cancellation exercises cleanup of an actual queued caller.
	done := make(chan error, 1)
	go func() { _, err := locks.acquire(ctx, "one"); done <- err }()
	require.Eventually(t, func() bool { locks.mu.Lock(); defer locks.mu.Unlock(); return locks.entries["one"].refs == 2 }, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	release()
	release()
	require.Empty(t, locks.entries)
}
