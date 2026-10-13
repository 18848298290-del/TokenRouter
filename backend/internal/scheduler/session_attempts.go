package scheduler

import (
	"context"
	"sync"
)

// Provider resources may be released before a request has its final service
// outcome. Session retention therefore follows logical attempt completion, so
// a billable partial result can keep its idle registration even after physical
// slots have already returned.
//
// SessionAttempts indexes the current logical attempts by provider and removes
// or snapshots those entries under its mutex. Outcome callbacks run after
// unlocking, allowing cache cleanup to proceed without holding the map lock.
// Abandon and Reset complete detached entries as unserved; final Finish
// closes registration and applies the selected outcome to the remaining
// entries. Selecting the same provider again replaces its logical binding
// while carrying forward the previous physical cleanup owner.

// SessionAttempts tracks session completion for a request. Resources may be released early,
// while session retention depends on whether execution served a successful or billable partial result.
type SessionAttempts struct {
	// mu protects closed and entries. Map entries are detached while
	// holding it, then AttemptLease callbacks run after unlocking;
	// resources attached concurrently use their own lease ownership
	// synchronization.
	mu          sync.Mutex              // protects attempt registration state
	closed      bool                    // final completion has begun
	entries     map[int64]*AttemptLease // current attempts by provider
	cache       SessionLimitCache       // optional idle session storage
	diagnostics Diagnostics             // session cleanup failure sink
}

// NewSessionAttempts constructs an open provider-indexed collection for
// request session outcomes.
func NewSessionAttempts(cache SessionLimitCache, diagnostics Diagnostics) *SessionAttempts {
	return &SessionAttempts{cache: cache, diagnostics: diagnostics, entries: map[int64]*AttemptLease{}}
}

// Track installs the logical binding for a selected provider, ignoring an
// empty session ID. Call it before Own attaches that provider resource.
//
// A closed collection immediately completes the new attempt as unserved.
// Repeated selection of the same provider replaces its outcome callback and
// adds the previous physical resource owner to the new cleanup list. The prior
// finish callback is not carried forward, so the new binding is the one whose
// idle registration receives the final outcome.
func (s *SessionAttempts) Track(binding SessionBinding) {
	if binding.SessionID == "" {
		return
	}
	attempt := NewAttemptLease(nil, func(outcome AttemptOutcome) {
		FinishSession(context.Background(), s.cache, binding, outcome, s.diagnostics)
	})
	// Install the logical owner under the map lock. Finish can then detach
	// it, while closure already observed by Track sends this new binding
	// directly to unserved cleanup.
	s.mu.Lock()
	if s.closed {
		// Finish has closed registration. Drop the map lock before
		// finishing the late binding because its session callback can
		// access storage and diagnostic sinks synchronously.
		s.mu.Unlock()
		attempt.Release()
		return
	}
	// The same provider was tracked again. Carry its physical cleanup
	// owner into the replacement attempt while letting the newly selected
	// binding own logical session completion.
	if previous := s.entries[binding.ProviderID]; previous != nil {
		attempt.resources.Own(previous.resources.Release)
	}
	s.entries[binding.ProviderID] = attempt
	s.mu.Unlock()
}

// Own attaches release to the provider attempt that was already tracked.
// Callers Track first, because an open collection with no entry leaves this
// callback unattached.
//
// The current attempt and closure state are sampled under the mutex, then
// physical ownership is registered through that attempt lease after unlocking.
// A concurrent Finish can close the physical owner before registration;
// Lease.Own then releases immediately. A closed collection with no entry also
// releases immediately, so late resources do not wait for another finalization
// pass.
func (s *SessionAttempts) Own(id int64, release func()) {
	if release == nil {
		return
	}
	s.mu.Lock()
	attempt, closed := s.entries[id], s.closed
	s.mu.Unlock()
	if attempt != nil {
		attempt.resources.Own(release)
	} else if closed {
		// Final completion has already removed the entry map. Return
		// this late resource immediately so it cannot outlive the
		// request without a registered cleanup owner.
		release()
	}
}

// Abandon removes one current provider attempt and completes it as unserved.
// Missing or already removed entries have no effect.
//
// The entry is detached under the collection mutex before its finish callback
// is invoked. Registration for other providers can proceed while session
// storage cleanup runs, and physical resources return through AttemptLease.
// Callers use this after abandoning that provider; a later selection can Track
// a new binding, whose relationship with any still-running storage cleanup
// remains the execution caller responsibility.
func (s *SessionAttempts) Abandon(id int64) {
	// Detach the provider before invoking its outcome callback. Session
	// removal can access storage after unlocking while other provider
	// registrations continue independently.
	s.mu.Lock()
	attempt := s.entries[id]
	delete(s.entries, id)
	s.mu.Unlock()
	if attempt != nil {
		attempt.Finish(AttemptOutcome{})
	}
}

// Reset detaches all current attempts and completes those entries as unserved
// before group fallback. An open collection then accepts new registrations; a
// collection already closed by Finish stays closed.
//
// Replacing the map under the mutex separates the cleanup snapshot from later
// Track calls. Cache callbacks execute after unlocking, so fallback cleanup
// does not hold the registration lock while accessing storage. Callers
// coordinate group transition and any new registrations so the detached
// attempt outcomes finish at the intended point in request execution.
func (s *SessionAttempts) Reset() {
	s.mu.Lock()
	entries := s.entries
	// Detach the old entries before invoking cleanup. New registrations on
	// an open collection use a fresh map and are not included in this
	// fallback reset.
	s.entries = map[int64]*AttemptLease{}
	s.mu.Unlock()
	for _, attempt := range entries {
		attempt.Finish(AttemptOutcome{})
	}
}

// Finish closes the collection and applies outcome to the remaining logical
// attempts. Repeated calls do nothing, and registrations arriving after
// closure are completed as unserved.
//
// The map is detached under the mutex before any outcome callbacks execute.
// Existing physical resource owners can already have released their slots, but
// that does not change whether the final Served outcome retains idle
// registration. Each AttemptLease fixes its own first outcome, so earlier
// abandon or parent cleanup remains final for that attempt.
func (s *SessionAttempts) Finish(outcome AttemptOutcome) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	// Close registration and detach the remaining entries together. A
	// concurrent Track can then finish its new binding as unserved instead
	// of escaping final cleanup.
	s.closed = true
	entries := s.entries
	s.entries = nil
	s.mu.Unlock()
	for _, attempt := range entries {
		attempt.Finish(outcome)
	}
}
