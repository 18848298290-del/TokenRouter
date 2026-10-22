package provider

import "time"

// claude_authorization_sessions.go retains Claude authorization proof and scope
// for later code exchange. The platform store uses authorizationSessionStore
// for pointer retention and expiry, while the saved scope distinguishes full
// OAuth authorization from inference-only setup tokens.

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

// ClaudeAuthorizationSessions retains typed authorization proof with a
// stoppable cleanup worker.
type ClaudeAuthorizationSessions struct {
	// authorizationSessionStore owns locking, expiration, and cleanup for these
	// sessions. Construct the enclosing platform store before invoking its methods.
	authorizationSessionStore[ClaudeAuthorizationSession]
}

// NewClaudeAuthorizationSessions creates empty authorization storage with
// cleanup idle.
// Call Start to begin periodic removal and Stop to join that worker; lookup
// checks creation-time expiry even before Start.
func NewClaudeAuthorizationSessions() *ClaudeAuthorizationSessions {
	return &ClaudeAuthorizationSessions{
		authorizationSessionStore: newAuthorizationSessionStore(
			ClaudeAuthorizationSessionTTL,
			func(session *ClaudeAuthorizationSession) time.Time { return session.CreatedAt },
		),
	}
}

// Set retains session under sessionID, replacing any previous pointer for that
// identifier. Supply a nonnil session and keep its fields stable while stored
// or borrowed by a reader, including after Stop.
func (s *ClaudeAuthorizationSessions) Set(sessionID string, session *ClaudeAuthorizationSession) {
	s.set(sessionID, session)
}

// Get returns the stored pointer when its creation age is at most
// ClaudeAuthorizationSessionTTL.
// Missing or older entries return nil and false; expired entries remain stored
// until deletion or cleanup, and returned fields remain shared.
func (s *ClaudeAuthorizationSessions) Get(sessionID string) (*ClaudeAuthorizationSession, bool) {
	return s.get(sessionID)
}

// Delete removes sessionID from subsequent lookups and accepts an absent ID.
// Borrowers retain previously returned pointers, so callers coordinate
// exclusive
// exchange separately from the store's Get and Delete operations.
func (s *ClaudeAuthorizationSessions) Delete(sessionID string) {
	s.delete(sessionID)
}

// Start admits periodic expiry cleanup once for a constructed store.
// Repeated calls leave the running worker unchanged, and calls after Stop
// return with cleanup permanently disabled for this instance.
func (s *ClaudeAuthorizationSessions) Start() {
	s.start()
}

// Stop closes cleanup admission and waits for any admitted worker to exit.
// Repeated calls and calls before Start are supported; Set, Get, and Delete
// remain available after cleanup stops.
func (s *ClaudeAuthorizationSessions) Stop() {
	s.stop()
}
