package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/creative"
)

// creativeImageResponseBody records closure to check that generation releases its connection before download.
type creativeImageResponseBody struct {
	io.Reader
	closed bool
}

func TestParseCreativeOpenAIImageOutputs(t *testing.T) {
	img1 := base64.StdEncoding.EncodeToString([]byte("png-bytes-1"))
	img2 := base64.StdEncoding.EncodeToString([]byte("png-bytes-2"))
	body, err := json.Marshal(map[string]any{
		"created": 123,
		"data": []map[string]any{
			{"b64_json": img1},
			{"b64_json": img2},
		},
	})
	require.NoError(t, err)

	outputs, err := ParseCreativeOpenAIImageOutputs(body)
	require.NoError(t, err)
	require.Len(t, outputs, 1)
	require.Equal(t, 0, outputs[0].Index)
	require.Equal(t, []byte("png-bytes-1"), outputs[0].Bytes)
	require.Equal(t, "image/png", outputs[0].Mime)
	// Empty data produces a retryable 502 upstream error.
	_, err = ParseCreativeOpenAIImageOutputs([]byte(`{"data":[]}`))
	require.Error(t, err)
	var upstreamErr *creative.CreativeUpstreamError
	require.True(t, errors.As(err, &upstreamErr))
	require.Equal(t, 502, upstreamErr.StatusCode)
	require.True(t, upstreamErr.Retryable)
}

// TestBuildCreativeOpenAIRequestBody checks generation and edit request fields.
func TestBuildCreativeOpenAIRequestBody(t *testing.T) {
	// Generation uses JSON.
	run := creative.CreativeRun{Operation: creative.CreativeOperationGenerate, ImageSize: "1K", RequestedOutputCount: 2}
	payload := creative.CreativeRunPayload{
		Prompt:     "hello",
		Quality:    "high",
		Background: "opaque",
	}
	body, contentType, err := BuildCreativeOpenAIRequestBody(run, payload, "gpt-image-2")
	require.NoError(t, err)
	require.Equal(t, "application/json", contentType)
	var generateBody map[string]any
	require.NoError(t, json.Unmarshal(body, &generateBody))
	require.Equal(t, "gpt-image-2", generateBody["model"])
	require.NotContains(t, generateBody, "response_format")
	require.Equal(t, float64(1), generateBody["n"])
	require.Equal(t, "high", generateBody["quality"])
	require.Equal(t, "png", generateBody["output_format"])
	require.NotContains(t, generateBody, "output_compression")
	require.Equal(t, "opaque", generateBody["background"])
	// DALL-E selects Base64 with response_format.
	dalleBody, _, err := BuildCreativeOpenAIRequestBody(run, payload, "dall-e-3")
	require.NoError(t, err)
	var dalleJSON map[string]any
	require.NoError(t, json.Unmarshal(dalleBody, &dalleJSON))
	require.Equal(t, "b64_json", dalleJSON["response_format"])

	// GPT Image 2 uses 3840x2160 for landscape 4K output.
	run = creative.CreativeRun{Operation: creative.CreativeOperationGenerate, ImageSize: "4K", AspectRatio: "16:9", RequestedOutputCount: 1}
	body, contentType, err = BuildCreativeOpenAIRequestBody(run, payload, "gpt-image-2")
	require.NoError(t, err)
	require.Equal(t, "application/json", contentType)
	require.Contains(t, string(body), `"size":"3840x2160"`)

	// Inpaint uses multipart image, mask, model, and prompt fields.
	run = creative.CreativeRun{Operation: creative.CreativeOperationInpaint, ImageSize: "1K", AspectRatio: "1:1", RequestedOutputCount: 2}
	payload = creative.CreativeRunPayload{
		Prompt:     "inpaint me",
		Sources:    []creative.CreativeInputImage{{Bytes: []byte("img"), Mime: "image/png"}},
		Mask:       &creative.CreativeInputImage{Bytes: []byte("mask"), Mime: "image/png"},
		Quality:    "high",
		Background: "opaque",
	}
	body, contentType, err = BuildCreativeOpenAIRequestBody(run, payload, "gpt-image-2")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(contentType, "multipart/form-data"))
	require.Contains(t, string(body), `name="image"`)
	require.Contains(t, string(body), `name="mask"`)
	require.Contains(t, string(body), `name="model"`)
	require.Contains(t, string(body), `name="prompt"`)
	require.Contains(t, string(body), `name="size"`)
	require.Contains(t, string(body), `name="n"`)
	require.Contains(t, string(body), `name="quality"`)
	require.Contains(t, string(body), `name="output_format"`)
	require.NotContains(t, string(body), `name="output_compression"`)
	require.Contains(t, string(body), `name="background"`)
}

