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

const (
	openAIWSResponseProviderCachePrefix = "openai:response:"
	openAIHTTPResponseOwnerUserPrefix   = "openai:http-response-owner:user:"
	openAIHTTPResponseOwnerKeyPrefix    = "openai:http-response-owner:key:"
	openAIWSStateStoreCleanupInterval   = time.Minute
	openAIWSStateStoreCleanupMaxPerMap  = 512
	openAIWSStateStoreMaxEntriesPerMap  = 65536
	openAIWSStateStoreRedisTimeout      = 3 * time.Second

	// openAIWSInvalidEncryptedDigestsPerSession bounds the stored digest set for each session.
	// New digests beyond the limit use normal recovery if the upstream rejects that ciphertext again.
	openAIWSInvalidEncryptedDigestsPerSession = 512

	// StateStoreRedisTimeout is the shared budget for session storage and preemption operations.
	StateStoreRedisTimeout = openAIWSStateStoreRedisTimeout
)

type openAIWSProviderBinding struct {
	providerID int64
	expiresAt  time.Time
}

type openAIHTTPResponseOwnerBinding struct {
	userID    int64
	apiKeyID  int64
	expiresAt time.Time
}

type openAIWSConnBinding struct {
	connID    string
	expiresAt time.Time
}

type openAIWSTurnStateBinding struct {
	turnState string
	expiresAt time.Time
}

type openAIWSSessionConnBinding struct {
	connID    string
	expiresAt time.Time
}

// openAIWSInvalidEncryptedBinding records digests of encrypted_content rejected
// by the upstream for a session (invalid_encrypted_content lineage).
type openAIWSInvalidEncryptedBinding struct {
	digests   map[string]struct{}
	expiresAt time.Time
}

// OpenAIWSStateStore maps WebSocket responses to providers and connections.
// Provider lookup checks the local cache before Redis to select a continuation provider.
// Connection bindings are process-local because connection state belongs to this instance.
type OpenAIWSStateStore interface {
	BindResponseProvider(ctx context.Context, groupID int64, responseID string, providerID int64, ttl time.Duration) error
	GetResponseProvider(ctx context.Context, groupID int64, responseID string) (int64, error)
	DeleteResponseProvider(ctx context.Context, groupID int64, responseID string) error
	BindHTTPResponseOwner(ctx context.Context, groupID int64, responseID string, userID, apiKeyID int64, ttl time.Duration) error
	GetHTTPResponseOwner(ctx context.Context, groupID int64, responseID string) (userID, apiKeyID int64, found bool, err error)

	BindResponseConn(responseID, connID string, ttl time.Duration)
	GetResponseConn(responseID string) (string, bool)
	DeleteResponseConn(responseID string)

	BindSessionTurnState(groupID int64, sessionHash, turnState string, ttl time.Duration)
	GetSessionTurnState(groupID int64, sessionHash string) (string, bool)
	DeleteSessionTurnState(groupID int64, sessionHash string)

	BindSessionConn(groupID int64, sessionHash, connID string, ttl time.Duration)
	GetSessionConn(groupID int64, sessionHash string) (string, bool)
	DeleteSessionConn(groupID int64, sessionHash string)

	// MarkSessionInvalidEncryptedContent records rejected ciphertext digests for stripping on later turns.
	MarkSessionInvalidEncryptedContent(groupID int64, sessionHash string, digests []string, ttl time.Duration)
	GetSessionInvalidEncryptedContentDigests(groupID int64, sessionHash string) map[string]struct{}
	// HasAnySessionInvalidEncryptedContent reports whether any session has recorded digests.
	// A false result lets callers skip session hashing and digest matching.
	HasAnySessionInvalidEncryptedContent() bool
}

type defaultOpenAIWSStateStore struct {
	observe func(string, ...any)
	cache   GatewayCache

	responseToProviderMu sync.RWMutex
	responseToProvider   map[string]openAIWSProviderBinding
	responseOwnerMu      sync.RWMutex
	responseOwners       map[string]openAIHTTPResponseOwnerBinding
	responseToConnMu     sync.RWMutex
	responseToConn       map[string]openAIWSConnBinding
	sessionToTurnStateMu sync.RWMutex
	sessionToTurnState   map[string]openAIWSTurnStateBinding
	sessionToConnMu      sync.RWMutex
	sessionToConn        map[string]openAIWSSessionConnBinding

	sessionInvalidEncryptedMu sync.RWMutex
	sessionInvalidEncrypted   map[string]openAIWSInvalidEncryptedBinding

	lastCleanupUnixNano atomic.Int64
}

