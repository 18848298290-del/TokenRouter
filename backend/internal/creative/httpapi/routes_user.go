package httpapi

import "github.com/gin-gonic/gin"

// Creative tasks are exposed under the authenticated dashboard route group.
// Creation also passes through the supplied heavy-request limiter, while model
// reads, task queries, binary output delivery, and acknowledgment use their
// individual handler scope checks.

// RegisterUserRoutes mounts creative endpoints under a group with
// caller-installed authentication and audit middleware. The supplied heavy
// handler precedes CreateRun alone; callers configure the endpoint service
// before registering these routes.
func RegisterUserRoutes(authenticated *gin.RouterGroup, endpoint *CreativeHandler, heavy gin.HandlerFunc) {
	creative := authenticated.Group("/creative")
	{
		creative.GET("/capabilities", endpoint.ListCapabilities)
		creative.GET("/models", endpoint.ListModels)
		creative.POST("/runs", heavy, endpoint.CreateRun)
		creative.GET("/runs/active", endpoint.ListActiveRuns)
		creative.GET("/runs", endpoint.ListRuns)
		creative.GET("/runs/:id", endpoint.GetRun)
		creative.GET("/runs/:id/outputs/:index/content", endpoint.GetOutputContent)
		creative.POST("/runs/:id/outputs/:index/ack", endpoint.AckOutput)
	}
}
