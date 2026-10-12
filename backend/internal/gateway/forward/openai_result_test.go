package forward

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIForwardResultSucceededForScheduling_TerminalEvents(t *testing.T) {
	tests := []struct {
		name     string
		result   *OpenAIResult
		expected bool
	}{
		{name: "nil legacy result", result: nil, expected: true},
		{name: "non websocket zero value", result: &OpenAIResult{}, expected: true},
		{name: "websocket legacy empty terminal", result: &OpenAIResult{OpenAIWSMode: true}, expected: true},
		{name: "completed", result: &OpenAIResult{OpenAIWSMode: true, UpstreamTerminalEvent: "response.completed"}, expected: true},
		{name: "local warmup", result: &OpenAIResult{LocalWarmup: true, OpenAIWSMode: true, UpstreamTerminalEvent: "response.completed"}, expected: false},
		{name: "done", result: &OpenAIResult{OpenAIWSMode: true, UpstreamTerminalEvent: "response.done"}, expected: true},
		{name: "failed", result: &OpenAIResult{OpenAIWSMode: true, UpstreamTerminalEvent: "response.failed"}, expected: false},
		{name: "incomplete", result: &OpenAIResult{OpenAIWSMode: true, UpstreamTerminalEvent: "response.incomplete"}, expected: false},
		{name: "cancelled", result: &OpenAIResult{OpenAIWSMode: true, UpstreamTerminalEvent: "response.cancelled"}, expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, tt.result.SucceededForScheduling())
		})
	}
}

// TestOpenAIResultReplayStaysOutsideJSON checks that replay setters do not expose input in result JSON.
func TestOpenAIResultReplayStaysOutsideJSON(t *testing.T) {
	result := &OpenAIResult{Model: "model-visible", LocalWarmup: true}
	result.SetWSReplayInput([]json.RawMessage{json.RawMessage(`{"prompt":"replay-private"}`)}, true)
	result.SetWSProviderFailoverReplayInput([]json.RawMessage{json.RawMessage(`{"prompt":"failover-private"}`)})
	input, present := result.WSReplayInput()
	require.True(t, present)
	require.Len(t, input, 1)
	require.Len(t, result.WSProviderFailoverReplayInput(), 1)
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	require.Contains(t, string(raw), "model-visible")
	require.NotContains(t, string(raw), "replay-private")
	require.NotContains(t, string(raw), "failover-private")
	require.NotContains(t, string(raw), "LocalWarmup")
}

func TestOpenAIForwardSucceededForScheduling(t *testing.T) {
	require.True(t, schedulingSucceeded(nil))
	require.True(t, schedulingSucceeded(&OpenAIResult{}))
	require.True(t, schedulingSucceeded(&OpenAIResult{
		OpenAIWSMode:          true,
		UpstreamTerminalEvent: "response.completed",
	}))
	require.False(t, schedulingSucceeded(&OpenAIResult{
		OpenAIWSMode:          true,
		UpstreamTerminalEvent: "response.failed",
	}))
}

// schedulingSucceeded returns the result's scheduling success classification.
func schedulingSucceeded(result *OpenAIResult) bool {
	return result.SucceededForScheduling()
}
