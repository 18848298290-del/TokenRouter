package googleforward

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
)

// TestIsImageGenerationModel_GeminiProImage supplies Pro image identifiers with
// suffix and models prefix variants. The image-model classifier accepts each
// exercised spelling for completion metadata inference.
func TestIsImageGenerationModel_GeminiProImage(t *testing.T) {
	require.True(t, antigravity.IsImageGenerationModel("gemini-3-pro-image"))
	require.True(t, antigravity.IsImageGenerationModel("gemini-3-pro-image-preview"))
	require.True(t, antigravity.IsImageGenerationModel("models/gemini-3-pro-image"))
}

// TestIsImageGenerationModel_GeminiFlashImage supplies Flash image identifiers
// with and without the preview suffix. The image-model classifier accepts both
// exercised spellings used by completion metadata inference.
func TestIsImageGenerationModel_GeminiFlashImage(t *testing.T) {
	require.True(t, antigravity.IsImageGenerationModel("gemini-2.5-flash-image"))
	require.True(t, antigravity.IsImageGenerationModel("gemini-2.5-flash-image-preview"))
}

// TestIsImageGenerationModel_RegularModel compares text models and custom
// identifiers embedding an image-model substring. Every exercised input is
// rejected by the image-name classifier used for completion inference.
func TestIsImageGenerationModel_RegularModel(t *testing.T) {
	require.False(t, antigravity.IsImageGenerationModel("claude-3-opus"))
	require.False(t, antigravity.IsImageGenerationModel("claude-sonnet-4-20250514"))
	require.False(t, antigravity.IsImageGenerationModel("gpt-4o"))
	require.False(t, antigravity.IsImageGenerationModel("gemini-2.5-pro")) // Text model.
	require.False(t, antigravity.IsImageGenerationModel("gemini-2.5-flash"))
	// A matching substring inside a custom identifier does not identify an
	// image model.
	require.False(t, antigravity.IsImageGenerationModel("my-gemini-3-pro-image-test"))
	require.False(t, antigravity.IsImageGenerationModel("custom-gemini-2.5-flash-image-wrapper"))
}

// TestIsImageGenerationModel_CaseInsensitive supplies uppercase and mixed-case
// Pro and Flash image identifiers. The classifier recognizes each spelling
// after the exercised case normalization of the model name.
func TestIsImageGenerationModel_CaseInsensitive(t *testing.T) {
	require.True(t, antigravity.IsImageGenerationModel("GEMINI-3-PRO-IMAGE"))
	require.True(t, antigravity.IsImageGenerationModel("Gemini-3-Pro-Image"))
	require.True(t, antigravity.IsImageGenerationModel("GEMINI-2.5-FLASH-IMAGE"))
}
