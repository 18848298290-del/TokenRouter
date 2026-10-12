package grok

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
)

var supportedVoiceHTTPEndpoints = map[string]struct{}{"tts": {}, "stt": {}, "custom-voices": {}}

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

// BuildVoiceRequest uses the supplied token and headers. Callers must validate targetURL against
// egress policy.
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
