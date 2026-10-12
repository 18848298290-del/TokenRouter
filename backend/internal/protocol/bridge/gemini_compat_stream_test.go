package bridge

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNativeGeminiCompatIteratorPreservesPartialUsageAndLazyIDs checks that early exit generates IDs only for yielded tools.
func TestNativeGeminiCompatIteratorPreservesPartialUsageAndLazyIDs(t *testing.T) {
	generated := 0
	runtime := NativeGeminiRuntime{RandomHex: func(size int) string { generated++; return strings.Repeat("a", size*2) }}
	state := NewNativeGeminiCompatStream(runtime)
	response := map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{
		map[string]any{"functionCall": map[string]any{"name": "first", "args": map[string]any{}}},
		map[string]any{"functionCall": map[string]any{"name": "second", "args": map[string]any{}}},
	}}}}}
	raw := []byte(`{"usageMetadata":{"promptTokenCount":12,"cachedContentTokenCount":2,"candidatesTokenCount":3}}`)
	for event := range state.Process(response, raw) {
		if event.Type == "content_block_start" {
			break
		}
	}
	require.Equal(t, 1, generated)
	require.Equal(t, 10, state.Usage().InputTokens)
	require.Equal(t, 3, state.Usage().OutputTokens)
	require.Equal(t, 2, state.Usage().CacheReadInputTokens)
}
