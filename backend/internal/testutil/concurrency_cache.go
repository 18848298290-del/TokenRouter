package testutil

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// Compile-time interface assertion.
var _ scheduler.ConcurrencyCache = StubConcurrencyCache{}

// StubConcurrencyCache accepts slot requests and reports zero load.
type StubConcurrencyCache struct{}

// AcquireProviderSlot reports a successful slot acquisition.
func (c StubConcurrencyCache) AcquireProviderSlot(_ context.Context, _ int64, _ int, _ string) (bool, error) {
	return true, nil
}

// ReleaseProviderSlot reports success without storing slot state.
func (c StubConcurrencyCache) ReleaseProviderSlot(_ context.Context, _ int64, _ string) error {
	return nil
}

// GetProviderConcurrency returns zero.
func (c StubConcurrencyCache) GetProviderConcurrency(_ context.Context, _ int64) (int, error) {
	return 0, nil
}

// IncrementProviderWaitCount accepts the waiting provider.
func (c StubConcurrencyCache) IncrementProviderWaitCount(_ context.Context, _ int64, _ int) (bool, error) {
	return true, nil
}

// DecrementProviderWaitCount reports success without storing queue state.
func (c StubConcurrencyCache) DecrementProviderWaitCount(_ context.Context, _ int64) error {
	return nil
}

// GetProviderWaitingCount returns zero.
func (c StubConcurrencyCache) GetProviderWaitingCount(_ context.Context, _ int64) (int, error) {
	return 0, nil
}

// AcquireUserSlot reports a successful slot acquisition.
func (c StubConcurrencyCache) AcquireUserSlot(_ context.Context, _ int64, _ int, _ string) (bool, error) {
	return true, nil
}

// ReleaseUserSlot reports success without storing slot state.
func (c StubConcurrencyCache) ReleaseUserSlot(_ context.Context, _ int64, _ string) error {
	return nil
}

// GetUserConcurrency returns zero.
func (c StubConcurrencyCache) GetUserConcurrency(_ context.Context, _ int64) (int, error) {
	return 0, nil
}

// IncrementWaitCount accepts the waiting user.
func (c StubConcurrencyCache) IncrementWaitCount(_ context.Context, _ int64, _ int) (bool, error) {
	return true, nil
}

// DecrementWaitCount reports success without storing queue state.
func (c StubConcurrencyCache) DecrementWaitCount(_ context.Context, _ int64) error { return nil }

// GetProvidersLoadBatch returns zero load for each requested provider.
func (c StubConcurrencyCache) GetProvidersLoadBatch(_ context.Context, providers []scheduler.ProviderWithConcurrency) (map[int64]*scheduler.ProviderLoadInfo, error) {
	result := make(map[int64]*scheduler.ProviderLoadInfo, len(providers))
	for _, acc := range providers {
		result[acc.ID] = &scheduler.ProviderLoadInfo{ProviderID: acc.ID, LoadRate: 0}
	}
	return result, nil
}

// GetUsersLoadBatch returns zero load for each requested user.
func (c StubConcurrencyCache) GetUsersLoadBatch(_ context.Context, users []scheduler.UserWithConcurrency) (map[int64]*scheduler.UserLoadInfo, error) {
	result := make(map[int64]*scheduler.UserLoadInfo, len(users))
	for _, u := range users {
		result[u.ID] = &scheduler.UserLoadInfo{UserID: u.ID, LoadRate: 0}
	}
	return result, nil
}

// GetProviderConcurrencyBatch returns zero concurrency for each requested provider.
func (c StubConcurrencyCache) GetProviderConcurrencyBatch(_ context.Context, providerIDs []int64) (map[int64]int, error) {
	result := make(map[int64]int, len(providerIDs))
	for _, id := range providerIDs {
		result[id] = 0
	}
	return result, nil
}

// CleanupExpiredProviderSlots reports success without storing slot state.
func (c StubConcurrencyCache) CleanupExpiredProviderSlots(_ context.Context, _ int64) error {
	return nil
}

// CleanupExpiredProviderSlotKeys reports success without storing slot keys.
func (c StubConcurrencyCache) CleanupExpiredProviderSlotKeys(_ context.Context) error {
	return nil
}

// CleanupStaleProcessSlots reports success without storing process slots.
func (c StubConcurrencyCache) CleanupStaleProcessSlots(_ context.Context, _ string) error {
	return nil
}
