package grok

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

type ResponsesRequestOptions struct {
	URL, Token, CacheIdentity, OpenAIBeta string
	OAuth                                 bool
	Profile                               func(context.Context) context.Context
	ApplyOverrides                        func(http.Header)
}

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

// ApplyCLIHeaders supplies the supported Grok CLI identity required for subscription
// OAuth traffic, including requests whose other fields already pass validation.
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

// ProviderTestBody builds a Responses probe from the administrator-supplied prompt.
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
