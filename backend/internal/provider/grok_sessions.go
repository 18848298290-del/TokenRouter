package provider

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// A Grok browser flow has one state proof and PKCE verifier, and completion
// needs a matching one-use claim. Shared backend writes let different processes
// retrieve and claim that flow. A failed remote write records the ID as
// local-only, while reads and claims after a successful write keep the shared
// backend as their authority even when it later fails.
//
// Local session pointers are stored together with that routing marker under the
// map mutex. Their public proof fields stay immutable after publication, and a
// separate session mutex protects local consumption. Remote consumption
// delegates its one-use guarantee to the backend. Cleanup removes expired local
// entries periodically, while every read also checks age; stopping cleanup
// prevents restart but leaves session operations callable.

// GrokSessionTTL bounds browser proof lifetime for local reads and cleanup;
// the shared backend adapter applies the same configured session lifetime.
const GrokSessionTTL = 30 * time.Minute

// GrokSessionBackend stores shared browser proof and provides a cross-process
// consumption claim.
type GrokSessionBackend interface {
	// Set stores serialized browser proof under the supplied ID with the
	// backend
	// lifetime. The core observes its error to decide whether subsequent
	// retrieval
	// and consumption use process-local storage.
	Set(context.Context, string, any) error
	// Get decodes shared proof into the supplied destination and reports
	// its
	// presence. The core treats absence, error, or excessive decoded age as
	// a
	// failed retrieval before authorization consumption.
	Get(context.Context, string, any) (bool, error)
	// Delete removes shared proof and its consumption marker for the
	// supplied ID.
	// The core discards deletion errors during completion cleanup and
	// independently clears its local pointer and routing marker.
	Delete(context.Context, string) error
	// TryConsume attempts the backend claim for shared proof and reports
	// whether
	// it succeeded. The core accepts only true with nil error;
	// shared-marker
	// contention or backend failure prevents exchange.
	TryConsume(context.Context, string) (bool, error)
}

// GrokOAuthSession contains immutable browser proof and a protected local
// consumption flag.
type GrokOAuthSession struct {
	// State, CodeVerifier, CodeChallenge, ClientID, Scope, ProxyURL, and
	// RedirectURI describe one flow. Callers leave those published fields
	// unchanged while storage and completion read their shared session
	// pointer.
	State         string `json:"state"`               // browser session state proof
	CodeVerifier  string `json:"code_verifier"`       // stored PKCE secret verifier
	CodeChallenge string `json:"code_challenge"`      // browser PKCE verifier challenge
	ClientID      string `json:"client_id,omitempty"` // stored OAuth client identity
	Scope         string `json:"scope,omitempty"`     // stored OAuth scope text
	ProxyURL      string `json:"proxy_url,omitempty"` // saved exchange proxy endpoint
	RedirectURI   string `json:"redirect_uri"`        // saved OAuth callback URI
	// CreatedAt supplies the age checked by Get and local cleanup. Future
	// instants
	// remain accepted by that age comparison until their elapsed lifetime
	// exceeds
	// GrokSessionTTL.
	CreatedAt time.Time `json:"created_at"` // browser session creation instant

	// mu protects consumed through the first claim transition. Exported
	// proof
	// fields remain immutable after publication, so their readers do not
	// acquire
	// this local claim lock.
	mu       sync.Mutex // local consumption transition lock
	consumed bool       // local one-use claim recorded
}

// GrokSessionStore selects shared or local browser state and owns local
// cleanup.
type GrokSessionStore struct {
	// runtimeMu protects runtimeStarted and runtimeStopped while Start
	// registers
	// runtimeWG before launching cleanup. Stop closes stopCh once, then
	// waits for
	// that registered loop to exit.
	runtimeMu      sync.Mutex     // cleanup lifecycle transition lock
	runtimeStarted bool           // cleanup loop already launched
	runtimeStopped bool           // cleanup restart permanently forbidden
	runtimeWG      sync.WaitGroup // cleanup actual completion barrier

	// mu protects sessions and localOnly through publication and removal. A
	// localOnly marker selects memory reads and claims; absent markers keep
	// a
	// configured remote backend authoritative.
	mu        sync.RWMutex                 // session map and routing lock
	sessions  map[string]*GrokOAuthSession // shared local session pointers
	localOnly map[string]struct{}          // IDs whose remote write failed

	// stopCh is closed once by Stop and observed by cleanup. Direct session
	// operations remain callable independently of this cleanup cancellation
	// signal.
	stopCh chan struct{} // cleanup cancellation broadcast channel
	// remote is retained by the constructor and used with background
	// contexts.
	// Owners keep the backend available for session operations
	// independently of
	// cleanup Start and Stop.
	remote GrokSessionBackend // optional shared proof backend
}

