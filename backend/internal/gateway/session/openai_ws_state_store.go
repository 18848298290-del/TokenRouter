package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// openai_ws_state_store.go stores Responses provider affinity, HTTP ownership,
// and local WebSocket continuation state. Provider and HTTP-owner records
// combine local maps with optional GatewayCache storage; connection IDs, turn
// state, and rejected-ciphertext digests remain process-local. Response
// provider and owner maps include group identity, while response-to-connection
// affinity uses the response ID alone.
//
// Local bindings are installed before shared persistence. A failed shared write
// leaves local state available, and HTTP ownership writes user and key
// separately using one caller-derived context. Reads also fetch those
// components separately, then cache a complete positive pair for one minute.
// Provider reads instead suppress shared-cache errors as misses and return
// successful shared results without populating the local provider map.
//
// Each map has its own lock, and capacity checks happen inside its write lock.
// A full map evicts one arbitrary existing key when a new key is admitted.
// Periodic cleanup elects one caller with an atomic timestamp and scans at most
// 512 entries per map; expired entries can remain physically present between
// scans. Rejected-digest reads copy their map under the lock so callers can
// modify the returned set independently.

// Shared-key prefixes separate provider and HTTP-owner records; the limits bound
// local map admission, incremental cleanup, and the timeout attached to
// shared-cache contexts.
const (
	openAIWSResponseProviderCachePrefix = "openai:response:"                 // shared response-to-provider key prefix
	openAIHTTPResponseOwnerUserPrefix   = "openai:http-response-owner:user:" // shared HTTP response user prefix
	openAIHTTPResponseOwnerKeyPrefix    = "openai:http-response-owner:key:"  // shared HTTP response credential prefix
	openAIWSStateStoreCleanupInterval   = time.Minute                        // minimum elected cleanup interval
	openAIWSStateStoreCleanupMaxPerMap  = 512                                // maximum entries scanned per map
	openAIWSStateStoreMaxEntriesPerMap  = 65536                              // normal per-map admission capacity
	openAIWSStateStoreRedisTimeout      = 3 * time.Second                    // shared-cache context timeout

	// openAIWSInvalidEncryptedDigestsPerSession limits retained ciphertext rejection
	// evidence per session; omitted digests can encounter ordinary upstream recovery again.
	openAIWSInvalidEncryptedDigestsPerSession = 512

	// StateStoreRedisTimeout sets the context timeout for shared-state and
	// preemption storage calls. Earlier parent deadlines and cancellation apply to
	// that context; storage implementations determine when their calls finish.
	StateStoreRedisTimeout = openAIWSStateStoreRedisTimeout
)

// openAIWSProviderBinding holds a locally bound continuation provider and
// deadline.
type openAIWSProviderBinding struct {
	providerID int64 // bound upstream provider identifier
	// expiresAt records binding validity. Local provider lookup requires time
	// before expiresAt; the exact deadline already falls through to shared lookup.
	expiresAt time.Time
}

// openAIHTTPResponseOwnerBinding stores the positive user/key pair for HTTP
// continuation.
type openAIHTTPResponseOwnerBinding struct {
	// userID and apiKeyID are a positive ownership pair stored together in the
	// local map; BindHTTPResponseOwner accepts the pair after validating both IDs.
	userID   int64 // stored authenticated user identifier
	apiKeyID int64 // stored authenticated credential identifier
	// expiresAt records binding validity. Local owner lookup requires time before
	// expiresAt; a shared positive pair receives a one-minute local deadline.
	expiresAt time.Time
}

// openAIWSConnBinding names a local socket preferred for one response
// continuation.
type openAIWSConnBinding struct {
	connID string // preferred local socket identifier
	// expiresAt records binding validity. Connection lookup accepts the exact
	// expiresAt instant and rejects strictly later observations of this stored
	// affinity.
	expiresAt time.Time
}

// openAIWSTurnStateBinding holds opaque upstream state for one group/session
// identity.
type openAIWSTurnStateBinding struct {
	turnState string // trimmed opaque upstream state
	// expiresAt records binding validity. Turn-state lookup accepts the exact
	// expiresAt instant; a later observation returns no state for the session.
	expiresAt time.Time
}

// openAIWSSessionConnBinding names local socket affinity for one group/session
// identity.
type openAIWSSessionConnBinding struct {
	connID string // preferred local socket identifier
	// expiresAt records binding validity. Session-connection lookup accepts the
	// exact expiresAt instant and rejects observations strictly after this stored
	// deadline.
	expiresAt time.Time
}

// openAIWSInvalidEncryptedBinding holds rejected ciphertext digests under one
// session deadline.
type openAIWSInvalidEncryptedBinding struct {
	// digests and expiresAt are merged and published under sessionInvalidEncryptedMu.
	// Digest reads copy the set while holding its read lock before callers can mutate their snapshot.
	digests   map[string]struct{} // retained rejected ciphertext hashes
	expiresAt time.Time           // shared digest evidence deadline
}

