package forward

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOpenAIForwardResultSucceededForScheduling_TerminalEvents supplies legacy,
// local-warmup, and declared WebSocket outcomes. It protects the scheduling
// success defaults, completed/done success, and rejection of unsuccessful terminal events.
func TestOpenAIForwardResultSucceededForScheduling_TerminalEvents(t *testing.T) {
	tests := []struct {
		name     string        // test outcome scenario
		result   *OpenAIResult // supplied forwarding result
		expected bool          // expected scheduling success
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

// TestOpenAIResultReplayStaysOutsideJSON stores turn and provider-failover replay
// items and retrieves their presence and lengths. JSON serialization contains
// the public model while excluding replay prompt text and the local-warmup marker.
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

// TestOpenAIForwardSucceededForScheduling invokes the delegation helper with
// nil, ordinary, completed, and failed results. The helper returns the result
// method's scheduling classification for each supplied outcome.
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

// schedulingSucceeded delegates a test result to its scheduling classifier.
// Nil and ordinary zero-value results follow that method's legacy success path,
// while a declared failed WebSocket terminal returns false.
func schedulingSucceeded(result *OpenAIResult) bool {
	return result.SucceededForScheduling()
}
