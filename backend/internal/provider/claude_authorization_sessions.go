package provider

import (
	"sync"
	"time"
)

const ClaudeAuthorizationSessionTTL = 30 * time.Minute

type ClaudeAuthorizationSession struct {
	State        string    `json:"state"`
	CodeVerifier string    `json:"code_verifier"`
	Scope        string    `json:"scope"`
	ProxyURL     string    `json:"proxy_url,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// ClaudeAuthorizationSessions manages OAuth sessions in memory.
type ClaudeAuthorizationSessions struct {
	runtimeMu      sync.Mutex
	runtimeStarted bool
	runtimeStopped bool
	runtimeWG      sync.WaitGroup

	mu       sync.RWMutex
	sessions map[string]*ClaudeAuthorizationSession

	stopCh chan struct{}
}

// NewClaudeAuthorizationSessions creates a session store; Start begins expiry cleanup.
func NewClaudeAuthorizationSessions() *ClaudeAuthorizationSessions {
	store := &ClaudeAuthorizationSessions{
		sessions: make(map[string]*ClaudeAuthorizationSession),
		stopCh:   make(chan struct{}),
	}

	return store
}

// Start starts the cleanup loop once; a stopped store cannot restart.
func (s *ClaudeAuthorizationSessions) Start() {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.runtimeStarted || s.runtimeStopped {
		return
	}
	s.runtimeStarted = true
	s.runtimeWG.Go(func() { ; s.cleanup() })
}

// Stop stops and joins the cleanup loop. It is safe before Start and on repeated calls.
func (s *ClaudeAuthorizationSessions) Stop() {
	s.runtimeMu.Lock()
	if !s.runtimeStopped {
		s.runtimeStopped = true
		close(s.stopCh)
	}
	s.runtimeMu.Unlock()
	s.runtimeWG.Wait()
}

func (s *ClaudeAuthorizationSessions) Set(sessionID string, session *ClaudeAuthorizationSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sessionID] = session
}

// Get returns the stored session pointer if it exists and has not expired.
func (s *ClaudeAuthorizationSessions) Get(sessionID string) (*ClaudeAuthorizationSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return nil, false
	}
	if time.Since(session.CreatedAt) > ClaudeAuthorizationSessionTTL {
		return nil, false
	}
	return session, true
}

// Delete removes a session.
func (s *ClaudeAuthorizationSessions) Delete(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionID)
}

// cleanup removes expired sessions periodically.
func (s *ClaudeAuthorizationSessions) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.mu.Lock()
			for id, session := range s.sessions {
				if time.Since(session.CreatedAt) > ClaudeAuthorizationSessionTTL {
					delete(s.sessions, id)
				}
			}
			s.mu.Unlock()
		}
	}
}