// OpenAIWSStateStore supplies response continuity, HTTP ownership, and local
// socket or ciphertext-recovery state. The default implementation combines
// optional shared provider/owner storage with process-local maps; callers use
// a constructed store and apply provider eligibility or authorization after
// reading its evidence.
type OpenAIWSStateStore interface {
	// BindResponseProvider records a positive provider for the group's trimmed
	// response ID. Blank IDs and nonpositive providers are ignored, and the
	// default store uses one hour for nonpositive TTL. Local binding happens
	// before optional shared persistence; a shared write error leaves the local
	// entry usable.
	//
	// Callers distinguish that local availability from successful cross-instance
	// persistence, and still check provider eligibility before continuing through
	// stored affinity. Local capacity eviction and map publication are
	// synchronized, while the subsequent shared update is a separate operation.
	BindResponseProvider(ctx context.Context, groupID int64, responseID string, providerID int64, ttl time.Duration) error
	// GetResponseProvider returns provider-affinity evidence for the group's
	// trimmed response ID. The default store accepts a live local binding first,
	// then queries optional shared storage. Shared errors or nonpositive values
	// become zero with nil error, leaving routing to choose an eligible provider
	// through its ordinary rules.
	//
	// Successful shared reads are returned without filling the local provider map.
	// That path can observe subsequent shared deletion, while a locally published
	// binding stays authoritative until expiry or removal. Callers treat the
	// result as selection evidence, not authorization to use an unavailable
	// provider.
	GetResponseProvider(ctx context.Context, groupID int64, responseID string) (int64, error)
	// DeleteResponseProvider removes the group's response-to-provider affinity for
	// a trimmed response ID. The default store removes local state before optional
	// shared deletion and ignores blank IDs. A returned shared error can therefore
	// leave a remote binding even after the local one has gone.
	//
	// The two removals have no rollback or cross-storage transaction. Subsequent
	// lookup can recover an unsuccessfully removed shared value, and concurrent
	// binding can change what either storage holds. Callers choose removal when
	// that stored preference should no longer guide response continuation.
	DeleteResponseProvider(ctx context.Context, groupID int64, responseID string) error
	// BindHTTPResponseOwner records positive user and API Key ownership for a
	// group's trimmed response ID. Blank response IDs or nonpositive owners are
	// ignored; nonpositive TTL selects one hour in the default store. The pair is
	// installed locally before optional shared user and key writes.
	//
	// Those shared components are written separately using one context deadline.
	// Failure can leave local ownership and the first remote component present, so
	// callers handle the returned persistence error independently of local
	// availability. The default store supplies no atomic remote pair replacement
	// or rollback.
	BindHTTPResponseOwner(ctx context.Context, groupID int64, responseID string, userID, apiKeyID int64, ttl time.Duration) error
	// GetHTTPResponseOwner returns user/key ownership and whether a usable pair
	// was found for the group's response ID. The default store accepts live local
	// ownership first, otherwise separately reads shared user and key components.
	// Either nonpositive component means no usable pair, and storage errors
	// propagate.
	//
	// A complete shared pair receives a one-minute local deadline. Shared reads
	// can observe different replacement generations, and an in-flight refresh can
	// replace a newer local bind. The caller applies ValidateHTTPResponseOwner's
	// identity comparison after obtaining the returned ownership evidence.
	GetHTTPResponseOwner(ctx context.Context, groupID int64, responseID string) (userID, apiKeyID int64, found bool, err error)

	// BindResponseConn stores a trimmed response ID's preferred local socket.
	// Blank IDs or connections are ignored; the default store normalizes
	// nonpositive TTL to one hour and holds this affinity on the current instance.
	BindResponseConn(responseID, connID string, ttl time.Duration)
	// GetResponseConn returns live local connection affinity for a trimmed
	// response ID. Missing, expired, or blank connection data returns false; the
	// ID is a pool preference whose actual socket availability is checked by its
	// consumer.
	GetResponseConn(responseID string) (string, bool)
	// DeleteResponseConn removes local response-to-connection affinity for a
	// trimmed response ID. Blank IDs and absent entries are harmless; removal
	// affects the remembered preference while the connection pool owns the socket
	// lifetime.
	DeleteResponseConn(responseID string)

	// BindSessionTurnState stores trimmed opaque state for a group and session
	// hash. Blank hashes or state are ignored; the default store uses one hour for
	// nonpositive TTL and retains this state on the current instance.
	BindSessionTurnState(groupID int64, sessionHash, turnState string, ttl time.Duration)
	// GetSessionTurnState returns live opaque state for the group/session
	// identity. Missing, expired, or blank state returns false; consumers use
	// remembered state when the current handshake supplies no newer value.
	GetSessionTurnState(groupID int64, sessionHash string) (string, bool)
	// DeleteSessionTurnState removes the group/session's local opaque state. Blank
	// hashes and absent bindings are harmless; subsequent lookup returns no
	// remembered value until another selected turn publishes state for that
	// identity.
	DeleteSessionTurnState(groupID int64, sessionHash string)

	// BindSessionConn stores a preferred local socket for the group/session hash.
	// Blank hashes or connections are ignored; nonpositive TTL selects one hour in
	// the default store, and the pool checks that preference's usability
	// separately.
	BindSessionConn(groupID int64, sessionHash, connID string, ttl time.Duration)
	// GetSessionConn returns live socket affinity for the group/session hash.
	// Missing, expired, or blank data returns false; store-disabled ingress uses
	// this preference when its request supplies no previous response ID.
	GetSessionConn(groupID int64, sessionHash string) (string, bool)
	// DeleteSessionConn removes local socket affinity for the group/session hash.
	// Blank hashes and absent bindings are harmless; the operation clears a
	// remembered preference while the connection pool continues to own the socket.
	DeleteSessionConn(groupID int64, sessionHash string)

	// MarkSessionInvalidEncryptedContent merges trimmed rejected-ciphertext
	// digests under the group/session identity. Blank hashes or empty slices are
	// ignored; nonpositive TTL uses one hour in the default store. The live
	// retained set is renewed, with at most 512 distinct digests kept per session.
	//
	// Expired evidence starts a fresh set, while capacity overflow omits newly
	// seen values and logs after releasing the write lock. Callers collect
	// rejected-item digests before stripping payloads, then use an independent
	// live snapshot on later turns to identify ciphertext already rejected for
	// that session.
	MarkSessionInvalidEncryptedContent(groupID int64, sessionHash string, digests []string, ttl time.Duration)
	// GetSessionInvalidEncryptedContentDigests returns an independent live
	// digest-set snapshot for a group/session identity. Missing, expired, or empty
	// evidence returns nil; callers can mutate a returned default-store snapshot
	// without changing stored digests.
	GetSessionInvalidEncryptedContentDigests(groupID int64, sessionHash string) map[string]struct{}
	// HasAnySessionInvalidEncryptedContent reports physical rejected-digest map
	// presence in the default store. False lets callers skip session hashing; true
	// can include expired or empty entries and requires a session-specific live
	// lookup.
	HasAnySessionInvalidEncryptedContent() bool
}

