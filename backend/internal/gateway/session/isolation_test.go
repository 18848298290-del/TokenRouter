package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// sessionIsolationCacheStub models first-owner insertion under a mutex for isolation tests.
type sessionIsolationCacheStub struct {
	// mu guards owners, ownerTTLs, setCalls, getCalls, and refreshCalls during
	// fixture operations; assertions read counters after the worker results have arrived.
	mu           sync.Mutex               // fixture state access lock
	owners       map[string]int64         // user/source/hash to first group
	ownerTTLs    map[string]time.Duration // owner identity to recorded lifetime
	setCalls     int                      // insertion attempt call count
	getCalls     int                      // owner lookup call count
	refreshCalls int                      // owner renewal call count
}

// TestEnsureSessionIsolation_FirstBindRecordsOwnerEvenWhenTargetNotIsolated starts
// with an unowned session and a permissive target. Its trimmed hash receives the
// first group and supplied TTL, with no separate renewal.
func TestEnsureSessionIsolation_FirstBindRecordsOwnerEvenWhenTargetNotIsolated(t *testing.T) {
	ctx := context.Background()
	cache := &sessionIsolationCacheStub{}
	ttl := 2 * time.Minute

	err := EnsureIsolation(ctx, cache, IsolationInput{GroupID: 11, Enabled: false, UserID: 7, Source: SessionIsolationSourceOpenAI, Hash: " session-a ", TTL: ttl})

	require.NoError(t, err)
	require.Equal(t, int64(11), cache.ownerGroupID(7, SessionIsolationSourceOpenAI, "session-a"))
	require.Equal(t, ttl, cache.ownerTTL(7, SessionIsolationSourceOpenAI, "session-a"))
	require.Equal(t, 1, cache.setCalls)
	require.Zero(t, cache.refreshCalls)
}

// TestEnsureSessionIsolation_SameOwnerRefreshesTTL preloads ownership for the target
// group and supplies a longer lifetime. The owner remains unchanged and one
// renewal installs the requested TTL.
func TestEnsureSessionIsolation_SameOwnerRefreshesTTL(t *testing.T) {
	ctx := context.Background()
	cache := &sessionIsolationCacheStub{}
	initialTTL := time.Minute
	refreshTTL := 5 * time.Minute
	_, err := cache.SetSessionOwnerGroupID(ctx, 7, SessionIsolationSourceGateway, "session-a", 11, initialTTL)
	require.NoError(t, err)

	err = EnsureIsolation(ctx, cache, IsolationInput{GroupID: 11, Enabled: true, UserID: 7, Source: SessionIsolationSourceGateway, Hash: "session-a", TTL: refreshTTL})

	require.NoError(t, err)
	require.Equal(t, int64(11), cache.ownerGroupID(7, SessionIsolationSourceGateway, "session-a"))
	require.Equal(t, refreshTTL, cache.ownerTTL(7, SessionIsolationSourceGateway, "session-a"))
	require.Equal(t, 1, cache.refreshCalls)
}

// TestEnsureSessionIsolation_NonIsolatedTargetAllowsDifferentOwner requests another
// group with isolation disabled. The request succeeds while the original owner
// remains recorded and receives no TTL renewal.
func TestEnsureSessionIsolation_NonIsolatedTargetAllowsDifferentOwner(t *testing.T) {
	ctx := context.Background()
	cache := &sessionIsolationCacheStub{}
	_, err := cache.SetSessionOwnerGroupID(ctx, 7, SessionIsolationSourceGateway, "session-a", 11, time.Minute)
	require.NoError(t, err)

	err = EnsureIsolation(ctx, cache, IsolationInput{GroupID: 22, Enabled: false, UserID: 7, Source: SessionIsolationSourceGateway, Hash: "session-a", TTL: time.Minute})

	require.NoError(t, err)
	require.Equal(t, int64(11), cache.ownerGroupID(7, SessionIsolationSourceGateway, "session-a"))
	require.Zero(t, cache.refreshCalls)
}

// TestEnsureSessionIsolation_IsolatedTargetRejectsDifferentOwner requests another
// group with isolation enabled. The existing owner remains unchanged, the conflict
// sentinel is returned, and the owner TTL is not renewed.
func TestEnsureSessionIsolation_IsolatedTargetRejectsDifferentOwner(t *testing.T) {
	ctx := context.Background()
	cache := &sessionIsolationCacheStub{}
	_, err := cache.SetSessionOwnerGroupID(ctx, 7, SessionIsolationSourceGemini, "session-a", 11, time.Minute)
	require.NoError(t, err)

	err = EnsureIsolation(ctx, cache, IsolationInput{GroupID: 22, Enabled: true, UserID: 7, Source: SessionIsolationSourceGemini, Hash: "session-a", TTL: time.Minute})

	require.ErrorIs(t, err, ErrSessionIsolationConflict)
	require.Equal(t, int64(11), cache.ownerGroupID(7, SessionIsolationSourceGemini, "session-a"))
	require.Zero(t, cache.refreshCalls)
}

