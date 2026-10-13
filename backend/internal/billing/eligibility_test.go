package billing

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// billingCacheMissStub forces every balance read to miss and counts successful
// population attempts.
type billingCacheMissStub struct {
	// setBalanceCalls is incremented atomically by SetUserBalance and polled
	// after queue execution. Population does not change this fixture's
	// always-missing subsequent balance reads.
	setBalanceCalls atomic.Int64 // observed balance population call count
}

// TestCheckBillingEligibilityRejectsBalanceBelowMinimumReserve provides half a
// cent of cached balance against a one-cent reserve. Funding admission must
// return insufficient balance despite the amount being positive.
func TestCheckBillingEligibilityRejectsBalanceBelowMinimumReserve(t *testing.T) {
	cache := &balanceEligibilityCacheStub{balance: 0.005}
	cfg := &EligibilityOptions{}
	cfg.Billing.MinimumBalanceReserve = 0.01
	svc := newEligibilityForTest(cache, nil, nil, cfg)
	svc.Start()
	t.Cleanup(svc.Stop)

	err := svc.CheckBillingEligibility(context.Background(), &UserSummary{ID: 1}, nil, nil, nil, "")
	require.ErrorIs(t, err, ErrInsufficientBalance)
}

// TestCheckBillingEligibilityAllowsBalanceAtMinimumReserve gives cached
// balance equal to its positive one-cent reserve. It requires successful
// funding admission, preserving the reserve comparison's strict less-than
// rule.
func TestCheckBillingEligibilityAllowsBalanceAtMinimumReserve(t *testing.T) {
	cache := &balanceEligibilityCacheStub{balance: 0.01}
	cfg := &EligibilityOptions{}
	cfg.Billing.MinimumBalanceReserve = 0.01
	svc := newEligibilityForTest(cache, nil, nil, cfg)
	svc.Start()
	t.Cleanup(svc.Stop)

	err := svc.CheckBillingEligibility(context.Background(), &UserSummary{ID: 1}, nil, nil, nil, "")
	require.NoError(t, err)
}

// TestBillingCacheServiceQueueHighLoad submits twice the queue capacity in
// deductions and one Key increment with workers running. It checks bounded
// submission time and eventually observes some balance and Key updates,
// without asserting every submitted write completed.
func TestBillingCacheServiceQueueHighLoad(t *testing.T) {
	cache := &billingCacheWorkerStub{}
	svc := NewEligibility(cache, nil, nil, func() EligibilityOptions { return EligibilityOptions{} }, nil)
	svc.Start()
	t.Cleanup(svc.Stop)

	start := time.Now()
	for range cacheWriteBufferSize * 2 {
		svc.QueueDeductBalance(1, 1)
	}
	// The burst timing covers enqueue and any synchronous full-queue fallback,
	// not a guarantee that every asynchronous cache write has drained.
	require.Less(t, time.Since(start), 2*time.Second)

	svc.QueueUpdateAPIKeyRateLimitUsage(9, 1.5)

	// Poll balanceUpdates and apiKeyUpdates because queued writes finish after
	// submission. Both positive observations protect deduction and spending
	// dispatch without requiring a count for every task in the burst.
	require.Eventually(t, func() bool {
		return atomic.LoadInt64(&cache.balanceUpdates) > 0
	}, 2*time.Second, 10*time.Millisecond)

	require.Eventually(t, func() bool {
		return atomic.LoadInt64(&cache.apiKeyUpdates) > 0
	}, 2*time.Second, 10*time.Millisecond)
}

// TestBillingCacheServiceEnqueueAfterStopReturnsFalse starts and fully stops
// the pool before another enqueue attempt. It checks false for that sequential
// closed-pool call; it does not exercise overlapping producer shutdown.
func TestBillingCacheServiceEnqueueAfterStopReturnsFalse(t *testing.T) {
	cache := &billingCacheWorkerStub{}
	svc := NewEligibility(cache, nil, nil, func() EligibilityOptions { return EligibilityOptions{} }, nil)
	svc.Start()
	svc.Stop()

	enqueued := svc.enqueueCacheWrite(cacheWriteTask{
		kind:   cacheWriteDeductBalance,
		userID: 1,
		amount: 1,
	})
	require.False(t, enqueued)
}

