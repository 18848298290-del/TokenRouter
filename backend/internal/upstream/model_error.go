package upstream

import (
	"net/http"
	"strings"
)

// Model-error detection uses both the HTTP status and normalized response
// wording. The strict detector requires a model mention, while the Antigravity
// hook accepts a bare 404 as the same failure signal for its attempt policy.

// upstreamModelNotFoundKeywords supplies normalized missing-model phrases
// checked after a 404 response mentions a model.
var upstreamModelNotFoundKeywords = []string{"model not found", "unknown model", "not found"}

// IsModelNotFoundError reports whether a 404 response contains model and
// missing-resource wording. Normalization accepts separator variants, but the
// model mention and keyword can occur anywhere in the same body.
func IsModelNotFoundError(statusCode int, body []byte) bool {
	if statusCode != http.StatusNotFound {
		return false
	}
	normalized := NormalizeModelErrorBody(body)
	if normalized == "" || !strings.Contains(normalized, "model") {
		return false
	}
	return containsModelNotFoundKeyword(normalized)
}

// IsModelNotFoundOrBare404 supplies the permissive missing-model signal used
// by Antigravity attempts. Every HTTP 404 matches, including empty bodies and
// endpoint errors that the stricter wording classifier rejects.
func IsModelNotFoundOrBare404(statusCode int, body []byte) bool {
	// The Antigravity attempt hook treats endpoint-level 404 responses as this
	// signal even when strict detection finds no model-specific wording in the body.
	return IsModelNotFoundError(statusCode, body) || statusCode == http.StatusNotFound
}

// containsModelNotFoundKeyword reports whether normalized text includes any
// configured missing-model phrase.
func containsModelNotFoundKeyword(normalizedBody string) bool {
	if normalizedBody == "" {
		return false
	}
	for _, keyword := range upstreamModelNotFoundKeywords {
		if strings.Contains(normalizedBody, keyword) {
			return true
		}
	}
	return false
}

// NormalizeModelErrorBody lowercases response text and collapses separator
// variants to spaces. Empty input returns empty; this textual normalization
// accepts JSON or plain text without parsing an error object.
func NormalizeModelErrorBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	normalized := strings.ToLower(string(body))
	// Compatible providers put missing-model wording in codes such as
	// model_not_found or hyphenated messages. Shared spacing makes those forms
	// available to the same textual keyword checks.
	normalized = strings.NewReplacer("_", " ", "-", " ", "\n", " ", "\r", " ", "\t", " ").Replace(normalized)
	return strings.Join(strings.Fields(normalized), " ")
}
