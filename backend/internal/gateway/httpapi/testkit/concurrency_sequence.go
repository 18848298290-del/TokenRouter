package testkit

import (
	"context"
	"sync"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// User and provider wait tests consume configured admission results one attempt
// at a time. A scheduler may retry acquisition while cancellation independently
// releases a prior slot, so sequence consumption and observed counters share a
// mutex. The fixture records calls even when an admission sequence has no
// remaining values, making exhaustion visible to assertions.
//
// User wait registration copies its permission and callback while recording the
// requested queue limit. The callback then runs after unlocking so it can
// cancel a request or inspect fixture data. Its changes affect later calls; the
// current call returns the permission already copied for that registration.

// ConcurrencySequence supplies ordered slot results and observable scheduler
// cleanup counts.
type ConcurrencySequence struct {
	// Mu protects sequence consumption, recorded counters and copied wait
	// admission settings. Tests configure fields before concurrent use and inspect
	// ordinary counters after operations finish.
	Mu sync.Mutex // sequence and counter lock

	ProviderSeq []bool // remaining provider admission results
	UserSeq     []bool // remaining user admission results

	ProviderAcquireCalls int  // provider acquisition call count
	UserAcquireCalls     int  // user acquisition call count
	ProviderReleaseCalls int  // provider release call count
	UserReleaseCalls     int  // user release call count
	WaitAllowed          bool // user queue admission result
	WaitIncrementCalls   int  // user wait registration count
	WaitDecrementCalls   int  // user wait removal count
	WaitMaxWait          int  // last queue admission limit
	// WaitIncrementHook runs after admission values are copied and Mu is unlocked.
	// It can trigger cancellation without changing the result already chosen for
	// this call.
	WaitIncrementHook  func()
	APIKeyTrackCalls   int     // Key-statistics acquisition call count
	APIKeyReleaseCalls int     // Key-statistics release call count
	APIKeyTrackIDs     []int64 // tracked Key identity sequence
}

// ConcurrencySequenceWithError replaces provider acquisition with an injected
// store failure.
type ConcurrencySequenceWithError struct {
	ConcurrencySequence       // embedded sequence fixture behavior
	Err                 error // injected provider store error
}

// AcquireProviderSlot consumes the next provider admission result and records
// this call under Mu. An exhausted sequence reports false with no error,
// letting the scheduler remain in its waiting path.
//
// The lock covers both counting and removal of the chosen value, so concurrent
// attempts cannot consume the same entry. Tests configure the sequence before
// use and synchronize inspection when callbacks may still be active; the call
// counter includes unsuccessful attempts after exhaustion.
func (s *ConcurrencySequence) AcquireProviderSlot(ctx context.Context, providerID int64, maxConcurrency int, requestID string) (bool, error) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	// Keep this provider attempt count and sequence consumption together under Mu,
	// so assertions can relate each consumed result to one recorded call.
	s.ProviderAcquireCalls++
	if len(s.ProviderSeq) == 0 {
		return false, nil
	}
	v := s.ProviderSeq[0]
	s.ProviderSeq = s.ProviderSeq[1:]
	return v, nil
}

// ReleaseProviderSlot counts a provider cleanup under the fixture mutex.
func (s *ConcurrencySequence) ReleaseProviderSlot(ctx context.Context, providerID int64, requestID string) error {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	// Release callbacks may run during cancellation while acquisition continues;
	// update the provider cleanup count under Mu so concurrent fixture
	// observations stay consistent.
	s.ProviderReleaseCalls++
	return nil
}

// GetProviderConcurrency returns zero occupancy for the requested fixture
// provider.
func (s *ConcurrencySequence) GetProviderConcurrency(ctx context.Context, providerID int64) (int, error) {
	return 0, nil
}

// GetProviderConcurrencyBatch returns a zero entry for each requested provider.
func (s *ConcurrencySequence) GetProviderConcurrencyBatch(ctx context.Context, providerIDs []int64) (map[int64]int, error) {
	out := make(map[int64]int, len(providerIDs))
	for _, providerID := range providerIDs {
		out[providerID] = 0
	}
	return out, nil
}

// IncrementProviderWaitCount accepts provider wait registration without
// retaining a count.
func (s *ConcurrencySequence) IncrementProviderWaitCount(ctx context.Context, providerID int64, maxWait int) (bool, error) {
	return true, nil
}

// DecrementProviderWaitCount reports success without tracking provider waiting
// membership.
func (s *ConcurrencySequence) DecrementProviderWaitCount(ctx context.Context, providerID int64) error {
	return nil
}

// GetProviderWaitingCount returns zero provider waiting occupancy in this
// fixture.
func (s *ConcurrencySequence) GetProviderWaitingCount(ctx context.Context, providerID int64) (int, error) {
	return 0, nil
}

// AcquireUserSlot consumes the next user admission result and records this call
// under Mu. An exhausted sequence reports false with no error, letting the
// scheduler remain in its waiting path.
//
// The lock covers both counting and removal of the chosen value, so concurrent
// attempts cannot consume the same entry. Tests configure the sequence before
// use and synchronize inspection when callbacks may still be active; the call
// counter includes unsuccessful attempts after exhaustion.
func (s *ConcurrencySequence) AcquireUserSlot(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	// Keep this user attempt count and sequence consumption together under Mu, so
	// assertions can relate each consumed result to one recorded call.
	s.UserAcquireCalls++
	if len(s.UserSeq) == 0 {
		return false, nil
	}
	v := s.UserSeq[0]
	s.UserSeq = s.UserSeq[1:]
	return v, nil
}

