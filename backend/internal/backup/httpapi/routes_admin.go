package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/server/adminapi"
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
		adminapi.Register(backup, ManagementOperations["backups.storage_config.list"], endpoint.GetStorageConfig)
		// Storage changes can redirect exports, so this route applies the supplied step-up policy before accepting new credentials.
		adminapi.Register(backup, ManagementOperations["backups.storage_config.update"], stepUpAuth, endpoint.UpdateStorageConfig)
		adminapi.Register(backup, ManagementOperations["backups.storage_config.test.create"], endpoint.TestStorageConnection)

		adminapi.Register(backup, ManagementOperations["backups.content_config.list"], endpoint.GetContentConfig)
		adminapi.Register(backup, ManagementOperations["backups.content_config.update"], endpoint.UpdateContentConfig)

		adminapi.Register(backup, ManagementOperations["backups.s3_config.list"], endpoint.GetS3Config)
		// Apply the supplied step-up policy before changing the remote destination that receives database archive contents.
		adminapi.Register(backup, ManagementOperations["backups.s3_config.update"], stepUpAuth, endpoint.UpdateS3Config)
		adminapi.Register(backup, ManagementOperations["backups.s3_config.test.create"], endpoint.TestS3Connection)

		adminapi.Register(backup, ManagementOperations["backups.schedule.list"], endpoint.GetSchedule)
		adminapi.Register(backup, ManagementOperations["backups.schedule.update"], endpoint.UpdateSchedule)

		adminapi.Register(backup, ManagementOperations["backups.create"], stepUpAuth, endpoint.CreateBackup)
		adminapi.Register(backup, ManagementOperations["backups.list"], endpoint.ListBackups)
		adminapi.Register(backup, ManagementOperations["backups.get"], RequireCanonicalBackupID, endpoint.GetBackup)
		adminapi.Register(backup, ManagementOperations["backups.delete"], RequireCanonicalBackupID, endpoint.DeleteBackup)
		// Archive downloads expose selected database contents, so both URL generation and streaming apply the supplied step-up policy.
		adminapi.Register(backup, ManagementOperations["backups.download_url.list"], RequireCanonicalBackupID, stepUpAuth, endpoint.GetDownloadURL)
		adminapi.Register(backup, ManagementOperations["backups.download.list"], RequireCanonicalBackupID, stepUpAuth, endpoint.DownloadBackup)

		// Apply the supplied step-up policy before restore reaches its separate current-password verification and asynchronous operation registration.
		adminapi.Register(backup, ManagementOperations["backups.restore.create"], RequireCanonicalBackupID, stepUpAuth, endpoint.RestoreBackup)
	}
}

// RegisterDataManagementRoutes retains retired agent endpoints beneath the protected
// administrator group. Existing step-up checks remain on selected profile and
// archive operations, while handlers validate legacy input before reporting configured agent retirement or absence.
func RegisterDataManagementRoutes(admin *gin.RouterGroup, endpoint *DataManagementHandler, stepUpAuth gin.HandlerFunc) {
	dataManagement := admin.Group("/data-management")
	{
		adminapi.Register(dataManagement, ManagementOperations["data_management.agent.health.list"], endpoint.GetAgentHealth)
		adminapi.Register(dataManagement, ManagementOperations["data_management.config.list"], endpoint.GetConfig)
		adminapi.Register(dataManagement, ManagementOperations["data_management.config.update"], endpoint.UpdateConfig)
		adminapi.Register(dataManagement, ManagementOperations["data_management.sources.profiles.list"], endpoint.ListSourceProfiles)
		adminapi.Register(dataManagement, ManagementOperations["data_management.sources.profiles.create"], endpoint.CreateSourceProfile)
		adminapi.Register(dataManagement, ManagementOperations["data_management.sources.profiles.update"], endpoint.UpdateSourceProfile)
		adminapi.Register(dataManagement, ManagementOperations["data_management.sources.profiles.delete"], endpoint.DeleteSourceProfile)
		adminapi.Register(dataManagement, ManagementOperations["data_management.sources.profiles.activate.create"], endpoint.SetActiveSourceProfile)
		adminapi.Register(dataManagement, ManagementOperations["data_management.s3.test.create"], endpoint.TestS3)
		adminapi.Register(dataManagement, ManagementOperations["data_management.s3.profiles.list"], endpoint.ListS3Profiles)
		// Retired destination changes retain their supplied step-up policy before the legacy handler performs validation and availability reporting.
		adminapi.Register(dataManagement, ManagementOperations["data_management.s3.profiles.create"], stepUpAuth, endpoint.CreateS3Profile)
		adminapi.Register(dataManagement, ManagementOperations["data_management.s3.profiles.update"], stepUpAuth, endpoint.UpdateS3Profile)
		adminapi.Register(dataManagement, ManagementOperations["data_management.s3.profiles.delete"], endpoint.DeleteS3Profile)
		adminapi.Register(dataManagement, ManagementOperations["data_management.s3.profiles.activate.create"], stepUpAuth, endpoint.SetActiveS3Profile)
		adminapi.Register(dataManagement, ManagementOperations["data_management.backups.create"], stepUpAuth, endpoint.CreateBackupJob)
		adminapi.Register(dataManagement, ManagementOperations["data_management.backups.list"], endpoint.ListBackupJobs)
		adminapi.Register(dataManagement, ManagementOperations["data_management.backups.get"], endpoint.GetBackupJob)
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
