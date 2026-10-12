package httpapi

import (
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/gateway"
	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/modeldisplay"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// QoderErrorPresenter combines Qoder error classification, display rules, and catalogue lookup.
type QoderErrorPresenter struct {
	Rules      ErrorRuleMatcher
	Describe   func(error) forward.QoderErrorView
	ReadAccess func(*gin.Context) (*apikey.APIKey, bool)
	Catalogue  modeldisplay.Catalog
}

// Details applies response overrides and monitoring exclusions to recognized upstream errors.
func (p QoderErrorPresenter) Details(c *gin.Context, err error) (int, string, string, bool) {
	value := p.Describe(err)
	status, errType, message, ok := value.Status, value.Kind, value.Message, value.Recognized
	if !ok || p.Rules == nil || value.SourceStatus <= 0 {
		return status, errType, message, ok
	}

	rule := p.Rules.MatchRule(capability.PlatformQoder, value.SourceStatus, []byte(value.Body))
	if rule == nil {
		return status, errType, message, ok
	}

	status = value.SourceStatus
	if !rule.PassthroughCode && rule.ResponseCode != nil {
		status = *rule.ResponseCode
	}
	errType = "upstream_error"
	if !rule.PassthroughBody && rule.CustomMessage != nil {
		message = *rule.CustomMessage
	} else if extracted := upstream.ExtractErrorMessage([]byte(value.Body)); extracted != "" {
		message = extracted
	}
	if rule.SkipMonitoring && c != nil {
		c.Set(OpsSkipPassthroughKey, true)
	}
	return status, errType, message, true
}

// Failure maps execution stages to HTTP errors and Retry-After values.
func (p QoderErrorPresenter) Failure(c *gin.Context, err error) *HTTPFailure {
	if direct, ok := errors.AsType[*HTTPFailure](err); ok {
		return direct
	}
	result := &HTTPFailure{Status: 502, Type: "upstream_error", Message: "Upstream request failed"}
	var failure *gateway.Failure
	if !errors.As(err, &failure) {
		return result
	}
	switch failure.Stage {
	case gateway.FailureBilling:
		result.Status, result.Type, result.Message, result.RetryAfter = BillingErrorDetails(failure.Cause)
	case gateway.FailureUserQueue:
		result.Status = 429
		result.Type = "rate_limit_error"
		result.Message = "Too many pending requests, please retry later"
	case gateway.FailureUserSlot, gateway.FailureProviderSlot:
		if failure.Stage == gateway.FailureProviderSlot && failure.Cause != nil && failure.Cause.Error() == "no available providers" {
			MarkOpsRoutingCapacityLimited(c)
			result.Status = 503
			result.Type = "api_error"
			result.Message = "No available providers"
			return result
		}
		slot := "user"
		if failure.Stage == gateway.FailureProviderSlot {
			slot = "provider"
		}
		result.Status, result.Type, _, result.Message = ConcurrencyErrorResponse(failure.Cause, slot)
	case gateway.FailureRefreshPending:
		result.Status = 503
		result.Message = "Qoder provider refresh is still in progress, please retry shortly"
		result.RetryAfter = 1
	case gateway.FailureSelection:
		MarkOpsRoutingCapacityLimitedIfNoAvailable(c, failure.Cause)
		handled := WriteGroupSelectionBusinessError(c, failure.Cause, c.Writer.Written(), p.ReadAccess, p.Catalogue, func(status int, kind, message string, _ bool) {
			result.Status = status
			result.Type = kind
			result.Message = message
		})
		if !handled {
			result.Status = 503
			result.Type = "api_error"
			result.Message = "No available providers: " + failure.Cause.Error()
		}
	case gateway.FailureUpstream, gateway.FailureExhausted:
		status, kind, message, ok := p.Details(c, failure.Cause)
		if ok {
			result.Status = status
			result.Type = kind
			result.Message = message
			SetOpsUpstreamError(c, p.Describe(failure.Cause).SourceStatus, message, "")
		} else if failure.Stage == gateway.FailureExhausted {
			result.Message = "All available providers exhausted"
		}
	}
	return result
}