// defaultOpenAIWSStateStore implements shared response identity and
// instance-local continuation maps.
type defaultOpenAIWSStateStore struct {
	// observe receives synchronous overflow diagnostics outside the digest lock;
	// nil suppresses reporting, and concurrent callers need a concurrency-safe observer.
	observe func(string, ...any)
	// cache supplies shared provider and HTTP-owner persistence; nil uses local
	// bindings alone, while nonnil storage calls receive a caller-derived context
	// with a deadline.
	cache GatewayCache

	// responseToProviderMu guards responseToProvider during lookup, mutation, and
	// cleanup. Admission evicts under this same write lock before publishing a new
	// binding for group/response provider bindings.
	responseToProviderMu sync.RWMutex                       // binding map access lock
	responseToProvider   map[string]openAIWSProviderBinding // group/response provider bindings
	// responseOwnerMu guards responseOwners during lookup, mutation, and cleanup.
	// Admission evicts under this same write lock before publishing a new binding
	// for group/response positive owner pairs.
	responseOwnerMu sync.RWMutex                              // binding map access lock
	responseOwners  map[string]openAIHTTPResponseOwnerBinding // group/response positive owner pairs
	// responseToConnMu guards responseToConn during lookup, mutation, and cleanup.
	// Admission evicts under this same write lock before publishing a new binding
	// for response-only socket preferences.
	responseToConnMu sync.RWMutex                   // binding map access lock
	responseToConn   map[string]openAIWSConnBinding // response-only socket preferences
	// sessionToTurnStateMu guards sessionToTurnState during lookup, mutation, and
	// cleanup. Admission evicts under this same write lock before publishing a new
	// binding for group/session opaque turn state.
	sessionToTurnStateMu sync.RWMutex                        // binding map access lock
	sessionToTurnState   map[string]openAIWSTurnStateBinding // group/session opaque turn state
	// sessionToConnMu guards sessionToConn during lookup, mutation, and cleanup.
	// Admission evicts under this same write lock before publishing a new binding
	// for group/session socket preferences.
	sessionToConnMu sync.RWMutex                          // binding map access lock
	sessionToConn   map[string]openAIWSSessionConnBinding // group/session socket preferences

	// sessionInvalidEncryptedMu guards sessionInvalidEncrypted during lookup,
	// mutation, and cleanup. Admission evicts under this same write lock before
	// publishing a new binding for group/session rejected-ciphertext sets.
	sessionInvalidEncryptedMu sync.RWMutex                               // binding map access lock
	sessionInvalidEncrypted   map[string]openAIWSInvalidEncryptedBinding // group/session rejected-ciphertext sets

	// lastCleanupUnixNano stores Unix nanoseconds to elect one cleanup caller;
	// the winner publishes its start time before scans, and other callers skip that interval.
	lastCleanupUnixNano atomic.Int64
}

// NewOpenAIWSStateStore initializes local maps and binds optional shared cache
// storage. Nil cache leaves the store local. Only the first observer is
// selected; it runs synchronously for digest overflow after the write lock is
// released, so concurrent callers need a suitable observer.
func NewOpenAIWSStateStore(cache GatewayCache, observers ...func(string, ...any)) OpenAIWSStateStore {
	store := &defaultOpenAIWSStateStore{
		cache:                   cache,
		responseToProvider:      make(map[string]openAIWSProviderBinding, 256),
		responseOwners:          make(map[string]openAIHTTPResponseOwnerBinding, 256),
		responseToConn:          make(map[string]openAIWSConnBinding, 256),
		sessionToTurnState:      make(map[string]openAIWSTurnStateBinding, 256),
		sessionToConn:           make(map[string]openAIWSSessionConnBinding, 256),
		sessionInvalidEncrypted: make(map[string]openAIWSInvalidEncryptedBinding),
	}
	store.lastCleanupUnixNano.Store(time.Now().UnixNano())
	if len(observers) > 0 {
		store.observe = observers[0]
	}
	return store
}

// BindHTTPResponseOwner installs a positive user/key pair for a trimmed
// response ID and group. Blank IDs or nonpositive owners are ignored;
// nonpositive TTL selects one hour. The local pair is published under its map
// lock before any shared persistence, so it remains readable after a returned
// shared error.
//
// Shared user and API Key values are written sequentially using one derived
// context. Failure can retain the first remote component without the second,
// and there is no rollback or remote atomic pair operation. Same-response
// callers coordinate ownership replacement if they need both instances to
// observe one consistent generation of the pair.
func (s *defaultOpenAIWSStateStore) BindHTTPResponseOwner(ctx context.Context, groupID int64, responseID string, userID, apiKeyID int64, ttl time.Duration) error {
	id := normalizeOpenAIWSResponseID(responseID)
	if id == "" || userID <= 0 || apiKeyID <= 0 {
		return nil
	}
	ttl = normalizeOpenAIWSTTL(ttl)
	s.maybeCleanup()

	mapKey := openAIWSResponseProviderMapKey(groupID, id)
	// Publish a complete local owner pair under one lock; the later shared
	// sequence is separate, so local authorization evidence survives shared write failure.
	s.responseOwnerMu.Lock()
	ensureBindingCapacity(s.responseOwners, mapKey, openAIWSStateStoreMaxEntriesPerMap)
	s.responseOwners[mapKey] = openAIHTTPResponseOwnerBinding{
		userID: userID, apiKeyID: apiKeyID, expiresAt: time.Now().Add(ttl),
	}
	s.responseOwnerMu.Unlock()

	if s.cache == nil {
		return nil
	}
	cacheCtx, cancel := withOpenAIWSStateStoreRedisTimeout(ctx)
	defer cancel()
	// Write user before key using the same derived context; if the second write
	// fails, the first component can remain shared because this sequence has no rollback.
	if err := s.cache.SetSessionProviderID(cacheCtx, groupID, openAIHTTPResponseOwnerCacheKey(openAIHTTPResponseOwnerUserPrefix, id), userID, ttl); err != nil {
		return err
	}
	return s.cache.SetSessionProviderID(cacheCtx, groupID, openAIHTTPResponseOwnerCacheKey(openAIHTTPResponseOwnerKeyPrefix, id), apiKeyID, ttl)
}

