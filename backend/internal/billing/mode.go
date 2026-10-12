package billing

import (
	"strings"
)

// API Key funding modes. Auto uses subscription quota first and balance for the remainder.
const (
	APIKeyBillingModeAuto         = "auto"
	APIKeyBillingModeSubscription = "subscription"
	APIKeyBillingModeBalance      = "balance"
)

// NormalizeAPIKeyBillingMode trims, lowercases, and validates the funding mode.
// An empty value selects auto for clients that omit this field.
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
