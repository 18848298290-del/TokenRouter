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

// Provider and user request slots remain owned until their acquired release
// handles finish cache cleanup. Runtime registration starts before cache I/O
// and transfers to successful cleanup, so shutdown can wait for resources
// still held by requests. Waiting counts and WebSocket ingress leases use
// separate lifetimes beside those ordinary slots.
//
// Provider load observations can be reused across requests through a short
// cache keyed by the ordered provider IDs and load denominators. Cached maps
// and records are shared read-only, while fresh reads bypass that reuse.
// WebSocket ingress renews ownership independently of request turns; missing
// membership cancels the lease, and unconfirmed renewals eventually exhaust
// its TTL. Cleanup uses detached bounded contexts, with adapter cooperation
// determining when those operations actually return.

// Concurrency limits and operation budgets bound ingress renewal, shared load
// reads, statistics, and local cache retention.
const (
	// unconfirmed ingress ownership lifetime.
	openAIWSIngressLeaseTTL = 60 * time.Second
	// periodic ingress renewal interval.
	openAIWSIngressLeaseRefreshInterval = 20 * time.Second
	// ingress cache operation budget.
	openAIWSIngressLeaseOperationTO = 2 * time.Second

	// additional user waiting positions.
	defaultExtraWaitSlots = 20

	// default provider observation lifetime.
	defaultProviderLoadBatchCacheTTL = 200 * time.Millisecond
	// shared load fetch budget.
	providerLoadBatchFetchTimeout = 3 * time.Second
	// retained batch key ceiling.
	maxProviderLoadBatchCacheEntries = 256
	// key statistics read budget.
	apiKeyConcurrencyFetchTimeout = 3 * time.Second
	// key statistics tracking budget.
	apiKeySlotTrackTimeout = 2 * time.Second
)

// The ingress loss cause and process-specific request identities support lease
// cancellation and cache-member ownership across concurrent requests.
var (
	// ErrOpenAIWSIngressLeaseLost identifies missing or persistently unconfirmed
	// ingress ownership.
	ErrOpenAIWSIngressLeaseLost = errors.New("openai websocket ingress lease lost")

	// requestIDPrefix stays fixed for this process, while requestIDCounter
	// assigns member sequences atomically. Their combined text identifies cache
	// ownership rather than the gateway's external request trace identifier.
	requestIDPrefix  = initRequestIDPrefix()
	requestIDCounter atomic.Uint64 // process cache-member sequence
)

