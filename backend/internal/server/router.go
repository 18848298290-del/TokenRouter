package server

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// router.go assembles middleware and module routes on a shared Gin engine.
// Public health and setup-status endpoints remain available alongside module
// routes. App supplies one ordered middleware chain and an optional frontend
// handler, then module registration callbacks add their endpoints to the same
// Gin engine after the common routes.

// RouterRuntime holds app's ordered middleware and module registrations for one engine.
type RouterRuntime struct {
	Middleware []gin.HandlerFunc // global middleware in execution order
	// Frontend is installed after Middleware when nonnil, so SPA serving sees
	// the same request context before common and module routes run.
	Frontend gin.HandlerFunc
	Register []func(*gin.Engine) // module route registration callbacks
}

// SetupRouter installs middleware, optional frontend serving, and all routes.
// Callers provide a nonnil runtime and engine before serving requests; registration
// callbacks run in slice order after the common endpoints have been registered.
func SetupRouter(r *gin.Engine, runtime *RouterRuntime) *gin.Engine {
	r.Use(runtime.Middleware...)
	if runtime.Frontend != nil {
		r.Use(runtime.Frontend)
	}
	RegisterCommonRoutes(r)
	for _, register := range runtime.Register {
		register(r)
	}
	return r
}

// RegisterCommonRoutes adds public health, telemetry acknowledgement, and setup status.
// The setup response describes an already configured process; callers register
// these routes once per engine because Gin rejects duplicate method/path pairs.
func RegisterCommonRoutes(r *gin.Engine) {
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Acknowledge Claude Code telemetry requests without processing their payload.
	r.POST("/api/event_logging/batch", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	// The frontend uses this completed status to detect the restart after setup.
	r.GET("/setup/status", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"code": 0,
			"data": gin.H{
				"needs_setup": false,
				"step":        "completed",
			},
		})
	})
}
