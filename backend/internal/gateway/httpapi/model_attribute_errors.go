package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// model_attribute_errors.go replaces server error details when request-local
// model attribute configuration is unavailable.
// Model attribute lookup failure can surface through several protocol error
// writers. A request-local unavailable marker replaces server-error details
// with a shared configuration-unavailable response, while client errors and
// requests with available attributes keep their existing presentation.

// modelAttributeError replaces server failures after request-local model
// attribute loading becomes unavailable. It returns a safe 503 message for that
// state; lower statuses, missing requests and available configuration retain
// the supplied status and message.
func modelAttributeError(c *gin.Context, status int, message string) (int, string) {
	if status >= 500 && c != nil && c.Request != nil && routing.ModelAttributesFromContext(c.Request.Context()).Unavailable() {
		return http.StatusServiceUnavailable, "Model attribute configuration is temporarily unavailable"
	}
	return status, message
}
