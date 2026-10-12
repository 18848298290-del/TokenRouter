package provider

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
)

func TestNormalizeTier(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		expected string
	}{
		{name: "empty string", raw: "", expected: ""},
		{name: "free-tier", raw: "free-tier", expected: "FREE"},
		{name: "g1-pro-tier", raw: "g1-pro-tier", expected: "PRO"},
		{name: "g1-ultra-tier", raw: "g1-ultra-tier", expected: "ULTRA"},
		{name: "unknown-something", raw: "unknown-something", expected: "UNKNOWN"},
		{name: "Google AI Pro contains pro keyword", raw: "Google AI Pro", expected: "PRO"},
		{name: "case insensitive FREE", raw: "FREE-TIER", expected: "FREE"},
		{name: "case insensitive Ultra", raw: "Ultra Plan", expected: "ULTRA"},
		{name: "arbitrary unrecognized string", raw: "enterprise-custom", expected: "UNKNOWN"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeAntigravityTier(tt.raw)
			require.Equal(t, tt.expected, got, "providercore.NormalizeAntigravityTier(%q)", tt.raw)
		})
	}
}

func aqfBoolPtr(v bool) *bool { return &v }

func aqfIntPtr(v int) *int { return &v }

func TestBuildUsageInfo_BasicModels(t *testing.T) {
	fetcher := &AntigravityQuota{}

	modelsResp := &antigravity.FetchAvailableModelsResponse{
		Models: map[string]antigravity.ModelInfo{
			"claude-sonnet-4-20250514": {
				QuotaInfo: &antigravity.ModelQuotaInfo{
					RemainingFraction: 0.75,
					ResetTime:         "2026-03-08T12:00:00Z",
				},
				DisplayName:      "Claude Sonnet 4",
				SupportsImages:   aqfBoolPtr(true),
				SupportsThinking: aqfBoolPtr(false),
				ThinkingBudget:   aqfIntPtr(0),
				Recommended:      aqfBoolPtr(true),
				MaxTokens:        aqfIntPtr(200000),
				MaxOutputTokens:  aqfIntPtr(16384),
				SupportedMimeTypes: map[string]bool{
					"image/png":  true,
					"image/jpeg": true,
				},
			},
			"gemini-2.5-pro": {
				QuotaInfo: &antigravity.ModelQuotaInfo{
					RemainingFraction: 0.50,
					ResetTime:         "2026-03-08T15:00:00Z",
				},
				DisplayName:     "Gemini 2.5 Pro",
				MaxTokens:       aqfIntPtr(1000000),
				MaxOutputTokens: aqfIntPtr(65536),
			},
		},
	}

	info := fetcher.BuildUsageInfo(modelsResp, "g1-pro-tier", "PRO", nil)

	// Check shared display fields.
	require.NotNil(t, info.UpdatedAt, "UpdatedAt should be set")
	require.Equal(t, "PRO", info.SubscriptionTier)
	require.Equal(t, "g1-pro-tier", info.SubscriptionTierRaw)

	// Check per-model used percentages.
	require.Len(t, info.AntigravityQuota, 2)

	sonnetQuota := info.AntigravityQuota["claude-sonnet-4-20250514"]
	require.NotNil(t, sonnetQuota)
	require.Equal(t, 25, sonnetQuota.Utilization) // (1 - 0.75) * 100 = 25
	require.Equal(t, "2026-03-08T12:00:00Z", sonnetQuota.ResetTime)

	geminiQuota := info.AntigravityQuota["gemini-2.5-pro"]
	require.NotNil(t, geminiQuota)
	require.Equal(t, 50, geminiQuota.Utilization) // (1 - 0.50) * 100 = 50
	require.Equal(t, "2026-03-08T15:00:00Z", geminiQuota.ResetTime)

	// Check per-model capability details.
	require.Len(t, info.AntigravityQuotaDetails, 2)

	sonnetDetail := info.AntigravityQuotaDetails["claude-sonnet-4-20250514"]
	require.NotNil(t, sonnetDetail)
	require.Equal(t, "Claude Sonnet 4", sonnetDetail.DisplayName)
	require.Equal(t, aqfBoolPtr(true), sonnetDetail.SupportsImages)
	require.Equal(t, aqfBoolPtr(false), sonnetDetail.SupportsThinking)
	require.Equal(t, aqfIntPtr(0), sonnetDetail.ThinkingBudget)
	require.Equal(t, aqfBoolPtr(true), sonnetDetail.Recommended)
	require.Equal(t, aqfIntPtr(200000), sonnetDetail.MaxTokens)
	require.Equal(t, aqfIntPtr(16384), sonnetDetail.MaxOutputTokens)
	require.Equal(t, map[string]bool{"image/png": true, "image/jpeg": true}, sonnetDetail.SupportedMimeTypes)

	geminiDetail := info.AntigravityQuotaDetails["gemini-2.5-pro"]
	require.NotNil(t, geminiDetail)
	require.Equal(t, "Gemini 2.5 Pro", geminiDetail.DisplayName)
	require.Nil(t, geminiDetail.SupportsImages)
	require.Nil(t, geminiDetail.SupportsThinking)
	require.Equal(t, aqfIntPtr(1000000), geminiDetail.MaxTokens)
	require.Equal(t, aqfIntPtr(65536), geminiDetail.MaxOutputTokens)
}