// ConcurrencyCache supplies ordinary slot admission, bounded waits, observed
// load, and stale-member cleanup.
type ConcurrencyCache interface {
	// AcquireProviderSlot reserves one provider member under requestID when
	// ordinary and Live occupancy permits admission. Callers release confirmed
	// ownership after execution; a cache error leaves admission unconfirmed and
	// expiry supplies eventual recovery.
	AcquireProviderSlot(ctx context.Context, providerID int64, maxConcurrency int, requestID string) (bool, error)
	// ReleaseProviderSlot removes the provider slot identified by requestID.
	// Callers retain that exact member ID from acquisition; removal is separate
	// from wait-count cleanup and errors leave physical cleanup unconfirmed.
	ReleaseProviderSlot(ctx context.Context, providerID int64, requestID string) error
	// GetProviderConcurrency reads the provider's ordinary and Live slot
	// occupancy after adapter expiry handling. The result is an observation for
	// monitoring; concurrent admissions can change occupancy after the read.
	GetProviderConcurrency(ctx context.Context, providerID int64) (int, error)
	// GetProviderConcurrencyBatch reads ordinary and Live occupancy for the
	// supplied provider IDs. Batch results are observations rather than a
	// transactionally consistent view of every provider during concurrent slot
	// changes.
	GetProviderConcurrencyBatch(ctx context.Context, providerIDs []int64) (map[int64]int, error)

	// IncrementProviderWaitCount attempts to add a provider wait position below
	// maxWait. Successful callers release confirmed ownership, while cache errors
	// can leave the increment uncertain and the service chooses its fail-open
	// wait policy.
	IncrementProviderWaitCount(ctx context.Context, providerID int64, maxWait int) (bool, error)
	// DecrementProviderWaitCount removes one confirmed provider wait position.
	// Callers decrement counts they own after waiting ends; uncertain admission
	// is left to the adapter's expiry rather than removing another request's
	// count.
	DecrementProviderWaitCount(ctx context.Context, providerID int64) error
	// GetProviderWaitingCount reads observed waiting pressure for one provider
	// queue. Selection uses this value when choosing a wait plan, and later
	// admission performs the separate bounded increment that confirms queue
	// ownership.
	GetProviderWaitingCount(ctx context.Context, providerID int64) (int, error)

	// AcquireUserSlot reserves one user request member under requestID while user
	// and Live occupancy permits admission. Callers release confirmed ownership
	// after the request's chosen cleanup point; cache errors leave admission
	// unconfirmed.
	AcquireUserSlot(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error)
	// ReleaseUserSlot removes the user member identified by its acquisition
	// requestID. The caller returns confirmed capacity after request cleanup,
	// with cache errors leaving removal unconfirmed until stale-member handling.
	ReleaseUserSlot(ctx context.Context, userID int64, requestID string) error
	// GetUserConcurrency reads the user's ordinary and Live request occupancy
	// after expiry handling. It reports observed load rather than reserving
	// capacity, so admission still requires a separate slot operation.
	GetUserConcurrency(ctx context.Context, userID int64) (int, error)

	// IncrementWaitCount attempts to reserve a bounded user waiting position.
	// Confirmed admission owns a later decrement, while the service allows
	// uncertain increments to expire when a cache error prevents ownership
	// confirmation.
	IncrementWaitCount(ctx context.Context, userID int64, maxWait int) (bool, error)
	// DecrementWaitCount removes one confirmed user waiting position when waiting
	// ends. Callers avoid decrementing an uncertain increment because another
	// request's count can already exist under the same user.
	DecrementWaitCount(ctx context.Context, userID int64) error

	// GetProvidersLoadBatch returns provider occupancy, waiting pressure, and
	// percentages using each supplied denominator. The Redis adapter prunes stale
	// members during observation, so the read can also perform expiry
	// maintenance.
	GetProvidersLoadBatch(ctx context.Context, providers []ProviderWithConcurrency) (map[int64]*ProviderLoadInfo, error)
	// GetUsersLoadBatch returns user occupancy, waiting pressure, and percentages
	// using the supplied user denominators. It remains an observational query;
	// the cache adapter owns expiry handling and reports cache failures.
	GetUsersLoadBatch(ctx context.Context, users []UserWithConcurrency) (map[int64]*UserLoadInfo, error)

	// CleanupExpiredProviderSlots removes stale ordinary members for one provider
	// according to the adapter's slot lifetime. It is maintenance after earlier
	// admissions, and current request release still supplies prompt owned-member
	// cleanup.
	CleanupExpiredProviderSlots(ctx context.Context, providerID int64) error
	// CleanupExpiredProviderSlotKeys examines indexed provider cleanup candidates
	// and removes expired slot members. The Redis adapter maintains the index
	// separately, so callers retain normal release and expiry handling alongside
	// this discovery path.
	CleanupExpiredProviderSlotKeys(ctx context.Context) error

	// CleanupStaleProcessSlots removes ordinary members outside
	// activeRequestPrefix and resets stale waiting state under the Redis
	// adapter's startup sweep. Callers supply this process prefix; multi-instance
	// startup coordination is outside this method's interface.
	CleanupStaleProcessSlots(ctx context.Context, activeRequestPrefix string) error
}

// APIKeyConcurrencyCache supplies active-request statistics and their
// owned-member cleanup for API keys.
type APIKeyConcurrencyCache interface {
	// TrackAPIKeySlot records an active statistics member using requestID. The
	// service treats tracking failure as unconfirmed ownership and returns no-op
	// statistics cleanup so unavailable observations do not reject the request.
	TrackAPIKeySlot(ctx context.Context, apiKeyID int64, requestID string) error
	// ReleaseAPIKeySlot removes the statistics member identified by requestID
	// after execution cleanup. Callers release confirmed tracking, while adapter
	// expiry remains available when removal returns an error.
	ReleaseAPIKeySlot(ctx context.Context, apiKeyID int64, requestID string) error
	// GetAPIKeyConcurrencyBatch returns ordinary and Live request observations
	// for API key IDs. The service can substitute zero counts on read failure, so
	// consumers treat these values as monitoring data rather than admission
	// decisions.
	GetAPIKeyConcurrencyBatch(ctx context.Context, apiKeyIDs []int64) (map[int64]int, error)
}

// OpenAIWSIngressLeaseCache maintains short API-key connection leases
// separately from ordinary request-turn slots.
type OpenAIWSIngressLeaseCache interface {
	// AcquireOpenAIWSIngressLease reserves an API-key ingress member under
	// leaseID below maxConnections. The caller renews confirmed ownership and
	// releases it when the connection ends; ordinary user/provider request slots
	// remain independently managed.
	AcquireOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, maxConnections int, leaseID string) (bool, error)
	// RefreshOpenAIWSIngressLease renews an existing ingress member and reports
	// whether ownership remains present. False ownership loses the connection
	// lease immediately, while operation errors leave renewal unconfirmed for the
	// service's TTL policy.
	RefreshOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, leaseID string) (bool, error)
	// ReleaseOpenAIWSIngressLease removes the API-key ingress member identified
	// by leaseID after its renewal loop exits. Callers use the same member
	// identity as acquisition, with adapter expiry covering unconfirmed cleanup
	// failures.
	ReleaseOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, leaseID string) error
}

