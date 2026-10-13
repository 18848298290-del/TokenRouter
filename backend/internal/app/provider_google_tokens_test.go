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

// Official AI Studio quota and third-party Gemini providers follow different
// local admission rules. A usage source reports the official Pro limit, and the
// scenario checks official rejection alongside third-party eligibility,
// cooldown, and quota display with its absent daily buckets.

// geminiFullLocalUsage supplies official Pro quota exhaustion for the precheck
// fixture.
type geminiFullLocalUsage struct {
	// UsageLogRepository supplies unused methods; this fixture implements
	// model statistics for the official quota precheck invoked by the scenario.
	usagecore.UsageLogRepository
}

// TestGeminiThirdPartyAPIKeySkipsLocalQuota compares official free-tier and
// third-party API-Key providers. It checks quota presence, third-party cooldown
// and display buckets, then verifies local Pro exhaustion denies the official
// provider while allowing the third-party path.
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

// GetModelStatsWithFilters returns fifty Pro requests for this quota fixture.
// It ignores filters and dates, providing the fixed exhausted usage observation
// consumed by the official precheck scenario.
func (r *geminiFullLocalUsage) GetModelStatsWithFilters(context.Context, time.Time, time.Time, int64, int64, int64, int64, *int16, *bool, *int8) ([]usagecore.ModelStat, error) {
	return []usagecore.ModelStat{{Model: "gemini-2.5-pro", Requests: 50}}, nil
}
