package provider

import (
	"sync"
	"time"
)

const OpenAISessionTTL = 30 * time.Minute

// OpenAIOAuthSession stores OAuth flow state for OpenAI.
type OpenAIOAuthSession struct {
	State        string    `json:"state"`
	CodeVerifier string    `json:"code_verifier"`
	ClientID     string    `json:"client_id,omitempty"`
	ProxyURL     string    `json:"proxy_url,omitempty"`
	RedirectURI  string    `json:"redirect_uri"`
	CreatedAt    time.Time `json:"created_at"`
}

// OpenAISessionStore manages OAuth sessions in memory.
type OpenAISessionStore struct {
	runtimeMu      sync.Mutex
	runtimeStarted bool
	runtimeStopped bool
	runtimeWG      sync.WaitGroup

	mu       sync.RWMutex
	sessions map[string]*OpenAIOAuthSession

	stopCh chan struct{}
}

// NewOpenAISessionStore creates a session store; Start launches periodic cleanup.
func NewOpenAISessionStore() *OpenAISessionStore {
	store := &OpenAISessionStore{
		sessions: make(map[string]*OpenAIOAuthSession),
		stopCh:   make(chan struct{}),
	}

	return store
}

// Set stores the session pointer; callers must treat the session as immutable afterward.
func (s *OpenAISessionStore) Set(sessionID string, session *OpenAIOAuthSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sessionID] = session
}

// Get returns the stored session pointer when unexpired. Callers must not mutate it.
func (s *OpenAISessionStore) Get(sessionID string) (*OpenAIOAuthSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return nil, false
	}
	// Enforce expiry even before periodic cleanup removes the entry.
	if time.Since(session.CreatedAt) > OpenAISessionTTL {
		return nil, false
	}
	return session, true
}

// Delete removes a session.
func (s *OpenAISessionStore) Delete(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionID)
}

// Start launches periodic cleanup once, unless the store has stopped.
func (s *OpenAISessionStore) Start() {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.runtimeStarted || s.runtimeStopped {
		return
	}
	s.runtimeStarted = true
	s.runtimeWG.Go(func() { ; s.cleanup() })
}

// Stop stops and waits for cleanup. Repeated calls and stopping before Start are safe.
func (s *OpenAISessionStore) Stop() {
	s.runtimeMu.Lock()
	if !s.runtimeStopped {
		s.runtimeStopped = true
		close(s.stopCh)
	}
	s.runtimeMu.Unlock()
	s.runtimeWG.Wait()
}

func (s *OpenAISessionStore) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.mu.Lock()
			for id, session := range s.sessions {
				if time.Since(session.CreatedAt) > OpenAISessionTTL {
					delete(s.sessions, id)
				}
			}
			s.mu.Unlock()
		}
	}
}
