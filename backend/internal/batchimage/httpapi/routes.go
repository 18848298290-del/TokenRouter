package httpapi

import "github.com/gin-gonic/gin"

// RegisterGatewayRoutes registers batch image paths behind the gateway key and protocol checks.
func RegisterGatewayRoutes(gateway *gin.RouterGroup, endpoint *BatchImageHandler) {
	gateway.POST("/images/batches", endpoint.Submit)
	gateway.GET("/images/batches", endpoint.List)
	gateway.GET("/images/batches/models", endpoint.Models)
	gateway.GET("/images/batches/:id", endpoint.Get)
	gateway.GET("/images/batches/:id/items", endpoint.Items)
	gateway.GET("/images/batches/:id/items/:custom_id/content", endpoint.ItemContent)
	gateway.GET("/images/batches/:id/download", endpoint.Download)
	gateway.POST("/images/batches/:id/cancel", endpoint.Cancel)
	gateway.DELETE("/images/batches/:id", endpoint.DeleteRecord)
	gateway.DELETE("/images/batches/:id/outputs", endpoint.DeleteOutputs)
}
