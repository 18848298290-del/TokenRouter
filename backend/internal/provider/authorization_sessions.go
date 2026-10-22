package provider

import (
	"sync"
	"time"
)

// authorization_sessions.go stores typed OAuth session pointers and coordinates
// their expiration worker. Authorization exchange needs the creation timestamp
// supplied with each session, so lookup and cleanup calculate age from that
// timestamp. Insertion retains the caller's pointer, and a failed lookup leaves
// an expired entry stored until deletion or a periodic sweep removes it.
// Platform stores provide the timestamp accessor and their session TTL.
//
// Construction initializes storage with cleanup idle. Lifecycle admission uses a
// separate mutex from map access, and worker registration precedes any shutdown
// wait. Stop permanently closes admission, including when called before Start,
// and joins admitted work after releasing the lifecycle mutex. Storage remains
// available after cleanup stops. Each sweep holds the map lock while comparing
// ages and removing entries, so replacement cannot invalidate a removal choice.

// authorizationSessionStore retains borrowed OAuth proof and owns one cleanup
// worker shared by the platform-specific session store implementations.
type authorizationSessionStore[T any] struct {
	// runtimeMu protects runtimeStarted and runtimeStopped while runtimeWG tracks
	// the admitted cleanup worker. Startup registers before releasing this lock;
	// shutdown closes stopCh once and waits after unlocking.
	runtimeMu      sync.Mutex     // cleanup lifecycle admission lock
	runtimeStarted bool           // cleanup worker was admitted
	runtimeStopped bool           // cleanup admission is closed
	runtimeWG      sync.WaitGroup // admitted cleanup worker completion

	// mu protects sessions membership and iteration. Stored pointers remain shared
	// with their callers, who keep pointed-to fields stable while stored or borrowed.
	mu       sync.RWMutex  // session map access lock
	sessions map[string]*T // sessions by opaque identifier

	// ttl and createdAt define lookup and sweep expiry. The platform supplies both
	// at construction, and neither changes while the store is available to callers.
	ttl       time.Duration      // maximum accepted session age
	createdAt func(*T) time.Time // session creation timestamp reader

	// stopCh is initialized at construction and closed once under runtimeMu.
	// Cleanup observes its closure before that worker can finish.
	stopCh chan struct{}
}

// newAuthorizationSessionStore creates empty storage using ttl and a nonnil
// creation timestamp reader. It starts no worker; callers initialize their
// owning store with this value before invoking storage or lifecycle methods.
func newAuthorizationSessionStore[T any](ttl time.Duration, createdAt func(*T) time.Time) authorizationSessionStore[T] {
	return authorizationSessionStore[T]{
		sessions:  make(map[string]*T),
		ttl:       ttl,
		createdAt: createdAt,
		stopCh:    make(chan struct{}),
	}
}

// set stores session by reference under sessionID, replacing any earlier entry.
// The session must be nonnil, and callers keep its fields stable while storage
// or borrowers can read them, including after shutdown.
func (s *authorizationSessionStore[T]) set(sessionID string, session *T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Replacement changes future lookups while earlier borrowers keep their pointers;
	// map ownership therefore does not authorize mutation of either published session.
	s.sessions[sessionID] = session
}

// get returns the borrowed session pointer when its creation age is no greater
// than ttl. Missing and older entries return nil and false; expired entries
// remain stored for deletion or cleanup.
func (s *authorizationSessionStore[T]) get(sessionID string) (*T, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return nil, false
	}
	// Lookup enforces age between sweeps so an expired entry retained in the map
	// cannot supply proof for a later authorization exchange.
	if time.Since(s.createdAt(session)) > s.ttl {
		return nil, false
	}
	return session, true
}

// delete removes sessionID from future lookups and accepts an absent identifier.
// Existing borrowers keep their session pointers, so callers arrange any exclusive
// exchange claim independently of this membership change.
func (s *authorizationSessionStore[T]) delete(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Removing membership leaves borrowed pointers intact; another exchange may
	// already have obtained the same proof before this deletion acquires the lock.
	delete(s.sessions, sessionID)
}

// start registers the cleanup goroutine once for an initialized store.
// Repeated starts and every start after shutdown return immediately.
//
// The runtime mutex keeps flag inspection and worker admission together, so
// shutdown cannot finish its wait before an accepted worker is registered.
// Cleanup uses the separate map lock for removal. The owning platform store
// later calls stop to join admitted cleanup work, while storage methods remain
// usable throughout that worker's lifetime.
func (s *authorizationSessionStore[T]) start() {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.runtimeStarted || s.runtimeStopped {
		return
	}
	// Register while lifecycle admission is locked, so a concurrent stop waits
	// for this accepted cleanup worker before reporting shutdown completion.
	s.runtimeStarted = true
	s.runtimeWG.Go(s.cleanup)
}

// stop closes cleanup admission and waits for the registered worker to exit.
// An initialized store can stop before startup, and concurrent callers share
// the same closed stop signal.
//
// Channel closure and the stopped flag change under the runtime mutex. Waiting
// begins after releasing that mutex, so other lifecycle callers can inspect the
// terminal state while the worker finishes. The session map remains available
// through its separate lock after cleanup ends, with borrowed pointer ownership
// still governed by storage callers.
func (s *authorizationSessionStore[T]) stop() {
	s.runtimeMu.Lock()
	// Publish the terminal flag and signal closure together. Future starts cannot
	// admit work, and concurrent stoppers avoid closing the signal a second time.
	if !s.runtimeStopped {
		s.runtimeStopped = true
		close(s.stopCh)
	}
	s.runtimeMu.Unlock()
	s.runtimeWG.Wait()
}

// cleanup removes sessions older than ttl on five-minute ticks until shutdown
// closes its signal. It runs as the registered worker and stops its ticker when
// returning.
//
// Entry-age checks and deletions share the map lock, so replacement cannot
// interleave with examination of an entry. Shutdown and a tick can become ready
// together; a selected tick completes its sweep before the next stop observation.
// Lookup checks expiry separately, so delayed removal does not extend a
// session's usable lifetime.
func (s *authorizationSessionStore[T]) cleanup() {
	// Sweep retained entries every five minutes; Get rejects expired proof between
	// these removal attempts, independently of whether cleanup has observed a tick.
	ticker := time.NewTicker(5 * time.Minute)
	// The worker owns its ticker and releases it even when shutdown is observed
	// before the first scheduled sweep has removed any entries.
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			// Hold membership stable for the whole sweep so a replacement cannot be
			// removed using the expired pointer examined before that replacement.
			s.mu.Lock()
			for id, session := range s.sessions {
				if time.Since(s.createdAt(session)) > s.ttl {
					delete(s.sessions, id)
				}
			}
			s.mu.Unlock()
		}
	}
}
