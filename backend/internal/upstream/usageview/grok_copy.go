package usageview

import (
	"slices"
)

// CloneQuotaWindow copies the window and its pointed-to values; nil remains nil.
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

// CloneBillingSummary copies the summary, slices, and pointed-to values, preserving nil and empty slices.
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
	for i := range out.ProductUsage {
		out.ProductUsage[i].UsagePercent = clonePointer(value.ProductUsage[i].UsagePercent)
	}
	return &out
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}
