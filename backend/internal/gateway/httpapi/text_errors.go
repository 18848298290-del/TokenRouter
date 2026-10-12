package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

func WriteAnthropicStreamError(c *gin.Context, status int, errType, code, message string, streamStarted bool, observe func(*gin.Context, string, string, int)) {
	if streamStarted {
		// A flushed keepalive or data frame commits the status, so subsequent errors use SSE.
		// Record stream errors so Ops can account for failures whose HTTP status is below 400.
		if observe != nil {
			observe(c, errType, message, status)
		}

		// Responses clients require a terminal response event even when the upstream uses Anthropic.
		if InboundIsResponses(c) {
			if WriteResponsesFailedSSE(c, errType, code, message, ErrorRequestID(c), ErrorRequestModel(c)) {
				return
			}
		}
		// Flush the protocol error before returning from the started stream.
		flusher, ok := c.Writer.(http.Flusher)
		if ok {
			// Quote variable strings when constructing the fixed SSE error schema.
			errorEvent := `data: {"type":"error","error":{"type":` + strconv.Quote(errType) + `,"message":` + strconv.Quote(message) + `}}` + "\n\n"
			if code != "" {
				errorObject := gin.H{"type": errType, "code": code, "message": message}
				payload, err := json.Marshal(gin.H{"type": "error", "error": errorObject})
				if err == nil {
					errorEvent = "data: " + string(payload) + "\n\n"
				}
			}
			if _, err := fmt.Fprint(c.Writer, errorEvent); err != nil {
				_ = c.Error(err)
			}
			flusher.Flush()
		}
		return
	}

	// Before streaming starts, return JSON with the requested error status.
	if code == "" {
		WriteAnthropicError(c, status, errType, "", message)
	} else {
		WriteAnthropicError(c, status, errType, code, message)
	}
}

func WriteAnthropicError(c *gin.Context, status int, errType, code, message string) {
	errorObject := gin.H{"type": errType, "message": message}
	if code != "" {
		errorObject["code"] = code
	}
	c.JSON(status, gin.H{
		"type":  "error",
		"error": errorObject,
	})
}

func BillingErrorDetails(err error) (status int, code, message string, retryAfter int) {
	if errors.Is(err, billing.ErrBillingServiceUnavailable) {
		msg := apperror.Message(err)
		if msg == "" {
			msg = "Billing service temporarily unavailable. Please retry later."
		}
		return http.StatusServiceUnavailable, "billing_service_error", msg, 0
	}
	if errors.Is(err, billing.ErrAPIKeyRateLimit5hExceeded) {
		msg := apperror.Message(err)
		return http.StatusTooManyRequests, "rate_limit_exceeded", msg, 0
	}
	if errors.Is(err, billing.ErrAPIKeyRateLimit1dExceeded) {
		msg := apperror.Message(err)
		return http.StatusTooManyRequests, "rate_limit_exceeded", msg, 0
	}
	if errors.Is(err, billing.ErrAPIKeyRateLimit7dExceeded) {
		msg := apperror.Message(err)
		return http.StatusTooManyRequests, "rate_limit_exceeded", msg, 0
	}
	// User and group RPM limits return 429 with the shared rate_limit_exceeded code.
	// Retry-After is the number of seconds remaining in the current minute.
	if errors.Is(err, scheduler.ErrGroupRPMExceeded) || errors.Is(err, scheduler.ErrUserRPMExceeded) {
		msg := apperror.Message(err)
		retrySeconds := 60 - int(time.Now().Unix()%60)
		return http.StatusTooManyRequests, "rate_limit_exceeded", msg, retrySeconds
	}

	msg := apperror.Message(err)
	if msg == "" {
		logging.L().With(
			zap.String("component", "handler.gateway.billing"),
			zap.Error(err),
		).Warn("gateway.billing_error_missing_message")
		msg = "Billing error"
	}
	return http.StatusForbidden, "billing_error", msg, 0
}

// ErrorRequestID reads the telemetry correlation ID from the request context.
func ErrorRequestID(c *gin.Context) string {
	if c != nil && c.Request != nil {
		value, _ := c.Request.Context().Value(telemetry.RequestID).(string)
		return value
	}
	return ""
}

func ErrorRequestModel(c *gin.Context) string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.GetString("ops_model"))
}
