package openai

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEnsureOpenAIRemoteCompactionV2BetaFeature supplies absent, existing,
// other-token, and multiline capability headers. It checks exact-token
// retention and addition of the V2 token while preserving other declared
// capabilities.
func TestEnsureOpenAIRemoteCompactionV2BetaFeature(t *testing.T) {
	t.Run("absent_sets_feature", func(t *testing.T) {
		h := http.Header{}
		EnsureRemoteCompactionV2Header(h)
		require.Equal(t, "remote_compaction_v2", h.Get("x-codex-beta-features"))
	})

	t.Run("present_unchanged", func(t *testing.T) {
		h := http.Header{}
		h.Set("x-codex-beta-features", "responses_websockets_v2, remote_compaction_v2")
		EnsureRemoteCompactionV2Header(h)
		require.Equal(t, "responses_websockets_v2, remote_compaction_v2", h.Get("x-codex-beta-features"))
	})

	t.Run("other_tokens_merged", func(t *testing.T) {
		h := http.Header{}
		h.Set("x-codex-beta-features", "responses_websockets_v2")
		EnsureRemoteCompactionV2Header(h)
		require.Equal(t, "responses_websockets_v2,remote_compaction_v2", h.Get("x-codex-beta-features"))
	})

	t.Run("multi_line_values_merged_single_line", func(t *testing.T) {
		h := http.Header{}
		h.Add("x-codex-beta-features", "feature_a")
		h.Add("x-codex-beta-features", "feature_b")
		EnsureRemoteCompactionV2Header(h)
		require.Equal(t, []string{"feature_a,feature_b,remote_compaction_v2"}, h.Values("x-codex-beta-features"))
	})
}

// TestAppendOpenAIResponsesRequestPathSuffixRefusesUnsafeSuffix supplies
// traversal, query-bearing, and compact suffixes to one Responses base URL.
// Unsafe values must retain the base while compact appends successfully.
func TestAppendOpenAIResponsesRequestPathSuffixRefusesUnsafeSuffix(t *testing.T) {
	// Unsafe suffixes leave the base URL unchanged.
	require.Equal(t, "https://chatgpt.com/backend-api/codex/responses", AppendResponsesPathSuffix("https://chatgpt.com/backend-api/codex/responses", "/../../x"))
	require.Equal(t, "https://chatgpt.com/backend-api/codex/responses", AppendResponsesPathSuffix("https://chatgpt.com/backend-api/codex/responses", "/?a=b"))
	require.Equal(t, "https://chatgpt.com/backend-api/codex/responses"+"/compact", AppendResponsesPathSuffix("https://chatgpt.com/backend-api/codex/responses", "/compact"))
}