// grokSessionDTO serializes browser proof without the process-local mutex and
// claim flag.
type grokSessionDTO struct {
	State         string    `json:"state"`               // browser session state proof
	CodeVerifier  string    `json:"code_verifier"`       // stored PKCE secret verifier
	CodeChallenge string    `json:"code_challenge"`      // browser PKCE verifier challenge
	ClientID      string    `json:"client_id,omitempty"` // stored OAuth client identity
	Scope         string    `json:"scope,omitempty"`     // stored OAuth scope text
	ProxyURL      string    `json:"proxy_url,omitempty"` // saved exchange proxy endpoint
	RedirectURI   string    `json:"redirect_uri"`        // saved OAuth callback URI
	CreatedAt     time.Time `json:"created_at"`          // serialized browser creation instant
}

// TryConsume claims this local browser session once and reports whether the
// transition succeeded. Nil sessions return false, and a previously consumed
// session remains unavailable.
//
// The session mutex protects the consumed check and assignment as one
// transition, so simultaneous completion attempts cannot both claim the same
// stored pointer. Proof fields are read separately and remain immutable after
// publication. Shared-backend flows use the backend consumption marker instead,
// because a local pointer's flag coordinates only callers within this process.
func (s *GrokOAuthSession) TryConsume() bool {
	if s == nil {
		return false
	}
	// Keep the first check and assignment together; a losing local
	// completion
	// leaves consumed set so the same shared pointer cannot be claimed
	// again.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.consumed {
		return false
	}
	s.consumed = true
	return true
}

// NewGrokSessionStore constructs empty local maps and a cleanup cancellation
// channel while retaining the optional backend. Nil remote selects memory
// storage; callers start cleanup separately and keep published session proof
// stable.
func NewGrokSessionStore(remote GrokSessionBackend) *GrokSessionStore {
	store := &GrokSessionStore{
		remote: remote,

		sessions: make(map[string]*GrokOAuthSession),

		localOnly: make(map[string]struct{}),

		stopCh: make(chan struct{}),
	}

	return store
}

// Set publishes a nonnil session pointer and records whether shared persistence
// succeeded. A nil session is skipped; owners use distinct IDs for outstanding
// flows and keep proof fields unchanged after publication.
//
// The backend write runs first with a background context. Local publication
// then updates the pointer and local-only marker together under mu. Failed
// remote writes select memory and log the failure; successful writes clear that
// marker. This ordering routes subsequent reads and claims consistently with
// this write outcome, while independent writes of the same ID still follow
// their completion order.
func (s *GrokSessionStore) Set(sessionID string, session *GrokOAuthSession) {
	if session == nil {
		return
	}
	var remoteErr error
	if s != nil && s.remote != nil {
		remoteErr = s.remote.Set(context.Background(), sessionID, grokSessionDTO{
			State:         session.State,
			CodeVerifier:  session.CodeVerifier,
			CodeChallenge: session.CodeChallenge,

			ClientID: session.ClientID,
			Scope:    session.Scope,
			ProxyURL: session.ProxyURL,

			RedirectURI: session.RedirectURI,
			CreatedAt:   session.CreatedAt,
		})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sessionID] = session
	// A failed shared write cannot support cross-process retrieval or
	// claims.
	// Publish its local-only marker with the pointer so both operations
	// select
	// the same memory authority.
	if remoteErr != nil {
		s.localOnly[sessionID] = struct{}{}
		slog.Warn("xai oauth session Redis write failed; using process-local fallback", "error", remoteErr)
	} else {
		delete(s.localOnly, sessionID)
	}
}

// Get returns an unexpired session using the routing marker stored during
// publication. Local-only IDs return the shared local pointer; other IDs use
// the configured backend, or memory when no backend exists.
//
// A failed, absent, or expired remote read returns false and keeps that backend
// authoritative for the flow. Successful remote decoding constructs a fresh
// local session and caches its pointer, while cross-process consumption still
// uses the backend marker. Callers leave the returned proof fields unchanged;
// retrieval alone does not claim the session.
func (s *GrokSessionStore) Get(sessionID string) (*GrokOAuthSession, bool) {
	if s.isLocalOnly(sessionID) {
		return s.getMemory(sessionID)
	}
	if s != nil && s.remote != nil {
		var dto grokSessionDTO
		ok, err := s.remote.Get(context.Background(), sessionID, &dto)
		// This ID was shared successfully, so a backend read failure
		// cannot use
		// a local copy whose claim flag is invisible to other
		// processes.
		if err != nil || !ok || time.Since(dto.CreatedAt) > GrokSessionTTL {
			return nil, false
		}
		session := &GrokOAuthSession{
			State:         dto.State,
			CodeVerifier:  dto.CodeVerifier,
			CodeChallenge: dto.CodeChallenge,

			ClientID: dto.ClientID,
			Scope:    dto.Scope,
			ProxyURL: dto.ProxyURL,

			RedirectURI: dto.RedirectURI,
			CreatedAt:   dto.CreatedAt,
		}
		// Cache the decoded proof pointer with a fresh local claim
		// flag; this
		// shared flow still consumes through the remote marker rather
		// than that flag.
		s.mu.Lock()
		s.sessions[sessionID] = session
		s.mu.Unlock()
		return session, true
	}
	return s.getMemory(sessionID)
}

