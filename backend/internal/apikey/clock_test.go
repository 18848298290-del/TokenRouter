package apikey

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestKeyClockExpiryBoundary checks that equality is unexpired and keys without expiry do not read the clock.
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
	now = now.Add(time.Nanosecond)
	require.ErrorIs(t, core.CheckAPIKeyQuotaAndExpiry(key), ErrAPIKeyExpired)
	worker := NewAuthCacheInvalidationWorker(nil, nil, core)
	require.Equal(t, now, worker.now())
	worker.Stop()
}