// GetHTTPResponseOwner returns the positive user/key pair for a trimmed response
// ID and group. Blank IDs return found=false; live local pairs return immediately.
// A local miss with no shared cache also returns found=false. Shared read errors
// propagate, and either nonpositive remote component makes the pair unavailable.
//
// The user and key are read separately using one derived context, so concurrent
// shared replacement can mix their generations. A complete pair is stored locally
// with a one-minute deadline measured from the lookup's start. That local refresh
// can replace a newer local bind installed while remote reads were in flight;
// callers coordinate replacements when cross-instance ownership ordering matters.
func (s *defaultOpenAIWSStateStore) GetHTTPResponseOwner(ctx context.Context, groupID int64, responseID string) (int64, int64, bool, error) {
	id := normalizeOpenAIWSResponseID(responseID)
	if id == "" {
		return 0, 0, false, nil
	}
	s.maybeCleanup()

	now := time.Now()
	mapKey := openAIWSResponseProviderMapKey(groupID, id)
	s.responseOwnerMu.RLock()
	if binding, ok := s.responseOwners[mapKey]; ok && now.Before(binding.expiresAt) {
		s.responseOwnerMu.RUnlock()
		return binding.userID, binding.apiKeyID, true, nil
	}
	s.responseOwnerMu.RUnlock()

	if s.cache == nil {
		return 0, 0, false, nil
	}
	cacheCtx, cancel := withOpenAIWSStateStoreRedisTimeout(ctx)
	defer cancel()
	// Read user and key using the same derived context. These independent reads can
	// observe mixed replacement generations, so cross-instance ordering needs caller coordination.
	userID, err := s.cache.GetSessionProviderID(cacheCtx, groupID, openAIHTTPResponseOwnerCacheKey(openAIHTTPResponseOwnerUserPrefix, id))
	if err != nil || userID <= 0 {
		return 0, 0, false, err
	}
	apiKeyID, err := s.cache.GetSessionProviderID(cacheCtx, groupID, openAIHTTPResponseOwnerCacheKey(openAIHTTPResponseOwnerKeyPrefix, id))
	if err != nil || apiKeyID <= 0 {
		return 0, 0, false, err
	}

	// Cache the pair fetched through separate shared reads for one minute.
	// An in-flight refresh can overwrite a local bind installed after lookup began.
	s.responseOwnerMu.Lock()
	ensureBindingCapacity(s.responseOwners, mapKey, openAIWSStateStoreMaxEntriesPerMap)
	s.responseOwners[mapKey] = openAIHTTPResponseOwnerBinding{
		userID: userID, apiKeyID: apiKeyID, expiresAt: now.Add(time.Minute),
	}
	s.responseOwnerMu.Unlock()
	return userID, apiKeyID, true, nil
}

// BindResponseProvider records a positive continuation provider in the group's
// trimmed response namespace. Blank IDs or nonpositive providers are ignored;
// nonpositive TTL selects one hour. Local capacity eviction and publication
// occur under the provider-map write lock.
//
// The optional shared write follows local publication with a context deadline.
// A returned storage error leaves the local binding usable until its deadline,
// so successful persistence and local lookup are different results. Local and
// shared updates do not form one transaction, and callers apply current
// provider eligibility after obtaining this stored affinity evidence.
func (s *defaultOpenAIWSStateStore) BindResponseProvider(ctx context.Context, groupID int64, responseID string, providerID int64, ttl time.Duration) error {
	id := normalizeOpenAIWSResponseID(responseID)
	if id == "" || providerID <= 0 {
		return nil
	}
	ttl = normalizeOpenAIWSTTL(ttl)
	s.maybeCleanup()

	expiresAt := time.Now().Add(ttl)
	mapKey := openAIWSResponseProviderMapKey(groupID, id)
	// Admit the local provider before shared persistence so a failed remote
	// write still leaves this instance able to resolve the selected response binding.
	s.responseToProviderMu.Lock()
	ensureBindingCapacity(s.responseToProvider, mapKey, openAIWSStateStoreMaxEntriesPerMap)
	s.responseToProvider[mapKey] = openAIWSProviderBinding{providerID: providerID, expiresAt: expiresAt}
	s.responseToProviderMu.Unlock()

	if s.cache == nil {
		return nil
	}
	cacheKey := openAIWSResponseProviderCacheKey(id)
	cacheCtx, cancel := withOpenAIWSStateStoreRedisTimeout(ctx)
	defer cancel()
	return s.cache.SetSessionProviderID(cacheCtx, groupID, cacheKey, providerID, ttl)
}

