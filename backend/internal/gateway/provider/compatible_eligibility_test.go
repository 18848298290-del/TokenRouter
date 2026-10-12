package provider

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

func TestEvaluateOpenAIQuotaAutoPause_UsesGlobalDefaultAndWindowReset(t *testing.T) {
	ctx := WithQuotaAutoPauseSettings(context.Background(), providercore.QuotaAutoPauseSettings{DefaultThreshold5h: 0.95})
	now := time.Now().UTC()
	provider := &ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 9001,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Extra: map[string]any{
				"codex_usage_updated_at": now.Format(time.RFC3339),
				"codex_5h_used_percent":  96.0,
				"codex_5h_reset_at":      now.Add(time.Hour).Format(time.RFC3339),
			},
		},
	}

	paused, _ := OpenAIQuotaPause(ctx, provider)
	require.True(t, paused)

	provider.Record.Extra["codex_5h_reset_at"] = now.Add(-time.Minute).Format(time.RFC3339)
	paused, _ = OpenAIQuotaPause(ctx, provider)
	require.False(t, paused)
}

func TestEvaluateOpenAIQuotaAutoPause_PerProviderDisableOverridesGlobalDefault(t *testing.T) {
	ctx := WithQuotaAutoPauseSettings(context.Background(), providercore.QuotaAutoPauseSettings{DefaultThreshold5h: 0.95})
	provider := &ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 9002,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Extra: map[string]any{
				"codex_5h_used_percent":  99.0,
				"auto_pause_5h_disabled": true,
			},
		},
	}

	paused, _ := OpenAIQuotaPause(ctx, provider)
	require.False(t, paused)
}

// TestCompatibleQuotaSnapshotPreservesParentAttemptState checks that derived attempts isolate thresholds and retain
// health-observation models.
func TestCompatibleQuotaSnapshotPreservesParentAttemptState(t *testing.T) {
	parent := requeststate.WithExecutionHints(context.Background(), requeststate.ExecutionHints{HealthModel: "observed-model"})
	parent = WithQuotaAutoPauseSettings(parent, providercore.QuotaAutoPauseSettings{DefaultThreshold5h: 0.99})
	child := WithQuotaAutoPauseSettings(parent, providercore.QuotaAutoPauseSettings{DefaultThreshold5h: 0.95})
	now := time.Now()
	value := &ExecutionProvider{Record: providercore.Record{ID: 9020, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Extra: map[string]any{"codex_usage_updated_at": now.UTC().Format(time.RFC3339), "codex_5h_used_percent": 96.0, "codex_5h_reset_at": now.Add(time.Hour).UTC().Format(time.RFC3339)}}}
	paused, _ := OpenAIQuotaPause(child, value)
	require.True(t, paused)
	paused, _ = OpenAIQuotaPause(parent, value)
	require.False(t, paused)
	require.Equal(t, "observed-model", requeststate.ExecutionHintsFromContext(child).HealthModel)
}

// TestGrokMediaCapabilityFiltersOnlyGeneration checks that media eligibility restricts generation requests.
func TestGrokMediaCapabilityFiltersOnlyGeneration(t *testing.T) {
	provider := &ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 1,
			Platform:    capability.PlatformGrok,
			Type:        capability.ProviderTypeOAuth,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Extra:       map[string]any{providercore.GrokMediaEligibleExtraKey: false},
		},
	}

	require.True(t, provideradapter.SupportsOpenAIEndpoint(ExecutionProtocolRecord(provider), providercore.OpenAIEndpointCapabilityTextGeneration))
	require.False(t, provideradapter.SupportsOpenAIEndpoint(ExecutionProtocolRecord(provider), providercore.OpenAIEndpointCapabilityGrokMediaGeneration))
	require.False(t, CompatibleProviderEligible(
		context.Background(), provider, capability.PlatformGrok, "grok-imagine-video", false,
		providercore.OpenAIEndpointCapabilityGrokMediaGeneration,
	))
}
