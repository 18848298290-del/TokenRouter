package openaiforward

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
)

// NativeAnthropicRequestOptions supplies headers, authentication, and body sanitization for one request.
type NativeAnthropicRequestOptions struct {
	Headers        http.Header
	GetHeader      func(http.Header, string) string
	OverrideValue  func(string) (string, bool)
	Sanitize       func([]byte, string) ([]byte, bool)
	AllowedHeader  func(string) bool
	WireCasing     func(string) string
	AddHeader      func(http.Header, string, string)
	SetHeader      func(http.Header, string, string)
	AuthHeader     func(http.Header, string)
	ApplyOverrides func(http.Header)
}

func NativeAnthropicTargetURL(providerID int64, baseURL string, validate func(string) (string, error)) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return "", fmt.Errorf("provider %d has no anthropic protocol base url", providerID)
	}
	validatedURL, err := validate(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid base_url: %w", err)
	}
	return strings.TrimRight(validatedURL, "/") + "/v1/messages", nil
}

func BuildNativeAnthropicRequest(ctx context.Context, body []byte, apiKey, targetURL string, o NativeAnthropicRequestOptions) (*http.Request, []byte, error) {
	// Keep beta-dependent body fields consistent with the final beta header.
	// Sending a field without its required beta token can cause an upstream 400.
	clientBeta := ""
	if o.Headers != nil {
		clientBeta = o.GetHeader(o.Headers, "anthropic-beta")
	}
	if beta, ok := o.OverrideValue("anthropic-beta"); ok {
		clientBeta = beta
	}
	if sanitized, changed := o.Sanitize(body, clientBeta); changed {
		body = sanitized
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}

	if o.Headers != nil {
		for key, values := range o.Headers {
			lowerKey := strings.ToLower(strings.TrimSpace(key))
			if !o.AllowedHeader(lowerKey) {
				continue
			}
			wireKey := o.WireCasing(key)
			for _, v := range values {
				o.AddHeader(req.Header, wireKey, v)
			}
		}
	}

	// Remove client credentials before applying upstream authentication.
	// AuthHeader selects x-api-key or the provider's configured Bearer scheme.
	req.Header.Del("authorization")
	req.Header.Del("x-api-key")
	req.Header.Del("x-goog-api-key")
	req.Header.Del("cookie")
	o.AuthHeader(req.Header, apiKey)

	if o.GetHeader(req.Header, "content-type") == "" {
		o.SetHeader(req.Header, "content-type", "application/json")
	}
	if o.GetHeader(req.Header, "anthropic-version") == "" {
		o.SetHeader(req.Header, "anthropic-version", "2023-06-01")
	}

	// Provider overrides take precedence over headers from all earlier sources.
	o.ApplyOverrides(req.Header)

	return req, body, nil
}
