package provider

import (
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
	upstreamopenai "github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// Request-content and request-shape failures need different treatment from
// provider outages during health observation and replay. Classifiers combine
// structured OpenAI markers with message matching and Grok content-policy
// rules, while transient status recognition identifies errors eligible for
// short-lived recovery policy.

// IsContentPolicyRejection recognizes OpenAI content refusals from nested
// error markers and selected message phrases. Empty bodies return false;
// callers exclude matching failures from provider-wide error rules even when a
// stream wrapper supplies HTTP 502.
func IsContentPolicyRejection(responseBody []byte) bool {
	if len(responseBody) == 0 {
		return false
	}
	for _, path := range []string{
		"error.code",
		"error.type",
		"response.error.code",
		"response.error.type",
	} {
		marker := strings.ToLower(strings.TrimSpace(gjson.GetBytes(responseBody, path).String()))
		if strings.Contains(marker, "content_policy") ||
			strings.Contains(marker, "content_filter") ||
			strings.Contains(marker, "safety") ||
			strings.Contains(marker, "moderation") {
			return true
		}
	}
	for _, path := range []string{"error.message", "response.error.message", "message"} {
		message := strings.ToLower(strings.TrimSpace(gjson.GetBytes(responseBody, path).String()))
		if strings.Contains(message, "content policy") ||
			strings.Contains(message, "blocked by policy") ||
			strings.Contains(message, "safety system") ||
			strings.Contains(message, "violates our policies") {
			return true
		}
	}
	return false
}

// IsRequestScopedProviderFailure identifies content, cyber, invalid-request,
// and context-window errors whose scope is this request. Grok targets also use
// Grok content-policy classification; HTTP 413 alone remains eligible for
// provider failover because a proxy may impose its own limit.
func IsRequestScopedProviderFailure(provider *ExecutionProvider, statusCode int, responseBody []byte) bool {
	upstreamMsg := strings.TrimSpace(upstream.ExtractErrorMessage(responseBody))
	if hit, _, _ := upstreamopenai.DetectOpenAICyberPolicy(responseBody); hit {
		return true
	}
	if IsOpenAICyberWarningPayload(responseBody, upstreamMsg) ||
		IsContentPolicyRejection(responseBody) ||
		upstreamopenai.IsOpenAIClientInvalidRequestError(statusCode, upstreamMsg, responseBody) ||
		upstreamopenai.IsOpenAIContextWindowError(upstreamMsg, responseBody) {
		return true
	}
	return provider != nil && provider.Record.Platform == capability.PlatformGrok && grok.IsGrokContentPolicyRejection(statusCode, responseBody)
}

// IsTransientProviderFailure recognizes server and proxy failure statuses,
// plus OpenAI transient-processing errors reported as HTTP 400. Other statuses
// return false, leaving caller policy to decide how classification affects
// cooldown or replay.
func IsTransientProviderFailure(statusCode int, responseBody []byte) bool {
	switch statusCode {
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 520, 521, 522, 523, 524:
		return true
	case http.StatusBadRequest:
		return upstreamopenai.IsOpenAITransientProcessingError(statusCode, "", responseBody)
	default:
		return false
	}
}

// OpenAIWSHTTPBridgeRequestScopedError recognizes cyber, warning,
// client-input, context-window, and Grok content-policy failures for a bridge turn.
// Callers exclude matches from provider health changes and pool retries
// because replay under another connection cannot resolve that request
// condition.
func OpenAIWSHTTPBridgeRequestScopedError(provider *ExecutionProvider, statusCode int, message string, body []byte) bool {
	if hit, _, _ := upstreamopenai.DetectOpenAICyberPolicy(body); hit {
		return true
	}
	if IsOpenAICyberWarningPayload(body, message) ||
		upstreamopenai.IsOpenAIClientInvalidRequestError(statusCode, message, body) ||
		upstreamopenai.IsOpenAIContextWindowError(message, body) {
		return true
	}
	return provider != nil && provider.Record.Platform == capability.PlatformGrok && grok.IsGrokContentPolicyRejection(statusCode, body)
}
