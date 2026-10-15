package provider

import (
	"sync"
	"sync/atomic"
	"time"
)

// runtime_block.go coordinates provider cooldowns, credential failure blocks,
// retry windows, and conditional recovery.
// Provider-wide cooldowns and credential-specific refresh failures can block
// the same provider for different reasons. A new provider declaration extends
// its deadline without shortening a longer window, but advances its generation
// even when the deadline stays unchanged. Provisional rollback uses that
// generation and the installed deadline to avoid removing a later declaration.
//
// Recovery clears both cooldown scopes and records a separate failure-clear
// generation. Publishers capture that value before persistence and reject
// notices after a clear; independently captured credential versions can
// otherwise keep separate deadlines. Accepted credential notices and retry
// timestamps do not advance the provider generation, so a managed recovery
// fence protects only mutations represented by that counter. Retry windows
// retain their first stored observation until explicitly reset.

// RuntimeBlockFallback and RuntimeRetryWindow define the default live cooldown
// and the fixed same-provider retry interval used by runtime state.
const (
	RuntimeBlockFallback = 2 * time.Minute // missing-deadline cooldown
	RuntimeRetryWindow   = 2 * time.Minute // first-observation retry interval
)

// RuntimeBlockState shares runtime cooldowns, retry observations, and recovery
// fences across callers. Construct it with NewRuntimeBlockState for deadline
// operations, and do not copy the value after first use.
type RuntimeBlockState struct {
	// until, locks, and generation coordinate provider-wide mutations under each
	// provider's mutex. Deadline entries written by this state are comparable
	// time.Time values; generation records declarations, clear, expiry cleanup,
	// and rollback.
	until      sync.Map      // provider cooldown deadlines
	locks      sync.Map      // provider mutation mutexes
	generation sync.Map      // represented provider mutations
	sequence   atomic.Uint64 // global generation counter
	// retryStarted stores the first installed retry time per provider. Expired
	// entries remain closed until ResetRetry or recovery deletes them; accesses
	// are independent of provider mutexes.
	retryStarted sync.Map
	// refreshFailureBlocks and refreshFailureClearGeneration coordinate notice
	// publication with recovery. Publishers capture the clear generation before
	// storage; clearing advances it, while accepted credential notices retain that
	// generation.
	// credential-specific failure deadlines
	refreshFailureBlocks RefreshFailureBlocks
	// provider failure-clear generations
	refreshFailureClearGeneration sync.Map
	// now supplies deadline comparisons and is initialized by NewRuntimeBlockState.
	// Supply a clock safe for concurrent calls. Provider operations invoke it
	// while holding their mutex, so it must not reenter operations needing that
	// same provider mutex.
	now func() time.Time
}

// NewRuntimeBlockState constructs empty runtime state with a clock for deadline
// operations. A nil callback selects time.Now; a supplied callback may be
// called concurrently by different provider and retry operations.
func NewRuntimeBlockState(now func() time.Time) *RuntimeBlockState {
	if now == nil {
		now = time.Now
	}
	return &RuntimeBlockState{now: now}
}

// Block installs or extends a provider-wide cooldown and advances its mutation
// generation, including declarations retaining a longer existing deadline. Nil
// state is ignored, nonfuture deadlines use RuntimeBlockFallback, and reason is
// discarded.
func (s *RuntimeBlockState) Block(id int64, until time.Time, reason string) {
	if s == nil {
		return
	}
	mu := s.lock(id)
	mu.Lock()
	defer mu.Unlock()
	_, _ = s.blockLocked(id, until, reason)
}

// BlockProviderScheduling forwards OpenAI and Grok records to the provider-wide
// Block operation. Nil records and other platforms are skipped; Block supplies
// fallback deadlines and retains any longer current window.
func (s *RuntimeBlockState) BlockProviderScheduling(value *Record, until time.Time, reason string) {
	if value == nil || (value.Platform != PlatformOpenAI && value.Platform != PlatformGrok) {
		return
	}
	s.Block(value.ID, until, reason)
}

// ResetRetry deletes the provider's stored retry observation so subsequent
// checks can begin a fresh interval. Callers use a nonnil instance; this
// deletion proceeds independently of provider mutation locks.
func (s *RuntimeBlockState) ResetRetry(id int64) {
	// Deletion lets a later retry claim a fresh interval. A concurrent check that
	// loads first can still evaluate the previous interval, so reset is not a
	// barrier for in-progress checks.
	s.retryStarted.Delete(id)
}

