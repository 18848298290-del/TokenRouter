package openai

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

type AlphaSearchRequestOptions struct {
	ResponsesRequestOptions
	Query               func() url.Values
	OAuth               func() bool
	InboundHeader       func(string) string
	IdentityWithKey     func(http.Header, int64)
	ResponsesLiteHeader string
}

// BuildAlphaSearchRequest builds a standalone search request and strips Responses headers after overrides.
func BuildAlphaSearchRequest(ctx context.Context, body []byte, options AlphaSearchRequestOptions) (*http.Request, error) {
	parsedURL, err := url.Parse(options.URL)
	if err != nil {
		return nil, fmt.Errorf("parse alpha search URL: %w", err)
	}
	if incomingQuery := options.Query(); incomingQuery != nil {
		query := parsedURL.Query()
		for key, values := range incomingQuery {
			for _, value := range values {
				query.Add(key, value)
			}
		}
		parsedURL.RawQuery = query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, parsedURL.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req = req.WithContext(upstream.WithHTTPUpstreamProfile(req.Context(), upstream.HTTPUpstreamProfileOpenAI))

	authHeaders, err := options.Authenticate(ctx)
	if err != nil {
		return nil, fmt.Errorf("build openai authentication headers: %w", err)
	}
	for key, values := range authHeaders {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	if options.OAuth() {
		req.Host = "chatgpt.com"
		if err := options.ProviderHeaders(ctx, req.Header); err != nil {
			return nil, fmt.Errorf("resolve chatgpt provider headers: %w", err)
		}

		if turnMetadata := options.InboundHeader("X-Codex-Turn-Metadata"); turnMetadata != "" {
			req.Header.Set("X-Codex-Turn-Metadata", turnMetadata)
		}
		options.ApplyProviderIdentity(req.Header)
		if version := options.InboundHeader("Version"); version != "" {
			req.Header.Set("Version", version)
		} else {
			req.Header.Set("Version", CodexCanonicalClientVersion())
		}
		req.Header.Set("Originator", options.Originator())
		options.ApplyUserAgent(req)
		EnforceCodexIdentityHeadersWithUA(req.Header, "")
	}

	options.OverrideHeaders(req.Header)
	StripAlphaSearchResponsesHeaders(req.Header, options.ResponsesLiteHeader)
	return req, nil
}

// BuildAlphaSearchResponsesRequest builds the streaming Responses fallback with an isolated search session.
func BuildAlphaSearchResponsesRequest(ctx context.Context, alphaBody []byte, body []byte, options AlphaSearchRequestOptions) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, options.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req = req.WithContext(upstream.WithHTTPUpstreamProfile(req.Context(), upstream.HTTPUpstreamProfileOpenAI))

	authHeaders, err := options.Authenticate(ctx)
	if err != nil {
		return nil, fmt.Errorf("build openai authentication headers: %w", err)
	}
	for key, values := range authHeaders {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	req.Host = "chatgpt.com"
	if err := options.ProviderHeaders(ctx, req.Header); err != nil {
		return nil, fmt.Errorf("resolve chatgpt provider headers: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	if turnMetadata := options.InboundHeader("X-Codex-Turn-Metadata"); turnMetadata != "" {
		req.Header.Set("X-Codex-Turn-Metadata", turnMetadata)
	}
	if version := options.InboundHeader("Version"); version != "" {
		req.Header.Set("Version", version)
	} else {
		req.Header.Set("Version", CodexCanonicalClientVersion())
	}
	req.Header.Set("Originator", options.Originator())
	apiKeyID := options.APIKeyID()
	if sessionID := strings.TrimSpace(gjson.GetBytes(alphaBody, "id").String()); sessionID != "" {
		isolated := options.IsolateSession(apiKeyID, sessionID)
		req.Header.Set("Session_ID", isolated)
		req.Header.Set("Conversation_ID", isolated)
	}
	options.ApplyUserAgent(req)
	options.IdentityWithKey(req.Header, apiKeyID)
	EnforceCodexIdentityHeadersWithUA(req.Header, "")
	options.OverrideHeaders(req.Header)
	return req, nil
}

// StripAlphaSearchResponsesHeaders removes Responses-specific headers after provider overrides.
// Standalone alpha/search uses the SearchClient headers, including turn metadata,
// originator and User-Agent. Responses beta, session and Lite headers can route
// PAT searches through the wrong upstream authentication path.
func StripAlphaSearchResponsesHeaders(headers http.Header, liteHeader string) {
	if headers == nil {
		return
	}
	for _, key := range []string{
		"OpenAI-Beta",
		"Session_ID",
		"Conversation_ID",
		"X-Codex-Beta-Features",
		"X-Codex-Turn-State",
		liteHeader,
	} {
		headers.Del(key)
	}
}