// TestBillingCacheServiceGetUserBalance_Singleflight releases sixteen
// same-user cache misses against a delayed repository read. All results must
// equal the supplied balance with one repository call, and at least one queued
// population must eventually occur.
func TestBillingCacheServiceGetUserBalance_Singleflight(t *testing.T) {
	cache := &billingCacheMissStub{}
	userRepo := &balanceLoadUserRepoStub{
		delay:   80 * time.Millisecond,
		balance: 12.34,
	}
	svc := newEligibilityForTest(cache, userRepo, nil, &EligibilityOptions{})
	svc.Start()
	t.Cleanup(svc.Stop)

	// Use sixteen callers behind one gate to overlap the delayed repository load.
	const goroutines = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)
	balCh := make(chan float64, goroutines)

	// Buffer one error and balance per worker before waiting, so workers can
	// publish both results without depending on this goroutine consuming them
	// early.
	for range goroutines {
		wg.Go(func() {
			<-start
			bal, err := svc.GetUserBalance(context.Background(), 99)
			errCh <- err
			balCh <- bal
		})
	}

	// Release all waiting readers before joining them; close result channels only
	// after the wait group confirms every producer has sent both observations.
	close(start)
	wg.Wait()
	close(errCh)
	close(balCh)

	for err := range errCh {
		require.NoError(t, err)
	}
	for bal := range balCh {
		require.Equal(t, 12.34, bal)
	}

	require.Equal(t, int64(1), userRepo.calls.Load(), "并发穿透应被 singleflight 合并")
	// The balance-read goroutines have finished, but population still belongs to
	// the queue. Wait for at least one SetUserBalance call instead of assuming it
	// completed with the read.
	require.Eventually(t, func() bool {
		return cache.setBalanceCalls.Load() >= 1
	}, time.Second, 10*time.Millisecond)
}

// TestBillingEligibility_ExhaustedSubscriptionWithZeroBalanceFails supplies
// exact and over-limit daily consumption with automatic funding and zero
// balance. Both cases must return insufficient balance after the exhausted
// grant reaches the balance path.
func TestBillingEligibility_ExhaustedSubscriptionWithZeroBalanceFails(t *testing.T) {
	for _, tt := range []struct {
		name string  // named exhaustion scenario label
		used float64 // USD
	}{
		{name: "exactly exhausted", used: 10},
		{name: "over limit", used: 12},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc := newBillingEligibilityService(t, 0)
			err := svc.CheckBillingEligibility(
				context.Background(),
				&UserSummary{ID: 1},
				nil,
				nil,
				activeBillingEligibilitySubscription(10, tt.used),
				"",
			)

			require.ErrorIs(t, err, ErrInsufficientBalance)
		})
	}
}

// TestBillingEligibility_ExhaustedSubscriptionFallsBackToBalance supplies an
// exactly exhausted daily grant and a positive persisted balance in auto mode.
// It requires successful admission from that available balance path.
func TestBillingEligibility_ExhaustedSubscriptionFallsBackToBalance(t *testing.T) {
	svc := newBillingEligibilityService(t, 1)
	err := svc.CheckBillingEligibility(
		context.Background(),
		&UserSummary{ID: 1},
		nil,
		nil,
		activeBillingEligibilitySubscription(10, 10),
		"",
	)

	require.NoError(t, err)
}

