package scheduler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	openAIWSIngressLeaseTTL             = 60 * time.Second
	openAIWSIngressLeaseRefreshInterval = 20 * time.Second
	openAIWSIngressLeaseOperationTO     = 2 * time.Second

	// Extra waiting slots beyond the user's concurrency limit.
	defaultExtraWaitSlots = 20

	defaultProviderLoadBatchCacheTTL = 200 * time.Millisecond
	providerLoadBatchFetchTimeout    = 3 * time.Second
	maxProviderLoadBatchCacheEntries = 256
	apiKeyConcurrencyFetchTimeout    = 3 * time.Second
	apiKeySlotTrackTimeout           = 2 * time.Second
)

var (
	ErrOpenAIWSIngressLeaseLost = errors.New("openai websocket ingress lease lost")

	requestIDPrefix  = initRequestIDPrefix()
	requestIDCounter atomic.Uint64
)

// ConcurrencyCache stores request slots and bounded waiting counts.
// The Redis implementation uses timestamped sorted-set members to expire slots.
type ConcurrencyCache interface {
	// Provider slots.
	// Redis key: concurrency:provider:{providerID}; sorted-set members are request IDs.
	AcquireProviderSlot(ctx context.Context, providerID int64, maxConcurrency int, requestID string) (bool, error)
	ReleaseProviderSlot(ctx context.Context, providerID int64, requestID string) error
	GetProviderConcurrency(ctx context.Context, providerID int64) (int, error)
	GetProviderConcurrencyBatch(ctx context.Context, providerIDs []int64) (map[int64]int, error)

	// Provider-scoped waiting queues.
	IncrementProviderWaitCount(ctx context.Context, providerID int64, maxWait int) (bool, error)
	DecrementProviderWaitCount(ctx context.Context, providerID int64) error
	GetProviderWaitingCount(ctx context.Context, providerID int64) (int, error)

	// User slots.
	// Redis key: concurrency:user:{userID}; sorted-set members are request IDs.
	AcquireUserSlot(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error)
	ReleaseUserSlot(ctx context.Context, userID int64, requestID string) error
	GetUserConcurrency(ctx context.Context, userID int64) (int, error)

	// User waiting counts refresh their TTL on enqueue; long waits require renewal.
	IncrementWaitCount(ctx context.Context, userID int64, maxWait int) (bool, error)
	DecrementWaitCount(ctx context.Context, userID int64) error

	// Read-only batch load queries.
	GetProvidersLoadBatch(ctx context.Context, providers []ProviderWithConcurrency) (map[int64]*ProviderLoadInfo, error)
	GetUsersLoadBatch(ctx context.Context, users []UserWithConcurrency) (map[int64]*UserLoadInfo, error)

	// Background cleanup of expired provider slots.
	CleanupExpiredProviderSlots(ctx context.Context, providerID int64) error
	CleanupExpiredProviderSlotKeys(ctx context.Context) error

	// Startup cleanup of slots and waiting counts left by older processes.
	CleanupStaleProcessSlots(ctx context.Context, activeRequestPrefix string) error
}

// APIKeyConcurrencyCache tracks active requests for API key statistics.
type APIKeyConcurrencyCache interface {
	TrackAPIKeySlot(ctx context.Context, apiKeyID int64, requestID string) error
	ReleaseAPIKeySlot(ctx context.Context, apiKeyID int64, requestID string) error
	GetAPIKeyConcurrencyBatch(ctx context.Context, apiKeyIDs []int64) (map[int64]int, error)
}

// OpenAIWSIngressLeaseCache limits active client WebSocket sessions using short distributed leases.
// Ingress leases have a separate namespace, so idle connections do not occupy request-turn slots.
type OpenAIWSIngressLeaseCache interface {
	AcquireOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, maxConnections int, leaseID string) (bool, error)
	RefreshOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, leaseID string) (bool, error)
	ReleaseOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, leaseID string) error
}

