package billing

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

// Billing admission tests need controlled catalog values, cache misses,
// and observable maintenance calls. These fixtures retain their programmed
// read responses while atomic counters record service calls, and an
// injected delay allows persisted-balance reads to overlap or observe
// cancellation in the coalescing scenarios.

// Balance-notification fixture labels select fixed amounts or recharge
// percentages using the same setting values as the threshold parser
// under test.
const (
	thresholdTypeFixed      = "fixed"      // absolute amount threshold
	thresholdTypePercentage = "percentage" // recharge percentage threshold
)

// defaultCatalogStub serves programmed catalog entries and rejects unexpected
// refresh attempts.
type defaultCatalogStub struct {
	// entries maps exact test model names to catalog pointers.
	// GetModelPricing returns the stored pointer directly, so pricing
	// readers share fixture data and callers leave it unchanged during
	// those reads.
	entries map[string]*pricing.CatalogModelPricing
}

// balanceEligibilityCacheStub programs balance reads and records deduction and
// invalidation calls.
type balanceEligibilityCacheStub struct {
	billingCacheWorkerStub // remaining cache-method fixture

	// balance is the programmed read response. SetUserBalance and
	// DeductUserBalance record calls through their fixture methods
	// without changing this configured value, so it is not maintained
	// wallet state.
	balance float64 // programmed cached USD balance
	// cacheMissAfterInvalidate and invalidated jointly control GetUserBalance
	// errors. InvalidateUserBalance sets the atomic invalidated flag;
	// reads return the programmed balance until both flag and configured
	// miss option are true.
	cacheMissAfterInvalidate bool        // enable post-invalidation read error
	invalidated              atomic.Bool // recorded invalidation state
	// deductCalls and invalidateCalls count their corresponding methods
	// with atomic increments. Tests read these counters atomically
	// when service maintenance can call the fixture from another
	// goroutine.
	deductCalls     atomic.Int64 // recorded deduction calls
	invalidateCalls atomic.Int64 // recorded invalidation calls
}

// billingCacheWorkerStub counts cache writes while supplying unavailable or
// empty read behavior.
type billingCacheWorkerStub struct {
	// balanceUpdates and apiKeyUpdates observe asynchronous cache
	// maintenance. Fixture methods use sync/atomic increments, and
	// concurrent test observations use atomic loads rather than reading
	// the plain int64 fields directly.
	balanceUpdates int64 // recorded balance write calls
	apiKeyUpdates  int64 // recorded key usage updates
}

// balanceLoadUserRepoStub programs persisted-balance reads with an optional
// cancellable delay.
type balanceLoadUserRepoStub struct {
	calls atomic.Int64 // recorded repository read calls
	// delay controls the cancellation path when positive. GetByID
	// then selects the timer or context; a nonpositive delay returns
	// its programmed user immediately after counting the call, without
	// checking cancellation.
	delay   time.Duration // configured repository read latency
	balance float64       // programmed persisted USD balance
}

// mediaPriceCards supplies one configured image card regardless of the
// requested identity.
type mediaPriceCards struct {
	// card is returned directly by every effective-price lookup,
	// including nil when unconfigured. The fixture ignores model and
	// group identity, so callers retain it as shared test input.
	card *ModelPricingEntry
}

// GetModelPricing returns the fixture pointer stored under the exact
// model key, or nil when absent. The value is shared with entries;
// this reader performs no cloning, normalization, or catalog refresh
// for the tested lookup.
func (s defaultCatalogStub) GetModelPricing(model string) *pricing.CatalogModelPricing {
	return s.entries[model]
}

// ForceUpdate panics when a tested path attempts catalog refresh.
// This fixture serves programmed lookup entries and implements no
// update source, so the panic exposes an unexpected refresh call
// to the invoking test.
func (s defaultCatalogStub) ForceUpdate() error {
	panic("default price queries must not update the catalog")
}

// GetUserBalance returns the programmed balance unless the configured
// post-invalidation miss is active. It checks both the option and
// atomic flag, then returns a cache-miss error when both are true;
// it does not modify the response state.
func (s *balanceEligibilityCacheStub) GetUserBalance(context.Context, int64) (float64, error) {
	if s.cacheMissAfterInvalidate && s.invalidated.Load() {
		return 0, errors.New("cache miss")
	}
	return s.balance, nil
}

// DeductUserBalance counts one attempted deduction and returns nil.
// User identity and amount do not change the programmed balance;
// tests use the atomic call count to observe the service's deduction
// behavior rather than a simulated wallet update.
func (s *balanceEligibilityCacheStub) DeductUserBalance(context.Context, int64, float64) error {
	s.deductCalls.Add(1)
	return nil
}

// InvalidateUserBalance records one invalidation call and stores
// the invalidated flag true. It returns nil for any user identity;
// subsequent reads miss when the fixture's post-invalidation option
// is enabled, and the fixed balance value is retained.
func (s *balanceEligibilityCacheStub) InvalidateUserBalance(context.Context, int64) error {
	s.invalidateCalls.Add(1)
	s.invalidated.Store(true)
	return nil
}

