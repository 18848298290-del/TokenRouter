package usageview

import (
	"errors"
	"math"
	"strings"
)

// Normalized usage needs enough observed data for its selected display mode
// before provider orchestration publishes it. Missing pointers differ from
// reported zero values: a quota amount can supply any observed component,
// while a limits result needs at least one window and a subscription needs a
// named plan.
//
// Validation checks finite numbers and the fields required by each mode, then
// checks supplied nested data even when the selected mode does not require it.
// Wallet and remaining amounts can be negative, but used amounts and totals
// cannot. Names are compared after trimming for duplicate windows without
// rewriting the source values. Arithmetic consistency and adapter-specific
// sentinel handling stay with the adapter that knows the response format.

// ValidateNormalizedUsage checks mode-specific structure and every supplied
// amount, window, subscription, and expiry. It requires a nonblank provider,
// an exact recognized mode, and an empty or recognized uppercase unit.
//
// A subscription with Unlimited omits Remaining and Limits; a limited
// subscription supplies at least one of them. Validation leaves the result
// unchanged and stops at the first error. Remaining values can be finite and
// negative, timestamps need to be nonzero when supplied, and arithmetic
// consistency is left to adapters rather than inferred from field presence.
func ValidateNormalizedUsage(usage *UpstreamUsageInfo) error {
	if usage == nil || strings.TrimSpace(usage.Provider) == "" || strings.TrimSpace(usage.Mode) == "" {
		return errors.New("missing normalized usage fields")
	}
	switch usage.Mode {
	case "balance", "quota", "limits", "subscription":
	default:
		return errors.New("unknown normalized usage mode")
	}
	// Required shape follows the selected display mode. The same result can
	// carry optional data from other modes, which is validated later when present.
	switch usage.Mode {
	case "balance", "quota":
		if usage.Balance == nil {
			return errors.New("missing normalized balance")
		}
	case "limits":
		if len(usage.Limits) == 0 && (usage.Subscription == nil || len(usage.Subscription.Limits) == 0) {
			return errors.New("missing normalized limits")
		}
	case "subscription":
		if usage.Subscription == nil {
			return errors.New("missing normalized subscription")
		}
	}
	if usage.Unit != "" && usage.Unit != "USD" && usage.Unit != "CNY" && usage.Unit != "TOKENS" && usage.Unit != "PERCENT" {
		return errors.New("unknown usage unit")
	}
	for _, balance := range usage.Balances {
		if strings.TrimSpace(balance.Currency) == "" || !ValidFiniteNumber(balance.Remaining) {
			return errors.New("invalid usage balance entry")
		}
	}
	if usage.Balance != nil {
		if err := ValidateUsageAmount(usage.Balance); err != nil {
			return err
		}
	}
	if err := ValidateUsageLimits(usage.Limits); err != nil {
		return err
	}
	if usage.Subscription != nil {
		if strings.TrimSpace(usage.Subscription.PlanName) == "" {
			return errors.New("missing subscription plan")
		}
		// Unlimited is a distinct representation of subscription quota. Reject
		// simultaneous finite-looking amounts so consumers do not display an
		// unlimited sentinel as a reported remaining amount.
		if usage.Subscription.Unlimited && (usage.Subscription.Remaining != nil || len(usage.Subscription.Limits) > 0) {
			return errors.New("unlimited subscription must not contain remaining or limits")
		}
		if !usage.Subscription.Unlimited && usage.Subscription.Remaining == nil && len(usage.Subscription.Limits) == 0 {
			return errors.New("limited subscription is missing remaining or limits")
		}
		if usage.Subscription.Remaining != nil && !ValidFiniteNumber(*usage.Subscription.Remaining) {
			return errors.New("invalid subscription remaining")
		}
		if usage.Subscription.ExpiresAt != nil && usage.Subscription.ExpiresAt.IsZero() {
			return errors.New("invalid subscription expiry")
		}
		if err := ValidateUsageLimits(usage.Subscription.Limits); err != nil {
			return err
		}
	}
	if usage.ExpiresAt != nil && usage.ExpiresAt.IsZero() {
		return errors.New("invalid expiry")
	}
	return nil
}

// ValidateUsageAmount accepts an amount with at least one observed component.
// Used and Total need finite nonnegative values, while Remaining can be any
// finite value, including a negative wallet balance; the amount is not
// mutated.
func ValidateUsageAmount(amount *UpstreamUsageAmount) error {
	if amount == nil || (amount.Used == nil && amount.Total == nil && amount.Remaining == nil) {
		return errors.New("missing usage amount values")
	}
	for _, value := range []*float64{amount.Used, amount.Total} {
		if value != nil && !ValidNonNegativeNumber(*value) {
			return errors.New("invalid usage amount")
		}
	}
	if amount.Remaining != nil && !ValidFiniteNumber(*amount.Remaining) {
		return errors.New("invalid usage remaining")
	}
	// Wallet debt can be reported as a negative remainder. Limited New API
	// token quota rules are checked by their adapter before this shared amount
	// validation.
	return nil
}

// ValidateUsageLimits checks that each window has a unique nonblank trimmed
// name and at least one observed amount. Empty collections are accepted,
// leaving mode-specific collection presence to ValidateNormalizedUsage.
//
// Used and Limit require finite nonnegative values, while Remaining can be
// finite and negative. A supplied ResetAt must be nonzero. Duplicate
// comparison is case-sensitive and leaves the original name unchanged; value
// consistency, currency meaning, and reset chronology remain with adapters
// because those rules depend on the upstream format.
func ValidateUsageLimits(limits []UpstreamUsageLimit) error {
	seen := make(map[string]struct{}, len(limits))
	for _, limit := range limits {
		// Compare trimmed names so spacing variants cannot create duplicate display
		// windows, while leaving each retained upstream spelling intact for the caller.
		name := strings.TrimSpace(limit.Name)
		if name == "" {
			return errors.New("missing usage limit name")
		}
		if _, exists := seen[name]; exists {
			return errors.New("duplicate usage limit")
		}
		seen[name] = struct{}{}
		for _, value := range []*float64{limit.Used, limit.Limit} {
			if value != nil && !ValidNonNegativeNumber(*value) {
				return errors.New("invalid usage limit amount")
			}
		}
		if limit.Used == nil && limit.Limit == nil && limit.Remaining == nil {
			return errors.New("missing usage limit values")
		}
		if limit.Remaining != nil && !ValidFiniteNumber(*limit.Remaining) {
			return errors.New("invalid usage limit remaining")
		}
		if limit.ResetAt != nil && limit.ResetAt.IsZero() {
			return errors.New("invalid usage limit reset")
		}
	}
	return nil
}

// ValidNonNegativeNumber reports whether value is finite and greater than or
// equal to zero.
func ValidNonNegativeNumber(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

// ValidPositiveNumber reports whether value is finite and strictly greater
// than zero.
func ValidPositiveNumber(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

// ValidFiniteNumber reports whether value excludes NaN and either infinity.
func ValidFiniteNumber(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