// OpenAIWSIngressLease renews an ingress lease and cancels its context when ownership is lost or unconfirmed for a
// full TTL.
// Handlers must call Release on every exit path to return capacity promptly.
type OpenAIWSIngressLease struct {
	diagnostics Diagnostics
	ctx         context.Context
	cancel      context.CancelCauseFunc
	cache       OpenAIWSIngressLeaseCache
	apiKeyID    int64
	leaseID     string

	stopOnce      sync.Once
	stopCh        chan struct{}
	refreshDone   chan struct{}
	finishRuntime func()
}

// ConcurrencyService manages provider and user request slots, waiting counts, and connection leases.
type ConcurrencyService struct {
	diagnostics Diagnostics
	runtime     WorkerRuntime

	cache ConcurrencyCache

	providerLoadCacheTTL atomic.Int64
	providerLoadCacheMu  sync.RWMutex
	providerLoadCache    map[string]cachedProviderLoadBatch
	providerLoadGroup    singleflight.Group
}

type cachedProviderLoadBatch struct {
	loadMap   map[int64]*ProviderLoadInfo
	expiresAt time.Time
}

// AcquireResult reports slot acquisition and supplies cleanup when acquired.
type AcquireResult struct {
	Acquired    bool
	ReleaseFunc func() // Call after an acquired request finishes; nil when acquisition was denied.
}

type ProviderWithConcurrency struct {
	ID             int64
	MaxConcurrency int
}

type UserWithConcurrency struct {
	ID             int64
	MaxConcurrency int
}

type ProviderLoadInfo struct {
	ProviderID         int64
	CurrentConcurrency int
	WaitingCount       int
	LoadRate           int // 0-100+ (percent)
}

type UserLoadInfo struct {
	UserID             int64
	CurrentConcurrency int
	WaitingCount       int
	LoadRate           int // 0-100+ (percent)
}

func (l *OpenAIWSIngressLease) Context() context.Context {
	if l == nil || l.ctx == nil {
		return context.Background()
	}
	return l.ctx
}

func (l *OpenAIWSIngressLease) Release() {
	if l == nil {
		return
	}
	l.stopOnce.Do(func() {
		if l.finishRuntime != nil {
			defer l.finishRuntime()
		}
		if l.stopCh != nil {
			close(l.stopCh)
		}
		if l.cancel != nil {
			l.cancel(nil)
		}
		if l.refreshDone != nil {
			<-l.refreshDone
		}
		if l.cache == nil || l.apiKeyID <= 0 || l.leaseID == "" {
			return
		}
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), openAIWSIngressLeaseOperationTO)
		defer releaseCancel()
		if err := l.cache.ReleaseOpenAIWSIngressLease(releaseCtx, l.apiKeyID, l.leaseID); err != nil {
			l.diagnostics.event("warn", "openai_ws_ingress_lease_release_failed",
				"api_key_id", l.apiKeyID,
				"error", err,
			)
		}
	})
}

func (l *OpenAIWSIngressLease) refreshLoop() {
	defer func() {
		if l != nil && l.refreshDone != nil {
			close(l.refreshDone)
		}
	}()
	if l == nil || l.cache == nil {
		return
	}
	ticker := time.NewTicker(openAIWSIngressLeaseRefreshInterval)
	defer ticker.Stop()
	lastConfirmedAt := time.Now()
	for {
		select {
		case <-l.ctx.Done():
			return
		case <-l.stopCh:
			return
		case <-ticker.C:
			var lost bool
			lastConfirmedAt, lost = l.refresh(lastConfirmedAt)
			if lost {
				l.cancel(ErrOpenAIWSIngressLeaseLost)
				return
			}
		}
	}
}

