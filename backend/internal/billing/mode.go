package billing

import (
	"strings"
)

// API keys can use automatic funding, one selected subscription, or wallet
// balance. Missing mode input selects automatic funding for older callers,
// while unsupported text remains distinguishable from that default so
// validation can reject an unrecognized user choice.

// Funding-mode strings are persisted on keys and read by admission and
// settlement to choose subscription quota, wallet balance, or automatic
// source allocation.
const (
	APIKeyBillingModeAuto         = "auto"         // subscriptions then wallet
	APIKeyBillingModeSubscription = "subscription" // selected subscription funding
	APIKeyBillingModeBalance      = "balance"      // wallet balance funding
)

// NormalizeAPIKeyBillingMode trims and lowercases a supported funding mode.
// Empty input returns auto with true for callers that omit the selection;
// unsupported input returns an empty mode with false so callers can reject
// it or choose their own compatibility handling.
func NormalizeAPIKeyBillingMode(value string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "", APIKeyBillingModeAuto:
		return APIKeyBillingModeAuto, true
	case APIKeyBillingModeSubscription, APIKeyBillingModeBalance:
		return normalized, true
	default:
		return "", false
	}
}
