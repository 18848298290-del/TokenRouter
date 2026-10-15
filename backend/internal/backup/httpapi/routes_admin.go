package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// routes_admin.go registers backup and retired data-management endpoints with
// their administrator authorization checks.
// Archive routes combine the administrator group's existing middleware with
// supplied step-up policy for credential changes, exports, creation, and restore.
// That middleware controls enforcement, including bypass when its global switch is disabled.
// Record-addressed archive routes reject IDs outside the generated eight-byte
// lowercase hexadecimal form before the handler can query or stream their data.

// RegisterBackupRoutes installs archive endpoints beneath the protected administrator
// group. App supplies authentication, rate limiting, and audit middleware beforehand;
// selected routes add step-up checks and canonical record IDs before handler execution.
func RegisterBackupRoutes(admin *gin.RouterGroup, endpoint *BackupHandler, stepUpAuth gin.HandlerFunc) {
	backup := admin.Group("/backups")
	{
		backup.GET("/storage-config", endpoint.GetStorageConfig)
		// Storage changes can redirect exports, so this route applies the supplied step-up policy before accepting new credentials.
		backup.PUT("/storage-config", stepUpAuth, endpoint.UpdateStorageConfig)
		backup.POST("/storage-config/test", endpoint.TestStorageConnection)

		backup.GET("/content-config", endpoint.GetContentConfig)
		backup.PUT("/content-config", endpoint.UpdateContentConfig)

		backup.GET("/s3-config", endpoint.GetS3Config)
		// Apply the supplied step-up policy before changing the remote destination that receives database archive contents.
		backup.PUT("/s3-config", stepUpAuth, endpoint.UpdateS3Config)
		backup.POST("/s3-config/test", endpoint.TestS3Connection)

		backup.GET("/schedule", endpoint.GetSchedule)
		backup.PUT("/schedule", endpoint.UpdateSchedule)

		backup.POST("", stepUpAuth, endpoint.CreateBackup)
		backup.GET("", endpoint.ListBackups)
		backup.GET("/:id", RequireCanonicalBackupID, endpoint.GetBackup)
		backup.DELETE("/:id", RequireCanonicalBackupID, endpoint.DeleteBackup)
		// Archive downloads expose selected database contents, so both URL generation and streaming apply the supplied step-up policy.
		backup.GET("/:id/download-url", RequireCanonicalBackupID, stepUpAuth, endpoint.GetDownloadURL)
		backup.GET("/:id/download", RequireCanonicalBackupID, stepUpAuth, endpoint.DownloadBackup)

		// Apply the supplied step-up policy before restore reaches its separate current-password verification and asynchronous operation registration.
		backup.POST("/:id/restore", RequireCanonicalBackupID, stepUpAuth, endpoint.RestoreBackup)
	}
}

// RegisterDataManagementRoutes retains retired agent endpoints beneath the protected
// administrator group. Existing step-up checks remain on selected profile and
// archive operations, while handlers validate legacy input before reporting configured agent retirement or absence.
func RegisterDataManagementRoutes(admin *gin.RouterGroup, endpoint *DataManagementHandler, stepUpAuth gin.HandlerFunc) {
	dataManagement := admin.Group("/data-management")
	{
		dataManagement.GET("/agent/health", endpoint.GetAgentHealth)
		dataManagement.GET("/config", endpoint.GetConfig)
		dataManagement.PUT("/config", endpoint.UpdateConfig)
		dataManagement.GET("/sources/:source_type/profiles", endpoint.ListSourceProfiles)
		dataManagement.POST("/sources/:source_type/profiles", endpoint.CreateSourceProfile)
		dataManagement.PUT("/sources/:source_type/profiles/:profile_id", endpoint.UpdateSourceProfile)
		dataManagement.DELETE("/sources/:source_type/profiles/:profile_id", endpoint.DeleteSourceProfile)
		dataManagement.POST("/sources/:source_type/profiles/:profile_id/activate", endpoint.SetActiveSourceProfile)
		dataManagement.POST("/s3/test", endpoint.TestS3)
		dataManagement.GET("/s3/profiles", endpoint.ListS3Profiles)
		// Retired destination changes retain their supplied step-up policy before the legacy handler performs validation and availability reporting.
		dataManagement.POST("/s3/profiles", stepUpAuth, endpoint.CreateS3Profile)
		dataManagement.PUT("/s3/profiles/:profile_id", stepUpAuth, endpoint.UpdateS3Profile)
		dataManagement.DELETE("/s3/profiles/:profile_id", endpoint.DeleteS3Profile)
		dataManagement.POST("/s3/profiles/:profile_id/activate", stepUpAuth, endpoint.SetActiveS3Profile)
		dataManagement.POST("/backups", stepUpAuth, endpoint.CreateBackupJob)
		dataManagement.GET("/backups", endpoint.ListBackupJobs)
		dataManagement.GET("/backups/:job_id", endpoint.GetBackupJob)
	}
}

// RequireCanonicalBackupID continues handlers when id matches the generated
// lowercase eight-byte hexadecimal shape. Other values abort with not found,
// preventing registered archive lookup, download, and restore handlers from processing malformed identifiers.
func RequireCanonicalBackupID(c *gin.Context) {
	if IsCanonicalBackupID(c.Param("id")) {
		c.Next()
		return
	}
	c.AbortWithStatus(http.StatusNotFound)
}

// IsCanonicalBackupID reports whether value contains exactly eight lowercase
// hexadecimal ASCII bytes. It does not trim input; uppercase or surrounding
// whitespace is rejected before registered archive handlers consume the route parameter.
func IsCanonicalBackupID(value string) bool {
	if len(value) != 8 {
		// Match the UUID prefix length used when the service creates record IDs.
		return false
	}
	for i := range len(value) {
		if (value[i] < '0' || value[i] > '9') && (value[i] < 'a' || value[i] > 'f') {
			return false
		}
	}
	return true
}