// NewOpenAIWSStateStore creates the in-process state maps and attaches an optional shared cache.
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

func (s *defaultOpenAIWSStateStore) BindHTTPResponseOwner(ctx context.Context, groupID int64, responseID string, userID, apiKeyID int64, ttl time.Duration) error {
	id := normalizeOpenAIWSResponseID(responseID)
	if id == "" || userID <= 0 || apiKeyID <= 0 {
		return nil
	}
	ttl = normalizeOpenAIWSTTL(ttl)
	s.maybeCleanup()

	mapKey := openAIWSResponseProviderMapKey(groupID, id)
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
	if err := s.cache.SetSessionProviderID(cacheCtx, groupID, openAIHTTPResponseOwnerCacheKey(openAIHTTPResponseOwnerUserPrefix, id), userID, ttl); err != nil {
		return err
	}
	return s.cache.SetSessionProviderID(cacheCtx, groupID, openAIHTTPResponseOwnerCacheKey(openAIHTTPResponseOwnerKeyPrefix, id), apiKeyID, ttl)
}

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
	userID, err := s.cache.GetSessionProviderID(cacheCtx, groupID, openAIHTTPResponseOwnerCacheKey(openAIHTTPResponseOwnerUserPrefix, id))
	if err != nil || userID <= 0 {
		return 0, 0, false, err
	}
	apiKeyID, err := s.cache.GetSessionProviderID(cacheCtx, groupID, openAIHTTPResponseOwnerCacheKey(openAIHTTPResponseOwnerKeyPrefix, id))
	if err != nil || apiKeyID <= 0 {
		return 0, 0, false, err
	}

	s.responseOwnerMu.Lock()
	ensureBindingCapacity(s.responseOwners, mapKey, openAIWSStateStoreMaxEntriesPerMap)
	s.responseOwners[mapKey] = openAIHTTPResponseOwnerBinding{
		userID: userID, apiKeyID: apiKeyID, expiresAt: now.Add(time.Minute),
	}
	s.responseOwnerMu.Unlock()
	return userID, apiKeyID, true, nil
}

func (s *defaultOpenAIWSStateStore) BindResponseProvider(ctx context.Context, groupID int64, responseID string, providerID int64, ttl time.Duration) error {
	id := normalizeOpenAIWSResponseID(responseID)
	if id == "" || providerID <= 0 {
		return nil
	}
	ttl = normalizeOpenAIWSTTL(ttl)
	s.maybeCleanup()

	expiresAt := time.Now().Add(ttl)
	mapKey := openAIWSResponseProviderMapKey(groupID, id)
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

func cleanupExpiredHTTPResponseOwnerBindings(bindings map[string]openAIHTTPResponseOwnerBinding, now time.Time, maxScan int) {
	if len(bindings) == 0 || maxScan <= 0 {
		return
	}
	scanned := 0
	for key, binding := range bindings {
		if now.After(binding.expiresAt) {
			delete(bindings, key)
		}
		scanned++
		if scanned >= maxScan {
			break
		}
	}
}

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
		// Treat cache read failures as misses so provider selection can continue.
		return 0, nil
	}
	return providerID, nil
}

