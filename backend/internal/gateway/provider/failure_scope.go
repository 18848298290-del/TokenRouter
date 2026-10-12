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

// IsContentPolicyRejection identifies OpenAI refusals tied to the request content.
// Callers must exclude these from provider-wide error rules, even when stream events imply HTTP 502.
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

// IsRequestScopedProviderFailure identifies failures specific to one request.
// HTTP 413 remains eligible for provider failover because an upstream proxy may impose its own size limit.
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

// IsTransientProviderFailure classifies status codes and processing errors that may recover shortly.
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

// OpenAIWSHTTPBridgeRequestScopedError identifies errors specific to the current request.
// Callers must neither change provider health nor replay the turn under pool retry rules for these errors.
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
