package httpapi

import (
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/server/adminapi"
)

// routes_admin.go registers proxy, TLS fingerprint, and collector routes under
// the administrator group.
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
		adminapi.Register(proxies, ManagementOperations["proxies.list"], endpoint.List)
		adminapi.Register(proxies, ManagementOperations["proxies.all.list"], endpoint.GetAll)
		// The backup response includes plaintext passwords. Run the supplied
		// step-up middleware before export so this credential read has its additional admin check.
		adminapi.Register(proxies, ManagementOperations["proxies.data.list"], stepUpAuth, endpoint.ExportData)
		adminapi.Register(proxies, ManagementOperations["proxies.data.create"], endpoint.ImportData)
		adminapi.Register(proxies, ManagementOperations["proxies.get"], endpoint.GetByID)
		adminapi.Register(proxies, ManagementOperations["proxies.create"], endpoint.Create)
		adminapi.Register(proxies, ManagementOperations["proxies.update"], endpoint.Update)
		adminapi.Register(proxies, ManagementOperations["proxies.delete"], endpoint.Delete)
		adminapi.Register(proxies, ManagementOperations["proxies.test.create"], endpoint.Test)
		adminapi.Register(proxies, ManagementOperations["proxies.quality_check.create"], endpoint.CheckQuality)
		adminapi.Register(proxies, ManagementOperations["proxies.stats.list"], endpoint.GetStats)
		adminapi.Register(proxies, ManagementOperations["proxies.providers.list"], endpoint.GetProxyProviders)
		adminapi.Register(proxies, ManagementOperations["proxies.batch_delete.create"], endpoint.BatchDelete)
		adminapi.Register(proxies, ManagementOperations["proxies.batch.create"], endpoint.BatchCreate)
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
			adminapi.Register(collector, ManagementOperations["tls_fingerprint_profiles.collector.status.list"], endpoint.CollectorStatus)
			adminapi.Register(collector, ManagementOperations["tls_fingerprint_profiles.collector.start.create"], endpoint.StartCollector)
			adminapi.Register(collector, ManagementOperations["tls_fingerprint_profiles.collector.stop.create"], endpoint.StopCollector)
			adminapi.Register(collector, ManagementOperations["tls_fingerprint_profiles.collector.sessions.create"], endpoint.CreateCollectorSession)
			adminapi.Register(collector, ManagementOperations["tls_fingerprint_profiles.collector.sessions.captures.list"], endpoint.ListCollectorCaptures)
			adminapi.Register(collector, ManagementOperations["tls_fingerprint_profiles.collector.sessions.delete"], endpoint.DeleteCollectorSession)
		}
		adminapi.Register(profiles, ManagementOperations["tls_fingerprint_profiles.list"], endpoint.List)
		adminapi.Register(profiles, ManagementOperations["tls_fingerprint_profiles.get"], endpoint.GetByID)
		adminapi.Register(profiles, ManagementOperations["tls_fingerprint_profiles.create"], endpoint.Create)
		adminapi.Register(profiles, ManagementOperations["tls_fingerprint_profiles.update"], endpoint.Update)
		adminapi.Register(profiles, ManagementOperations["tls_fingerprint_profiles.delete"], endpoint.Delete)
	}
}

// RegisterTLSFingerprintRouterRoutes mounts router CRUD endpoints under the
// existing protected admin group. The application installs authentication, rate
// limiting, and auditing before this registration and supplies the initialized
// router handler.
func RegisterTLSFingerprintRouterRoutes(admin *gin.RouterGroup, endpoint *TLSFingerprintRouterHandler) {
	routers := admin.Group("/tls-fingerprint-routers")
	{
		adminapi.Register(routers, ManagementOperations["tls_fingerprint_routers.list"], endpoint.List)
		adminapi.Register(routers, ManagementOperations["tls_fingerprint_routers.get"], endpoint.GetByID)
		adminapi.Register(routers, ManagementOperations["tls_fingerprint_routers.create"], endpoint.Create)
		adminapi.Register(routers, ManagementOperations["tls_fingerprint_routers.update"], endpoint.Update)
		adminapi.Register(routers, ManagementOperations["tls_fingerprint_routers.delete"], endpoint.Delete)
	}
}
