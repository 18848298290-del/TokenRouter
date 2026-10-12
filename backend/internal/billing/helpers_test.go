package billing

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

const (
	thresholdTypeFixed      = "fixed"
	thresholdTypePercentage = "percentage"
)

type defaultCatalogStub struct {
	entries map[string]*pricing.CatalogModelPricing
}

type balanceEligibilityCacheStub struct {
	billingCacheWorkerStub

	balance                  float64
	cacheMissAfterInvalidate bool
	invalidated              atomic.Bool
	deductCalls              atomic.Int64
	invalidateCalls          atomic.Int64
}

type billingCacheWorkerStub struct {
	balanceUpdates int64
	apiKeyUpdates  int64
}

type balanceLoadUserRepoStub struct {
	calls   atomic.Int64
	delay   time.Duration
	balance float64
}

// mediaPriceCards supplies a full-model image billing price card.
type mediaPriceCards struct {
	card *ModelPricingEntry
}

func (s defaultCatalogStub) GetModelPricing(model string) *pricing.CatalogModelPricing {
	return s.entries[model]
}

func (s defaultCatalogStub) ForceUpdate() error {
	panic("default price queries must not update the catalog")
}

func (s *balanceEligibilityCacheStub) GetUserBalance(context.Context, int64) (float64, error) {
	if s.cacheMissAfterInvalidate && s.invalidated.Load() {
		return 0, errors.New("cache miss")
	}
	return s.balance, nil
}

func (s *balanceEligibilityCacheStub) DeductUserBalance(context.Context, int64, float64) error {
	s.deductCalls.Add(1)
	return nil
}

func (s *balanceEligibilityCacheStub) InvalidateUserBalance(context.Context, int64) error {
	s.invalidateCalls.Add(1)
	s.invalidated.Store(true)
	return nil
}

// newEligibilityForTest injects a goroutine runner for background cache maintenance.
func newEligibilityForTest(cache BillingCache, users BalanceReader, keys APIKeyRateLimitLoader, options *EligibilityOptions) *Eligibility {
	return NewEligibility(cache, users, keys, func() EligibilityOptions { return *options }, nil, func(_ string, fn func()) { go fn() })
}

func (b *billingCacheWorkerStub) GetUserBalance(ctx context.Context, userID int64) (float64, error) {
	return 0, errors.New("not implemented")
}

func (b *billingCacheWorkerStub) SetUserBalance(ctx context.Context, userID int64, balance float64) error {
	atomic.AddInt64(&b.balanceUpdates, 1)
	return nil
}

func (b *billingCacheWorkerStub) DeductUserBalance(ctx context.Context, userID int64, amount float64) error {
	atomic.AddInt64(&b.balanceUpdates, 1)
	return nil
}

func (b *billingCacheWorkerStub) InvalidateUserBalance(ctx context.Context, userID int64) error {
	return nil
}

func (b *billingCacheWorkerStub) GetAPIKeyRateLimit(ctx context.Context, keyID int64) (*APIKeyRateLimitCacheData, error) {
	return nil, errors.New("not implemented")
}

func (b *billingCacheWorkerStub) SetAPIKeyRateLimit(ctx context.Context, keyID int64, data *APIKeyRateLimitCacheData) error {
	return nil
}

func (b *billingCacheWorkerStub) UpdateAPIKeyRateLimitUsage(ctx context.Context, keyID int64, cost float64) error {
	atomic.AddInt64(&b.apiKeyUpdates, 1)
	return nil
}

func (b *billingCacheWorkerStub) InvalidateAPIKeyRateLimit(ctx context.Context, keyID int64) error {
	return nil
}

func (s *balanceLoadUserRepoStub) GetByID(ctx context.Context, id int64) (*UserSummary, error) {
	s.calls.Add(1)
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &UserSummary{ID: id, Balance: s.balance}, nil
}

// GetEffectiveConfigModelPricing returns the test's configured price card.
func (s mediaPriceCards) GetEffectiveConfigModelPricing(context.Context, int64, string) *ModelPricingEntry {
	return s.card
}
