package admission

import (
	"context"
	"errors"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// consumption.go checks consumption eligibility for an authenticated key and
// its selected funding source. A rejected key cannot consume a subscription
// even if that subscription has remaining quota, and balance is consulted only
// when no subscription was selected. Failures retain their cause so each HTTP
// protocol can choose its own status and client-facing message.
//
// Subscription validation can clear expired usage counters in the supplied
// object and request window maintenance. Admission persists that maintenance,
// obtains the refreshed subscription, and validates it again before returning
// it to the caller. This second check uses the stored result when concurrent
// requests compete to reset a usage window.

// Consumption failure kinds identify the rejected stage so HTTP handlers can
// select protocol-specific error messages and status codes.
const (
	// KeyQuotaExceeded identifies exhausted key quota.
	KeyQuotaExceeded ConsumptionFailureKind = "key_quota"
	// KeyExpired identifies an expired API key.
	KeyExpired ConsumptionFailureKind = "key_expired"
	// MaintenanceFailed identifies failed subscription window maintenance.
	MaintenanceFailed ConsumptionFailureKind = "maintenance"
	// SubscriptionInvalid identifies an unusable selected subscription.
	SubscriptionInvalid ConsumptionFailureKind = "subscription"
	// SubscriptionLimitExceeded identifies an exhausted subscription window.
	SubscriptionLimitExceeded ConsumptionFailureKind = "subscription_limit"
	// InsufficientBalance identifies a nonpositive payer balance.
	InsufficientBalance ConsumptionFailureKind = "balance"
)

// KeyLimits supplies live expiry and quota checks for an authenticated key.
type KeyLimits interface {
	// IsExpired reports whether the key's configured expiry has been reached.
	IsExpired() bool
	// IsQuotaExhausted reports whether the key's configured total quota is spent.
	IsQuotaExhausted() bool
}

// SubscriptionValidator validates subscription limits and refreshes expired usage windows.
type SubscriptionValidator interface {
	// ValidateAndCheckLimits checks subscription status and remaining window quota.
	// A true result requests persisted window maintenance, which callers complete
	// before admitting consumption and validating the refreshed subscription again.
	ValidateAndCheckLimits(*billing.UserSubscription) (bool, error)
	// EnsureWindowMaintenance persists needed activation or resets and returns the
	// stored subscription. Callers validate that refreshed object because another
	// request can win a conditional reset and leave different usage in storage.
	EnsureWindowMaintenance(context.Context, *billing.UserSubscription) (*billing.UserSubscription, error)
}

// ConsumptionFailureKind identifies the admission rule that rejects consumption.
type ConsumptionFailureKind string

// ConsumptionFailure identifies the rejected check. HTTP handlers choose protocol-specific messages and status codes.
type ConsumptionFailure struct {
	Kind ConsumptionFailureKind // rejected admission stage
	// Cause carries a subscription or maintenance error when available. Error
	// exposes its message and Unwrap allows handlers to inspect the underlying error.
	Cause error
}

// ConsumptionInput supplies authenticated key limits and the resolved funding source.
type ConsumptionInput struct {
	Status string // loaded API-key status
	// Limits supplies the key's expiry and quota checks. CheckConsumption calls
	// it after status checks, so callers provide a usable implementation.
	Limits  KeyLimits
	Balance float64 // payer balance in USD
	// Subscription is the selected funding object, or nil for balance funding.
	// Validation and maintenance may mutate or replace it before admission succeeds.
	Subscription *billing.UserSubscription
}

// Error returns the underlying failure message when a cause is available.
// Failures produced directly from key or balance checks expose their kind
// string, which protocol handlers can classify separately.
func (e *ConsumptionFailure) Error() string {
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return string(e.Kind)
}

// Unwrap exposes the underlying subscription or maintenance error to error-chain
// classification. Key and balance failures have no underlying cause and return
// nil, leaving their Kind as the rejection detail.
func (e *ConsumptionFailure) Unwrap() error { return e.Cause }

// CheckConsumption validates key limits and the funding source already chosen
// for this request. A subscription-funded success returns the validated
// subscription, while a balance-funded success returns nil for both results.
// Callers supply Limits and, when Subscription is present, a subscription validator.
//
// Key status, expiry, and quota checks precede subscription or balance checks.
// If subscription validation requests window maintenance, CheckConsumption
// completes it and checks the refreshed record again before granting admission.
// Maintenance errors are distinguished from invalid subscriptions and exhausted
// window limits so protocol handlers can report the relevant rejection.
func CheckConsumption(ctx context.Context, input ConsumptionInput, subscriptions SubscriptionValidator) (*billing.UserSubscription, *ConsumptionFailure) {
	switch input.Status {
	case apikey.StatusAPIKeyQuotaExhausted:
		return nil, &ConsumptionFailure{Kind: KeyQuotaExceeded}
	case apikey.StatusAPIKeyExpired:
		return nil, &ConsumptionFailure{Kind: KeyExpired}
	}
	if input.Limits.IsExpired() {
		return nil, &ConsumptionFailure{Kind: KeyExpired}
	}
	if input.Limits.IsQuotaExhausted() {
		return nil, &ConsumptionFailure{Kind: KeyQuotaExceeded}
	}
	subscription := input.Subscription
	if subscription != nil {
		needsMaintenance, err := subscriptions.ValidateAndCheckLimits(subscription)
		if needsMaintenance {
			// Validation may have cleared local stale counters. Persist maintenance
			// and recheck the returned record so concurrent resets cannot grant stale quota.
			refreshed, maintenanceErr := subscriptions.EnsureWindowMaintenance(ctx, subscription)
			if maintenanceErr != nil {
				return nil, &ConsumptionFailure{Kind: MaintenanceFailed, Cause: maintenanceErr}
			}
			subscription = refreshed
			_, err = subscriptions.ValidateAndCheckLimits(subscription)
		}
		if err != nil {
			kind := SubscriptionInvalid
			if errors.Is(err, billing.ErrDailyLimitExceeded) || errors.Is(err, billing.ErrWeeklyLimitExceeded) || errors.Is(err, billing.ErrMonthlyLimitExceeded) {
				kind = SubscriptionLimitExceeded
			}
			return nil, &ConsumptionFailure{Kind: kind, Cause: err}
		}
		return subscription, nil
	}
	if input.Balance <= 0 {
		return nil, &ConsumptionFailure{Kind: InsufficientBalance}
	}
	return nil, nil
}
