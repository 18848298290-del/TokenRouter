package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/backup"
	middleware "github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// backup.go handles administrator backup configuration, archive downloads, and
// password-verified restore requests.
// Administrator backup requests configure storage and content, launch tracked
// background operations, and retrieve archive metadata or download responses.
// Connection tests report their outcome inside a successful JSON envelope, while
// restoration verifies the current subject's password before asking the service
// to register its asynchronous database operation.

type (
	// PasswordVerifier checks the current subject's password before a restore operation.
	// False rejects the request as an incorrect password; verification errors pass
	// through shared HTTP error handling before restoration can be registered.
	PasswordVerifier func(context.Context, int64, string) (bool, error)
	// BackupHandler adapts administrator archive operations to JSON and streamed HTTP responses.
	BackupHandler struct {
		backupService *backup.BackupService // archive configuration and operation service
		// userService rechecks the authenticated subject's password on each restore.
		// App binds a verifier before serving that endpoint; nil verification is unsupported.
		userService PasswordVerifier
	}
)

// CreateBackupRequest supplies the optional expiration policy for a manual archive.
type CreateBackupRequest struct {
	// ExpireDays defaults to fourteen when missing or JSON null leaves the pointer
	// nil. The service omits expiration for zero or negative supplied day counts.
	ExpireDays *int `json:"expire_days"`
}

// RestoreBackupRequest carries the password required again before database restoration.
type RestoreBackupRequest struct {
	// Password is required by JSON binding and passed unchanged to the verifier.
	// Whitespace remains part of the supplied credential rather than being trimmed by this handler.
	Password string `json:"password" binding:"required"`
}

// NewBackupHandler binds the archive service and current-password verifier for
// administrator endpoints. Dependencies are retained for request handling; app
// supplies administrator group protection and binds a verifier for restore requests before serving.
func NewBackupHandler(backupService *backup.BackupService, userService PasswordVerifier) *BackupHandler {
	return &BackupHandler{
		backupService: backupService,
		userService:   userService,
	}
}

// GetStorageConfig returns the service's administrator storage view with its
// effective local path and hidden S3 secret. Configuration errors are translated
// through the shared response helper before a successful JSON envelope is written.
func (h *BackupHandler) GetStorageConfig(c *gin.Context) {
	cfg, err := h.backupService.GetStorageConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}

// UpdateStorageConfig binds and saves the unified destination through the backup
// service, returning its secret-hidden view. Binding errors return bad request;
// destination normalization, secret reuse, encryption, and cached-store reset belong to the service.
func (h *BackupHandler) UpdateStorageConfig(c *gin.Context) {
	var req backup.BackupStorageConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	cfg, err := h.backupService.UpdateStorageConfig(c.Request.Context(), req)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}

// TestStorageConnection binds a candidate destination and reports its access
// probe inside a success envelope. Failed probes return ok=false and their message;
// malformed request JSON still returns bad request before the probe runs.
func (h *BackupHandler) TestStorageConnection(c *gin.Context) {
	var req backup.BackupStorageConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	err := h.backupService.TestStorageConnection(c.Request.Context(), req)
	if err != nil {
		// Connection failure is a tested configuration outcome for the administrator,
		// so report it as ok=false in data while reserving request errors for malformed input.
		response.Success(c, gin.H{"ok": false, "message": err.Error()})
		return
	}
	response.Success(c, gin.H{"ok": true, "message": "connection successful"})
}

// GetContentConfig returns optional backup data selections and computed table
// exclusions from the service. Missing or failed reads use disabled-group defaults;
// malformed saved JSON uses shared error mapping before a response is written.
func (h *BackupHandler) GetContentConfig(c *gin.Context) {
	cfg, err := h.backupService.GetContentConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}

// UpdateContentConfig binds the administrator's optional data-group selections
// and returns the saved view with computed exclusions. Binding and service errors
// are mapped before success; the service discards client exclusion lists and recomputes them from its group rules.
func (h *BackupHandler) UpdateContentConfig(c *gin.Context) {
	var req backup.BackupContentConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	cfg, err := h.backupService.UpdateContentConfig(c.Request.Context(), req)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}

