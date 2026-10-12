package routing_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// TestGroupClientProtocolsDoNotRecoverLegacyPolicy checks that an empty set disables all entries regardless of
// legacy switches.
func TestGroupClientProtocolsDoNotRecoverLegacyPolicy(t *testing.T) {
	group := &routing.Group{
		AllowMessagesDispatch: true,
	}

	require.NotNil(t, group.EffectiveAllowedProtocols())
	require.Empty(t, group.EffectiveAllowedProtocols())
	require.False(t, group.AllowsClientProtocol(protocol.ProtocolAnthropicMessages))
}

// TestGroupProtocolMembershipAndSnapshotIsolation checks direct membership and independent response snapshots.
func TestGroupProtocolMembershipAndSnapshotIsolation(t *testing.T) {
	var missing *routing.Group
	require.False(t, missing.AllowsClientProtocol(protocol.ProtocolOpenAIResponses))
	group := &routing.Group{AllowedProtocols: []protocol.ProtocolID{protocol.ProtocolOpenAIResponses}}
	require.True(t, group.AllowsClientProtocol(protocol.ProtocolOpenAIResponses))
	require.False(t, group.AllowsClientProtocol(protocol.ProtocolOpenAIChatCompletions))
	copy := group.EffectiveAllowedProtocols()
	copy[0] = protocol.ProtocolOpenAIChatCompletions
	require.True(t, group.AllowsClientProtocol(protocol.ProtocolOpenAIResponses))
	require.Zero(t, testing.AllocsPerRun(100, func() {
		if !group.AllowsClientProtocol(protocol.ProtocolOpenAIResponses) {
			panic("unexpected protocol membership")
		}
	}))
}
