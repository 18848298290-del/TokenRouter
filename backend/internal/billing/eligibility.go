package billing

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
)

// eligibility.go checks request funding eligibility and coordinates cached
// balance and spending updates.
// Funding prechecks combine Key source policy, selected subscription windows,
// cached balance and Key spending ceilings before the request coordinator
// applies RPM. Cache errors can start a per-user coalesced balance load with
// an independent timeout, while absent cache loads use the caller's context
// directly. Subscription checks clear eligible expired counters only in a
// local copy; durable maintenance and final charging belong to their owning
// services and transactions.
//
// Cache population uses a bounded worker queue. Full queues perform balance
// deductions and spending increments synchronously, but population and
// closed-pool tasks can be dropped. Stop joins workers and retains its closed
// channel during their drain, so callers quiesce producers before stopping. A
// separate breaker gates every precheck but changes state from balance-read
// outcomes; half-open admissions and successful subscription-only checks
// therefore have distinct effects.

const (
	// Cache operation kinds distinguish absolute balance population from
	// committed deductions and Key usage increments; workers select the
	// corresponding task fields when consuming a queued value.
	cacheWriteSetBalance           cacheWriteKind = iota // populate absolute user balance
	cacheWriteDeductBalance                              // subtract committed user amount
	cacheWriteUpdateRateLimitUsage                       // add committed Key spending

	// Worker and queue bounds limit asynchronous cache work. Operation deadlines
	// bound cooperative writes; drop logging and independent balance loads use
	// separate intervals rather than the request context.
	cacheWriteWorkerCount     = 10              // asynchronous cache worker count
	cacheWriteBufferSize      = 1000            // buffered cache task capacity
	cacheWriteTimeout         = 2 * time.Second // cooperative cache operation deadline
	cacheWriteDropLogInterval = 5 * time.Second // drop-summary throttle interval
	balanceLoadTimeout        = 3 * time.Second // independent balance-load deadline
)

//nolint:decorder // Separate const blocks keep each iota sequence starting at zero.
const (
	// Queue outcomes let callers separate accepted work from full capacity and
	// closed/unstarted pools, choosing synchronous updates or an observed drop
	// for each operation kind.
	cacheWriteEnqueued    cacheWriteEnqueueResult = iota // accepted buffered cache task
	cacheWriteQueueFull                                  // queue has no capacity
	cacheWriteQueueClosed                                // pool stopped or unstarted
)

//nolint:decorder // Separate const blocks keep each iota sequence starting at zero.
const (
	// Breaker states distinguish unrestricted checks, temporary rejection and a
	// bounded probe allowance. Balance-read outcomes decide when these shared
	// states close or reopen after admission.
	billingCircuitClosed   billingCircuitBreakerState = iota // unrestricted precheck admission state
	billingCircuitOpen                                       // reject until reset delay
	billingCircuitHalfOpen                                   // bounded precheck probe allowance
)

var (
	// ErrBillingServiceUnavailable reports balance-read failure or breaker
	// rejection, carrying a read cause when available. Callers receive
	// service-unavailable classification before interpreting a funding amount.
	ErrBillingServiceUnavailable = apperror.ServiceUnavailable("BILLING_SERVICE_ERROR", "Billing service temporarily unavailable. Please retry later.")

	// Funding and spending errors identify missing records, exhausted Key/window
	// quotas, unavailable preferred grants and insufficient balance. Adapters and
	// prechecks return these domain values for shared HTTP classification.
	ErrAPIKeyNotFound                    = apperror.NotFound("API_KEY_NOT_FOUND", "api key not found")
	ErrAPIKeyQuotaExhausted              = apperror.TooManyRequests("API_KEY_QUOTA_EXHAUSTED", "api key quota exhausted")
	ErrAPIKeyRateLimit1dExceeded         = apperror.TooManyRequests("API_KEY_RATE_1D_EXCEEDED", "The API key daily limit has been reached")
	ErrAPIKeyRateLimit5hExceeded         = apperror.TooManyRequests("API_KEY_RATE_5H_EXCEEDED", "The API key five-hour limit has been reached")
	ErrAPIKeyRateLimit7dExceeded         = apperror.TooManyRequests("API_KEY_RATE_7D_EXCEEDED", "The API key seven-day limit has been reached")
	ErrProviderNotFound                  = apperror.NotFound("PROVIDER_NOT_FOUND", "provider not found")
	ErrPreferredSubscriptionInsufficient = apperror.TooManyRequests("PREFERRED_SUBSCRIPTION_EXHAUSTED", "preferred subscription has insufficient remaining quota")
	ErrPreferredSubscriptionInvalid      = apperror.Forbidden("PREFERRED_SUBSCRIPTION_INVALID", "preferred subscription is unavailable")
	ErrUserNotFound                      = apperror.NotFound("USER_NOT_FOUND", "user not found")
	ErrInsufficientBalance               = apperror.BadRequest("INSUFFICIENT_BALANCE", "insufficient balance")
	ErrPreferredSubscriptionGroup        = apperror.Forbidden("PREFERRED_SUBSCRIPTION_GROUP_NOT_ALLOWED", "preferred subscription does not allow this group")
)

// cacheWriteKind identifies which cached financial observation a queued task
// updates.
type cacheWriteKind int

// cacheWriteEnqueueResult distinguishes accepted cache work from full or
// unavailable pool states.
type cacheWriteEnqueueResult int

// cacheWriteTask carries copied scalar arguments for one queued cache
// population or increment.
type cacheWriteTask struct {
	// kind, userID, apiKeyID, balance and amount describe one copied task
	// together. Population uses userID/balance, deduction uses userID/amount, and
	// spending uses apiKeyID/amount; other arguments are ignored for that kind.
	kind     cacheWriteKind // selected cached update operation
	userID   int64          // user balance cache identifier
	apiKeyID int64          // Key spending cache identifier
	balance  float64        // USD
	amount   float64        // USD
}