// RetryWindowActive starts a fixed retry interval when its first timestamp is
// stored and reports whether the service clock remains before its deadline. Nil
// state returns false. Expired timestamps remain stored, so later checks stay
// closed until ResetRetry or recovery deletes them.
//
// LoadOrStore shares the first installed timestamp across concurrent normal
// observations instead of sliding the window on every check. An unexpectedly
// typed entry is replaced with the current time before this decision. The
// interval measures clock observations and does not schedule a timer or remove
// expired history.
func (s *RuntimeBlockState) RetryWindowActive(providerID int64) bool {
	if s == nil {
		return false
	}
	now := s.now()
	// Retain the first installed retry timestamp across repeated checks, so a
	// continuing series of retries cannot extend or reopen this provider's fixed
	// recovery interval.
	value, _ := s.retryStarted.LoadOrStore(providerID, now)
	startedAt, ok := value.(time.Time)
	// An unusable timestamp cannot establish a retry deadline. Replace that entry
	// with this observation so the decision uses a valid clock value.
	if !ok {
		s.retryStarted.Store(providerID, now)
		startedAt = now
	}
	return now.Before(startedAt.Add(RuntimeRetryWindow))
}

// RetryDeadline returns the deadline derived from a stored first retry
// observation. Nil state, absent entries, and unexpected types return zero; an
// expired observation still returns its deadline and remains stored.
func (s *RuntimeBlockState) RetryDeadline(providerID int64) time.Time {
	if s == nil {
		return time.Time{}
	}
	value, ok := s.retryStarted.Load(providerID)
	if !ok {
		return time.Time{}
	}
	startedAt, ok := value.(time.Time)
	if !ok {
		return time.Time{}
	}
	return startedAt.Add(RuntimeRetryWindow)
}

// lock obtains the mutex stored for providerID, allocating it on first use.
// Normal entries are reused; an unexpected stored type is replaced, and callers
// acquire the returned mutex before provider-wide mutations.
func (s *RuntimeBlockState) lock(providerID int64) *sync.Mutex {
	actual, _ := s.locks.LoadOrStore(providerID, &sync.Mutex{})
	mu, ok := actual.(*sync.Mutex)
	// An unexpected lock entry cannot serialize provider mutations. Replace it
	// with a usable mutex before the caller enters the corresponding state update.
	if !ok {
		mu = &sync.Mutex{}
		s.locks.Store(providerID, mu)
	}
	return mu
}

// blockLocked records a provider declaration and reports its generation and
// whether the deadline changed. Callers hold that provider's mutex and retain
// comparable until entries; this state writes time.Time values. Every
// declaration advances generation before comparing deadlines.
//
// Zero or elapsed input selects a fallback from the current clock. Publication
// installs an absent deadline, replaces a comparable unusable entry, or extends
// a shorter deadline; a longer or equal existing deadline returns unchanged.
// Advancing generation even in that last case transfers declaration ownership
// and invalidates earlier provisional rollback.
func (s *RuntimeBlockState) blockLocked(providerID int64, until time.Time, _ string) (uint64, bool) {
	// Even an equal or shorter declaration represents newer ownership. Advance its
	// generation before comparison so an earlier rollback cannot remove that newly
	// affirmed block.
	generation := s.sequence.Add(1)
	s.generation.Store(providerID, generation)
	now := s.now()
	blockUntil := until
	// A caller can signal a block without a future reset. Give that declaration a
	// finite runtime window while retaining any already longer provider deadline.
	if blockUntil.IsZero() || !blockUntil.After(now) {
		blockUntil = now.Add(RuntimeBlockFallback)
	}

	// Publish against the observed comparable entry, retrying if it changes. The
	// comparison retains a later deadline while the caller's provider mutex also
	// coordinates ordinary mutations.
	for {
		current, loaded := s.until.Load(providerID)
		if !loaded {
			actual, stored := s.until.LoadOrStore(providerID, blockUntil)
			if !stored {
				return generation, true
			}
			current = actual
		}

		currentUntil, ok := current.(time.Time)
		if !ok || currentUntil.IsZero() {
			if s.until.CompareAndSwap(providerID, current, blockUntil) {
				return generation, true
			}
			continue
		}
		if !blockUntil.After(currentUntil) {
			return generation, false
		}
		if s.until.CompareAndSwap(providerID, current, blockUntil) {
			return generation, true
		}
	}
}