// TestBillingEligibility_PreferredSubscriptionDoesNotFallBackToBalance
// supplies already exhausted/over-limit preferred snapshots with ample
// balance. It checks their first preferred validation returns insufficiency;
// it does not cross an observation boundary between the two subscription
// checks.
func TestBillingEligibility_PreferredSubscriptionDoesNotFallBackToBalance(t *testing.T) {
	for _, tt := range []struct {
		name string  // named exhaustion scenario label
		used float64 // USD
	}{
		{name: "exactly exhausted", used: 10},
		{name: "already over limit", used: 12},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc := newBillingEligibilityService(t, 100)
			err := svc.CheckBillingEligibility(
				context.Background(),
				&UserSummary{ID: 1},
				&KeySnapshot{BillingMode: APIKeyBillingModeSubscription},
				nil,
				activeBillingEligibilitySubscription(10, tt.used),
				"",
			)

			require.ErrorIs(t, err, ErrPreferredSubscriptionInsufficient)
		})
	}
}

// TestBillingEligibility_PreferredSubscriptionRejectsFallbackGroupOutsidePlan
// supplies a preferred grant restricted to group ten while selecting group
// eleven. It checks the group error precedes using that grant or available
// balance.
func TestBillingEligibility_PreferredSubscriptionRejectsFallbackGroupOutsidePlan(t *testing.T) {
	svc := newBillingEligibilityService(t, 1)
	subscription := activeBillingEligibilitySubscription(10, 0)
	subscription.Plan = &SubscriptionPlan{GroupIDs: []int64{10}}
	err := svc.CheckBillingEligibility(
		context.Background(),
		&UserSummary{ID: 1},
		&KeySnapshot{BillingMode: APIKeyBillingModeSubscription},
		&GroupSnapshot{ID: 11},
		subscription,
		"",
	)

	require.ErrorIs(t, err, ErrPreferredSubscriptionGroup)
}

// TestBillingEligibility_BalanceModeIgnoresProvidedSubscription supplies a
// usable daily grant with explicit balance mode and zero persisted balance. It
// requires insufficient balance, showing the grant snapshot does not fund that
// mode.
func TestBillingEligibility_BalanceModeIgnoresProvidedSubscription(t *testing.T) {
	svc := newBillingEligibilityService(t, 0)
	err := svc.CheckBillingEligibility(
		context.Background(),
		&UserSummary{ID: 1},
		&KeySnapshot{BillingMode: APIKeyBillingModeBalance},
		nil,
		activeBillingEligibilitySubscription(10, 0),
		"",
	)

	require.ErrorIs(t, err, ErrInsufficientBalance)
}

// TestBillingEligibility_UnlimitedSubscriptionDoesNotRequireBalance supplies
// an effective grant with absent/zero limits and no balance reader or cache.
// It requires successful auto admission without accessing those unavailable
// balance dependencies.
func TestBillingEligibility_UnlimitedSubscriptionDoesNotRequireBalance(t *testing.T) {
	now := time.Now()
	svc := newEligibilityForTest(nil, nil, nil, &EligibilityOptions{})
	svc.Start()
	t.Cleanup(svc.Stop)

	err := svc.CheckBillingEligibility(
		context.Background(),
		&UserSummary{ID: 1},
		nil,
		nil,
		&UserSubscription{
			ID:              1,
			UserID:          1,
			PlanID:          1,
			StartsAt:        now.Add(-time.Hour),
			ExpiresAt:       now.Add(time.Hour),
			Status:          SubscriptionStatusActive,
			DailyLimitUSD:   billingEligibilityLimitPtr(0),
			WeeklyLimitUSD:  nil,
			MonthlyLimitUSD: billingEligibilityLimitPtr(0),
		},
		"",
	)

	require.NoError(t, err)
}

// GetUserBalance returns a cache-miss error for every user ID, including after
// SetUserBalance succeeds. It reads no stored amount, keeping the concurrency
// scenario on the repository-load path for each request.
func (s *billingCacheMissStub) GetUserBalance(ctx context.Context, userID int64) (float64, error) {
	return 0, errors.New("cache miss")
}