// Eligibility evaluates funding and spending prechecks and owns optional
// cache-write workers.
type Eligibility struct {
	// background is the first supplied runner and is used for Key-window
	// maintenance. Nil runs that callback inline; cache workers use their own
	// goroutines rather than this runner.
	background func(string, func()) // optional named maintenance task runner
	// cache may be absent, selecting direct balance/Key usage loads and no-op
	// queue methods. Failed balance cache reads use coalesced loads, while failed
	// Key loads can permit admission.
	cache BillingCache // optional billing observation cache
	// userRepo is required whenever GetUserBalance reaches storage and supplies a
	// nonnil successful record. Cached successful reads can avoid it; nil service
	// receivers are not usable database loaders.
	userRepo BalanceReader // persisted user balance reader
	// apiKeyRateLimitLoader supplies nonnil successful usage observations and can
	// expose optional ResetRateLimitWindows. Missing or failing usage loads
	// permit this spending precheck, with errors excluded from balance-breaker
	// outcomes.
	apiKeyRateLimitLoader APIKeyRateLimitLoader // optional persisted Key window reader
	// options must be callable during NewEligibility, which samples breaker
	// settings once. Reserve and date helpers query it later; one reserve lookup
	// can invoke it more than once.
	options func() EligibilityOptions // current eligibility option getter
	// observer receives nil-safe Printf calls from loads, worker updates and drop
	// summaries. Callers supply a concurrency-safe hook; breaker observation also
	// occurs while its state mutex is held.
	observer Observe // optional billing event observation hook
	// circuitBreaker is created from constructor settings and gates
	// CheckBillingEligibility before mode selection. Balance reads alone report
	// its success/failure outcomes; subscription-only success does not
	// necessarily close it.
	circuitBreaker *billingCircuitBreaker // optional all-mode admission breaker

	// cacheWriteChan is created under cacheWriteMu, closed while stopping and
	// left referenced during worker drain. Producers quiesce before Stop because
	// the early stopped check precedes enqueue's read lock.
	cacheWriteChan chan cacheWriteTask // optional worker task queue
	// cacheWriteStartOnce, cacheWriteStopOnce, cacheWriteWg, cacheWriteMu and
	// stopped coordinate one pool lifetime. The wait group joins workers only;
	// synchronous overflow writes and balance loads are outside its drain.
	cacheWriteStartOnce sync.Once      // single worker startup admission
	cacheWriteWg        sync.WaitGroup // asynchronous cache worker completion
	cacheWriteStopOnce  sync.Once      // single worker shutdown admission
	cacheWriteMu        sync.RWMutex   // queue publication and closure mutex
	stopped             atomic.Bool    // irreversible worker shutdown marker
	// balanceLoadSF groups cache-error loads by decimal user ID on this instance.
	// Its Do wait is independent of caller cancellation; no-cache loads bypass
	// this group entirely.
	balanceLoadSF singleflight.Group // per-instance user balance load coalescing
	// cacheWriteDropFullCount, cacheWriteDropFullLastLog,
	// cacheWriteDropClosedCount and cacheWriteDropClosedLastLog form independent
	// atomic reason counters/timestamps. Each winning log exchanges its reason's
	// count for zero after the throttle interval.
	cacheWriteDropFullCount     uint64 // full-queue dropped task count
	cacheWriteDropFullLastLog   int64  // Unix nanoseconds
	cacheWriteDropClosedCount   uint64 // closed-pool dropped task count
	cacheWriteDropClosedLastLog int64  // Unix nanoseconds
}

// billingCircuitBreakerState identifies shared admission rejection and bounded
// balance-recovery probe states.
type billingCircuitBreakerState int

// billingCircuitBreaker tracks balance-read outcomes while gating all funding
// precheck admissions.
type billingCircuitBreaker struct {
	// observer runs during locked transition handling. A configured callback
	// returns without reentering breaker methods, since those methods acquire the
	// same nonreentrant mutex.
	observer Observe // optional breaker transition observer
	// mu guards state, failures, openedAt and halfOpenRemaining after
	// construction. openedAt records system time.Now, and a successful balance
	// read clears the failure and probe allowance fields.
	mu       sync.Mutex                 // shared breaker state mutex
	state    billingCircuitBreakerState // current breaker admission state
	failures int                        // consecutive balance-read failure count
	openedAt time.Time                  // latest breaker opening time
	// failureThreshold, resetTimeout and halfOpenRequests are normalized
	// construction settings. They remain fixed while admission and balance
	// results mutate the protected state; later Eligibility option reads do not
	// replace them.
	failureThreshold int           // captured failure opening threshold
	resetTimeout     time.Duration // captured open rejection duration
	halfOpenRequests int           // captured probe admission allowance
	// halfOpenRemaining counts admissions still available, not currently running
	// reads. Allow consumes it before mode selection; subscription-only success
	// can leave that consumed allowance without a balance outcome.
	halfOpenRemaining int // remaining half-open admission count
}

// NewEligibility retains funding/cache dependencies and samples callable
// options to construct its optional breaker. It starts no workers; callers
// Start before queue use and quiesce producers before Stop. Only the first
// background runner is retained, with nil selecting inline Key maintenance.
func NewEligibility(cache BillingCache, users BalanceReader, keys APIKeyRateLimitLoader, options func() EligibilityOptions, observe Observe, background ...func(string, func())) *Eligibility {
	s := &Eligibility{cache: cache, userRepo: users, apiKeyRateLimitLoader: keys, options: options, observer: observe}
	if len(background) > 0 {
		s.background = background[0]
	}
	s.circuitBreaker = newBillingCircuitBreaker(options().Billing.CircuitBreaker)
	if s.circuitBreaker != nil {
		s.circuitBreaker.observer = observe
	}
	return s
}

// Start admits worker creation once after dependencies are usable,
// coordinating queue publication under the write mutex. A prior Stop seals
// startup permanently; repeated starts do not allocate another pool. The
// constructor itself does not start background work.
//
// The same mutex orders publication against Stop's queue snapshot, and stopped
// prevents an unstarted pool from being recreated during shutdown. Callers
// retain service lifetime ownership and stop producers before joining its
// workers. This start method does not register those goroutines with the
// injected maintenance runner.
func (s *Eligibility) Start() {
	s.cacheWriteStartOnce.Do(func() {
		// Publish the queue while holding the startup/stop mutex, so Stop snapshots
		// either the created pool or the sealed unstarted state.
		s.cacheWriteMu.Lock()
		defer s.cacheWriteMu.Unlock()
		if !s.stopped.Load() {
			s.startCacheWriteWorkers()
		}
	})
}

