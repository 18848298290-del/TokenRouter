package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// RegisterBackupRoutes registers admin routes after app installs authentication, rate limits, and auditing.
func RegisterBackupRoutes(admin *gin.RouterGroup, endpoint *BackupHandler, stepUpAuth gin.HandlerFunc) {
	backup := admin.Group("/backups")
	{
		backup.GET("/storage-config", endpoint.GetStorageConfig)
		// Changing the unified storage configuration can redirect backups to S3 and requires step-up verification.
		backup.PUT("/storage-config", stepUpAuth, endpoint.UpdateStorageConfig)
		backup.POST("/storage-config/test", endpoint.TestStorageConnection)

		backup.GET("/content-config", endpoint.GetContentConfig)
		backup.PUT("/content-config", endpoint.UpdateContentConfig)

		backup.GET("/s3-config", endpoint.GetS3Config)
		// Changing the S3 destination requires step-up verification because it changes where database backups are sent.
		backup.PUT("/s3-config", stepUpAuth, endpoint.UpdateS3Config)
		backup.POST("/s3-config/test", endpoint.TestS3Connection)

		backup.GET("/schedule", endpoint.GetSchedule)
		backup.PUT("/schedule", endpoint.UpdateSchedule)

		backup.POST("", stepUpAuth, endpoint.CreateBackup)
		backup.GET("", endpoint.ListBackups)
		backup.GET("/:id", RequireCanonicalBackupID, endpoint.GetBackup)
		backup.DELETE("/:id", RequireCanonicalBackupID, endpoint.DeleteBackup)
		// Download URLs expose the selected database contents and require step-up verification.
		backup.GET("/:id/download-url", RequireCanonicalBackupID, stepUpAuth, endpoint.GetDownloadURL)
		backup.GET("/:id/download", RequireCanonicalBackupID, stepUpAuth, endpoint.DownloadBackup)

		// Restore can replace security settings, including the step-up toggle, so verify before starting it.
		backup.POST("/:id/restore", RequireCanonicalBackupID, stepUpAuth, endpoint.RestoreBackup)
	}
}

// RegisterDataManagementRoutes registers admin routes after app installs authentication, rate limits, and auditing.
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
		// Changing the S3 destination requires step-up verification because it redirects backups.
		dataManagement.POST("/s3/profiles", stepUpAuth, endpoint.CreateS3Profile)
		dataManagement.PUT("/s3/profiles/:profile_id", stepUpAuth, endpoint.UpdateS3Profile)
		dataManagement.DELETE("/s3/profiles/:profile_id", endpoint.DeleteS3Profile)
		dataManagement.POST("/s3/profiles/:profile_id/activate", stepUpAuth, endpoint.SetActiveS3Profile)
		dataManagement.POST("/backups", stepUpAuth, endpoint.CreateBackupJob)
		dataManagement.GET("/backups", endpoint.ListBackupJobs)
		dataManagement.GET("/backups/:job_id", endpoint.GetBackupJob)
	}
}

func RequireCanonicalBackupID(c *gin.Context) {
	if IsCanonicalBackupID(c.Param("id")) {
		c.Next()
		return
	}
	c.AbortWithStatus(http.StatusNotFound)
}

func IsCanonicalBackupID(value string) bool {
	if len(value) != 8 {
		return false
	}
	for i := range len(value) {
		if (value[i] < '0' || value[i] > '9') && (value[i] < 'a' || value[i] > 'f') {
			return false
		}
	}
	return true
}
