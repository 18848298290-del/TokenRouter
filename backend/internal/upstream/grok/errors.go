package grok

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/tidwall/gjson"
)

// IsGrokContentPolicyRejection recognizes request-level xAI content rejections.
// These prompt or media failures should not consume the OAuth provider pool through retries.
// Entitlement and account bans can also mention policy and require provider failover.
func IsGrokContentPolicyRejection(statusCode int, responseBody []byte) bool {
	if statusCode != http.StatusForbidden || len(responseBody) == 0 {
		return false
	}
	if GrokProviderAccessMessage(string(responseBody)) {
		return false
	}

	var payload any
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

func NormalizeGrokErrorMarker(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", "_")
	value = strings.ReplaceAll(value, " ", "_")
	return value
}

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

func IsGrokProviderAccessCode(value string) bool {
	// permission-denied can also indicate moderation; classify it by message instead.
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

func GrokContentPolicyMessage(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	if lower == "" {
		return false
	}

	// Match specific media moderation phrases to distinguish account and entitlement failures.
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

func GrokContentPolicyClientMessage(message string) string {
	if message == "" {
		return "Request blocked by upstream content policy"
	}
	return message
}

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
	if !json.Valid(body) {
		if plaintext := strings.TrimSpace(string(body)); plaintext != "" {
			candidates = append(candidates, plaintext)
		}
	}
	return candidates
}

// IsSpendingLimitError reports whether the response indicates exhausted spending or credits.
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
