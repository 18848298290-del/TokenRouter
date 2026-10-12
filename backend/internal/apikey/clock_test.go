package apikey

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestKeyClockExpiryBoundary places expiry exactly at the injected service
// time, then advances by one nanosecond. It checks equality admission, clock-
// free unbounded expiry, later rejection, and worker use of the same clock.
func TestKeyClockExpiryBoundary(t *testing.T) {
	now := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	calls := 0
	core := NewAPIKeyService(nil, nil, nil, nil, nil, nil, &Options{Now: func() time.Time { calls++; return now }})
	key := &APIKey{}
	require.NoError(t, core.CheckAPIKeyQuotaAndExpiry(key))
	require.Zero(t, calls)
	expiry := now
	key.ExpiresAt = &expiry
	require.NoError(t, core.CheckAPIKeyQuotaAndExpiry(key))
	require.Equal(t, 1, calls)
	// Advance past equality by the smallest duration to distinguish the
	// strict expiry comparison from a check that rejects equality itself.
	now = now.Add(time.Nanosecond)
	require.ErrorIs(t, core.CheckAPIKeyQuotaAndExpiry(key), ErrAPIKeyExpired)
	worker := NewAuthCacheInvalidationWorker(nil, nil, core)
	require.Equal(t, now, worker.now())
	// The constructor allocates a cancellation context even when dependencies
	// are nil. Release that context after checking the worker's delegated clock.
	worker.Stop()
}
