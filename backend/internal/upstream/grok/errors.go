package grok

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/tidwall/gjson"
)

// errors.go distinguishes Grok content refusals, provider access failures,
// decoder incompatibilities, and spending-limit errors.
// An xAI HTTP 403 can describe refused content or loss of provider access, and
// those outcomes lead the gateway to different retry decisions. Account and
// entitlement text or structured markers take precedence over content-policy
// evidence, so a provider-access denial cannot be consumed as an ordinary prompt
// refusal.
//
// Structured matching walks maps and arrays while recognizing marker fields by
// normalized names. Specific content phrases handle unstructured bodies and
// cases whose code alone is ambiguous, including permission_denied. Decoder
// compatibility uses a separate 422 check that requires both decoding evidence
// and the relevant input shape. Spending-limit recognition selects its own code
// and message fields, keeping billing diagnostics separate from the
// request-content classifier.

// IsGrokContentPolicyRejection recognizes request-content refusal in an HTTP 403
// body. Empty bodies and other statuses return false. Callers use this decision
// before provider failure classification so refused content stays with the
// request.
//
// Provider-access phrases and structured account or entitlement markers take
// precedence over moderation markers. Otherwise recognized structured content
// codes or specific moderation text establish refusal. This order matters when a
// body contains both account-access and policy language: access evidence leaves
// provider health and caller failover policy available for the failed account.
func IsGrokContentPolicyRejection(statusCode int, responseBody []byte) bool {
	if statusCode != http.StatusForbidden || len(responseBody) == 0 {
		return false
	}
	if GrokProviderAccessMessage(string(responseBody)) {
		return false
	}

	var payload any
	// Structured markers take part when decoding succeeds; non-JSON bodies still
	// reach the specific text matcher so plain upstream refusals remain
	// recognizable.
	if json.Unmarshal(responseBody, &payload) == nil {
		if GrokStructuredProviderAccessMarker(payload) {
			return false
		}
		if GrokStructuredContentPolicyMarker(payload) {
			return true
		}
	}

	return GrokContentPolicyMessage(string(responseBody))
}

// GrokStructuredProviderAccessMarker recursively finds recognized access codes
// in marker fields.
func GrokStructuredProviderAccessMarker(value any) bool {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			normalizedKey := NormalizeGrokErrorMarker(key)
			switch normalizedKey {
			case "code", "error_code", "type", "category", "reason":
				if marker, ok := child.(string); ok && IsGrokProviderAccessCode(marker) {
					return true
				}
			}
			if GrokStructuredProviderAccessMarker(child) {
				return true
			}
		}
	case []any:
		if slices.ContainsFunc(node, GrokStructuredProviderAccessMarker) {
			return true
		}
	}
	return false
}

// GrokStructuredContentPolicyMarker recursively finds recognized moderation
// codes in marker fields.
func GrokStructuredContentPolicyMarker(value any) bool {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			normalizedKey := NormalizeGrokErrorMarker(key)
			switch normalizedKey {
			case "code", "error_code", "type", "category", "reason":
				if marker, ok := child.(string); ok && IsGrokContentPolicyCode(marker) {
					return true
				}
			}
			if GrokStructuredContentPolicyMarker(child) {
				return true
			}
		}
	case []any:
		if slices.ContainsFunc(node, GrokStructuredContentPolicyMarker) {
			return true
		}
	}
	return false
}

// NormalizeGrokErrorMarker lowercases trimmed markers and normalizes spaces and
// hyphens.
func NormalizeGrokErrorMarker(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	// Normalize marker separators before matching known codes so flat and nested
	// envelopes can express the same recognized error category.
	value = strings.ReplaceAll(value, "-", "_")
	value = strings.ReplaceAll(value, " ", "_")
	return value
}

// IsGrokContentPolicyCode reports whether a normalized code names recognized
// moderation.
func IsGrokContentPolicyCode(value string) bool {
	switch NormalizeGrokErrorMarker(value) {
	case "content_filter",
		"content_policy",
		"content_policy_violation",
		"content_moderation",
		"cyber_policy",
		"new_sensitive":
		return true
	default:
		return false
	}
}

// IsGrokProviderAccessCode recognizes account and entitlement markers requiring
// provider handling.
func IsGrokProviderAccessCode(value string) bool {
	// permission_denied can describe a content refusal, so that code alone cannot
	// select provider-access handling; the caller also checks specific message
	// evidence.
	switch NormalizeGrokErrorMarker(value) {
	case "account_suspended",
		"account_disabled",
		"user_suspended",
		"user_disabled",
		"subscription_required",
		"entitlement_required",
		"not_entitled",
		"plan_required":
		return true
	default:
		return false
	}
}

