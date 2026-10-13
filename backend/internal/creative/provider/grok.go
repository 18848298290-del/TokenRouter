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

// Grok creative edits submit reference bytes in JSON data URLs, with one
// source in image and multiple sources in images. Generation and edits use
// separate xAI endpoints but share the requested resolution, ratio, quality,
// one-image count, and base64 response preference. Inpaint is rejected before
// transport is configured.
//
// The adapter acquires credentials at execution time, applies optional CLI
// proxy headers for OAuth, then applies provider overrides after its defaults.
// Response bodies are read through the bounded shared reader and closed before
// the call returns. The OpenAI-shaped base64 parser supplies one output; its
// retryable malformed-result errors remain distinct from OpenAI URL-result
// delivery failures.

// ExecuteGrok sends one xAI generation or edit request through configured Grok
// callbacks. Editing requires one to three sources, other operations fail
// without retry, and credential or endpoint acquisition failures become
// connection-class creative errors. Provider headers override prepared
// authentication and JSON defaults; the response read returns at most
// sixty-four MiB before status and image parsing.
func (e *Target) ExecuteGrok(ctx context.Context, run creative.CreativeRun, payload creative.CreativeRunPayload, upstreamModel string) ([]creative.CreativeOutput, error) {
	if run.Operation != creative.CreativeOperationGenerate && run.Operation != creative.CreativeOperationEdit {
		return nil, creative.CreativeNonRetryableError("grok platform does not support creative operation %s", run.Operation)
	}
	if run.Operation == creative.CreativeOperationEdit {
		if len(payload.Sources) == 0 {
			return nil, creative.CreativeNonRetryableError("grok image edit requires at least one source image")
		}
		// The xAI edit representation accepts at most three references.
		// Enforce this
		// before request construction so direct adapter calls honor the
		// same source
		// limit exposed by creative model capabilities.
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
	// OAuth requests aimed at a CLI proxy use the shared Grok CLI headers.
	// Per-provider overrides run afterward, so this compatibility branch does not
	// overwrite header values configured for the selected provider.
	if e.Grok.OAuth && (grok.MediaCodec{}).IsGrokCLIProxyTarget(targetURL) {
		grok.ApplyCLIHeaders(req.Header)
	}
	// Credential and JSON defaults are installed before provider overrides. Apply
	// the overrides here so a configured header controls the value sent through
	// this selected provider transport.
	e.Grok.ApplyHeaders(req.Header)

	resp, err := e.Grok.Do(req)
	if err != nil {
		return nil, creative.CreativeHTTPStatusError(0, err.Error())
	}
	// A successful transport response owns a body until parsing finishes. Close
	// it on all read and status paths so generation connections return to the
	// selected provider transport; close errors do not change the result.
	defer func() { _ = resp.Body.Close() }()
	// The shared limiter can return a truncated prefix successfully. Parse the
	// bounded response afterward so malformed image content still reaches the
	// Grok adapter retryable result classification.
	respBody, err := ReadCreativeUpstreamBody(resp.Body, 64<<20)
	if err != nil {
		return nil, creative.CreativeHTTPStatusError(0, err.Error())
	}
	if resp.StatusCode >= 400 {
		return nil, creative.CreativeHTTPStatusError(resp.StatusCode, upstream.ExtractErrorMessage(respBody))
	}
	return ParseCreativeOpenAIImageOutputs(respBody)
}

// BuildCreativeGrokRequest constructs the xAI generation body with one output
// and a base64 response preference. It uses the fixed upstreamModel,
// normalized resolution and supported ratio, and passes nonblank trimmed
// quality directly.
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

// BuildCreativeGrokEditRequest constructs one xAI edit body from supplied
// source bytes. Callers validate source count and model capabilities before
// sending this body, and a missing source MIME uses image/png.
//
// A single source is represented by image; zero or multiple sources use images
// in their supplied order. Each source becomes a data URL under the xAI url
// field, keeping its MIME and base64 bytes together. Resolution, ratio,
// quality, and the fixed upstream model are carried alongside the references,
// while output count remains one.
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
	// xAI uses a singular image object for one reference and an images array
	// for several; retain source order while choosing that protocol representation.
	if len(images) == 1 {
		request["image"] = images[0]
	} else {
		request["images"] = images
	}
	return request
}
