package vertex

import (
	"bytes"
	"context"
	"net/http"
	"strconv"
	"strings"

	wire "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
)

// request.go builds authenticated Vertex Claude requests with filtered beta
// features and sanitized bodies.
// Claude requests sent to Vertex carry the model in the URL and a Vertex
// version in the body. Beta policy and the local allowlist decide the
// final header before body sanitization, so context-management options follow
// the features actually forwarded.
//
// Client headers pass through caller-supplied name and value handlers on a
// fresh request. The builder then selects service-account authentication and
// replaces the copied beta header with the filtered value. Project and
// location callbacks resolve endpoint identity for the same chosen model; the
// caller supplies those callbacks and sends the returned request.

// vertexSupportedBetaTokens is the local forwarding allowlist for Anthropic
// beta features on Vertex. FilterBetaTokens requires membership after applying
// configured drops.
var vertexSupportedBetaTokens = map[string]bool{
	"context-1m-2025-08-07":                  true,
	"context-management-2025-06-27":          true,
	"fine-grained-tool-streaming-2025-05-14": true,
	"interleaved-thinking-2025-05-14":        true,
}

// AnthropicRequestOptions supplies request metadata and callbacks for one
// Vertex Claude request.
type AnthropicRequestOptions struct {
	ClientBeta     string          // client beta header
	ClientHeaders  http.Header     // source request headers
	AllowedHeaders map[string]bool // allowed lowercase header names
	// Project, Location, Policy, SanitizeBody, SetHeader, DeleteHeader, and Debug
	// are called for every valid request and must be supplied. WireCasing and
	// AddHeader handle allowed ClientHeaders entries when present.
	Project  func() string       // selected project identifier
	Location func(string) string // location for model identifier
	// configured beta drop evaluation
	Policy       func(context.Context, string) (map[string]struct{}, error)
	SanitizeBody func([]byte, string) ([]byte, bool) // body and forwarded-beta sanitizer
	WireCasing   func(string) string                 // outgoing header name spelling
	// AddHeader appends header values; SetHeader replaces header values.
	AddHeader, SetHeader func(http.Header, string, string)
	DeleteHeader         func(http.Header, string) // all-spelling header removal
	// prepared request diagnostic callback
	Debug func(http.Header, []byte, map[string]string)
}

// FilterBetaTokens parses the client beta header, applies configured drops,
// and keeps tokens in vertexSupportedBetaTokens. Accepted values are
// deduplicated in input order; the result is empty when every token is
// removed.
func FilterBetaTokens(header string, drop map[string]struct{}) string {
	tokens := wire.ParseAnthropicBetaHeader(header)
	if len(tokens) == 0 {
		return ""
	}
	out := make([]string, 0, len(tokens))
	seen := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		if _, dropped := drop[t]; dropped {
			continue
		}
		if !vertexSupportedBetaTokens[t] {
			continue
		}
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return strings.Join(out, ",")
}

// BuildAnthropicRequest returns a prepared Vertex Claude request for the
// selected model and service-account token. Callers supply a nonnull JSON
// object and the callbacks required by AnthropicRequestOptions, then send the
// returned request with their transport policy.
//
// Beta policy runs before the allowlist, and the resulting header controls
// body sanitization. Header copying follows the caller allowlist before
// authentication and beta replacement. A policy, body conversion, endpoint, or
// request-construction failure returns before any network exchange.
func BuildAnthropicRequest(ctx context.Context, body []byte, token, modelID string, reqStream bool, options AnthropicRequestOptions) (*http.Request, error) {
	vertexBody, err := BuildVertexAnthropicRequestBody(body)
	if err != nil {
		return nil, err
	}

	// Evaluate the configured drop policy before the platform allowlist so a
	// blocked beta can fail the request before any outbound request exists.
	clientBeta := options.ClientBeta
	drop, err := options.Policy(ctx, clientBeta)
	if err != nil {
		return nil, err
	}
	finalBeta := FilterBetaTokens(clientBeta, drop)

	// Use the filtered beta header for body cleanup so context_management cannot
	// keep options whose enabling beta was removed by either filter.
	if sanitized, changed := options.SanitizeBody(vertexBody, finalBeta); changed {
		vertexBody = sanitized
	}
	fullURL, err := BuildVertexAnthropicURL(options.Project(), options.Location(modelID), modelID, reqStream)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fullURL, bytes.NewReader(vertexBody))
	if err != nil {
		return nil, err
	}

	if options.ClientHeaders != nil {
		for key, values := range options.ClientHeaders {
			lowerKey := strings.ToLower(strings.TrimSpace(key))
			if !options.AllowedHeaders[lowerKey] || lowerKey == "anthropic-version" {
				continue
			}
			wireKey := options.WireCasing(key)
			for _, v := range values {
				options.AddHeader(req.Header, wireKey, v)
			}
		}
	}

	// Clear canonical client credential headers before setting the service account
	// bearer. Caller header handlers govern raw spelling, and the beta
	// replacement below uses DeleteHeader.
	req.Header.Del("authorization")
	req.Header.Del("x-api-key")
	req.Header.Del("x-goog-api-key")
	req.Header.Del("cookie")
	req.Header.Del("anthropic-version")
	options.SetHeader(req.Header, "authorization", "Bearer "+token)
	options.SetHeader(req.Header, "content-type", "application/json")

	// Remove copied beta spellings through the caller handler before inserting
	// the filtered value, including when filtering leaves no header to send.
	options.DeleteHeader(req.Header, "anthropic-beta")
	if finalBeta != "" {
		options.SetHeader(req.Header, "anthropic-beta", finalBeta)
	}

	options.Debug(req.Header, vertexBody, map[string]string{
		"url":        req.URL.String(),
		"token_type": "service_account",
		"model":      modelID,
		"stream":     strconv.FormatBool(reqStream),
	})

	return req, nil
}
