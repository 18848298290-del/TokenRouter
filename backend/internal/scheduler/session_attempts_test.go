package scheduler

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// sessionAttemptCache implements session removal for logical attempt cleanup
// tests. The remaining embedded methods are unimplemented, so these tests
// restrict cache calls to UnregisterSession.
type sessionAttemptCache struct {
	SessionLimitCache // unused session method delegation
	// unregistered retains provider IDs in callback order. The fixture appends
	// and reads this slice sequentially because it supplies no synchronization
	// for concurrent session removals.
	unregistered []int64 // ordered removed provider identifiers
}

// UnregisterSession records the provider ID and returns successful session
// cleanup. The fixture appends to shared slice storage, so tests invoke
// removals sequentially before inspecting their order.
func (c *sessionAttemptCache) UnregisterSession(_ context.Context, id int64, _ string) error {
	c.unregistered = append(c.unregistered, id)
	return nil
}

// TestSessionAttemptsPreservePartialOutcomeAfterEarlyRelease returns physical
// capacity before finishing the tracked attempt as served. It checks one
// physical release and no session removal even after a later repeated failed
// finish.
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

// TestSessionAttemptsAbandonResetAndFinalCleanup abandons one provider, resets
// another, then finishes a new unsuccessful attempt. It checks ordered session
// removals and one owned release despite repeated abandon and finish calls.
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
