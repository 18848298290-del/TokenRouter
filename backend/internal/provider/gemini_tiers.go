package provider

import (
	"strings"
)

const (
	// Canonical tier IDs are persisted in provider credentials.
	GeminiTierGoogleOneFree    = "google_one_free"
	GeminiTierGoogleAIPro      = "google_ai_pro"
	GeminiTierGoogleAIUltra    = "google_ai_ultra"
	GeminiTierGCPStandard      = "gcp_standard"
	GeminiTierGCPEnterprise    = "gcp_enterprise"
	GeminiTierAIStudioFree     = "aistudio_free"
	GeminiTierAIStudioPaid     = "aistudio_paid"
	GeminiTierGoogleOneUnknown = "google_one_unknown"

	// Legacy tier IDs accepted from stored credentials and upstream responses.
	LegacyTierAIPremium          = "AI_PREMIUM"
	LegacyTierGoogleOneStandard  = "GOOGLE_ONE_STANDARD"
	LegacyTierGoogleOneBasic     = "GOOGLE_ONE_BASIC"
	LegacyTierFree               = "FREE"
	LegacyTierGoogleOneUnknown   = "GOOGLE_ONE_UNKNOWN"
	LegacyTierGoogleOneUnlimited = "GOOGLE_ONE_UNLIMITED"
)

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

	// Code Assist also returns hyphenated tier IDs.
	switch lower {
	case "standard-tier", "pro-tier":
		return GeminiTierGCPStandard
	case "ultra-tier":
		return GeminiTierGCPEnterprise
	}

	return ""
}

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
		// Unknown OAuth types accept any recognized canonical tier.
		return canonical
	}
}