// TestEnsureSessionIsolation_ConcurrentFirstBindAllowsSingleOwner releases two
// isolated-group callers against one unowned session together. Exactly one call
// succeeds and the other conflicts, leaving one of their groups as owner.
func TestEnsureSessionIsolation_ConcurrentFirstBindAllowsSingleOwner(t *testing.T) {
	ctx := context.Background()
	cache := &sessionIsolationCacheStub{}
	start := make(chan struct{})
	results := make(chan error, 2)
	// Both workers wait for one release so atomic insertion decides the owner;
	// collecting both buffered results joins them before counters are inspected.
	for _, groupID := range []int64{11, 22} {
		go func() {
			<-start
			results <- EnsureIsolation(ctx, cache, IsolationInput{GroupID: groupID, Enabled: true, UserID: 7, Source: SessionIsolationSourceOpenAI, Hash: "session-a", TTL: time.Minute})
		}()
	}

	close(start)
	errA := <-results
	errB := <-results

	successes := 0
	conflicts := 0
	for _, err := range []error{errA, errB} {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrSessionIsolationConflict):
			conflicts++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)
	require.Contains(t, []int64{11, 22}, cache.ownerGroupID(7, SessionIsolationSourceOpenAI, "session-a"))
}

// ownerKey joins the fixture's user, source, and session hash identity.
func (c *sessionIsolationCacheStub) ownerKey(userID int64, source, sessionHash string) string {
	return fmt.Sprintf("%d:%s:%s", userID, source, sessionHash)
}

// ownerGroupID reads the stored fixture owner under its mutex.
// An absent identity returns zero from the map; callers invoke this helper after
// any concurrent first-bind workers have completed.
func (c *sessionIsolationCacheStub) ownerGroupID(userID int64, source, sessionHash string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.owners[c.ownerKey(userID, source, sessionHash)]
}

// ownerTTL reads the fixture lifetime recorded for a user/source/hash identity.
// The mutex protects the read, and absent entries return zero so assertions can
// inspect whether insertion or renewal happened.
func (c *sessionIsolationCacheStub) ownerTTL(userID int64, source, sessionHash string) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ownerTTLs[c.ownerKey(userID, source, sessionHash)]
}

// GetSessionProviderID reports a miss because this fixture tests group ownership.
// It returns a not-found error for every provider lookup, leaving sticky routing
// outside the fixture's modeled state.
func (c *sessionIsolationCacheStub) GetSessionProviderID(context.Context, int64, string) (int64, error) {
	return 0, errors.New("not found")
}

// SetSessionProviderID accepts provider writes and leaves the fixture state unchanged.
func (c *sessionIsolationCacheStub) SetSessionProviderID(context.Context, int64, string, int64, time.Duration) error {
	return nil
}

// RefreshSessionTTL accepts provider-lifetime requests without changing owner test state.
func (c *sessionIsolationCacheStub) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}

// DeleteSessionProviderID accepts provider removals without changing owner test state.
func (c *sessionIsolationCacheStub) DeleteSessionProviderID(context.Context, int64, string) error {
	return nil
}

// SetSessionOwnerGroupID inserts fixture ownership and TTL under one mutex.
// A preexisting user/source/hash returns false with its values retained; every
// invocation increments setCalls, including a rejected insertion attempt.
func (c *sessionIsolationCacheStub) SetSessionOwnerGroupID(_ context.Context, userID int64, source, sessionHash string, groupID int64, ttl time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setCalls++
	if c.owners == nil {
		c.owners = make(map[string]int64)
	}
	if c.ownerTTLs == nil {
		c.ownerTTLs = make(map[string]time.Duration)
	}
	key := c.ownerKey(userID, source, sessionHash)
	if _, ok := c.owners[key]; ok {
		return false, nil
	}
	// Keep ownership and its recorded TTL in the same critical section so
	// the losing first-bind worker reads the winner's complete fixture state.
	c.owners[key] = groupID
	c.ownerTTLs[key] = ttl
	return true, nil
}

// GetSessionOwnerGroupID counts and reads fixture ownership under one mutex.
// It returns the group's ID when present and a not-found error otherwise, allowing
// tests to model insertion followed by owner lookup.
func (c *sessionIsolationCacheStub) GetSessionOwnerGroupID(_ context.Context, userID int64, source, sessionHash string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.getCalls++
	key := c.ownerKey(userID, source, sessionHash)
	if owner, ok := c.owners[key]; ok {
		return owner, nil
	}
	return 0, errors.New("not found")
}

// RefreshSessionOwnerTTL counts a renewal and assigns its fixture TTL under the
// mutex. It does not change the group owner or model expiration, and it returns
// nil after the assignment.
func (c *sessionIsolationCacheStub) RefreshSessionOwnerTTL(_ context.Context, userID int64, source, sessionHash string, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshCalls++
	if c.ownerTTLs == nil {
		c.ownerTTLs = make(map[string]time.Duration)
	}
	c.ownerTTLs[c.ownerKey(userID, source, sessionHash)] = ttl
	return nil
}
