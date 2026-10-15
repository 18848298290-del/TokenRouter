package testkit

import (
	"context"
	"sync/atomic"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// concurrency_hooks.go provides configurable slot-store test hooks and atomic
// counters for user, provider and ingress releases.
// Slot admission tests inject acquisition results while release counters record
// cleanup from cancellation and normal return. The store supplies empty
// occupancy and load observations, accepted wait registration and successful
// renewal defaults, keeping each test focused on its configured hooks.

// ConcurrencyHooks implements the scheduler store using injected admission and
// cleanup hooks.
type ConcurrencyHooks struct {
	// AcquireUserSlotFn controls user admission when nonnil; a missing hook
	// returns false with no error so tests determine their own successful slot
	// setup.
	AcquireUserSlotFn func(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error)
	// AcquireProviderSlotFn controls provider admission when nonnil; an absent
	// hook leaves capacity unavailable, with no error reported by this fixture
	// store.
	AcquireProviderSlotFn func(ctx context.Context, providerID int64, maxConcurrency int, requestID string) (bool, error)
	// AcquireIngressLeaseFn controls connection lease admission when nonnil;
	// missing configuration returns false with no error and remains independent of
	// per-turn slot hooks.
	AcquireIngressLeaseFn func(ctx context.Context, apiKeyID int64, maxConnections int, leaseID string) (bool, error)
	// ReleaseIngressLeaseFn optionally injects a release error after the atomic
	// counter increases, so its failure cannot hide that cleanup was attempted.
	ReleaseIngressLeaseFn func(ctx context.Context, apiKeyID int64, leaseID string) error
	// ReleaseUserCalled, ReleaseProviderCalled and ReleaseIngressCalled count
	// cleanup attempts with atomic writes. Readers use atomic loads while
	// cancellation and normal completion callbacks may still be active.
	ReleaseUserCalled     int32 // atomic user release count
	ReleaseProviderCalled int32 // atomic provider release count
	ReleaseIngressCalled  int32 // atomic ingress release count
}

// AcquireProviderSlot invokes the configured provider admission hook when
// present. An absent hook reports unavailable capacity with no error, allowing
// tests to inject acquisition behavior without modeling a real store.
func (m *ConcurrencyHooks) AcquireProviderSlot(ctx context.Context, providerID int64, maxConcurrency int, requestID string) (bool, error) {
	if m.AcquireProviderSlotFn != nil {
		return m.AcquireProviderSlotFn(ctx, providerID, maxConcurrency, requestID)
	}
	return false, nil
}

// ReleaseProviderSlot atomically counts a provider release and returns success.
func (m *ConcurrencyHooks) ReleaseProviderSlot(ctx context.Context, providerID int64, requestID string) error {
	// Record provider cleanup atomically because cancellation callbacks can run
	// independently of the request goroutine inspecting this fixture store.
	atomic.AddInt32(&m.ReleaseProviderCalled, 1)
	return nil
}

// GetProviderConcurrency returns an empty provider occupancy for this fixture.
func (m *ConcurrencyHooks) GetProviderConcurrency(ctx context.Context, providerID int64) (int, error) {
	return 0, nil
}

// GetProviderConcurrencyBatch returns zero occupancy for every requested
// provider ID.
func (m *ConcurrencyHooks) GetProviderConcurrencyBatch(ctx context.Context, providerIDs []int64) (map[int64]int, error) {
	result := make(map[int64]int, len(providerIDs))
	for _, providerID := range providerIDs {
		result[providerID] = 0
	}
	return result, nil
}

// IncrementProviderWaitCount admits provider waiting without storing a fixture
// count.
func (m *ConcurrencyHooks) IncrementProviderWaitCount(ctx context.Context, providerID int64, maxWait int) (bool, error) {
	return true, nil
}

// DecrementProviderWaitCount reports successful removal without tracking
// provider wait membership.
func (m *ConcurrencyHooks) DecrementProviderWaitCount(ctx context.Context, providerID int64) error {
	return nil
}

// GetProviderWaitingCount returns zero waiting requests for every provider
// lookup.
func (m *ConcurrencyHooks) GetProviderWaitingCount(ctx context.Context, providerID int64) (int, error) {
	return 0, nil
}

// AcquireUserSlot invokes the configured user admission hook when supplied. An
// absent hook reports unavailable capacity with no error; tests install the
// hook to decide the observed user slot result.
func (m *ConcurrencyHooks) AcquireUserSlot(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error) {
	if m.AcquireUserSlotFn != nil {
		return m.AcquireUserSlotFn(ctx, userID, maxConcurrency, requestID)
	}
	return false, nil
}

// ReleaseUserSlot atomically counts a user release and returns success.
func (m *ConcurrencyHooks) ReleaseUserSlot(ctx context.Context, userID int64, requestID string) error {
	// Record user cleanup atomically because cancellation callbacks can run
	// independently of the request goroutine inspecting this fixture store.
	atomic.AddInt32(&m.ReleaseUserCalled, 1)
	return nil
}

// GetUserConcurrency returns zero occupancy for every fixture user lookup.
func (m *ConcurrencyHooks) GetUserConcurrency(ctx context.Context, userID int64) (int, error) {
	return 0, nil
}

// IncrementWaitCount admits user waiting without storing fixture queue
// membership.
func (m *ConcurrencyHooks) IncrementWaitCount(ctx context.Context, userID int64, maxWait int) (bool, error) {
	return true, nil
}

// DecrementWaitCount reports successful removal without tracking user wait
// membership.
func (m *ConcurrencyHooks) DecrementWaitCount(ctx context.Context, userID int64) error {
	return nil
}

// GetProvidersLoadBatch returns an empty provider-load map for scheduler
// observations.
func (m *ConcurrencyHooks) GetProvidersLoadBatch(ctx context.Context, providers []scheduler.ProviderWithConcurrency) (map[int64]*scheduler.ProviderLoadInfo, error) {
	return map[int64]*scheduler.ProviderLoadInfo{}, nil
}

// GetUsersLoadBatch returns an empty user-load map for scheduler observations.
func (m *ConcurrencyHooks) GetUsersLoadBatch(ctx context.Context, users []scheduler.UserWithConcurrency) (map[int64]*scheduler.UserLoadInfo, error) {
	return map[int64]*scheduler.UserLoadInfo{}, nil
}

// CleanupExpiredProviderSlots reports success without expiring any fixture
// provider slots.
func (m *ConcurrencyHooks) CleanupExpiredProviderSlots(ctx context.Context, providerID int64) error {
	return nil
}

// CleanupExpiredProviderSlotKeys reports success without maintaining fixture
// expiration keys.
func (m *ConcurrencyHooks) CleanupExpiredProviderSlotKeys(ctx context.Context) error {
	return nil
}

// CleanupStaleProcessSlots reports success without tracking fixture process
// slot ownership.
func (m *ConcurrencyHooks) CleanupStaleProcessSlots(ctx context.Context, activeRequestPrefix string) error {
	return nil
}

// AcquireOpenAIWSIngressLease invokes the configured connection admission hook
// when present. An absent hook reports no lease with no error, allowing tests
// to inject ingress capacity independently of user and provider slots.
func (m *ConcurrencyHooks) AcquireOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, maxConnections int, leaseID string) (bool, error) {
	if m.AcquireIngressLeaseFn != nil {
		return m.AcquireIngressLeaseFn(ctx, apiKeyID, maxConnections, leaseID)
	}
	return false, nil
}

// RefreshOpenAIWSIngressLease reports successful renewal for every fixture
// connection lease.
func (m *ConcurrencyHooks) RefreshOpenAIWSIngressLease(context.Context, int64, string) (bool, error) {
	return true, nil
}

// ReleaseOpenAIWSIngressLease atomically counts a release before invoking its
// optional hook. The hook result is returned when present; otherwise release
// succeeds, so failed injected cleanup remains visible in the attempt counter.
func (m *ConcurrencyHooks) ReleaseOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, leaseID string) error {
	// Count the cleanup attempt before the optional failure hook, retaining
	// evidence that ingress release was invoked even when storage reports an
	// error.
	atomic.AddInt32(&m.ReleaseIngressCalled, 1)
	if m.ReleaseIngressLeaseFn != nil {
		return m.ReleaseIngressLeaseFn(ctx, apiKeyID, leaseID)
	}
	return nil
}
