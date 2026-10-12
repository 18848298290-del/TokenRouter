package httpapi

import "github.com/gin-gonic/gin"

// RegisterUserRoutes registers creative endpoints after app authentication, rate limiting, and audit middleware.
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
