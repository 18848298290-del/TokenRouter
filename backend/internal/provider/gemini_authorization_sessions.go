package provider

import "time"

// gemini_authorization_sessions.go retains Gemini authorization proof alongside
// project, tier, OAuth variant, and redirect choices used during code exchange.
// The platform store delegates pointer retention and periodic expiry cleanup to
// authorizationSessionStore so those saved choices share the session lifetime.

// GeminiAuthorizationSessionTTL bounds authorization proof reuse while Get
// checks age and periodic cleanup removes expired entries.
const GeminiAuthorizationSessionTTL = 30 * time.Minute

// GeminiOAuthSession retains temporary OAuth proof and the selected
// authorization request context.
type GeminiOAuthSession struct {
	State        string `json:"state"`                // generated OAuth state proof
	CodeVerifier string `json:"code_verifier"`        // stored PKCE verifier text
	ProxyURL     string `json:"proxy_url,omitempty"`  // selected authorization proxy URL
	RedirectURI  string `json:"redirect_uri"`         // authorization callback URI text
	ProjectID    string `json:"project_id,omitempty"` // selected OAuth project identity
	// TierID records the selected discovery fallback. Google One and Code Assist
	// exchange prefer a detected tier and use this value when detection fails.
	TierID    string `json:"tier_id,omitempty"`
	OAuthType string `json:"oauth_type"` // code_assist, google_one, or ai_studio
	// CreatedAt anchors lookup expiry and periodic removal. Keep it unchanged
	// while the retained session is stored and read by the cleanup worker.
	CreatedAt time.Time `json:"created_at"`
}

// GeminiAuthorizationSessions retains typed authorization proof with a
// stoppable cleanup worker.
type GeminiAuthorizationSessions struct {
	// authorizationSessionStore owns locking, expiration, and cleanup for these
	// sessions. Construct the enclosing platform store before invoking its methods.
	authorizationSessionStore[GeminiOAuthSession]
}

// NewGeminiAuthorizationSessions creates empty authorization storage with
// cleanup idle.
// Call Start to begin periodic removal and Stop to join that worker; lookup
// checks creation-time expiry even before Start.
func NewGeminiAuthorizationSessions() *GeminiAuthorizationSessions {
	return &GeminiAuthorizationSessions{
		authorizationSessionStore: newAuthorizationSessionStore(
			GeminiAuthorizationSessionTTL,
			func(session *GeminiOAuthSession) time.Time { return session.CreatedAt },
		),
	}
}

// Set retains session under sessionID, replacing any previous pointer for that
// identifier. Supply a nonnil session and keep its fields stable while stored
// or borrowed by a reader, including after Stop.
func (s *GeminiAuthorizationSessions) Set(sessionID string, session *GeminiOAuthSession) {
	s.set(sessionID, session)
}

// Get returns the stored pointer when its creation age is at most
// GeminiAuthorizationSessionTTL.
// Missing or older entries return nil and false; expired entries remain stored
// until deletion or cleanup, and returned fields remain shared.
func (s *GeminiAuthorizationSessions) Get(sessionID string) (*GeminiOAuthSession, bool) {
	return s.get(sessionID)
}

// Delete removes sessionID from subsequent lookups and accepts an absent ID.
// Borrowers retain previously returned pointers, so callers coordinate
// exclusive
// exchange separately from the store's Get and Delete operations.
func (s *GeminiAuthorizationSessions) Delete(sessionID string) {
	s.delete(sessionID)
}

// Start admits periodic expiry cleanup once for a constructed store.
// Repeated calls leave the running worker unchanged, and calls after Stop
// return with cleanup permanently disabled for this instance.
func (s *GeminiAuthorizationSessions) Start() {
	s.start()
}

// Stop closes cleanup admission and waits for any admitted worker to exit.
// Repeated calls and calls before Start are supported; Set, Get, and Delete
// remain available after cleanup stops.
func (s *GeminiAuthorizationSessions) Stop() {
	s.stop()
}
