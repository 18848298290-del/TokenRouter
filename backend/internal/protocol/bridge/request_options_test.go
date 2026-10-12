package bridge

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRequestOptionsAreIndependentOfModelName supplies an opaque model with
// temperature and max effort, then varies conversion options. Both request
// converters must follow sampling/max flags while the original temperature
// value remains unchanged after conversion.
func TestRequestOptionsAreIndependentOfModelName(t *testing.T) {
	temperature := 0.4
	request := &AnthropicRequest{
		Model:        "opaque-model-identity",
		Temperature:  &temperature,
		OutputConfig: &AnthropicOutputConfig{Effort: "max"},
	}
	for _, options := range []RequestOptions{
		{DropSampling: true, SupportsMaxEffort: true},
		{DropSampling: false, SupportsMaxEffort: false},
	} {
		responses, err := AnthropicToResponses(request, options)
		require.NoError(t, err)
		chat, err := AnthropicToChatCompletionsRequest(request, options)
		require.NoError(t, err)
		if options.DropSampling {
			require.Nil(t, responses.Temperature)
			require.Nil(t, chat.Temperature)
		} else {
			require.Equal(t, temperature, *responses.Temperature)
			require.Equal(t, temperature, *chat.Temperature)
		}
		effort := "xhigh"
		if options.SupportsMaxEffort {
			effort = "max"
		}
		require.Equal(t, effort, responses.Reasoning.Effort)
		require.Equal(t, effort, chat.ReasoningEffort)
	}
	require.Equal(t, temperature, *request.Temperature)
}
