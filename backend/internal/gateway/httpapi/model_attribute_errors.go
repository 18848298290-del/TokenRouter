package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// modelAttributeError replaces server errors with a configuration-unavailable response when model
// attributes could not load.
func modelAttributeError(c *gin.Context, status int, message string) (int, string) {
	if status >= 500 && c != nil && c.Request != nil && routing.ModelAttributesFromContext(c.Request.Context()).Unavailable() {
		return http.StatusServiceUnavailable, "Model attribute configuration is temporarily unavailable"
	}
	return status, message
}