// ClearProviderSchedulingBlock removes provider-wide and credential-failure
// cooldowns plus the current retry observation, then advances both recovery
// generations. Nil state and nonpositive IDs are skipped; this operation
// performs an unconditional clear.
func (s *RuntimeBlockState) ClearProviderSchedulingBlock(providerID int64) {
	if s == nil || providerID <= 0 {
		return
	}
	mu := s.lock(providerID)
	mu.Lock()
	defer mu.Unlock()
	s.clearLocked(providerID)
}

// ManagedRecoveryFence returns the represented provider mutation generation, or
// zero for absent state and invalid IDs. Credential-failure publication and
// independent retry timestamps do not advance this counter, so they are outside
// its comparison coverage.
func (s *RuntimeBlockState) ManagedRecoveryFence(providerID int64) uint64 {
	if s == nil || providerID <= 0 {
		return 0
	}
	mu := s.lock(providerID)
	mu.Lock()
	defer mu.Unlock()
	value, _ := s.generation.Load(providerID)
	generation, _ := value.(uint64)
	return generation
}

// ClearProviderSchedulingBlockIfFence reports whether recovery cleared a
// provider whose represented generation still equals expected. Nil state and
// nonpositive IDs return false; zero expected can match an instance with no
// represented mutation.
//
// Comparison and clear share the provider mutex, so a newer provider-wide
// declaration, expiry cleanup, rollback, or clear prevents this recovery from
// succeeding with an older fence. Accepted credential notices alone do not
// advance that counter. A successful clear removes both cooldown scopes and
// advances the failure-clear generation, invalidating previously captured
// publishers.
func (s *RuntimeBlockState) ClearProviderSchedulingBlockIfFence(providerID int64, expected uint64) bool {
	if s == nil || providerID <= 0 {
		return false
	}
	mu := s.lock(providerID)
	mu.Lock()
	defer mu.Unlock()
	value, _ := s.generation.Load(providerID)
	generation, _ := value.(uint64)
	// Keep fence comparison and state clearing inside the same provider lock, so a
	// represented mutation cannot intervene between accepting recovery and
	// invalidating older publishers.
	if generation != expected {
		return false
	}
	s.clearLocked(providerID)
	return true
}

// clearLocked removes the provider's current failure entries, cooldown, and
// retry observation, then publishes new mutation and failure-clear generations.
// Callers hold the provider mutex so provider declarations and credential
// publication wait until that clear finishes.
//
// The failure-clear generation rejects publishers captured before recovery even
// when their persistence completes later. Retry operations use their own
// concurrent map access, so a new observation can begin independently after its
// prior entry is deleted. Persistent health state is managed by the caller
// separately.
func (s *RuntimeBlockState) clearLocked(providerID int64) {
	// Remove current cooldown entries before publishing the new clear generation.
	// Notice callbacks use this provider mutex, so older captured publishers
	// cannot restore those entries afterward.
	s.refreshFailureBlocks.Clear(providerID)
	s.until.Delete(providerID)
	s.retryStarted.Delete(providerID)
	generation := s.sequence.Add(1)
	s.generation.Store(providerID, generation)
	s.refreshFailureClearGeneration.Store(providerID, generation)
}

// Blocked reports whether the current credential identity or provider-wide
// cooldown remains blocked. Nil state returns false. A callable identity is
// required when live refresh-failure entries exist; it runs while this
// provider's mutex is held and must not reenter state methods needing that
// mutex.
//
// Credential lookup precedes the provider deadline. A matching failure returns
// true before provider-wide expiry cleanup; otherwise malformed, zero, or
// elapsed provider deadlines are removed and advance the represented
// generation. Live failure identities retain their independent deadlines, while
// a provider-wide deadline covers the provider regardless of the identity
// supplied.
func (s *RuntimeBlockState) Blocked(providerID int64, identity func() string) bool {
	if s == nil {
		return false
	}
	mu := s.lock(providerID)
	mu.Lock()
	defer mu.Unlock()
	// RefreshFailureBlocks releases its shared internal mutex during identity
	// evaluation, but this outer provider mutex remains held to coordinate lookup
	// with recovery and notice publication.
	if s.refreshFailureBlocks.Blocked(providerID, s.now(), identity) {
		return true
	}
	value, ok := s.until.Load(providerID)
	if !ok {
		return false
	}
	cooldownUntil, ok := value.(time.Time)
	// Unusable provider deadlines cannot keep scheduling blocked. Remove their
	// entry and advance the generation so a recovery observation reflects this
	// cleanup.
	if !ok || cooldownUntil.IsZero() {
		s.until.Delete(providerID)
		s.generation.Store(providerID, s.sequence.Add(1))
		return false
	}
	if s.now().Before(cooldownUntil) {
		return true
	}
	// A deadline reached by the service clock is no longer active. Record its
	// removal as a represented mutation so older recovery fences become stale.
	s.until.Delete(providerID)
	s.generation.Store(providerID, s.sequence.Add(1))
	return false
}

