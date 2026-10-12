package selection

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

func TestGrokTeamModelRateLimit_MarksAndFiltersSiblings(t *testing.T) {
	// Use a time-qualified team ID to separate this fixture's cooldown state.
	team := "team-test-" + time.Now().Format("150405.000")
	a1 := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 101, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth,
			Credentials: map[string]any{"model_whitelist": []string{"*"}, "team_id": team},
		},
	}
	a2 := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 102, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth,
			Credentials: map[string]any{"model_whitelist": []string{"*"}, "team_id": team},
		},
	}
	other := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 103, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth,
			Credentials: map[string]any{"model_whitelist": []string{"*"}, "team_id": team + "-other"},
		},
	}
	noTeam := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 104, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth,
			Credentials: map[string]any{"model_whitelist": []string{"*"}},
		},
	}

	now := time.Now()
	providercore.MarkGrokTeamModelRateLimit(gatewayprovider.ExecutionRecord(a1), "grok-4.5", now.Add(5*time.Minute))

	require.True(t, isGrokTeamModelRateLimited(a1, "grok-4.5", now))
	require.True(t, isGrokTeamModelRateLimited(a2, "grok-4.5", now), "sibling with same team must cool")
	require.False(t, isGrokTeamModelRateLimited(a2, "grok-4.3", now), "other model stays pickable")
	require.False(t, isGrokTeamModelRateLimited(other, "grok-4.5", now))
	require.False(t, isGrokTeamModelRateLimited(noTeam, "grok-4.5", now))

	filtered := filterGrokTeamModelRateLimitedProviders([]gatewayprovider.ExecutionProvider{*a1, *a2, *other, *noTeam}, "grok-4.5", now)
	require.Len(t, filtered, 2)
	ids := []int64{filtered[0].Record.ID, filtered[1].Record.ID}
	require.Contains(t, ids, int64(103))
	require.Contains(t, ids, int64(104))
}

func TestGrokTeamModelRateLimit_Expires(t *testing.T) {
	team := "team-expire-" + time.Now().Format("150405.000")
	a := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 201, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth,
			Credentials: map[string]any{"model_whitelist": []string{"*"}, "team_id": team},
		},
	}
	past := time.Now().Add(-time.Minute)
	providercore.MarkGrokTeamModelRateLimit(gatewayprovider.ExecutionRecord(a), "grok-4.5", past)
	// Marking an expired deadline substitutes the default TTL from the current time.
	// The resulting cooldown must therefore still be active.
	require.True(t, isGrokTeamModelRateLimited(a, "grok-4.5", time.Now()))
}

func TestGrokTeamModelRateLimitFilterUsesMappedUpstreamModel(t *testing.T) {
	now := time.Now()
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 301,
			Platform: capability.PlatformGrok,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"team_id":       "team-mapped-301",
				"model_mapping": map[string]any{"gpt-*": "grok-4.5"},
			},
		},
	}
	providercore.MarkGrokTeamModelRateLimit(gatewayprovider.ExecutionRecord(provider), "grok-4.5", now.Add(time.Hour))

	require.Empty(t, filterGrokTeamModelRateLimitedProviders([]gatewayprovider.ExecutionProvider{*provider}, "gpt-5", now))
}