// newEligibilityForTest constructs eligibility with a live options
// pointer and an asynchronous runner for rate-window reset tasks.
// Pass nonnil options; the callback reads its current value. Synchronize
// option changes with every service read. Cache worker startup and Stop
// remain the service's own lifecycle, separate from this injected runner.
func newEligibilityForTest(cache BillingCache, users BalanceReader, keys APIKeyRateLimitLoader, options *EligibilityOptions) *Eligibility {
	return NewEligibility(cache, users, keys, func() EligibilityOptions { return *options }, nil, func(_ string, fn func()) {
		// Run the service's rate-window reset callback asynchronously so
		// eligibility tests can observe its background maintenance path;
		// cache workers are launched separately by Eligibility.Start.
		go fn()
	})
}

// GetUserBalance returns an unavailable-read error for every user.
// The worker fixture observes maintenance writes instead of providing
// a loaded balance, so eligibility callers that need a read outcome
// use a more specific programmed fixture.
func (b *billingCacheWorkerStub) GetUserBalance(ctx context.Context, userID int64) (float64, error) {
	return 0, errors.New("not implemented")
}

// SetUserBalance records one balance-cache write with an atomic
// increment and returns nil. It ignores identity and numeric value;
// the fixture exposes activity counts to queue tests and retains
// no simulated cache entry.
func (b *billingCacheWorkerStub) SetUserBalance(ctx context.Context, userID int64, balance float64) error {
	atomic.AddInt64(&b.balanceUpdates, 1)
	return nil
}

// DeductUserBalance records one balance-maintenance call through
// the same atomic write counter as SetUserBalance. It returns nil
// and ignores amount and user identity, so queue tests observe
// execution without simulating account balances.
func (b *billingCacheWorkerStub) DeductUserBalance(ctx context.Context, userID int64, amount float64) error {
	atomic.AddInt64(&b.balanceUpdates, 1)
	return nil
}

// InvalidateUserBalance accepts the maintenance call without changing
// fixture state. It returns nil for every user, allowing worker
// and queue tests to supply the cache interface while observing
// only their configured write counters.
func (b *billingCacheWorkerStub) InvalidateUserBalance(ctx context.Context, userID int64) error {
	return nil
}

// GetAPIKeyRateLimit returns nil with an unavailable-read error for
// every key. The worker fixture supplies no spending-window snapshot;
// tests needing rate-limit read data configure a dedicated fixture
// rather than relying on its activity counters.
func (b *billingCacheWorkerStub) GetAPIKeyRateLimit(ctx context.Context, keyID int64) (*APIKeyRateLimitCacheData, error) {
	return nil, errors.New("not implemented")
}

// SetAPIKeyRateLimit accepts a supplied snapshot and returns nil
// without retaining it or incrementing the usage counter. Queue
// tests use this interface method as a successful store placeholder,
// including for nil data.
func (b *billingCacheWorkerStub) SetAPIKeyRateLimit(ctx context.Context, keyID int64, data *APIKeyRateLimitCacheData) error {
	return nil
}

// UpdateAPIKeyRateLimitUsage records one usage-update call with an
// atomic increment and returns nil. The key and cost are not
// retained, allowing queue tests to observe execution through the
// usage-update counter.
func (b *billingCacheWorkerStub) UpdateAPIKeyRateLimitUsage(ctx context.Context, keyID int64, cost float64) error {
	atomic.AddInt64(&b.apiKeyUpdates, 1)
	return nil
}

// InvalidateAPIKeyRateLimit returns nil for any key and retains
// no snapshot state. It completes the worker fixture's cache
// interface while tests observe rate-limit maintenance through the
// separate usage-update counter.
func (b *billingCacheWorkerStub) InvalidateAPIKeyRateLimit(ctx context.Context, keyID int64) error {
	return nil
}

// GetByID counts the persisted-balance read and returns a new
// summary with the requested ID and programmed balance. Positive
// delay selects timer completion or context cancellation; if
// cancellation wins, its error is returned. Nonpositive delay skips
// that cancellation check.
func (s *balanceLoadUserRepoStub) GetByID(ctx context.Context, id int64) (*UserSummary, error) {
	s.calls.Add(1)
	// The injected delay makes database reads overlap in singleflight
	// tests. Observe cancellation during that wait so a canceled
	// read can return before the artificial latency elapses.
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &UserSummary{ID: id, Balance: s.balance}, nil
}

// GetEffectiveConfigModelPricing returns the shared configured card
// for every context, group, and model input. Nil card produces nil,
// and the fixture applies no pattern matching or validation; pricing
// resolution tests determine the card contents they need.
func (s mediaPriceCards) GetEffectiveConfigModelPricing(context.Context, int64, string) *ModelPricingEntry {
	return s.card
}
