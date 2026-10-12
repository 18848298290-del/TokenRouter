package server

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// RouterRuntime contains middleware, the frontend handler, and route registration functions supplied by app.
type RouterRuntime struct {
	Middleware []gin.HandlerFunc
	Frontend   gin.HandlerFunc
	Register   []func(*gin.Engine)
}

// SetupRouter installs global middleware, the frontend handler, and routes in that order.
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

// RegisterCommonRoutes registers health, telemetry, and setup status endpoints.
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
