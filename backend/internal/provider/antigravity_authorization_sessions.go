package provider

import (
	"sync"
	"time"
)

const AntigravitySessionTTL = 30 * time.Minute

// AntigravityAuthorizationSession stores temporary OAuth state and its PKCE verifier.
type AntigravityAuthorizationSession struct {
	State        string    `json:"state"`
	CodeVerifier string    `json:"code_verifier"`
	ProxyURL     string    `json:"proxy_url,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// AntigravityAuthorizationSessions stores OAuth sessions with an expiring cleanup loop.
type AntigravityAuthorizationSessions struct {
	runtimeMu      sync.Mutex
	runtimeStarted bool
	runtimeStopped bool
	runtimeWG      sync.WaitGroup

	mu       sync.RWMutex
	sessions map[string]*AntigravityAuthorizationSession
	stopCh   chan struct{}
}

func NewAntigravityAuthorizationSessions() *AntigravityAuthorizationSessions {
	store := &AntigravityAuthorizationSessions{
		sessions: make(map[string]*AntigravityAuthorizationSession),
		stopCh:   make(chan struct{}),
	}

	return store
}

func (s *AntigravityAuthorizationSessions) Set(sessionID string, session *AntigravityAuthorizationSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sessionID] = session
}

func (s *AntigravityAuthorizationSessions) Get(sessionID string) (*AntigravityAuthorizationSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return nil, false
	}
	if time.Since(session.CreatedAt) > AntigravitySessionTTL {
		return nil, false
	}
	return session, true
}

func (s *AntigravityAuthorizationSessions) Delete(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionID)
}

// Start starts the cleanup loop once; a stopped store cannot restart.
func (s *AntigravityAuthorizationSessions) Start() {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.runtimeStarted || s.runtimeStopped {
		return
	}
	s.runtimeStarted = true
	s.runtimeWG.Go(func() { ; s.cleanup() })
}

// Stop stops and joins the cleanup loop. It is safe before Start and on repeated calls.
func (s *AntigravityAuthorizationSessions) Stop() {
	s.runtimeMu.Lock()
	if !s.runtimeStopped {
		s.runtimeStopped = true
		close(s.stopCh)
	}
	s.runtimeMu.Unlock()
	s.runtimeWG.Wait()
}

func (s *AntigravityAuthorizationSessions) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.mu.Lock()
			for id, session := range s.sessions {
				if time.Since(session.CreatedAt) > AntigravitySessionTTL {
					delete(s.sessions, id)
				}
			}
			s.mu.Unlock()
		}
	}
}
