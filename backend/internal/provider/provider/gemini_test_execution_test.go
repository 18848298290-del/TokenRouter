package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	testassert "github.com/TokenFlux/TokenRouter/internal/testutil/assertion"
)

// Gemini image probes request output modalities through generationConfig, while
// text probes use a different payload shape. These cases exercise
// gemini_test_execution.go and check the prompt, square image setting and
// explicit test-type precedence over image markers in model names.

// TestCreateGeminiTestPayload_ImageModel builds a probe from an image-marked model.
// It checks the single prompt part, TEXT and IMAGE modalities, and square aspect
// ratio in the encoded generation configuration.
func TestCreateGeminiTestPayload_ImageModel(t *testing.T) {
	t.Parallel()

	payload := geminiTestPayload("gemini-2.5-flash-image", "draw a tiny robot", provider.ProviderTestTypeImage)

	var parsed struct {
		Contents []struct { // probe conversation messages
			Parts []struct { // message content part list
				Text string `json:"text"` // encoded user prompt
			} `json:"parts"`
		} `json:"contents"`
		GenerationConfig struct { // image generation option fields
			ResponseModalities []string `json:"responseModalities"` // requested output modality names
			ImageConfig        struct { // requested image format options
				AspectRatio string `json:"aspectRatio"` // requested image aspect ratio
			} `json:"imageConfig"`
		} `json:"generationConfig"`
	}

	require.NoError(t, json.Unmarshal(payload, &parsed))
	require.Len(t, parsed.Contents, 1)
	require.Len(t, parsed.Contents[0].Parts, 1)
	require.Equal(t, "draw a tiny robot", parsed.Contents[0].Parts[0].Text)
	require.Equal(t, []string{"TEXT", "IMAGE"}, parsed.GenerationConfig.ResponseModalities)
	require.Equal(t, "1:1", parsed.GenerationConfig.ImageConfig.AspectRatio)
}

// TestCreateGeminiTestPayload_ExplicitTypeOverridesModelName selects text for an
// image-marked name and image for a plain name. It checks the text prompt and
// payload shape, and that image modalities follow the explicit image selection.
func TestCreateGeminiTestPayload_ExplicitTypeOverridesModelName(t *testing.T) {
	t.Parallel()

	textPayload := geminiTestPayload("gemini-2.5-flash-image", "reply briefly", provider.ProviderTestTypeText)
	var textParsed map[string]any
	require.NoError(t, json.Unmarshal(textPayload, &textParsed))
	require.NotContains(t, textParsed, "generationConfig")
	content := testassert.MustType[map[string]any](testassert.MustType[[]any](textParsed["contents"])[0])
	parts := testassert.MustType[[]any](content["parts"])
	require.Equal(t, "reply briefly", testassert.MustType[map[string]any](parts[0])["text"])

	imagePayload := geminiTestPayload("gemini-2.5-flash", "draw a tiny robot", provider.ProviderTestTypeImage)
	var imageParsed map[string]any
	require.NoError(t, json.Unmarshal(imagePayload, &imageParsed))
	config := testassert.MustType[map[string]any](imageParsed["generationConfig"])
	require.Equal(t, []any{"TEXT", "IMAGE"}, config["responseModalities"])
}
