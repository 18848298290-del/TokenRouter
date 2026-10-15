package openaiforward

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
)

// anthropic_request.go builds authenticated Anthropic requests with
// beta-dependent body sanitation and ordered header application.
// Anthropic body fields can depend on the beta header that the upstream
// actually receives. Request construction chooses the provider beta override
// before sanitizing dependent fields, then copies allowed incoming headers,
// replaces caller authentication, and applies provider overrides after required
// header defaults.
//
// Header callbacks retain platform-specific casing and authentication rules
// while the builder owns their execution order. The sanitized body is returned
// beside the request so callers can retain the bytes actually sent. Target URL
// validation precedes construction, and neither header processing nor
// sanitation changes the provider credential selected by the caller for
// authentication.

// NativeAnthropicRequestOptions supplies headers, authentication, and body sanitization for one request.
type NativeAnthropicRequestOptions struct {
	// Headers supplies inbound values for synchronous construction. Callers keep
	// the map and its value slices stable while header callbacks read them.
	Headers       http.Header                      // borrowed inbound header values
	GetHeader     func(http.Header, string) string // case-aware header value reader
	OverrideValue func(string) (string, bool)      // permitted provider override reader
	// Sanitize receives the body and final beta input before request construction;
	// its changed result selects the returned and transmitted body bytes.
	Sanitize      func([]byte, string) ([]byte, bool)
	AllowedHeader func(string) bool                 // incoming header allow predicate
	WireCasing    func(string) string               // platform header spelling mapper
	AddHeader     func(http.Header, string, string) // wire header append callback
	SetHeader     func(http.Header, string, string) // wire header replacement callback
	AuthHeader    func(http.Header, string)         // upstream credential header writer
	// ApplyOverrides runs after authentication and default headers; the supplied
	// adapter filters forbidden names before permitted values replace earlier
	// header sources.
	ApplyOverrides func(http.Header)
}

// NativeAnthropicTargetURL validates a trimmed configured base and appends
// /v1/messages after removing trailing slashes. A blank base returns a
// provider-specific error, and validator failures are wrapped before any target
// address is returned.
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

// BuildNativeAnthropicRequest returns an authenticated native Messages request
// and its adopted body bytes. Callers provide the header and sanitation
// callbacks used on their paths, and the incoming header map is borrowed for
// synchronous reading. The context, API credential, and target are supplied by
// the selected attempt.
//
// A provider beta override replaces the incoming beta before body sanitation,
// keeping beta-dependent fields consistent with the effective request. Allowed
// incoming headers are copied using wire spelling, caller credentials are
// removed before upstream authentication, and permitted provider overrides run
// after required defaults. A changed sanitation result is the body used to
// build the request and returned to the caller.
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

	// Apply permitted provider overrides after authentication and required
	// defaults so allowed replacements determine the final wire values already
	// considered during beta-body sanitation.
	o.ApplyOverrides(req.Header)

	return req, body, nil
}
