package provider

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCreativeOpenAIImageSize checks OpenAI size mappings.
func TestCreativeOpenAIImageSize(t *testing.T) {
	require.Equal(t, "1024x1024", CreativeOpenAIImageSize("1K", "1:1"))
	require.Equal(t, "1536x1024", CreativeOpenAIImageSize("1K", "16:9"))
	require.Equal(t, "1024x1536", CreativeOpenAIImageSize("1K", "9:16"))
	require.Equal(t, "1536x1536", CreativeOpenAIImageSize("2K", ""))
	require.Equal(t, "2880x2880", CreativeOpenAIImageSize("4K", "1:1"))
	require.Equal(t, "3840x2160", CreativeOpenAIImageSize("4K", "16:9"))
	require.Equal(t, "2160x3840", CreativeOpenAIImageSize("4K", "9:16"))
	require.Equal(t, "3264x2448", CreativeOpenAIImageSize("4K", "4:3"))
}
