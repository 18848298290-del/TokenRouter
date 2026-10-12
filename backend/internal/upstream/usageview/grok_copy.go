package usageview

import (
	"slices"
)

// Grok usage snapshots contain optional numeric pointers and per-product
// observations that can be shared by cached results. Copy helpers allocate new
// numeric values and collection storage before a caller independently updates
// its quota or billing display result.

// CloneQuotaWindow copies a quota window and allocates separate storage for
// each optional numeric value. Nil input remains nil; the returned value and
// pointed numbers can be modified independently of the source.
func CloneQuotaWindow(value *QuotaWindow) *QuotaWindow {
	if value == nil {
		return nil
	}
	out := *value
	out.Limit = clonePointer(value.Limit)
	out.Remaining = clonePointer(value.Remaining)
	out.ResetUnix = clonePointer(value.ResetUnix)
	return &out
}

// CloneBillingSummary copies billing metadata, slices, and all optional
// numeric values, including percentages inside ProductUsage. Nil input remains
// nil and slices retain nil versus empty presence; callers can independently
// mutate the copied collections and numbers.
func CloneBillingSummary(value *BillingSummary) *BillingSummary {
	if value == nil {
		return nil
	}
	out := *value
	out.UsagePercent = clonePointer(value.UsagePercent)
	out.ProductUsage = slices.Clone(value.ProductUsage)
	out.MonthlyLimitCents = clonePointer(value.MonthlyLimitCents)
	out.UsedCents = clonePointer(value.UsedCents)
	out.IncludedUsedCents = clonePointer(value.IncludedUsedCents)
	out.UsedPercent = clonePointer(value.UsedPercent)
	out.FailedWindows = slices.Clone(value.FailedWindows)
	out.PrepaidBalance = clonePointer(value.PrepaidBalance)
	out.OnDemandCap = clonePointer(value.OnDemandCap)
	out.OnDemandUsed = clonePointer(value.OnDemandUsed)
	out.MonthlyLimit = clonePointer(value.MonthlyLimit)
	out.MonthlyUsed = clonePointer(value.MonthlyUsed)
	// Cloning the product slice copies its structs but retains percentage
	// pointers. Replace those pointers too before consumers mutate the copied
	// billing result.
	for i := range out.ProductUsage {
		out.ProductUsage[i].UsagePercent = clonePointer(value.ProductUsage[i].UsagePercent)
	}
	return &out
}

// clonePointer allocates one pointed-to value by assignment, or returns nil
// for nil input. The copy is shallow for a T containing references; current
// quota and billing callers pass scalar numeric types.
func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}
