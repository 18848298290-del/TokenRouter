package moderation

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	upstreamcore "github.com/TokenFlux/TokenRouter/internal/upstream"
)

// Image extraction keeps references available for moderation, while these
// scenarios build logs from aggregate text alone. Anthropic inline data and
// prepared OpenAI image/mask uploads are checked separately from the text-only
// excerpt assertions; media database retention is outside these fixtures.

// TestExtractContentModerationInput_AnthropicImageSourceOnlyParticipatesInMemory
// extracts an inline Anthropic image and text from the latest turn. It checks
// the image reference in memory and builds a text-only log whose excerpt
// contains the text rather than base64 data.
func TestExtractContentModerationInput_AnthropicImageSourceOnlyParticipatesInMemory(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role":"user","content":"old"},
			{"role":"assistant","content":"ok"},
			{"role":"user","content":[
				{"type":"text","text":"检查这张图"},
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}
			]}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolAnthropicMessages, body)
	require.Equal(t, "检查这张图", input.Text)
	require.Equal(t, []string{"data:image/png;base64,aGVsbG8="}, input.Images)

	log := (&ContentModerationService{runtime: testModerationRuntime()}).buildTextLogForTest(ContentModerationCheckInput{}, defaultContentModerationConfig(), ContentModerationActionAllow, false, "", 0, nil, input.Text, nil, nil, "")
	require.Equal(t, "检查这张图", log.InputExcerpt)
	require.NotContains(t, log.InputExcerpt, "aGVsbG8=")
}

// TestOpenAIImagesRequestModerationBody_MultipartEditIncludesUploadsInMemory
// converts prepared image and mask upload bytes into moderation references. It
// checks both encoded references and a separately built text-only log excerpt,
// without exercising media persistence.
func TestOpenAIImagesRequestModerationBody_MultipartEditIncludesUploadsInMemory(t *testing.T) {
	parsed := &media.ImageRequest{
		Endpoint: upstreamcore.OpenAIImagesEditsEndpoint,
		Prompt:   "replace background",
		Uploads: []upstreamcore.ImageUpload{{
			FieldName:   "image",
			FileName:    "source.png",
			ContentType: "image/png",
			Data:        []byte("fake-image-bytes"),
		}},
		MaskUpload: &upstreamcore.ImageUpload{
			FieldName:   "mask",
			FileName:    "mask.png",
			ContentType: "image/png",
			Data:        []byte("fake-mask-bytes"),
		},
	}

	input := ExtractContentModerationInput(ContentModerationProtocolOpenAIImages, parsed.ModerationBody())

	require.Equal(t, "replace background", input.Text)
	require.Equal(t, []string{
		"data:image/png;base64,ZmFrZS1pbWFnZS1ieXRlcw==",
		"data:image/png;base64,ZmFrZS1tYXNrLWJ5dGVz",
	}, input.Images)

	log := (&ContentModerationService{}).buildTextLogForTest(ContentModerationCheckInput{}, defaultContentModerationConfig(), ContentModerationActionAllow, false, "", 0, nil, input.Text, nil, nil, "")
	require.Equal(t, "replace background", log.InputExcerpt)
	require.NotContains(t, log.InputExcerpt, "ZmFrZS")
}