// refresh checks ownership. A missing member loses the lease immediately; Redis errors are tolerated for at most
// one TTL.
func (l *OpenAIWSIngressLease) refresh(lastConfirmedAt time.Time) (time.Time, bool) {
	refreshCtx, refreshCancel := context.WithTimeout(context.Background(), openAIWSIngressLeaseOperationTO)
	owned, err := l.cache.RefreshOpenAIWSIngressLease(refreshCtx, l.apiKeyID, l.leaseID)
	refreshCancel()
	if err == nil && owned {
		return time.Now(), false
	}
	if err == nil {
		err = ErrOpenAIWSIngressLeaseLost
	}
	elapsed := time.Since(lastConfirmedAt)
	l.diagnostics.event("warn", "openai_ws_ingress_lease_refresh_failed",
		"api_key_id", l.apiKeyID,
		"unconfirmed_for", elapsed,
		"error", err,
	)
	if errors.Is(err, ErrOpenAIWSIngressLeaseLost) || elapsed >= openAIWSIngressLeaseTTL {
		l.diagnostics.event("error", "openai_ws_ingress_lease_lost",
			"api_key_id", l.apiKeyID,
			"unconfirmed_for", elapsed,
			"error", err,
		)
		return lastConfirmedAt, true
	}
	return lastConfirmedAt, false
}

func initRequestIDPrefix() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err == nil {
		return "r" + strconv.FormatUint(binary.BigEndian.Uint64(b), 36)
	}
	fallback := uint64(time.Now().UnixNano()) ^ (uint64(os.Getpid()) << 16)
	return "r" + strconv.FormatUint(fallback, 36)
}

func RequestIDPrefix() string {
	return requestIDPrefix
}

func GenerateRequestID() string {
	seq := requestIDCounter.Add(1)
	return requestIDPrefix + "-" + strconv.FormatUint(seq, 36)
}

// NewConcurrencyService creates concurrency control with the default short provider-load cache.
func NewConcurrencyService(cache ConcurrencyCache, options ...Diagnostics) *ConcurrencyService {
	var diagnostics Diagnostics
	if len(options) > 0 {
		diagnostics = options[0]
	}

	svc := &ConcurrencyService{
		cache:             cache,
		diagnostics:       diagnostics,
		providerLoadCache: make(map[string]cachedProviderLoadBatch),
	}
	svc.SetProviderLoadBatchCacheTTL(defaultProviderLoadBatchCacheTTL)
	return svc
}

func (s *ConcurrencyService) CleanupStaleProcessSlots(ctx context.Context) error {
	if s == nil || s.cache == nil {
		return nil
	}
	return s.cache.CleanupStaleProcessSlots(ctx, RequestIDPrefix())
}

