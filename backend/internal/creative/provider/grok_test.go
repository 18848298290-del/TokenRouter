package provider

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/creative"
)

// TestCreativeGrokOperationMatrix checks rejection of inpaint.
func TestCreativeGrokOperationMatrix(t *testing.T) {
	executor := &Target{}
	for _, operation := range []string{creative.CreativeOperationInpaint} {
		run := creative.CreativeRun{RunID: "crun_x", Operation: operation, RequestedOutputCount: 1}
		payload := creative.CreativeRunPayload{Prompt: "p"}
		_, err := executor.ExecuteGrok(context.Background(), run, payload, "grok-imagine")
		require.Error(t, err)
		require.False(t, creative.IsRetryableCreativeError(err), "grok %s 应当不可重试", operation)
	}
}

// TestBuildCreativeGrokRequest checks Grok generation request fields.
func TestBuildCreativeGrokRequest(t *testing.T) {
	run := creative.CreativeRun{ImageSize: "2K", AspectRatio: "16:9", RequestedOutputCount: 2}
	payload := creative.CreativeRunPayload{Prompt: "画猫", Quality: "low"}
	request := BuildCreativeGrokRequest(run, payload, "grok-imagine")
	require.Equal(t, "grok-imagine", request["model"])
	require.Equal(t, "画猫", request["prompt"])
	require.Equal(t, 1, request["n"])
	require.Equal(t, "b64_json", request["response_format"])
	require.Equal(t, "2k", request["resolution"])
	require.Equal(t, "16:9", request["aspect_ratio"])
	require.Equal(t, "low", request["quality"])

	// Unsupported aspect ratios are omitted; 1K maps to 1k.
	run = creative.CreativeRun{ImageSize: "1K", AspectRatio: "21:99"}
	request = BuildCreativeGrokRequest(run, payload, "grok-imagine")
	require.Equal(t, "1k", request["resolution"])
	_, ok := request["aspect_ratio"]
	require.False(t, ok)
}

// TestBuildCreativeGrokEditRequest checks single-image and multiple-image JSON fields.
func TestBuildCreativeGrokEditRequest(t *testing.T) {
	run := creative.CreativeRun{ImageSize: "2K", AspectRatio: "16:9", RequestedOutputCount: 2}
	payload := creative.CreativeRunPayload{
		Prompt:  "edit image",
		Quality: "medium",
		Sources: []creative.CreativeInputImage{
			{Bytes: []byte("first"), Mime: "image/png"},
			{Bytes: []byte("second"), Mime: "image/jpeg"},
		},
	}
	request := BuildCreativeGrokEditRequest(run, payload, "grok-imagine-image-2.0")
	require.Equal(t, "grok-imagine-image-2.0", request["model"])
	require.Equal(t, "edit image", request["prompt"])
	require.Equal(t, "b64_json", request["response_format"])
	require.Equal(t, "2k", request["resolution"])
	require.Equal(t, "16:9", request["aspect_ratio"])
	require.Equal(t, 1, request["n"])
	require.Equal(t, "medium", request["quality"])
	require.NotContains(t, request, "image")
	images, ok := request["images"].([]map[string]string)
	require.True(t, ok)
	require.Len(t, images, 2)
	require.Equal(t, "image_url", images[0]["type"])
	require.Equal(t, "data:image/png;base64,Zmlyc3Q=", images[0]["url"])
	require.Equal(t, "data:image/jpeg;base64,c2Vjb25k", images[1]["url"])

	one := BuildCreativeGrokEditRequest(run, creative.CreativeRunPayload{
		Prompt:  "edit image",
		Sources: []creative.CreativeInputImage{{Bytes: []byte("one"), Mime: "image/png"}},
	}, "grok-imagine-image-2.0")
	require.Contains(t, one, "image")
	require.NotContains(t, one, "images")
}
