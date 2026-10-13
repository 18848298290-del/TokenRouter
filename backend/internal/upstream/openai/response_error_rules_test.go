package openai

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// TestIsOpenAICodexPlanGatedModelError compares detail and nested-message plan
// rejection with unrelated, empty, and different-status bodies. It checks
// supported Codex plan evidence qualifies only under the expected 400 status.
func TestIsOpenAICodexPlanGatedModelError(t *testing.T) {
	tests := []struct {
		name       string // plan rejection case description
		statusCode int    // upstream fixture HTTP status
		body       []byte // upstream plan rejection bytes
		want       bool   // expected plan rejection result
	}{
		{
			name:       "400 codex plan gated detail payload",
			statusCode: http.StatusBadRequest,
			body:       []byte(`{"detail":"The 'gpt-5.6-sol' model is not supported when using Codex with a ChatGPT provider."}`),
			want:       true,
		},
		{
			name:       "400 codex plan gated error message payload",
			statusCode: http.StatusBadRequest,
			body:       []byte(`{"error":{"message":"The 'gpt-5.4' model is not supported when using Codex with a ChatGPT provider."}}`),
			want:       true,
		},
		{
			name:       "400 unrelated invalid request does not match",
			statusCode: http.StatusBadRequest,
			body:       []byte(`{"error":{"message":"Invalid schema for response_format 'agentic_plan'"}}`),
			want:       false,
		},
		{
			name:       "404 with plan gated message does not match",
			statusCode: http.StatusNotFound,
			body:       []byte(`{"detail":"The 'gpt-5.6-sol' model is not supported when using Codex with a ChatGPT provider."}`),
			want:       false,
		},
		{
			name:       "400 empty body does not match",
			statusCode: http.StatusBadRequest,
			body:       nil,
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsCodexPlanGatedModelError(tt.statusCode, tt.body); got != tt.want {
				t.Fatalf("IsCodexPlanGatedModelError() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestExtractOpenAISSETerminalEventUsesEventField supplies an error frame whose
// JSON omits its event type. It checks the event line supplies terminal
// classification and the nested failure message remains readable.
func TestExtractOpenAISSETerminalEventUsesEventField(t *testing.T) {
	t.Parallel()

	body := "event: error\n" +
		"data: {\"error\":{\"message\":\"provider failed\"}}\n\n" +
		"data: [DONE]\n\n"
	eventType, payload, ok := protocolopenai.ExtractOpenAISSETerminalEvent(body)
	require.True(t, ok)
	require.Equal(t, "error", eventType)
	require.Equal(t, "provider failed", ExtractOpenAISSEErrorMessage(payload))
}
