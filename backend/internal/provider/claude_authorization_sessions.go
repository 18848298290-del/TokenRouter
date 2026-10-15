package provider

import (
	"sync"
	"time"
)

// claude_authorization_sessions.go stores Claude authorization scope and PKCE
// proof and coordinates the lifetime of expiry cleanup.
// Claude authorization saves scope beside state and PKCE proof so exchange can
// distinguish full OAuth from inference-only setup tokens. Lookup checks the
// thirty-minute session age before returning that saved authentication input.
//
// Construction initializes storage and the stop signal but starts no worker.
// Start registers one cleanup goroutine while the runtime mutex protects its
// started and stopped flags; Stop closes the signal once and waits after
// unlocking. Stopping before startup closes further cleanup admission. The
// separate map lock protects entry lookup and replacement, while session
// pointers remain borrowed and their fields stay stable during use. Get
// rejects expired entries without deleting them; Delete and five-minute
// cleanup remove map entries. Set/Get remain storage operations after cleanup
// stops.

// ClaudeAuthorizationSessionTTL bounds authorization proof reuse while Get
// checks age and periodic cleanup removes expired entries.
const ClaudeAuthorizationSessionTTL = 30 * time.Minute

// ClaudeAuthorizationSession retains temporary OAuth proof and the selected
// authorization request context.
type ClaudeAuthorizationSession struct {
	State        string `json:"state"`               // generated OAuth state proof
	CodeVerifier string `json:"code_verifier"`       // stored PKCE verifier text
	Scope        string `json:"scope"`               // selected OAuth scope text
	ProxyURL     string `json:"proxy_url,omitempty"` // selected authorization proxy URL
	// CreatedAt anchors lookup expiry and periodic removal. Keep it unchanged
	// while the retained session is stored and read by the cleanup worker.
	CreatedAt time.Time `json:"created_at"`
}

// ClaudeAuthorizationSessions coordinates borrowed authorization sessions and
// one explicitly started cleanup worker.
type ClaudeAuthorizationSessions struct {
	// runtimeMu protects runtimeStarted and runtimeStopped while runtimeWG tracks
	// the admitted cleanup worker. Start registers before releasing this lock;
	// Stop closes stopCh once and waits after unlocking.
	runtimeMu      sync.Mutex     // cleanup lifecycle admission lock
	runtimeStarted bool           // cleanup worker was admitted
	runtimeStopped bool           // cleanup admission is closed
	runtimeWG      sync.WaitGroup // admitted cleanup worker completion

	// mu protects map access, while sessions retains nonnil caller pointers. Get
	// returns those same pointers, so callers keep session fields stable during
	// lookup and cleanup.
	mu       sync.RWMutex                           // authorization session entry lock
	sessions map[string]*ClaudeAuthorizationSession // sessions indexed by opaque identifier

	// stopCh is initialized at construction and closed once under runtimeMu.
	// Cleanup observes its closure before that worker can finish.
	stopCh chan struct{}
}

// NewClaudeAuthorizationSessions creates initialized empty session storage and
// an unopened stop signal. Construction starts no cleanup; callers invoke
// Start when periodic expiry removal is wanted and Stop before releasing the
// store.
func NewClaudeAuthorizationSessions() *ClaudeAuthorizationSessions {
	store := &ClaudeAuthorizationSessions{
		sessions: make(map[string]*ClaudeAuthorizationSession),
		stopCh:   make(chan struct{}),
	}

	return store
}

// Start registers the cleanup goroutine once for an initialized store.
// Repeated starts and every start after Stop return immediately, with no new
// worker.
//
// The runtime mutex keeps flag inspection, worker admission, and the started
// flag together, so Stop cannot finish its wait before an accepted worker has
// been registered. The worker uses the separate session map lock for removal.
// Callers create the store through its constructor and later call Stop to join
// the admitted cleanup work.
func (s *ClaudeAuthorizationSessions) Start() {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.runtimeStarted || s.runtimeStopped {
		return
	}
	// Register this worker while lifecycle admission is locked, so a concurrent
	// Stop waits for the same accepted cleanup before reporting completion.
	s.runtimeStarted = true
	s.runtimeWG.Go(func() { ; s.cleanup() })
}

// Stop closes cleanup admission and waits for its registered worker to exit.
// An initialized store can stop before Start, and repeated or concurrent stops
// share the same closed stop signal.
//
// Closing the signal and setting runtimeStopped occur under the runtime mutex.
// The mutex is released before waiting, so another lifecycle caller can
// inspect the closed state while the worker finishes. Session storage remains
// available through its separate lock after cleanup ends; callers arrange
// their own lifetime for borrowed session pointers.
func (s *ClaudeAuthorizationSessions) Stop() {
	s.runtimeMu.Lock()
	// Publish the stopped flag and close its signal together before waiting.
	// Concurrent stops then avoid a second close, and future starts cannot add
	// work.
	if !s.runtimeStopped {
		s.runtimeStopped = true
		close(s.stopCh)
	}
	s.runtimeMu.Unlock()
	s.runtimeWG.Wait()
}

// Set retains a nonnil session pointer under sessionID, replacing the previous
// entry. Callers keep its fields stable while Get or cleanup reads it; the map
// lock does not protect pointed-to session mutation.
func (s *ClaudeAuthorizationSessions) Set(sessionID string, session *ClaudeAuthorizationSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sessionID] = session
}

// Get returns the stored session pointer when its age is no greater than the
// configured TTL. Missing and older entries return false; expired entries
// remain stored until Delete or cleanup, and callers treat returned fields as
// immutable.
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

// Delete removes the selected authorization session under the map mutation
// lock. Missing IDs have no effect, and callers use this after a successful
// exchange to consume the stored proof.
func (s *ClaudeAuthorizationSessions) Delete(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionID)
}

// cleanup removes sessions older than the TTL on five-minute ticks until Stop
// closes its signal. It runs as the registered worker and releases its ticker
// when returning.
//
// Entry-age checks and deletions share the map lock, so replacement cannot
// interleave with examination of that entry. Stop and a tick can become ready
// together; a selected cleanup tick completes its locked sweep before the next
// stop observation. Get checks expiry separately, so periodic removal delay
// does not extend the lookup lifetime.
func (s *ClaudeAuthorizationSessions) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			// Compare ages and delete under the entry lock. A newly
			// replaced session cannot be removed using the expired
			// entry examined before that replacement.
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
