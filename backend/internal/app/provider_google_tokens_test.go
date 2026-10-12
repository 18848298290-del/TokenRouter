package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	usagecore "github.com/TokenFlux/TokenRouter/internal/usage"
)

// geminiFullLocalUsage returns usage at the quota limit through the statistics interface.
type geminiFullLocalUsage struct{ usagecore.UsageLogRepository }

func TestGeminiThirdPartyAPIKeySkipsLocalQuota(t *testing.T) {
	ctx := context.Background()
	quotaService := provider.NewGeminiQuotaService(provider.GeminiQuotaOptions{})
	official := &provider.Record{
		ID:       101,
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeAPIKey,
		Credentials: map[string]any{
			"tier_id":       provider.GeminiTierAIStudioFree,
			"provider_type": "official",
		},
	}
	thirdParty := &provider.Record{
		ID:       102,
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeAPIKey,
		Credentials: map[string]any{
			"provider_type": provider.GeminiProviderTypeThirdParty,
		},
	}

	_, officialHasQuota := quotaService.QuotaForProvider(ctx, official)
	_, thirdPartyHasQuota := quotaService.QuotaForProvider(ctx, thirdParty)
	require.True(t, officialHasQuota)
	require.False(t, thirdPartyHasQuota)
	require.True(t, thirdParty.IsGeminiThirdPartyProvider())
	require.Equal(t, 5*time.Minute, quotaService.CooldownForProvider(ctx, thirdParty))

	usageSvc := provider.NewOAuthUsageService(nil, nil, nil, provider.OAuthUsageOptions{Gemini: provider.GeminiUsageOptions{
		Location: geminiQuotaLocation, Quota: quotaService.QuotaForProvider,
		Totals: func(context.Context, int64, time.Time, time.Time) (provider.GeminiUsageTotals, error) {
			return provider.GeminiUsageTotals{}, nil
		},
	}})
	usage, err := usageSvc.GetGeminiUsage(ctx, thirdParty)
	require.NoError(t, err)
	require.Nil(t, usage.GeminiSharedDaily)
	require.Nil(t, usage.GeminiProDaily)
	require.Nil(t, usage.GeminiFlashDaily)

	// The free tier allows 50 Pro requests per day; the fixture reaches that limit.
	rateLimitSvc := provideGeminiPrecheck(quotaService, &geminiFullLocalUsage{})
	officialAllowed, err := rateLimitSvc.PreCheckUsage(ctx, official, "gemini-2.5-pro")
	require.NoError(t, err)
	require.False(t, officialAllowed)

	thirdPartyAllowed, err := rateLimitSvc.PreCheckUsage(ctx, thirdParty, "gemini-2.5-pro")
	require.NoError(t, err)
	require.True(t, thirdPartyAllowed)
}

func (r *geminiFullLocalUsage) GetModelStatsWithFilters(context.Context, time.Time, time.Time, int64, int64, int64, int64, *int16, *bool, *int8) ([]usagecore.ModelStat, error) {
	return []usagecore.ModelStat{{Model: "gemini-2.5-pro", Requests: 50}}, nil
}
