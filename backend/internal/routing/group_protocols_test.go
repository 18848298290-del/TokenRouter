package routing_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// An empty authoritative protocol selection closes entries even when a legacy
// admission boolean is true. Returned protocol snapshots copy slice storage,
// while direct membership reads inspect the saved selection; these cases check
// nil-group handling and the one-entry allocation probe separately.

// TestGroupClientProtocolsDoNotRecoverLegacyPolicy supplies a legacy Messages
// flag with no selected protocols. It checks a non-nil empty response slice and
// denial of Messages admission.
func TestGroupClientProtocolsDoNotRecoverLegacyPolicy(t *testing.T) {
	group := &routing.Group{
		AllowMessagesDispatch: true,
	}

	require.NotNil(t, group.EffectiveAllowedProtocols())
	require.Empty(t, group.EffectiveAllowedProtocols())
	require.False(t, group.AllowsClientProtocol(protocol.ProtocolAnthropicMessages))
}

// TestGroupProtocolMembershipAndSnapshotIsolation checks nil-group denial and
// membership in a one-entry selection. It mutates the returned snapshot and
// asserts zero allocations for the repeated one-entry membership probe.
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
