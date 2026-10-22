package provider

import "time"

// openai_sessions.go retains OpenAI PKCE proof and the parameters selected for
// authorization code exchange. Its typed store delegates expiry and cleanup to
// authorizationSessionStore, and the session records client, proxy, and
// redirect
// choices consumed by OpenAIAuthorization.

// OpenAISessionTTL limits PKCE session age for both lookups and periodic
// eviction
// from the in-memory authorization store.
const OpenAISessionTTL = 30 * time.Minute

// OpenAIOAuthSession retains immutable PKCE proof and exchange parameters for
// one authorization session.
type OpenAIOAuthSession struct {
	State        string `json:"state"`         // generated authorization state proof
	CodeVerifier string `json:"code_verifier"` // stored PKCE verifier secret
	// ClientID stores the authorization client's identity. Exchange trims it and
	// uses the configured default when this session value is empty after trimming.
	ClientID string `json:"client_id,omitempty"`
	// ProxyURL records the resolved authorization proxy. A found input proxy
	// overrides it during exchange; an absent lookup retains this stored URL.
	ProxyURL string `json:"proxy_url,omitempty"`
	// RedirectURI records the authorization redirect selected at URL generation.
	// Exchange retains it unless its input supplies a nonempty override value.
	RedirectURI string `json:"redirect_uri"`
	// CreatedAt starts the entry's TTL calculation in Get and cleanup. Callers
	// initialize it before publishing the immutable session pointer through Set.
	CreatedAt time.Time `json:"created_at"`
}

// OpenAISessionStore retains typed authorization proof with a stoppable cleanup
// worker.
type OpenAISessionStore struct {
	// authorizationSessionStore owns locking, expiration, and cleanup for these
	// sessions. Construct the enclosing platform store before invoking its methods.
	authorizationSessionStore[OpenAIOAuthSession]
}

// NewOpenAISessionStore creates empty authorization storage with cleanup idle.
// Call Start to begin periodic removal and Stop to join that worker; lookup
// checks creation-time expiry even before Start.
func NewOpenAISessionStore() *OpenAISessionStore {
	return &OpenAISessionStore{
		authorizationSessionStore: newAuthorizationSessionStore(
			OpenAISessionTTL,
			func(session *OpenAIOAuthSession) time.Time { return session.CreatedAt },
		),
	}
}

// Set retains session under sessionID, replacing any previous pointer for that
// identifier. Supply a nonnil session and keep its fields stable while stored
// or borrowed by a reader, including after Stop.
func (s *OpenAISessionStore) Set(sessionID string, session *OpenAIOAuthSession) {
	s.set(sessionID, session)
}

// Get returns the stored pointer when its creation age is at most
// OpenAISessionTTL.
// Missing or older entries return nil and false; expired entries remain stored
// until deletion or cleanup, and returned fields remain shared.
func (s *OpenAISessionStore) Get(sessionID string) (*OpenAIOAuthSession, bool) {
	return s.get(sessionID)
}

// Delete removes sessionID from subsequent lookups and accepts an absent ID.
// Borrowers retain previously returned pointers, so callers coordinate
// exclusive
// exchange separately from the store's Get and Delete operations.
func (s *OpenAISessionStore) Delete(sessionID string) {
	s.delete(sessionID)
}

// Start admits periodic expiry cleanup once for a constructed store.
// Repeated calls leave the running worker unchanged, and calls after Stop
// return with cleanup permanently disabled for this instance.
func (s *OpenAISessionStore) Start() {
	s.start()
}

// Stop closes cleanup admission and waits for any admitted worker to exit.
// Repeated calls and calls before Start are supported; Set, Get, and Delete
// remain available after cleanup stops.
func (s *OpenAISessionStore) Stop() {
	s.stop()
}
