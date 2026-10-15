package testutil

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// session_limit_cache.go provides empty session-limit and billing-window
// observations for test fixtures.
// Session-limit fixtures sometimes need scheduling to proceed while omitting
// stored active sessions and cost windows. StubSessionLimitCache accepts
// registration and cleanup, reports no active session, and makes cost lookups
// miss, allowing callers to exercise their uncached paths.

// These assertions keep the shared fixture usable by session-limit scheduling
// and billing window-cost consumers as their required cache methods evolve.
var (
	_ scheduler.SessionLimitCache = StubSessionLimitCache{}
	_ billing.WindowCostCache     = StubSessionLimitCache{}
)

// StubSessionLimitCache supplies fixed empty session counts and missing cost
// observations. It ignores identifiers, TTLs, and cancellation; registrations
// and cost writes leave subsequent fixture reads unchanged.
type StubSessionLimitCache struct{}

// RegisterSession returns successful registration for every session fixture
// request. The limit and TTL are ignored, and later active checks still return
// false because the stub stores no session membership.
func (c StubSessionLimitCache) RegisterSession(_ context.Context, _ int64, _ string, _ int, _ time.Duration) (bool, error) {
	return true, nil
}

// RefreshSession accepts an expiration refresh and returns a nil fixture error.
func (c StubSessionLimitCache) RefreshSession(_ context.Context, _ int64, _ string, _ time.Duration) error {
	return nil
}

// UnregisterSession accepts membership cleanup and returns a nil fixture error.
func (c StubSessionLimitCache) UnregisterSession(_ context.Context, _ int64, _ string) error {
	return nil
}

// GetActiveSessionCount returns zero active sessions with a nil fixture error.
func (c StubSessionLimitCache) GetActiveSessionCount(_ context.Context, _ int64) (int, error) {
	return 0, nil
}

// GetActiveSessionCountBatch returns a nil result map and a nil fixture error.
// Requested identifiers and timeout overrides are ignored, so consumers observe
// missing per-provider counts through nil-map lookup.
func (c StubSessionLimitCache) GetActiveSessionCountBatch(_ context.Context, _ []int64, _ map[int64]time.Duration) (map[int64]int, error) {
	return nil, nil
}

// IsSessionActive returns false and a nil error for every supplied fixture
// session. Even a previously registered session remains absent, so
// membership-sensitive tests need a cache that records registrations.
func (c StubSessionLimitCache) IsSessionActive(_ context.Context, _ int64, _ string) (bool, error) {
	return false, nil
}

// GetWindowCost returns zero cost with a false hit flag and no error. This
// reports a missing cost observation, allowing billing consumers to exercise
// their cache-miss behavior.
func (c StubSessionLimitCache) GetWindowCost(_ context.Context, _ int64) (float64, bool, error) {
	return 0, false, nil
}

// SetWindowCost accepts a cost publication and returns a nil fixture error.
func (c StubSessionLimitCache) SetWindowCost(_ context.Context, _ int64, _ float64) error {
	return nil
}

// GetWindowCostBatch returns a nil map and a nil error for any requested IDs.
// Per-provider lookups therefore remain absent even after SetWindowCost accepts
// a value in the fixture.
func (c StubSessionLimitCache) GetWindowCostBatch(_ context.Context, _ []int64) (map[int64]float64, error) {
	return nil, nil
}
