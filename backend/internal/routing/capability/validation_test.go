package capability

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
)

// Protocol fallback snapshots distinguish an absent source from a source
// configured with no conversion targets. The fixture mutates a cloned target
// list and verifies its source stays unchanged, then checks preserved empty
// entries and rejection of invalid policy shapes.

// TestMixedGroupFallbackSnapshotOwnsLists changes a cloned conversion list
// while retaining an explicit empty entry. The input must stay unchanged, and
// validation must accept it while rejecting unknown sources and duplicate
// targets.
func TestMixedGroupFallbackSnapshotOwnsLists(t *testing.T) {
	input := map[ProtocolID][]ProtocolID{ProtocolOpenAIChatCompletions: {ProtocolAnthropicMessages}, ProtocolOpenAIResponses: {}}
	copy := protocol.CloneFallbacks(input)
	copy[ProtocolOpenAIChatCompletions][0] = ProtocolGeminiGenerateContent
	require.Equal(t, ProtocolAnthropicMessages, input[ProtocolOpenAIChatCompletions][0])
	_, exists := copy[ProtocolOpenAIResponses]
	require.True(t, exists)
	require.Empty(t, copy[ProtocolOpenAIResponses])
	require.NoError(t, ValidateProtocolFallbacks("", input))
	require.Error(t, ValidateProtocolFallbacks("", map[ProtocolID][]ProtocolID{"unknown": {}}))
	require.Error(t, ValidateProtocolFallbacks("", map[ProtocolID][]ProtocolID{ProtocolOpenAIChatCompletions: {ProtocolAnthropicMessages, ProtocolAnthropicMessages}}))
}
