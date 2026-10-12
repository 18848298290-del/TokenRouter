package scheduler

import (
	"context"
	"sync"
)

// SessionAttempts tracks session completion for a request. Resources may be released early,
// while session retention depends on whether execution served a successful or billable partial result.
type SessionAttempts struct {
	mu          sync.Mutex
	closed      bool
	entries     map[int64]*AttemptLease
	cache       SessionLimitCache
	diagnostics Diagnostics
}

func NewSessionAttempts(cache SessionLimitCache, diagnostics Diagnostics) *SessionAttempts {
	return &SessionAttempts{cache: cache, diagnostics: diagnostics, entries: map[int64]*AttemptLease{}}
}

// Track records a session binding, replacing it when the same provider is selected again.
func (s *SessionAttempts) Track(binding SessionBinding) {
	if binding.SessionID == "" {
		return
	}
	attempt := NewAttemptLease(nil, func(outcome AttemptOutcome) {
		FinishSession(context.Background(), s.cache, binding, outcome, s.diagnostics)
	})
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		attempt.Release()
		return
	}
	if previous := s.entries[binding.ProviderID]; previous != nil {
		attempt.resources.Own(previous.resources.Release)
	}
	s.entries[binding.ProviderID] = attempt
	s.mu.Unlock()
}

// Own attaches a resource release to an attempt, shared by early release and final completion.
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
		release()
	}
}

// Abandon unregisters the previous provider's session when switching providers.
func (s *SessionAttempts) Abandon(id int64) {
	s.mu.Lock()
	attempt := s.entries[id]
	delete(s.entries, id)
	s.mu.Unlock()
	if attempt != nil {
		attempt.Finish(AttemptOutcome{})
	}
}

// Reset finishes the current attempts before group fallback and permits new registrations.
func (s *SessionAttempts) Reset() {
	s.mu.Lock()
	entries := s.entries
	s.entries = map[int64]*AttemptLease{}
	s.mu.Unlock()
	for _, attempt := range entries {
		attempt.Finish(AttemptOutcome{})
	}
}

// Finish completes all attempts with the final outcome; subsequent calls have no effect.
func (s *SessionAttempts) Finish(outcome AttemptOutcome) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	entries := s.entries
	s.entries = nil
	s.mu.Unlock()
	for _, attempt := range entries {
		attempt.Finish(outcome)
	}
}
