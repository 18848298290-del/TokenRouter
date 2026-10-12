package httpapi

import (
	"github.com/gin-gonic/gin"
)

// Proxy backup export includes plaintext credentials and installs the
// application-supplied step-up handler before its endpoint. Proxy
// administration, TLS profile/router management, and collector sessions mount
// under an existing admin group whose authentication, rate limits, and auditing
// are already installed by app wiring.

// RegisterProxyRoutes mounts administration, probes, associations, and backup
// routes under the protected admin group. The application supplies group
// authentication/auditing and a configured step-up handler for credential
// export before calling this registrar.
func RegisterProxyRoutes(admin *gin.RouterGroup, endpoint *ProxyHandler, stepUpAuth gin.HandlerFunc) {
	proxies := admin.Group("/proxies")
	{
		proxies.GET("", endpoint.List)
		proxies.GET("/all", endpoint.GetAll)
		// The backup response includes plaintext passwords. Run the supplied
		// step-up middleware before export so this credential read has its additional admin check.
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

// RegisterTLSFingerprintProfileRoutes mounts profile CRUD and collector
// control/session routes under the protected admin group. The application
// supplies authentication, rate limiting, and auditing before registration,
// while collector availability is handled by the endpoint.
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

// RegisterTLSFingerprintRouterRoutes mounts router CRUD endpoints under the
// existing protected admin group. The application installs authentication, rate
// limiting, and auditing before this registration and supplies the initialized
// router handler.
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
