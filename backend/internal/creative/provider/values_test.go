package provider

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// OpenAI request dimensions come from tier and orientation tables rather than
// arbitrary ratio arithmetic. The fixture lists square, landscape, portrait,
// and four-K dimensions so changes to those explicit request mappings are
// visible as exact string mismatches.

// TestCreativeOpenAIImageSize compares the listed one-K, two-K, and four-K
// size/ratio pairs. Each assertion requires the exact pixel-dimension string
// selected for that case by the request adapter.
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
