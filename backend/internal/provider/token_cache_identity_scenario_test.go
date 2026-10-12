package provider

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// OpenAI and Claude token sources can receive the same provider ID while
// sharing one cache adapter. This scenario spans openai_token.go and
// claude_token.go and verifies that their platform prefixes keep those
// provider-ID entries distinct.

// TestCacheKeyUniqueness gives the OpenAI and Claude key builders an identical
// provider ID. The assertions require different values and the presence of
// each platform's label, protecting distinct identities for these two token families.
func TestCacheKeyUniqueness(t *testing.T) {
	// Both builders receive one record so that a provider-ID difference cannot
	// explain the distinct cache identities being asserted here.
	provider := &Record{ID: 123}

	openaiKey := OpenAITokenCacheKey(provider)
	claudeKey := ClaudeTokenCacheKey(provider)

	require.NotEqual(t, openaiKey, claudeKey, "OpenAI and Claude cache keys should be different")
	require.Contains(t, openaiKey, "openai:")
	require.Contains(t, claudeKey, "claude:")
}
