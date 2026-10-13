package provider_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/tierpolicy"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	claude "github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

// TestClaudeAPIKeyFastModeWireEncoding forces Fast on for a priced Anthropic
// API Key model, then forces it off. Speed and Fast beta token are added
// together, removed together, and an unrelated beta token survives removal.
func TestClaudeAPIKeyFastModeWireEncoding(t *testing.T) {
	resolver := fastModeTestResolver()
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeAPIKey}}

	forceOnCtx := fastModeTestContext(apikey.APIKeyFastModePolicyForceOn, "claude-opus-4-8")
	body, headers, err := gatewayprovider.
		ApplyAnthropicFastMode(forceOnCtx, resolver, provider, "claude-opus-4-8", []byte(`{"model":"claude-opus-4-8"}`), http.Header{})
	require.NoError(t, err)
	require.Equal(t, "fast", gjson.GetBytes(body, "speed").String())
	require.True(t, claude.ContainsBetaToken(claude.GetHeaderRaw(headers, "anthropic-beta"), claude.BetaFastMode))

	forceOffCtx := fastModeTestContext(apikey.APIKeyFastModePolicyForceOff, "claude-opus-4-8")
	claude.SetHeaderRaw(headers, "anthropic-beta", claude.BetaFastMode+",context-management-2025-06-27")
	body, headers, err = gatewayprovider.
		ApplyAnthropicFastMode(forceOffCtx, resolver, provider, "claude-opus-4-8", body, headers)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(body, "speed").Exists())
	require.False(t, claude.ContainsBetaToken(claude.GetHeaderRaw(headers, "anthropic-beta"), claude.BetaFastMode))
	require.True(t, claude.ContainsBetaToken(claude.GetHeaderRaw(headers, "anthropic-beta"), "context-management-2025-06-27"))
}

// TestClaudeAPIKeyFastModeForceOffIgnoresCapabilityAndCredentialType uses
// force-off with no price resolver on Anthropic API Key, OAuth, and
// setup-token targets. Every tested type loses Fast signaling while an
// unrelated beta remains.
func TestClaudeAPIKeyFastModeForceOffIgnoresCapabilityAndCredentialType(t *testing.T) {
	ctx := fastModeTestContext(apikey.APIKeyFastModePolicyForceOff, "claude-opus-4-8")

	for _, providerType := range []string{capability.ProviderTypeAPIKey, capability.ProviderTypeOAuth, capability.ProviderTypeSetupToken} {
		t.Run(providerType, func(t *testing.T) {
			headers := http.Header{}
			claude.SetHeaderRaw(headers, "anthropic-beta", claude.BetaFastMode+",context-management-2025-06-27")
			body, updatedHeaders, err := gatewayprovider.
				ApplyAnthropicFastMode(
					ctx, nil,

					&gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformAnthropic, Type: providerType}},
					"claude-opus-4-8",
					[]byte(`{"model":"claude-opus-4-8","speed":"fast"}`),
					headers,
				)
			require.NoError(t, err)
			require.False(t, gjson.GetBytes(body, "speed").Exists())
			require.False(t, claude.ContainsBetaToken(claude.GetHeaderRaw(updatedHeaders, "anthropic-beta"), claude.BetaFastMode))
			require.True(t, claude.ContainsBetaToken(claude.GetHeaderRaw(updatedHeaders, "anthropic-beta"), "context-management-2025-06-27"))
		})
	}
}

// TestAPIKeyFastModeIgnoresUnsupportedProviderAdapters uses Key force-on for a
// Grok target in OpenAI tier policy and a Bedrock target in Anthropic policy.
// Neither gains the unsupported forced wire fields.
func TestAPIKeyFastModeIgnoresUnsupportedProviderAdapters(t *testing.T) {
	openAISvc := newFastPolicyContract(t, tierpolicy.Default())
	openAISvc.Prices = fastModeTestResolver()
	ctx := fastModeTestContext(apikey.APIKeyFastModePolicyForceOn, "gpt-5.5")
	body, err := tierpolicy.ApplyBody([]byte(`{"model":"gpt-5.5"}`), openAISvc.Input(ctx, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformGrok, Type: capability.ProviderTypeAPIKey}}, "gpt-5.5"))
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(body, "service_tier").Exists())

	claudePrices := fastModeTestResolver()
	body, headers, err := gatewayprovider.
		ApplyAnthropicFastMode(ctx, claudePrices, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeBedrock}}, "claude-opus-4-8", []byte(`{"model":"claude-opus-4-8"}`), http.Header{})
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(body, "speed").Exists())
	require.Empty(t, claude.GetHeaderRaw(headers, "anthropic-beta"))
}