// Stop seals worker startup, closes the published task channel and waits for
// its workers to drain accepted tasks. Calls are idempotent, and callers
// quiesce producers first. The channel pointer remains nonnil until draining
// finishes; synchronous full-queue writes and coalesced balance loads are
// outside this wait.
//
// An enqueuer can observe stopped=false before acquiring its read lock, then
// arrive after the channel close. Because that path does not recheck stopped
// inside the lock, overlapping producer shutdown can reach a closed-channel
// send. Caller quiescence is therefore required; normal app shutdown orders
// completion consumption before this pool, and this method supplies no
// deadline for worker cooperation.
func (s *Eligibility) Stop() {
	s.cacheWriteStopOnce.Do(func() {
		// Seal startup and future stopped observations before closing the queue;
		// producers already past their early check must have quiesced before this
		// close/drain sequence.
		s.stopped.Store(true)

		s.cacheWriteMu.Lock()
		ch := s.cacheWriteChan
		if ch != nil {
			close(ch)
		}
		s.cacheWriteMu.Unlock()

		if ch == nil {
			return
		}
		// Join accepted worker work before clearing its channel reference. This wait
		// excludes synchronous overflow and singleflight loads and relies on
		// cooperative worker operations returning.
		s.cacheWriteWg.Wait()

		s.cacheWriteMu.Lock()
		if s.cacheWriteChan == ch {
			s.cacheWriteChan = nil
		}
		s.cacheWriteMu.Unlock()
	})
}

// startCacheWriteWorkers publishes a bounded queue and launches ten consumers
// while Start holds the queue write mutex. The caller invokes it during the
// single admitted startup; Stop uses the same mutex to snapshot that queue.
//
// Each consumer enters the wait group before launch, so a subsequent Stop joins
// every created worker as it drains accepted tasks. Publishing and registering
// workers before releasing the mutex prevents shutdown from observing a queue
// whose consumer registration is still incomplete.
func (s *Eligibility) startCacheWriteWorkers() {
	ch := make(chan cacheWriteTask, cacheWriteBufferSize)
	s.cacheWriteChan = ch
	// Register each consumer before launch so Stop's wait includes every created
	// worker and its accepted channel drain after closure.
	for range cacheWriteWorkerCount {
		s.cacheWriteWg.Add(1)
		go s.cacheWriteWorker(ch)
	}
}

// tryEnqueueCacheWrite attempts a nonblocking task send and reports accepted,
// full or closed/unstarted pool. The caller decides whether capacity overflow
// needs synchronous work; this helper never performs the update itself. Queue
// access uses a read lock, while stopped is observed before that lock.
//
// Callers quiesce producers before Stop: a false stopped observation can
// become stale while waiting to acquire the lock, and the draining closed
// channel remains referenced. The lock excludes simultaneous closure during an
// admitted send but does not make that earlier observation a universal
// concurrent-shutdown guarantee.
func (s *Eligibility) tryEnqueueCacheWrite(task cacheWriteTask) cacheWriteEnqueueResult {
	if s.stopped.Load() {
		return cacheWriteQueueClosed
	}

	// Hold the read lock through the attempted send to exclude simultaneous
	// closure; the earlier stopped observation still needs caller producer
	// quiescence around Stop.
	s.cacheWriteMu.RLock()
	defer s.cacheWriteMu.RUnlock()

	if s.cacheWriteChan == nil {
		return cacheWriteQueueClosed
	}

	select {
	case s.cacheWriteChan <- task:
		return cacheWriteEnqueued
	default:
		// Return capacity exhaustion without choosing an update policy; committed
		// increments use synchronous work while absolute population can be dropped
		// by the caller.
		return cacheWriteQueueFull
	}
}

// enqueueCacheWrite reports whether population work entered the queue and
// observes full/closed drops otherwise. It performs no synchronous update, so
// a dropped cache fill leaves the successful loaded balance available to the
// requesting caller.
func (s *Eligibility) enqueueCacheWrite(task cacheWriteTask) (enqueued bool) {
	switch s.tryEnqueueCacheWrite(task) {
	case cacheWriteEnqueued:
		return true
	case cacheWriteQueueFull:
		s.logCacheWriteDrop(task, "full")
	case cacheWriteQueueClosed:
		s.logCacheWriteDrop(task, "closed")
	}
	return false
}

// cacheWriteWorker drains accepted tasks until its channel closes and then
// completes its wait-group entry. Every task receives a fresh background
// timeout, independently of the originating request's cancellation.
// Population, deduction and spending operations use their kind-specific scalar
// arguments.
//
// Operation errors are passed to optional observation and do not stop later
// tasks or retry that update. Cancellation is released after each operation
// returns; timeout bounds cooperative implementations rather than forcibly
// interrupting them. Stop therefore waits for worker calls to return,
// including buffered accepted work.
func (s *Eligibility) cacheWriteWorker(ch <-chan cacheWriteTask) {
	defer s.cacheWriteWg.Done()
	for task := range ch {
		// Give each accepted task its own background deadline and release
		// cancellation after return; request cancellation cannot remove work already
		// accepted by this queue.
		ctx, cancel := context.WithTimeout(context.Background(), cacheWriteTimeout)
		switch task.kind {
		case cacheWriteSetBalance:
			s.setBalanceCache(ctx, task.userID, task.balance)
		case cacheWriteDeductBalance:
			if s.cache != nil {
				// Observe this post-charge cache failure and continue later tasks; retry
				// and durable charge rollback are outside this worker's operation.
				if err := s.cache.DeductUserBalance(ctx, task.userID, task.amount); err != nil {
					s.observer.Printf("service.billing_cache", "Warning: deduct balance cache failed for user %d: %v", task.userID, err)
				}
			}
		case cacheWriteUpdateRateLimitUsage:
			if s.cache != nil {
				// Observe the failed cached increment without stopping the queue, leaving
				// subsequent accepted observations eligible for their own independent
				// operation.
				if err := s.cache.UpdateAPIKeyRateLimitUsage(ctx, task.apiKeyID, task.amount); err != nil {
					s.observer.Printf("service.billing_cache", "Warning: update rate limit usage cache failed for api key %d: %v", task.apiKeyID, err)
				}
			}
		}
		cancel()
	}
}

// cacheWriteKindName returns the cache operation's drop-log label or unknown
// for unrecognized kinds. It only formats observation metadata and does not
// select or execute any cache update.
func cacheWriteKindName(kind cacheWriteKind) string {
	switch kind {
	case cacheWriteSetBalance:
		return "set_balance"
	case cacheWriteDeductBalance:
		return "deduct_balance"
	case cacheWriteUpdateRateLimitUsage:
		return "update_rate_limit_usage"
	default:
		return "unknown"
	}
}

