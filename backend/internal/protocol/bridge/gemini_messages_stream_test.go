package bridge

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Native Messages iteration updates usage after its output and flush yields
// finish. A consumer stopping at the visible delta therefore retains earlier
// counts; this fixture checks that ordering with one text part and supplied
// prompt metadata.

// TestNativeGeminiMessagesIteratorKeepsUsageUpdateAfterOutput breaks on the
// text delta before the remaining flush and usage update. It checks that the
// supplied prompt count has not reached the state.
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
