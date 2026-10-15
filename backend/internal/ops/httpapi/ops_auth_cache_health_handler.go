package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// ops_auth_cache_health_handler.go serves administrator authentication-cache
// health observations after checking monitoring availability.
// Authentication cache health combines persisted outbox progress with local
// subscriber, lookup, and invalid-credential observations. The administrator
// endpoint checks monitoring before requesting those signals and returns the
// service's zero-valued sections when a corresponding observer is absent.

// GetAuthCacheInvalidationHealth returns the combined authentication-cache
// diagnostics after checking monitoring is enabled. A missing Ops service
// returns service unavailable; the request context governs the persisted outbox
// health read.
func (h *OpsHandler) GetAuthCacheInvalidationHealth(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, h.opsService.GetAuthCacheInvalidationHealth(c.Request.Context()))
}