// AcquireOpenAIWSIngressLease reserves an API key's ingress connection; a nonpositive limit disables the check.
func (s *ConcurrencyService) AcquireOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, maxConnections int) (*OpenAIWSIngressLease, bool, error) {
	if maxConnections <= 0 {
		return nil, true, nil
	}
	if s == nil || s.cache == nil || apiKeyID <= 0 {
		return nil, false, errors.New("openai websocket ingress lease cache is unavailable")
	}
	cache, ok := s.cache.(OpenAIWSIngressLeaseCache)
	if !ok {
		return nil, false, errors.New("openai websocket ingress lease cache is unsupported")
	}
	leaseID := GenerateRequestID()
	baseCtx, done, enterErr := s.runtime.Enter(context.WithoutCancel(nonNilContext(ctx)), fmt.Sprintf("ws-ingress:%d", apiKeyID))
	if enterErr != nil {
		return nil, false, enterErr
	}
	acquireCtx, acquireCancel := context.WithTimeout(baseCtx, openAIWSIngressLeaseOperationTO)
	acquired, err := cache.AcquireOpenAIWSIngressLease(acquireCtx, apiKeyID, maxConnections, leaseID)
	acquireCancel()
	if err != nil || !acquired {
		done()
		return nil, acquired, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	leaseCtx, leaseCancel := context.WithCancelCause(ctx)
	lease := &OpenAIWSIngressLease{
		diagnostics: s.diagnostics,
		ctx:         leaseCtx,
		cancel:      leaseCancel,
		cache:       cache,
		apiKeyID:    apiKeyID,
		leaseID:     leaseID,
		stopCh:      make(chan struct{}),
		refreshDone: make(chan struct{}),
	}
	stop := context.AfterFunc(baseCtx, func() { leaseCancel(ErrRuntimeStopped) })
	lease.finishRuntime = func() { stop(); done() }
	go lease.refreshLoop()
	return lease, true, nil
}

// SetProviderLoadBatchCacheTTL sets the provider-load cache lifetime; nonpositive values disable and clear it.
func (s *ConcurrencyService) SetProviderLoadBatchCacheTTL(ttl time.Duration) {
	if s == nil {
		return
	}
	s.providerLoadCacheTTL.Store(int64(ttl))
	if ttl <= 0 {
		s.providerLoadCacheMu.Lock()
		s.providerLoadCache = make(map[string]cachedProviderLoadBatch)
		s.providerLoadCacheMu.Unlock()
	}
}

// acquireProviderSlot attempts to acquire a provider request slot.
// At capacity it returns Acquired=false; successful callers release the slot when the request finishes.
func (s *ConcurrencyService) acquireProviderSlot(ctx context.Context, providerID int64, maxConcurrency int) (*AcquireResult, error) {
	// A nonpositive limit permits the request without acquiring a slot.
	if maxConcurrency <= 0 {
		return &AcquireResult{
			Acquired:    true,
			ReleaseFunc: func() {}, // no-op
		}, nil
	}

	requestID := GenerateRequestID()

	acquired, err := s.cache.AcquireProviderSlot(ctx, providerID, maxConcurrency, requestID)
	if err != nil {
		return nil, err
	}

	if acquired {
		return &AcquireResult{
			Acquired: true,
			ReleaseFunc: func() {
				bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := s.cache.ReleaseProviderSlot(bgCtx, providerID, requestID); err != nil {
					s.diagnostics.printf("service.concurrency", "Warning: failed to release provider slot for %d (req=%s): %v", providerID, requestID, err)
				}
			},
		}, nil
	}

	return &AcquireResult{
		Acquired:    false,
		ReleaseFunc: nil,
	}, nil
}

// acquireUserSlot returns Acquired=false when the user's concurrency limit is reached.
// Successful callers release the slot when the request finishes.
func (s *ConcurrencyService) acquireUserSlot(ctx context.Context, userID int64, maxConcurrency int) (*AcquireResult, error) {
	// A nonpositive limit permits the request without acquiring a slot.
	if maxConcurrency <= 0 {
		return &AcquireResult{
			Acquired:    true,
			ReleaseFunc: func() {}, // no-op
		}, nil
	}

	requestID := GenerateRequestID()

	acquired, err := s.cache.AcquireUserSlot(ctx, userID, maxConcurrency, requestID)
	if err != nil {
		return nil, err
	}

	if acquired {
		return &AcquireResult{
			Acquired: true,
			ReleaseFunc: func() {
				bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := s.cache.ReleaseUserSlot(bgCtx, userID, requestID); err != nil {
					s.diagnostics.printf("service.concurrency", "Warning: failed to release user slot for %d (req=%s): %v", userID, requestID, err)
				}
			},
		}, nil
	}

	return &AcquireResult{
		Acquired:    false,
		ReleaseFunc: nil,
	}, nil
}

// trackAPIKeySlot records an active request for statistics.
// Statistics fail open: Redis errors are logged and return a no-op release function.
func (s *ConcurrencyService) trackAPIKeySlot(ctx context.Context, apiKeyID int64) func() {
	if s == nil || s.cache == nil || apiKeyID <= 0 {
		return func() {}
	}
	cache, ok := s.cache.(APIKeyConcurrencyCache)
	if !ok {
		return func() {}
	}

	requestID := GenerateRequestID()
	baseCtx := context.Background()
	if ctx != nil {
		baseCtx = ctx
	}
	trackCtx, cancel := context.WithTimeout(baseCtx, apiKeySlotTrackTimeout)
	err := cache.TrackAPIKeySlot(trackCtx, apiKeyID, requestID)
	cancel()
	if err != nil {
		s.diagnostics.printf("service.concurrency", "Warning: failed to track api key slot for %d (req=%s): %v", apiKeyID, requestID, err)
		return func() {}
	}

	return func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := cache.ReleaseAPIKeySlot(bgCtx, apiKeyID, requestID); err != nil {
			s.diagnostics.printf("service.concurrency", "Warning: failed to release api key slot for %d (req=%s): %v", apiKeyID, requestID, err)
		}
	}
}

