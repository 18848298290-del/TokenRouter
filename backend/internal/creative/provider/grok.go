package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/creative"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// ExecuteGrok dispatches generate and edit operations to the corresponding xAI image endpoints.
func (e *Target) ExecuteGrok(ctx context.Context, run creative.CreativeRun, payload creative.CreativeRunPayload, upstreamModel string) ([]creative.CreativeOutput, error) {
	if run.Operation != creative.CreativeOperationGenerate && run.Operation != creative.CreativeOperationEdit {
		return nil, creative.CreativeNonRetryableError("grok platform does not support creative operation %s", run.Operation)
	}
	if run.Operation == creative.CreativeOperationEdit {
		if len(payload.Sources) == 0 {
			return nil, creative.CreativeNonRetryableError("grok image edit requires at least one source image")
		}
		if len(payload.Sources) > 3 {
			return nil, creative.CreativeNonRetryableError("grok image edit supports at most %d source images", 3)
		}
	}
	if e.Grok == nil {
		return nil, errors.New("creative grok gateway is not configured")
	}
	endpoint := grok.GrokMediaEndpointImagesGenerations
	if run.Operation == creative.CreativeOperationEdit {
		endpoint = grok.GrokMediaEndpointImagesEdits
	}
	targetURL, err := e.Grok.URL(endpoint)
	if err != nil {
		return nil, creative.CreativeHTTPStatusError(0, err.Error())
	}
	token, err := e.Grok.Token(ctx)
	if err != nil {
		return nil, creative.CreativeHTTPStatusError(0, err.Error())
	}
	request := BuildCreativeGrokRequest(run, payload, upstreamModel)
	if run.Operation == creative.CreativeOperationEdit {
		request = BuildCreativeGrokEditRequest(run, payload, upstreamModel)
	}
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req = e.Grok.Prepare(req)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if e.Grok.OAuth && (grok.MediaCodec{}).IsGrokCLIProxyTarget(targetURL) {
		grok.ApplyCLIHeaders(req.Header)
	}
	// Apply provider header overrides last so configured values take precedence over defaults.
	e.Grok.ApplyHeaders(req.Header)

	resp, err := e.Grok.Do(req)
	if err != nil {
		return nil, creative.CreativeHTTPStatusError(0, err.Error())
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := ReadCreativeUpstreamBody(resp.Body, 64<<20)
	if err != nil {
		return nil, creative.CreativeHTTPStatusError(0, err.Error())
	}
	if resp.StatusCode >= 400 {
		return nil, creative.CreativeHTTPStatusError(resp.StatusCode, upstream.ExtractErrorMessage(respBody))
	}
	return ParseCreativeOpenAIImageOutputs(respBody)
}

// BuildCreativeGrokRequest builds the xAI images/generations JSON body.
func BuildCreativeGrokRequest(run creative.CreativeRun, payload creative.CreativeRunPayload, upstreamModel string) map[string]any {
	request := map[string]any{
		"model":           upstreamModel,
		"prompt":          payload.Prompt,
		"n":               1,
		"response_format": "b64_json",
		"resolution":      CreativeGrokImageResolution(run.ImageSize),
	}
	if aspectRatio := CreativeGrokAspectRatio(run.AspectRatio); aspectRatio != "" {
		request["aspect_ratio"] = aspectRatio
	}
	if quality := strings.TrimSpace(payload.Quality); quality != "" {
		request["quality"] = quality
	}
	return request
}

// BuildCreativeGrokEditRequest builds the xAI images/edits JSON body.
// The edit request embeds images as data URIs in JSON, as required by the xAI endpoint.
func BuildCreativeGrokEditRequest(run creative.CreativeRun, payload creative.CreativeRunPayload, upstreamModel string) map[string]any {
	request := map[string]any{
		"model":           upstreamModel,
		"prompt":          payload.Prompt,
		"n":               1,
		"response_format": "b64_json",
		"resolution":      CreativeGrokImageResolution(run.ImageSize),
	}
	if aspectRatio := CreativeGrokAspectRatio(run.AspectRatio); aspectRatio != "" {
		request["aspect_ratio"] = aspectRatio
	}
	if quality := strings.TrimSpace(payload.Quality); quality != "" {
		request["quality"] = quality
	}

	images := make([]map[string]string, 0, len(payload.Sources))
	for _, source := range payload.Sources {
		mime := source.Mime
		if mime == "" {
			mime = "image/png"
		}
		images = append(images, map[string]string{
			"type": "image_url",
			"url":  "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(source.Bytes),
		})
	}
	if len(images) == 1 {
		request["image"] = images[0]
	} else {
		request["images"] = images
	}
	return request
}
