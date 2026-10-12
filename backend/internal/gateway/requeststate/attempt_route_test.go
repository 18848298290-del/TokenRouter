package requeststate

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// TestCapturedCandidatePlanDoesNotResolveMissingOrChangedCandidate reads an
// absent plan and captures a provided candidate before resetting its owner. The
// captured value must retain its group and provider identifiers after later
// state changes.
func TestCapturedCandidatePlanDoesNotResolveMissingOrChangedCandidate(t *testing.T) {
	var attempt AttemptRoute
	_, ok := attempt.Candidate()
	require.False(t, ok)
	attempt = AttemptRoute{candidate: routing.CandidatePlan{ProviderID: 3, GroupID: 7}, planned: true}
	captured, ok := attempt.Candidate()
	require.True(t, ok)
	attempt.candidate.GroupID = 8
	attempt = AttemptRoute{}
	_, ok = attempt.Candidate()
	require.False(t, ok)
	require.Equal(t, int64(7), captured.GroupID)
	require.Equal(t, int64(3), captured.ProviderID)
}

// TestAttemptRouteRechecksCapabilitiesAndReadsCurrentMapping refreshes provider
// capabilities while supplying different model mappings to one captured
// attempt. Resolution must use each current mapping, reject the changed
// capability, and retain a previous attempt when request routing state is
// absent.
func TestAttemptRouteRechecksCapabilitiesAndReadsCurrentMapping(t *testing.T) {
	group := &routing.Group{ID: 7, ProtocolFallbacks: map[protocol.ProtocolID][]protocol.ProtocolID{protocol.ProtocolAnthropicMessages: {protocol.ProtocolOpenAIResponses}}}
	ctx := WithClientProtocol(WithGroup(context.Background(), group), protocol.ProtocolAnthropicMessages)
	ctx = WithRoutePlan(ctx, routing.Plan(routing.PlanInput{Group: group, ClientProtocol: protocol.ProtocolAnthropicMessages}))
	state := RoutingStateFromContext(ctx)
	record := &provider.Record{ID: 1, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Credentials: map[string]any{provider.UpstreamProtocolsKey: []string{"openai_responses"}}}
	attempt, resolved, err := state.ResolveAttempt(record.RoutingSnapshot(), AttemptRoute{})
	require.NoError(t, err)
	require.True(t, resolved)
	require.Equal(t, protocol.ProtocolOpenAIResponses, attempt.Protocol())
	for _, target := range []string{"first", "latest"} {
		model, matched := attempt.ResolveModel(1, record.Platform, map[string]string{"group-model": target}, "group-model")
		require.True(t, matched)
		require.Equal(t, target, model)
	}
	record.Credentials[provider.UpstreamProtocolsKey] = []string{"openai_chat_completions"}
	_, _, err = state.ResolveAttempt(record.RoutingSnapshot(), attempt)
	require.Error(t, err)
	retained, resolved, err := (RoutingState{}).ResolveAttempt(record.RoutingSnapshot(), attempt)
	require.NoError(t, err)
	require.False(t, resolved)
	require.Equal(t, attempt, retained)
}
