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

type PassthroughRequestOptions struct {
	ResponsesRequestOptions
	AllowTimeoutHeaders    func() bool
	AllowPassthroughHeader func(string, bool) bool
	MatchedOriginator      func() string
}

// BuildPassthroughRequest retains body bytes and resolves attempt-specific identity and session headers.
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

	// Drop turn state known to belong to a previous provider after failover.
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
	// Passthrough shares the credential namespace and precomputed fingerprint with ordinary requests.
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
	// Apply session beta features after overrides so required capabilities survive.
	options.BetaFeatures(req.Header)
	options.RoutingHint(req.Header, body)
	options.Diagnostics(req.Header, body)

	return req, nil
}

func StripLegacyResponsesBeta(headers http.Header) {
	if headers == nil {
		return
	}

	preserved := make([]string, 0)
	for key, values := range headers {
		if !strings.EqualFold(strings.TrimSpace(key), "OpenAI-Beta") {
			continue
		}
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
