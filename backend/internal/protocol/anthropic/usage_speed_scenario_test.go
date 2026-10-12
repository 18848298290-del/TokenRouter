package anthropic

// Speed scenarios cover SSE parsing in usage_patch.go and response parsing in usage_events.go.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
)

// TestClaudeUsageSpeedParsing reads processing speed from an initial SSE event
// and a buffered Messages response. Both parsing paths must retain the upstream
// speed label for subsequent usage processing.
func TestClaudeUsageSpeedParsing(t *testing.T) {
	usage := &protocol.TokenUsage{}
	ParseSSEUsage(`{"type":"message_start","message":{"usage":{"input_tokens":10,"speed":"fast"}}}`, usage)
	require.Equal(t, "fast", usage.Speed)

	parsed := ParseClaudeUsageFromResponseBody([]byte(`{"usage":{"input_tokens":10,"output_tokens":2,"speed":"standard"}}`))
	require.Equal(t, "standard", parsed.Speed)
}
