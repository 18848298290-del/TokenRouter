package vertex

import (
	"bytes"
	"context"
	"net/http"
	"strconv"
	"strings"

	wire "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
)

// vertexSupportedBetaTokens lists the anthropic-beta tokens accepted by Vertex AI.
// Unknown tokens cause HTTP 400, so FilterBetaTokens drops values outside this list.
// Extend this list when Vertex supports another beta.
//
// Issue #3358 identifies rejected tokens: advisor-tool-2026-03-01,
// prompt-caching-scope-2026-01-05, redact-thinking-2026-02-12, and
// thinking-token-count-2026-05-13. Client identity betas such as
// claude-code-20250219 and oauth-2025-04-20 are also excluded; service accounts use Bearer authentication.
var vertexSupportedBetaTokens = map[string]bool{
	"context-1m-2025-08-07":                  true,
	"context-management-2025-06-27":          true,
	"fine-grained-tool-streaming-2025-05-14": true,
	"interleaved-thinking-2025-05-14":        true,
}

// AnthropicRequestOptions configures headers, project, location, and beta handling.
type AnthropicRequestOptions struct {
	ClientBeta           string
	ClientHeaders        http.Header
	AllowedHeaders       map[string]bool
	Project              func() string
	Location             func(string) string
	Policy               func(context.Context, string) (map[string]struct{}, error)
	SanitizeBody         func([]byte, string) ([]byte, bool)
	WireCasing           func(string) string
	AddHeader, SetHeader func(http.Header, string, string)
	DeleteHeader         func(http.Header, string)
	Debug                func(http.Header, []byte, map[string]string)
}

// FilterBetaTokens removes dropped and unsupported tokens from an anthropic-beta header.
// It deduplicates accepted tokens in input order and joins them with commas.
// The result is empty when no tokens remain.
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

func BuildAnthropicRequest(ctx context.Context, body []byte, token, modelID string, reqStream bool, options AnthropicRequestOptions) (*http.Request, error) {
	vertexBody, err := BuildVertexAnthropicRequestBody(body)
	if err != nil {
		return nil, err
	}

	// Apply configured policy first, then remove tokens outside Vertex support.
	clientBeta := options.ClientBeta
	drop, err := options.Policy(ctx, clientBeta)
	if err != nil {
		return nil, err
	}
	finalBeta := FilterBetaTokens(clientBeta, drop)

	// Sanitize context_management against the beta tokens that will actually
	// reach Vertex.
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

	req.Header.Del("authorization")
	req.Header.Del("x-api-key")
	req.Header.Del("x-goog-api-key")
	req.Header.Del("cookie")
	req.Header.Del("anthropic-version")
	options.SetHeader(req.Header, "authorization", "Bearer "+token)
	options.SetHeader(req.Header, "content-type", "application/json")

	// Replace any copied client anthropic-beta value with the filtered result.
	// Omit the header when every token was removed.
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
