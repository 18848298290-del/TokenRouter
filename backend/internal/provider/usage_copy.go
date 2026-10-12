package provider

import "slices"

// CloneUpstreamUsageResult creates an independent result for a waiter, preserving nil and empty collections.
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
		// Keep shared balance and subscription pointers between HTTP fields and internal usage within the copy.
		if value.Balance == value.Usage.Balance {
			out.Balance = usage.Balance
		}
		if value.Subscription == value.Usage.Subscription {
			out.Subscription = usage.Subscription
		}
	}
	return &out
}

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
