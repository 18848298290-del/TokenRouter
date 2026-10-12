package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/gateway/tierpolicy"
)

// WriteFastPolicyBlockedResponse records a policy denial and writes JSON or SSE according to response state.
func WriteFastPolicyBlockedResponse(c *gin.Context, err *tierpolicy.BlockedError) {
	if c == nil || err == nil {
		return
	}
	MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
	WriteForwardFastPolicyBlocked(c, err.Message, StopOpenAICompactSSEKeepaliveCommitted, func(c *gin.Context, status int, kind, message string) {
		WriteOpenAICompactSSEFailureMessage(c, status, kind, message, MarkOpsStreamError)
	})
}

// WriteForwardAnthropicError writes an Anthropic JSON error.
func WriteForwardAnthropicError(c *gin.Context, statusCode int, errType, message string) {
	c.JSON(statusCode, gin.H{
		"type": "error",
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
}

// WriteForwardAnthropicErrorBody wraps a valid error value in an Anthropic envelope or passes the body through.
func WriteForwardAnthropicErrorBody(c *gin.Context, statusCode int, body []byte) {
	errorObject := gjson.GetBytes(body, "error")
	if !errorObject.Exists() || !gjson.Valid(errorObject.Raw) {
		c.Data(statusCode, "application/json; charset=utf-8", body)
		return
	}
	wrapped := []byte(`{"type":"error","error":` + errorObject.Raw + `}`)
	c.Data(statusCode, "application/json; charset=utf-8", wrapped)
}

// BuildForwardAnthropicStreamError builds an Anthropic SSE error event.
func BuildForwardAnthropicStreamError(errType, message string) string {
	payload, err := json.Marshal(gin.H{
		"type": "error",
		"error": gin.H{
			"type":    strings.TrimSpace(errType),
			"message": strings.TrimSpace(message),
		},
	})
	if err != nil {
		return `event: error` + "\n" + `data: {"type":"error","error":{"type":"invalid_request_error","message":"Request blocked by upstream cyber-security policy"}}` + "\n\n"
	}
	return "event: error\ndata: " + string(payload) + "\n\n"
}

// WriteForwardChatError marks the response committed and writes a Chat JSON error.
func WriteForwardChatError(c *gin.Context, statusCode int, errType, message string) {
	MarkResponseCommitted(c)
	c.JSON(statusCode, gin.H{
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
}

// WriteForwardChatErrorBody marks the response committed and writes a JSON error body.
func WriteForwardChatErrorBody(c *gin.Context, statusCode int, body []byte) {
	MarkResponseCommitted(c)
	c.Data(statusCode, "application/json; charset=utf-8", body)
}

// BuildForwardChatStreamError builds a Chat SSE error event.
func BuildForwardChatStreamError(code, message string) string {
	payload, err := json.Marshal(gin.H{
		"error": gin.H{
			"type":    "invalid_request_error",
			"code":    strings.TrimSpace(code),
			"message": strings.TrimSpace(message),
		},
	})
	if err != nil {
		return `data: {"error":{"type":"invalid_request_error","code":"cyber_policy","message":"Request blocked by upstream cyber-security policy"}}` + "\n\n"
	}
	return "data: " + string(payload) + "\n\n"
}

// WriteForwardResponsesFallbackError writes a Responses JSON error.
func WriteForwardResponsesFallbackError(c *gin.Context, statusCode int, errType, message string) {
	c.JSON(statusCode, gin.H{
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
}

// WriteForwardPassthroughErrorHeaders sets error headers and copies a valid Retry-After value.
func WriteForwardPassthroughErrorHeaders(dst, src http.Header) {
	if dst == nil {
		return
	}
	dst.Set("Content-Type", "application/json; charset=utf-8")
	dst.Set("Cache-Control", "no-store")
	dst.Del("Retry-After")
	if src == nil {
		return
	}
	rawRetryAfter := strings.TrimSpace(src.Get("Retry-After"))
	if ValidForwardPassthroughRetryAfter(rawRetryAfter, time.Now()) {
		dst.Set("Retry-After", rawRetryAfter)
	}
}

// ValidForwardPassthroughRetryAfter accepts positive seconds or a future HTTP date.
func ValidForwardPassthroughRetryAfter(raw string, now time.Time) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	delaySeconds := true
	for i := range len(raw) {
		if raw[i] < '0' || raw[i] > '9' {
			delaySeconds = false
			break
		}
	}
	if delaySeconds {
		seconds, err := strconv.ParseUint(raw, 10, 64)
		return err == nil && seconds > 0
	}
	parsed, err := http.ParseTime(raw)
	return err == nil && parsed.After(now)
}

// WriteSanitizedForwardPassthroughError maps upstream statuses to client messages and maps 401 and 403 to 502.
func WriteSanitizedForwardPassthroughError(c *gin.Context, upstreamStatus int, upstreamHeaders http.Header, compact func(*gin.Context, int, []byte) bool) {
	downstreamStatus := upstreamStatus
	message := "Upstream request failed"
	switch upstreamStatus {
	case http.StatusUnauthorized:
		downstreamStatus = http.StatusBadGateway
		message = "Upstream authentication failed"
	case http.StatusForbidden:
		downstreamStatus = http.StatusBadGateway
		message = "Upstream access denied"
	default:
		if upstreamStatus >= http.StatusInternalServerError {
			message = "Upstream service temporarily unavailable"
		}
	}
	WriteForwardPassthroughErrorEnvelope(c, downstreamStatus, upstreamHeaders, message, compact)
}

// WriteForwardPassthroughErrorEnvelope offers the error to the Compact writer before writing JSON.
func WriteForwardPassthroughErrorEnvelope(c *gin.Context, downstreamStatus int, upstreamHeaders http.Header, message string, compact func(*gin.Context, int, []byte) bool) {
	if c == nil {
		return
	}
	body, _ := json.Marshal(gin.H{
		"error": gin.H{
			"type":    "upstream_error",
			"message": message,
		},
	})
	if compact(c, downstreamStatus, body) {
		return
	}
	WriteForwardPassthroughErrorHeaders(c.Writer.Header(), upstreamHeaders)
	c.Data(downstreamStatus, "application/json; charset=utf-8", body)
}

// WriteForwardFastPolicyBlocked emits a terminal SSE error after a Compact heartbeat commit, or a 403 JSON
// error otherwise.
func WriteForwardFastPolicyBlocked(c *gin.Context, message string, stop func(*gin.Context) bool, failure func(*gin.Context, int, string, string)) {
	if stop(c) {
		failure(c, http.StatusForbidden, "permission_error", message)
		return
	}
	c.JSON(http.StatusForbidden, gin.H{"error": gin.H{"type": "permission_error", "message": message}})
}

// WriteOpenAIForwardRejection writes an OpenAI error with an optional parameter name.
func WriteOpenAIForwardRejection(c *gin.Context, status int, kind, message, param string) {
	if c == nil {
		return
	}
	payload := gin.H{"type": kind, "message": message}
	if param != "" {
		payload["param"] = param
	}
	c.JSON(status, gin.H{"error": payload})
}
