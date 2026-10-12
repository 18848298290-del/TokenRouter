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

const (
	cacheWriteSetBalance cacheWriteKind = iota
	cacheWriteDeductBalance
	cacheWriteUpdateRateLimitUsage

	// The fixed worker pool bounds concurrent cache writes.
	//
	// Balance deductions and usage increments run synchronously when the queue is full.
	// Cache population tasks may be dropped, with throttled logging.
	// A per-write context timeout bounds cooperative cache operations.
	cacheWriteWorkerCount     = 10
	cacheWriteBufferSize      = 1000
	cacheWriteTimeout         = 2 * time.Second // Timeout per cache operation.
	cacheWriteDropLogInterval = 5 * time.Second // Minimum interval between drop summaries.
	balanceLoadTimeout        = 3 * time.Second
)

//nolint:decorder // Separate const blocks keep each iota sequence starting at zero.
const (
	cacheWriteEnqueued cacheWriteEnqueueResult = iota
	cacheWriteQueueFull
	cacheWriteQueueClosed
)

//nolint:decorder // Separate const blocks keep each iota sequence starting at zero.
const (
	billingCircuitClosed billingCircuitBreakerState = iota
	billingCircuitOpen
	billingCircuitHalfOpen
)

var (
	ErrBillingServiceUnavailable = apperror.ServiceUnavailable("BILLING_SERVICE_ERROR", "Billing service temporarily unavailable. Please retry later.")

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

// cacheWriteKind identifies the cache operation to execute.
type cacheWriteKind int

type cacheWriteEnqueueResult int

// cacheWriteTask carries a balance or usage cache update.
type cacheWriteTask struct {
	kind     cacheWriteKind
	userID   int64
	apiKeyID int64
	balance  float64
	amount   float64
}

// Eligibility checks funds and API Key spending limits using cached balances and usage.
// It also owns the cache-write worker lifecycle.
type Eligibility struct {
	background            func(string, func())
	cache                 BillingCache
	userRepo              BalanceReader
	apiKeyRateLimitLoader APIKeyRateLimitLoader
	options               func() EligibilityOptions
	observer              Observe
	circuitBreaker        *billingCircuitBreaker

	cacheWriteChan      chan cacheWriteTask
	cacheWriteStartOnce sync.Once
	cacheWriteWg        sync.WaitGroup
	cacheWriteStopOnce  sync.Once
	cacheWriteMu        sync.RWMutex
	stopped             atomic.Bool
	balanceLoadSF       singleflight.Group
	// Drop counts and timestamps throttle summaries independently for full and closed queues.
	cacheWriteDropFullCount     uint64
	cacheWriteDropFullLastLog   int64
	cacheWriteDropClosedCount   uint64
	cacheWriteDropClosedLastLog int64
}

type billingCircuitBreakerState int

type billingCircuitBreaker struct {
	observer          Observe
	mu                sync.Mutex
	state             billingCircuitBreakerState
	failures          int
	openedAt          time.Time
	failureThreshold  int
	resetTimeout      time.Duration
	halfOpenRequests  int
	halfOpenRemaining int
}

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

// Start launches cache workers once after application dependencies have been bound. A stopped pool cannot restart.
func (s *Eligibility) Start() {
	s.cacheWriteStartOnce.Do(func() {
		s.cacheWriteMu.Lock()
		defer s.cacheWriteMu.Unlock()
		if !s.stopped.Load() {
			s.startCacheWriteWorkers()
		}
	})
}

// Stop closes the queue and waits for workers to drain accepted tasks. Repeated calls are safe.
func (s *Eligibility) Stop() {
	s.cacheWriteStopOnce.Do(func() {
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
		s.cacheWriteWg.Wait()

		s.cacheWriteMu.Lock()
		if s.cacheWriteChan == ch {
			s.cacheWriteChan = nil
		}
		s.cacheWriteMu.Unlock()
	})
}

func (s *Eligibility) startCacheWriteWorkers() {
	ch := make(chan cacheWriteTask, cacheWriteBufferSize)
	s.cacheWriteChan = ch
	for range cacheWriteWorkerCount {
		s.cacheWriteWg.Add(1)
		go s.cacheWriteWorker(ch)
	}
}

func (s *Eligibility) tryEnqueueCacheWrite(task cacheWriteTask) cacheWriteEnqueueResult {
	if s.stopped.Load() {
		return cacheWriteQueueClosed
	}

	s.cacheWriteMu.RLock()
	defer s.cacheWriteMu.RUnlock()

	if s.cacheWriteChan == nil {
		return cacheWriteQueueClosed
	}

	select {
	case s.cacheWriteChan <- task:
		return cacheWriteEnqueued
	default:
		// Let the caller decide whether a full queue requires a synchronous write.
		return cacheWriteQueueFull
	}
}

// enqueueCacheWrite returns false and logs a drop when the queue is full or closed.
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

func (s *Eligibility) cacheWriteWorker(ch <-chan cacheWriteTask) {
	defer s.cacheWriteWg.Done()
	for task := range ch {
		ctx, cancel := context.WithTimeout(context.Background(), cacheWriteTimeout)
		switch task.kind {
		case cacheWriteSetBalance:
			s.setBalanceCache(ctx, task.userID, task.balance)
		case cacheWriteDeductBalance:
			if s.cache != nil {
				if err := s.cache.DeductUserBalance(ctx, task.userID, task.amount); err != nil {
					s.observer.Printf("service.billing_cache", "Warning: deduct balance cache failed for user %d: %v", task.userID, err)
				}
			}
		case cacheWriteUpdateRateLimitUsage:
			if s.cache != nil {
				if err := s.cache.UpdateAPIKeyRateLimitUsage(ctx, task.apiKeyID, task.amount); err != nil {
					s.observer.Printf("service.billing_cache", "Warning: update rate limit usage cache failed for api key %d: %v", task.apiKeyID, err)
				}
			}
		}
		cancel()
	}
}

// cacheWriteKindName returns the operation label used in drop logs.
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

// logCacheWriteDrop emits throttled summaries with the number of dropped tasks.
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

// GetUserBalance reads the cache first and coalesces database reads after cache errors.
func (s *Eligibility) GetUserBalance(ctx context.Context, userID int64) (float64, error) {
	if s == nil || s.cache == nil {
		return s.getUserBalanceFromDB(ctx, userID)
	}

	balance, err := s.cache.GetUserBalance(ctx, userID)
	if err == nil {
		return balance, nil
	}

	// Coalesce concurrent database reads for the same user with an independent timeout.
	value, err, _ := s.balanceLoadSF.Do(strconv.FormatInt(userID, 10), func() (any, error) {
		loadCtx, cancel := context.WithTimeout(context.Background(), balanceLoadTimeout)
		defer cancel()

		balance, err := s.getUserBalanceFromDB(loadCtx, userID)
		if err != nil {
			return nil, err
		}

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

// getUserBalanceFromDB reads the user's persisted balance.
func (s *Eligibility) getUserBalanceFromDB(ctx context.Context, userID int64) (float64, error) {
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("get user balance: %w", err)
	}
	return user.Balance, nil
}

// setBalanceCache populates the balance cache and logs write failures.
func (s *Eligibility) setBalanceCache(ctx context.Context, userID int64, balance float64) {
	if s == nil || s.cache == nil {
		return
	}
	if err := s.cache.SetUserBalance(ctx, userID, balance); err != nil {
		s.observer.Printf("service.billing_cache", "Warning: set balance cache failed for user %d: %v", userID, err)
	}
}

// DeductBalanceCache synchronously deducts a cached balance; an absent cache is a no-op.
func (s *Eligibility) DeductBalanceCache(ctx context.Context, userID int64, amount float64) error {
	if s == nil || s.cache == nil {
		return nil
	}
	return s.cache.DeductUserBalance(ctx, userID, amount)
}

// QueueDeductBalance queues a deduction, writes synchronously if full, and drops it if the pool is closed.
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
	// A full queue must not silently discard balance deductions.
	ctx, cancel := context.WithTimeout(context.Background(), cacheWriteTimeout)
	defer cancel()
	if err := s.DeductBalanceCache(ctx, userID, amount); err != nil {
		s.observer.Printf("service.billing_cache", "Warning: deduct balance cache fallback failed for user %d: %v", userID, err)
	}
}

// InvalidateUserBalance removes the cached balance; an absent cache is a no-op.
func (s *Eligibility) InvalidateUserBalance(ctx context.Context, userID int64) error {
	if s == nil || s.cache == nil {
		return nil
	}
	if err := s.cache.InvalidateUserBalance(ctx, userID); err != nil {
		s.observer.Printf("service.billing_cache", "Warning: invalidate balance cache failed for user %d: %v", userID, err)
		return err
	}
	return nil
}

// InvalidateAPIKeyRateLimit invalidates the Redis rate-limit usage cache for an API key.
func (s *Eligibility) InvalidateAPIKeyRateLimit(ctx context.Context, keyID int64) error {
	if s == nil || s.cache == nil {
		return nil
	}
	if err := s.cache.InvalidateAPIKeyRateLimit(ctx, keyID); err != nil {
		s.observer.Printf("service.billing_cache", "Warning: invalidate api key rate limit cache failed for key %d: %v", keyID, err)
		return err
	}
	return nil
}

// checkAPIKeyRateLimits checks rate limit windows for an API key.
// It loads usage from Redis cache (falling back to DB on cache miss),
// resets expired windows in-memory and triggers async DB reset,
// and returns an error if any window limit is exceeded.
func (s *Eligibility) checkAPIKeyRateLimits(ctx context.Context, apiKey *KeySnapshot) error {
	if s == nil || s.cache == nil {
		// No cache: fall back to reading from DB directly
		if s.apiKeyRateLimitLoader == nil {
			return nil
		}
		data, err := s.apiKeyRateLimitLoader.GetRateLimitData(ctx, apiKey.ID)
		if err != nil {
			return nil // Don't block requests on DB errors
		}
		return s.evaluateRateLimits(ctx, apiKey, data.Usage5h, data.Usage1d, data.Usage7d,
			data.Window5hStart, data.Window1dStart, data.Window7dStart)
	}

	cacheData, err := s.cache.GetAPIKeyRateLimit(ctx, apiKey.ID)
	if err != nil {
		// Cache miss: load from DB and populate cache
		if s.apiKeyRateLimitLoader == nil {
			return nil
		}
		dbData, dbErr := s.apiKeyRateLimitLoader.GetRateLimitData(ctx, apiKey.ID)
		if dbErr != nil {
			return nil // Don't block requests on DB errors
		}
		// Build cache entry from DB data
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
		_ = s.cache.SetAPIKeyRateLimit(ctx, apiKey.ID, cacheEntry)
		cacheData = cacheEntry
	}

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

// evaluateRateLimits checks usage against limits, triggering async resets for expired windows.
func (s *Eligibility) evaluateRateLimits(ctx context.Context, apiKey *KeySnapshot, usage5h, usage1d, usage7d float64, w5h, w1d, w7d *time.Time) error {
	needsReset := false

	// Reset expired windows in-memory for check purposes
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

	// Trigger async DB reset if any window expired
	if needsReset {
		keyID := apiKey.ID
		s.runBackground("service/billing_cache_service.go:evaluateRateLimits", func() {
			resetCtx, cancel := context.WithTimeout(context.Background(), cacheWriteTimeout)
			defer cancel()
			if s.apiKeyRateLimitLoader != nil {
				// Reset through the repository when supported, then invalidate the cache.
				if loader, ok := s.apiKeyRateLimitLoader.(interface {
					ResetRateLimitWindows(ctx context.Context, id int64) error
				}); ok {
					if err := loader.ResetRateLimitWindows(resetCtx, keyID); err != nil {
						s.observer.Printf("service.billing_cache", "Warning: reset rate limit windows failed for api key %d: %v", keyID, err)
					}
				}
			}
			// Invalidate cache so next request loads fresh data
			if s.cache != nil {
				if err := s.cache.InvalidateAPIKeyRateLimit(resetCtx, keyID); err != nil {
					s.observer.Printf("service.billing_cache", "Warning: invalidate rate limit cache failed for api key %d: %v", keyID, err)
				}
			}
		})
	}

	// Check limits
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

// QueueUpdateAPIKeyRateLimitUsage queues a usage increment, writes synchronously
// if the queue is full, and drops it if the pool is closed.
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
	// A full queue must not silently discard usage increments.
	ctx, cancel := context.WithTimeout(context.Background(), cacheWriteTimeout)
	defer cancel()
	if err := s.cache.UpdateAPIKeyRateLimitUsage(ctx, apiKeyID, cost); err != nil {
		s.observer.Printf("service.billing_cache", "Warning: update rate limit usage cache fallback failed for api key %d: %v", apiKeyID, err)
	}
}

// CheckBillingEligibility checks funds and API Key spending limits.
// Auto mode can use balance after subscription quota exhaustion; subscription and balance modes honor the Key's
// selected source.
func (s *Eligibility) CheckBillingEligibility(ctx context.Context, user *UserSummary, apiKey *KeySnapshot, group *GroupSnapshot, subscription *UserSubscription, platform string) error {
	if s.circuitBreaker != nil && !s.circuitBreaker.Allow() {
		return ErrBillingServiceUnavailable
	}

	billingMode := effectiveKeyBillingMode(apiKey)
	if billingMode == APIKeyBillingModeSubscription {
		// Recheck plan scope after group changes and for asynchronous request paths.
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
		// Balance-only mode ignores any supplied subscription snapshot.
		subscription = nil
	}

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

	// API Key spending limits apply to every funding mode.
	if apiKey != nil && apiKey.HasRateLimits() {
		if err := s.checkAPIKeyRateLimits(ctx, apiKey); err != nil {
			return err
		}
	}

	return nil
}

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

func IsSubscriptionQuotaExceeded(err error) bool {
	return errors.Is(err, ErrDailyLimitExceeded) ||
		errors.Is(err, ErrWeeklyLimitExceeded) ||
		errors.Is(err, ErrMonthlyLimitExceeded)
}

func (s *Eligibility) MinimumBalanceReserve() float64 {
	if s == nil || s.options == nil || s.options().Billing.MinimumBalanceReserve <= 0 {
		return 0
	}
	return s.options().Billing.MinimumBalanceReserve
}

func (s *Eligibility) BalanceBelowEligibilityThreshold(balance float64) bool {
	if balance <= 0 {
		return true
	}
	minimumReserve := s.MinimumBalanceReserve()
	return minimumReserve > 0 && balance < minimumReserve
}

// checkBalanceEligibility checks the minimum available balance and updates the circuit breaker on read outcomes.
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

func newBillingCircuitBreaker(cfg CircuitBreakerOptions) *billingCircuitBreaker {
	if !cfg.Enabled {
		return nil
	}
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

func (b *billingCircuitBreaker) Allow() bool {
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

func (b *billingCircuitBreaker) OnFailure(err error) {
	if b == nil {
		return
	}
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

func (b *billingCircuitBreaker) OnSuccess() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	previousState := b.state
	previousFailures := b.failures

	b.state = billingCircuitClosed
	b.failures = 0
	b.halfOpenRemaining = 0

	// Log state transitions and failure-count resets, not every success.
	if previousState != billingCircuitClosed {
		b.observer.Printf("service.billing_cache", "ALERT: billing circuit breaker closed (was %s)", circuitStateString(previousState))
	} else if previousFailures > 0 {
		b.observer.Printf("service.billing_cache", "INFO: billing circuit breaker failures reset from %d", previousFailures)
	}
}

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

// Check validates funding and spending limits. The request coordinator checks RPM afterward.
func (s *Eligibility) Check(ctx context.Context, input CheckInput) error {
	return s.CheckBillingEligibility(ctx, input.Payer, input.Key, input.Group, input.Subscription, input.Platform)
}

// runBackground uses the injected task runner, or runs synchronously when none is supplied.
func (s *Eligibility) runBackground(name string, fn func()) {
	if s.background == nil {
		fn()
		return
	}
	s.background(name, fn)
}

func (s *Eligibility) dateRuntime() DateRuntime {
	if s != nil && s.options != nil {
		return s.options().Dates
	}
	return DateRuntime{}
}
