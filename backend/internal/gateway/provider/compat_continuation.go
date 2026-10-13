package provider

import (
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Previous-response recovery needs to distinguish an unavailable continuation
// ID from an unsupported continuation parameter. Compatible API Key requests
// enable continuation only for selected model identities and saved support
// policy; classifiers then recognize upstream text and structured error forms
// before a caller decides how to retry.

// CompatContinuationEnabled permits automatic continuation for API Key targets
// whose saved extras enable it and whose model qualifies for compatibility
// cache keys. Nil targets, other credential types, or unsupported model
// identities return false.
func CompatContinuationEnabled(provider *ExecutionProvider, model string) bool {
	if provider == nil || provider.Record.Type != capability.ProviderTypeAPIKey {
		return false
	}
	if !providercore.ResolveResponsesContinuationSupported(provider.Record.Extra) {
		return false
	}
	return ShouldAutoInjectPromptCacheKeyForCompat(model)
}

// CompatPreviousResponseNotFound recognizes missing or unavailable
// previous-response evidence for HTTP 400 and 404. It checks supplied text,
// raw body, and nested error code/message, including unsupported-parameter
// compatibility markers.
func CompatPreviousResponseNotFound(statusCode int, upstreamMsg string, upstreamBody []byte) bool {
	if statusCode != http.StatusBadRequest && statusCode != http.StatusNotFound {
		return false
	}
	check := func(s string) bool {
		lower := strings.ToLower(strings.TrimSpace(s))
		return strings.Contains(lower, "previous_response_not_found") ||
			lower == "previous_response_id is not available for this user" ||
			(strings.Contains(lower, "previous response") && strings.Contains(lower, "not found")) ||
			(strings.Contains(lower, "unsupported parameter") && strings.Contains(lower, "previous_response_id"))
	}
	if check(upstreamMsg) || check(string(upstreamBody)) {
		return true
	}
	return check(gjson.GetBytes(upstreamBody, "error.code").String()) ||
		check(gjson.GetBytes(upstreamBody, "error.message").String())
}

// CompatPreviousResponseUnsupported recognizes HTTP 400 text naming
// previous_response_id alongside an unsupported-protocol marker. It checks
// message, raw body, and nested errors; other status codes return false.
func CompatPreviousResponseUnsupported(statusCode int, upstreamMsg string, upstreamBody []byte) bool {
	if statusCode != http.StatusBadRequest {
		return false
	}
	check := func(s string) bool {
		lower := strings.ToLower(strings.TrimSpace(s))
		if !strings.Contains(lower, "previous_response_id") {
			return false
		}
		return strings.Contains(lower, "unsupported parameter") ||
			strings.Contains(lower, "only supported on responses websocket") ||
			strings.Contains(lower, "not supported") ||
			strings.Contains(lower, "requires an openai api-key provider for http requests")
	}
	if check(upstreamMsg) || check(string(upstreamBody)) {
		return true
	}
	return check(gjson.GetBytes(upstreamBody, "error.code").String()) ||
		check(gjson.GetBytes(upstreamBody, "error.message").String())
}