func (s *defaultOpenAIWSStateStore) DeleteResponseProvider(ctx context.Context, groupID int64, responseID string) error {
	id := normalizeOpenAIWSResponseID(responseID)
	if id == "" {
		return nil
	}
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

func (s *defaultOpenAIWSStateStore) BindResponseConn(responseID, connID string, ttl time.Duration) {
	id := normalizeOpenAIWSResponseID(responseID)
	conn := strings.TrimSpace(connID)
	if id == "" || conn == "" {
		return
	}
	ttl = normalizeOpenAIWSTTL(ttl)
	s.maybeCleanup()

	s.responseToConnMu.Lock()
	ensureBindingCapacity(s.responseToConn, id, openAIWSStateStoreMaxEntriesPerMap)
	s.responseToConn[id] = openAIWSConnBinding{
		connID:    conn,
		expiresAt: time.Now().Add(ttl),
	}
	s.responseToConnMu.Unlock()
}

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

func (s *defaultOpenAIWSStateStore) DeleteResponseConn(responseID string) {
	id := normalizeOpenAIWSResponseID(responseID)
	if id == "" {
		return
	}
	s.responseToConnMu.Lock()
	delete(s.responseToConn, id)
	s.responseToConnMu.Unlock()
}

func (s *defaultOpenAIWSStateStore) BindSessionTurnState(groupID int64, sessionHash, turnState string, ttl time.Duration) {
	key := openAIWSSessionTurnStateKey(groupID, sessionHash)
	state := strings.TrimSpace(turnState)
	if key == "" || state == "" {
		return
	}
	ttl = normalizeOpenAIWSTTL(ttl)
	s.maybeCleanup()

	s.sessionToTurnStateMu.Lock()
	ensureBindingCapacity(s.sessionToTurnState, key, openAIWSStateStoreMaxEntriesPerMap)
	s.sessionToTurnState[key] = openAIWSTurnStateBinding{
		turnState: state,
		expiresAt: time.Now().Add(ttl),
	}
	s.sessionToTurnStateMu.Unlock()
}

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

func (s *defaultOpenAIWSStateStore) DeleteSessionTurnState(groupID int64, sessionHash string) {
	key := openAIWSSessionTurnStateKey(groupID, sessionHash)
	if key == "" {
		return
	}
	s.sessionToTurnStateMu.Lock()
	delete(s.sessionToTurnState, key)
	s.sessionToTurnStateMu.Unlock()
}

func (s *defaultOpenAIWSStateStore) BindSessionConn(groupID int64, sessionHash, connID string, ttl time.Duration) {
	key := openAIWSSessionTurnStateKey(groupID, sessionHash)
	conn := strings.TrimSpace(connID)
	if key == "" || conn == "" {
		return
	}
	ttl = normalizeOpenAIWSTTL(ttl)
	s.maybeCleanup()

	s.sessionToConnMu.Lock()
	ensureBindingCapacity(s.sessionToConn, key, openAIWSStateStoreMaxEntriesPerMap)
	s.sessionToConn[key] = openAIWSSessionConnBinding{
		connID:    conn,
		expiresAt: time.Now().Add(ttl),
	}
	s.sessionToConnMu.Unlock()
}

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

func (s *defaultOpenAIWSStateStore) DeleteSessionConn(groupID int64, sessionHash string) {
	key := openAIWSSessionTurnStateKey(groupID, sessionHash)
	if key == "" {
		return
	}
	s.sessionToConnMu.Lock()
	delete(s.sessionToConn, key)
	s.sessionToConnMu.Unlock()
}

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
	if !ok || now.After(binding.expiresAt) || binding.digests == nil {
		binding = openAIWSInvalidEncryptedBinding{digests: make(map[string]struct{}, len(digests))}
	}
	for _, digest := range digests {
		digest = strings.TrimSpace(digest)
		if digest == "" {
			continue
		}
		if len(binding.digests) >= openAIWSInvalidEncryptedDigestsPerSession {
			if _, exists := binding.digests[digest]; !exists {
				overflowed++
			}
			continue
		}
		binding.digests[digest] = struct{}{}
	}
	binding.expiresAt = now.Add(ttl)
	s.sessionInvalidEncrypted[key] = binding
	s.sessionInvalidEncryptedMu.Unlock()

	// Hot-path digest readers share this lock; keep logging I/O outside it.
	if overflowed > 0 {
		s.log(
			"invalid_encrypted_lineage_capacity_overflow dropped_digests=%d capacity=%d",
			overflowed,
			openAIWSInvalidEncryptedDigestsPerSession,
		)
	}
}

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
	digests := make(map[string]struct{}, len(binding.digests))
	for digest := range binding.digests {
		digests[digest] = struct{}{}
	}
	return digests
}

func (s *defaultOpenAIWSStateStore) HasAnySessionInvalidEncryptedContent() bool {
	s.sessionInvalidEncryptedMu.RLock()
	defer s.sessionInvalidEncryptedMu.RUnlock()
	return len(s.sessionInvalidEncrypted) > 0
}

func (s *defaultOpenAIWSStateStore) maybeCleanup() {
	if s == nil {
		return
	}
	now := time.Now()
	last := time.Unix(0, s.lastCleanupUnixNano.Load())
	if now.Sub(last) < openAIWSStateStoreCleanupInterval {
		return
	}
	if !s.lastCleanupUnixNano.CompareAndSwap(last.UnixNano(), now.UnixNano()) {
		return
	}

	// Bound each cleanup scan to limit lock time as caches grow.
	s.responseToProviderMu.Lock()
	cleanupExpiredProviderBindings(s.responseToProvider, now, openAIWSStateStoreCleanupMaxPerMap)
	s.responseToProviderMu.Unlock()

	s.responseOwnerMu.Lock()
	cleanupExpiredHTTPResponseOwnerBindings(s.responseOwners, now, openAIWSStateStoreCleanupMaxPerMap)
	s.responseOwnerMu.Unlock()

	s.responseToConnMu.Lock()
	cleanupExpiredConnBindings(s.responseToConn, now, openAIWSStateStoreCleanupMaxPerMap)
	s.responseToConnMu.Unlock()

	s.sessionToTurnStateMu.Lock()
	cleanupExpiredTurnStateBindings(s.sessionToTurnState, now, openAIWSStateStoreCleanupMaxPerMap)
	s.sessionToTurnStateMu.Unlock()

	s.sessionToConnMu.Lock()
	cleanupExpiredSessionConnBindings(s.sessionToConn, now, openAIWSStateStoreCleanupMaxPerMap)
	s.sessionToConnMu.Unlock()

	s.sessionInvalidEncryptedMu.Lock()
	cleanupExpiredInvalidEncryptedBindings(s.sessionInvalidEncrypted, now, openAIWSStateStoreCleanupMaxPerMap)
	s.sessionInvalidEncryptedMu.Unlock()
}