// GetS3Config returns the secret-hidden remote configuration for legacy API clients.
// The service handles missing or failed reads through compatible settings and defaults;
// a malformed present legacy object reaches the shared HTTP error response path.
func (h *BackupHandler) GetS3Config(c *gin.Context) {
	cfg, err := h.backupService.GetS3Config(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}

// UpdateS3Config binds legacy S3 settings and returns the service's saved view.
// The service prepares secret reuse or encryption and invalidates its cached
// store; malformed JSON and persistence errors are mapped before a response succeeds.
func (h *BackupHandler) UpdateS3Config(c *gin.Context) {
	var req backup.BackupS3Config
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	cfg, err := h.backupService.UpdateS3Config(c.Request.Context(), req)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}

// TestS3Connection probes candidate legacy S3 settings and returns ok plus a
// message inside a successful envelope. Malformed binding returns bad request,
// while a failed bucket probe remains a configuration result visible in response data.
func (h *BackupHandler) TestS3Connection(c *gin.Context) {
	var req backup.BackupS3Config
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	err := h.backupService.TestS3Connection(c.Request.Context(), req)
	if err != nil {
		// Keep legacy connection-test responses compatible with the unified probe's
		// ok/message data shape, so a rejected bucket remains an inspectable administrator test result.
		response.Success(c, gin.H{"ok": false, "message": err.Error()})
		return
	}
	response.Success(c, gin.H{"ok": true, "message": "connection successful"})
}

// GetSchedule returns the service's saved cron and retention view. Its reader
// may supply disabled defaults for missing or unusable settings; any returned
// service error uses shared HTTP mapping before success.
func (h *BackupHandler) GetSchedule(c *gin.Context) {
	cfg, err := h.backupService.GetSchedule(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}

// UpdateSchedule binds and persists cron and retention settings through the service.
// The service requires an expression when enabled and validates every nonempty expression before applying it;
// binding and application errors are returned before the updated JSON view succeeds.
func (h *BackupHandler) UpdateSchedule(c *gin.Context) {
	var req backup.BackupScheduleConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	cfg, err := h.backupService.UpdateSchedule(c.Request.Context(), req)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}

// CreateBackup launches a tracked manual archive and returns its initial record
// with accepted status. Binding errors are ignored, so the resulting ExpireDays
// pointer determines policy: nil chooses fourteen days and supplied nonpositive
// values leave the archive without expiration. Later operation results are queried by record ID.
func (h *BackupHandler) CreateBackup(c *gin.Context) {
	var req CreateBackupRequest
	// Keep optional-body behavior even when binding reports an error, using whatever
	// expiration pointer remains in req rather than rejecting this archive-start request for its body.
	_ = c.ShouldBindJSON(&req) // An empty body uses the default expiration.

	expireDays := 14 // Default expiration in days
	if req.ExpireDays != nil {
		expireDays = *req.ExpireDays
	}

	record, err := h.backupService.StartBackup(c.Request.Context(), "manual", expireDays)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Accepted(c, record)
}

// ListBackups returns the service's ordered archive records under the items key.
// A nil record list becomes an empty JSON array for clients; read errors are
// mapped instead of being replaced by a successful empty result here.
func (h *BackupHandler) ListBackups(c *gin.Context) {
	records, err := h.backupService.ListBackups(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if records == nil {
		// Emit an empty array rather than JSON null for the collection-shaped response,
		// allowing clients to render a cleared or initially empty backup history consistently.
		records = []backup.BackupRecord{}
	}
	response.Success(c, gin.H{"items": records})
}

// GetBackup returns one stored archive record for the nonempty route identifier.
// Registered routes enforce canonical IDs before this handler; direct fixtures
// can call it with other nonempty values and receive the service's lookup result.
func (h *BackupHandler) GetBackup(c *gin.Context) {
	backupID := c.Param("id")
	if backupID == "" {
		response.BadRequest(c, "backup ID is required")
		return
	}
	record, err := h.backupService.GetBackupRecord(c.Request.Context(), backupID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, record)
}

// DeleteBackup removes the selected record through service-managed object cleanup.
// A nonempty ID is required here and canonical validation belongs to registered
// routes; service errors are returned before the deleted=true success response is written.
func (h *BackupHandler) DeleteBackup(c *gin.Context) {
	backupID := c.Param("id")
	if backupID == "" {
		response.BadRequest(c, "backup ID is required")
		return
	}
	if err := h.backupService.DeleteBackup(c.Request.Context(), backupID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"deleted": true})
}

// GetDownloadURL returns the application download path for matching single-file
// local metadata, or delegates signed single/part URLs to the service. The local
// path response itself does not check completion; opening that download and the
// service's signed-URL path perform their respective completed-record checks.
func (h *BackupHandler) GetDownloadURL(c *gin.Context) {
	backupID := c.Param("id")
	if backupID == "" {
		response.BadRequest(c, "backup ID is required")
		return
	}
	record, err := h.backupService.GetBackupRecord(c.Request.Context(), backupID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if len(record.Parts) == 0 && (record.StorageType == backup.BackupStorageTypeLocal || (record.StorageType == "" && record.S3Key == "")) {
		// A local file needs the protected application stream endpoint instead of an
		// object-store presigner, including legacy single-file records with no remote key or storage type.
		response.Success(c, gin.H{"url": fmt.Sprintf("/api/v1/admin/backups/%s/download", backupID)})
		return
	}
	download, err := h.backupService.GetBackupDownloadURL(c.Request.Context(), backupID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, download)
}

// DownloadBackup opens a completed single-file archive and streams its recorded
// byte length as gzip content, closing the reader on return. Missing filenames
// use the ID plus sql.gz suffix, and service validation errors are returned before streaming begins.
func (h *BackupHandler) DownloadBackup(c *gin.Context) {
	backupID := c.Param("id")
	if backupID == "" {
		response.BadRequest(c, "backup ID is required")
		return
	}
	body, record, err := h.backupService.OpenBackupDownload(c.Request.Context(), backupID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	// Close the archive after streaming has chosen the response. A deferred closing
	// error is discarded because this handler has no later response step to report it.
	defer func() { _ = body.Close() }()

	fileName := strings.TrimSpace(record.FileName)
	if fileName == "" {
		// Older metadata can omit FileName. Supply an archive-shaped download name
		// derived from the requested record ID while keeping its actual storage key unchanged.
		fileName = backupID + ".sql.gz"
	}
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fileName))
	c.DataFromReader(http.StatusOK, record.SizeBytes, "application/gzip", body, nil)
}

// RestoreBackup requires a bound password verifier, request password, and current
// authentication subject before launching asynchronous restoration. The password is
// checked exactly as supplied for that subject's ID; false returns bad request
// and verifier errors use shared mapping. Successful registration returns accepted
// status with the service's running restore record.
func (h *BackupHandler) RestoreBackup(c *gin.Context) {
	backupID := c.Param("id")
	if backupID == "" {
		response.BadRequest(c, "backup ID is required")
		return
	}

	var req RestoreBackupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "password is required for restore operation")
		return
	}

	sub, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "unauthorized")
		return
	}

	valid, err := h.userService(c.Request.Context(), sub.UserID, req.Password)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if !valid {
		response.BadRequest(c, "incorrect admin password")
		return
	}

	record, err := h.backupService.StartRestore(c.Request.Context(), backupID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Accepted(c, record)
}
