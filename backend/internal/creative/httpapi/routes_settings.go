package httpapi

import "github.com/gin-gonic/gin"

// Creative administrator settings expose model candidates and the live worker
// snapshot through separate reads. Registration uses the existing admin
// settings group, so its caller supplies authentication and audit middleware
// before these handlers become reachable.

// CreativeSettingsEndpoints exposes creative model candidates and worker status
// through administration handlers.
type CreativeSettingsEndpoints interface {
	// ListCreativeModelCandidates writes the currently selectable creative group
	// and model candidates to the HTTP response. It uses the caller admin
	// middleware for access and maps service failures through common errors.
	ListCreativeModelCandidates(*gin.Context)
	// GetCreativeWorkerStatus writes the runtime worker usage snapshot for the
	// administration response. The installed caller middleware controls access,
	// and missing runtime readers are returned as configured-service errors.
	GetCreativeWorkerStatus(*gin.Context)
}

// RegisterCreativeSettingsRoutes mounts model-candidate and worker-status
// reads under the supplied administration settings group. Callers install
// authentication and audit middleware on that group before making either
// endpoint reachable.
func RegisterCreativeSettingsRoutes(adminSettings *gin.RouterGroup, endpoint CreativeSettingsEndpoints) {
	adminSettings.GET("/creative-model-candidates", endpoint.ListCreativeModelCandidates)
	adminSettings.GET("/creative-worker-status", endpoint.GetCreativeWorkerStatus)
}
