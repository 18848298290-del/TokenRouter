package provider

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// config_quota_window.go translates provider quota settings into billing window
// calculations and updates their reset timestamps and usage fields.
// Fixed quota configuration needs both the next reset shown to administrators
// and the current window used for stored consumption. These dates share a
// timezone and calendar configuration but have different eligibility rules:
// publication needs fixed mode, while reconciliation also needs a positive
// limit.
//
// The provider helpers translate Extra values into billing's fixed-window rules
// and write the resulting UTC RFC3339 timestamps back into the caller's map.
// Publishing a nonfixed dimension removes its stale reset timestamp.
// Reconciliation clears usage only when billing reports a missing or older
// window start, retaining consumption already belonging to the current window.
// Callers own persistence and supply a stable calculation time and timezone
// loader.

// quotaWindowLocation resolves quota_reset_timezone through the supplied
// loader. Missing or nonstring names select UTC; a loader error also selects
// time.UTC so stored dates can still be evaluated.
func quotaWindowLocation(extra map[string]any, load func(string) (*time.Location, error)) *time.Location {
	name, _ := extra["quota_reset_timezone"].(string)
	if name == "" {
		name = "UTC"
	}
	location, err := load(name)
	if err != nil {
		// Invalid saved timezone text cannot supply calendar rules. Use UTC so
		// configuration reconciliation can still determine a usable reset date.
		return time.UTC
	}
	return location
}

// providerWindowConfig builds billing's fixed-window input from one Extra key
// prefix. Missing reset_day selects day one, while other numeric fields use
// ParseExtraFloat64 and integer conversion; billing's wrapper performs
// clock-range normalization.
func providerWindowConfig(extra map[string]any, prefix string, period billing.ProviderWindowPeriod) billing.FixedProviderWindow {
	mode, _ := extra[prefix+"_reset_mode"].(string)
	day := 1
	if raw, ok := extra[prefix+"_reset_day"]; ok {
		// Calendar reset days use whole indices after configuration input validation.
		day = int(ParseExtraFloat64(raw))
	}
	// Reset hours use the same whole-number interpretation as calendar day input,
	// and billing then normalizes out-of-range values before calendar calculation.
	return billing.FixedProviderWindow{Period: period, Mode: mode, Limit: ParseExtraFloat64(extra[prefix+"_limit"]), Hour: int(ParseExtraFloat64(extra[prefix+"_reset_hour"])), Day: day}
}

// ComputeQuotaResetAt updates daily, weekly, and monthly next-reset metadata in the
// caller's Extra map. The supplied loader resolves the configured timezone, and
// fixed-mode dates are encoded in UTC RFC3339.
//
// Each dimension independently asks billing.NextProviderFixedReset whether it
// has a next reset. A false result removes that dimension's old reset_at entry
// so switching to rolling mode cannot display a stale fixed date. This
// operation publishes dates even when the configured limit is zero; persistence
// remains the caller's responsibility.
func ComputeQuotaResetAt(extra map[string]any, now time.Time, load func(string) (*time.Location, error)) {
	location := quotaWindowLocation(extra, load)
	for _, dimension := range []struct {
		prefix string                       // quota Extra key prefix
		period billing.ProviderWindowPeriod // billing reset period selector
	}{
		{"quota_daily", billing.ProviderDailyWindow},
		{"quota_weekly", billing.ProviderWeeklyWindow},
		{"quota_monthly", billing.ProviderMonthlyWindow},
	} {
		reset, ok := billing.NextProviderFixedReset(providerWindowConfig(extra, dimension.prefix, dimension.period), location, now)
		// A disabled fixed schedule must lose its prior publication timestamp;
		// retaining it would show a date belonging to configuration that is gone.
		if ok {
			extra[dimension.prefix+"_reset_at"] = reset.UTC().Format(time.RFC3339)
		} else {
			delete(extra, dimension.prefix+"_reset_at")
		}
	}
}

// NormalizeFixedQuotaWindows reconciles daily, weekly, and monthly stored usage
// against
// the supplied calculation time. Nil Extra is ignored; the timezone loader
// supplies local calendar rules for active fixed windows.
//
// Billing requests a reset only for a positive fixed-window limit whose stored
// start is zero or predates the current reset. A requested reset writes zero
// usage and the current window start together, retaining total usage and
// already current periodic consumption. The caller persists the modified
// map with its configuration change.
func NormalizeFixedQuotaWindows(extra map[string]any, now time.Time, load func(string) (*time.Location, error)) {
	if extra == nil {
		return
	}
	location := quotaWindowLocation(extra, load)
	for _, dimension := range []struct {
		prefix string                       // quota Extra key prefix
		period billing.ProviderWindowPeriod // billing reset period selector
	}{
		{"quota_daily", billing.ProviderDailyWindow},
		{"quota_weekly", billing.ProviderWeeklyWindow},
		{"quota_monthly", billing.ProviderMonthlyWindow},
	} {
		last, reset := billing.ReconcileProviderFixedWindow(providerWindowConfig(extra, dimension.prefix, dimension.period), ParseExtraTime(extra[dimension.prefix+"_start"]), location, now)
		// Store the new start with zero usage so the next billing increment
		// belongs to the computed window instead of the expired consumption period.
		if reset {
			extra[dimension.prefix+"_used"] = 0.0
			extra[dimension.prefix+"_start"] = last.UTC().Format(time.RFC3339)
		}
	}
}
