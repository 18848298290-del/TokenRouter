package provider

import "slices"

// A coalesced upstream query returns one shared observation, but each waiter
// needs mutable display data of its own. Amount pointers, window slices, expiry
// values and subscription data are copied before QueryProvider returns,
// preserving nil and empty collections so absence stays distinguishable from a
// present empty result.
//
// The outer HTTP fields and the internal Usage object can refer to the same
// balance or subscription in the source. Those two relationships are retained
// inside the copy when their source pointers match. Other copied values are
// detached independently, so this routine does not reconstruct every arbitrary
// alias between fields.

// CloneUpstreamUsageResult copies the result and nested mutable usage values for
// one waiter. Nil input returns nil, and nil versus empty slices retain their
// representations. Keep the source stable throughout copying.
//
// When the source outer Balance or Subscription points to its corresponding
// Usage object, the returned pair shares its copied object. Numeric and time
// pointers, limits and other collections are copied independently, so unrelated
// source aliases are not reconstructed.
func CloneUpstreamUsageResult(value *UpstreamUsageQueryResult) *UpstreamUsageQueryResult {
	if value == nil {
		return nil
	}
	out := *value
	out.Balance = cloneUsageAmount(value.Balance)
	out.Balances = slices.Clone(value.Balances)
	out.Available = clonePointer(value.Available)
	out.Limits = cloneUsageLimits(value.Limits)
	out.Subscription = cloneUsageSubscription(value.Subscription)
	out.ExpiresAt = clonePointer(value.ExpiresAt)
	if value.Usage != nil {
		usage := *value.Usage
		usage.Balance = cloneUsageAmount(usage.Balance)
		usage.Balances = slices.Clone(usage.Balances)
		usage.Available = clonePointer(usage.Available)
		usage.Limits = cloneUsageLimits(usage.Limits)
		usage.Subscription = cloneUsageSubscription(usage.Subscription)
		usage.ExpiresAt = clonePointer(usage.ExpiresAt)
		out.Usage = &usage
		// Reconnect matching outer and internal objects to the same copied value so
		// a waiter sees their shared balance and subscription consistently after editing.
		if value.Balance == value.Usage.Balance {
			out.Balance = usage.Balance
		}
		if value.Subscription == value.Usage.Subscription {
			out.Subscription = usage.Subscription
		}
	}
	return &out
}

// cloneUsageAmount copies an optional amount and its used, total and remaining
// pointers. Nil input returns nil; the source remains stable while the copy is
// constructed for a query waiter.
func cloneUsageAmount(value *UpstreamUsageAmount) *UpstreamUsageAmount {
	if value == nil {
		return nil
	}
	out := *value
	out.Used = clonePointer(value.Used)
	out.Total = clonePointer(value.Total)
	out.Remaining = clonePointer(value.Remaining)
	return &out
}

// cloneUsageLimits copies a window slice and each amount and reset pointer. Nil
// and empty slices retain their distinction, and callers keep the source slice
// stable throughout copying.
func cloneUsageLimits(value []UpstreamUsageLimit) []UpstreamUsageLimit {
	out := slices.Clone(value)
	for i := range out {
		out[i].Used = clonePointer(out[i].Used)
		out[i].Limit = clonePointer(out[i].Limit)
		out[i].Remaining = clonePointer(out[i].Remaining)
		out[i].ResetAt = clonePointer(out[i].ResetAt)
	}
	return out
}

// cloneUsageSubscription copies an optional subscription, its remaining and
// expiry pointers, and its nested windows. Nil input returns nil; the caller
// keeps these source values stable during copying.
func cloneUsageSubscription(value *UpstreamUsageSubscription) *UpstreamUsageSubscription {
	if value == nil {
		return nil
	}
	out := *value
	out.Remaining = clonePointer(value.Remaining)
	out.ExpiresAt = clonePointer(value.ExpiresAt)
	out.Limits = cloneUsageLimits(value.Limits)
	return &out
}