// logCacheWriteDrop accumulates a full/closed reason's dropped count and emits
// a throttled summary when its timestamp compare-and-swap wins. Unknown
// reasons do nothing. Separate reason counters allow one saturated queue
// condition to be reported independently of the other.
//
// The clock comes from DateRuntime and is stored in Unix nanoseconds.
// Concurrent contenders can increment while the winner exchanges the count for
// zero, so that summary reports the captured count and latest triggering task
// rather than a per-task history. Optional observation receives the summary
// after its atomic exchange.
func (s *Eligibility) logCacheWriteDrop(task cacheWriteTask, reason string) {
	var (
		countPtr *uint64
		lastPtr  *int64
	)
	switch reason {
	case "full":
		countPtr = &s.cacheWriteDropFullCount
		lastPtr = &s.cacheWriteDropFullLastLog
	case "closed":
		countPtr = &s.cacheWriteDropClosedCount
		lastPtr = &s.cacheWriteDropClosedLastLog
	default:
		return
	}

	// Increment this reason's atomic count before racing for its log timestamp;
	// only the compare-and-swap winner exchanges the accumulated count. Increments
	// after that exchange remain available for a later summary.
	atomic.AddUint64(countPtr, 1)
	now := s.dateRuntime().now().UnixNano()
	last := atomic.LoadInt64(lastPtr)
	if now-last < int64(cacheWriteDropLogInterval) {
		return
	}
	if !atomic.CompareAndSwapInt64(lastPtr, last, now) {
		return
	}
	dropped := atomic.SwapUint64(countPtr, 0)
	if dropped == 0 {
		return
	}
	s.observer.Printf("service.billing_cache", "Warning: cache write queue %s, dropped %d tasks in last %s (latest kind=%s user %d)",
		reason,
		dropped,
		cacheWriteDropLogInterval,
		cacheWriteKindName(task.kind),
		task.userID,
	)
}

// GetUserBalance returns a successful cache balance or loads the user's
// persisted amount after any cache error. An initialized service and usable
// successful storage reader are required on that load path. Without cache it
// reads directly with caller context and bypasses coalescing.
//
// With cache, same-user errors share this instance's singleflight Do load
// using an independent three-second background context. Caller cancellation
// does not cancel that shared load or its Do wait. Successful storage values
// can be returned even when queued population is dropped; storage failure
// propagates, and an unexpected shared value type becomes an error.
func (s *Eligibility) GetUserBalance(ctx context.Context, userID int64) (float64, error) {
	// Without cache, use the caller context directly rather than the independent
	// shared load. The database helper still requires a usable nonnil service and
	// reader.
	if s == nil || s.cache == nil {
		return s.getUserBalanceFromDB(ctx, userID)
	}

	balance, err := s.cache.GetUserBalance(ctx, userID)
	if err == nil {
		return balance, nil
	}

	// Coalesce this instance's same-user cache errors under a background load
	// budget; callers wait through Do, so their canceled request does not cancel
	// the shared read.
	value, err, _ := s.balanceLoadSF.Do(strconv.FormatInt(userID, 10), func() (any, error) {
		loadCtx, cancel := context.WithTimeout(context.Background(), balanceLoadTimeout)
		defer cancel()

		balance, err := s.getUserBalanceFromDB(loadCtx, userID)
		if err != nil {
			return nil, err
		}

		// Population may be dropped by a full or unavailable pool. Keep the
		// successfully loaded balance as this request's result regardless of that
		// cache admission outcome.
		_ = s.enqueueCacheWrite(cacheWriteTask{
			kind:    cacheWriteSetBalance,
			userID:  userID,
			balance: balance,
		})
		return balance, nil
	})
	if err != nil {
		return 0, err
	}
	balance, ok := value.(float64)
	if !ok {
		return 0, fmt.Errorf("unexpected balance type: %T", value)
	}
	return balance, nil
}

// getUserBalanceFromDB reads the supplied user's stored balance and wraps read
// failures with their cause. It requires an initialized service, usable reader
// and nonnil successful user result, then returns that result's Balance
// without cache access.
func (s *Eligibility) getUserBalanceFromDB(ctx context.Context, userID int64) (float64, error) {
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("get user balance: %w", err)
	}
	return user.Balance, nil
}

// setBalanceCache attempts absolute cache population and passes failures to
// the optional observer. Nil service/cache is a no-op; it returns no write
// result and does not retry or undo the successful storage load that supplied
// this balance.
func (s *Eligibility) setBalanceCache(ctx context.Context, userID int64, balance float64) {
	if s == nil || s.cache == nil {
		return
	}
	// Cache fill follows a successful storage observation. Pass its failure to
	// optional logging, leaving the loaded value usable rather than turning
	// population into a failed read.
	if err := s.cache.SetUserBalance(ctx, userID, balance); err != nil {
		s.observer.Printf("service.billing_cache", "Warning: set balance cache failed for user %d: %v", userID, err)
	}
}

// DeductBalanceCache synchronously delegates a cached-balance subtraction
// using the supplied context. Nil service/cache returns nil; amounts pass
// through unchanged, and the caller decides how to handle a cache update
// error.
func (s *Eligibility) DeductBalanceCache(ctx context.Context, userID int64, amount float64) error {
	if s == nil || s.cache == nil {
		return nil
	}
	return s.cache.DeductUserBalance(ctx, userID, amount)
}

// QueueDeductBalance admits a copied deduction task when workers are available
// and capacity remains. Nil service/cache is a no-op; closed or unstarted
// pools observe a drop, while full capacity attempts the deduction
// synchronously with an independent two-second timeout.
//
// The synchronous operation's error is observed and then discarded, keeping
// cache work separate from durable billing. This path is outside Stop's worker
// wait group, so callers quiesce both enqueueing and overflow writers before
// shutdown. The method neither validates the amount nor retries failures.
func (s *Eligibility) QueueDeductBalance(userID int64, amount float64) {
	if s == nil || s.cache == nil {
		return
	}
	task := cacheWriteTask{
		kind:   cacheWriteDeductBalance,
		userID: userID,
		amount: amount,
	}
	switch s.tryEnqueueCacheWrite(task) {
	case cacheWriteEnqueued:
		return
	case cacheWriteQueueClosed:
		s.logCacheWriteDrop(task, "closed")
		return
	}
	// Capacity overflow performs this committed deduction synchronously with an
	// independent deadline; it is outside the worker wait group and failures
	// remain optional observations.
	ctx, cancel := context.WithTimeout(context.Background(), cacheWriteTimeout)
	defer cancel()
	if err := s.DeductBalanceCache(ctx, userID, amount); err != nil {
		s.observer.Printf("service.billing_cache", "Warning: deduct balance cache fallback failed for user %d: %v", userID, err)
	}
}