func cleanupExpiredInvalidEncryptedBindings(bindings map[string]openAIWSInvalidEncryptedBinding, now time.Time, maxScan int) {
	if len(bindings) == 0 || maxScan <= 0 {
		return
	}
	scanned := 0
	for key, binding := range bindings {
		if now.After(binding.expiresAt) {
			delete(bindings, key)
		}
		scanned++
		if scanned >= maxScan {
			break
		}
	}
}

func cleanupExpiredProviderBindings(bindings map[string]openAIWSProviderBinding, now time.Time, maxScan int) {
	if len(bindings) == 0 || maxScan <= 0 {
		return
	}
	scanned := 0
	for key, binding := range bindings {
		if now.After(binding.expiresAt) {
			delete(bindings, key)
		}
		scanned++
		if scanned >= maxScan {
			break
		}
	}
}

func cleanupExpiredConnBindings(bindings map[string]openAIWSConnBinding, now time.Time, maxScan int) {
	if len(bindings) == 0 || maxScan <= 0 {
		return
	}
	scanned := 0
	for key, binding := range bindings {
		if now.After(binding.expiresAt) {
			delete(bindings, key)
		}
		scanned++
		if scanned >= maxScan {
			break
		}
	}
}

func cleanupExpiredTurnStateBindings(bindings map[string]openAIWSTurnStateBinding, now time.Time, maxScan int) {
	if len(bindings) == 0 || maxScan <= 0 {
		return
	}
	scanned := 0
	for key, binding := range bindings {
		if now.After(binding.expiresAt) {
			delete(bindings, key)
		}
		scanned++
		if scanned >= maxScan {
			break
		}
	}
}

func cleanupExpiredSessionConnBindings(bindings map[string]openAIWSSessionConnBinding, now time.Time, maxScan int) {
	if len(bindings) == 0 || maxScan <= 0 {
		return
	}
	scanned := 0
	for key, binding := range bindings {
		if now.After(binding.expiresAt) {
			delete(bindings, key)
		}
		scanned++
		if scanned >= maxScan {
			break
		}
	}
}

func ensureBindingCapacity[T any](bindings map[string]T, incomingKey string, maxEntries int) {
	if len(bindings) < maxEntries || maxEntries <= 0 {
		return
	}
	if _, exists := bindings[incomingKey]; exists {
		return
	}
	// Evict an arbitrary entry to keep memory bounded.
	for key := range bindings {
		delete(bindings, key)
		return
	}
}

func normalizeOpenAIWSResponseID(responseID string) string {
	return strings.TrimSpace(responseID)
}

func openAIWSResponseProviderCacheKey(responseID string) string {
	sum := sha256.Sum256([]byte(responseID))
	return openAIWSResponseProviderCachePrefix + hex.EncodeToString(sum[:])
}

func openAIHTTPResponseOwnerCacheKey(prefix, responseID string) string {
	sum := sha256.Sum256([]byte(responseID))
	return prefix + hex.EncodeToString(sum[:])
}

// openAIWSResponseProviderMapKey scopes local entries by group, matching Redis isolation.
func openAIWSResponseProviderMapKey(groupID int64, responseID string) string {
	return fmt.Sprintf("%d:%s", groupID, responseID)
}

func normalizeOpenAIWSTTL(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return time.Hour
	}
	return ttl
}

func openAIWSSessionTurnStateKey(groupID int64, sessionHash string) string {
	hash := strings.TrimSpace(sessionHash)
	if hash == "" {
		return ""
	}
	return fmt.Sprintf("%d:%s", groupID, hash)
}

func withOpenAIWSStateStoreRedisTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, openAIWSStateStoreRedisTimeout)
}

func (s *defaultOpenAIWSStateStore) log(message string, args ...any) {
	if s.observe != nil {
		s.observe(message, args...)
	}
}
