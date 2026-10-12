package provider

// These scenarios cover protocol selection, image policy, and independent transports across route planning and provider eligibility.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/creative"
	gatewaymedia "github.com/TokenFlux/TokenRouter/internal/gateway/media"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	protocolcore "github.com/TokenFlux/TokenRouter/internal/protocol"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

func TestProtocolImagePolicyAndBatchBinding(t *testing.T) {
	group := &routing.Group{AllowedProtocols: []protocolcore.ProtocolID{}, ResponsesImagePolicy: "enabled"}
	require.NoError(t, routing.NormalizeGroupProtocolPolicy(group, nil))
	*group = *routing.CloneGroup(group)
	require.False(t, gatewaymedia.GroupImagePermission(group != nil, group.AllowImageGeneration))
	require.True(t, routing.GroupAllowsResponsesImages(group))
	require.Equal(t, providercore.CodexImagePolicyAllow, GroupResponsesExplicitToolPolicy(group, providercore.CodexImagePolicyStrip))
	group.ResponsesImagePolicy = "block"
	require.Equal(t, providercore.CodexImagePolicyStrip, GroupResponsesExplicitToolPolicy(group, providercore.CodexImagePolicyAllow))
	for _, tc := range []struct {
		kind   string
		target protocolcore.ProtocolID
	}{{capability.ProviderTypeAPIKey, protocolcore.ProtocolGeminiBatch}, {capability.ProviderTypeServiceAccount, protocolcore.ProtocolVertexBatch}} {
		a := &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformGemini, Type: tc.kind, Credentials: map[string]any{providercore.UpstreamProtocolsKey: []protocolcore.ProtocolID{tc.target}}}}
		target, ok := ExecutionModelPolicy(a).ProtocolRoute(nil, protocolcore.ProtocolImageBatches)
		require.True(t, ok)
		require.Equal(t, tc.target, target)
	}
}

func TestProtocolAuxiliaryModelURLAndIndependentTransports(t *testing.T) {
	provider := &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformKimi, Type: capability.ProviderTypeAPIKey, Credentials: map[string]any{"api_protocol": "anthropic", "base_url": "https://relay.example/custom/anthropic", "api_key": "test"}}}
	require.NoError(t, NormalizeExecutionProtocols(provider))
	require.Equal(t, "https://relay.example/custom", ExecutionProtocolTarget(provider).GetOpenAIFormatBaseURL())
	for _, protocol := range []protocolcore.ProtocolID{protocolcore.ProtocolResponsesWebSocket, protocolcore.ProtocolResponsesCompact} {
		a := &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Credentials: map[string]any{providercore.UpstreamProtocolsKey: []protocolcore.ProtocolID{protocol}}}}
		ctx := requeststate.WithClientProtocol(context.Background(), protocol)
		require.True(t, SupportsRequestCapability(ctx, a, providercore.OpenAIEndpointCapabilityResponses))
		require.False(t, provideradapter.SupportsOpenAIEndpoint(ExecutionProtocolRecord(a), providercore.OpenAIEndpointCapabilityTextGeneration))
	}
	group := &routing.Group{AllowedProtocols: []protocolcore.ProtocolID{protocolcore.ProtocolImagesEdits}, ResponsesImagePolicy: "block"}
	require.Equal(t, []string{creative.CreativeOperationEdit, creative.CreativeOperationInpaint}, creative.OperationsForGroup(group.ResponsesImagePolicy != "" || group.ProtocolFallbacks != nil, group.AllowsClientProtocol)[creative.PlatformOpenAI])
	require.Nil(t, ResponsesPolicyGroup(requeststate.WithClientProtocol(context.Background(), protocolcore.ProtocolImagesEdits), group))
}
