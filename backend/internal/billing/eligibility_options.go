package billing

import (
	"context"
)

// Observe sends component names and formatted log events to the application's logger.
type Observe func(component, format string, args ...any)

// EligibilityOptions contains billing admission runtime settings.
type EligibilityOptions struct {
	Dates   DateRuntime
	Billing BillingOptions
}
type BillingOptions struct {
	MinimumBalanceReserve float64
	CircuitBreaker        CircuitBreakerOptions
}
type CircuitBreakerOptions struct {
	Enabled             bool
	FailureThreshold    int
	ResetTimeoutSeconds int
	HalfOpenRequests    int
}

// BalanceReader reads the user's balance and entitlement summary.
type BalanceReader interface {
	GetByID(context.Context, int64) (*UserSummary, error)
}
type APIKeyRateLimitLoader interface {
	GetRateLimitData(context.Context, int64) (*APIKeyRateLimitData, error)
}

// KeySnapshot contains the Key fields needed for spending admission.
type KeySnapshot struct {
	ID          int64
	BillingMode string
	RateLimit5h float64
	RateLimit1d float64
	RateLimit7d float64
}

// GroupSnapshot identifies the final group used for the request.
type GroupSnapshot struct{ ID int64 }

// CheckInput contains the payer, Key, group, and subscription used for funding admission.
type CheckInput struct {
	Payer        *UserSummary
	Key          *KeySnapshot
	Group        *GroupSnapshot
	Subscription *UserSubscription
	Platform     string
}

func (o Observe) Printf(component, format string, args ...any) {
	if o != nil {
		o(component, format, args...)
	}
}

func (k *KeySnapshot) HasRateLimits() bool {
	return k.RateLimit5h > 0 || k.RateLimit1d > 0 || k.RateLimit7d > 0
}

func effectiveKeyBillingMode(k *KeySnapshot) string {
	if k == nil {
		return APIKeyBillingModeAuto
	}
	mode, ok := NormalizeAPIKeyBillingMode(k.BillingMode)
	if !ok {
		return APIKeyBillingModeAuto
	}
	return mode
}