// GetResponseProvider returns the group's remembered provider for a trimmed
// response ID, or zero when none is usable. A local binding is accepted
// strictly before its deadline. Otherwise optional shared storage is queried
// with a context deadline; its errors and nonpositive values become zero with
// nil error.
//
// A successful shared result is returned directly without filling the local
// map. The next such lookup can therefore observe shared deletion, while
// locally bound entries remain authoritative until expiry or explicit removal.
// This read supplies affinity for selection, so callers still check the
// provider's current eligibility before using the remembered target.
func (s *defaultOpenAIWSStateStore) GetResponseProvider(ctx context.Context, groupID int64, responseID string) (int64, error) {
	id := normalizeOpenAIWSResponseID(responseID)
	if id == "" {
		return 0, nil
	}
	s.maybeCleanup()

	now := time.Now()
	mapKey := openAIWSResponseProviderMapKey(groupID, id)
	s.responseToProviderMu.RLock()
	if binding, ok := s.responseToProvider[mapKey]; ok {
		if now.Before(binding.expiresAt) {
			providerID := binding.providerID
			s.responseToProviderMu.RUnlock()
			return providerID, nil
		}
	}
	s.responseToProviderMu.RUnlock()

	if s.cache == nil {
		return 0, nil
	}

	cacheKey := openAIWSResponseProviderCacheKey(id)
	cacheCtx, cancel := withOpenAIWSStateStoreRedisTimeout(ctx)
	defer cancel()
	providerID, err := s.cache.GetSessionProviderID(cacheCtx, groupID, cacheKey)
	if err != nil || providerID <= 0 {
		// Shared storage failure supplies no provider-affinity evidence. Return a
		// miss so provider selection can continue its current eligibility and recovery rules.
		return 0, nil
	}
	return providerID, nil
}

// DeleteResponseProvider removes provider affinity for the group's trimmed
// response ID. Blank IDs are ignored, and local deletion happens under the map
// lock before optional shared removal. The pool and provider records retain
// their own lifetimes independently of this routing key.
//
// A shared delete failure is returned after local state has already been
// removed; there is no rollback. A later lookup can still obtain the remote
// value if that removal failed. Local and remote changes are separate, so
// concurrent same-response binding can also change what survives either
// operation.
func (s *defaultOpenAIWSStateStore) DeleteResponseProvider(ctx context.Context, groupID int64, responseID string) error {
	id := normalizeOpenAIWSResponseID(responseID)
	if id == "" {
		return nil
	}
	// Remove local affinity before shared deletion; failure of that remote step
	// can leave a value that the next lookup obtains through the shared-cache path.
	s.responseToProviderMu.Lock()
	delete(s.responseToProvider, openAIWSResponseProviderMapKey(groupID, id))
	s.responseToProviderMu.Unlock()

	if s.cache == nil {
		return nil
	}
	cacheCtx, cancel := withOpenAIWSStateStoreRedisTimeout(ctx)
	defer cancel()
	return s.cache.DeleteSessionProviderID(cacheCtx, groupID, openAIWSResponseProviderCacheKey(id))
}

// BindResponseConn stores a trimmed response ID's preferred local socket.
// Blank IDs or connections are ignored; the default store normalizes
// nonpositive TTL to one hour and holds this affinity on the current instance.
// Capacity eviction and replacement happen together under the local map lock.
func (s *defaultOpenAIWSStateStore) BindResponseConn(responseID, connID string, ttl time.Duration) {
	id := normalizeOpenAIWSResponseID(responseID)
	conn := strings.TrimSpace(connID)
	if id == "" || conn == "" {
		return
	}
	ttl = normalizeOpenAIWSTTL(ttl)
	s.maybeCleanup()

	// Keep capacity eviction and publishing response-to-socket preference in the
	// same critical section so another caller cannot admit a new key between those
	// mutations.
	s.responseToConnMu.Lock()
	ensureBindingCapacity(s.responseToConn, id, openAIWSStateStoreMaxEntriesPerMap)
	s.responseToConn[id] = openAIWSConnBinding{
		connID:    conn,
		expiresAt: time.Now().Add(ttl),
	}
	s.responseToConnMu.Unlock()
}

// GetResponseConn returns live local connection affinity for a trimmed
// response ID. Missing, expired, or blank connection data returns false; the
// ID is a pool preference whose actual socket availability is checked by its
// consumer. The exact expiration instant is accepted.
func (s *defaultOpenAIWSStateStore) GetResponseConn(responseID string) (string, bool) {
	id := normalizeOpenAIWSResponseID(responseID)
	if id == "" {
		return "", false
	}
	s.maybeCleanup()

	now := time.Now()
	s.responseToConnMu.RLock()
	binding, ok := s.responseToConn[id]
	s.responseToConnMu.RUnlock()
	if !ok || now.After(binding.expiresAt) || strings.TrimSpace(binding.connID) == "" {
		return "", false
	}
	return binding.connID, true
}

// DeleteResponseConn removes local response-to-connection affinity for a
// trimmed response ID. Blank IDs and absent entries are harmless; removal
// affects the remembered preference while the connection pool owns the socket
// lifetime.
func (s *defaultOpenAIWSStateStore) DeleteResponseConn(responseID string) {
	id := normalizeOpenAIWSResponseID(responseID)
	if id == "" {
		return
	}
	// Remove the remembered response socket under the map lock; an overlapping
	// lookup can still return a snapshot read before this preference was deleted.
	s.responseToConnMu.Lock()
	delete(s.responseToConn, id)
	s.responseToConnMu.Unlock()
}

