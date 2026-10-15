package httpapi

import "github.com/gin-gonic/gin"

// routes.go registers batch-image submission, discovery, management, and
// download endpoints on the gateway router.
// Batch image management and download paths share the gateway router group's
// middleware. Static model discovery is registered alongside job IDs, and the
// handler applies owner-scoped use cases to submission, queries, cancellation,
// record hiding, and output deletion.

// RegisterGatewayRoutes installs batch image endpoints on the supplied gateway
// group. The caller provides its authentication and protocol middleware and
// a configured handler before serving; downloads and management inherit that
// group's request context.
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