func TestBuildUsageInfo_DeprecatedModels(t *testing.T) {
	fetcher := &AntigravityQuota{}

	modelsResp := &antigravity.FetchAvailableModelsResponse{
		Models: map[string]antigravity.ModelInfo{
			"claude-sonnet-4-20250514": {
				QuotaInfo: &antigravity.ModelQuotaInfo{
					RemainingFraction: 1.0,
				},
			},
		},
		DeprecatedModelIDs: map[string]antigravity.DeprecatedModelInfo{
			"claude-3-sonnet-20240229": {NewModelID: "claude-sonnet-4-20250514"},
			"claude-3-haiku-20240307":  {NewModelID: "claude-haiku-3.5-latest"},
		},
	}

	info := fetcher.BuildUsageInfo(modelsResp, "", "", nil)

	require.Len(t, info.ModelForwardingRules, 2)
	require.Equal(t, "claude-sonnet-4-20250514", info.ModelForwardingRules["claude-3-sonnet-20240229"])
	require.Equal(t, "claude-haiku-3.5-latest", info.ModelForwardingRules["claude-3-haiku-20240307"])
}

func TestBuildUsageInfo_NoDeprecatedModels(t *testing.T) {
	fetcher := &AntigravityQuota{}

	modelsResp := &antigravity.FetchAvailableModelsResponse{
		Models: map[string]antigravity.ModelInfo{
			"some-model": {
				QuotaInfo: &antigravity.ModelQuotaInfo{RemainingFraction: 0.9},
			},
		},
	}

	info := fetcher.BuildUsageInfo(modelsResp, "", "", nil)

	require.Nil(t, info.ModelForwardingRules, "ModelForwardingRules should be nil when no deprecated models")
}

func TestBuildUsageInfo_EmptyModels(t *testing.T) {
	fetcher := &AntigravityQuota{}

	modelsResp := &antigravity.FetchAvailableModelsResponse{
		Models: map[string]antigravity.ModelInfo{},
	}

	info := fetcher.BuildUsageInfo(modelsResp, "", "", nil)

	require.NotNil(t, info)
	require.NotNil(t, info.AntigravityQuota)
	require.Empty(t, info.AntigravityQuota)
	require.NotNil(t, info.AntigravityQuotaDetails)
	require.Empty(t, info.AntigravityQuotaDetails)
	require.Nil(t, info.FiveHour, "FiveHour should be nil when no priority model exists")
}

