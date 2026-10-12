package provider

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// GrokTierRules supplies upstream tier parsing and free-quota recognition.
type GrokTierRules struct {
	SubscriptionTierFromJWT   func(string) string
	NormalizeSubscriptionTier func(string) string
	IsFreeRollingTokenLimit   func(int64) bool
}

func KnownGrokFreeProvider(provider *Record, rules GrokTierRules) bool {
	if provider == nil || !provider.IsGrokOAuth() {
		return false
	}
	// The current access token's tier overrides older billing and credential snapshots after a tier change.
	if jwtTier := rules.SubscriptionTierFromJWT(provider.GetCredential("access_token")); jwtTier != "" {
		return isGrokFreeSubscriptionTier(jwtTier, rules)
	}
	freeSignal := false
	paidSignal := false
	inferredFreeSignal := false
	if billing, err := ParseGrokBillingSnapshot(provider.Extra); err == nil && billing != nil {
		if tier := strings.TrimSpace(billing.Plan); tier != "" {
			if isGrokFreeSubscriptionTier(tier, rules) {
				freeSignal = true
			} else if !isGrokUnknownSubscriptionTier(tier) {
				paidSignal = true
			}
		}
		// Usage percentages or a positive monthly spending limit count as paid-plan evidence.
		if billing.UsagePercent != nil || billing.UsedPercent != nil ||
			(billing.MonthlyLimitCents != nil && *billing.MonthlyLimitCents > 0) {
			paidSignal = true
		}
		// A successful monthly observation without paid evidence can identify a free provider.
		// An incomplete observation qualifies only when MonthlyUpdatedAt confirms monthly data.
		if strings.TrimSpace(billing.MonthlyUpdatedAt) != "" ||
			(billing.StatusCode >= 200 && billing.StatusCode < 300 &&
				!billing.Partial && len(billing.FailedWindows) == 0) {
			inferredFreeSignal = true
		}
	}
	if snapshot, err := GrokQuotaSnapshotFromExtra(provider.Extra); err == nil && snapshot != nil {
		if tier := strings.TrimSpace(snapshot.SubscriptionTier); tier != "" {
			if isGrokFreeSubscriptionTier(tier, rules) {
				freeSignal = true
			} else if !isGrokUnknownSubscriptionTier(tier) {
				paidSignal = true
			}
		}
		if snapshot.Tokens != nil && snapshot.Tokens.Limit != nil &&
			rules.IsFreeRollingTokenLimit(*snapshot.Tokens.Limit) {
			inferredFreeSignal = true
		}
	}
	// Use the credential subscription_tier here; plan_type and arbitrary extra fields are not tier evidence.
	if tier := strings.TrimSpace(provider.GetCredential("subscription_tier")); tier != "" {
		if isGrokFreeSubscriptionTier(tier, rules) {
			freeSignal = true
		} else if !isGrokUnknownSubscriptionTier(tier) {
			paidSignal = true
		}
	}
	// Paid evidence takes precedence over free signals, including a stale free-token limit.
	return !paidSignal && (freeSignal || inferredFreeSignal)
}

func isGrokFreeSubscriptionTier(tier string, rules GrokTierRules) bool {
	switch rules.NormalizeSubscriptionTier(tier) {
	case "free", "x_basic":
		return true
	default:
		return false
	}
}

func isGrokUnknownSubscriptionTier(tier string) bool {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "", "unknown", "n/a", "none":
		return true
	default:
		return false
	}
}

func GrokMediaGenerationEligibility(a *Record, rules GrokTierRules) (bool, string) {
	if a == nil || !a.IsGrok() {
		return false, "not_grok"
	}
	if override, ok := GrokMediaEligibilityOverride(a.Extra); ok {
		if override {
			return true, "override_enabled"
		}
		return false, "override_disabled"
	}
	if a.Type != capability.ProviderTypeOAuth {
		return true, "non_oauth"
	}

	billing, err := ParseGrokBillingSnapshot(a.Extra)
	if err != nil || billing == nil {
		return false, "billing_unobserved"
	}
	if billing.StatusCode == 403 || billing.WeeklyStatusCode == 403 || billing.MonthlyStatusCode == 403 {
		return false, "billing_forbidden"
	}
	if KnownGrokFreeProvider(a, rules) {
		return false, "billing_free_tier"
	}
	if !GrokBillingHasAuthoritativeQuota(billing) {
		return false, "billing_inconclusive"
	}
	return true, "eligible"
}
