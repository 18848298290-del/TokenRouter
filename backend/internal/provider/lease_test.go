package provider

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Singleton maintenance distinguishes a held leader key from an unavailable
// backend. The in-memory fixture records owner-matched acquisition and release
// so tests can observe skipped contention, later admission after cleanup, and
// usable release callbacks when coordination is absent or failing.
//
// Fixture claims remain until explicit release, with no TTL expiry or context
// handling. The map lock covers availability checking and owner insertion
// together, and cleanup removes a claim only for its recorded owner. Injected
// backend failure is returned before that state is inspected, keeping the
// failure path separate from contention. Successive A and B owners make the
// admission transition visible through the held-key lookup.

// fakeLeaderLockCache models owner-matched leader claims for singleton lease
// tests.
type fakeLeaderLockCache struct {
	// mu protects owners during acquisition, release, and inspection. The map keeps
	// one owner per key until a matching release removes that claim.
	mu     sync.Mutex        // fixture leader state lock
	owners map[string]string // held key owner names
	// acquireErr is configured before fixture use and returned before inspecting
	// owners, allowing tests to model backend failure independently of contention.
	acquireErr error
}

// TryAcquireLeaderLock claims an unowned fixture key or reports contention. The
// injected acquisition error wins before map access; context and TTL inputs are
// ignored because these tests model ownership transitions directly.
//
// The mutex covers initial map creation, the existing-owner check, and insertion
// of the supplied owner. Competing acquisitions therefore cannot both claim one
// key before its recorded owner releases it. Held claims remain until explicit
// owner-matched cleanup rather than expiring according to the supplied duration.
func (f *fakeLeaderLockCache) TryAcquireLeaderLock(_ context.Context, key, owner string, _ time.Duration) (bool, error) {
	if f.acquireErr != nil {
		return false, f.acquireErr
	}
	// Keep the unowned check and claim insertion together so two acquisitions
	// cannot both observe an available fixture key before either owner is stored.
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.owners == nil {
		f.owners = map[string]string{}
	}
	if _, held := f.owners[key]; held {
		return false, nil
	}
	f.owners[key] = owner
	return true, nil
}

// ReleaseLeaderLock removes a fixture claim when its recorded owner matches the
// supplied owner. Missing keys and different owners succeed with the held state
// unchanged, and the cleanup context is ignored.
func (f *fakeLeaderLockCache) ReleaseLeaderLock(_ context.Context, key, owner string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Compare the recorded owner while holding the map lock so cleanup from a
	// different claimant leaves the currently held fixture lease available for
	// inspection.
	if f.owners[key] == owner {
		delete(f.owners, key)
	}
	return nil
}

// heldBy reads the owner currently recorded for a fixture key.
func (f *fakeLeaderLockCache) heldBy(key string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.owners[key]
}

// TestTryAcquireSingletonLeaderLock_NoBackendRunsUngated calls singleton
// admission with both backends absent. It requires successful admission and a
// nonnil release callback that can be invoked safely.
func TestTryAcquireSingletonLeaderLock_NoBackendRunsUngated(t *testing.T) {
	release, ok := AcquireSingletonLease(context.Background(), nil, nil, "k", "inst", time.Minute)
	require.True(t, ok)
	require.NotNil(t, release)
	require.NotPanics(t, release)
}

// TestTryAcquireSingletonLeaderLock_ContendedThenReleased holds one key for
// owner A while owner B attempts admission. It verifies rejection during that
// claim and successful B admission after A releases it.
func TestTryAcquireSingletonLeaderLock_ContendedThenReleased(t *testing.T) {
	cache := &fakeLeaderLockCache{}
	ctx := context.Background()
	const key = "leader:test:contended"

	releaseA, ok := AcquireSingletonLease(ctx, cache, nil, key, "A", time.Minute)
	require.True(t, ok)
	require.Equal(t, "A", cache.heldBy(key))

	_, okB := AcquireSingletonLease(ctx, cache, nil, key, "B", time.Minute)
	require.False(t, okB)

	releaseA()
	require.Empty(t, cache.heldBy(key))

	releaseB, okB := AcquireSingletonLease(ctx, cache, nil, key, "B", time.Minute)
	require.True(t, okB)
	releaseB()
}

// TestTryAcquireSingletonLeaderLock_CacheErrorFallsThrough injects a leader
// acquisition error with no advisory backend. It requires admitted work to
// receive a nonnil release callback that can be invoked safely.
func TestTryAcquireSingletonLeaderLock_CacheErrorFallsThrough(t *testing.T) {
	cache := &fakeLeaderLockCache{acquireErr: context.DeadlineExceeded}

	release, ok := AcquireSingletonLease(context.Background(), cache, nil, "k", "inst", time.Minute)

	require.True(t, ok)
	require.NotNil(t, release)
	require.NotPanics(t, release)
}
