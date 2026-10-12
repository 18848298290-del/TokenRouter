package openai

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCopyOpenAIUsageFromResponsesUsageTrustsCanonicalCacheCreationValue
// supplies a zero canonical cache-write count beside nonzero nested detail.
// Copying must retain that canonical zero and the supplied input/cache-read
// counts, including the nested cached-token observation.
func TestCopyOpenAIUsageFromResponsesUsageTrustsCanonicalCacheCreationValue(t *testing.T) {
	usage := &ResponsesUsage{
		InputTokens:              20,
		OutputTokens:             2,
		CacheCreationInputTokens: 0,
		InputTokensDetails: &ResponsesInputTokensDetails{
			CachedTokens:     3,
			CacheWriteTokens: 19,
		},
	}

	got := CopyForwardUsage(usage)

	require.Equal(t, 20, got.InputTokens)
	require.Equal(t, 3, got.CacheReadInputTokens)
	require.Zero(t, got.CacheCreationInputTokens)
}
