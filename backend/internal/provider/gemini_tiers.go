package provider

import (
	"strings"
)

// gemini_tiers.go normalizes Gemini account-tier identifiers and filters them
// by OAuth variant.
// Gemini account tier labels arrive as canonical IDs, historical names, and
// hyphenated Code Assist values. Normalization maps those aliases to local
// credential IDs, then OAuth-specific filtering rejects recognized tiers
// belonging to another variant while allowing unknown variants to carry any
// recognized ID.
//
// Recognition and compatibility are separate decisions. Google One Unknown
// remains a recognized observation, but the Google One compatibility filter
// rejects it so authorization and quota callers can apply their own defaults.
// Empty or unknown variant text accepts recognized labels from any family,
// leaving the owning caller to choose whether that account metadata applies.
// These helpers normalize identifiers; quota values are selected by
// GeminiQuotaPolicy.

// Gemini tier constants name stored canonical labels and accepted historical
// aliases used by authorization discovery, quota policy lookup, and credential
// normalization.
const (
	// Canonical IDs are stored in credentials and select the matching local
	// family during authorization and quota lookup. Unknown retains an unresolved
	// Google One observation but is filtered from that variant’s compatible set.
	// Google One free account.
	GeminiTierGoogleOneFree = "google_one_free"
	// Google AI Pro account.
	GeminiTierGoogleAIPro = "google_ai_pro"
	// Google AI Ultra account.
	GeminiTierGoogleAIUltra = "google_ai_ultra"
	// Code Assist standard account.
	GeminiTierGCPStandard = "gcp_standard"
	// Code Assist enterprise account.
	GeminiTierGCPEnterprise = "gcp_enterprise"
	// AI Studio free account.
	GeminiTierAIStudioFree = "aistudio_free"
	// AI Studio paid account.
	GeminiTierAIStudioPaid = "aistudio_paid"
	// unresolved Google One observation.
	GeminiTierGoogleOneUnknown = "google_one_unknown"

	// Historical labels are accepted by CanonicalGeminiTierID and converted to
	// current local families, allowing stored records and upstream responses to
	// use the same later quota keys.
	// historical Pro tier alias.
	LegacyTierAIPremium = "AI_PREMIUM"
	// historical free tier alias.
	LegacyTierGoogleOneStandard = "GOOGLE_ONE_STANDARD"
	// historical free tier alias.
	LegacyTierGoogleOneBasic = "GOOGLE_ONE_BASIC"
	// historical free tier alias.
	LegacyTierFree = "FREE"
	// historical unknown tier alias.
	LegacyTierGoogleOneUnknown = "GOOGLE_ONE_UNKNOWN"
	// historical Ultra tier alias.
	LegacyTierGoogleOneUnlimited = "GOOGLE_ONE_UNLIMITED"
)

// CanonicalGeminiTierID trims input and maps recognized canonical or legacy
// text to a local ID. Matching ignores case, hyphenated Code Assist aliases
// are accepted, and blank or unknown text returns empty.
func CanonicalGeminiTierID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	lower := strings.ToLower(raw)
	switch lower {
	case GeminiTierGoogleOneFree,
		GeminiTierGoogleAIPro,
		GeminiTierGoogleAIUltra,
		GeminiTierGCPStandard,
		GeminiTierGCPEnterprise,
		GeminiTierAIStudioFree,
		GeminiTierAIStudioPaid,
		GeminiTierGoogleOneUnknown:
		return lower
	}

	// Legacy PRO and ULTRA identify Code Assist families here. Map those
	// historical labels before hyphenated forms so later quota lookup receives
	// the registered local family.
	upper := strings.ToUpper(raw)
	switch upper {
	// Legacy Google One tiers.
	case LegacyTierAIPremium:
		return GeminiTierGoogleAIPro
	case LegacyTierGoogleOneUnlimited:
		return GeminiTierGoogleAIUltra
	case LegacyTierFree, LegacyTierGoogleOneBasic, LegacyTierGoogleOneStandard:
		return GeminiTierGoogleOneFree
	case LegacyTierGoogleOneUnknown:
		return GeminiTierGoogleOneUnknown

	// Legacy Code Assist tiers.
	case "STANDARD", "PRO", "LEGACY":
		return GeminiTierGCPStandard
	case "ENTERPRISE", "ULTRA":
		return GeminiTierGCPEnterprise
	}

	// Hyphenated Code Assist IDs name the same local families as historical
	// uppercase labels, allowing quota lookup to use one canonical identifier.
	switch lower {
	case "standard-tier", "pro-tier":
		return GeminiTierGCPStandard
	case "ultra-tier":
		return GeminiTierGCPEnterprise
	}

	return ""
}

// CanonicalGeminiTierIDForOAuthType normalizes tier text and filters it for
// the trimmed lowercase OAuth variant. Unknown or empty variants accept any
// recognized ID; known variants reject foreign-family tiers and Google One
// Unknown.
//
// That distinction lets callers separate unrecognized input from a compatible
// account label before applying their own default. Google One accepts Free,
// Pro, and Ultra, Code Assist accepts Standard and Enterprise, and AI Studio
// accepts Free and Paid. No quota values are inferred here; callers use the
// resulting identifier for their own policy or metadata selection.
func CanonicalGeminiTierIDForOAuthType(oauthType, tierID string) string {
	oauthType = strings.ToLower(strings.TrimSpace(oauthType))
	canonical := CanonicalGeminiTierID(tierID)
	if canonical == "" {
		return ""
	}

	switch oauthType {
	case "google_one":
		switch canonical {
		case GeminiTierGoogleOneFree, GeminiTierGoogleAIPro, GeminiTierGoogleAIUltra:
			return canonical
		default:
			return ""
		}
	case "code_assist":
		switch canonical {
		case GeminiTierGCPStandard, GeminiTierGCPEnterprise:
			return canonical
		default:
			return ""
		}
	case "ai_studio":
		switch canonical {
		case GeminiTierAIStudioFree, GeminiTierAIStudioPaid:
			return canonical
		default:
			return ""
		}
	default:
		// Blank or unknown variants leave a recognized tier available for the
		// owning authorization caller to interpret before applying its variant policy.
		return canonical
	}
}
