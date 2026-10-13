//go:build integration

package provider_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	settingspostgres "github.com/TokenFlux/TokenRouter/internal/settings/postgres"
)

// Gemini quota settings override a static policy after the cached policy
// expires. A dedicated database setting and injected clock exercise that
// timing, while mutation of a returned policy checks isolation and malformed
// settings exercise the static-policy recovery path.

// TestGeminiQuotaPolicyLoadsSettingsAndKeepsSnapshot controls time around a
// dedicated quota setting. Reads check cached and expired ProRPD values,
// isolation of a ProRPD override on the returned policy, and static quota
// recovery for malformed JSON.
func TestGeminiQuotaPolicyLoadsSettingsAndKeepsSnapshot(t *testing.T) {
	ctx := context.Background()
	repo := settings.New(settingspostgres.NewSettingRepository(testEntClient(t)))
	key := "test.gemini.quota.policy"
	require.NoError(t, repo.Delete(ctx, key))
	t.Cleanup(func() { require.NoError(t, repo.Delete(ctx, key)) })
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	core := provider.NewGeminiQuotaService(provider.GeminiQuotaOptions{Now: func() time.Time { return now }, NotFound: settings.ErrSettingNotFound, StaticPolicy: `{"tiers":{"aistudio_free":{"pro_rpd":10}}}`, LoadPolicy: func(ctx context.Context) (string, error) { return repo.GetValue(ctx, key) }})
	before, _ := core.Policy(ctx).QuotaForTier("aistudio_free")
	require.Equal(t, int64(10), before.ProRPD)
	require.NoError(t, repo.Set(ctx, key, `{"quota_rules":{"aistudio_free":{"gemini_pro":{"rpd":25}}}}`))
	cached, _ := core.Policy(ctx).QuotaForTier("aistudio_free")
	require.Equal(t, int64(10), cached.ProRPD)
	// Advance the injected clock past the policy cache interval so this read
	// reloads the current setting and exercises its next parsed or recovery value.
	now = now.Add(time.Minute)
	policy := core.Policy(ctx)
	fresh, _ := policy.QuotaForTier("aistudio_free")
	require.Equal(t, int64(25), fresh.ProRPD)
	rpd := int64(999)
	policy.ApplyOverrides(map[string]provider.GeminiTierQuotaOverride{"aistudio_free": {ProRPD: &rpd}})
	still, _ := core.Policy(ctx).QuotaForTier("aistudio_free")
	require.Equal(t, int64(25), still.ProRPD)
	require.NoError(t, repo.Set(ctx, key, "not-json"))
	// Advance the injected clock past the policy cache interval so this read
	// reloads the current setting and exercises its next parsed or recovery value.
	now = now.Add(time.Minute)
	fallback, _ := core.Policy(ctx).QuotaForTier("aistudio_free")
	require.Equal(t, int64(10), fallback.ProRPD)
}
