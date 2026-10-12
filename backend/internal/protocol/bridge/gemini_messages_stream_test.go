package bridge

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNativeGeminiMessagesIteratorKeepsUsageUpdateAfterOutput verifies that stopping iteration before all output skips the usage update.
func TestNativeGeminiMessagesIteratorKeepsUsageUpdateAfterOutput(t *testing.T) {
	state := NewNativeGeminiMessagesStream(NativeGeminiRuntime{})
	response := map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": "hello"}}}}}}
	raw := []byte(`{"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":3}}`)
	for event := range state.Process(response, raw) {
		if event.Name == "content_block_delta" {
			break
		}
	}
	require.Zero(t, state.Usage().InputTokens)
}
