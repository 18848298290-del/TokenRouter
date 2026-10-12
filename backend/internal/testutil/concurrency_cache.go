package testutil

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// Scheduler tests can reach provider selection and forwarding without
// installing a backing concurrency cache. StubConcurrencyCache accepts
// admissions and releases with fixed success results and returns zero counts or
// newly allocated zero-load entries, so those tests do not simulate changing
// occupancy.

// The interface assertion keeps this fixed-result fixture compatible with the
// concurrency methods required by scheduler.ConcurrencyCache when production
// cache capabilities change.
var _ scheduler.ConcurrencyCache = StubConcurrencyCache{}

// StubConcurrencyCache supplies fixed admission and load observations for
// scheduler fixtures. It ignores cancellation, identifiers, and limits and
// stores no occupancy, so consumers cannot use it to verify slot or queue
// accounting.
type StubConcurrencyCache struct{}

// AcquireProviderSlot returns a successful admission result for every provider
// fixture request. It ignores limits and cancellation, so this result permits
// the caller's forwarding path rather than measuring an occupied slot.
func (c StubConcurrencyCache) AcquireProviderSlot(_ context.Context, _ int64, _ int, _ string) (bool, error) {
	return true, nil
}

// ReleaseProviderSlot accepts provider cleanup calls and returns a nil fixture
// error.
func (c StubConcurrencyCache) ReleaseProviderSlot(_ context.Context, _ int64, _ string) error {
	return nil
}

// GetProviderConcurrency returns a zero occupied-slot count with a nil fixture
// error.
func (c StubConcurrencyCache) GetProviderConcurrency(_ context.Context, _ int64) (int, error) {
	return 0, nil
}

// IncrementProviderWaitCount returns successful waiter admission for the
// requested provider fixture. Repeated calls still succeed, and subsequent
// count reads remain zero because this stub has no queue state.
func (c StubConcurrencyCache) IncrementProviderWaitCount(_ context.Context, _ int64, _ int) (bool, error) {
	return true, nil
}

// DecrementProviderWaitCount accepts provider waiter cleanup and returns a nil
// fixture error.
func (c StubConcurrencyCache) DecrementProviderWaitCount(_ context.Context, _ int64) error {
	return nil
}

// GetProviderWaitingCount returns zero waiting requests and a nil fixture
// error.
func (c StubConcurrencyCache) GetProviderWaitingCount(_ context.Context, _ int64) (int, error) {
	return 0, nil
}

// AcquireUserSlot returns successful admission for every user fixture request,
// including repeated calls. The user limit and request identifier are ignored,
// so tests exercise admitted paths without occupancy tracking.
func (c StubConcurrencyCache) AcquireUserSlot(_ context.Context, _ int64, _ int, _ string) (bool, error) {
	return true, nil
}

// ReleaseUserSlot accepts user slot cleanup and returns a nil fixture error.
func (c StubConcurrencyCache) ReleaseUserSlot(_ context.Context, _ int64, _ string) error {
	return nil
}

// GetUserConcurrency returns zero occupied user slots and a nil fixture error.
func (c StubConcurrencyCache) GetUserConcurrency(_ context.Context, _ int64) (int, error) {
	return 0, nil
}

// IncrementWaitCount returns successful waiter admission for each supplied user
// fixture. The waiting limit is ignored, and later reads keep reporting zero
// because the stub records no queued requests.
func (c StubConcurrencyCache) IncrementWaitCount(_ context.Context, _ int64, _ int) (bool, error) {
	return true, nil
}

// DecrementWaitCount accepts user waiter cleanup and returns a nil fixture
// error.
func (c StubConcurrencyCache) DecrementWaitCount(_ context.Context, _ int64) error { return nil }

// GetProvidersLoadBatch allocates zero-load entries indexed by each supplied
// provider ID. Duplicate IDs collapse to one map entry, empty input yields an
// allocated empty map, and concurrency limits are ignored.
func (c StubConcurrencyCache) GetProvidersLoadBatch(_ context.Context, providers []scheduler.ProviderWithConcurrency) (map[int64]*scheduler.ProviderLoadInfo, error) {
	result := make(map[int64]*scheduler.ProviderLoadInfo, len(providers))
	for _, acc := range providers {
		result[acc.ID] = &scheduler.ProviderLoadInfo{ProviderID: acc.ID, LoadRate: 0}
	}
	return result, nil
}

// GetUsersLoadBatch allocates zero-load entries indexed by each supplied user
// ID. Repeated IDs collapse into one map entry, empty input yields an allocated
// empty map, and fixture limits are ignored.
func (c StubConcurrencyCache) GetUsersLoadBatch(_ context.Context, users []scheduler.UserWithConcurrency) (map[int64]*scheduler.UserLoadInfo, error) {
	result := make(map[int64]*scheduler.UserLoadInfo, len(users))
	for _, u := range users {
		result[u.ID] = &scheduler.UserLoadInfo{UserID: u.ID, LoadRate: 0}
	}
	return result, nil
}

// GetProviderConcurrencyBatch returns a newly allocated map of requested IDs to
// zero counts. Repeated IDs collapse into one entry, and nil input returns an
// allocated empty map.
func (c StubConcurrencyCache) GetProviderConcurrencyBatch(_ context.Context, providerIDs []int64) (map[int64]int, error) {
	result := make(map[int64]int, len(providerIDs))
	for _, id := range providerIDs {
		result[id] = 0
	}
	return result, nil
}

// CleanupExpiredProviderSlots accepts expiry cleanup for a provider and returns
// a nil error.
func (c StubConcurrencyCache) CleanupExpiredProviderSlots(_ context.Context, _ int64) error {
	return nil
}

// CleanupExpiredProviderSlotKeys accepts key cleanup calls and returns a nil
// fixture error.
func (c StubConcurrencyCache) CleanupExpiredProviderSlotKeys(_ context.Context) error {
	return nil
}

// CleanupStaleProcessSlots accepts process cleanup calls and returns a nil
// fixture error.
func (c StubConcurrencyCache) CleanupStaleProcessSlots(_ context.Context, _ string) error {
	return nil
}
