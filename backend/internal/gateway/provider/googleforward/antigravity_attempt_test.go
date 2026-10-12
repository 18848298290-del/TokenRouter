package googleforward

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
)

// TestIsImageGenerationModel_GeminiProImage covers Gemini Pro image identifiers.
func TestIsImageGenerationModel_GeminiProImage(t *testing.T) {
	require.True(t, antigravity.IsImageGenerationModel("gemini-3-pro-image"))
	require.True(t, antigravity.IsImageGenerationModel("gemini-3-pro-image-preview"))
	require.True(t, antigravity.IsImageGenerationModel("models/gemini-3-pro-image"))
}

// TestIsImageGenerationModel_GeminiFlashImage covers Gemini Flash image identifiers.
func TestIsImageGenerationModel_GeminiFlashImage(t *testing.T) {
	require.True(t, antigravity.IsImageGenerationModel("gemini-2.5-flash-image"))
	require.True(t, antigravity.IsImageGenerationModel("gemini-2.5-flash-image-preview"))
}

// TestIsImageGenerationModel_RegularModel rejects text and custom model identifiers.
func TestIsImageGenerationModel_RegularModel(t *testing.T) {
	require.False(t, antigravity.IsImageGenerationModel("claude-3-opus"))
	require.False(t, antigravity.IsImageGenerationModel("claude-sonnet-4-20250514"))
	require.False(t, antigravity.IsImageGenerationModel("gpt-4o"))
	require.False(t, antigravity.IsImageGenerationModel("gemini-2.5-pro")) // Text model.
	require.False(t, antigravity.IsImageGenerationModel("gemini-2.5-flash"))
	// A matching substring inside a custom identifier does not identify an image model.
	require.False(t, antigravity.IsImageGenerationModel("my-gemini-3-pro-image-test"))
	require.False(t, antigravity.IsImageGenerationModel("custom-gemini-2.5-flash-image-wrapper"))
}

// TestIsImageGenerationModel_CaseInsensitive checks case-insensitive matching.
func TestIsImageGenerationModel_CaseInsensitive(t *testing.T) {
	require.True(t, antigravity.IsImageGenerationModel("GEMINI-3-PRO-IMAGE"))
	require.True(t, antigravity.IsImageGenerationModel("Gemini-3-Pro-Image"))
	require.True(t, antigravity.IsImageGenerationModel("GEMINI-2.5-FLASH-IMAGE"))
}