// BindSessionTurnState stores trimmed opaque state for a group and session
// hash. Blank hashes or state are ignored; the default store uses one hour for
// nonpositive TTL and retains this state on the current instance. Replacement
// occurs under the turn-state map lock.
func (s *defaultOpenAIWSStateStore) BindSessionTurnState(groupID int64, sessionHash, turnState string, ttl time.Duration) {
	key := openAIWSSessionTurnStateKey(groupID, sessionHash)
	state := strings.TrimSpace(turnState)
	if key == "" || state == "" {
		return
	}
	ttl = normalizeOpenAIWSTTL(ttl)
	s.maybeCleanup()

	// Keep capacity eviction and publishing session opaque state in the same
	// critical section so another caller cannot admit a new key between those
	// mutations.
	s.sessionToTurnStateMu.Lock()
	ensureBindingCapacity(s.sessionToTurnState, key, openAIWSStateStoreMaxEntriesPerMap)
	s.sessionToTurnState[key] = openAIWSTurnStateBinding{
		turnState: state,
		expiresAt: time.Now().Add(ttl),
	}
	s.sessionToTurnStateMu.Unlock()
}

// GetSessionTurnState returns live opaque state for the group/session
// identity. Missing, expired, or blank state returns false; consumers use
// remembered state when the current handshake supplies no newer value. The
// exact deadline remains valid.
func (s *defaultOpenAIWSStateStore) GetSessionTurnState(groupID int64, sessionHash string) (string, bool) {
	key := openAIWSSessionTurnStateKey(groupID, sessionHash)
	if key == "" {
		return "", false
	}
	s.maybeCleanup()

	now := time.Now()
	s.sessionToTurnStateMu.RLock()
	binding, ok := s.sessionToTurnState[key]
	s.sessionToTurnStateMu.RUnlock()
	if !ok || now.After(binding.expiresAt) || strings.TrimSpace(binding.turnState) == "" {
		return "", false
	}
	return binding.turnState, true
}

// DeleteSessionTurnState removes the group/session's local opaque state. Blank
// hashes and absent bindings are harmless; subsequent lookup returns no
// remembered value until another selected turn publishes state for that
// identity.
func (s *defaultOpenAIWSStateStore) DeleteSessionTurnState(groupID int64, sessionHash string) {
	key := openAIWSSessionTurnStateKey(groupID, sessionHash)
	if key == "" {
		return
	}
	// Clear remembered opaque state under its owning lock; a lookup already
	// reading the old snapshot can still supply that state to its current handshake.
	s.sessionToTurnStateMu.Lock()
	delete(s.sessionToTurnState, key)
	s.sessionToTurnStateMu.Unlock()
}

// BindSessionConn stores a preferred local socket for the group/session hash.
// Blank hashes or connections are ignored; nonpositive TTL selects one hour in
// the default store, and the pool checks that preference's usability
// separately. Capacity eviction and replacement hold the session-connection
// lock.
func (s *defaultOpenAIWSStateStore) BindSessionConn(groupID int64, sessionHash, connID string, ttl time.Duration) {
	key := openAIWSSessionTurnStateKey(groupID, sessionHash)
	conn := strings.TrimSpace(connID)
	if key == "" || conn == "" {
		return
	}
	ttl = normalizeOpenAIWSTTL(ttl)
	s.maybeCleanup()

	// Keep capacity eviction and publishing store-disabled socket preference in
	// the same critical section so another caller cannot admit a new key between
	// those mutations.
	s.sessionToConnMu.Lock()
	ensureBindingCapacity(s.sessionToConn, key, openAIWSStateStoreMaxEntriesPerMap)
	s.sessionToConn[key] = openAIWSSessionConnBinding{
		connID:    conn,
		expiresAt: time.Now().Add(ttl),
	}
	s.sessionToConnMu.Unlock()
}

// GetSessionConn returns live socket affinity for the group/session hash.
// Missing, expired, or blank data returns false; store-disabled ingress uses
// this preference when its request supplies no previous response ID. The exact
// deadline remains valid.
func (s *defaultOpenAIWSStateStore) GetSessionConn(groupID int64, sessionHash string) (string, bool) {
	key := openAIWSSessionTurnStateKey(groupID, sessionHash)
	if key == "" {
		return "", false
	}
	s.maybeCleanup()

	now := time.Now()
	s.sessionToConnMu.RLock()
	binding, ok := s.sessionToConn[key]
	s.sessionToConnMu.RUnlock()
	if !ok || now.After(binding.expiresAt) || strings.TrimSpace(binding.connID) == "" {
		return "", false
	}
	return binding.connID, true
}

// DeleteSessionConn removes local socket affinity for the group/session hash.
// Blank hashes and absent bindings are harmless; the operation clears a
// remembered preference while the connection pool continues to own the socket.
func (s *defaultOpenAIWSStateStore) DeleteSessionConn(groupID int64, sessionHash string) {
	key := openAIWSSessionTurnStateKey(groupID, sessionHash)
	if key == "" {
		return
	}
	// Remove the session socket preference under its owning lock; a concurrent
	// ingress lookup can retain the snapshot it read before this deletion.
	s.sessionToConnMu.Lock()
	delete(s.sessionToConn, key)
	s.sessionToConnMu.Unlock()
}

