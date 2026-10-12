package httpapi

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// AnthropicForwardErrorOutput writes Messages errors. Callers manage commit and stream state.
type AnthropicForwardErrorOutput struct{ Context *gin.Context }

// MessagesErrorOutput writes protocol errors using shared passthrough rules.
type MessagesErrorOutput struct {
	Rules *errorpolicy.ErrorPassthroughService
}

func (o AnthropicForwardErrorOutput) Message(status int, kind, message string) {
	o.Context.JSON(status, gin.H{"type": "error", "error": gin.H{"type": kind, "message": message}})
}

func (o AnthropicForwardErrorOutput) Raw(status int, body []byte) {
	o.Context.Data(status, "application/json", body)
}

// ConcurrencyError writes a slot acquisition failure in the appropriate response format.
func (h MessagesErrorOutput) ConcurrencyError(c *gin.Context, err error, slotType string, streamStarted bool) {
	status, errType, code, message := ConcurrencyErrorResponse(err, slotType)
	h.StreamErrorWithCode(c, status, errType, code, message, streamStarted)
}

// ExhaustedStatus maps an upstream status when no error body is available.
func (h MessagesErrorOutput) ExhaustedStatus(c *gin.Context, statusCode int, streamStarted bool) {
	status, errType, errMsg := AnthropicUpstreamError(statusCode)
	SetOpsUpstreamError(c, statusCode, errMsg, "")
	h.StreamError(c, status, errType, errMsg, streamStarted)
}

// StreamError handles errors that may occur after streaming has started.
func (h MessagesErrorOutput) StreamError(c *gin.Context, status int, errType, message string, streamStarted bool) {
	h.StreamErrorWithCode(c, status, errType, "", message, streamStarted)
}

// StreamErrorWithCode writes an Anthropic error, accounting for stream state and unavailable model attributes.
func (h MessagesErrorOutput) StreamErrorWithCode(c *gin.Context, status int, errType, code, message string, streamStarted bool) {
	status, message = modelAttributeError(c, status, message)
	WriteAnthropicStreamError(c, status, errType, code, message, streamStarted, MarkOpsStreamError)
}

// EnsureResponse writes a generic error when no final response has been committed.
// If headers were flushed by a heartbeat, it writes an SSE terminal error
// so the client receives a failure event instead of a silent EOF.
func (h MessagesErrorOutput) EnsureResponse(c *gin.Context, streamStarted bool) bool {
	if c == nil || c.Writer == nil {
		return false
	}
	if IsResponseCommitted(c) {
		return false
	}
	if c.Writer.Written() {
		streamStarted = true
	}
	h.StreamError(c, http.StatusBadGateway, "upstream_error", "Upstream request failed", streamStarted)
	return true
}

// ForwardErrorAlreadyCommunicated reports whether an error accompanied new non-SSE response bytes.
// SSE heartbeats and partial data still require a terminal error event.
func ForwardErrorAlreadyCommunicated(c *gin.Context, writerSizeBeforeForward int, err error) bool {
	if err == nil || c == nil || c.Writer == nil {
		return false
	}
	if c.Writer.Size() == writerSizeBeforeForward {
		return false
	}

	contentType := strings.ToLower(strings.TrimSpace(c.Writer.Header().Get("Content-Type")))
	if contentType == "" {
		return false
	}
	return !strings.Contains(contentType, "text/event-stream")
}

// Error writes a Claude-compatible error response.
func (h MessagesErrorOutput) Error(c *gin.Context, status int, errType, message string) {
	h.ErrorWithCode(c, status, errType, "", message)
}

// ErrorWithCode writes an Anthropic JSON error with an optional code.
func (h MessagesErrorOutput) ErrorWithCode(c *gin.Context, status int, errType, code, message string) {
	WriteAnthropicError(c, status, errType, code, message)
}

// Exhausted applies error presentation rules to a classified failover failure.
func (h MessagesErrorOutput) Exhausted(c *gin.Context, failure *forwardcore.UpstreamFailoverError, platform string, started bool) {
	var rules ErrorRuleMatcher
	if h.Rules != nil {
		rules = h.Rules
	}
	WriteAnthropicFailover(c, failure, platform, started, rules, forwardcore.IsOpenAISilentRefusalErrorBody, forwardcore.OpenAISilentRefusalClientMessage())
}

// ResponsesError writes an error in OpenAI Responses API format.
func (h MessagesErrorOutput) ResponsesError(c *gin.Context, status int, code, message string) {
	WriteCompatibleResponsesError(c, status, code, message)
}

