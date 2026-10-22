package openai

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// request_headers.go builds Responses HTTP requests and supplies authenticated
// POST construction for image and search requests.
// Responses request identity depends on authentication, client headers,
// credential scoping, and optional fingerprint policy. Incoming turn state is
// filtered before Codex session headers are rebuilt, so known failover state
// cannot be associated with the newly selected provider.
//
// Codex requests isolate session/conversation values by API key and credential
// namespace, with special unary handling for the legacy compact endpoint and
// different header selection for a Messages bridge. Provider identity applies
// before precomputed fingerprints, which can intentionally replace those scoped
// values. Final allowed overrides precede OpenCode session handling, beta
// features, routing hints, and diagnostics. Request construction uses the
// prepared body throughout, letting the generated routing hint describe the
// actual model/tier fields after upstream request preparation.

// ResponsesRequestOptions supplies the selected URL and identity/header
// callbacks for Responses construction.
type ResponsesRequestOptions struct {
	URL             string                                     // validated Responses upstream endpoint
	ForwardHeaders  func() http.Header                         // required inbound header reader
	Authenticate    func(context.Context) (http.Header, error) // required authentication header resolver
	ProviderHeaders func(context.Context, http.Header) error   // required account header resolver
	// UsesCodex reports internal ChatGPT eligibility, IsCompact selects legacy
	// unary compact routing, and ForceCodexCLI selects canonical client identity.
	// These readers stay stable during one build so authentication and header
	// phases use the same prepared provider decision.
	UsesCodex, IsCompact, ForceCodexCLI func() bool
	AllowHeader                         func(string) bool // required forwarded-header admission predicate
	GuardTurnState                      func(http.Header) // required continuation-origin header filter
	MessagesBridge                      func([]byte) bool // required Messages compatibility predicate
	// Originator supplies the outgoing client name, and CompactSession supplies
	// the legacy compact session value. Codex construction reads the applicable
	// value before session isolation and final client-identity pairing.
	Originator, CompactSession func() string
	APIKeyID                   func() int64               // required caller key-ID reader
	IsolateSession             func(int64, string) string // required upstream session isolation
	ApplyUserAgent             func(*http.Request)        // required outgoing User-Agent writer
	// ApplyProviderIdentity scopes credential IDs, ApplyFingerprint writes attempt
	// IDs, OverrideHeaders applies provider values, OpenCodeSession applies client
	// session compatibility, and BetaFeatures negotiates capabilities. These
	// callbacks mutate the same header map in the order used by the builder.
	ApplyProviderIdentity, ApplyFingerprint, OverrideHeaders, OpenCodeSession, BetaFeatures func(http.Header)
	// RoutingHint writes final model/tier routing information, and Diagnostics
	// records outgoing routing fields. Both receive prepared headers and the
	// same body bytes used to construct the outbound request.
	RoutingHint, Diagnostics func(http.Header, []byte)
}

