package capability

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A mixed group exposes the same client protocol choices as a single-platform
// group. These fixtures check catalogue ordering, initial text entry defaults,
// copied selection storage, and validation of explicit empty selections before
// provider-specific routing is considered.

// TestGroupClientProtocolMatrix checks the public catalogue and three default
// text entries. It also mutates a returned selection to verify later reads
// retain their original catalogue order.
func TestGroupClientProtocolMatrix(t *testing.T) {
	supported := SupportedGroupClientProtocols("")
	require.Len(t, supported, 22)
	require.Contains(t, supported, ProtocolImageBatches)
	require.Contains(t, supported, ProtocolVoiceRealtime)
	require.NotContains(t, supported, ProtocolQoderChat)
	require.Equal(t, []ProtocolID{ProtocolAnthropicMessages, ProtocolOpenAIResponses, ProtocolOpenAIChatCompletions}, DefaultGroupClientProtocols(""))
	require.Empty(t, DefaultProtocolFallbacks(""))
	supported[0] = ProtocolLive
	require.Equal(t, ProtocolAnthropicMessages, SupportedGroupClientProtocols("")[0])
}

// TestValidateGroupClientProtocols supplies reordered, mixed-platform,
// duplicate, unknown, and empty selections. Validation must restore catalogue
// order, allow public protocols across platforms, and retain an allocated empty
// result.
func TestValidateGroupClientProtocols(t *testing.T) {
	validated, err := ValidateGroupClientProtocols(PlatformOpenAI, []ProtocolID{
		ProtocolOpenAIChatCompletions,
		ProtocolAnthropicMessages,
		ProtocolOpenAIResponses,
	})
	require.NoError(t, err)
	require.Equal(t, []ProtocolID{
		ProtocolAnthropicMessages,
		ProtocolOpenAIResponses,
		ProtocolOpenAIChatCompletions,
	}, validated)

	emptyOpenAI, err := ValidateGroupClientProtocols(PlatformOpenAI, []ProtocolID{})
	require.NoError(t, err)
	require.NotNil(t, emptyOpenAI)
	_, err = ValidateGroupClientProtocols(PlatformAnthropic, []ProtocolID{ProtocolAnthropicMessages, ProtocolGeminiGenerateContent})
	require.NoError(t, err)
	_, err = ValidateGroupClientProtocols(PlatformQoder, []ProtocolID{ProtocolAnthropicMessages, ProtocolAnthropicMessages})
	require.ErrorContains(t, err, "duplicated")
	_, err = ValidateGroupClientProtocols(PlatformQoder, []ProtocolID{"unknown"})
	require.ErrorContains(t, err, "unknown protocol")

	empty, err := ValidateGroupClientProtocols(PlatformQoder, []ProtocolID{})
	require.NoError(t, err)
	require.NotNil(t, empty)
}
