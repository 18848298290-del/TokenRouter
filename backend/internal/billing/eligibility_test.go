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

type billingCacheMissStub struct {
	setBalanceCalls atomic.Int64
}

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

func TestBillingCacheServiceQueueHighLoad(t *testing.T) {
	cache := &billingCacheWorkerStub{}
	svc := NewEligibility(cache, nil, nil, func() EligibilityOptions { return EligibilityOptions{} }, nil)
	svc.Start()
	t.Cleanup(svc.Stop)

	start := time.Now()
	for range cacheWriteBufferSize * 2 {
		svc.QueueDeductBalance(1, 1)
	}
	require.Less(t, time.Since(start), 2*time.Second)

	svc.QueueUpdateAPIKeyRateLimitUsage(9, 1.5)

	require.Eventually(t, func() bool {
		return atomic.LoadInt64(&cache.balanceUpdates) > 0
	}, 2*time.Second, 10*time.Millisecond)

	require.Eventually(t, func() bool {
		return atomic.LoadInt64(&cache.apiKeyUpdates) > 0
	}, 2*time.Second, 10*time.Millisecond)
}

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

func TestBillingCacheServiceGetUserBalance_Singleflight(t *testing.T) {
	cache := &billingCacheMissStub{}
	userRepo := &balanceLoadUserRepoStub{
		delay:   80 * time.Millisecond,
		balance: 12.34,
	}
	svc := newEligibilityForTest(cache, userRepo, nil, &EligibilityOptions{})
	svc.Start()
	t.Cleanup(svc.Stop)

	const goroutines = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)
	balCh := make(chan float64, goroutines)

	for range goroutines {
		wg.Go(func() {
			<-start
			bal, err := svc.GetUserBalance(context.Background(), 99)
			errCh <- err
			balCh <- bal
		})
	}

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
	require.Eventually(t, func() bool {
		return cache.setBalanceCalls.Load() >= 1
	}, time.Second, 10*time.Millisecond)
}

func TestBillingEligibility_ExhaustedSubscriptionWithZeroBalanceFails(t *testing.T) {
	for _, tt := range []struct {
		name string
		used float64
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

func TestBillingEligibility_PreferredSubscriptionDoesNotFallBackToBalance(t *testing.T) {
	for _, tt := range []struct {
		name string
		used float64
	}{
		{name: "exactly exhausted", used: 10},
		{name: "already over limit", used: 12},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// A Key pinned to an exhausted subscription must reject admission even when balance remains.
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

func (s *billingCacheMissStub) GetUserBalance(ctx context.Context, userID int64) (float64, error) {
	return 0, errors.New("cache miss")
}

func (s *billingCacheMissStub) SetUserBalance(ctx context.Context, userID int64, balance float64) error {
	s.setBalanceCalls.Add(1)
	return nil
}

func (s *billingCacheMissStub) DeductUserBalance(ctx context.Context, userID int64, amount float64) error {
	return nil
}

func (s *billingCacheMissStub) InvalidateUserBalance(ctx context.Context, userID int64) error {
	return nil
}

func (s *billingCacheMissStub) GetAPIKeyRateLimit(ctx context.Context, keyID int64) (*APIKeyRateLimitCacheData, error) {
	return nil, errors.New("cache miss")
}

func (s *billingCacheMissStub) SetAPIKeyRateLimit(ctx context.Context, keyID int64, data *APIKeyRateLimitCacheData) error {
	return nil
}

func (s *billingCacheMissStub) UpdateAPIKeyRateLimitUsage(ctx context.Context, keyID int64, cost float64) error {
	return nil
}

func (s *billingCacheMissStub) InvalidateAPIKeyRateLimit(ctx context.Context, keyID int64) error {
	return nil
}

func billingEligibilityLimitPtr(v float64) *float64 {
	return &v
}

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

func newBillingEligibilityService(t *testing.T, balance float64) *Eligibility {
	t.Helper()

	svc := newEligibilityForTest(nil, &balanceLoadUserRepoStub{balance: balance}, nil, &EligibilityOptions{})
	svc.Start()
	t.Cleanup(svc.Stop)
	return svc
}