// InvalidateUserBalance removes the user's cached amount using the supplied
// context, returning any invalidation error after optional observation. Nil
// service/cache is a no-op; successful invalidation makes a later balance
// check eligible for a storage reload.
func (s *Eligibility) InvalidateUserBalance(ctx context.Context, userID int64) error {
	if s == nil || s.cache == nil {
		return nil
	}
	// Remove this observation before a subsequent precheck can reuse it; a
	// returned invalidation error lets the caller report that the cached value
	// may remain.
	if err := s.cache.InvalidateUserBalance(ctx, userID); err != nil {
		s.observer.Printf("service.billing_cache", "Warning: invalidate balance cache failed for user %d: %v", userID, err)
		return err
	}
	return nil
}

// InvalidateAPIKeyRateLimit removes the Key's cached window observations using
// the supplied context. Nil service/cache is a no-op; failures are observed
// and returned, leaving their handling and any retry to the caller.
func (s *Eligibility) InvalidateAPIKeyRateLimit(ctx context.Context, keyID int64) error {
	if s == nil || s.cache == nil {
		return nil
	}
	// Discard the old spending snapshot so a later read can load current windows;
	// failed invalidation remains visible to callers rather than guaranteeing
	// that reload.
	if err := s.cache.InvalidateAPIKeyRateLimit(ctx, keyID); err != nil {
		s.observer.Printf("service.billing_cache", "Warning: invalidate api key rate limit cache failed for key %d: %v", keyID, err)
		return err
	}
	return nil
}

// checkAPIKeyRateLimits evaluates the supplied nonnil Key's spending windows
// through cache or storage observations. A missing loader or storage read
// failure permits this check; cache failure selects the loader, and successful
// nonnil loaded data is encoded into a best-effort cache entry.
//
// Cached positive Unix-second starts decode into times, while
// absent/nonpositive starts remain nil. evaluateRateLimits clears
// elapsed/absent usage locally before testing ceilings and requests optional
// maintenance. This path does not drive the balance breaker, and cache
// population failure does not replace the usable loaded observation.
func (s *Eligibility) checkAPIKeyRateLimits(ctx context.Context, apiKey *KeySnapshot) error {
	if s == nil || s.cache == nil {
		// Absent cache selects the supplied usage loader directly. If that optional
		// dependency is absent, this spending precheck permits the request without a
		// usage observation.
		if s.apiKeyRateLimitLoader == nil {
			return nil
		}
		data, err := s.apiKeyRateLimitLoader.GetRateLimitData(ctx, apiKey.ID)
		if err != nil {
			// Unavailable persisted usage does not block this Key-window precheck,
			// including returned context errors; funding and its balance breaker retain
			// their separate decisions.
			return nil
		}
		return s.evaluateRateLimits(ctx, apiKey, data.Usage5h, data.Usage1d, data.Usage7d,
			data.Window5hStart, data.Window1dStart, data.Window7dStart)
	}

	cacheData, err := s.cache.GetAPIKeyRateLimit(ctx, apiKey.ID)
	if err != nil {
		// Any cache read error discards its companion data and selects persisted
		// usage. Missing or failing loader data permits this check rather than
		// converting a cache miss into balance unavailability.
		if s.apiKeyRateLimitLoader == nil {
			return nil
		}
		dbData, dbErr := s.apiKeyRateLimitLoader.GetRateLimitData(ctx, apiKey.ID)
		if dbErr != nil {
			// Unavailable persisted usage does not block this Key-window precheck,
			// including returned context errors; funding and its balance breaker retain
			// their separate decisions.
			return nil
		}
		// Encode loaded optional timestamps as whole epoch seconds for Redis; absent
		// starts remain zero while fractional seconds are omitted from the cache
		// representation.
		cacheEntry := &APIKeyRateLimitCacheData{
			Usage5h: dbData.Usage5h,
			Usage1d: dbData.Usage1d,
			Usage7d: dbData.Usage7d,
		}
		if dbData.Window5hStart != nil {
			cacheEntry.Window5h = dbData.Window5hStart.Unix()
		}
		if dbData.Window1dStart != nil {
			cacheEntry.Window1d = dbData.Window1dStart.Unix()
		}
		if dbData.Window7dStart != nil {
			cacheEntry.Window7d = dbData.Window7dStart.Unix()
		}
		// Cache population is best effort after a usable usage load. Retain the
		// constructed snapshot for this check even when Redis cannot store it.
		_ = s.cache.SetAPIKeyRateLimit(ctx, apiKey.ID, cacheEntry)
		cacheData = cacheEntry
	}

	// Decode only positive epoch seconds as window anchors; zero or negative
	// encodings become absent and trigger evaluateRateLimits' expired-window
	// handling.
	var w5h, w1d, w7d *time.Time
	if cacheData.Window5h > 0 {
		t := time.Unix(cacheData.Window5h, 0)
		w5h = &t
	}
	if cacheData.Window1d > 0 {
		t := time.Unix(cacheData.Window1d, 0)
		w1d = &t
	}
	if cacheData.Window7d > 0 {
		t := time.Unix(cacheData.Window7d, 0)
		w7d = &t
	}
	return s.evaluateRateLimits(ctx, apiKey, cacheData.Usage5h, cacheData.Usage1d, cacheData.Usage7d, w5h, w1d, w7d)
}

