package provider

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/creative"
	gemininative "github.com/TokenFlux/TokenRouter/internal/upstream/gemini"
)

// TestParseCreativeGeminiImageOutputs parses inlineData from a Gemini generateContent response.
func TestParseCreativeGeminiImageOutputs(t *testing.T) {
	img := base64.StdEncoding.EncodeToString([]byte("gemini-image-bytes"))
	body := []byte(fmt.Sprintf(`{
		"candidates": [{"content": {"parts": [
			{"text": "说明"},
			{"inlineData": {"mimeType": "image/png", "data": "%s"}},
			{"inlineData": {"mime_type": "image/webp", "data": "%s"}}
		]}}]
	}`, img, base64.StdEncoding.EncodeToString([]byte("webp-bytes"))))

	outputs, err := parseCreativeGeminiImageOutputs(body)
	require.NoError(t, err)
	require.Len(t, outputs, 1)
	require.Equal(t, []byte("webp-bytes"), outputs[0].Bytes)
	require.Equal(t, "image/webp", outputs[0].Mime)

	// Missing candidate content produces a retryable error.
	_, err = parseCreativeGeminiImageOutputs([]byte(`{"candidates":[]}`))
	require.Error(t, err)
	var upstreamErr *creative.CreativeUpstreamError
	require.True(t, errors.As(err, &upstreamErr))
	require.True(t, upstreamErr.Retryable)
}

// TestBuildCreativeGeminiRequest checks edit text, reference images, and generation settings.
func TestBuildCreativeGeminiRequest(t *testing.T) {
	run := creative.CreativeRun{Operation: creative.CreativeOperationEdit, ImageSize: "2K", AspectRatio: "16:9"}
	payload := creative.CreativeRunPayload{
		Prompt:        "重绘",
		ThinkingLevel: "high",
		Sources:       []creative.CreativeInputImage{{Bytes: []byte("src"), Mime: "image/jpeg"}},
	}
	request := BuildCreativeGeminiRequest(run, payload, "gemini-3.1-flash-image")
	require.Len(t, request.Contents, 1)
	parts := request.Contents[0].Parts
	require.Len(t, parts, 2)
	require.Equal(t, "重绘", parts[0].Text)
	require.Equal(t, "image/jpeg", parts[1].InlineData.MimeType)
	require.Equal(t, []string{"TEXT", "IMAGE"}, request.GenerationConfig.ResponseModalities)
	require.NotNil(t, request.GenerationConfig.ImageConfig)
	require.Equal(t, "2K", request.GenerationConfig.ImageConfig.ImageSize)
	require.Equal(t, "16:9", request.GenerationConfig.ImageConfig.AspectRatio)
	require.NotNil(t, request.GenerationConfig.ThinkingConfig)
	require.Equal(t, "high", request.GenerationConfig.ThinkingConfig.ThinkingLevel)
	require.False(t, request.GenerationConfig.ThinkingConfig.IncludeThoughts)

	// Serialized fields retain the upstream request nesting.
	body, err := json.Marshal(request)
	require.NoError(t, err)
	require.JSONEq(t, `{"contents":[{"parts":[{"text":"重绘"},{"inlineData":{"mimeType":"image/jpeg","data":"c3Jj"}}]}],"generationConfig":{"responseModalities":["TEXT","IMAGE"],"imageConfig":{"imageSize":"2K","aspectRatio":"16:9"},"thinkingConfig":{"thinkingLevel":"high","includeThoughts":false}}}`, string(body))
}

// TestParseCreativeGeminiImageOutputsUsesFinalImagePart checks selection of the last image part.
func TestParseCreativeGeminiImageOutputsUsesFinalImagePart(t *testing.T) {
	thought := base64.StdEncoding.EncodeToString([]byte("thought-image"))
	final := base64.StdEncoding.EncodeToString([]byte("final-image"))
	body := fmt.Sprintf(`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":%q}},{"inlineData":{"mimeType":"image/png","data":%q}}]}}]}`, thought, final)

	outputs, err := parseCreativeGeminiImageOutputs([]byte(body))
	require.NoError(t, err)
	require.Len(t, outputs, 1)
	require.Equal(t, []byte("final-image"), outputs[0].Bytes)
}

// parseCreativeGeminiImageOutputs converts upstream Gemini parser errors to creative errors.
func parseCreativeGeminiImageOutputs(body []byte) ([]creative.CreativeOutput, error) {
	return gemininative.ParseImageOutputs(body, func(status int, message string) error { return creative.CreativeHTTPStatusError(status, message) })
}
