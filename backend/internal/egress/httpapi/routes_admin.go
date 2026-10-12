package httpapi

import (
	"github.com/gin-gonic/gin"
)

// RegisterProxyRoutes mounts proxy routes after app installs group authentication, rate limits, and auditing.
func RegisterProxyRoutes(admin *gin.RouterGroup, endpoint *ProxyHandler, stepUpAuth gin.HandlerFunc) {
	proxies := admin.Group("/proxies")
	{
		proxies.GET("", endpoint.List)
		proxies.GET("/all", endpoint.GetAll)
		// Exports contain plaintext proxy passwords and require step-up authentication.
		proxies.GET("/data", stepUpAuth, endpoint.ExportData)
		proxies.POST("/data", endpoint.ImportData)
		proxies.GET("/:id", endpoint.GetByID)
		proxies.POST("", endpoint.Create)
		proxies.PUT("/:id", endpoint.Update)
		proxies.DELETE("/:id", endpoint.Delete)
		proxies.POST("/:id/test", endpoint.Test)
		proxies.POST("/:id/quality-check", endpoint.CheckQuality)
		proxies.GET("/:id/stats", endpoint.GetStats)
		proxies.GET("/:id/providers", endpoint.GetProxyProviders)
		proxies.POST("/batch-delete", endpoint.BatchDelete)
		proxies.POST("/batch", endpoint.BatchCreate)
	}
}

// RegisterTLSFingerprintProfileRoutes mounts profile and collector routes on the protected admin group.
func RegisterTLSFingerprintProfileRoutes(admin *gin.RouterGroup, endpoint *TLSFingerprintProfileHandler) {
	profiles := admin.Group("/tls-fingerprint-profiles")
	{
		collector := profiles.Group("/collector")
		{
			collector.GET("/status", endpoint.CollectorStatus)
			collector.POST("/start", endpoint.StartCollector)
			collector.POST("/stop", endpoint.StopCollector)
			collector.POST("/sessions", endpoint.CreateCollectorSession)
			collector.GET("/sessions/:token/captures", endpoint.ListCollectorCaptures)
			collector.DELETE("/sessions/:token", endpoint.DeleteCollectorSession)
		}
		profiles.GET("", endpoint.List)
		profiles.GET("/:id", endpoint.GetByID)
		profiles.POST("", endpoint.Create)
		profiles.PUT("/:id", endpoint.Update)
		profiles.DELETE("/:id", endpoint.Delete)
	}
}

// RegisterTLSFingerprintRouterRoutes mounts TLS router routes on the protected admin group.
func RegisterTLSFingerprintRouterRoutes(admin *gin.RouterGroup, endpoint *TLSFingerprintRouterHandler) {
	routers := admin.Group("/tls-fingerprint-routers")
	{
		routers.GET("", endpoint.List)
		routers.GET("/:id", endpoint.GetByID)
		routers.POST("", endpoint.Create)
		routers.PUT("/:id", endpoint.Update)
		routers.DELETE("/:id", endpoint.Delete)
	}
}
