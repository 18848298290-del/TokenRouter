package provider

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// New Grok media generation applies an OAuth billing gate, while text
// forwarding and existing media queries apply their own eligibility rules. An
// administrator
// override is evaluated before OAuth billing evidence. OAuth accounts with
// absent, forbidden, free, or inconclusive billing are excluded from this
// generation decision, and non-OAuth Grok accounts pass its billing gate.
//
// Free classification combines several observations with defined precedence. A
// nonempty tier returned by the current access-token parser decides
// immediately.
// Otherwise explicit paid evidence from billing, quota, or credentials
// overrides free and inferred signals. A weekly observation lacking accepted
// plan, percentage, or positive monthly-limit evidence supplies no accepted
// quota signal by its window label alone. Accepted signal presence still needs
// the separate forbidden and free checks.

// GrokTierRules supplies platform parsers used to classify subscription
// evidence.
type GrokTierRules struct {
	// SubscriptionTierFromJWT, NormalizeSubscriptionTier, and
	// IsFreeRollingTokenLimit are required for reached classification
	// paths. The
	// caller supplies consistent platform rules so JWT and stored
	// observations use
	// the same tier meanings.
	SubscriptionTierFromJWT   func(string) string // current access-token tier parser
	NormalizeSubscriptionTier func(string) string // canonical subscription label parser
	IsFreeRollingTokenLimit   func(int64) bool    // free token allowance recognizer
}

// KnownGrokFreeProvider reports whether a Grok OAuth row has free evidence
// without an overriding paid signal. Nil and other provider kinds return false;
// callers supply usable parsing callbacks.
//
// A nonempty current JWT tier determines the result first. Otherwise billing,
// rolling quota, and subscription_tier credentials contribute explicit free or
// paid evidence, while recognized small token limits and monthly observations
// can infer free status. Paid signals win across those sources. Parsing
// failures omit their source, so the result describes accepted evidence rather
// than authenticating or checking the age of every observation.
func KnownGrokFreeProvider(provider *Record, rules GrokTierRules) bool {
	if provider == nil || !provider.IsGrokOAuth() {
		return false
	}
	// The current access token's tier overrides older billing and
	// credential snapshots after a tier change.
	if jwtTier := rules.SubscriptionTierFromJWT(provider.GetCredential("access_token")); jwtTier != "" {
		return isGrokFreeSubscriptionTier(jwtTier, rules)
	}
	freeSignal := false
	paidSignal := false
	inferredFreeSignal := false
	// A billing decode failure contributes no tier evidence. Continue with
	// the rolling snapshot and credentials so those accepted sources can
	// determine classification.
	if billing, err := ParseGrokBillingSnapshot(provider.Extra); err == nil && billing != nil {
		if tier := strings.TrimSpace(billing.Plan); tier != "" {
			if isGrokFreeSubscriptionTier(tier, rules) {
				freeSignal = true
			} else if !isGrokUnknownSubscriptionTier(tier) {
				paidSignal = true
			}
		}
		// Populated percentage pointers also represent zero usage.
		// Their presence
		// or a positive monthly allowance supplies paid evidence even
		// when another
		// stored source still says free.
		if billing.UsagePercent != nil || billing.UsedPercent != nil ||
			(billing.MonthlyLimitCents != nil && *billing.MonthlyLimitCents > 0) {
			paidSignal = true
		}
		// Nonblank MonthlyUpdatedAt selects monthly inference without
		// timestamp
		// parsing. A complete 2xx summary can also infer free status
		// after the
		// final paid-signal precedence is applied.
		if strings.TrimSpace(billing.MonthlyUpdatedAt) != "" ||
			(billing.StatusCode >= 200 && billing.StatusCode < 300 &&
				!billing.Partial && len(billing.FailedWindows) == 0) {
			inferredFreeSignal = true
		}
	}
	// An undecodable quota representation cannot contribute its plan or
	// free
	// token limit; retained credential evidence still participates in the
	// final
	// precedence decision.
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
	// Use the credential subscription_tier here; plan_type and arbitrary
	// extra fields are not tier evidence.
	if tier := strings.TrimSpace(provider.GetCredential("subscription_tier")); tier != "" {
		if isGrokFreeSubscriptionTier(tier, rules) {
			freeSignal = true
		} else if !isGrokUnknownSubscriptionTier(tier) {
			paidSignal = true
		}
	}
	// Paid evidence takes precedence over free signals; the stored
	// free-token
	// allowance participates without a freshness check in this
	// classification.
	return !paidSignal && (freeSignal || inferredFreeSignal)
}

// isGrokFreeSubscriptionTier reports whether platform normalization classifies
// the tier as free or x_basic.
func isGrokFreeSubscriptionTier(tier string, rules GrokTierRules) bool {
	switch rules.NormalizeSubscriptionTier(tier) {
	case "free", "x_basic":
		return true
	default:
		return false
	}
}

// isGrokUnknownSubscriptionTier recognizes blank and case-insensitive
// placeholder subscription labels.
func isGrokUnknownSubscriptionTier(tier string) bool {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "", "unknown", "n/a", "none":
		return true
	default:
		return false
	}
}

// GrokMediaGenerationEligibility returns whether this Grok row can start media
// generation and a machine-readable reason. Nil or non-Grok records fail before
// overrides; a recognized override then decides, and other Grok credential
// types pass the OAuth billing gate.
//
// OAuth requires decoded billing without any recorded 403, no winning free
// signal, and an authoritative quota signal. Those checks are ordered so a
// paid-looking summary with forbidden access remains excluded. This function
// interprets the supplied record only; refreshing absent billing is a separate
// probe, and scheduling/status obligations belong to its caller.
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

	// New OAuth media generation needs accepted billing evidence even when
	// a
	// token names a paid tier; decode errors remain an unobserved-billing
	// rejection for the probe caller.
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
