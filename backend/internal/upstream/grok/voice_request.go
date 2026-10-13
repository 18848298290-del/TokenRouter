package grok

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
)

// Grok Voice subpaths belong to custom-Voice management and optional audio
// downloads; TTS and STT each use one base endpoint. Endpoint validation keeps
// those route shapes distinct, and request construction carries the caller's
// chosen HTTP method, bearer token, content type and header policy.

// supportedVoiceHTTPEndpoints names the three Voice route families admitted
// before path validation and upstream request construction.
var supportedVoiceHTTPEndpoints = map[string]struct{}{"tts": {}, "stt": {}, "custom-voices": {}}

// ValidateVoiceEndpoint trims the route and returns its normalized path plus
// base operation. TTS and STT accept no subpath; custom-voices accepts an
// identifier and optional audio suffix. Empty, dot, traversal and query-like
// subsegments are rejected before URL construction.
func ValidateVoiceEndpoint(endpoint string) (string, string, error) {
	endpoint = strings.Trim(strings.TrimSpace(endpoint), "/")
	parts := strings.Split(endpoint, "/")
	baseEndpoint := parts[0]
	if _, ok := supportedVoiceHTTPEndpoints[baseEndpoint]; !ok {
		return "", "", fmt.Errorf("unsupported grok voice endpoint: %s", endpoint)
	}
	if len(parts) > 1 && baseEndpoint != "custom-voices" {
		return "", "", fmt.Errorf("unsupported grok voice endpoint: %s", endpoint)
	}
	if baseEndpoint == "custom-voices" {
		if len(parts) > 3 || (len(parts) == 3 && parts[2] != "audio") {
			return "", "", fmt.Errorf("unsupported grok voice endpoint: %s", endpoint)
		}
	}
	for _, part := range parts[1:] {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "?#\\") {
			return "", "", fmt.Errorf("invalid grok voice endpoint path")
		}
	}
	return endpoint, baseEndpoint, nil
}

// BuildVoiceRequest builds the supplied method with bearer authentication and
// JSON/audio response acceptance. A blank contentType becomes application/json
// and applyHeaders runs last. Callers validate targetURL against outbound policy
// and keep body bytes stable while the HTTP request uses them.
func BuildVoiceRequest(ctx context.Context, method, targetURL, token, contentType string, body []byte, applyHeaders func(http.Header)) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json, audio/*")
	if strings.TrimSpace(contentType) == "" {
		contentType = "application/json"
	}
	req.Header.Set("Content-Type", contentType)
	if applyHeaders != nil {
		applyHeaders(req.Header)
	}
	return req, nil
}