// BlockRollback installs a provisional provider-wide deadline and returns a
// function restoring its previous entry when this installation still owns the
// state. Nil state or an unchanged deadline returns a no-op; even an unchanged
// declaration advances the represented generation through blockLocked.
//
// Rollback requires both the installed generation and deadline to match, so a
// later declaration retaining the same deadline also invalidates it. Successful
// restoration or deletion advances generation and makes repeated rollback
// ineffective. Credential-specific refresh failures and their clear generation
// remain intact, allowing independent failure notices to survive or publish
// after a provisional rollback.
func (s *RuntimeBlockState) BlockRollback(providerID int64, until time.Time, reason string) func() {
	if s == nil {
		return func() {}
	}
	mu := s.lock(providerID)
	mu.Lock()
	// Capture the previous entry and installed ownership under one provider lock.
	// The returned rollback can then compare its installation with state reached
	// by later declarations.
	before, hadBefore := s.until.Load(providerID)
	installedGeneration, changed := s.blockLocked(providerID, until, reason)
	installed, installedOK := s.until.Load(providerID)
	installedUntil, isTime := installed.(time.Time)
	mu.Unlock()
	if !changed || !installedOK || !isTime {
		return func() {}
	}
	if hadBefore {
		if beforeUntil, ok := before.(time.Time); ok && beforeUntil.Equal(installedUntil) {
			return func() {}
		}
	}
	return func() {
		mu.Lock()
		defer mu.Unlock()
		// Require this installation's generation and deadline before restoring
		// earlier state. A later declaration can affirm the same deadline and still
		// invalidate this rollback.
		generation, ok := s.generation.Load(providerID)
		if !ok || generation != installedGeneration {
			return
		}
		current, ok := s.until.Load(providerID)
		currentUntil, isTime := current.(time.Time)
		if !ok || !isTime || !currentUntil.Equal(installedUntil) {
			return
		}
		// Restore or remove only the provider-wide entry, then retire this
		// installation's generation. Credential failures keep their independent
		// entries and previously captured publication remains valid.
		if hadBefore {
			s.until.Store(providerID, before)
			s.generation.Store(providerID, s.sequence.Add(1))
			return
		}
		s.until.Delete(providerID)
		s.generation.Store(providerID, s.sequence.Add(1))
	}
}

// PrepareRefreshFailure captures a provider's failure-clear generation and
// returns a notice publisher for persistence completion. Nil state yields a
// no-op. Installing a failure requires a positive matching provider ID, a
// nonempty identity, and an OpenAI or Grok notice; a clear after capture makes
// the publisher discard the notice.
//
// Capture, validation, and clear use the same provider mutex. Accepted notices
// retain independent credential deadlines, using RuntimeBlockFallback for a
// nonfuture reset. Publication does not advance the represented provider
// mutation generation, so it does not by itself invalidate a
// ManagedRecoveryFence. Callers prepare the publisher before writing failure
// state.
func (s *RuntimeBlockState) PrepareRefreshFailure(id int64) func(RefreshFailureNotice) {
	if s == nil {
		return func(RefreshFailureNotice) {}
	}
	mu := s.lock(id)
	mu.Lock()
	// Capture before the caller persists failure state. Publication later
	// validates this clear generation under the same provider lock used by
	// recovery.
	generation, _ := s.refreshFailureClearGeneration.Load(id)
	mu.Unlock()
	return func(notice RefreshFailureNotice) {
		if notice.ProviderID != id || notice.Identity == "" || (notice.Platform != PlatformOpenAI && notice.Platform != PlatformGrok) {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		// Validate recovery's clear generation before publishing the persisted
		// result. A callback captured before clearing cannot reinstall credential
		// failures after that clear completes.
		current, _ := s.refreshFailureClearGeneration.Load(id)
		if current != generation {
			return
		}
		now := s.now()
		until := notice.Until
		// A notice lacking a future reset still represents refresh failure. Give its
		// credential identity the same finite fallback window used for provider-wide
		// declarations.
		if !until.After(now) {
			until = now.Add(RuntimeBlockFallback)
		}
		s.refreshFailureBlocks.Block(id, notice.Identity, until, now)
	}
}
