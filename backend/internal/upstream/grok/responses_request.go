package grok

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// responses_request.go builds authenticated Grok Responses requests with
// cache, CLI, and transport-profile settings.
// Grok Responses requests combine bearer authentication with optional
// subscription CLI identity and cache routing headers. Provider overrides are
// applied after the built-in fields, while the supplied context and optional
// profile callback carry request-specific transport policy into the HTTP
// exchange.

// ResponsesRequestOptions contains request identity and header callbacks for
// Responses forwarding.
type ResponsesRequestOptions struct {
	// URL, Token, CacheIdentity and OpenAIBeta supply the endpoint, bearer
	// credential, cache route and beta header used for one request; builders apply
	// caller overrides after these values. URL: outbound Responses URL; Token:
	// bearer credential value; CacheIdentity: conversation cache identity;
	// OpenAIBeta: optional beta header
	URL, Token, CacheIdentity, OpenAIBeta string
	OAuth                                 bool // subscription CLI identity
	// Profile replaces the request context with its returned context before the
	// request is handed to transport.
	Profile func(context.Context) context.Context // optional context profile
	// ApplyOverrides receives the populated request headers last; the caller
	// supplies the shared filtering policy for permitted overrides.
	ApplyOverrides func(http.Header) // optional provider headers
}

// BuildResponsesRequest creates a POST whose context carries this exchange's
// cancellation and profile. OAuth adds CLI identity, cache routing and optional
// beta headers follow, and ApplyOverrides runs last. The caller validates URL
// against outbound policy and keeps body bytes stable until the request
// finishes.
func BuildResponsesRequest(ctx context.Context, body []byte, o ResponsesRequestOptions) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if o.Profile != nil {
		req = req.WithContext(o.Profile(req.Context()))
	}
	req.Header.Set("Authorization", "Bearer "+o.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if o.OAuth {
		ApplyCLIHeaders(req.Header)
	}
	ApplyGrokCacheHeaders(req.Header, o.CacheIdentity)
	if strings.TrimSpace(o.OpenAIBeta) != "" {
		req.Header.Set("OpenAI-Beta", o.OpenAIBeta)
	}
	if o.ApplyOverrides != nil {
		o.ApplyOverrides(req.Header)
	}
	return req, nil
}

// ApplyCLIHeaders writes the supported Grok CLI identity into headers. A nil map
// is ignored. Header.Set canonicalizes both version spellings to the same key,
// and case-insensitive Header.Get retrieves the assigned version value.
func ApplyCLIHeaders(headers http.Header) {
	if headers == nil {
		return
	}
	version := ResolveCLIVersion()
	headers.Set("User-Agent", CLIUserAgent(version))
	headers.Set("X-Grok-Client-Version", version)
	headers.Set("x-grok-client-version", version)
	headers.Set("x-grok-client-identifier", CLIClientIdentifier)
	headers.Set("X-Grok-Client-Mode", "interactive")
}

// ProviderTestBody builds a streaming Responses probe from trimmed administrator
// input. A blank model selects DefaultResponsesModel and a blank prompt becomes
// hi; callers send the encoded body through the usual provider-test transport.
func ProviderTestBody(model, prompt string) ([]byte, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		model = DefaultResponsesModel
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		prompt = "hi"
	}
	return json.Marshal(map[string]any{
		"model":  model,
		"input":  prompt,
		"stream": true,
	})
}
