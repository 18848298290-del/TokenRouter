package admission

import (
	"context"
	"errors"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
)

const (
	KeyQuotaExceeded          ConsumptionFailureKind = "key_quota"
	KeyExpired                ConsumptionFailureKind = "key_expired"
	MaintenanceFailed         ConsumptionFailureKind = "maintenance"
	SubscriptionInvalid       ConsumptionFailureKind = "subscription"
	SubscriptionLimitExceeded ConsumptionFailureKind = "subscription_limit"
	InsufficientBalance       ConsumptionFailureKind = "balance"
)

// KeyLimits reports API key expiry and quota exhaustion.
type KeyLimits interface {
	IsExpired() bool
	IsQuotaExhausted() bool
}

// SubscriptionValidator validates subscription limits and refreshes expired usage windows.
type SubscriptionValidator interface {
	ValidateAndCheckLimits(*billing.UserSubscription) (bool, error)
	EnsureWindowMaintenance(context.Context, *billing.UserSubscription) (*billing.UserSubscription, error)
}

type ConsumptionFailureKind string

// ConsumptionFailure identifies the rejected check. HTTP handlers choose protocol-specific messages and status codes.
type ConsumptionFailure struct {
	Kind  ConsumptionFailureKind
	Cause error
}

type ConsumptionInput struct {
	Status       string
	Limits       KeyLimits
	Balance      float64
	Subscription *billing.UserSubscription
}

func (e *ConsumptionFailure) Error() string {
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return string(e.Kind)
}
func (e *ConsumptionFailure) Unwrap() error { return e.Cause }

// CheckConsumption validates key limits and funding after funding-source resolution. RPM accounting is separate.
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