// OpenAIWSIngressLease holds renewable API-key ingress ownership and its
// cancellation context. Callers invoke Release on every exit path; it waits
// for renewal work before cache removal and runtime completion.
type OpenAIWSIngressLease struct {
	diagnostics Diagnostics // ingress failure observation callbacks
	// ctx and cancel represent connection ownership rather than a request-turn
	// slot. Acquisition links caller cancellation and runtime stop to this
	// context, while renewal adds a loss cause.
	ctx    context.Context         // connection ownership cancellation context
	cancel context.CancelCauseFunc // connection ownership cancellation cause
	// cache remains available through renewal and Release. The owning connection
	// keeps its API-key/member identity stable while the shared adapter refreshes
	// or removes that same membership.
	cache    OpenAIWSIngressLeaseCache // shared ingress membership adapter
	apiKeyID int64                     // ingress owner key identifier
	leaseID  string                    // acquired ingress member identity

	// stopOnce, stopCh, refreshDone, and finishRuntime coordinate cleanup.
	// Release closes the stop signal, cancels ctx, waits for refreshDone, removes
	// the cache member, then invokes finishRuntime after that cleanup attempt.
	stopOnce      sync.Once     // shared ingress cleanup guard
	stopCh        chan struct{} // explicit renewal stop signal
	refreshDone   chan struct{} // renewal loop completion signal
	finishRuntime func()        // runtime registration cleanup callback
}

// ConcurrencyService manages shared request capacity, wait ownership,
// monitoring reads, and ingress leases.
type ConcurrencyService struct {
	diagnostics Diagnostics // capacity operation observation callbacks
	// runtime owns admitted operations until their cleanup callback completes.
	// Successful slots, waits, and ingress leases retain registration after
	// acquisition I/O returns.
	runtime WorkerRuntime // request and worker shutdown tracking

	// cache remains available while this service or its cleanup handles operate.
	// Optional statistics and connection support are discovered through
	// additional interfaces on the same adapter.
	cache ConcurrencyCache // shared distributed capacity adapter

	// providerLoadCacheTTL selects reuse, providerLoadCacheMu protects
	// providerLoadCache publication and removal, and providerLoadGroup coalesces
	// same-key misses. Published maps and their load pointers stay immutable for
	// concurrent readers.
	providerLoadCacheTTL atomic.Int64 // nanoseconds
	providerLoadCacheMu  sync.RWMutex // batch cache publication mutex
	// shared read-only provider observations
	providerLoadCache map[string]cachedProviderLoadBatch
	providerLoadGroup singleflight.Group // per-batch coalesced fetch group
}

// cachedProviderLoadBatch contains shared immutable load records and their
// chosen reuse deadline.
type cachedProviderLoadBatch struct {
	// loadMap is copied before cache insertion and borrowed by cached readers.
	// Callers leave both its entries and pointed-to records unchanged while using
	// returned load observations.
	loadMap   map[int64]*ProviderLoadInfo // shared provider load observations
	expiresAt time.Time                   // batch observation reuse deadline
}

// AcquireResult distinguishes confirmed slot admission from denial and carries
// any owned cleanup handle.
type AcquireResult struct {
	Acquired bool // confirmed slot admission flag
	// ReleaseFunc is present after confirmed admission, including a no-op when
	// limits are disabled. Public acquisition wraps cleanup for repeated release;
	// private raw callbacks rely on their owning caller.
	ReleaseFunc func() // owned slot cleanup callback
}

// ProviderWithConcurrency pairs a provider identifier with the denominator
// supplied to load observation.
type ProviderWithConcurrency struct {
	ID int64 // observed provider record identifier
	// MaxConcurrency supplies the load denominator to batch reads. Provider
	// selection can pass EffectiveLoadFactor here, which differs from the
	// physical concurrency limit used for slot admission.
	MaxConcurrency int // requested load percentage denominator
}

// UserWithConcurrency pairs a user identifier with the denominator supplied to
// load observation.
type UserWithConcurrency struct {
	ID int64 // observed user record identifier
	// MaxConcurrency supplies the denominator to user load observations. The
	// Redis adapter reports zero percentage for nonpositive denominators; this
	// batch query does not itself admit a user slot.
	MaxConcurrency int // requested load percentage denominator
}

// ProviderLoadInfo contains observed request occupancy, waiting pressure, and
// a derived load percentage.
type ProviderLoadInfo struct {
	ProviderID         int64 // observed capacity owner identifier
	CurrentConcurrency int   // observed active request count
	WaitingCount       int   // observed queued request count
	LoadRate           int   // percent
}

// UserLoadInfo contains observed request occupancy, waiting pressure, and a
// derived load percentage.
type UserLoadInfo struct {
	UserID             int64 // observed capacity owner identifier
	CurrentConcurrency int   // observed active request count
	WaitingCount       int   // observed queued request count
	LoadRate           int   // percent
}

// Context returns the ingress ownership context used by its connection
// handler. A nil lease or missing context returns context.Background; acquired
// lease contexts follow caller cancellation, explicit release, runtime stop,
// and confirmed ownership loss.
func (l *OpenAIWSIngressLease) Context() context.Context {
	if l == nil || l.ctx == nil {
		return context.Background()
	}
	return l.ctx
}