// BuildResponsesRequest creates an authenticated Responses POST carrying the
// prepared body. Required option callbacks supply provider eligibility, inbound
// headers, client identity, session isolation, and final policy; callers keep
// those eligibility decisions stable during construction. The returned request
// retains ctx for its later HTTP exchange.
//
// Codex sessions are rebuilt after forwarding and turn-origin filtering. Legacy
// compact acceptance is JSON, and its isolated session can be replaced by
// promptCacheKey when supplied; ordinary Codex acceptance is SSE. Credential
// identity precedes the attempt fingerprint, then official client pairing runs
// after User-Agent selection. Allowed overrides, OpenCode session handling,
// beta features, routing hints, and diagnostics run in that order, making later
// policy callbacks see the prepared outgoing headers.
func BuildResponsesRequest(ctx context.Context, body []byte, promptCacheKey string, options ResponsesRequestOptions) (*http.Request, error) {
	req, err := newAuthenticatedPostRequest(ctx, options.URL, body, options.Authenticate)
	if err != nil {
		return nil, err
	}

	// Codex authentication also needs account headers and the ChatGPT Host;
	// keep these request properties paired with the selected internal endpoint.
	if options.UsesCodex() {
		// Host is an HTTP request property; setting req.Host supplies the ChatGPT
		// host used by the selected internal endpoint rather than an ordinary header field.
		req.Host = "chatgpt.com"
		if err := options.ProviderHeaders(ctx, req.Header); err != nil {
			return nil, fmt.Errorf("resolve chatgpt provider headers: %w", err)
		}
	}

	// Forward permitted client headers after authentication, allowing the prepared
	// allowlist to select which client identity and protocol metadata reaches this request.
	for key, values := range options.ForwardHeaders() {
		lowerKey := strings.ToLower(key)
		if options.AllowHeader(lowerKey) {
			for _, v := range values {
				req.Header.Add(key, v)
			}
		}
	}
	// Reject known turn state from a previous provider before rebuilding sessions,
	// preventing an opaque continuation value from being reused under another credential identity.
	options.GuardTurnState(req.Header)
	if options.UsesCodex() {
		compatMessagesBridge := options.MessagesBridge(body)
		// Save the incoming conversation-presence signal before rebuilding isolated
		// session headers, so the Messages bridge retains its rule for absent conversation identity.
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

	// The selected force policy replaces any earlier User-Agent with the
	// canonical identity before credential scoping and final Codex pairing.
	// Allowed header overrides still run later in this builder.
	if options.ForceCodexCLI() {
		req.Header.Set("user-agent", CodexCanonicalUserAgent())
	}

	// Scope IDs to the OAuth credential so failover changes their namespace.
	// The optional fingerprint policy can override them afterward.
	options.ApplyProviderIdentity(req.Header)

	// Fingerprints use the same precomputed IDs already written into the body;
	// applying them after credential scoping lets the selected mode unify those outgoing identities.
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

	// Gateway overrides follow the selected provider's eligibility after earlier
	// header choices. OpenAI OAuth has no override map; other platforms apply
	// their own supported provider types.
	options.OverrideHeaders(req.Header)
	// Apply caller-supplied OpenCode session handling after provider overrides.
	options.OpenCodeSession(req.Header)
	// Beta negotiation follows allowed overrides so required feature tokens can
	// be added to the final header values before routing hints and diagnostic observation.
	options.BetaFeatures(req.Header)
	options.RoutingHint(req.Header, body)
	options.Diagnostics(req.Header, body)

	return req, nil
}

// newAuthenticatedPostRequest creates a POST carrying the OpenAI transport
// profile and copied authentication headers. Request construction errors precede
// authentication; authenticate receives ctx, while the returned request carries
// the transport profile in a derived context. Header values retain their per-key
// order and use slices owned by the request, including when authenticate reuses
// its map.
func newAuthenticatedPostRequest(ctx context.Context, address string, body []byte, authenticate func(context.Context) (http.Header, error)) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req = req.WithContext(upstream.WithHTTPUpstreamProfile(req.Context(), upstream.HTTPUpstreamProfileOpenAI))
	authHeaders, err := authenticate(ctx)
	if err != nil {
		return nil, fmt.Errorf("build openai authentication headers: %w", err)
	}
	for key, values := range authHeaders {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	return req, nil
}

// String returns a fixed Responses label that omits forwarded client headers.
func (ResponsesRequestOptions) String() string { return "openai responses request options" }

// GoString returns the redacted Responses option label for Go-syntax
// formatting.
func (o ResponsesRequestOptions) GoString() string { return o.String() }

// EnsureRemoteCompactionV2Header adds the exact remote_compaction_v2 token to
// writable headers when absent. Nil headers are ignored; an existing token
// returns with all current spelling and values intact, otherwise declared
// tokens are merged into one value.
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

// AppendResponsesPathSuffix appends a permitted path suffix to the trimmed base
// URL. Unsafe, empty, or unusable suffixes return that base, and an empty base
// returns empty text; validation precedes any concatenation.
func AppendResponsesPathSuffix(baseURL, suffix string) string {
	trimmedBase := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	// Reject unsupported path spellings before URL concatenation so traversal or
	// query-bearing suffixes cannot change the selected Responses endpoint through this helper.
	trimmedSuffix, ok := upstream.SanitizedUpstreamPathSuffix(suffix)
	if !ok || trimmedBase == "" || trimmedSuffix == "" {
		return trimmedBase
	}
	return trimmedBase + trimmedSuffix
}
