package provider

import (
	"sync"
	"sync/atomic"
	"time"
)

// RuntimeBlockFallback and RuntimeRetryWindow bound in-memory blocks and same-provider retry windows.
const (
	RuntimeBlockFallback = 2 * time.Minute
	RuntimeRetryWindow   = 2 * time.Minute
)

// RuntimeBlockState owns scheduling blocks, refresh-failure blocks, and recovery generations shared by callers.
type RuntimeBlockState struct {
	until                         sync.Map
	locks                         sync.Map
	generation                    sync.Map
	sequence                      atomic.Uint64
	retryStarted                  sync.Map
	refreshFailureBlocks          RefreshFailureBlocks
	refreshFailureClearGeneration sync.Map
	now                           func() time.Time
}

// NewRuntimeBlockState creates runtime state with the supplied clock, defaulting to time.Now.
func NewRuntimeBlockState(now func() time.Time) *RuntimeBlockState {
	if now == nil {
		now = time.Now
	}
	return &RuntimeBlockState{now: now}
}

// Block extends existing deadlines; every declaration advances the generation to protect rollback ownership.
func (s *RuntimeBlockState) Block(id int64, until time.Time, reason string) {
	if s == nil {
		return
	}
	mu := s.lock(id)
	mu.Lock()
	defer mu.Unlock()
	_, _ = s.blockLocked(id, until, reason)
}

// BlockProviderScheduling installs an in-memory block for OpenAI and Grok providers.
func (s *RuntimeBlockState) BlockProviderScheduling(value *Record, until time.Time, reason string) {
	if value == nil || (value.Platform != PlatformOpenAI && value.Platform != PlatformGrok) {
		return
	}
	s.Block(value.ID, until, reason)
}

// ResetRetry clears the same-provider recovery window after success.
func (s *RuntimeBlockState) ResetRetry(id int64) { s.retryStarted.Delete(id) }

// RetryWindowActive starts a window on first observation and reports whether it remains open.
func (s *RuntimeBlockState) RetryWindowActive(providerID int64) bool {
	if s == nil {
		return false
	}
	now := s.now()
	value, _ := s.retryStarted.LoadOrStore(providerID, now)
	startedAt, ok := value.(time.Time)
	if !ok {
		s.retryStarted.Store(providerID, now)
		startedAt = now
	}
	return now.Before(startedAt.Add(RuntimeRetryWindow))
}

// RetryDeadline returns the existing retry deadline, or zero when no window exists.
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

func (s *RuntimeBlockState) lock(providerID int64) *sync.Mutex {
	actual, _ := s.locks.LoadOrStore(providerID, &sync.Mutex{})
	mu, ok := actual.(*sync.Mutex)
	if !ok {
		mu = &sync.Mutex{}
		s.locks.Store(providerID, mu)
	}
	return mu
}

func (s *RuntimeBlockState) blockLocked(providerID int64, until time.Time, _ string) (uint64, bool) {
	generation := s.sequence.Add(1)
	s.generation.Store(providerID, generation)
	now := s.now()
	blockUntil := until
	if blockUntil.IsZero() || !blockUntil.After(now) {
		blockUntil = now.Add(RuntimeBlockFallback)
	}

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

// ClearProviderSchedulingBlock clears blocks and retry state and advances the clear generation.
func (s *RuntimeBlockState) ClearProviderSchedulingBlock(providerID int64) {
	if s == nil || providerID <= 0 {
		return
	}
	mu := s.lock(providerID)
	mu.Lock()
	defer mu.Unlock()
	s.clearLocked(providerID)
}

// ManagedRecoveryFence returns the generation observed by administrative recovery.
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

// ClearProviderSchedulingBlockIfFence clears state when its generation still matches the recovery observation.
func (s *RuntimeBlockState) ClearProviderSchedulingBlockIfFence(providerID int64, expected uint64) bool {
	if s == nil || providerID <= 0 {
		return false
	}
	mu := s.lock(providerID)
	mu.Lock()
	defer mu.Unlock()
	value, _ := s.generation.Load(providerID)
	generation, _ := value.(uint64)
	if generation != expected {
		return false
	}
	s.clearLocked(providerID)
	return true
}

func (s *RuntimeBlockState) clearLocked(providerID int64) {
	s.refreshFailureBlocks.Clear(providerID)
	s.until.Delete(providerID)
	s.retryStarted.Delete(providerID)
	generation := s.sequence.Add(1)
	s.generation.Store(providerID, generation)
	s.refreshFailureClearGeneration.Store(providerID, generation)
}

// Blocked checks credential-scoped refresh failures and unexpired provider-wide cooldowns.
func (s *RuntimeBlockState) Blocked(providerID int64, identity func() string) bool {
	if s == nil {
		return false
	}
	mu := s.lock(providerID)
	mu.Lock()
	defer mu.Unlock()
	if s.refreshFailureBlocks.Blocked(providerID, s.now(), identity) {
		return true
	}
	value, ok := s.until.Load(providerID)
	if !ok {
		return false
	}
	cooldownUntil, ok := value.(time.Time)
	if !ok || cooldownUntil.IsZero() {
		s.until.Delete(providerID)
		s.generation.Store(providerID, s.sequence.Add(1))
		return false
	}
	if s.now().Before(cooldownUntil) {
		return true
	}
	s.until.Delete(providerID)
	s.generation.Store(providerID, s.sequence.Add(1))
	return false
}

// BlockRollback installs a provisional block and returns a rollback guarded by the installed generation.
func (s *RuntimeBlockState) BlockRollback(providerID int64, until time.Time, reason string) func() {
	if s == nil {
		return func() {}
	}
	mu := s.lock(providerID)
	mu.Lock()
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
		generation, ok := s.generation.Load(providerID)
		if !ok || generation != installedGeneration {
			return
		}
		current, ok := s.until.Load(providerID)
		currentUntil, isTime := current.(time.Time)
		if !ok || !isTime || !currentUntil.Equal(installedUntil) {
			return
		}
		if hadBefore {
			s.until.Store(providerID, before)
			s.generation.Store(providerID, s.sequence.Add(1))
			return
		}
		s.until.Delete(providerID)
		s.generation.Store(providerID, s.sequence.Add(1))
	}
}

// PrepareRefreshFailure captures the clear generation before persistence so late notices cannot restore cleared state.
func (s *RuntimeBlockState) PrepareRefreshFailure(id int64) func(RefreshFailureNotice) {
	if s == nil {
		return func(RefreshFailureNotice) {}
	}
	mu := s.lock(id)
	mu.Lock()
	generation, _ := s.refreshFailureClearGeneration.Load(id)
	mu.Unlock()
	return func(notice RefreshFailureNotice) {
		if notice.ProviderID != id || notice.Identity == "" || (notice.Platform != PlatformOpenAI && notice.Platform != PlatformGrok) {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		current, _ := s.refreshFailureClearGeneration.Load(id)
		if current != generation {
			return
		}
		now := s.now()
		until := notice.Until
		if !until.After(now) {
			until = now.Add(RuntimeBlockFallback)
		}
		s.refreshFailureBlocks.Block(id, notice.Identity, until, now)
	}
}