// evaluateRateLimits checks five-hour, daily and weekly usage in that error
// precedence after locally treating absent/elapsed anchors as zero use. It
// requests maintenance whenever any anchor expires, even for a disabled
// ceiling. Positive ceilings reject usage at equality; nonpositive ceilings do
// not constrain the result.
//
// Maintenance runs through the injected runner or inline, with an independent
// timeout. A supported ResetRateLimitWindows is attempted before cache
// invalidation, which still runs after reset failure; both errors are observed
// and discarded. That best-effort work does not turn this local snapshot check
// into persisted reset confirmation.
func (s *Eligibility) evaluateRateLimits(ctx context.Context, apiKey *KeySnapshot, usage5h, usage1d, usage7d float64, w5h, w1d, w7d *time.Time) error {
	needsReset := false

	// Absent or elapsed anchors make only these copied usage values zero for
	// admission. Persisted counters are handled separately, so this precheck does
	// not confirm a database reset.
	if IsWindowExpired(w5h, RateLimitWindow5h) {
		usage5h = 0
		needsReset = true
	}
	if IsWindowExpired(w1d, RateLimitWindow1d) {
		usage1d = 0
		needsReset = true
	}
	if IsWindowExpired(w7d, RateLimitWindow7d) {
		usage7d = 0
		needsReset = true
	}

	// Request best-effort maintenance after any elapsed anchor. Capture the Key
	// ID for later execution; a missing runner instead performs the callback
	// inline before limit comparison.
	if needsReset {
		keyID := apiKey.ID
		s.runBackground("service/billing_cache_service.go:evaluateRateLimits", func() {
			// Maintenance receives an independent two-second budget because local
			// admission has already selected its snapshot; request cancellation does
			// not undo that observed reset decision.
			resetCtx, cancel := context.WithTimeout(context.Background(), cacheWriteTimeout)
			defer cancel()
			if s.apiKeyRateLimitLoader != nil {
				// A loader may expose reset persistence in addition to reads. Attempt that
				// supported operation first, then continue to invalidation even if reset
				// returned an error.
				if loader, ok := s.apiKeyRateLimitLoader.(interface {
					// ResetRateLimitWindows attempts persisted maintenance for this Key using
					// the supplied independent context. evaluateRateLimits observes errors
					// and still attempts cache invalidation, so success is not required for
					// its local usage comparison.
					ResetRateLimitWindows(ctx context.Context, id int64) error
				}); ok {
					// A failed optional persisted reset is observed but does not prevent
					// cache invalidation; the local usage decision remains independent of
					// maintenance success.
					if err := loader.ResetRateLimitWindows(resetCtx, keyID); err != nil {
						s.observer.Printf("service.billing_cache", "Warning: reset rate limit windows failed for api key %d: %v", keyID, err)
					}
				}
			}
			// Attempt invalidation after optional reset handling so a later request can
			// reload persisted windows; failure is observed and leaves that reload
			// unconfirmed.
			if s.cache != nil {
				if err := s.cache.InvalidateAPIKeyRateLimit(resetCtx, keyID); err != nil {
					s.observer.Printf("service.billing_cache", "Warning: invalidate rate limit cache failed for api key %d: %v", keyID, err)
				}
			}
		})
	}

	// Check five-hour, daily and weekly ceilings in fixed precedence; equality
	// already exhausts a positive ceiling before another request is admitted.
	if apiKey.RateLimit5h > 0 && usage5h >= apiKey.RateLimit5h {
		return ErrAPIKeyRateLimit5hExceeded
	}
	if apiKey.RateLimit1d > 0 && usage1d >= apiKey.RateLimit1d {
		return ErrAPIKeyRateLimit1dExceeded
	}
	if apiKey.RateLimit7d > 0 && usage7d >= apiKey.RateLimit7d {
		return ErrAPIKeyRateLimit7dExceeded
	}
	return nil
}

// QueueUpdateAPIKeyRateLimitUsage queues a copied spending increment when
// capacity remains. Nil service/cache is a no-op; closed or unstarted pools
// observe a drop, and full capacity uses synchronous cache update with an
// independent two-second timeout.
//
// Failures are observed without a returned error or retry. The synchronous
// writer is outside the worker drain, so producer quiescence includes this
// overflow path before Stop. Amounts are forwarded as supplied, with durable
// charging owned by settlement.
func (s *Eligibility) QueueUpdateAPIKeyRateLimitUsage(apiKeyID int64, cost float64) {
	if s == nil || s.cache == nil {
		return
	}
	task := cacheWriteTask{
		kind:     cacheWriteUpdateRateLimitUsage,
		apiKeyID: apiKeyID,
		amount:   cost,
	}
	switch s.tryEnqueueCacheWrite(task) {
	case cacheWriteEnqueued:
		return
	case cacheWriteQueueClosed:
		s.logCacheWriteDrop(task, "closed")
		return
	}
	// Capacity overflow attempts the committed spending increment synchronously
	// under its own deadline, keeping this work outside the worker drain and any
	// request-context cancellation.
	ctx, cancel := context.WithTimeout(context.Background(), cacheWriteTimeout)
	defer cancel()
	if err := s.cache.UpdateAPIKeyRateLimitUsage(ctx, apiKeyID, cost); err != nil {
		s.observer.Printf("service.billing_cache", "Warning: update rate limit usage cache fallback failed for api key %d: %v", apiKeyID, err)
	}
}

