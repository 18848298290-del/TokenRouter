package scheduler

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// sessionAttemptCache records the provider IDs whose sessions were unregistered.
type sessionAttemptCache struct {
	SessionLimitCache
	unregistered []int64
}

func (c *sessionAttemptCache) UnregisterSession(_ context.Context, id int64, _ string) error {
	c.unregistered = append(c.unregistered, id)
	return nil
}

func TestSessionAttemptsPreservePartialOutcomeAfterEarlyRelease(t *testing.T) {
	cache := &sessionAttemptCache{}
	attempts := NewSessionAttempts(cache, Diagnostics{})
	attempts.Track(SessionBinding{ProviderID: 1, SessionID: "session", Enabled: true, Limit: 2})
	var released atomic.Int64
	physical := NewLease(context.Background(), ReleaseOnCompletion, func() { released.Add(1) })
	attempts.Own(1, physical.Release)
	physical.Release()
	attempts.Finish(AttemptOutcome{Served: true})
	attempts.Finish(AttemptOutcome{})
	require.Equal(t, int64(1), released.Load())
	require.Empty(t, cache.unregistered)
}

func TestSessionAttemptsAbandonResetAndFinalCleanup(t *testing.T) {
	cache := &sessionAttemptCache{}
	attempts := NewSessionAttempts(cache, Diagnostics{})
	for _, id := range []int64{1, 2} {
		attempts.Track(SessionBinding{ProviderID: id, SessionID: "session", Enabled: true, Limit: 2})
	}
	attempts.Abandon(1)
	attempts.Abandon(1)
	attempts.Reset()
	attempts.Track(SessionBinding{ProviderID: 3, SessionID: "session", Enabled: true, Limit: 2})
	var released atomic.Int64
	attempts.Own(3, func() { released.Add(1) })
	attempts.Finish(AttemptOutcome{})
	attempts.Finish(AttemptOutcome{})
	require.Equal(t, []int64{1, 2, 3}, cache.unregistered)
	require.Equal(t, int64(1), released.Load())
}
