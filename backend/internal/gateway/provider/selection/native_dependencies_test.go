package selection

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

func TestResolveProviderUpstreamModel_Antigravity(t *testing.T) {
	t.Parallel()
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, Platform: capability.PlatformAntigravity}}
	// Antigravity uses DefaultAntigravityModelMapping.
	got := resolveProviderUpstreamModel(context.Background(), provider, "claude-sonnet-4-6")
	require.Equal(t, "claude-sonnet-4-6", got)
}

func TestResolveProviderUpstreamModel_Antigravity_Unsupported(t *testing.T) {
	t.Parallel()
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformAntigravity}}
	got := resolveProviderUpstreamModel(context.Background(), provider, "totally-unknown-model")
	require.Equal(t, "totally-unknown-model", got, "空白名单允许未命中映射的模型按请求名称通过")
}

func TestResolveProviderUpstreamModel_NonAntigravity(t *testing.T) {
	t.Parallel()
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, Platform: capability.PlatformAnthropic}}
	got := resolveProviderUpstreamModel(context.Background(), provider, "claude-sonnet-4-6")
	require.Equal(t, "claude-sonnet-4-6", got, "no mapping = passthrough")
}

func TestResolveProviderUpstreamModel_AnthropicOAuthAppliesMappingBeforeNormalization(t *testing.T) {
	t.Parallel()
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformAnthropic,
			Type: capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"model_mapping": map[string]any{"client-alias": "claude-sonnet-4-5"},
			},
		},
	}

	got := resolveProviderUpstreamModel(context.Background(), provider, "client-alias")
	require.Equal(t, "claude-sonnet-4-5", got)
}

func TestResolveProviderUpstreamModel_BedrockUsesRegionalFinalModel(t *testing.T) {
	t.Parallel()
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformAnthropic,
			Type: capability.ProviderTypeBedrock,
			Credentials: map[string]any{
				"model_whitelist": []string{"*"},
				"aws_region":      "us-east-1",
			},
		},
	}

	got := resolveProviderUpstreamModel(context.Background(), provider, "claude-sonnet-4-5")
	require.Equal(t, "us.anthropic.claude-sonnet-4-5-20250929-v1:0", got)
}

func TestResolveProviderUpstreamModel_AntigravityUsesThinkingContext(t *testing.T) {
	t.Parallel()
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, Platform: capability.PlatformAntigravity}}
	ctx := requeststate.WithThinkingEnabled(context.Background(), true)

	got := resolveProviderUpstreamModel(ctx, provider, "claude-sonnet-4-5")
	require.Equal(t, "claude-sonnet-4-5-thinking", got)
}

func TestGatewayAnthropicProviderSupportMapsBeforePlatformNormalization(t *testing.T) {
	tests := []struct {
		name           string
		providerType   string
		finalModel     string
		whitelistModel string
	}{
		{
			name:           "OAuth",
			providerType:   capability.ProviderTypeOAuth,
			finalModel:     "claude-sonnet-4-5-20250929",
			whitelistModel: "claude-sonnet-4-5-20250929",
		},
		{
			name:           "ServiceAccount",
			providerType:   capability.ProviderTypeServiceAccount,
			finalModel:     "claude-sonnet-4-5@20250929",
			whitelistModel: "claude-sonnet-4-5@20250929",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, Platform: capability.PlatformAnthropic,
					Type: tt.providerType,
					Credentials: map[string]any{
						"model_mapping":   map[string]any{"group-model": "claude-sonnet-4-5-20250929"},
						"model_whitelist": []any{tt.whitelistModel},
					},
				},
			}

			require.True(t, gatewayprovider.ExecutionModelPolicy(provider).Supports(context.Background(), "group-model"))
			require.Equal(t, tt.finalModel, resolveProviderUpstreamModel(context.Background(), provider, "group-model"))
		})
	}
}
