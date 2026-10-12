package provider

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// quotaWindowLocation resolves the configured time zone; billing computes reset dates and windows.
func quotaWindowLocation(extra map[string]any, load func(string) (*time.Location, error)) *time.Location {
	name, _ := extra["quota_reset_timezone"].(string)
	if name == "" {
		name = "UTC"
	}
	location, err := load(name)
	if err != nil {
		return time.UTC
	}
	return location
}

func providerWindowConfig(extra map[string]any, prefix string, period billing.ProviderWindowPeriod) billing.FixedProviderWindow {
	mode, _ := extra[prefix+"_reset_mode"].(string)
	day := 1
	if raw, ok := extra[prefix+"_reset_day"]; ok {
		day = int(ParseExtraFloat64(raw))
	}
	return billing.FixedProviderWindow{Period: period, Mode: mode, Limit: ParseExtraFloat64(extra[prefix+"_limit"]), Hour: int(ParseExtraFloat64(extra[prefix+"_reset_hour"])), Day: day}
}

func ComputeQuotaResetAt(extra map[string]any, now time.Time, load func(string) (*time.Location, error)) {
	location := quotaWindowLocation(extra, load)
	for _, dimension := range []struct {
		prefix string
		period billing.ProviderWindowPeriod
	}{{"quota_daily", billing.ProviderDailyWindow}, {"quota_weekly", billing.ProviderWeeklyWindow}} {
		reset, ok := billing.NextProviderFixedReset(providerWindowConfig(extra, dimension.prefix, dimension.period), location, now)
		if ok {
			extra[dimension.prefix+"_reset_at"] = reset.UTC().Format(time.RFC3339)
		} else {
			delete(extra, dimension.prefix+"_reset_at")
		}
	}
}

func NormalizeFixedQuotaWindows(extra map[string]any, now time.Time, load func(string) (*time.Location, error)) {
	if extra == nil {
		return
	}
	location := quotaWindowLocation(extra, load)
	for _, dimension := range []struct {
		prefix string
		period billing.ProviderWindowPeriod
	}{{"quota_daily", billing.ProviderDailyWindow}, {"quota_weekly", billing.ProviderWeeklyWindow}} {
		last, reset := billing.ReconcileProviderFixedWindow(providerWindowConfig(extra, dimension.prefix, dimension.period), ParseExtraTime(extra[dimension.prefix+"_start"]), location, now)
		if reset {
			extra[dimension.prefix+"_used"] = 0.0
			extra[dimension.prefix+"_start"] = last.UTC().Format(time.RFC3339)
		}
	}
}