// GrokProviderAccessMessage finds account or entitlement phrases in trimmed
// lowercase text.
func GrokProviderAccessMessage(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	for _, phrase := range []string{
		"account suspended",
		"account has been suspended",
		"account disabled",
		"account has been disabled",
		"user suspended",
		"user has been suspended",
		"subscription required",
		"entitlement required",
		"not entitled",
	} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// GrokContentPolicyMessage recognizes specific request-refusal phrases in
// case-insensitive response text.
func GrokContentPolicyMessage(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	if lower == "" {
		return false
	}

	// Require concrete request-refusal phrases here because generic policy language
	// can accompany account or entitlement errors that need separate provider
	// handling.
	for _, phrase := range []string{
		"the moderation feature is not available",
		"image is sensitive",
		"text is sensitive",
		"prohibited content",
		"forbidden content",
		"content policy violation",
		"content policy rejection",
		"content policy rejected",
		"content moderation rejection",
		"content moderation rejected",
		"content moderation blocked",
		"request blocked by content moderation",
		"request rejected by content moderation",
		"request blocked by policy",
		"request rejected by policy",
		"request violates policy",
		"prompt violates content policy",
		"prompt violates policy",
		"input violates content policy",
		"input violates policy",
		"violates usage guidelines",
	} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}

	return false
}

// GrokContentPolicyClientMessage supplies the standard refusal message when
// message is exactly empty. Nonempty strings pass through unchanged; callers
// trim and sanitize upstream text before invoking this helper for client
// presentation.
func GrokContentPolicyClientMessage(message string) string {
	if message == "" {
		return "Request blocked by upstream content policy"
	}
	return message
}

// IsGrokDecoderCompatibilityError recognizes HTTP 422 decoder errors tied to
// input or message content. Other statuses and empty bodies return false before
// selected error messages are inspected.
//
// Each candidate needs decoding language together with ModelInput or indexed
// input evidence. Indexed messages instead need both content and variant-mismatch
// wording. Requiring both signals in the same message keeps an unrelated decoder
// failure from selecting request-shape recovery. Gateway failover policy uses
// this result to try another compatible provider for the rejected wire input.
func IsGrokDecoderCompatibilityError(statusCode int, responseBody []byte) bool {
	if statusCode != http.StatusUnprocessableEntity || len(responseBody) == 0 {
		return false
	}
	for _, candidate := range GrokStructuredErrorMessageCandidates(responseBody) {
		message := strings.ToLower(candidate)
		decoderSignal := strings.Contains(message, "untagged enum") ||
			strings.Contains(message, "decode") ||
			strings.Contains(message, "deserialize") ||
			strings.Contains(message, "deserializ") ||
			strings.Contains(message, "decoder")
		inputSignal := strings.Contains(message, "modelinput") ||
			strings.Contains(message, "model input") ||
			strings.Contains(message, "input[") ||
			strings.Contains(message, "input.")
		messageContentSignal := (strings.Contains(message, "messages[") ||
			strings.Contains(message, "messages.")) &&
			strings.Contains(message, "content") &&
			strings.Contains(message, "did not match any variant")
		if decoderSignal && (inputSignal || messageContentSignal) {
			return true
		}
	}
	return false
}

// GrokStructuredErrorMessageCandidates collects nonblank nested and flat error
// message fields in lookup order. Invalid JSON also contributes trimmed
// plaintext. Valid JSON outside those selected fields contributes no plaintext
// fallback, and repeated messages may remain.
func GrokStructuredErrorMessageCandidates(body []byte) []string {
	candidates := make([]string, 0, 6)
	appendCandidate := func(result gjson.Result) {
		if !result.Exists() {
			return
		}
		value := strings.TrimSpace(result.String())
		if value != "" {
			candidates = append(candidates, value)
		}
	}
	appendCandidate(gjson.GetBytes(body, "error.message"))
	appendCandidate(gjson.GetBytes(body, "error.error"))
	errorNode := gjson.GetBytes(body, "error")
	if errorNode.Type == gjson.String {
		appendCandidate(errorNode)
	}
	appendCandidate(gjson.GetBytes(body, "message"))
	appendCandidate(gjson.GetBytes(body, "detail"))
	// Non-JSON bodies can carry upstream error text directly. Retain that text
	// after selected field lookups so recovery classification still has message
	// evidence.
	if !json.Valid(body) {
		if plaintext := strings.TrimSpace(string(body)); plaintext != "" {
			candidates = append(candidates, plaintext)
		}
	}
	return candidates
}

// IsSpendingLimitError recognizes the selected spending-limit code or
// exhausted-credit message text. Flat code precedes nested code; message
// selection prefers error before error.message and message. Empty bodies and
// unmatched selected fields return false.
func IsSpendingLimitError(responseBody []byte) bool {
	if len(responseBody) == 0 {
		return false
	}
	code := strings.ToLower(strings.TrimSpace(firstNonEmpty(
		gjson.GetBytes(responseBody, "code").String(),
		gjson.GetBytes(responseBody, "error.code").String(),
	)))
	if code == "personal-team-blocked:spending-limit" {
		return true
	}
	message := strings.ToLower(strings.TrimSpace(firstNonEmpty(
		gjson.GetBytes(responseBody, "error").String(),
		gjson.GetBytes(responseBody, "error.message").String(),
		gjson.GetBytes(responseBody, "message").String(),
	)))
	return strings.Contains(message, "spending limit") || strings.Contains(message, "run out of credits")
}