// ReleaseUserSlot counts user cleanup while holding the shared fixture mutex.
func (s *ConcurrencySequence) ReleaseUserSlot(ctx context.Context, userID int64, requestID string) error {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	// Release callbacks may run during cancellation while acquisition continues;
	// update the user cleanup count under Mu so concurrent fixture observations
	// stay consistent.
	s.UserReleaseCalls++
	return nil
}

// GetUserConcurrency returns zero occupancy for the requested fixture user.
func (s *ConcurrencySequence) GetUserConcurrency(ctx context.Context, userID int64) (int, error) {
	return 0, nil
}

// TrackAPIKeySlot records the Key ID and track count under Mu.
func (s *ConcurrencySequence) TrackAPIKeySlot(ctx context.Context, apiKeyID int64, requestID string) error {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	// Record the tracked Key identity together with its count under Mu so
	// concurrent tests see matching identity and acquisition observations.
	s.APIKeyTrackCalls++
	s.APIKeyTrackIDs = append(s.APIKeyTrackIDs, apiKeyID)
	return nil
}

// ReleaseAPIKeySlot counts Key-statistics cleanup under the shared fixture
// mutex.
func (s *ConcurrencySequence) ReleaseAPIKeySlot(ctx context.Context, apiKeyID int64, requestID string) error {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	// Key cleanup can run independently of request inspection; serialize this
	// release count with the other fixture observations under Mu.
	s.APIKeyReleaseCalls++
	return nil
}

// GetAPIKeyConcurrencyBatch returns zero occupancy for every requested Key ID.
func (s *ConcurrencySequence) GetAPIKeyConcurrencyBatch(ctx context.Context, apiKeyIDs []int64) (map[int64]int, error) {
	out := make(map[int64]int, len(apiKeyIDs))
	for _, apiKeyID := range apiKeyIDs {
		out[apiKeyID] = 0
	}
	return out, nil
}

// IncrementWaitCount records user queue admission and copies its decision and
// hook under Mu. The hook runs after unlocking, so it can cancel the request or
// inspect the fixture without lock recursion.
//
// The previously copied WaitAllowed value determines the result even if the
// hook changes configuration. Recorded call and limit values remain available
// after denial; they describe the attempted registration, while the returned
// boolean determines whether the scheduler owns a waiting entry.
func (s *ConcurrencySequence) IncrementWaitCount(ctx context.Context, userID int64, maxWait int) (bool, error) {
	s.Mu.Lock()
	// Capture admission configuration with the call count and limit before the
	// hook runs, so its later changes cannot rewrite this registration decision.
	s.WaitIncrementCalls++
	s.WaitMaxWait = maxWait
	waitAllowed := s.WaitAllowed
	hook := s.WaitIncrementHook
	s.Mu.Unlock()

	// Run the copied hook after unlocking so cancellation can progress
	// independently and hook inspection cannot deadlock on the fixture mutex.
	if hook != nil {
		hook()
	}
	if !waitAllowed {
		return false, nil
	}
	return true, nil
}

// DecrementWaitCount counts removed user wait entries under the fixture mutex.
func (s *ConcurrencySequence) DecrementWaitCount(ctx context.Context, userID int64) error {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	// Queue cleanup can run after cancellation or timeout; record removal under Mu
	// with the other wait observations to retain consistent counts.
	s.WaitDecrementCalls++
	return nil
}

// GetProvidersLoadBatch returns ID-labelled provider load entries with zero
// measurements.
func (s *ConcurrencySequence) GetProvidersLoadBatch(ctx context.Context, providers []scheduler.ProviderWithConcurrency) (map[int64]*scheduler.ProviderLoadInfo, error) {
	out := make(map[int64]*scheduler.ProviderLoadInfo, len(providers))
	for _, acc := range providers {
		out[acc.ID] = &scheduler.ProviderLoadInfo{ProviderID: acc.ID}
	}
	return out, nil
}

// GetUsersLoadBatch returns ID-labelled user load entries with zero
// measurements.
func (s *ConcurrencySequence) GetUsersLoadBatch(ctx context.Context, users []scheduler.UserWithConcurrency) (map[int64]*scheduler.UserLoadInfo, error) {
	out := make(map[int64]*scheduler.UserLoadInfo, len(users))
	for _, user := range users {
		out[user.ID] = &scheduler.UserLoadInfo{UserID: user.ID}
	}
	return out, nil
}

// CleanupExpiredProviderSlots reports success without changing the injected
// admission sequence.
func (s *ConcurrencySequence) CleanupExpiredProviderSlots(ctx context.Context, providerID int64) error {
	return nil
}

// CleanupExpiredProviderSlotKeys reports success without maintaining expiration
// keys for fixtures.
func (s *ConcurrencySequence) CleanupExpiredProviderSlotKeys(ctx context.Context) error {
	return nil
}

// CleanupStaleProcessSlots reports success without tracking fixture process
// slot records.
func (s *ConcurrencySequence) CleanupStaleProcessSlots(ctx context.Context, activeRequestPrefix string) error {
	return nil
}

// AcquireProviderSlot returns the configured store error and reports no slot.
// It bypasses the embedded admission sequence and its counters, so tests can
// exercise the scheduler error path directly.
func (s *ConcurrencySequenceWithError) AcquireProviderSlot(ctx context.Context, providerID int64, maxConcurrency int, requestID string) (bool, error) {
	return false, s.Err
}