// getMemory returns the shared local session pointer when its age is within
// GrokSessionTTL. Missing or expired entries return false without immediate
// deletion; callers keep returned proof fields immutable after the map lock is
// released.
func (s *GrokSessionStore) getMemory(sessionID string) (*GrokOAuthSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return nil, false
	}
	if time.Since(session.CreatedAt) > GrokSessionTTL {
		return nil, false
	}
	return session, true
}

// Delete attempts background-context remote deletion, discards its error, and
// clears both local maps under mu. A failed remote delete can leave shared
// state present, so callers use the separate consumption claim for one-use
// exchange.
func (s *GrokSessionStore) Delete(sessionID string) {
	if s != nil && s.remote != nil {
		// Deletion is best effort after completion. Clear the local
		// pointer and
		// routing marker even if shared cleanup fails; consumption
		// remains the
		// exchange's one-use check.
		_ = s.remote.Delete(context.Background(), sessionID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionID)
	delete(s.localOnly, sessionID)
}

// TryConsumeSession selects the same local-only routing used for retrieval and
// reports a successful one-use claim. Nil stores return false. Local claims
// require an unexpired stored session and use its mutex-protected flag.
//
// IDs whose remote write succeeded use backend TryConsume, and any backend
// error becomes a failed claim. A failed remote claim therefore cannot be
// retried through an unshared local flag, which would permit different
// processes to exchange the same flow. Owners keep outstanding IDs distinct and
// let the shared backend implement its cross-process claim.
func (s *GrokSessionStore) TryConsumeSession(sessionID string) bool {
	if s == nil {
		return false
	}
	if s.isLocalOnly(sessionID) {
		return s.tryConsumeMemory(sessionID)
	}
	if s.remote != nil {
		// Keep successful shared publication paired with the shared
		// claim; an
		// error cannot safely switch to a local flag that other
		// processes cannot
		// observe.
		ok, err := s.remote.TryConsume(context.Background(), sessionID)
		return err == nil && ok
	}
	return s.tryConsumeMemory(sessionID)
}

// isLocalOnly reads the process-local routing marker under the session map
// lock.
func (s *GrokSessionStore) isLocalOnly(sessionID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.localOnly[sessionID]
	return ok
}

// tryConsumeMemory looks up an unexpired local session and attempts its
// protected one-use claim. Missing or expired proof returns false; the returned
// pointer's claim lock determines which local completion wins.
func (s *GrokSessionStore) tryConsumeMemory(sessionID string) bool {
	session, ok := s.getMemory(sessionID)
	return ok && session.TryConsume()
}

// Start registers and launches local cleanup once, unless this store has
// already stopped. runtimeMu serializes that admission with Stop, and runtimeWG
// records the loop before it is started.
//
// The cleanup goroutine therefore belongs to the completion barrier seen by a
// concurrent stop. Repeated starts return without another loop, and stopped
// stores remain unable to restart cleanup. Session Set, Get, Delete, and claim
// operations have their own behavior and remain available after this lifecycle
// flag changes.
func (s *GrokSessionStore) Start() {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.runtimeStarted || s.runtimeStopped {
		return
	}
	s.runtimeStarted = true
	// Register cleanup while the startup lock still excludes Stop, so its
	// completion barrier includes the goroutine even before it begins
	// running.
	s.runtimeWG.Go(func() { ; s.cleanup() })
}

// Stop permanently rejects cleanup startup, closes its cancellation channel
// once, and waits for the cleanup loop's actual exit. Calling it before Start
// or repeatedly is supported for a constructed store.
//
// The lifecycle mutex covers channel closure and startup flags, while waiting
// happens after releasing that mutex so cleanup can finish its map access.
// Session operations use a separate map lock and stay callable after cleanup
// stops. Remote operations use their own background contexts and are not
// registered with this cleanup barrier.
func (s *GrokSessionStore) Stop() {
	s.runtimeMu.Lock()
	if !s.runtimeStopped {
		s.runtimeStopped = true
		// Close once under the lifecycle lock, then wait outside it so
		// the loop
		// can finish its protected map cleanup before Stop returns.
		close(s.stopCh)
	}
	s.runtimeMu.Unlock()
	s.runtimeWG.Wait()
}

// cleanup checks local session ages every five minutes and removes expired
// pointers with their routing markers under mu. It releases the ticker on exit;
// stopCh ends this loop while direct reads continue to enforce age
// independently.
func (s *GrokSessionStore) cleanup() {
	// Periodic removal supplements the immediate age check on every read.
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			// Remove pointer and local-only marker under the
			// publication lock so
			// cleanup cannot discard only one half of a newly
			// stored session.
			s.mu.Lock()
			for id, session := range s.sessions {
				if time.Since(session.CreatedAt) > GrokSessionTTL {
					delete(s.sessions, id)
					delete(s.localOnly, id)
				}
			}
			s.mu.Unlock()
		}
	}
}
