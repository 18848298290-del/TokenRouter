package provider

// Token cache identities are namespaced by platform.

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCacheKeyUniqueness(t *testing.T) {
	// The same provider ID must produce different keys across platforms.
	provider := &Record{ID: 123}

	openaiKey := OpenAITokenCacheKey(provider)
	claudeKey := ClaudeTokenCacheKey(provider)

	require.NotEqual(t, openaiKey, claudeKey, "OpenAI and Claude cache keys should be different")
	require.Contains(t, openaiKey, "openai:")
	require.Contains(t, claudeKey, "claude:")
}
