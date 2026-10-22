package httpapi

import (
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/server/adminapi"
)

// routes_settings.go registers combined settings and preaggregation endpoints
// on the administrator route group.
// Administrator settings combine broad configuration GET/PUT with dedicated
// preaggregation controls on the same authenticated route group. The mounting
// caller supplies authentication and audit middleware before these paths are
// registered, while each endpoint retains its own request validation and errors.

// SettingsSettingsEndpoints exposes combined configuration reads and serialized partial updates.
type SettingsSettingsEndpoints interface {
	// GetSettings returns the combined administrator response through c after
	// loading current module settings. The mounting caller supplies administrator
	// authentication and audit middleware on the group that invokes this endpoint.
	GetSettings(*gin.Context)
	// UpdateSettings validates a partial request and coordinates its persistence
	// and runtime application. The authenticated caller receives committed data
	// or an error identifying the failed preparation, write, or application phase.
	UpdateSettings(*gin.Context)
}

// PreAggregationEndpoints exposes normalized controls, full replacement, and
// recent usage backfill admission.
type PreAggregationEndpoints interface {
	// GetPreAggregationSettings returns runtime controls, deployment capabilities,
	// and task observations. Settings resolution can use cached or deployment values
	// when a current storage read fails.
	GetPreAggregationSettings(*gin.Context)
	// UpdatePreAggregationSettings validates the requested cadence and deployment
	// permissions before replacing the unified configuration. The endpoint accepts
	// a complete settings value and reports persistence failures to the administrator.
	UpdatePreAggregationSettings(*gin.Context)
	// BackfillPreAggregation admits a recent usage range after permissions and
	// limits are checked. Successful HTTP acceptance means the task has persisted
	// and been requested; completion is reflected by later task observations.
	BackfillPreAggregation(*gin.Context)
}

// RegisterSettingsSettingsRoutes mounts combined settings and dedicated
// preaggregation handlers on the supplied administrator group. The caller installs
// authentication and audit middleware first and supplies every endpoint dependency
// before requests can reach these routes.
func RegisterSettingsSettingsRoutes(adminSettings *gin.RouterGroup, endpoint SettingsSettingsEndpoints, pre PreAggregationEndpoints) {
	adminapi.Register(adminSettings, ManagementOperations["settings.list"], endpoint.GetSettings)
	adminapi.Register(adminSettings, ManagementOperations["settings.update"], endpoint.UpdateSettings)
	adminapi.Register(adminSettings, ManagementOperations["settings.pre_aggregation.list"], pre.GetPreAggregationSettings)
	adminapi.Register(adminSettings, ManagementOperations["settings.pre_aggregation.update"], pre.UpdatePreAggregationSettings)
	adminapi.Register(adminSettings, ManagementOperations["settings.pre_aggregation.backfill.create"], pre.BackfillPreAggregation)
}