// GetAPIKeyConcurrencyBatch reads active request counts for API keys.
// Unsupported caches and Redis failures yield zero counts; a stopped runtime returns an error.
func (s *ConcurrencyService) GetAPIKeyConcurrencyBatch(ctx context.Context, apiKeyIDs []int64) (map[int64]int, error) {
	result := zeroAPIKeyConcurrencyMap(apiKeyIDs)
	if len(apiKeyIDs) == 0 {
		return result, nil
	}
	if s == nil || s.cache == nil {
		return result, nil
	}
	cache, ok := s.cache.(APIKeyConcurrencyCache)
	if !ok {
		return result, nil
	}

	operation, done, err := s.runtime.Enter(context.Background(), "GetAPIKeyConcurrencyBatch")
	if err != nil {
		return result, err
	}
	defer done()
	redisCtx, cancel := context.WithTimeout(operation, apiKeyConcurrencyFetchTimeout)
	defer cancel()

	counts, err := cache.GetAPIKeyConcurrencyBatch(redisCtx, apiKeyIDs)
	if err != nil {
		s.diagnostics.printf("service.concurrency", "Warning: get api key concurrency batch failed: %v", err)
		return result, nil
	}
	for _, apiKeyID := range apiKeyIDs {
		result[apiKeyID] = counts[apiKeyID]
	}
	return result, nil
}

func zeroAPIKeyConcurrencyMap(apiKeyIDs []int64) map[int64]int {
	result := make(map[int64]int, len(apiKeyIDs))
	for _, apiKeyID := range apiKeyIDs {
		result[apiKeyID] = 0
	}
	return result
}

// GetProviderWaitingCount returns the provider's current waiting count.
func (s *ConcurrencyService) GetProviderWaitingCount(ctx context.Context, providerID int64) (int, error) {
	if s.cache == nil {
		return 0, nil
	}
	return s.cache.GetProviderWaitingCount(ctx, providerID)
}

// CalculateMaxWait adds defaultExtraWaitSlots to the user limit, treating a nonpositive limit as 1.
func CalculateMaxWait(userConcurrency int) int {
	if userConcurrency <= 0 {
		userConcurrency = 1
	}
	return userConcurrency + defaultExtraWaitSlots
}

// GetProvidersLoadBatch reads provider loads, reusing the short-lived batch cache when enabled.
// Treat the returned map and load records as read-only because cached callers share them.
func (s *ConcurrencyService) GetProvidersLoadBatch(ctx context.Context, providers []ProviderWithConcurrency) (map[int64]*ProviderLoadInfo, error) {
	return s.getProvidersLoadBatch(ctx, providers, true)
}

// GetProvidersLoadBatchFresh bypasses the batch cache to refresh candidates after slot contention.
func (s *ConcurrencyService) GetProvidersLoadBatchFresh(ctx context.Context, providers []ProviderWithConcurrency) (map[int64]*ProviderLoadInfo, error) {
	return s.getProvidersLoadBatch(ctx, providers, false)
}