func (b *creativeImageResponseBody) Close() error {
	b.closed = true
	return nil
}

// TestOpenAIImageURLResults checks download retries and generation counts across models and operations.
func TestOpenAIImageURLResults(t *testing.T) {
	for _, model := range []string{"gpt-image-2.5-flare", "gpt-image-2.5-sunburst"} {
		for _, operation := range []string{"generate", "edit", "inpaint"} {
			t.Run(model+"/"+operation, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					body := &creativeImageResponseBody{Reader: strings.NewReader(`{"data":[{"url":"https://cdn.example.com/image.png?sig=test"}]}`)}
					generations, downloads := 0, 0
					target := &Target{OpenAI: &OpenAIOptions{
						Token:        func(context.Context) (string, error) { return "token", nil },
						URL:          func(endpoint string) (string, error) { return "https://relay.example.com" + endpoint, nil },
						Prepare:      func(req *http.Request) *http.Request { return req },
						AuthHeaders:  func(context.Context, string) (http.Header, error) { return http.Header{}, nil },
						ApplyHeaders: func(http.Header) {},
						Do: func(req *http.Request) (*http.Response, error) {
							generations++
							require.Equal(t, http.MethodPost, req.Method)
							return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
						},
						FetchImage: func(ctx context.Context, raw string) (string, error) {
							downloads++
							require.True(t, body.closed)
							require.Equal(t, "https://cdn.example.com/image.png?sig=test", raw)
							if downloads == 1 {
								return "", errors.New("temporary download failure")
							}
							return base64.StdEncoding.EncodeToString([]byte("image bytes")), nil
						},
					}}
					outputs, err := target.ExecuteOpenAI(context.Background(), creative.CreativeRun{Operation: operation, ImageSize: "1K"}, creative.CreativeRunPayload{Prompt: "test"}, model)
					require.NoError(t, err)
					require.Len(t, outputs, 1)
					require.Equal(t, []byte("image bytes"), outputs[0].Bytes)
					require.Equal(t, 1, generations)
					require.Equal(t, 2, downloads)
				})
			})
		}
	}
}

// TestOpenAIImageResultFailures checks error classification and filtering of sensitive download addresses.
func TestOpenAIImageResultFailures(t *testing.T) {
	for _, test := range []struct {
		name, body, code string
		cancel           bool
		wantDownloads    int
	}{
		{name: "download exhausted", body: `{"data":[{"url":"https://cdn.example.com/private?sig=secret"}]}`, code: "IMAGE_DOWNLOAD_FAILED", wantDownloads: 3},
		{name: "cancelled download", body: `{"data":[{"url":"https://cdn.example.com/private?sig=secret"}]}`, code: "IMAGE_DOWNLOAD_FAILED", cancel: true, wantDownloads: 1},
		{name: "empty output", body: `{"data":[]}`, code: "INVALID_IMAGE_RESPONSE"},
		{name: "invalid base64", body: `{"data":[{"b64_json":"%%%"}]}`, code: "INVALID_IMAGE_RESPONSE"},
		{name: "invalid json", body: `not json`, code: "INVALID_IMAGE_RESPONSE"},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				downloads := 0
				target := &Target{OpenAI: &OpenAIOptions{FetchImage: func(context.Context, string) (string, error) {
					downloads++
					if test.cancel {
						cancel()
					}
					return "", errors.New("failed https://cdn.example.com/private?sig=secret")
				}}}
				_, err := target.parseOpenAIImageOutputs(ctx, []byte(test.body))
				var resultErr *creative.CreativeUpstreamError
				require.ErrorAs(t, err, &resultErr)
				require.Equal(t, test.code, resultErr.Code)
				require.False(t, creative.IsRetryableCreativeError(err))
				require.NotContains(t, err.Error(), "secret")
				require.Equal(t, test.wantDownloads, downloads)
			})
		})
	}
}

// TestOpenAIImageBase64Preferred checks that decodable Base64 avoids a URL download.
func TestOpenAIImageBase64Preferred(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		target := &Target{OpenAI: &OpenAIOptions{FetchImage: func(context.Context, string) (string, error) {
			t.Fatal("unexpected image download")
			return "", nil
		}}}
		outputs, err := target.parseOpenAIImageOutputs(context.Background(), []byte(`{"data":[{"url":"https://cdn.example.com/image.png","b64_json":"aW1hZ2U="}]}`))
		require.NoError(t, err)
		require.Equal(t, []byte("image"), outputs[0].Bytes)
	})
}