func TestBuildUsageInfo_ModelWithNilQuotaInfo(t *testing.T) {
	fetcher := &AntigravityQuota{}

	modelsResp := &antigravity.FetchAvailableModelsResponse{
		Models: map[string]antigravity.ModelInfo{
			"model-without-quota": {
				DisplayName: "No Quota Model",
				// This model omits QuotaInfo.
			},
		},
	}

	info := fetcher.BuildUsageInfo(modelsResp, "", "", nil)

	require.NotNil(t, info)
	require.Empty(t, info.AntigravityQuota, "models with nil QuotaInfo should be skipped")
	require.Empty(t, info.AntigravityQuotaDetails, "models with nil QuotaInfo should be skipped from details too")
}

func TestBuildUsageInfo_FiveHourPriorityOrder(t *testing.T) {
	fetcher := &AntigravityQuota{}

	// priorityModels = ["claude-sonnet-4-20250514", "claude-sonnet-4", "gemini-2.5-pro"]
	// FiveHour uses the first available priority model's quota.
	modelsResp := &antigravity.FetchAvailableModelsResponse{
		Models: map[string]antigravity.ModelInfo{
			"gemini-2.5-pro": {
				QuotaInfo: &antigravity.ModelQuotaInfo{
					RemainingFraction: 0.40,
					ResetTime:         "2026-03-08T18:00:00Z",
				},
			},
			"claude-sonnet-4-20250514": {
				QuotaInfo: &antigravity.ModelQuotaInfo{
					RemainingFraction: 0.80,
					ResetTime:         "2026-03-08T12:00:00Z",
				},
			},
		},
	}

	info := fetcher.BuildUsageInfo(modelsResp, "", "", nil)

	require.NotNil(t, info.FiveHour, "FiveHour should be set when a priority model exists")
	// claude-sonnet-4-20250514 takes precedence over the other priority models.
	expectedUtilization := (1.0 - 0.80) * 100 // 20
	require.InDelta(t, expectedUtilization, info.FiveHour.Utilization, 0.01)
	require.NotNil(t, info.FiveHour.ResetsAt, "ResetsAt should be parsed from ResetTime")
}

func TestBuildUsageInfo_FiveHourFallbackToClaude4(t *testing.T) {
	fetcher := &AntigravityQuota{}

	// Only the second priority model, claude-sonnet-4, is available.
	modelsResp := &antigravity.FetchAvailableModelsResponse{
		Models: map[string]antigravity.ModelInfo{
			"claude-sonnet-4": {
				QuotaInfo: &antigravity.ModelQuotaInfo{
					RemainingFraction: 0.60,
					ResetTime:         "2026-03-08T14:00:00Z",
				},
			},
			"gemini-2.5-pro": {
				QuotaInfo: &antigravity.ModelQuotaInfo{
					RemainingFraction: 0.30,
				},
			},
		},
	}

	info := fetcher.BuildUsageInfo(modelsResp, "", "", nil)

	require.NotNil(t, info.FiveHour)
	expectedUtilization := (1.0 - 0.60) * 100 // 40
	require.InDelta(t, expectedUtilization, info.FiveHour.Utilization, 0.01)
}

func TestBuildUsageInfo_FiveHourFallbackToGemini(t *testing.T) {
	fetcher := &AntigravityQuota{}

	// Only the third priority model, gemini-2.5-pro, is available.
	modelsResp := &antigravity.FetchAvailableModelsResponse{
		Models: map[string]antigravity.ModelInfo{
			"gemini-2.5-pro": {
				QuotaInfo: &antigravity.ModelQuotaInfo{
					RemainingFraction: 0.30,
				},
			},
			"other-model": {
				QuotaInfo: &antigravity.ModelQuotaInfo{
					RemainingFraction: 0.90,
				},
			},
		},
	}

	info := fetcher.BuildUsageInfo(modelsResp, "", "", nil)

	require.NotNil(t, info.FiveHour)
	expectedUtilization := (1.0 - 0.30) * 100 // 70
	require.InDelta(t, expectedUtilization, info.FiveHour.Utilization, 0.01)
}