// CheckBillingEligibility combines Key source policy, selected grant windows,
// balance and Key spending ceilings. It requires an initialized service and a
// nonnil payer whenever a balance path is reached; platform is unused. The
// breaker gates admission before source-policy selection, including requests
// funded wholly by a subscription.
//
// Subscription mode first requires a snapshot, checks group coverage when
// supplied, and maps its first quota/state failure to
// preferred-insufficient/invalid. Balance mode discards the snapshot. The
// subsequent common subscription branch evaluates a selected snapshot again: a
// quota failure there reads balance, even after a preferred-mode first check
// passed. Separate clocks/calendars can therefore make these observations
// differ across the two checks.
//
// Auto requests can use balance after quota exhaustion but propagate other
// subscription failures. After funding passes, positive Key spending ceilings
// apply to every mode. Successful balance reads update breaker recovery before
// the reserve comparison; successful subscription-only checks may consume a
// half-open allowance without reporting a balance result.
func (s *Eligibility) CheckBillingEligibility(ctx context.Context, user *UserSummary, apiKey *KeySnapshot, group *GroupSnapshot, subscription *UserSubscription, platform string) error {
	// Consume breaker admission before selecting a funding source, so
	// open/half-open policy can reject subscription-funded requests even when no
	// balance read would follow.
	if s.circuitBreaker != nil && !s.circuitBreaker.Allow() {
		return ErrBillingServiceUnavailable
	}

	billingMode := effectiveKeyBillingMode(apiKey)
	if billingMode == APIKeyBillingModeSubscription {
		// When final group data is supplied, require this preferred grant to cover
		// it before quota checks; a nil group leaves that scope comparison to the
		// admission caller.
		if subscription == nil {
			return ErrPreferredSubscriptionInvalid
		}
		if group != nil && !SubscriptionAllowsGroup(subscription, group.ID) {
			return ErrPreferredSubscriptionGroup
		}
		if err := CheckEffectiveSubscriptionEligibility(subscription, s.dateRuntime().calendar()); err != nil {
			if IsSubscriptionQuotaExceeded(err) {
				return ErrPreferredSubscriptionInsufficient
			}
			return ErrPreferredSubscriptionInvalid
		}
	}
	if billingMode == APIKeyBillingModeBalance {
		// Balance mode discards any grant snapshot so its ordinary branch reads
		// payer balance instead of accepting an unrelated subscription's available
		// quota.
		subscription = nil
	}

	// The common branch reevaluates a selected grant after mode checks. A quota
	// failure here can reach balance even for a previously passed preferred check
	// if observations changed.
	if subscription != nil {
		if err := CheckEffectiveSubscriptionEligibility(subscription, s.dateRuntime().calendar()); err != nil {
			if !IsSubscriptionQuotaExceeded(err) {
				return err
			}
			if err := s.checkBalanceEligibility(ctx, user.ID); err != nil {
				return err
			}
		}
	} else {
		if err := s.checkBalanceEligibility(ctx, user.ID); err != nil {
			return err
		}
	}

	// Apply Key spending ceilings after funding succeeds, regardless of its
	// chosen source, so a usable subscription does not bypass the Key's own
	// billed-usage policy.
	if apiKey != nil && apiKey.HasRateLimits() {
		if err := s.checkAPIKeyRateLimits(ctx, apiKey); err != nil {
			return err
		}
	}

	return nil
}

// CheckEffectiveSubscriptionEligibility rejects absent or currently
// ineffective grants, then tests remaining quota using a value copy. Deletion,
// pending, suspension and expiry all map to subscription-invalid; status uses
// wall clock independently of the supplied calendar.
//
// Eligible daily/weekly/monthly resets clear only the copied usage counters,
// retaining the caller's original snapshot and pointers. Limit checking with
// zero additional cost requires some amount to remain in each positive
// configured window. This helper neither persists those resets nor verifies
// owner/group identity, and its separate time observations are not one frozen
// request instant.
func CheckEffectiveSubscriptionEligibility(subscription *UserSubscription, calendar timezone.Calendar) error {
	if subscription == nil {
		return ErrSubscriptionInvalid
	}
	switch subscription.EffectiveStatus(time.Now()) {
	case SubscriptionStatusExpired:
		return ErrSubscriptionInvalid
	case SubscriptionStatusSuspended:
		return ErrSubscriptionInvalid
	case SubscriptionStatusPending:
		return ErrSubscriptionInvalid
	case SubscriptionStatusRevoked:
		return ErrSubscriptionInvalid
	}

	// Clear stale usage only in this value copy for the precheck; the original
	// counters and pointer fields remain available for separate persisted
	// maintenance and final charging.
	effective := *subscription
	if effective.NeedsDailyReset(calendar) {
		effective.DailyUsageUSD = 0
	}
	if effective.NeedsWeeklyReset() {
		effective.WeeklyUsageUSD = 0
	}
	if effective.NeedsMonthlyReset() {
		effective.MonthlyUsageUSD = 0
	}

	return CheckSubscriptionUsageLimits(&effective, 0)
}

// IsSubscriptionQuotaExceeded reports an error matching a daily, weekly or
// monthly quota sentinel through errors.Is. Other errors and nil return false,
// allowing funding selection to separate exhaustion from invalid subscription
// state.
func IsSubscriptionQuotaExceeded(err error) bool {
	return errors.Is(err, ErrDailyLimitExceeded) ||
		errors.Is(err, ErrWeeklyLimitExceeded) ||
		errors.Is(err, ErrMonthlyLimitExceeded)
}

// MinimumBalanceReserve reads a configured extra USD reserve, returning zero
// for absent options or a nonpositive first observation. The getter may run
// twice, so callers supplying changing options do not receive one frozen
// configuration observation.
func (s *Eligibility) MinimumBalanceReserve() float64 {
	if s == nil || s.options == nil || s.options().Billing.MinimumBalanceReserve <= 0 {
		return 0
	}
	return s.options().Billing.MinimumBalanceReserve
}

// BalanceBelowEligibilityThreshold reports nonpositive balance or a positive
// amount strictly below the configured reserve. Equality at a positive reserve
// passes; the amount uses ordinary floating-point comparisons with no separate
// finiteness validation.
func (s *Eligibility) BalanceBelowEligibilityThreshold(balance float64) bool {
	if balance <= 0 {
		return true
	}
	minimumReserve := s.MinimumBalanceReserve()
	return minimumReserve > 0 && balance < minimumReserve
}

// checkBalanceEligibility returns a balance-read service error or
// insufficient-balance result for the payer. Read failure reports breaker
// failure and keeps its cause; any successful read reports recovery before
// applying the zero/reserve threshold.
//
// A readable under-reserve balance can close the breaker while still denying
// this funding check. Cache and direct/coalesced storage behavior come from
// GetUserBalance; subscription-only paths bypassing balance skip these outcome
// callbacks altogether.
func (s *Eligibility) checkBalanceEligibility(ctx context.Context, userID int64) error {
	balance, err := s.GetUserBalance(ctx, userID)
	if err != nil {
		if s.circuitBreaker != nil {
			s.circuitBreaker.OnFailure(err)
		}
		s.observer.Printf("service.billing_cache", "ALERT: billing balance check failed for user %d: %v", userID, err)
		return ErrBillingServiceUnavailable.WithCause(err)
	}
	if s.circuitBreaker != nil {
		s.circuitBreaker.OnSuccess()
	}

	if s.BalanceBelowEligibilityThreshold(balance) {
		return ErrInsufficientBalance
	}

	return nil
}

