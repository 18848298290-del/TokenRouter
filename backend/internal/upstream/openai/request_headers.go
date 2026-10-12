package openai

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// ResponsesRequestOptions supplies the URL and callbacks; identity is resolved during request construction.
type ResponsesRequestOptions struct {
	URL                                                                                     string
	ForwardHeaders                                                                          func() http.Header
	Authenticate                                                                            func(context.Context) (http.Header, error)
	ProviderHeaders                                                                         func(context.Context, http.Header) error
	UsesCodex, IsCompact, ForceCodexCLI                                                     func() bool
	AllowHeader                                                                             func(string) bool
	GuardTurnState                                                                          func(http.Header)
	MessagesBridge                                                                          func([]byte) bool
	Originator, CompactSession                                                              func() string
	APIKeyID                                                                                func() int64
	IsolateSession                                                                          func(int64, string) string
	ApplyUserAgent                                                                          func(*http.Request)
	ApplyProviderIdentity, ApplyFingerprint, OverrideHeaders, OpenCodeSession, BetaFeatures func(http.Header)
	RoutingHint, Diagnostics                                                                func(http.Header, []byte)
}

// BuildResponsesRequest builds a Responses HTTP request from the supplied options.
func BuildResponsesRequest(ctx context.Context, body []byte, promptCacheKey string, options ResponsesRequestOptions) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, options.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req = req.WithContext(upstream.WithHTTPUpstreamProfile(req.Context(), upstream.HTTPUpstreamProfileOpenAI))

	// Authenticate resolves a request-specific Agent assertion or bearer credentials.
	authHeaders, err := options.Authenticate(ctx)
	if err != nil {
		return nil, fmt.Errorf("build openai authentication headers: %w", err)
	}
	for key, values := range authHeaders {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	// Set headers specific to OAuth providers (ChatGPT internal API)
	if options.UsesCodex() {
		// Required: set Host for ChatGPT API (must use req.Host, not Header.Set)
		req.Host = "chatgpt.com"
		if err := options.ProviderHeaders(ctx, req.Header); err != nil {
			return nil, fmt.Errorf("resolve chatgpt provider headers: %w", err)
		}
	}

	// Whitelist passthrough headers
	for key, values := range options.ForwardHeaders() {
		lowerKey := strings.ToLower(key)
		if options.AllowHeader(lowerKey) {
			for _, v := range values {
				req.Header.Add(key, v)
			}
		}
	}
	// Drop turn state known to belong to a previous provider after failover.
	options.GuardTurnState(req.Header)
	if options.UsesCodex() {
		compatMessagesBridge := options.MessagesBridge(body)
		// Rebuild session headers with isolated values to prevent cross-user collisions.
		clientConversationID := strings.TrimSpace(req.Header.Get("conversation_id"))
		req.Header.Del("conversation_id")
		req.Header.Del("session_id")

		if compatMessagesBridge {
			req.Header.Del("OpenAI-Beta")
			req.Header.Del("originator")
		} else {
			req.Header.Set("originator", options.Originator())
		}
		apiKeyID := options.APIKeyID()
		if options.IsCompact() {
			req.Header.Set("accept", "application/json")
			if req.Header.Get("version") == "" {
				req.Header.Set("version", CodexCanonicalClientVersion())
			}
			compactSession := options.CompactSession()
			req.Header.Set("session_id", options.IsolateSession(apiKeyID, compactSession))
		} else {
			req.Header.Set("accept", "text/event-stream")
		}
		if promptCacheKey != "" {
			isolated := options.IsolateSession(apiKeyID, promptCacheKey)
			req.Header.Set("session_id", isolated)
			if !compatMessagesBridge || clientConversationID != "" {
				req.Header.Set("conversation_id", isolated)
			}
		}
	} else if options.IsCompact() {
		// Compact returns unary JSON. Set Accept for API-key providers too, so compatible gateways avoid SSE (issue #3777).
		req.Header.Set("accept", "application/json")
	}

	// Resolve the UA from TLS routing, provider settings and global defaults.
	options.ApplyUserAgent(req)

	// ForceCodexCLI supplies the canonical identity when forwarding or overrides would omit it.
	if options.ForceCodexCLI() {
		req.Header.Set("user-agent", CodexCanonicalUserAgent())
	}

	// Scope IDs to the OAuth credential so failover changes their namespace.
	// The optional fingerprint policy can override them afterward.
	options.ApplyProviderIdentity(req.Header)

	// Apply the precomputed IDs shared with the request body.
	options.ApplyFingerprint(req.Header)

	// Pair official OAuth UA and originator values and correct old version headers.
	// Unrecognized UAs, including browsers, use the canonical identity.
	if options.UsesCodex() {
		EnforceCodexIdentityHeadersWithUA(req.Header, "")
	}

	// Ensure required headers exist
	if req.Header.Get("content-type") == "" {
		req.Header.Set("content-type", "application/json")
	}

	// The callback applies enabled API-key provider overrides; OAuth supplies a no-op.
	options.OverrideHeaders(req.Header)
	// Apply caller-supplied OpenCode session handling after provider overrides.
	options.OpenCodeSession(req.Header)
	// Apply session beta features after overrides so required capabilities survive.
	options.BetaFeatures(req.Header)
	options.RoutingHint(req.Header, body)
	options.Diagnostics(req.Header, body)

	return req, nil
}

// String returns a label that omits forwarded client headers.
func (ResponsesRequestOptions) String() string { return "openai responses request options" }

func (o ResponsesRequestOptions) GoString() string { return o.String() }

// EnsureRemoteCompactionV2Header adds remote_compaction_v2 while retaining other declared capabilities.
func EnsureRemoteCompactionV2Header(h http.Header) {
	if h == nil {
		return
	}
	tokens := make([]string, 0, 4)
	for _, value := range h.Values("x-codex-beta-features") {
		for token := range strings.SplitSeq(value, ",") {
			token = strings.TrimSpace(token)
			if token == "" {
				continue
			}
			if token == "remote_compaction_v2" {
				return
			}
			tokens = append(tokens, token)
		}
	}
	tokens = append(tokens, "remote_compaction_v2")
	h.Set("x-codex-beta-features", strings.Join(tokens, ","))
}

// AppendResponsesPathSuffix appends a validated suffix, returning the trimmed base for invalid input.
func AppendResponsesPathSuffix(baseURL, suffix string) string {
	trimmedBase := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	// Validate before concatenating any suffix into the upstream URL.
	trimmedSuffix, ok := upstream.SanitizedUpstreamPathSuffix(suffix)
	if !ok || trimmedBase == "" || trimmedSuffix == "" {
		return trimmedBase
	}
	return trimmedBase + trimmedSuffix
}