func TestBuildUsageInfo_FiveHourNoPriorityModel(t *testing.T) {
	fetcher := &AntigravityQuota{}

	// No priority model is present.
	modelsResp := &antigravity.FetchAvailableModelsResponse{
		Models: map[string]antigravity.ModelInfo{
			"some-other-model": {
				QuotaInfo: &antigravity.ModelQuotaInfo{
					RemainingFraction: 0.50,
				},
			},
		},
	}

	info := fetcher.BuildUsageInfo(modelsResp, "", "", nil)

	require.Nil(t, info.FiveHour, "FiveHour should be nil when no priority model exists")
}

func TestBuildUsageInfo_FiveHourWithEmptyResetTime(t *testing.T) {
	fetcher := &AntigravityQuota{}

	modelsResp := &antigravity.FetchAvailableModelsResponse{
		Models: map[string]antigravity.ModelInfo{
			"claude-sonnet-4-20250514": {
				QuotaInfo: &antigravity.ModelQuotaInfo{
					RemainingFraction: 0.50,
					ResetTime:         "", // empty reset time
				},
			},
		},
	}

	info := fetcher.BuildUsageInfo(modelsResp, "", "", nil)

	require.NotNil(t, info.FiveHour)
	require.Nil(t, info.FiveHour.ResetsAt, "ResetsAt should be nil when ResetTime is empty")
	require.Equal(t, 0, info.FiveHour.RemainingSeconds)
}

func TestBuildUsageInfo_FullUtilization(t *testing.T) {
	fetcher := &AntigravityQuota{}

	modelsResp := &antigravity.FetchAvailableModelsResponse{
		Models: map[string]antigravity.ModelInfo{
			"claude-sonnet-4-20250514": {
				QuotaInfo: &antigravity.ModelQuotaInfo{
					RemainingFraction: 0.0, // fully used
					ResetTime:         "2026-03-08T12:00:00Z",
				},
			},
		},
	}

	info := fetcher.BuildUsageInfo(modelsResp, "", "", nil)

	quota := info.AntigravityQuota["claude-sonnet-4-20250514"]
	require.NotNil(t, quota)
	require.Equal(t, 100, quota.Utilization)
}

func TestBuildUsageInfo_ZeroUtilization(t *testing.T) {
	fetcher := &AntigravityQuota{}

	modelsResp := &antigravity.FetchAvailableModelsResponse{
		Models: map[string]antigravity.ModelInfo{
			"claude-sonnet-4-20250514": {
				QuotaInfo: &antigravity.ModelQuotaInfo{
					RemainingFraction: 1.0, // fully available
				},
			},
		},
	}

	info := fetcher.BuildUsageInfo(modelsResp, "", "", nil)
	quota := info.AntigravityQuota["claude-sonnet-4-20250514"]
	require.NotNil(t, quota)
	require.Equal(t, 0, quota.Utilization)
}

func TestBuildUsageInfo_AICredits(t *testing.T) {
	fetcher := &AntigravityQuota{}
	modelsResp := &antigravity.FetchAvailableModelsResponse{
		Models: map[string]antigravity.ModelInfo{},
	}
	loadResp := &antigravity.LoadCodeAssistResponse{
		PaidTier: &antigravity.PaidTierInfo{
			ID: "g1-pro-tier",
			AvailableCredits: []antigravity.AvailableCredit{
				{
					CreditType:                  "GOOGLE_ONE_AI",
					CreditAmount:                "25",
					MinimumCreditAmountForUsage: "5",
				},
			},
		},
	}

	info := fetcher.BuildUsageInfo(modelsResp, "g1-pro-tier", "PRO", loadResp)

	require.Len(t, info.AICredits, 1)
	require.Equal(t, "GOOGLE_ONE_AI", info.AICredits[0].CreditType)
	require.Equal(t, 25.0, info.AICredits[0].Amount)
	require.Equal(t, 5.0, info.AICredits[0].MinimumBalance)
}
