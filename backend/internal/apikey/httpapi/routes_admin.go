package httpapi

import (
	"github.com/gin-gonic/gin"
)

// routes_admin.go registers the administrative Key-update route for binding
// changes and optional spending-window resets. Route registration uses the
// caller's already configured admin group so its authentication, rate controls
// and auditing apply before the supplied handler runs.

// RegisterAdminAPIKeyRoutes binds the administrative Key update handler below
// the supplied admin group. Callers install authentication, rate controls and
// auditing on that group before registration; the handler receives its
// dependencies from assembly.
func RegisterAdminAPIKeyRoutes[G any](admin *gin.RouterGroup, endpoint *AdminAPIKeyHandler[G]) {
	apiKeys := admin.Group("/api-keys")
	{
		apiKeys.PUT("/:id", endpoint.UpdateGroup)
	}
}
