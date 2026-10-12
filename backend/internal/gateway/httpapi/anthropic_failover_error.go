package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// WriteAnthropicFailover applies silent rejection, configured error rules, then default status mapping.
func WriteAnthropicFailover(c *gin.Context, failoverErr *forwardcore.UpstreamFailoverError, platform string, streamStarted bool, rules ErrorRuleMatcher, silent func([]byte) bool, silentMessage string) {
	statusCode := failoverErr.StatusCode
	responseBody := failoverErr.ResponseBody
	if silent(responseBody) {
		SetOpsUpstreamError(c, statusCode, silentMessage, "")
		writeAnthropicFailure(c, http.StatusBadGateway, "upstream_error", silentMessage, streamStarted)
		return
	}

	// Configured rules take precedence over the default mapping.
	if rules != nil && len(responseBody) > 0 {
		if rule := rules.MatchRule(platform, statusCode, responseBody); rule != nil {
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

			writeAnthropicFailure(c, respCode, "upstream_error", msg, streamStarted)
			return
		}
	}

	// Record the upstream status and message for Ops error logs.
	upstreamMsg := upstream.ExtractErrorMessage(responseBody)
	SetOpsUpstreamError(c, statusCode, upstreamMsg, "")

	status, errType, errMsg := AnthropicUpstreamError(statusCode)
	writeAnthropicFailure(c, status, errType, errMsg, streamStarted)
}

func AnthropicUpstreamError(statusCode int) (int, string, string) {
	switch statusCode {
	case 401:
		return http.StatusBadGateway, "upstream_error", "Upstream authentication failed, please contact administrator"
	case 403:
		return http.StatusBadGateway, "upstream_error", "Upstream access forbidden, please contact administrator"
	case 429:
		return http.StatusTooManyRequests, "rate_limit_error", "Upstream rate limit exceeded, please retry later"
	case 529:
		return http.StatusServiceUnavailable, "overloaded_error", "Upstream service overloaded, please retry later"
	case 500, 502, 503, 504:
		return http.StatusBadGateway, "upstream_error", "Upstream service temporarily unavailable"
	default:
		return http.StatusBadGateway, "upstream_error", "Upstream request failed"
	}
}

// writeAnthropicFailure writes a terminal stream error and records it for Ops when output has started.
func writeAnthropicFailure(c *gin.Context, status int, kind, message string, started bool) {
	WriteAnthropicStreamError(c, status, kind, "", message, started, MarkOpsStreamError)
}
