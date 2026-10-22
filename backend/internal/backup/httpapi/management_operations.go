package httpapi

import (
	"github.com/TokenFlux/TokenRouter/internal/backup"
	"github.com/TokenFlux/TokenRouter/internal/server/adminapi"
)

// management_operations.go describes registered administrative HTTP operations for
// MCP discovery and permission selection. Request schemas reference the same input
// types decoded by handlers; metadata records query arguments, side effects, and
// browser confirmation requirements before the shared executor admits a call.

// ManagementOperations supplies stable permission identities and parameter descriptions
// for this module. Registrars mount these descriptions together with existing HTTP handlers.
var ManagementOperations = map[string]*adminapi.Operation{
	"backups.content_config.list": adminapi.Define(adminapi.Operation{
		ID:     "backups.content_config.list",
		Module: "backups",
		Title:  "Get Content Config",
		Description: "GetContentConfig returns optional backup data selections and computed table exclusions from " +
			"the service. Missing or failed reads use disabled-group defaults; malformed saved JSON uses " +
			"shared error mapping before a response is written. This operation is available through " +
			"read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/backups/content-config",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/backups",
		Input:       adminapi.Arguments("/api/v1/admin/backups/content-config", []string{}, nil),
	}),
	"backups.content_config.update": adminapi.Define(adminapi.Operation{
		ID:     "backups.content_config.update",
		Module: "backups",
		Title:  "Update Content Config",
		Description: "UpdateContentConfig binds the administrator's optional data-group selections and returns the " +
			"saved view with computed exclusions. Binding and service errors are mapped before success; " +
			"the service discards client exclusion lists and recomputes them from its group rules. " +
			"Omitted body fields follow the HTTP handler defaults; explicit null and empty collections " +
			"retain their documented meanings. Browser confirmation by the token owner is required.",
		Method:      "PUT",
		Path:        "/api/v1/admin/backups/content-config",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/backups",
		Input:       adminapi.Arguments("/api/v1/admin/backups/content-config", []string{}, (*backup.BackupContentConfig)(nil)),
	}),
	"backups.create": adminapi.Define(adminapi.Operation{
		ID:     "backups.create",
		Module: "backups",
		Title:  "Create Backup",
		Description: "CreateBackup launches a tracked manual archive and returns its initial record with accepted " +
			"status. Binding errors are ignored, so the resulting ExpireDays pointer determines policy: " +
			"nil chooses fourteen days and supplied nonpositive values leave the archive without " +
			"expiration. Later operation results are queried by record ID. Omitted body fields follow the " +
			"HTTP handler defaults; explicit null and empty collections retain their documented meanings. " +
			"Browser confirmation by the token owner is required.",
		Method:      "POST",
		Path:        "/api/v1/admin/backups",
		Sensitive:   true,
		StepUp:      true,
		Response:    "json",
		BrowserPath: "/admin/backups",
		Input:       adminapi.Arguments("/api/v1/admin/backups", []string{}, (*CreateBackupRequest)(nil)),
	}),
	"backups.delete": adminapi.Define(adminapi.Operation{
		ID:     "backups.delete",
		Module: "backups",
		Title:  "Delete Backup",
		Description: "DeleteBackup removes the selected record through service-managed object cleanup. A nonempty " +
			"ID is required here and canonical validation belongs to registered routes; service errors " +
			"are returned before the deleted=true success response is written. Browser confirmation by " +
			"the token owner is required.",
		Method:      "DELETE",
		Path:        "/api/v1/admin/backups/:id",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/backups",
		Input:       adminapi.Arguments("/api/v1/admin/backups/:id", []string{}, nil),
	}),
	"backups.download.list": adminapi.Define(adminapi.Operation{
		ID:     "backups.download.list",
		Module: "backups",
		Title:  "Download Backup",
		Description: "DownloadBackup opens a completed single-file archive and streams its recorded byte length as " +
			"gzip content, closing the reader on return. Missing filenames use the ID plus sql.gz suffix, " +
			"and service validation errors are returned before streaming begins. This operation is " +
			"available through read_operation. Browser confirmation by the token owner is required.",
		Method:      "GET",
		Path:        "/api/v1/admin/backups/:id/download",
		ReadOnly:    true,
		Sensitive:   true,
		StepUp:      true,
		Response:    "file",
		BrowserPath: "/admin/backups",
		Input:       adminapi.Arguments("/api/v1/admin/backups/:id/download", []string{}, nil),
	}),
	"backups.download_url.list": adminapi.Define(adminapi.Operation{
		ID:     "backups.download_url.list",
		Module: "backups",
		Title:  "Get Download URL",
		Description: "GetDownloadURL returns the application download path for matching single-file local " +
			"metadata, or delegates signed single/part URLs to the service. The local path response " +
			"itself does not check completion; opening that download and the service's signed-URL path " +
			"perform their respective completed-record checks. This operation is available through " +
			"read_operation. Browser confirmation by the token owner is required.",
		Method:      "GET",
		Path:        "/api/v1/admin/backups/:id/download-url",
		ReadOnly:    true,
		Sensitive:   true,
		StepUp:      true,
		Response:    "file",
		BrowserPath: "/admin/backups",
		Input:       adminapi.Arguments("/api/v1/admin/backups/:id/download-url", []string{}, nil),
	}),
	"backups.get": adminapi.Define(adminapi.Operation{
		ID:     "backups.get",
		Module: "backups",
		Title:  "Get Backup",
		Description: "GetBackup returns one stored archive record for the nonempty route identifier. Registered " +
			"routes enforce canonical IDs before this handler; direct fixtures can call it with other " +
			"nonempty values and receive the service's lookup result. This operation is available through " +
			"read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/backups/:id",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/backups",
		Input:       adminapi.Arguments("/api/v1/admin/backups/:id", []string{}, nil),
	}),
	"backups.list": adminapi.Define(adminapi.Operation{
		ID:     "backups.list",
		Module: "backups",
		Title:  "List Backups",
		Description: "ListBackups returns the service's ordered archive records under the items key. A nil record " +
			"list becomes an empty JSON array for clients; read errors are mapped instead of being " +
			"replaced by a successful empty result here. This operation is available through " +
			"read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/backups",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/backups",
		Input:       adminapi.Arguments("/api/v1/admin/backups", []string{}, nil),
	}),
	"backups.restore.create": adminapi.Define(adminapi.Operation{
		ID:     "backups.restore.create",
		Module: "backups",
		Title:  "Restore Backup",
		Description: "RestoreBackup requires a bound password verifier, request password, and current " +
			"authentication subject before launching asynchronous restoration. The password is checked " +
			"exactly as supplied for that subject's ID; false returns bad request and verifier errors use " +
			"shared mapping. Successful registration returns accepted status with the service's running " +
			"restore record. Omitted body fields follow the HTTP handler defaults; explicit null and " +
			"empty collections retain their documented meanings. Browser confirmation by the token owner " +
			"is required.",
		Method:      "POST",
		Path:        "/api/v1/admin/backups/:id/restore",
		Sensitive:   true,
		StepUp:      true,
		Response:    "json",
		BrowserPath: "/admin/backups",
		Input:       adminapi.Arguments("/api/v1/admin/backups/:id/restore", []string{}, (*RestoreBackupRequest)(nil)),
	}),
	"backups.s3_config.list": adminapi.Define(adminapi.Operation{
		ID:     "backups.s3_config.list",
		Module: "backups",
		Title:  "Get S3 Config",
		Description: "GetS3Config returns the secret-hidden remote configuration for legacy API clients. The " +
			"service handles missing or failed reads through compatible settings and defaults; a " +
			"malformed present legacy object reaches the shared HTTP error response path. This operation " +
			"is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/backups/s3-config",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/backups",
		Input:       adminapi.Arguments("/api/v1/admin/backups/s3-config", []string{}, nil),
	}),
	"backups.s3_config.test.create": adminapi.Define(adminapi.Operation{
		ID:     "backups.s3_config.test.create",
		Module: "backups",
		Title:  "Test S3 Connection",
		Description: "TestS3Connection probes candidate legacy S3 settings and returns ok plus a message inside a " +
			"successful envelope. Malformed binding returns bad request, while a failed bucket probe " +
			"remains a configuration result visible in response data. Omitted body fields follow the HTTP " +
			"handler defaults; explicit null and empty collections retain their documented meanings. " +
			"Browser confirmation by the token owner is required.",
		Method:      "POST",
		Path:        "/api/v1/admin/backups/s3-config/test",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/backups",
		Input:       adminapi.Arguments("/api/v1/admin/backups/s3-config/test", []string{}, (*backup.BackupS3Config)(nil)),
	}),
	"backups.s3_config.update": adminapi.Define(adminapi.Operation{
		ID:     "backups.s3_config.update",
		Module: "backups",
		Title:  "Update S3 Config",
		Description: "UpdateS3Config binds legacy S3 settings and returns the service's saved view. The service " +
			"prepares secret reuse or encryption and invalidates its cached store; malformed JSON and " +
			"persistence errors are mapped before a response succeeds. Omitted body fields follow the " +
			"HTTP handler defaults; explicit null and empty collections retain their documented meanings. " +
			"Browser confirmation by the token owner is required.",
		Method:      "PUT",
		Path:        "/api/v1/admin/backups/s3-config",
		Sensitive:   true,
		StepUp:      true,
		Response:    "json",
		BrowserPath: "/admin/backups",
		Input:       adminapi.Arguments("/api/v1/admin/backups/s3-config", []string{}, (*backup.BackupS3Config)(nil)),
	}),
	"backups.schedule.list": adminapi.Define(adminapi.Operation{
		ID:     "backups.schedule.list",
		Module: "backups",
		Title:  "Get Schedule",
		Description: "GetSchedule returns the service's saved cron and retention view. Its reader may supply " +
			"disabled defaults for missing or unusable settings; any returned service error uses shared " +
			"HTTP mapping before success. This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/backups/schedule",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/backups",
		Input:       adminapi.Arguments("/api/v1/admin/backups/schedule", []string{}, nil),
	}),
	"backups.schedule.update": adminapi.Define(adminapi.Operation{
		ID:     "backups.schedule.update",
		Module: "backups",
		Title:  "Update Schedule",
		Description: "UpdateSchedule binds and persists cron and retention settings through the service. The " +
			"service requires an expression when enabled and validates every nonempty expression before " +
			"applying it; binding and application errors are returned before the updated JSON view " +
			"succeeds. Omitted body fields follow the HTTP handler defaults; explicit null and empty " +
			"collections retain their documented meanings. Browser confirmation by the token owner is " +
			"required.",
		Method:      "PUT",
		Path:        "/api/v1/admin/backups/schedule",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/backups",
		Input:       adminapi.Arguments("/api/v1/admin/backups/schedule", []string{}, (*backup.BackupScheduleConfig)(nil)),
	}),
	"backups.storage_config.list": adminapi.Define(adminapi.Operation{
		ID:     "backups.storage_config.list",
		Module: "backups",
		Title:  "Get Storage Config",
		Description: "GetStorageConfig returns the service's administrator storage view with its effective local " +
			"path and hidden S3 secret. Configuration errors are translated through the shared response " +
			"helper before a successful JSON envelope is written. This operation is available through " +
			"read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/backups/storage-config",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/backups",
		Input:       adminapi.Arguments("/api/v1/admin/backups/storage-config", []string{}, nil),
	}),
	"backups.storage_config.test.create": adminapi.Define(adminapi.Operation{
		ID:     "backups.storage_config.test.create",
		Module: "backups",
		Title:  "Test Storage Connection",
		Description: "TestStorageConnection binds a candidate destination and reports its access probe inside a " +
			"success envelope. Failed probes return ok=false and their message; malformed request JSON " +
			"still returns bad request before the probe runs. Omitted body fields follow the HTTP handler " +
			"defaults; explicit null and empty collections retain their documented meanings. Browser " +
			"confirmation by the token owner is required.",
		Method:      "POST",
		Path:        "/api/v1/admin/backups/storage-config/test",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/backups",
		Input:       adminapi.Arguments("/api/v1/admin/backups/storage-config/test", []string{}, (*backup.BackupStorageConfig)(nil)),
	}),
	"backups.storage_config.update": adminapi.Define(adminapi.Operation{
		ID:     "backups.storage_config.update",
		Module: "backups",
		Title:  "Update Storage Config",
		Description: "UpdateStorageConfig binds and saves the unified destination through the backup service, " +
			"returning its secret-hidden view. Binding errors return bad request; destination " +
			"normalization, secret reuse, encryption, and cached-store reset belong to the service. " +
			"Omitted body fields follow the HTTP handler defaults; explicit null and empty collections " +
			"retain their documented meanings. Browser confirmation by the token owner is required.",
		Method:      "PUT",
		Path:        "/api/v1/admin/backups/storage-config",
		Sensitive:   true,
		StepUp:      true,
		Response:    "json",
		BrowserPath: "/admin/backups",
		Input:       adminapi.Arguments("/api/v1/admin/backups/storage-config", []string{}, (*backup.BackupStorageConfig)(nil)),
	}),
	"data_management.agent.health.list": adminapi.Define(adminapi.Operation{
		ID:     "data_management.agent.health.list",
		Module: "data-management",
		Title:  "Get Agent Health",
		Description: "GetAgentHealth returns availability and diagnostic socket metadata in a success envelope. A " +
			"disabled or absent service remains inspectable through this endpoint because enabled and " +
			"reason express that state inside the data payload. This operation is available through " +
			"read_operation. Browser confirmation by the token owner is required.",
		Method:      "GET",
		Path:        "/api/v1/admin/data-management/agent/health",
		ReadOnly:    true,
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/data-management",
		Input:       adminapi.Arguments("/api/v1/admin/data-management/agent/health", []string{}, nil),
	}),
	"data_management.backups.create": adminapi.Define(adminapi.Operation{
		ID:     "data_management.backups.create",
		Module: "data-management",
		Title:  "Create Backup Job",
		Description: "CreateBackupJob validates the legacy job request and its required source selection. Accepted " +
			"input then receives the service availability response; the retired route does not start " +
			"PostgreSQL, Redis, or full-source archive work. Omitted body fields follow the HTTP handler " +
			"defaults; explicit null and empty collections retain their documented meanings. Browser " +
			"confirmation by the token owner is required.",
		Method:      "POST",
		Path:        "/api/v1/admin/data-management/backups",
		Sensitive:   true,
		StepUp:      true,
		Response:    "json",
		BrowserPath: "/admin/data-management",
		Input:       adminapi.Arguments("/api/v1/admin/data-management/backups", []string{}, (*CreateBackupJobRequest)(nil)),
	}),
	"data_management.backups.get": adminapi.Define(adminapi.Operation{
		ID:     "data_management.backups.get",
		Module: "data-management",
		Title:  "Get Backup Job",
		Description: "GetBackupJob accepts a nonblank trimmed job route ID before checking availability. Missing " +
			"identifiers return bad request; accepted identifiers reach the retired facade response and " +
			"are not used to retrieve an agent job. This operation is available through read_operation. " +
			"Browser confirmation by the token owner is required.",
		Method:      "GET",
		Path:        "/api/v1/admin/data-management/backups/:job_id",
		ReadOnly:    true,
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/data-management",
		Input:       adminapi.Arguments("/api/v1/admin/data-management/backups/:job_id", []string{}, nil),
	}),
	"data_management.backups.list": adminapi.Define(adminapi.Operation{
		ID:     "data_management.backups.list",
		Module: "data-management",
		Title:  "List Backup Jobs",
		Description: "ListBackupJobs returns the service availability response for the retired job-list route. The " +
			"bound facade reports deprecation, while a zero-value handler reports an absent service " +
			"instead of retrieving archive jobs from an agent. This operation is available through " +
			"read_operation. Browser confirmation by the token owner is required.",
		Method:      "GET",
		Path:        "/api/v1/admin/data-management/backups",
		ReadOnly:    true,
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/data-management",
		Input:       adminapi.Arguments("/api/v1/admin/data-management/backups", []string{}, nil),
	}),
	"data_management.config.list": adminapi.Define(adminapi.Operation{
		ID:     "data_management.config.list",
		Module: "data-management",
		Title:  "Get Config",
		Description: "GetConfig reports availability for the retired configuration retrieval route. A missing " +
			"service returns its unconfigured reason, while the bound retired facade supplies deprecation " +
			"instead of retrieving any agent configuration. This operation is available through " +
			"read_operation. Browser confirmation by the token owner is required.",
		Method:      "GET",
		Path:        "/api/v1/admin/data-management/config",
		ReadOnly:    true,
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/data-management",
		Input:       adminapi.Arguments("/api/v1/admin/data-management/config", []string{}, nil),
	}),
	"data_management.config.update": adminapi.Define(adminapi.Operation{
		ID:     "data_management.config.update",
		Module: "data-management",
		Title:  "Update Config",
		Description: "UpdateConfig validates legacy configuration JSON before checking agent availability. Binding " +
			"errors return bad request, while valid input reaches the retired facade and is not applied " +
			"to database or object-storage settings by this handler. Omitted body fields follow the HTTP " +
			"handler defaults; explicit null and empty collections retain their documented meanings. " +
			"Browser confirmation by the token owner is required.",
		Method:      "PUT",
		Path:        "/api/v1/admin/data-management/config",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/data-management",
		Input:       adminapi.Arguments("/api/v1/admin/data-management/config", []string{}, (*backup.DataManagementConfig)(nil)),
	}),
	"data_management.s3.profiles.activate.create": adminapi.Define(adminapi.Operation{
		ID:     "data_management.s3.profiles.activate.create",
		Module: "data-management",
		Title:  "Set Active S3 Profile",
		Description: "SetActiveS3Profile validates the trimmed route profile ID and then checks agent " +
			"availability. Empty identifiers return bad request; accepted requests receive the retired " +
			"facade response before any active-profile selection can be applied. Browser confirmation by " +
			"the token owner is required.",
		Method:      "POST",
		Path:        "/api/v1/admin/data-management/s3/profiles/:profile_id/activate",
		Sensitive:   true,
		StepUp:      true,
		Response:    "json",
		BrowserPath: "/admin/data-management",
		Input:       adminapi.Arguments("/api/v1/admin/data-management/s3/profiles/:profile_id/activate", []string{}, nil),
	}),
	"data_management.s3.profiles.create": adminapi.Define(adminapi.Operation{
		ID:     "data_management.s3.profiles.create",
		Module: "data-management",
		Title:  "Create S3 Profile",
		Description: "CreateS3Profile binds the legacy remote-profile request before checking service " +
			"availability. Missing required name or profile ID and malformed binding return bad request; " +
			"accepted data reaches retirement reporting without profile persistence. Omitted body fields " +
			"follow the HTTP handler defaults; explicit null and empty collections retain their " +
			"documented meanings. Browser confirmation by the token owner is required.",
		Method:      "POST",
		Path:        "/api/v1/admin/data-management/s3/profiles",
		Sensitive:   true,
		StepUp:      true,
		Response:    "json",
		BrowserPath: "/admin/data-management",
		Input:       adminapi.Arguments("/api/v1/admin/data-management/s3/profiles", []string{}, (*CreateS3ProfileRequest)(nil)),
	}),
	"data_management.s3.profiles.delete": adminapi.Define(adminapi.Operation{
		ID:     "data_management.s3.profiles.delete",
		Module: "data-management",
		Title:  "Delete S3 Profile",
		Description: "DeleteS3Profile validates a nonblank trimmed profile route ID before checking agent " +
			"availability. Accepted IDs reach the retired facade response, and this compatibility handler " +
			"performs no remote-profile deletion itself. Browser confirmation by the token owner is " +
			"required.",
		Method:      "DELETE",
		Path:        "/api/v1/admin/data-management/s3/profiles/:profile_id",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/data-management",
		Input:       adminapi.Arguments("/api/v1/admin/data-management/s3/profiles/:profile_id", []string{}, nil),
	}),
	"data_management.s3.profiles.list": adminapi.Define(adminapi.Operation{
		ID:     "data_management.s3.profiles.list",
		Module: "data-management",
		Title:  "List S3 Profiles",
		Description: "ListS3Profiles delegates the retired profile-list request to its availability response. An " +
			"absent service and the bound retired facade provide their respective unconfigured or " +
			"deprecation reasons before any profile data can be returned. This operation is available " +
			"through read_operation. Browser confirmation by the token owner is required.",
		Method:      "GET",
		Path:        "/api/v1/admin/data-management/s3/profiles",
		ReadOnly:    true,
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/data-management",
		Input:       adminapi.Arguments("/api/v1/admin/data-management/s3/profiles", []string{}, nil),
	}),
	"data_management.s3.profiles.update": adminapi.Define(adminapi.Operation{
		ID:     "data_management.s3.profiles.update",
		Module: "data-management",
		Title:  "Update S3 Profile",
		Description: "UpdateS3Profile binds the legacy remote settings and validates a nonblank trimmed route " +
			"profile ID before checking availability. The binding check occurs first, preserving which " +
			"malformed request error is returned when both inputs are unusable. Omitted body fields " +
			"follow the HTTP handler defaults; explicit null and empty collections retain their " +
			"documented meanings. Browser confirmation by the token owner is required.",
		Method:      "PUT",
		Path:        "/api/v1/admin/data-management/s3/profiles/:profile_id",
		Sensitive:   true,
		StepUp:      true,
		Response:    "json",
		BrowserPath: "/admin/data-management",
		Input:       adminapi.Arguments("/api/v1/admin/data-management/s3/profiles/:profile_id", []string{}, (*UpdateS3ProfileRequest)(nil)),
	}),
	"data_management.s3.test.create": adminapi.Define(adminapi.Operation{
		ID:     "data_management.s3.test.create",
		Module: "data-management",
		Title:  "Test S3",
		Description: "TestS3 validates the legacy connection-test shape, including its required region and bucket, " +
			"before checking availability. Valid input reaches the retired service response; this handler " +
			"does not construct or probe an S3 client. Omitted body fields follow the HTTP handler " +
			"defaults; explicit null and empty collections retain their documented meanings. Browser " +
			"confirmation by the token owner is required.",
		Method:      "POST",
		Path:        "/api/v1/admin/data-management/s3/test",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/data-management",
		Input:       adminapi.Arguments("/api/v1/admin/data-management/s3/test", []string{}, (*TestS3ConnectionRequest)(nil)),
	}),
	"data_management.sources.profiles.activate.create": adminapi.Define(adminapi.Operation{
		ID:     "data_management.sources.profiles.activate.create",
		Module: "data-management",
		Title:  "Set Active Source Profile",
		Description: "SetActiveSourceProfile accepts postgres or redis plus a nonblank trimmed profile route ID " +
			"before checking availability. Accepted requests reach the retired facade response; this " +
			"handler does not change the active source profile. Browser confirmation by the token owner " +
			"is required.",
		Method:      "POST",
		Path:        "/api/v1/admin/data-management/sources/:source_type/profiles/:profile_id/activate",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/data-management",
		Input:       adminapi.Arguments("/api/v1/admin/data-management/sources/:source_type/profiles/:profile_id/activate", []string{}, nil),
	}),
	"data_management.sources.profiles.create": adminapi.Define(adminapi.Operation{
		ID:     "data_management.sources.profiles.create",
		Module: "data-management",
		Title:  "Create Source Profile",
		Description: "CreateSourceProfile validates the postgres or redis selector and legacy creation JSON before " +
			"checking availability. Source and binding errors return bad request; accepted profile data " +
			"is retained only in this request before retirement reporting. Omitted body fields follow the " +
			"HTTP handler defaults; explicit null and empty collections retain their documented meanings. " +
			"Browser confirmation by the token owner is required.",
		Method:      "POST",
		Path:        "/api/v1/admin/data-management/sources/:source_type/profiles",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/data-management",
		Input:       adminapi.Arguments("/api/v1/admin/data-management/sources/:source_type/profiles", []string{}, (*CreateSourceProfileRequest)(nil)),
	}),
	"data_management.sources.profiles.delete": adminapi.Define(adminapi.Operation{
		ID:     "data_management.sources.profiles.delete",
		Module: "data-management",
		Title:  "Delete Source Profile",
		Description: "DeleteSourceProfile validates the source selector and nonblank trimmed route ID before " +
			"checking availability. The bound retired facade returns deprecation, leaving the profile " +
			"identifier as request input without performing a deletion. Browser confirmation by the token " +
			"owner is required.",
		Method:      "DELETE",
		Path:        "/api/v1/admin/data-management/sources/:source_type/profiles/:profile_id",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/data-management",
		Input:       adminapi.Arguments("/api/v1/admin/data-management/sources/:source_type/profiles/:profile_id", []string{}, nil),
	}),
	"data_management.sources.profiles.list": adminapi.Define(adminapi.Operation{
		ID:     "data_management.sources.profiles.list",
		Module: "data-management",
		Title:  "List Source Profiles",
		Description: "ListSourceProfiles accepts trimmed postgres or redis source selectors before checking " +
			"availability. Missing or unsupported selectors return bad request; accepted selectors reach " +
			"the retired service response rather than a profile query. This operation is available " +
			"through read_operation. Browser confirmation by the token owner is required.",
		Method:      "GET",
		Path:        "/api/v1/admin/data-management/sources/:source_type/profiles",
		ReadOnly:    true,
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/data-management",
		Input:       adminapi.Arguments("/api/v1/admin/data-management/sources/:source_type/profiles", []string{}, nil),
	}),
	"data_management.sources.profiles.update": adminapi.Define(adminapi.Operation{
		ID:     "data_management.sources.profiles.update",
		Module: "data-management",
		Title:  "Update Source Profile",
		Description: "UpdateSourceProfile requires an accepted source selector, a nonblank trimmed profile route " +
			"ID, and bindable legacy update data. Those checks precede agent availability, so malformed " +
			"input can return bad request before the retirement response. Omitted body fields follow the " +
			"HTTP handler defaults; explicit null and empty collections retain their documented meanings. " +
			"Browser confirmation by the token owner is required.",
		Method:      "PUT",
		Path:        "/api/v1/admin/data-management/sources/:source_type/profiles/:profile_id",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/data-management",
		Input:       adminapi.Arguments("/api/v1/admin/data-management/sources/:source_type/profiles/:profile_id", []string{}, (*UpdateSourceProfileRequest)(nil)),
	}),
}
