package provider

import (
	"sync"
	"time"
)

// An OpenAI authorization URL stores state, PKCE verifier and exchange
// parameters under a generated session ID for later code exchange. Get rejects
// entries older than the session TTL even when cleanup has not run, so
// retained expired entries cannot start that exchange path.
//
// The store protects map access while returning the stored session pointer
// directly; callers treat each published session as immutable. Cleanup sweeps
// expired entries periodically. Start registers one worker while holding the
// runtime mutex, and Stop closes its channel through that same mutex before
// joining the worker. A stopped store cannot restart cleanup, but its map
// remains available to Set, Get and Delete. Lookup and deletion are separate
// operations rather than an atomic session claim.

// OpenAISessionTTL limits PKCE session age for both lookups and periodic eviction
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

// OpenAISessionStore stores borrowed authorization-session pointers and
// coordinates one periodic eviction worker.
type OpenAISessionStore struct {
	// runtimeMu protects runtimeStarted and runtimeStopped while registering work
	// with runtimeWG or closing stopCh. Stop waits after releasing this mutex.
	runtimeMu      sync.Mutex     // startup and stop state guard
	runtimeStarted bool           // cleanup worker registration state
	runtimeStopped bool           // permanent cleanup stop state
	runtimeWG      sync.WaitGroup // registered cleanup worker lifetime

	// mu protects sessions membership and iteration. Each stored pointer remains
	// shared after Get returns, so owners keep the pointed session fields immutable.
	mu       sync.RWMutex                   // session map access guard
	sessions map[string]*OpenAIOAuthSession // published authorization session pointers

	// stopCh is closed once under runtimeMu to request worker termination. It
	// remains closed after Stop, preventing that initialized store from restarting cleanup.
	stopCh chan struct{}
}

// NewOpenAISessionStore creates an empty store with a fresh stop channel and
// an idle cleanup worker. Call Start to launch cleanup; Get enforces entry age
// even while cleanup remains idle.
func NewOpenAISessionStore() *OpenAISessionStore {
	store := &OpenAISessionStore{
		sessions: make(map[string]*OpenAIOAuthSession),
		stopCh:   make(chan struct{}),
	}

	return store
}

// Set stores session by reference under sessionID, replacing any earlier entry
// for that ID. Supply an initialized store and nonnil session with its
// creation time set; callers treat published session fields as immutable,
// including after Stop.
func (s *OpenAISessionStore) Set(sessionID string, session *OpenAIOAuthSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Replacing membership changes future reads while earlier borrowers retain
	// their session pointers; published objects stay immutable across this map mutation.
	s.sessions[sessionID] = session
}

// Get returns the stored session pointer when its age is no greater than
// OpenAISessionTTL, or nil and false for absent or older entries. The returned
// pointer remains borrowed and immutable; reading does not consume the entry
// or remove expired storage.
func (s *OpenAISessionStore) Get(sessionID string) (*OpenAIOAuthSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return nil, false
	}
	// Lookup enforces session age between eviction sweeps so physical retention
	// of an expired PKCE entry cannot make a later exchange start successfully.
	if time.Since(session.CreatedAt) > OpenAISessionTTL {
		return nil, false
	}
	return session, true
}

// Delete removes sessionID from future lookups and accepts an absent ID.
// Previously borrowed session pointers remain available to their holders, so
// callers requiring exclusive exchange coordinate that claim separately from
// Get and Delete.
func (s *OpenAISessionStore) Delete(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Removing membership leaves already borrowed pointers intact; this map operation
	// cannot revoke an exchange that obtained its immutable session through an earlier Get.
	delete(s.sessions, sessionID)
}

// Start launches the cleanup worker once unless this initialized store has
// already stopped. Repeated calls return with the existing worker unchanged,
// and a stopped store retains its permanent stop state.
//
// The runtime mutex protects the started and stopped flags and remains held
// while the worker is registered with runtimeWG. That registration precedes
// any Stop call's wait, preventing shutdown from overlooking a newly launched
// worker. Session lookups enforce expiry independently of whether cleanup is
// running.
func (s *OpenAISessionStore) Start() {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.runtimeStarted || s.runtimeStopped {
		return
	}
	// Register the worker while startup still holds runtimeMu, so a racing Stop
	// observes its lifetime before waiting and cannot miss this accepted cleanup launch.
	s.runtimeStarted = true
	s.runtimeWG.Go(func() { ; s.cleanup() })
}

// Stop permanently prevents cleanup startup, closes the stop signal once and
// waits for the registered worker to finish. Calling Stop before Start or from
// concurrent callers follows the same runtime-state guard.
//
// Channel closure and startup decisions share runtimeMu, while waiting occurs
// after releasing it so competing Stop calls can join the same worker. Cleanup
// owns its ticker and can finish a selected sweep before observing the closed
// signal. Stored entries remain available through the map methods; Stop
// supplies no context deadline or map clearing.
func (s *OpenAISessionStore) Stop() {
	s.runtimeMu.Lock()
	// Share the permanent stop transition with startup and channel closure;
	// concurrent stoppers join one worker while exactly one caller closes the signal.
	if !s.runtimeStopped {
		s.runtimeStopped = true
		close(s.stopCh)
	}
	s.runtimeMu.Unlock()
	s.runtimeWG.Wait()
}

// cleanup evicts entries older than OpenAISessionTTL until the store's stop
// channel is observed. Start is its worker owner, and Stop joins its return
// after closing that channel.
//
// A selected tick locks the session map for the complete sweep so concurrent
// Set and Delete operations cannot alter the iteration. If the stop signal and
// a tick are simultaneously ready, selecting the tick permits another sweep
// before shutdown is observed. The worker stops its own ticker on return; Get
// already enforces expiry between sweeps.
func (s *OpenAISessionStore) cleanup() {
	// Sweep expired storage every five minutes; lookups reject stale sessions between
	// these scheduled cleanup attempts, regardless of when the worker next runs.
	ticker := time.NewTicker(5 * time.Minute)
	// The worker owns the ticker and stops it on every return path, including
	// a stop signal observed before the first periodic eviction sweep.
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			// Hold membership stable for the whole expiry sweep; Set and Delete wait
			// so the iteration removes the same session pointers whose ages it inspected.
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