// ResponsesExhausted writes a failover-exhausted error in Responses format.
func (h MessagesErrorOutput) ResponsesExhausted(c *gin.Context, lastErr *forwardcore.UpstreamFailoverError, streamStarted bool) {
	if lastErr != nil {
		CopyFailoverRetryAfter(c, lastErr.ResponseHeaders)
	}
	statusCode := http.StatusBadGateway
	if lastErr != nil && lastErr.StatusCode > 0 {
		statusCode = lastErr.StatusCode
	}
	status, code, message := statusCode, "server_error", "All available providers exhausted"
	if lastErr != nil && lastErr.IsCredentialFailure() {
		status, message = CredentialFailoverClientResponse(lastErr)
	} else if lastErr != nil && gatewayprovider.IsOpenAICapacityShed(lastErr) && strings.TrimSpace(lastErr.ClientMessage) != "" {
		status = lastErr.ClientStatusCode
		if status <= 0 {
			status = http.StatusServiceUnavailable
		}
		message = lastErr.ClientMessage
	} else if lastErr != nil && forwardcore.IsOpenAISilentRefusalErrorBody(lastErr.ResponseBody) {
		SetOpsUpstreamError(c, statusCode, forwardcore.OpenAISilentRefusalClientMessage(), "")
		status, code, message = http.StatusBadGateway, "upstream_error", forwardcore.OpenAISilentRefusalClientMessage()
	} else if lastErr != nil && statusCode == http.StatusTooManyRequests {
		status, code, message = http.StatusTooManyRequests, "rate_limit_error", "All available providers are currently rate-limited. Please retry later."
	}
	if streamStarted {
		// A slot-wait heartbeat commits HTTP 200 before any upstream response.
		// In that case a terminal frame is still required; once any semantic or
		// official terminal bytes exist, preserve them without appending a second
		// generic response.failed.
		MarkOpsStreamError(c, code, message, status)
		if c != nil && c.Writer != nil && (c.Writer.Size() <= 0 || StreamHasOnlyHeartbeats(c)) {
			WriteResponsesFailedSSE(c, code, "", message, ErrorRequestID(c), ErrorRequestModel(c))
		}
		return
	}
	h.ResponsesError(c, status, code, message)
}

// ChatError writes an error in OpenAI Chat Completions format.
func (h MessagesErrorOutput) ChatError(c *gin.Context, status int, errType, message string) {
	WriteCompatibleChatError(c, status, errType, message)
}

// ChatExhausted writes a failover-exhausted error in Chat Completions format.
func (h MessagesErrorOutput) ChatExhausted(c *gin.Context, lastErr *forwardcore.UpstreamFailoverError, streamStarted bool) {
	if streamStarted {
		return
	}
	if lastErr != nil {
		CopyFailoverRetryAfter(c, lastErr.ResponseHeaders)
	}
	if lastErr != nil && lastErr.IsCredentialFailure() {
		status, message := CredentialFailoverClientResponse(lastErr)
		h.ChatError(c, status, "server_error", message)
		return
	}
	if lastErr != nil && gatewayprovider.IsOpenAICapacityShed(lastErr) && strings.TrimSpace(lastErr.ClientMessage) != "" {
		status := lastErr.ClientStatusCode
		if status <= 0 {
			status = http.StatusServiceUnavailable
		}
		h.ChatError(c, status, "server_error", lastErr.ClientMessage)
		return
	}
	statusCode := http.StatusBadGateway
	if lastErr != nil && lastErr.StatusCode > 0 {
		statusCode = lastErr.StatusCode
	}
	if lastErr != nil && forwardcore.IsOpenAISilentRefusalErrorBody(lastErr.ResponseBody) {
		SetOpsUpstreamError(c, statusCode, forwardcore.OpenAISilentRefusalClientMessage(), "")
		h.ChatError(c, http.StatusBadGateway, "upstream_error", forwardcore.OpenAISilentRefusalClientMessage())
		return
	}
	h.ChatError(c, statusCode, "server_error", "All available providers exhausted")
}

func (h MessagesErrorOutput) GeminiExhausted(c *gin.Context, failoverErr *forwardcore.UpstreamFailoverError) {
	if failoverErr == nil {
		WriteGoogleError(c, http.StatusBadGateway, "Upstream request failed")
		return
	}

	statusCode := failoverErr.StatusCode
	responseBody := failoverErr.ResponseBody

	// Configured passthrough rules take precedence over default mappings.
	if h.Rules != nil && len(responseBody) > 0 {
		if rule := h.Rules.MatchRule(capability.PlatformGemini, statusCode, responseBody); rule != nil {
			respCode := statusCode
			if !rule.PassthroughCode && rule.ResponseCode != nil {
				respCode = *rule.ResponseCode
			}

			msg := upstream.ExtractErrorMessage(responseBody)
			if !rule.PassthroughBody && rule.CustomMessage != nil {
				msg = *rule.CustomMessage
			}

			if rule.SkipMonitoring {
				c.Set(OpsSkipPassthroughKey, true)
			}

			WriteGoogleError(c, respCode, msg)
			return
		}
	}

	// Record the upstream status and message for Ops error logs.
	upstreamMsg := upstream.ExtractErrorMessage(responseBody)
	SetOpsUpstreamError(c, statusCode, upstreamMsg, "")

	status, message := GeminiUpstreamError(statusCode)
	WriteGoogleError(c, status, message)
}

func GeminiUpstreamError(statusCode int) (int, string) {
	switch statusCode {
	case 401:
		return http.StatusBadGateway, "Upstream authentication failed, please contact administrator"
	case 403:
		return http.StatusBadGateway, "Upstream access forbidden, please contact administrator"
	case 429:
		return http.StatusTooManyRequests, "Upstream rate limit exceeded, please retry later"
	case 529:
		return http.StatusServiceUnavailable, "Upstream service overloaded, please retry later"
	case 500, 502, 503, 504:
		return http.StatusBadGateway, "Upstream service temporarily unavailable"
	default:
		return http.StatusBadGateway, "Upstream request failed"
	}
}