func (s *ConcurrencyService) getProvidersLoadBatch(ctx context.Context, providers []ProviderWithConcurrency, allowCache bool) (map[int64]*ProviderLoadInfo, error) {
	if len(providers) == 0 {
		return map[int64]*ProviderLoadInfo{}, nil
	}
	if s.cache == nil {
		return map[int64]*ProviderLoadInfo{}, nil
	}

	ttl := time.Duration(s.providerLoadCacheTTL.Load())
	if !allowCache || ttl <= 0 {
		return s.fetchProvidersLoadBatch(ctx, providers)
	}

	key := providerLoadBatchCacheKey(providers)
	if cached, ok := s.getCachedProviderLoadBatch(key, time.Now()); ok {
		return cached, nil
	}

	value, err, _ := s.providerLoadGroup.Do(key, func() (any, error) {
		now := time.Now()
		if cached, ok := s.getCachedProviderLoadBatch(key, now); ok {
			return cached, nil
		}
		loadMap, fetchErr := s.fetchProvidersLoadBatch(ctx, providers)
		if fetchErr != nil {
			return nil, fetchErr
		}
		cached := cloneProviderLoadMap(loadMap)
		s.storeCachedProviderLoadBatch(key, cached, now.Add(ttl))
		return cached, nil
	})
	if err != nil {
		return nil, err
	}
	loadMap, _ := value.(map[int64]*ProviderLoadInfo)
	if loadMap == nil {
		return map[int64]*ProviderLoadInfo{}, nil
	}
	return loadMap, nil
}

func (s *ConcurrencyService) fetchProvidersLoadBatch(ctx context.Context, providers []ProviderWithConcurrency) (map[int64]*ProviderLoadInfo, error) {
	if s.cache == nil {
		return map[int64]*ProviderLoadInfo{}, nil
	}
	operation, done, err := s.runtime.Enter(context.WithoutCancel(nonNilContext(ctx)), "fetchProvidersLoadBatch")
	if err != nil {
		return nil, err
	}
	defer done()
	redisCtx, cancel := context.WithTimeout(operation, providerLoadBatchFetchTimeout)
	defer cancel()
	return s.cache.GetProvidersLoadBatch(redisCtx, providers)
}

func (s *ConcurrencyService) getCachedProviderLoadBatch(key string, now time.Time) (map[int64]*ProviderLoadInfo, bool) {
	s.providerLoadCacheMu.RLock()
	cached, ok := s.providerLoadCache[key]
	s.providerLoadCacheMu.RUnlock()
	if !ok {
		return nil, false
	}
	if !now.Before(cached.expiresAt) {
		s.providerLoadCacheMu.Lock()
		if current, exists := s.providerLoadCache[key]; exists && !now.Before(current.expiresAt) {
			delete(s.providerLoadCache, key)
		}
		s.providerLoadCacheMu.Unlock()
		return nil, false
	}
	return cached.loadMap, true
}

func (s *ConcurrencyService) storeCachedProviderLoadBatch(key string, loadMap map[int64]*ProviderLoadInfo, expiresAt time.Time) {
	s.providerLoadCacheMu.Lock()
	if s.providerLoadCache == nil {
		s.providerLoadCache = make(map[string]cachedProviderLoadBatch)
	}
	if len(s.providerLoadCache) >= maxProviderLoadBatchCacheEntries {
		now := time.Now()
		for cacheKey, cached := range s.providerLoadCache {
			if !now.Before(cached.expiresAt) {
				delete(s.providerLoadCache, cacheKey)
			}
		}
		for len(s.providerLoadCache) >= maxProviderLoadBatchCacheEntries {
			for cacheKey := range s.providerLoadCache {
				delete(s.providerLoadCache, cacheKey)
				break
			}
		}
	}
	s.providerLoadCache[key] = cachedProviderLoadBatch{
		loadMap:   loadMap,
		expiresAt: expiresAt,
	}
	s.providerLoadCacheMu.Unlock()
}

