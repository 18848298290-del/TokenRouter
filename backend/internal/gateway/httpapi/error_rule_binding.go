package httpapi

import (
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

const errorPassthroughServiceContextKey = "error_passthrough_service"

// BindErrorPassthroughService binds the shared error-rule service to this HTTP request.
func BindErrorPassthroughService(c *gin.Context, svc *errorpolicy.ErrorPassthroughService) {
	if c == nil || svc == nil {
		return
	}
	c.Set(errorPassthroughServiceContextKey, svc)
}

// BoundErrorPassthroughService reads the error-rule service bound to this request.
func BoundErrorPassthroughService(c *gin.Context) *errorpolicy.ErrorPassthroughService {
	if c == nil {
		return nil
	}
	v, ok := c.Get(errorPassthroughServiceContextKey)
	if !ok {
		return nil
	}
	svc, ok := v.(*errorpolicy.ErrorPassthroughService)
	if !ok {
		return nil
	}
	return svc
}

// ApplyErrorPassthroughRule applies a matching rule, or returns the supplied defaults when none matches.
func ApplyErrorPassthroughRule(
	c *gin.Context,
	platform string,
	upstreamStatus int,
	responseBody []byte,
	defaultStatus int,
	defaultErrType string,
	defaultErrMsg string,
) (status int, errType string, errMsg string, matched bool) {
	status = defaultStatus
	errType = defaultErrType
	errMsg = defaultErrMsg

	svc := BoundErrorPassthroughService(c)
	if svc == nil {
		return status, errType, errMsg, false
	}

	rule := svc.MatchRule(platform, upstreamStatus, responseBody)
	if rule == nil {
		return status, errType, errMsg, false
	}

	status = upstreamStatus
	if !rule.PassthroughCode && rule.ResponseCode != nil {
		status = *rule.ResponseCode
	}

	errMsg = upstream.ExtractErrorMessage(responseBody)
	if !rule.PassthroughBody && rule.CustomMessage != nil {
		errMsg = *rule.CustomMessage
	}

	// Mark matching skip_monitoring rules so Ops can omit ordinary error logging.
	if rule.SkipMonitoring {
		c.Set(OpsSkipPassthroughKey, true)
	}

	// Matched rules use the upstream_error type, as in failover error responses.
	errType = "upstream_error"
	return status, errType, errMsg, true
}
