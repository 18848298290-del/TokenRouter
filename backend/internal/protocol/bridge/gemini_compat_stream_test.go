package bridge

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A consumer can abandon native compatibility iteration before all tool parts
// have been visited. The first payload's usage is captured before that
// output, so this fixture can read its counts while checking that only the
// reached tool generated an identity.

// TestNativeGeminiCompatIteratorPreservesPartialUsageAndLazyIDs stops on the
// first tool start in a two-call payload. It checks one generated ID and the
// already recorded input, output, and cached counts.
func TestNativeGeminiCompatIteratorPreservesPartialUsageAndLazyIDs(t *testing.T) {
	generated := 0
	// The serial fixture returns two hexadecimal characters per requested byte,
	// making the generator's byte-unit callback visible while counting only tool starts reached.
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