// MarkSessionInvalidEncryptedContent merges rejected ciphertext digests into a
// process-local group/session set and renews its shared lifetime. Blank
// session hashes or empty slices are ignored; nonpositive TTL selects one
// hour. Digests are trimmed, blank values are skipped, and expired or absent
// evidence starts a fresh set under the same write lock as publication.
//
// At most 512 distinct values are retained. New values beyond that capacity
// are omitted, while existing values continue to belong to the set; each
// omitted input occurrence increments the diagnostic count. The whole set's
// TTL is renewed even when all supplied strings are blank or already recorded.
// Overflow logging runs after unlocking, and callers use the store's
// independent snapshot for subsequent stripping.
func (s *defaultOpenAIWSStateStore) MarkSessionInvalidEncryptedContent(groupID int64, sessionHash string, digests []string, ttl time.Duration) {
	key := openAIWSSessionTurnStateKey(groupID, sessionHash)
	if key == "" || len(digests) == 0 {
		return
	}
	ttl = normalizeOpenAIWSTTL(ttl)
	s.maybeCleanup()

	now := time.Now()
	overflowed := 0
	s.sessionInvalidEncryptedMu.Lock()
	ensureBindingCapacity(s.sessionInvalidEncrypted, key, openAIWSStateStoreMaxEntriesPerMap)
	binding, ok := s.sessionInvalidEncrypted[key]
	// Begin a fresh set after expiration so old rejected ciphertext does not
	// reenter the renewed evidence merely because this session hash was reused.
	if !ok || now.After(binding.expiresAt) || binding.digests == nil {
		binding = openAIWSInvalidEncryptedBinding{digests: make(map[string]struct{}, len(digests))}
	}
	for _, digest := range digests {
		digest = strings.TrimSpace(digest)
		if digest == "" {
			continue
		}
		// Retain recorded evidence at capacity and omit new digest occurrences;
		// omitted ciphertext can still use ordinary recovery if upstream rejects it later.
		if len(binding.digests) >= openAIWSInvalidEncryptedDigestsPerSession {
			if _, exists := binding.digests[digest]; !exists {
				overflowed++
			}
			continue
		}
		binding.digests[digest] = struct{}{}
	}
	// Publish the merged set and one renewed deadline while holding the lock,
	// including duplicate-only or blank-only input that added no new evidence.
	binding.expiresAt = now.Add(ttl)
	s.sessionInvalidEncrypted[key] = binding
	s.sessionInvalidEncryptedMu.Unlock()

	// Readers need this map lock while making their snapshot. Release it before
	// observer work so a slow diagnostic callback does not delay those digest reads.
	if overflowed > 0 {
		s.log(
			"invalid_encrypted_lineage_capacity_overflow dropped_digests=%d capacity=%d",
			overflowed,
			openAIWSInvalidEncryptedDigestsPerSession,
		)
	}
}

// GetSessionInvalidEncryptedContentDigests returns a copied live rejection set
// for the group/session hash. Blank hashes, missing or expired bindings, and
// empty sets return nil. The copy is made under the read lock, allowing
// callers to mutate the returned map independently afterward.
func (s *defaultOpenAIWSStateStore) GetSessionInvalidEncryptedContentDigests(groupID int64, sessionHash string) map[string]struct{} {
	key := openAIWSSessionTurnStateKey(groupID, sessionHash)
	if key == "" {
		return nil
	}
	s.maybeCleanup()

	now := time.Now()
	s.sessionInvalidEncryptedMu.RLock()
	defer s.sessionInvalidEncryptedMu.RUnlock()
	binding, ok := s.sessionInvalidEncrypted[key]
	if !ok || now.After(binding.expiresAt) || len(binding.digests) == 0 {
		return nil
	}
	// Copy the set under its read lock so writers cannot change its contents
	// mid-copy and the stripping caller can then mutate an independent map.
	digests := make(map[string]struct{}, len(binding.digests))
	for digest := range binding.digests {
		digests[digest] = struct{}{}
	}
	return digests
}

// HasAnySessionInvalidEncryptedContent reports physical rejected-digest map
// presence in the default store. False lets callers skip session hashing; true
// can include expired or empty entries and requires a session-specific live
// lookup.
func (s *defaultOpenAIWSStateStore) HasAnySessionInvalidEncryptedContent() bool {
	s.sessionInvalidEncryptedMu.RLock()
	defer s.sessionInvalidEncryptedMu.RUnlock()
	return len(s.sessionInvalidEncrypted) > 0
}

// maybeCleanup elects one caller to scan expired entries after the cleanup
// interval. Nil receivers are ignored, and an atomic timestamp comparison
// makes concurrent callers skip a round already claimed by another caller. The
// elected time is published before map scanning begins.
//
// Each map is scanned separately under its own write lock, with at most 512
// entries inspected whether expired or live. Map iteration is unordered, so
// one round can leave expired entries unvisited and later rounds do not
// guarantee a particular key's cleanup time. This incremental physical removal
// is separate from read-time validity and capacity eviction.
func (s *defaultOpenAIWSStateStore) maybeCleanup() {
	if s == nil {
		return
	}
	now := time.Now()
	last := time.Unix(0, s.lastCleanupUnixNano.Load())
	if now.Sub(last) < openAIWSStateStoreCleanupInterval {
		return
	}
	// Publish the chosen cleanup start before acquiring any map lock; another
	// caller seeing the old timestamp loses this comparison and skips the same interval.
	if !s.lastCleanupUnixNano.CompareAndSwap(last.UnixNano(), now.UnixNano()) {
		return
	}

	// Scan maps under separate locks with bounded inspected counts so cleanup
	// does not hold every continuity map while processing one map's expired bindings.
	s.responseToProviderMu.Lock()
	cleanupExpiredBindings(s.responseToProvider, now, openAIWSStateStoreCleanupMaxPerMap, func(binding openAIWSProviderBinding) time.Time {
		return binding.expiresAt
	})
	s.responseToProviderMu.Unlock()

	s.responseOwnerMu.Lock()
	cleanupExpiredBindings(s.responseOwners, now, openAIWSStateStoreCleanupMaxPerMap, func(binding openAIHTTPResponseOwnerBinding) time.Time {
		return binding.expiresAt
	})
	s.responseOwnerMu.Unlock()

	s.responseToConnMu.Lock()
	cleanupExpiredBindings(s.responseToConn, now, openAIWSStateStoreCleanupMaxPerMap, func(binding openAIWSConnBinding) time.Time {
		return binding.expiresAt
	})
	s.responseToConnMu.Unlock()

	s.sessionToTurnStateMu.Lock()
	cleanupExpiredBindings(s.sessionToTurnState, now, openAIWSStateStoreCleanupMaxPerMap, func(binding openAIWSTurnStateBinding) time.Time {
		return binding.expiresAt
	})
	s.sessionToTurnStateMu.Unlock()

	s.sessionToConnMu.Lock()
	cleanupExpiredBindings(s.sessionToConn, now, openAIWSStateStoreCleanupMaxPerMap, func(binding openAIWSSessionConnBinding) time.Time {
		return binding.expiresAt
	})
	s.sessionToConnMu.Unlock()

	s.sessionInvalidEncryptedMu.Lock()
	cleanupExpiredBindings(s.sessionInvalidEncrypted, now, openAIWSStateStoreCleanupMaxPerMap, func(binding openAIWSInvalidEncryptedBinding) time.Time {
		return binding.expiresAt
	})
	s.sessionInvalidEncryptedMu.Unlock()
}

