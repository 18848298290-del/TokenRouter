package openai

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// Passthrough requests keep prepared body bytes while selecting new
// authentication and provider-specific identity headers. Client authentication
// headers are removed before the selected assertion or bearer headers are
// installed, and turn-origin filtering runs before Codex session
// reconstruction.
//
// Existing session and conversation values have precedence over a body
// prompt_cache_key, with legacy compact supplying its own session when absent.
// Those values are isolated separately before credential scoping and optional
// attempt fingerprints apply. Codex removes the legacy Responses experiment but
// retains independent beta declarations. Allowed overrides run before final
// OpenCode session, beta, routing-hint, and diagnostic callbacks, keeping
// passthrough policy aligned with the same identity resources used by ordinary
// Responses requests.

// PassthroughRequestOptions extends Responses construction with passthrough
// header and routed-originator rules.
type PassthroughRequestOptions struct {
	ResponsesRequestOptions                         // shared Responses request callback sources
	AllowTimeoutHeaders     func() bool             // required timeout-forwarding policy reader
	AllowPassthroughHeader  func(string, bool) bool // required passthrough header predicate
	MatchedOriginator       func() string           // required routed originator reader
}

// BuildPassthroughRequest creates a POST retaining the prepared body and
// resolving selected-provider authentication. The option callbacks are required
// when their branches run; callers keep eligibility readers stable during one
// build. Header forwarding uses the timeout policy before selected credentials
// replace client authentication headers.
//
// Codex retains inbound session/conversation values when present, fills their
// absence from compact or prompt-cache values, then isolates them by API key.
// User-Agent selection precedes credential scoping, precomputed fingerprints,
// and official identity pairing. Overrides and final session/beta/routing
// callbacks follow. Legacy compact acceptance is JSON for both credential
// forms, so an inbound SSE Accept cannot change its unary response shape.
func BuildPassthroughRequest(ctx context.Context, body []byte, options PassthroughRequestOptions) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, options.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req = req.WithContext(upstream.WithHTTPUpstreamProfile(req.Context(), upstream.HTTPUpstreamProfileOpenAI))

	allowTimeoutHeaders := options.AllowTimeoutHeaders()
	{
		for key, values := range options.ForwardHeaders() {
			lower := strings.ToLower(strings.TrimSpace(key))
			if !options.AllowPassthroughHeader(lower, allowTimeoutHeaders) {
				continue
			}
			for _, v := range values {
				req.Header.Add(key, v)
			}
		}
	}

	// Filter turn state before replacing credentials so a known previous-provider
	// continuation cannot be sent under the newly selected authentication identity.
	options.GuardTurnState(req.Header)
	req.Header.Del("authorization")
	req.Header.Del("x-api-key")
	req.Header.Del("x-goog-api-key")
	authHeaders, err := options.Authenticate(ctx)
	if err != nil {
		return nil, fmt.Errorf("build openai authentication headers: %w", err)
	}
	for key, values := range authHeaders {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	// Supply the headers required by the ChatGPT internal API.
	if options.UsesCodex() {
		// Codex OAuth HTTP omits the legacy Responses experiment. Remove that token
		// from older clients while retaining independent beta negotiation.
		StripLegacyResponsesBeta(req.Header)
		promptCacheKey := strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String())
		req.Host = "chatgpt.com"
		if err := options.ProviderHeaders(ctx, req.Header); err != nil {
			return nil, fmt.Errorf("resolve chatgpt provider headers: %w", err)
		}
		apiKeyID := options.APIKeyID()

		clientSessionID := strings.TrimSpace(req.Header.Get("session_id"))
		clientConversationID := strings.TrimSpace(req.Header.Get("conversation_id"))
		if options.IsCompact() {
			req.Header.Set("accept", "application/json")
			if req.Header.Get("version") == "" {
				req.Header.Set("version", CodexCanonicalClientVersion())
			}
			if clientSessionID == "" {
				clientSessionID = options.CompactSession()
			}
		} else if req.Header.Get("accept") == "" {
			req.Header.Set("accept", "text/event-stream")
		}
		if req.Header.Get("originator") == "" {
			req.Header.Set("originator", ResolveCodexOutboundIdentity("").Originator)
		}
		if originator := options.MatchedOriginator(); originator != "" {
			req.Header.Set("originator", originator)
		}

		if clientSessionID == "" {
			clientSessionID = promptCacheKey
		}
		if clientConversationID == "" {
			clientConversationID = promptCacheKey
		}
		if clientSessionID != "" {
			req.Header.Set("session_id", options.IsolateSession(apiKeyID, clientSessionID))
		}
		if clientConversationID != "" {
			req.Header.Set("conversation_id", options.IsolateSession(apiKeyID, clientConversationID))
		}
	} else if options.IsCompact() {
		// Compact requires unary JSON, even if the client forwarded Accept: text/event-stream.
		// Set the API-key request Accept header accordingly (issue #3777).
		req.Header.Set("accept", "application/json")
	}

	options.ApplyUserAgent(req)
	// Scope under the selected credential before applying the attempt fingerprint;
	// this gives passthrough the same header/body identity tuple used by ordinary Responses construction.
	options.ApplyProviderIdentity(req.Header)
	options.ApplyFingerprint(req.Header)
	// Apply the same official identity pairing and version correction as ordinary OAuth requests.
	if options.UsesCodex() {
		EnforceCodexIdentityHeadersWithUA(req.Header, "")
	}

	if req.Header.Get("content-type") == "" {
		req.Header.Set("content-type", "application/json")
	}

	options.OverrideHeaders(req.Header)
	options.OpenCodeSession(req.Header)
	// Apply beta negotiation after allowed overrides so the final request retains
	// required protocol feature tokens before its routing hint is generated.
	options.BetaFeatures(req.Header)
	options.RoutingHint(req.Header, body)
	options.Diagnostics(req.Header, body)

	return req, nil
}

// StripLegacyResponsesBeta removes the Responses experimental token from all
// case-insensitive beta header entries. Nil headers are ignored; other nonblank
// tokens are retained and republished under the canonical header name, with
// multiple original values remaining separate.
func StripLegacyResponsesBeta(headers http.Header) {
	if headers == nil {
		return
	}

	preserved := make([]string, 0)
	for key, values := range headers {
		if !strings.EqualFold(strings.TrimSpace(key), "OpenAI-Beta") {
			continue
		}
		// Remove each spelling before republishing kept tokens canonically, so an
		// alternate-case map key cannot retain the obsolete Responses negotiation value.
		delete(headers, key)
		for _, value := range values {
			parts := strings.Split(value, ",")
			kept := parts[:0]
			for _, part := range parts {
				part = strings.TrimSpace(part)
				if part == "" || strings.EqualFold(part, "responses=experimental") {
					continue
				}
				kept = append(kept, part)
			}
			if len(kept) > 0 {
				preserved = append(preserved, strings.Join(kept, ", "))
			}
		}
	}
	for _, value := range preserved {
		headers.Add("OpenAI-Beta", value)
	}
}
