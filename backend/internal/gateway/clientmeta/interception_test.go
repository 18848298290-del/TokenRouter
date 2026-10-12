package clientmeta

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDetectInterceptType_SuggestionModeUnaffected supplies a final user
// message beginning with the suggestion marker. It remains locally classified
// as suggestion mode when the request is not marked as a Claude client.
func TestDetectInterceptType_SuggestionModeUnaffected(t *testing.T) {
	body := []byte(`{
		"messages":[{
			"role":"user",
			"content":[{"type":"text","text":"[SUGGESTION MODE:foo]"}]
		}],
		"system":[]
	}`)

	got := DetectInterceptType(body, "claude-sonnet-4-5", 256, false)
	require.Equal(t, InterceptTypeSuggestionMode, got)
}

// TestDetectInterceptType_MaxTokensOneHaikuRequiresClaudeCodeClient supplies
// the same single-token Haiku payload with both client classifications. Local
// probe interception requires the validated Claude-client flag, while the model
// and token predicate recognizes the probe itself.
func TestDetectInterceptType_MaxTokensOneHaikuRequiresClaudeCodeClient(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)

	notClaudeCode := DetectInterceptType(body, "claude-haiku-4-5", 1, false)
	require.Equal(t, InterceptTypeNone, notClaudeCode)

	isClaudeCode := DetectInterceptType(body, "claude-haiku-4-5", 1, true)
	require.Equal(t, InterceptTypeMaxTokensOneHaiku, isClaudeCode)

	require.True(t, IsHaikuProbe("claude-haiku-4-5", 1), "流式 haiku 探测也应被识别")
}
