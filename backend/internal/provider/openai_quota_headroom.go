package provider

import (
	"fmt"
	"time"
)

// Headroom scoring uses neutral defaults, a low-secondary-quota discount, and snapshot expiry.
const (
	openAIQuotaHeadroomNeutralFactor      = 0.5
	openAIQuotaHeadroomSecondaryLowRemain = 0.10
	openAIQuotaHeadroomSnapshotStaleAfter = 8 * time.Hour
)

// OpenAIQuotaHeadroomFactor converts a Codex quota snapshot into a scheduling factor from zero to one.
// It scores primary usage, falling back to seven-day usage, and discounts low secondary or five-hour headroom.
func OpenAIQuotaHeadroomFactor(provider *Record, now time.Time) float64 {
	if provider == nil || len(provider.Extra) == 0 || openAIQuotaHeadroomSnapshotStale(provider.Extra, now) {
		return openAIQuotaHeadroomNeutralFactor
	}
	primaryUsedPercent, ok := ResolveProviderExtraNumber(provider.Extra, "codex_primary_used_percent", "codex_7d_used_percent")
	if !ok || openAIQuotaWindowResetAny(provider.Extra, now, "primary", "7d") {
		return openAIQuotaHeadroomNeutralFactor
	}

	factor := 1 - clampQuotaFactor(primaryUsedPercent/100)
	if secondaryUsedPercent, ok := ResolveProviderExtraNumber(provider.Extra, "codex_secondary_used_percent", "codex_5h_used_percent"); ok &&
		!openAIQuotaWindowResetAny(provider.Extra, now, "secondary", "5h") {
		secondaryRemaining := 1 - clampQuotaFactor(secondaryUsedPercent/100)
		if secondaryRemaining < openAIQuotaHeadroomSecondaryLowRemain {
			factor *= openAIQuotaHeadroomNeutralFactor
		}
	}
	return factor
}

// openAIQuotaHeadroomSnapshotStale reports missing, invalid, or expired snapshot timestamps.
func openAIQuotaHeadroomSnapshotStale(extra map[string]any, now time.Time) bool {
	updatedRaw, ok := extra["codex_usage_updated_at"]
	if !ok {
		return true
	}
	updatedAt, err := ParseUsageTime(fmt.Sprint(updatedRaw))
	if err != nil {
		return true
	}
	return now.Sub(updatedAt) >= openAIQuotaHeadroomSnapshotStaleAfter
}

// openAIQuotaWindowResetAny checks reset timing across compatible window field names.
func openAIQuotaWindowResetAny(extra map[string]any, now time.Time, windows ...string) bool {
	for _, window := range windows {
		if OpenAIQuotaWindowReset(extra, window, now) {
			return true
		}
	}
	return false
}

func clampQuotaFactor(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	default:
		return v
	}
}
