package openai

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"
)

// alpha_search_headers.go builds authenticated requests for standalone Alpha
// Search and its Responses search alternative.
// Standalone Alpha Search uses search-specific authentication headers even when
// the caller arrived through a Responses-compatible client. Provider overrides
// run before Responses-only headers are removed, so an override cannot
// accidentally reintroduce session or beta routing into the standalone search
// request.
//
// The PAT Responses fallback uses a separate authenticated streaming request.
// Its search ID becomes a key-isolated session and conversation ID, keeping
// hosted-search work distinct across API keys. OAuth identity is paired after
// User-Agent selection; search metadata remains available to both request
// forms, while only the standalone path merges incoming query parameters into
// its target URL.

// AlphaSearchRequestOptions extends Responses construction with
// standalone-search header and query sources.
type AlphaSearchRequestOptions struct {
	ResponsesRequestOptions                          // shared Responses request callback sources
	Query                   func() url.Values        // required incoming query reader
	OAuth                   func() bool              // required OAuth eligibility reader
	InboundHeader           func(string) string      // required incoming header reader
	IdentityWithKey         func(http.Header, int64) // required key-scoped identity writer
	ResponsesLiteHeader     string                   // Responses Lite header name
}

// BuildAlphaSearchRequest constructs an authenticated standalone-search POST
// carrying body. Required option callbacks provide query values,
// authentication, OAuth eligibility, overrides, and identity details;
// OAuth-only callbacks are used when OAuth reports true. Existing target query
// parameters retain their values and incoming values are appended.
//
// OAuth requests receive the ChatGPT host and paired Codex identity after
// User-Agent selection. Header overrides are then applied, followed by removal
// of Responses beta, session, turn-state, and Lite headers. That final removal
// keeps provider overrides from changing a search request into a
// Responses-specific authentication path.
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

	req, err := newAuthenticatedPostRequest(ctx, parsedURL.String(), body, options.Authenticate)
	if err != nil {
		return nil, err
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

	// Apply overrides before stripping transport-specific fields so standalone
	// search cannot regain Responses routing headers through provider header configuration.
	options.OverrideHeaders(req.Header)
	StripAlphaSearchResponsesHeaders(req.Header, options.ResponsesLiteHeader)
	return req, nil
}

// BuildAlphaSearchResponsesRequest constructs the prepared PAT hosted-search
// fallback POST. The required option callbacks supply authentication, provider
// headers, identity, API-key isolation, and overrides, and body is already the
// converted Responses payload. The original Alpha Search body supplies its
// optional search ID.
//
// A nonblank ID is isolated with APIKeyID and written to both session and
// conversation headers before provider identity is applied. This binds the
// fallback to the caller's search while separating identical IDs used by
// different keys. The request accepts SSE, uses the ChatGPT host, pairs the
// final User-Agent and originator, and applies overrides last.
func BuildAlphaSearchResponsesRequest(ctx context.Context, alphaBody []byte, body []byte, options AlphaSearchRequestOptions) (*http.Request, error) {
	req, err := newAuthenticatedPostRequest(ctx, options.URL, body, options.Authenticate)
	if err != nil {
		return nil, err
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
		// Namespace the search ID by API key before publishing paired session
		// headers, so matching IDs from different callers do not share upstream history.
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