// Release ends ingress renewal and attempts removal of the acquired member
// once. Nil receivers are harmless, and concurrent callers share stopOnce so
// completed cleanup is not repeated. The connection caller still invokes this
// method after cancellation to return capacity promptly.
//
// Cleanup closes the stop channel and cancels the connection context before
// waiting for refreshDone. Waiting prevents a late renewal from racing after
// member removal. Cache removal uses an independent two-second budget and logs
// failures, while finishRuntime runs after the cleanup attempt. The budget
// requires adapter cooperation and does not bound a renewal implementation
// that ignores its context.
func (l *OpenAIWSIngressLease) Release() {
	if l == nil {
		return
	}
	l.stopOnce.Do(func() {
		if l.finishRuntime != nil {
			defer l.finishRuntime()
		}
		// Stop renewal before removing the cache member. Waiting for its completion
		// prevents an in-flight refresh from racing after cleanup has returned
		// capacity.
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

// refreshLoop periodically confirms ingress ownership until cancellation or
// explicit release. It closes refreshDone on exit so Release can wait before
// deleting the member. Its ticker is stopped when the loop returns.
//
// The initial confirmation instant is the loop start, and each successful
// refresh replaces it. A missing membership loses ownership immediately;
// errors retain that instant until one full lease TTL passes. Loss cancels the
// connection with ErrOpenAIWSIngressLeaseLost and ends renewal, leaving the
// handler to perform final Release.
func (l *OpenAIWSIngressLease) refreshLoop() {
	// Release waits on refreshDone before removing cache membership. Close this
	// completion signal after the renewal loop and its ticker cleanup finish so
	// no in-flight refresh remains.
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
	// Begin the unconfirmed interval when renewal starts. Successful refreshes
	// replace this instant; repeated adapter errors retain it so connection
	// ownership eventually reaches its loss threshold.
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

// refresh attempts bounded renewal and returns the last confirmation time plus
// a loss decision. Successful ownership samples the current clock, while
// errors retain the previous confirmation and log the unconfirmed interval.
//
// A successful response reporting absent membership is converted to the
// lease-lost cause and loses immediately. Other failures are tolerated until
// the elapsed unconfirmed interval reaches the ingress TTL. The independent
// operation context permits cleanup coordination even when the connection's
// request context has already ended.
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
	// A missing member is confirmed loss, while other errors are uncertainty.
	// Stop after one unconfirmed TTL so the connection cannot continue
	// indefinitely on unrenewed ownership.
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

// initRequestIDPrefix chooses the process component of internal cache-member
// IDs. Successful entropy reads encode eight random bytes as a base36 value
// with an r prefix. The value is initialized once and combined with
// GenerateRequestID's per-process sequence for each new member.
//
// If entropy acquisition reports an error, the method XORs a nanosecond clock
// sample with the process ID shifted by sixteen bits and uses the same text
// encoding. These observations keep an ID available for acquisition but do not
// establish uniqueness across process lifetimes or hosts.
func initRequestIDPrefix() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err == nil {
		return "r" + strconv.FormatUint(binary.BigEndian.Uint64(b), 36)
	}
	// If entropy acquisition reports failure, preserve a usable process prefix by
	// mixing clock and process observations; this degraded choice does not
	// establish globally unique identity.
	fallback := uint64(time.Now().UnixNano()) ^ (uint64(os.Getpid()) << 16)
	return "r" + strconv.FormatUint(fallback, 36)
}

// RequestIDPrefix returns the stable process prefix used by internal
// cache-member identities.
func RequestIDPrefix() string {
	return requestIDPrefix
}

// GenerateRequestID combines the fixed process prefix with an atomically
// allocated base36 sequence. Concurrent calls share that sequence; these IDs
// identify owned cache members, while gateway request correlation uses its
// separate request identifier.
func GenerateRequestID() string {
	seq := requestIDCounter.Add(1)
	return requestIDPrefix + "-" + strconv.FormatUint(seq, 36)
}

// NewConcurrencyService binds a shared cache and optional first diagnostic
// value with a short provider-load cache. Callers keep the adapter available
// while admitted resources and cleanup handles remain owned; unused diagnostic
// arguments after the first are ignored.
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

// CleanupStaleProcessSlots delegates startup cleanup using this process's
// request prefix. Missing receiver or cache skips the sweep; the Redis adapter
// removes other prefixes, so callers coordinate startup consistently with
// their deployment's process ownership.
func (s *ConcurrencyService) CleanupStaleProcessSlots(ctx context.Context) error {
	if s == nil || s.cache == nil {
		return nil
	}
	return s.cache.CleanupStaleProcessSlots(ctx, RequestIDPrefix())
}

// AcquireOpenAIWSIngressLease reserves an API-key ingress member and starts
// renewable ownership for the connection. A nonpositive limit returns
// successful bypass with no lease, even when the service is absent. Positive
// limits require a supported cache, valid key ID, and admitted runtime
// operation.
//
// Acquisition uses a two-second budget detached from caller cancellation.
// After confirmed acquisition, the lease context follows the caller and
// runtime stop, while the runtime registration remains owned until Release
// finishes cleanup. The method starts renewal before returning; callers
// release the lease on every exit, including ownership cancellation or an
// already-canceled input context.
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
	// Connect runtime cancellation to the acquired lease's cause before starting
	// renewal. Release removes that callback and completes runtime tracking after
	// member cleanup.
	stop := context.AfterFunc(baseCtx, func() { leaseCancel(ErrRuntimeStopped) })
	lease.finishRuntime = func() { stop(); done() }
	go lease.refreshLoop()
	return lease, true, nil
}

// SetProviderLoadBatchCacheTTL updates provider observation reuse and clears
// current entries for nonpositive lifetimes. A fetch already running can still
// publish after the clear; subsequent reads bypass reuse while disabled,
// regardless of that retained entry.
func (s *ConcurrencyService) SetProviderLoadBatchCacheTTL(ttl time.Duration) {
	if s == nil {
		return
	}
	s.providerLoadCacheTTL.Store(int64(ttl))
	// Publish the disabled reuse value before clearing current entries. Readers
	// bypass the cache once disabled, even if an earlier fetch later stores
	// another observation.
	if ttl <= 0 {
		s.providerLoadCacheMu.Lock()
		s.providerLoadCache = make(map[string]cachedProviderLoadBatch)
		s.providerLoadCacheMu.Unlock()
	}
}

// acquireProviderSlot returns confirmed provider admission or a denied result
// through the cache adapter. Nonpositive limits bypass cache access with a
// no-op release; positive limits require a usable cache and successful callers
// return capacity after execution using the raw cleanup callback.
func (s *ConcurrencyService) acquireProviderSlot(ctx context.Context, providerID int64, maxConcurrency int) (*AcquireResult, error) {
	if maxConcurrency <= 0 {
		return &AcquireResult{
			Acquired:    true,
			ReleaseFunc: func() {},
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
				// Cleanup uses an independent request budget because the original context
				// may have ended. A failed removal is logged, leaving adapter expiry as
				// recovery for unconfirmed release.
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

// acquireUserSlot returns confirmed user admission or a denied result through
// the cache adapter. Nonpositive limits bypass cache access, and raw
// successful cleanup uses an independent five-second budget; the public
// wrapper supplies its release guard and runtime ownership.
func (s *ConcurrencyService) acquireUserSlot(ctx context.Context, userID int64, maxConcurrency int) (*AcquireResult, error) {
	if maxConcurrency <= 0 {
		return &AcquireResult{
			Acquired:    true,
			ReleaseFunc: func() {},
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
				// Cleanup uses an independent request budget because the original context
				// may have ended. A failed removal is logged, leaving adapter expiry as
				// recovery for unconfirmed release.
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

// trackAPIKeySlot records one internal statistics member and returns its
// cleanup callback. Missing support, invalid IDs, or tracking errors produce a
// no-op handle; confirmed cleanup uses an independent five-second context and
// logs release failures.
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
	// A failed tracking call leaves ownership uncertain. Return no-op statistics
	// cleanup so monitoring availability cannot reject a request or remove
	// another request's member.
	if err != nil {
		s.diagnostics.printf("service.concurrency", "Warning: failed to track api key slot for %d (req=%s): %v", apiKeyID, requestID, err)
		return func() {}
	}

	return func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Cleanup uses an independent request budget because the original context
		// may have ended. A failed removal is logged, leaving adapter expiry as
		// recovery for unconfirmed release.
		if err := cache.ReleaseAPIKeySlot(bgCtx, apiKeyID, requestID); err != nil {
			s.diagnostics.printf("service.concurrency", "Warning: failed to release api key slot for %d (req=%s): %v", apiKeyID, requestID, err)
		}
	}
}

// GetAPIKeyConcurrencyBatch returns counts for requested API keys, filling
// absent support or failed reads with zero observations. Its three-second read
// is independent of caller cancellation and registered with runtime shutdown;
// a stopped runtime returns an error alongside the initialized zero map.
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

	// Read statistics independently of request cancellation within the runtime
	// budget. A failed read keeps the initialized zero map for monitoring while a
	// runtime admission failure is returned.
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

// zeroAPIKeyConcurrencyMap constructs a new zero-count entry for each supplied
// key identifier.
func zeroAPIKeyConcurrencyMap(apiKeyIDs []int64) map[int64]int {
	result := make(map[int64]int, len(apiKeyIDs))
	for _, apiKeyID := range apiKeyIDs {
		result[apiKeyID] = 0
	}
	return result
}

// GetProviderWaitingCount reads observed queue pressure or zero when the cache
// is absent. Cache errors pass to the caller, which separately decides whether
// unavailable queue observation permits a wait plan.
func (s *ConcurrencyService) GetProviderWaitingCount(ctx context.Context, providerID int64) (int, error) {
	if s.cache == nil {
		return 0, nil
	}
	return s.cache.GetProviderWaitingCount(ctx, providerID)
}

// CalculateMaxWait adds the configured extra waiting positions to the user's
// concurrency limit. Nonpositive input is treated as one, allowing callers to
// derive a finite queue allowance even when the input supplies no positive
// concurrency.
func CalculateMaxWait(userConcurrency int) int {
	if userConcurrency <= 0 {
		userConcurrency = 1
	}
	return userConcurrency + defaultExtraWaitSlots
}

// GetProvidersLoadBatch reads provider observations with optional short cache
// reuse. The returned map and load pointers can be shared across callers and
// remain read-only; each supplied denominator participates in the ordered
// batch cache key.
func (s *ConcurrencyService) GetProvidersLoadBatch(ctx context.Context, providers []ProviderWithConcurrency) (map[int64]*ProviderLoadInfo, error) {
	return s.getProvidersLoadBatch(ctx, providers, true)
}

// GetProvidersLoadBatchFresh bypasses the observation cache and same-key
// coalescing for a current adapter read. The operation survives caller
// cancellation within its runtime-owned three-second budget, and returned data
// follows the adapter's ownership.
func (s *ConcurrencyService) GetProvidersLoadBatchFresh(ctx context.Context, providers []ProviderWithConcurrency) (map[int64]*ProviderLoadInfo, error) {
	return s.getProvidersLoadBatch(ctx, providers, false)
}

// getProvidersLoadBatch selects cached, coalesced, or direct load observation
// according to allowCache and the current TTL. Empty input or absent cache
// returns a new empty map. Cached results retain shared immutable maps and
// records.
//
// Same-key misses coalesce through singleflight and wait synchronously for one
// detached fetch. The cached deadline is based on the pre-fetch clock, so slow
// reads can consume part of the chosen reuse interval. Publication copies
// returned records and can occur after a concurrent cache disable; disabled
// readers still bypass that stored result.
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
		// Copy adapter records before publishing shared observations. A later
		// adapter result or fixture mutation then cannot replace the scalar records
		// already retained for cached readers.
		cached := cloneProviderLoadMap(loadMap)
		s.storeCachedProviderLoadBatch(key, cached, now.Add(ttl))
		return cached, nil
	})
	if err != nil {
		return nil, err
	}
	loadMap, _ := value.(map[int64]*ProviderLoadInfo)
	// The coalesced result can supply no map. Normalize that result to empty
	// observations so callers can continue their missing-load policy without
	// dereferencing absent storage.
	if loadMap == nil {
		return map[int64]*ProviderLoadInfo{}, nil
	}
	return loadMap, nil
}

// fetchProvidersLoadBatch registers one adapter read with runtime shutdown and
// applies a three-second budget. It detaches request cancellation, preserves
// caller context values, and returns cache errors directly; absent cache
// returns an empty observation map.
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

// getCachedProviderLoadBatch returns shared observations when the sampled
// entry is still live. It copies the cache entry under a read lock, then
// compares that entry's deadline with now. Callers leave any returned map and
// load records unchanged.
//
// Expiration cleanup reacquires the write lock and checks the entry currently
// stored under the key. A newer publication can therefore survive even though
// this particular read reports a miss. The method returns its sampled
// observation; later fetch coordination decides whether another cached or
// adapter result supplies that caller's load data.
func (s *ConcurrencyService) getCachedProviderLoadBatch(key string, now time.Time) (map[int64]*ProviderLoadInfo, bool) {
	s.providerLoadCacheMu.RLock()
	cached, ok := s.providerLoadCache[key]
	s.providerLoadCacheMu.RUnlock()
	if !ok {
		return nil, false
	}
	if !now.Before(cached.expiresAt) {
		// A writer can replace this key after the read lock ends. Recheck its
		// current deadline under the write lock before deleting so a fresh
		// publication survives expiration cleanup.
		s.providerLoadCacheMu.Lock()
		if current, exists := s.providerLoadCache[key]; exists && !now.Before(current.expiresAt) {
			delete(s.providerLoadCache, key)
		}
		s.providerLoadCacheMu.Unlock()
		return nil, false
	}
	return cached.loadMap, true
}

// storeCachedProviderLoadBatch publishes supplied immutable observations under
// the cache write lock. Callers provide map and record storage that remains
// read-only while cached consumers borrow it. The entry receives the supplied
// deadline, including a deadline chosen before a slow fetch.
//
// At the local entry ceiling, expiration removes stale observations first. If
// live entries still fill the cache, arbitrary retained keys are removed until
// the new insertion fits. This bounds local batch-key retention independently
// of request-slot ownership; eviction follows map iteration order.
func (s *ConcurrencyService) storeCachedProviderLoadBatch(key string, loadMap map[int64]*ProviderLoadInfo, expiresAt time.Time) {
	s.providerLoadCacheMu.Lock()
	if s.providerLoadCache == nil {
		s.providerLoadCache = make(map[string]cachedProviderLoadBatch)
	}
	if len(s.providerLoadCache) >= maxProviderLoadBatchCacheEntries {
		now := time.Now()
		// Reclaim expired batches before evicting live observations. Removing these
		// observation entries changes load reuse; admitted request slots retain
		// their separate release owners.
		for cacheKey, cached := range s.providerLoadCache {
			if !now.Before(cached.expiresAt) {
				delete(s.providerLoadCache, cacheKey)
			}
		}
		// After expired records are removed, remaining keys can all be live. Evict
		// arbitrary retained batches to honor the local entry ceiling before
		// publishing this new observation.
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

// providerLoadBatchCacheKey identifies an ordered batch of provider IDs and
// load denominators with its length and a SHA256 digest. Each ID and denominator
// occupies eight little-endian bytes, and signed values retain their bit
// representation when converted to uint64.
//
// Fixed-width pairs distinguish the entries without separators, while their
// sequence preserves the caller's order. Changing a denominator also changes
// the encoded observation request, so load percentages calculated with
// different capacities cannot reuse the same input encoding. The digest is a
// compact cache key, with the collision properties of SHA256.
func providerLoadBatchCacheKey(providers []ProviderWithConcurrency) string {
	hash := sha256.New()
	var buf [16]byte
	// Encode both values at fixed widths before hashing. SHA256 accepts the full
	// pair with a nil write error, so no entry boundary depends on decimal text.
	for _, provider := range providers {
		binary.LittleEndian.PutUint64(buf[:8], uint64(provider.ID))
		binary.LittleEndian.PutUint64(buf[8:], uint64(int64(provider.MaxConcurrency)))
		_, _ = hash.Write(buf[:])
	}
	sum := hash.Sum(nil)
	return strconv.Itoa(len(providers)) + ":" + hex.EncodeToString(sum)
}

// cloneProviderLoadMap copies the map and every nonnil load record before
// cache publication. Nil record entries remain nil and empty input yields a
// new empty map, giving cached observations independent scalar record storage.
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

// GetUsersLoadBatch delegates user observations under the supplied context or
// returns an empty map for an absent cache. It has no provider-batch cache or
// detached timeout; adapter read errors pass directly to its caller.
func (s *ConcurrencyService) GetUsersLoadBatch(ctx context.Context, users []UserWithConcurrency) (map[int64]*UserLoadInfo, error) {
	if s.cache == nil {
		return map[int64]*UserLoadInfo{}, nil
	}
	return s.cache.GetUsersLoadBatch(ctx, users)
}

// CleanupExpiredProviderSlots delegates one provider's expiry maintenance
// under the supplied context. An absent cache skips the operation and adapter
// errors are returned, while request release remains the prompt path for
// confirmed owned capacity.
func (s *ConcurrencyService) CleanupExpiredProviderSlots(ctx context.Context, providerID int64) error {
	if s.cache == nil {
		return nil
	}
	return s.cache.CleanupExpiredProviderSlots(ctx, providerID)
}

// StartSlotCleanupWorker starts immediate and periodic indexed provider expiry
// maintenance through the runtime. Missing receiver, cache, or positive
// interval skips startup; each cleanup receives a five-second context and
// failures are logged before later scheduled work.
func (s *ConcurrencyService) StartSlotCleanupWorker(interval time.Duration) {
	if s == nil || s.cache == nil || interval <= 0 {
		return
	}
	s.runtime.Start(RuntimeTask{Name: "slot-cleanup", Interval: interval, Immediate: true, Run: func(ctx context.Context) {
		cleanup, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		// Background cleanup failure leaves ordinary release and expiry handling
		// available. Log the failed sweep and allow the runtime's later interval to
		// attempt maintenance again.
		if err := s.cache.CleanupExpiredProviderSlotKeys(cleanup); err != nil {
			s.diagnostics.printf("service.concurrency", "Warning: cleanup expired provider slots failed: %v", err)
		}
	}})
}

// Stop cancels runtime work and waits using a thirty-second shutdown budget.
// It discards the returned stop error, so callers that need pending-work
// evidence or the application's remaining deadline use StopContext.
func (s *ConcurrencyService) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// This void entry point returns no shutdown cause. Callers needing pending
	// task details obtain the runtime's retained first result from StopContext
	// with their own remaining budget.
	_ = s.StopContext(ctx)
}

// StopContext delegates cancellation and joining of admitted runtime work to
// the caller's budget. Nil receiver succeeds; the runtime retains its first
// stop result and rejects later admitted operations even when waiting times
// out.
func (s *ConcurrencyService) StopContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	return s.runtime.StopContext(ctx)
}

// GetProviderConcurrencyBatch returns provider occupancy with a runtime-owned
// three-second read independent of caller cancellation. Empty input returns an
// empty map, absent cache fills requested IDs with zeros, and adapter or
// stopped-runtime errors are propagated.
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

// EnterUserWait admits a user wait under runtime tracking and returns its
// ownership result. Denied or failed admission ends runtime registration
// immediately; successful results require Release after waiting, including
// fail-open admission that owns no confirmed count.
//
// The returned result wraps previous wait cleanup and runtime completion in
// one completion-based lease. Cleanup runs before registration ends, so
// shutdown can wait for confirmed decrement attempts. Repeated result releases
// share that owning lease, and ordinary request cancellation does not itself
// invoke this wrapper's completion policy.
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
	// Allowed waiting retains runtime ownership after admission I/O. Adopt the
	// previous cleanup before done so Stop keeps waiting until the wait resource
	// has finished release.
	previous := result.resource
	var release func()
	if previous != nil {
		release = previous.Release
	}
	result.resource = NewLease(context.Background(), ReleaseOnCompletion, done, release)
	return result, nil
}

// EnterProviderWait admits a provider wait and retains runtime tracking in its
// returned owner. Failed or denied results end tracking immediately, while
// allowed results require caller release after waiting even when cache failure
// leaves the count uncertain.
//
// A completion-based lease adopts prior wait cleanup before runtime
// completion. That ordering keeps admitted wait work visible until its
// decrement attempt returns, and repeated releases share one owner. The
// underlying ownership policy decides whether a count was confirmed;
// unconfirmed increments do not remove another request's waiting count.
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
	// Allowed waiting retains runtime ownership after admission I/O. Adopt the
	// previous cleanup before done so Stop keeps waiting until the wait resource
	// has finished release.
	previous := result.resource
	var release func()
	if previous != nil {
		release = previous.Release
	}
	result.resource = NewLease(context.Background(), ReleaseOnCompletion, done, release)
	return result, nil
}

// LiveLeases returns the adapter's long-session lease capability or nil when
// unavailable. Missing receiver, cache, or unsupported interface leaves
// callers without this capability, while ordinary request admission remains a
// separate service operation.
func (s *ConcurrencyService) LiveLeases() LiveConcurrencyCache {
	if s == nil || s.cache == nil {
		return nil
	}
	cache, _ := s.cache.(LiveConcurrencyCache)
	return cache
}

// AcquireProviderSlot admits a provider request under runtime tracking and
// returns an idempotent cleanup handle. Nonpositive limits bypass physical
// cache admission but still participate in runtime ownership. Cache failures
// or denial end registration before the method returns.
//
// Confirmed cleanup owns cache release before runtime completion, and an
// acquisition racing with shutdown is returned immediately. A request lease
// from ctx also adopts provider cleanup, while standalone callers invoke the
// returned release themselves. Client cancellation follows the request's
// chosen release mode; an uncertain cache acquisition error cannot prove
// whether a remote member was created.
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
	// Return capacity when shutdown won after acquisition. Client cancellation
	// still follows the caller's chosen release mode through request ownership.
	if stopErr := s.runtime.Context().Err(); stopErr != nil {
		release()
		return nil, stopErr
	}
	ownRequestResource(ctx, release)
	result.ReleaseFunc = release
	return result, nil
}