// newBillingCircuitBreaker returns nil when disabled or initializes a closed
// breaker from copied settings. Nonpositive failure threshold and probe
// allowance select five and one; a nonpositive computed seconds duration
// selects thirty seconds. No worker or timer starts here.
func newBillingCircuitBreaker(cfg CircuitBreakerOptions) *billingCircuitBreaker {
	if !cfg.Enabled {
		return nil
	}
	// Convert configured seconds into the captured elapsed rejection period.
	resetTimeout := time.Duration(cfg.ResetTimeoutSeconds) * time.Second
	if resetTimeout <= 0 {
		resetTimeout = 30 * time.Second
	}
	halfOpen := cfg.HalfOpenRequests
	if halfOpen <= 0 {
		halfOpen = 1
	}
	threshold := cfg.FailureThreshold
	if threshold <= 0 {
		threshold = 5
	}
	return &billingCircuitBreaker{
		state:            billingCircuitClosed,
		failureThreshold: threshold,
		resetTimeout:     resetTimeout,
		halfOpenRequests: halfOpen,
	}
}

// Allow decides admission under the state mutex for a nonnil breaker. Closed
// state admits freely; open state rejects until system elapsed time
// reaches the captured reset delay, then initializes a half-open allowance and
// consumes one admission from it. Unknown states reject.
//
// Half-open calls consume allowance even when later funding does not read
// balance. OnSuccess/OnFailure drive subsequent state changes, so
// subscription-only success need not close the breaker. Observer calls occur
// while locked and must return without reentering these methods; the allowance
// limits admission, not a separate tracked set of in-flight I/O probes.
func (b *billingCircuitBreaker) Allow() bool {
	// Serialize state and allowance changes together so concurrent half-open
	// callers consume one shared count; outcome callbacks later act on this same
	// evolving state.
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case billingCircuitClosed:
		return true
	case billingCircuitOpen:
		if time.Since(b.openedAt) < b.resetTimeout {
			return false
		}
		b.state = billingCircuitHalfOpen
		b.halfOpenRemaining = b.halfOpenRequests
		b.observer.Printf("service.billing_cache", "ALERT: billing circuit breaker entering half-open state")
		fallthrough
	case billingCircuitHalfOpen:
		if b.halfOpenRemaining <= 0 {
			return false
		}
		b.halfOpenRemaining--
		return true
	default:
		return false
	}
}

// OnFailure records a balance-read error under the state mutex, tolerating a
// nil breaker. Open state keeps its existing timestamp; half-open failure
// reopens immediately with a new wall time and zero allowance. Otherwise
// failure count advances and reaching the threshold opens the breaker.
//
// The observer sees transitions while locked and therefore avoids reentry.
// This method does not cancel already admitted requests or tie an error to a
// generation-specific probe token; an outcome updates whichever shared state
// exists when the callback acquires the mutex.
func (b *billingCircuitBreaker) OnFailure(err error) {
	if b == nil {
		return
	}
	// Record the failure against the state present at lock acquisition, including
	// a state changed by other admitted reads; no generation token filters this
	// outcome.
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case billingCircuitOpen:
		return
	case billingCircuitHalfOpen:
		b.state = billingCircuitOpen
		b.openedAt = time.Now()
		b.halfOpenRemaining = 0
		b.observer.Printf("service.billing_cache", "ALERT: billing circuit breaker opened after half-open failure: %v", err)
		return
	default:
		b.failures++
		if b.failures >= b.failureThreshold {
			b.state = billingCircuitOpen
			b.openedAt = time.Now()
			b.halfOpenRemaining = 0
			b.observer.Printf("service.billing_cache", "ALERT: billing circuit breaker opened after %d failures: %v", b.failures, err)
		}
	}
}

// OnSuccess closes the current breaker and clears failures and half-open
// allowance under the mutex, tolerating nil. Eligibility calls it after
// successful balance reads even if that balance later fails its reserve
// threshold. A late read can update shared state after other outcomes
// progressed.
//
// Only a prior nonclosed state or nonzero failure count produces an observed
// reset message. The callback does not identify a particular probe generation
// or refund admission tokens; it records the balance-read recovery against the
// state seen at lock acquisition.
func (b *billingCircuitBreaker) OnSuccess() {
	if b == nil {
		return
	}
	// Reset current shared failure/probe state after a readable balance, even
	// when other reads progressed or this request will subsequently fail its
	// reserve check.
	b.mu.Lock()
	defer b.mu.Unlock()

	previousState := b.state
	previousFailures := b.failures

	b.state = billingCircuitClosed
	b.failures = 0
	b.halfOpenRemaining = 0

	// Observe recovery only when state or failure count changed; ordinary
	// already-closed successes need no repeated transition message, and the mutex
	// remains held through observation.
	if previousState != billingCircuitClosed {
		b.observer.Printf("service.billing_cache", "ALERT: billing circuit breaker closed (was %s)", circuitStateString(previousState))
	} else if previousFailures > 0 {
		b.observer.Printf("service.billing_cache", "INFO: billing circuit breaker failures reset from %d", previousFailures)
	}
}

// circuitStateString returns the breaker state's observation label or unknown
// for unsupported values. It does not admit requests or mutate state;
// transition methods use the label while reporting their protected updates.
func circuitStateString(state billingCircuitBreakerState) string {
	switch state {
	case billingCircuitClosed:
		return "closed"
	case billingCircuitOpen:
		return "open"
	case billingCircuitHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// Check forwards the caller-resolved payer, Key, group and subscription to
// CheckBillingEligibility. It establishes no authentication and performs no
// RPM update; request admission coordinates that later check after successful
// funding.
func (s *Eligibility) Check(ctx context.Context, input CheckInput) error {
	return s.CheckBillingEligibility(ctx, input.Payer, input.Key, input.Group, input.Subscription, input.Platform)
}

// runBackground submits a nonnil named maintenance callback to the retained
// runner or executes it inline when absent. Runner lifetime/admission belongs
// to its implementation, while this wrapper returns no acceptance or
// completion result.
func (s *Eligibility) runBackground(name string, fn func()) {
	if s.background == nil {
		// With no injected runner, maintenance executes inline and its latency
		// remains on this precheck path; callback completion therefore precedes this
		// wrapper's return.
		fn()
		return
	}
	s.background(name, fn)
}

// dateRuntime returns the current getter's date helpers or an empty runtime
// for absent service/options. The empty runtime chooses wall time and the
// local calendar; retrieving this value does not freeze those later
// observations.
func (s *Eligibility) dateRuntime() DateRuntime {
	if s != nil && s.options != nil {
		return s.options().Dates
	}
	return DateRuntime{}
}
