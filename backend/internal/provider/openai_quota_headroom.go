package provider

import (
	"fmt"
	"time"
)

// Scheduler headroom uses recent Codex utilization while missing or reset
// observations contribute the neutral factor. Primary measurement wins over
// its seven-day compatibility field, and a live secondary or five-hour window
// with little remaining quota discounts that primary score.
//
// The eight-hour timestamp gate is independent of reset checks. Either
// compatible primary reset field can invalidate primary scoring, while either
// secondary reset prevents that window's discount. Finite utilization is
// interpreted as a percentage and clamped to zero through one before computing
// remaining headroom. The result combines the primary remaining fraction with
// a half-factor only when secondary remaining quota is below ten percent;
// scheduling eligibility is assessed separately.

// Headroom scoring uses neutral defaults, a low-secondary-quota discount, and
// snapshot expiry.
const (
	openAIQuotaHeadroomNeutralFactor      = 0.5
	openAIQuotaHeadroomSecondaryLowRemain = 0.10
	openAIQuotaHeadroomSnapshotStaleAfter = 8 * time.Hour
)

// OpenAIQuotaHeadroomFactor combines recent primary and secondary Codex
// utilization into a scheduler factor. Nil records, absent Extra, stale
// timestamps, missing primary measurements, and reset primary windows return
// the neutral factor.
//
// Primary fields take precedence over compatible seven-day fields, while
// either primary reset name can make that score neutral. A live secondary
// measurement can halve primary remaining headroom when less than ten percent
// remains; expired secondary data supplies no discount. Percentages are
// divided by one hundred and clamped before this calculation, and callers keep
// the record stable during evaluation. Supply finite measurements when a
// finite factor is required; parsed NaN passes through the numeric reader and
// clamp.
func OpenAIQuotaHeadroomFactor(provider *Record, now time.Time) float64 {
	if provider == nil || len(provider.Extra) == 0 || openAIQuotaHeadroomSnapshotStale(provider.Extra, now) {
		return openAIQuotaHeadroomNeutralFactor
	}
	primaryUsedPercent, ok := ResolveProviderExtraNumber(provider.Extra, "codex_primary_used_percent", "codex_7d_used_percent")
	if !ok || openAIQuotaWindowResetAny(provider.Extra, now, "primary", "7d") {
		return openAIQuotaHeadroomNeutralFactor
	}

	// Quota fields encode percentages; convert to remaining fractional headroom.
	factor := 1 - clampQuotaFactor(primaryUsedPercent/100)
	if secondaryUsedPercent, ok := ResolveProviderExtraNumber(provider.Extra, "codex_secondary_used_percent", "codex_5h_used_percent"); ok &&
		!openAIQuotaWindowResetAny(provider.Extra, now, "secondary", "5h") {
		// A live secondary percentage supplies its remaining fractional headroom.
		secondaryRemaining := 1 - clampQuotaFactor(secondaryUsedPercent/100)
		// A nearly exhausted secondary window discounts primary capacity despite its
		// own remaining quota, so high primary headroom cannot hide this additional
		// pressure.
		if secondaryRemaining < openAIQuotaHeadroomSecondaryLowRemain {
			factor *= openAIQuotaHeadroomNeutralFactor
		}
	}
	return factor
}

// openAIQuotaHeadroomSnapshotStale rejects missing, invalid, or eight-hour-old
// observation timestamps. It uses the supplied now for age; future timestamps
// remain fresh because their computed age is negative.
func openAIQuotaHeadroomSnapshotStale(extra map[string]any, now time.Time) bool {
	updatedRaw, ok := extra["codex_usage_updated_at"]
	if !ok {
		return true
	}
	updatedAt, err := ParseUsageTime(fmt.Sprint(updatedRaw))
	// Invalid observation time cannot establish recent headroom. Treat it as
	// stale so the scheduler receives the neutral factor for this missing age
	// evidence.
	if err != nil {
		return true
	}
	return now.Sub(updatedAt) >= openAIQuotaHeadroomSnapshotStaleAfter
}

// openAIQuotaWindowResetAny reports whether any supplied compatible window
// name has reset. It returns false when none qualifies, letting callers use
// unreset measurements in headroom calculation. Keep Extra stable across
// all candidate-name checks.
func openAIQuotaWindowResetAny(extra map[string]any, now time.Time, windows ...string) bool {
	for _, window := range windows {
		if OpenAIQuotaWindowReset(extra, window, now) {
			return true
		}
	}
	return false
}

// clampQuotaFactor bounds finite utilization fractions to zero through one for
// headroom. Values outside that range select an endpoint, while NaN passes
// through because neither comparison matches.
func clampQuotaFactor(v float64) float64 {
	// Headroom subtracts this clamped utilization fraction from one.
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	default:
		return v
	}
}
