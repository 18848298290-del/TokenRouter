package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// notifications.go converts provider quota settings and usage into daily,
// weekly, monthly, and total billing notification dimensions. Provider settings
// supply each dimension's enabled flag, threshold mode, usage, and limit; the adapter
// keeps all four dimensions in that order so billing can apply its
// notification rules to disabled as well as enabled entries.

// QuotaNotification copies provider settings into daily, weekly, monthly, and
// total notification dimensions. A nil record returns nil; each nonnil result
// includes disabled dimensions so billing can decide which thresholds apply.
func QuotaNotification(a *provider.Record) *billing.QuotaNotifyProvider {
	if a == nil {
		return nil
	}
	return &billing.QuotaNotifyProvider{ID: a.ID, Name: a.Name, Platform: a.Platform, Dimensions: []billing.QuotaNotifyDimension{
		{Name: "daily", Enabled: a.GetQuotaNotifyDailyEnabled(), Threshold: a.GetQuotaNotifyDailyThreshold(), ThresholdType: a.GetQuotaNotifyDailyThresholdType(), CurrentUsed: a.GetQuotaDailyUsed(), Limit: a.GetQuotaDailyLimit()},
		{Name: "weekly", Enabled: a.GetQuotaNotifyWeeklyEnabled(), Threshold: a.GetQuotaNotifyWeeklyThreshold(), ThresholdType: a.GetQuotaNotifyWeeklyThresholdType(), CurrentUsed: a.GetQuotaWeeklyUsed(), Limit: a.GetQuotaWeeklyLimit()},
		{Name: "monthly", Enabled: a.GetQuotaNotifyMonthlyEnabled(), Threshold: a.GetQuotaNotifyMonthlyThreshold(), ThresholdType: a.GetQuotaNotifyMonthlyThresholdType(), CurrentUsed: a.GetQuotaMonthlyUsed(), Limit: a.GetQuotaMonthlyLimit()},
		{Name: "total", Enabled: a.GetQuotaNotifyTotalEnabled(), Threshold: a.GetQuotaNotifyTotalThreshold(), ThresholdType: a.GetQuotaNotifyTotalThresholdType(), CurrentUsed: a.GetQuotaUsed(), Limit: a.GetQuotaLimit()},
	}}
}
