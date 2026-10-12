package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/creative"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// ExecuteOpenAI calls the Images generation or edit endpoint and reads Base64 or URL results.
func (e *Target) ExecuteOpenAI(ctx context.Context, run creative.CreativeRun, payload creative.CreativeRunPayload, upstreamModel string) ([]creative.CreativeOutput, error) {
	if e.OpenAI == nil {
		return nil, errors.New("creative openai gateway is not configured")
	}
	endpoint := upstream.OpenAIImagesGenerationsEndpoint
	if run.Operation != creative.CreativeOperationGenerate {
		endpoint = upstream.OpenAIImagesEditsEndpoint
	}
	body, contentType, err := BuildCreativeOpenAIRequestBody(run, payload, upstreamModel)
	if err != nil {
		return nil, err
	}
	token, err := e.OpenAI.Token(ctx)
	if err != nil {
		return nil, creative.CreativeHTTPStatusError(0, err.Error())
	}
	targetURL, err := e.CreativeOpenAIURL(endpoint)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req = e.OpenAI.Prepare(req)
	authHeaders, err := e.OpenAI.AuthHeaders(ctx, token)
	if err != nil {
		return nil, creative.CreativeHTTPStatusError(0, err.Error())
	}
	for key, values := range authHeaders {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	if strings.TrimSpace(contentType) != "" {
		req.Header.Set("Content-Type", contentType)
	}
	// Apply provider header overrides last so configured values take precedence over defaults.
	e.OpenAI.ApplyHeaders(req.Header)

	resp, err := e.OpenAI.Do(req)
	if err != nil {
		return nil, creative.CreativeHTTPStatusError(0, err.Error())
	}
	respBody, err := ReadCreativeUpstreamBody(resp.Body, 64<<20)
	// Release the generation connection before a download needs a slot from the same provider pool.
	_ = resp.Body.Close()
	if err != nil {
		return nil, creative.CreativeHTTPStatusError(0, err.Error())
	}
	if resp.StatusCode >= 400 {
		return nil, creative.CreativeHTTPStatusError(resp.StatusCode, upstream.ExtractErrorMessage(respBody))
	}
	return e.parseOpenAIImageOutputs(ctx, respBody)
}

// parseOpenAIImageOutputs prefers decodable Base64, then tries URLs; result errors prevent generation retries.
func (e *Target) parseOpenAIImageOutputs(ctx context.Context, body []byte) ([]creative.CreativeOutput, error) {
	if !gjson.ValidBytes(body) {
		return nil, creative.CreativeImageResultError("INVALID_IMAGE_RESPONSE", "creative platform returned invalid image response")
	}
	if outputs, err := ParseCreativeOpenAIImageOutputs(body); err == nil {
		return outputs, nil
	}
	for _, item := range gjson.GetBytes(body, "data").Array() {
		rawURL := strings.TrimSpace(item.Get("url").String())
		if rawURL == "" {
			continue
		}
		if e.OpenAI.FetchImage != nil {
			for attempt := 0; attempt < 3 && ctx.Err() == nil; attempt++ {
				encoded, err := e.OpenAI.FetchImage(ctx, rawURL)
				if err == nil {
					decoded, decodeErr := DecodeBase64Image(encoded)
					if decodeErr == nil && len(decoded.Bytes) > 0 {
						return []creative.CreativeOutput{{Index: 0, Bytes: decoded.Bytes, Mime: decoded.Mime}}, nil
					}
				}
				if attempt < 2 {
					timer := time.NewTimer(time.Second)
					select {
					case <-ctx.Done():
						timer.Stop()
					case <-timer.C:
					}
				}
			}
		}
		// Download errors may expose signed URLs or internal addresses; return a fixed public message.
		return nil, creative.CreativeImageResultError("IMAGE_DOWNLOAD_FAILED", "creative image download failed")
	}
	return nil, creative.CreativeImageResultError("INVALID_IMAGE_RESPONSE", "creative platform returned no decodable image output")
}

func (e *Target) CreativeOpenAIURL(endpoint string) (string, error) { return e.OpenAI.URL(endpoint) }

// BuildCreativeOpenAIRequestBody builds JSON for generation and multipart for
// edit/inpaint, including source images and an optional mask.
// It requests PNG output; callers validate optional parameters against model capabilities.
func BuildCreativeOpenAIRequestBody(run creative.CreativeRun, payload creative.CreativeRunPayload, upstreamModel string) ([]byte, string, error) {
	if run.Operation == creative.CreativeOperationGenerate {
		bodyMap := map[string]any{
			"model":         upstreamModel,
			"prompt":        payload.Prompt,
			"n":             1,
			"output_format": "png",
			"size":          CreativeOpenAIImageSize(run.ImageSize, run.AspectRatio),
		}
		if CreativeOpenAIUsesResponseFormat(upstreamModel) {
			bodyMap["response_format"] = "b64_json"
		}
		if quality := strings.TrimSpace(payload.Quality); quality != "" {
			bodyMap["quality"] = quality
		}
		if background := strings.TrimSpace(payload.Background); background != "" {
			bodyMap["background"] = background
		}
		body, err := json.Marshal(bodyMap)
		if err != nil {
			return nil, "", err
		}
		return body, "application/json", nil
	}

	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	for i, image := range payload.Sources {
		part, err := writer.CreateFormFile("image", fmt.Sprintf("source_%d.%s", i, CreativeFileExtension(image.Mime)))
		if err != nil {
			return nil, "", err
		}
		if _, err := part.Write(image.Bytes); err != nil {
			return nil, "", err
		}
	}
	if payload.Mask != nil {
		part, err := writer.CreateFormFile("mask", "mask."+CreativeFileExtension(payload.Mask.Mime))
		if err != nil {
			return nil, "", err
		}
		if _, err := part.Write(payload.Mask.Bytes); err != nil {
			return nil, "", err
		}
	}
	if err := writer.WriteField("model", upstreamModel); err != nil {
		return nil, "", err
	}
	if err := writer.WriteField("prompt", payload.Prompt); err != nil {
		return nil, "", err
	}
	if CreativeOpenAIUsesResponseFormat(upstreamModel) {
		if err := writer.WriteField("response_format", "b64_json"); err != nil {
			return nil, "", err
		}
	}
	if err := writer.WriteField("output_format", "png"); err != nil {
		return nil, "", err
	}
	if err := writer.WriteField("size", CreativeOpenAIImageSize(run.ImageSize, run.AspectRatio)); err != nil {
		return nil, "", err
	}
	if err := writer.WriteField("n", "1"); err != nil {
		return nil, "", err
	}
	if quality := strings.TrimSpace(payload.Quality); quality != "" {
		if err := writer.WriteField("quality", quality); err != nil {
			return nil, "", err
		}
	}
	if background := strings.TrimSpace(payload.Background); background != "" {
		if err := writer.WriteField("background", background); err != nil {
			return nil, "", err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return buffer.Bytes(), writer.FormDataContentType(), nil
}

// CreativeOpenAIUsesResponseFormat enables the legacy response_format field for DALL-E model names.
func CreativeOpenAIUsesResponseFormat(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "dall-e")
}

// ParseCreativeOpenAIImageOutputs returns the first decodable data[].b64_json image from OpenAI or Grok.
func ParseCreativeOpenAIImageOutputs(body []byte) ([]creative.CreativeOutput, error) {
	data := gjson.GetBytes(body, "data")
	if !data.IsArray() || len(data.Array()) == 0 {
		return nil, creative.CreativeHTTPStatusError(http.StatusBadGateway, "upstream returned no image output")
	}
	outputs := make([]creative.CreativeOutput, 0, len(data.Array()))
	for _, item := range data.Array() {
		b64 := strings.TrimSpace(item.Get("b64_json").String())
		if b64 == "" {
			continue
		}
		decoded, err := DecodeBase64Image(b64)
		if err != nil || len(decoded.Bytes) == 0 {
			continue
		}
		outputs = append(outputs, creative.CreativeOutput{Index: len(outputs), Bytes: decoded.Bytes, Mime: decoded.Mime})
		break
	}
	if len(outputs) == 0 {
		return nil, creative.CreativeHTTPStatusError(http.StatusBadGateway, "upstream returned no decodable image output")
	}
	return outputs, nil
}