// cleanupExpiredBindings removes entries whose deadlines precede now while
// inspecting at most maxScan keys. The caller holds the map's write lock and
// supplies an expiry reader for its binding type. Nil or empty maps and
// nonpositive budgets skip scanning. Every visited entry consumes the budget,
// including live entries and deadlines equal to now; map iteration is unordered.
func cleanupExpiredBindings[T any](bindings map[string]T, now time.Time, maxScan int, expiresAt func(T) time.Time) {
	if len(bindings) == 0 || maxScan <= 0 {
		return
	}
	scanned := 0
	// Count every inspected binding so a map with many live values holds its
	// owning lock for the same bounded number of visits as an expired map.
	for key, binding := range bindings {
		if now.After(expiresAt(binding)) {
			delete(bindings, key)
		}
		scanned++
		if scanned >= maxScan {
			break
		}
	}
}

// ensureBindingCapacity removes one arbitrary existing key before admitting a
// new key into a full map. The caller holds the owning write lock and performs
// its insertion before unlocking. Nonpositive capacity disables eviction, and
// updating an existing key retains every other binding.
//
// The helper deletes at most one entry and does not prefer expired or older
// values. Callers keep normal admission at or below maxEntries; an already
// oversized map is not reduced to that limit by one call. A live continuation
// binding can be evicted, leaving its later consumer to use ordinary lookup
// recovery.
func ensureBindingCapacity[T any](bindings map[string]T, incomingKey string, maxEntries int) {
	if len(bindings) < maxEntries || maxEntries <= 0 {
		return
	}
	if _, exists := bindings[incomingKey]; exists {
		return
	}
	// Admission needs one slot while the caller holds the map lock. Arbitrary
	// eviction can remove live affinity, which later lookup treats as missing evidence.
	for key := range bindings {
		delete(bindings, key)
		return
	}
}

// normalizeOpenAIWSResponseID trims surrounding whitespace from the supplied
// response identifier.
func normalizeOpenAIWSResponseID(responseID string) string {
	return strings.TrimSpace(responseID)
}

// openAIWSResponseProviderCacheKey labels a SHA-256 response-ID digest for
// shared provider lookup.
func openAIWSResponseProviderCacheKey(responseID string) string {
	sum := sha256.Sum256([]byte(responseID))
	return openAIWSResponseProviderCachePrefix + hex.EncodeToString(sum[:])
}

// openAIHTTPResponseOwnerCacheKey labels the response-ID digest with the
// supplied owner component prefix.
func openAIHTTPResponseOwnerCacheKey(prefix, responseID string) string {
	sum := sha256.Sum256([]byte(responseID))
	return prefix + hex.EncodeToString(sum[:])
}

// openAIWSResponseProviderMapKey combines the decimal group with response ID
// for local lookup.
func openAIWSResponseProviderMapKey(groupID int64, responseID string) string {
	return fmt.Sprintf("%d:%s", groupID, responseID)
}

// normalizeOpenAIWSTTL returns a positive supplied lifetime or defaults to one
// hour. Bind operations call it before publishing their local deadlines and
// passing the same duration to any optional shared persistence.
func normalizeOpenAIWSTTL(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return time.Hour
	}
	return ttl
}

// openAIWSSessionTurnStateKey joins the group with trimmed session hash for
// local session maps. Whitespace-only hashes return empty; group IDs are used
// as supplied without checking whether the group currently exists.
func openAIWSSessionTurnStateKey(groupID int64, sessionHash string) string {
	hash := strings.TrimSpace(sessionHash)
	if hash == "" {
		return ""
	}
	return fmt.Sprintf("%d:%s", groupID, hash)
}

// withOpenAIWSStateStoreRedisTimeout derives a shared-cache context with a
// deadline at most three seconds away. Nil parents use context.Background;
// cancellation and earlier parent deadlines apply to the derived context.
// Callers cancel it after their shared storage sequence finishes.
//
// Context expiry does not guarantee that a cache operation has returned. The
// production Redis client leaves ContextTimeoutEnabled disabled, so command
// socket I/O follows the configured read/write timeouts. Connection acquisition
// and retry waits still receive the caller context.
func withOpenAIWSStateStoreRedisTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, openAIWSStateStoreRedisTimeout)
}

// log invokes the configured observer synchronously with a message and values.
// A nil observer skips reporting; its caller releases the invalid-digest lock
// before logging so observer work does not hold that map's access lock.
func (s *defaultOpenAIWSStateStore) log(message string, args ...any) {
	if s.observe != nil {
		s.observe(message, args...)
	}
}
