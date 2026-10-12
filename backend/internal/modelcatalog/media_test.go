package modelcatalog

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMediaForModelRegisteredParameters(t *testing.T) {
	for _, tc := range []struct {
		platform, model string
		sizes           []string
		references      int
	}{
		{"openai", "gpt-image-1", []string{"1K", "2K"}, 16},
		{"openai", "gpt-image-1-mini", []string{"1K", "2K"}, 16},
		{"openai", "gpt-image-1.5", []string{"1K", "2K"}, 16},
		{"openai", "gpt-image-2", []string{"1K", "2K", "4K"}, 16},
		{"grok", "grok-imagine-image", []string{"1K", "2K"}, 3},
		{"grok", "grok-imagine-image-quality", []string{"1K", "2K"}, 3},
		{"grok", "grok-imagine-image-2.0", []string{"1K", "2K"}, 3},
		{"gemini", "gemini-2.5-flash-image", []string{"1K"}, 3},
		{"gemini", "gemini-3.1-flash-lite-image", []string{"1K"}, 14},
		{"gemini", "gemini-3.1-flash-image", []string{"512", "1K", "2K", "4K"}, 14},
		{"gemini", "gemini-3-pro-image", []string{"1K", "2K", "4K"}, 14},
		{"gemini", "gemini-3-pro-image-preview", []string{"1K", "2K", "4K"}, 14},
		{"gemini", "gemini-2.0-flash-exp-image-generation", []string{"1K"}, 1},
	} {
		t.Run(tc.model, func(t *testing.T) {
			media := MediaForModel(tc.platform, tc.model)
			require.NotNil(t, media)
			require.Equal(t, tc.sizes, media.ImageSizes)
			require.Equal(t, tc.references, *media.MaxReferenceImages)
		})
	}
	require.Equal(t, []string{"low", "medium"}, MediaForModel("grok", "grok-imagine-image-2.0").Qualities)
	require.Equal(t, valuePointer(false), MediaForModel("openai", "gpt-image-2").InputFidelity)
	require.Equal(t, []string{"generate", "edit", "extend"}, MediaForModel("grok", "grok-imagine-video-1.5").VideoOperations)
	require.Nil(t, MediaForModel("gemini", "gemini-3-pro-image-future"))
	require.Nil(t, MediaForModel("grok", "grok-imagine-image-future"))
}