func providerLoadBatchCacheKey(providers []ProviderWithConcurrency) string {
	hash := sha256.New()
	var buf [16]byte
	for _, provider := range providers {
		binary.LittleEndian.PutUint64(buf[:8], uint64(provider.ID))
		binary.LittleEndian.PutUint64(buf[8:], uint64(int64(provider.MaxConcurrency)))
		_, _ = hash.Write(buf[:])
	}
	sum := hash.Sum(nil)
	return strconv.Itoa(len(providers)) + ":" + hex.EncodeToString(sum)
}

func cloneProviderLoadMap(loadMap map[int64]*ProviderLoadInfo) map[int64]*ProviderLoadInfo {
	if len(loadMap) == 0 {
		return map[int64]*ProviderLoadInfo{}
	}
	clone := make(map[int64]*ProviderLoadInfo, len(loadMap))
	for providerID, loadInfo := range loadMap {
		if loadInfo == nil {
			clone[providerID] = nil
			continue
		}
		copied := *loadInfo
		clone[providerID] = &copied
	}
	return clone
}

// GetUsersLoadBatch reads user load in a batch.
func (s *ConcurrencyService) GetUsersLoadBatch(ctx context.Context, users []UserWithConcurrency) (map[int64]*UserLoadInfo, error) {
	if s.cache == nil {
		return map[int64]*UserLoadInfo{}, nil
	}
	return s.cache.GetUsersLoadBatch(ctx, users)
}

// CleanupExpiredProviderSlots removes expired slots for the supplied provider.
func (s *ConcurrencyService) CleanupExpiredProviderSlots(ctx context.Context, providerID int64) error {
	if s.cache == nil {
		return nil
	}
	return s.cache.CleanupExpiredProviderSlots(ctx, providerID)
}

// StartSlotCleanupWorker cleans immediately and periodically until runtime cancellation.
func (s *ConcurrencyService) StartSlotCleanupWorker(interval time.Duration) {
	if s == nil || s.cache == nil || interval <= 0 {
		return
	}
	s.runtime.Start(RuntimeTask{Name: "slot-cleanup", Interval: interval, Immediate: true, Run: func(ctx context.Context) {
		cleanup, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := s.cache.CleanupExpiredProviderSlotKeys(cleanup); err != nil {
			s.diagnostics.printf("service.concurrency", "Warning: cleanup expired provider slots failed: %v", err)
		}
	}})
}

// Stop waits up to 30 seconds; callers with a shutdown deadline can use StopContext.
func (s *ConcurrencyService) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = s.StopContext(ctx)
}

func (s *ConcurrencyService) StopContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	return s.runtime.StopContext(ctx)
}

// GetProviderConcurrencyBatch reads current provider concurrency in a batch.
// Its own timeout lets the read finish after HTTP request cancellation.
func (s *ConcurrencyService) GetProviderConcurrencyBatch(ctx context.Context, providerIDs []int64) (map[int64]int, error) {
	if len(providerIDs) == 0 {
		return map[int64]int{}, nil
	}
	if s.cache == nil {
		result := make(map[int64]int, len(providerIDs))
		for _, providerID := range providerIDs {
			result[providerID] = 0
		}
		return result, nil
	}

	operation, done, err := s.runtime.Enter(context.Background(), "GetProviderConcurrencyBatch")
	if err != nil {
		return nil, err
	}
	defer done()
	redisCtx, cancel := context.WithTimeout(operation, 3*time.Second)
	defer cancel()

	return s.cache.GetProviderConcurrencyBatch(redisCtx, providerIDs)
}