// AcquireUserSlot admits a user request under runtime tracking and returns
// idempotent cleanup. Nonpositive limits bypass physical counting while
// retaining runtime registration, and denied or failed cache operations end
// that registration immediately.
//
// Confirmed release attempts cache removal before declaring runtime
// completion. Shutdown observed after acquisition returns the unused slot
// immediately; ordinary request cancellation follows the owner supplied by the
// caller. Unlike provider acquisition, this method returns its release handle
// without automatically attaching it to a request lease, so higher-level user
// admission adopts that resource.
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
	// Return capacity when shutdown won after acquisition. Client cancellation
	// still follows the caller's chosen release mode through request ownership.
	if stopErr := s.runtime.Context().Err(); stopErr != nil {
		release()
		return nil, stopErr
	}
	result.ReleaseFunc = release
	return result, nil
}

// TrackAPIKeySlot records statistics within a detached runtime operation and
// returns idempotent cleanup. Missing receiver or stopped runtime produces a
// no-op callback, while allowed tracking retains runtime registration until
// its cleanup is released by the request owner.
func (s *ConcurrencyService) TrackAPIKeySlot(ctx context.Context, id int64) func() {
	if s == nil {
		return func() {}
	}
	operation, done, err := s.runtime.Enter(context.WithoutCancel(nonNilContext(ctx)), fmt.Sprintf("apikey-slot:%d", id))
	if err != nil {
		// Shutdown rejected statistics work before tracking began. Return an empty
		// cleanup callback so this monitoring failure supplies no owned member.
		return func() {}
	}
	release := s.trackAPIKeySlot(operation, id)
	return NewLease(context.Background(), ReleaseOnCompletion, done, release).Release
}

// nonNilContext returns the supplied context or context.Background when it is
// absent. It lets detached monitoring and ingress operations apply their own
// budget while retaining values from any supplied request context.
func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