// SetUserBalance atomically records a successful population attempt and
// returns nil. It ignores the supplied user/amount and creates no reusable
// cached value, so subsequent GetUserBalance calls still miss.
func (s *billingCacheMissStub) SetUserBalance(ctx context.Context, userID int64, balance float64) error {
	s.setBalanceCalls.Add(1)
	return nil
}

// DeductUserBalance reports successful fixture deduction for any user/amount
// pair without changing stored state or population count. Tests use this cache
// for forced-read misses rather than deduction arithmetic assertions.
func (s *billingCacheMissStub) DeductUserBalance(ctx context.Context, userID int64, amount float64) error {
	return nil
}

// InvalidateUserBalance reports success for every requested user without
// changing the always-missing fixture. No record is removed, and the
// population counter continues to track only explicit SetUserBalance calls.
func (s *billingCacheMissStub) InvalidateUserBalance(ctx context.Context, userID int64) error {
	return nil
}

// GetAPIKeyRateLimit reports a cache-miss error for every Key ID and returns
// no usage data. Its setter does not retain entries, so this fixture never
// provides successful spending observations.
func (s *billingCacheMissStub) GetAPIKeyRateLimit(ctx context.Context, keyID int64) (*APIKeyRateLimitCacheData, error) {
	return nil, errors.New("cache miss")
}

// SetAPIKeyRateLimit reports successful fixture population without retaining
// the supplied data pointer or Key ID. Later spending reads remain misses, and
// this method changes no balance population instrumentation.
func (s *billingCacheMissStub) SetAPIKeyRateLimit(ctx context.Context, keyID int64, data *APIKeyRateLimitCacheData) error {
	return nil
}

// UpdateAPIKeyRateLimitUsage reports success for every Key/cost pair without
// storing counters or timestamps. The fixture's balance-read concurrency
// scenario does not use this method to assert spending arithmetic.
func (s *billingCacheMissStub) UpdateAPIKeyRateLimitUsage(ctx context.Context, keyID int64, cost float64) error {
	return nil
}

// InvalidateAPIKeyRateLimit reports success without removing a record or
// changing instrumentation. Spending reads already miss for all Keys in this
// fixture, so invalidation introduces no additional persisted observation.
func (s *billingCacheMissStub) InvalidateAPIKeyRateLimit(ctx context.Context, keyID int64) error {
	return nil
}

// billingEligibilityLimitPtr returns fresh scalar storage for an
// eligibility-test quota amount.
func billingEligibilityLimitPtr(v float64) *float64 {
	return &v
}

// activeBillingEligibilitySubscription creates a two-hour active-label fixture
// with a daily anchor and supplied quota/use. Fresh scalar/time storage
// carries its basic values, while this short lifetime keeps daily reset
// one-time during exhaustion checks.
func activeBillingEligibilitySubscription(limit float64, used float64) *UserSubscription {
	now := time.Now()
	windowStart := now.Add(-time.Hour)
	return &UserSubscription{
		ID:               1,
		UserID:           1,
		PlanID:           1,
		StartsAt:         now.Add(-time.Hour),
		ExpiresAt:        now.Add(time.Hour),
		Status:           SubscriptionStatusActive,
		DailyWindowStart: &windowStart,
		DailyLimitUSD:    billingEligibilityLimitPtr(limit),
		DailyUsageUSD:    used,
	}
}

// newBillingEligibilityService constructs a direct-load service around the
// supplied fixed repository balance and starts workers. It registers Stop for
// test cleanup; the missing cache bypasses balance singleflight and cache
// writes.
func newBillingEligibilityService(t *testing.T, balance float64) *Eligibility {
	t.Helper()

	svc := newEligibilityForTest(nil, &balanceLoadUserRepoStub{balance: balance}, nil, &EligibilityOptions{})
	svc.Start()
	t.Cleanup(svc.Stop)
	return svc
}