// EnterUserWait and EnterProviderWait attach wait-count cleanup to scheduler-owned leases.
func (s *ConcurrencyService) EnterUserWait(ctx context.Context, id int64, limit int) (WaitResult, error) {
	operation, done, err := s.runtime.Enter(ctx, "EnterUserWait")
	if err != nil {
		return WaitResult{}, err
	}
	result, err := EnterUserWait(operation, s.cache, id, limit, s.diagnostics)
	if err != nil || !result.Allowed {
		done()
		return result, err
	}
	previous := result.resource
	var release func()
	if previous != nil {
		release = previous.Release
	}
	result.resource = NewLease(context.Background(), ReleaseOnCompletion, done, release)
	return result, nil
}

func (s *ConcurrencyService) EnterProviderWait(ctx context.Context, id int64, limit int) (WaitResult, error) {
	operation, done, err := s.runtime.Enter(ctx, "EnterProviderWait")
	if err != nil {
		return WaitResult{}, err
	}
	result, err := EnterProviderWait(operation, s.cache, id, limit, s.diagnostics)
	if err != nil || !result.Allowed {
		done()
		return result, err
	}
	previous := result.resource
	var release func()
	if previous != nil {
		release = previous.Release
	}
	result.resource = NewLease(context.Background(), ReleaseOnCompletion, done, release)
	return result, nil
}

// LiveLeases returns long-session lease support, or nil when unsupported.
func (s *ConcurrencyService) LiveLeases() LiveConcurrencyCache {
	if s == nil || s.cache == nil {
		return nil
	}
	cache, _ := s.cache.(LiveConcurrencyCache)
	return cache
}

// AcquireProviderSlot registers in-flight I/O with the runtime and transfers completion tracking to the acquired
// slot's release.
func (s *ConcurrencyService) AcquireProviderSlot(ctx context.Context, providerID int64, limit int) (*AcquireResult, error) {
	operation, done, err := s.runtime.Enter(ctx, fmt.Sprintf("provider-slot:%d", providerID))
	if err != nil {
		return nil, err
	}
	result, err := s.acquireProviderSlot(operation, providerID, limit)
	if err != nil || result == nil || !result.Acquired {
		done()
		return result, err
	}
	release := NewLease(context.Background(), ReleaseOnCompletion, done, result.ReleaseFunc).Release
	// Release an acquisition that raced with shutdown; ordinary request cancellation follows the caller's release mode.
	if stopErr := s.runtime.Context().Err(); stopErr != nil {
		release()
		return nil, stopErr
	}
	ownRequestResource(ctx, release)
	result.ReleaseFunc = release
	return result, nil
}

// AcquireUserSlot registers in-flight I/O with the runtime and transfers completion tracking to the acquired
// slot's release.
func (s *ConcurrencyService) AcquireUserSlot(ctx context.Context, userID int64, limit int) (*AcquireResult, error) {
	operation, done, err := s.runtime.Enter(ctx, fmt.Sprintf("user-slot:%d", userID))
	if err != nil {
		return nil, err
	}
	result, err := s.acquireUserSlot(operation, userID, limit)
	if err != nil || result == nil || !result.Acquired {
		done()
		return result, err
	}
	release := NewLease(context.Background(), ReleaseOnCompletion, done, result.ReleaseFunc).Release
	// Release an acquisition that raced with shutdown; ordinary request cancellation follows the caller's release mode.
	if stopErr := s.runtime.Context().Err(); stopErr != nil {
		release()
		return nil, stopErr
	}
	result.ReleaseFunc = release
	return result, nil
}

// TrackAPIKeySlot tracks a slot within a bounded operation and rejects new tracking after runtime shutdown.
func (s *ConcurrencyService) TrackAPIKeySlot(ctx context.Context, id int64) func() {
	if s == nil {
		return func() {}
	}
	operation, done, err := s.runtime.Enter(context.WithoutCancel(nonNilContext(ctx)), fmt.Sprintf("apikey-slot:%d", id))
	if err != nil {
		return func() {}
	}
	release := s.trackAPIKeySlot(operation, id)
	return NewLease(context.Background(), ReleaseOnCompletion, done, release).Release
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
