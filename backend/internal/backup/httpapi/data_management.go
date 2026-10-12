package httpapi

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/backup"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// Retired agent routes retain their JSON and path validation before checking
// availability. Health remains a successful response containing its disabled
// state and diagnostic socket path, while configuration, profile, and job requests
// use the availability error after valid legacy input has been accepted.

// DataManagementHandler retains agent health and request validation for compatibility endpoints.
type DataManagementHandler struct {
	// dataManagementService supplies the availability result after request validation.
	// A nil interface reports absence; NewDataManagementHandler can retain a typed nil facade whose nil-safe methods report retirement.
	dataManagementService dataManagementService
}

// dataManagementService provides the legacy handler's availability and health decisions.
type dataManagementService interface {
	// EnsureAgentEnabled returns the reason an agent operation is unavailable.
	// The bound backup facade reports deprecation; a nil result lets the handler's
	// availability helper return without writing an error response itself.
	EnsureAgentEnabled(ctx context.Context) error
	// GetAgentHealth supplies availability, reason, and socket metadata for the health endpoint.
	// The HTTP handler returns this payload with success status even when its
	// enabled field reports that agent operations are unavailable.
	GetAgentHealth(ctx context.Context) backup.DataManagementAgentHealth
}

// TestS3ConnectionRequest retains remote connection-test fields validated by the retired route.
type TestS3ConnectionRequest struct {
	Endpoint string `json:"endpoint"` // legacy object-storage service endpoint
	// Region and Bucket carry required binding tags for the connection-test shape.
	// Other connection fields remain accepted input before the handler reports agent unavailability.
	Region          string `json:"region" binding:"required"` // legacy object-storage region label
	Bucket          string `json:"bucket" binding:"required"` // legacy archive bucket name
	AccessKeyID     string `json:"access_key_id"`             // legacy access credential identifier
	SecretAccessKey string `json:"secret_access_key"`         // legacy secret credential value
	Prefix          string `json:"prefix"`                    // legacy archive key prefix
	ForcePathStyle  bool   `json:"force_path_style"`          // legacy path-style addressing flag
	UseSSL          bool   `json:"use_ssl"`                   // legacy TLS transport flag
}

// CreateBackupJobRequest retains the legacy job selection and retry-identity request shape.
type CreateBackupJobRequest struct {
	// BackupType is required and binds only postgres, redis, or full selections.
	// Successful binding reaches the availability check; the retired handler launches no job.
	BackupType     string `json:"backup_type" binding:"required,oneof=postgres redis full"`
	UploadToS3     bool   `json:"upload_to_s3"`        // legacy remote upload selection
	S3ProfileID    string `json:"s3_profile_id"`       // selected object-storage profile identifier
	PostgresID     string `json:"postgres_profile_id"` // selected PostgreSQL profile identifier
	RedisID        string `json:"redis_profile_id"`    // selected Redis profile identifier
	IdempotencyKey string `json:"idempotency_key"`     // legacy job retry identifier
}

// CreateSourceProfileRequest retains legacy source-profile creation data for request binding.
type CreateSourceProfileRequest struct {
	// ProfileID and Name require nonempty strings during JSON binding. Their values
	// retain whitespace because source-profile creation does not trim fields inside the request body.
	ProfileID string                            `json:"profile_id" binding:"required"` // submitted legacy profile identifier
	Name      string                            `json:"name" binding:"required"`       // submitted profile display name
	Config    backup.DataManagementSourceConfig `json:"config" binding:"required"`     // legacy source connection data
	SetActive bool                              `json:"set_active"`                    // legacy active-profile selection flag
}

// UpdateSourceProfileRequest retains source name and connection data for retired update binding.
type UpdateSourceProfileRequest struct {
	// Name requires nonempty text during JSON binding; whitespace remains accepted
	// in this field before the retired handler checks service availability.
	Name   string                            `json:"name" binding:"required"`   // submitted profile display name
	Config backup.DataManagementSourceConfig `json:"config" binding:"required"` // legacy source connection data
}

// CreateS3ProfileRequest retains the remote profile creation shape accepted before retirement reporting.
type CreateS3ProfileRequest struct {
	// ProfileID and Name require nonempty strings during JSON binding. Their values
	// retain whitespace because remote-profile creation does not trim fields inside the request body.
	ProfileID       string `json:"profile_id" binding:"required"` // submitted legacy profile identifier
	Name            string `json:"name" binding:"required"`       // submitted profile display name
	Enabled         bool   `json:"enabled"`                       // legacy profile enabled flag
	Endpoint        string `json:"endpoint"`                      // legacy object-storage service endpoint
	Region          string `json:"region"`                        // legacy object-storage region label
	Bucket          string `json:"bucket"`                        // legacy archive bucket name
	AccessKeyID     string `json:"access_key_id"`                 // legacy access credential identifier
	SecretAccessKey string `json:"secret_access_key"`             // legacy secret credential value
	Prefix          string `json:"prefix"`                        // legacy archive key prefix
	ForcePathStyle  bool   `json:"force_path_style"`              // legacy path-style addressing flag
	UseSSL          bool   `json:"use_ssl"`                       // legacy TLS transport flag
	SetActive       bool   `json:"set_active"`                    // legacy active-profile selection flag
}

// UpdateS3ProfileRequest retains named remote-profile settings for retired request validation.
type UpdateS3ProfileRequest struct {
	// Name requires nonempty text during JSON binding; whitespace remains accepted
	// in this field before the retired handler checks service availability.
	Name            string `json:"name" binding:"required"` // submitted profile display name
	Enabled         bool   `json:"enabled"`                 // legacy profile enabled flag
	Endpoint        string `json:"endpoint"`                // legacy object-storage service endpoint
	Region          string `json:"region"`                  // legacy object-storage region label
	Bucket          string `json:"bucket"`                  // legacy archive bucket name
	AccessKeyID     string `json:"access_key_id"`           // legacy access credential identifier
	SecretAccessKey string `json:"secret_access_key"`       // legacy secret credential value
	Prefix          string `json:"prefix"`                  // legacy archive key prefix
	ForcePathStyle  bool   `json:"force_path_style"`        // legacy path-style addressing flag
	UseSSL          bool   `json:"use_ssl"`                 // legacy TLS transport flag
}

// NewDataManagementHandler retains the retired facade for health and availability
// responses. A nil concrete pointer becomes a typed service interface; its nil-safe
// facade methods still report retirement using the default socket path.
func NewDataManagementHandler(dataManagementService *backup.DataManagementService) *DataManagementHandler {
	return &DataManagementHandler{dataManagementService: dataManagementService}
}

// GetAgentHealth returns availability and diagnostic socket metadata in a success
// envelope. A disabled or absent service remains inspectable through this endpoint
// because enabled and reason express that state inside the data payload.
func (h *DataManagementHandler) GetAgentHealth(c *gin.Context) {
	health := h.getAgentHealth(c)
	payload := gin.H{
		"enabled":     health.Enabled,
		"reason":      health.Reason,
		"socket_path": health.SocketPath,
	}
	response.Success(c, payload)
}

// GetConfig reports availability for the retired configuration retrieval route.
// A missing service returns its unconfigured reason, while the bound retired
// facade supplies deprecation instead of retrieving any agent configuration.
func (h *DataManagementHandler) GetConfig(c *gin.Context) {
	h.respondAgentUnavailable(c)
}

// UpdateConfig validates legacy configuration JSON before checking agent availability.
// Binding errors return bad request, while valid input reaches the retired facade
// and is not applied to database or object-storage settings by this handler.
func (h *DataManagementHandler) UpdateConfig(c *gin.Context) {
	var req backup.DataManagementConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	h.respondAgentUnavailable(c)
}

// TestS3 validates the legacy connection-test shape, including its required region
// and bucket, before checking availability. Valid input reaches the retired
// service response; this handler does not construct or probe an S3 client.
func (h *DataManagementHandler) TestS3(c *gin.Context) {
	var req TestS3ConnectionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	h.respondAgentUnavailable(c)
}

// CreateBackupJob validates the legacy job request and its required source selection.
// Accepted input then receives the service availability response; the retired
// route does not start PostgreSQL, Redis, or full-source archive work.
func (h *DataManagementHandler) CreateBackupJob(c *gin.Context) {
	var req CreateBackupJobRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	h.respondAgentUnavailable(c)
}

// ListSourceProfiles accepts trimmed postgres or redis source selectors before
// checking availability. Missing or unsupported selectors return bad request;
// accepted selectors reach the retired service response rather than a profile query.
func (h *DataManagementHandler) ListSourceProfiles(c *gin.Context) {
	sourceType := strings.TrimSpace(c.Param("source_type"))
	if sourceType == "" {
		response.BadRequest(c, "Invalid source_type")
		return
	}
	if sourceType != "postgres" && sourceType != "redis" {
		response.BadRequest(c, "source_type must be postgres or redis")
		return
	}

	h.respondAgentUnavailable(c)
}

// CreateSourceProfile validates the postgres or redis selector and legacy creation
// JSON before checking availability. Source and binding errors return bad request;
// accepted profile data is retained only in this request before retirement reporting.
func (h *DataManagementHandler) CreateSourceProfile(c *gin.Context) {
	sourceType := strings.TrimSpace(c.Param("source_type"))
	if sourceType != "postgres" && sourceType != "redis" {
		response.BadRequest(c, "source_type must be postgres or redis")
		return
	}

	var req CreateSourceProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	h.respondAgentUnavailable(c)
}

// UpdateSourceProfile requires an accepted source selector, a nonblank trimmed
// profile route ID, and bindable legacy update data. Those checks precede agent
// availability, so malformed input can return bad request before the retirement response.
func (h *DataManagementHandler) UpdateSourceProfile(c *gin.Context) {
	sourceType := strings.TrimSpace(c.Param("source_type"))
	if sourceType != "postgres" && sourceType != "redis" {
		response.BadRequest(c, "source_type must be postgres or redis")
		return
	}
	profileID := strings.TrimSpace(c.Param("profile_id"))
	if profileID == "" {
		response.BadRequest(c, "Invalid profile_id")
		return
	}

	var req UpdateSourceProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	h.respondAgentUnavailable(c)
}

// DeleteSourceProfile validates the source selector and nonblank trimmed route ID
// before checking availability. The bound retired facade returns deprecation,
// leaving the profile identifier as request input without performing a deletion.
func (h *DataManagementHandler) DeleteSourceProfile(c *gin.Context) {
	sourceType := strings.TrimSpace(c.Param("source_type"))
	if sourceType != "postgres" && sourceType != "redis" {
		response.BadRequest(c, "source_type must be postgres or redis")
		return
	}
	profileID := strings.TrimSpace(c.Param("profile_id"))
	if profileID == "" {
		response.BadRequest(c, "Invalid profile_id")
		return
	}

	h.respondAgentUnavailable(c)
}

// SetActiveSourceProfile accepts postgres or redis plus a nonblank trimmed profile
// route ID before checking availability. Accepted requests reach the retired
// facade response; this handler does not change the active source profile.
func (h *DataManagementHandler) SetActiveSourceProfile(c *gin.Context) {
	sourceType := strings.TrimSpace(c.Param("source_type"))
	if sourceType != "postgres" && sourceType != "redis" {
		response.BadRequest(c, "source_type must be postgres or redis")
		return
	}
	profileID := strings.TrimSpace(c.Param("profile_id"))
	if profileID == "" {
		response.BadRequest(c, "Invalid profile_id")
		return
	}

	h.respondAgentUnavailable(c)
}

// ListS3Profiles delegates the retired profile-list request to its availability
// response. An absent service and the bound retired facade provide their respective
// unconfigured or deprecation reasons before any profile data can be returned.
func (h *DataManagementHandler) ListS3Profiles(c *gin.Context) {
	h.respondAgentUnavailable(c)
}

// CreateS3Profile binds the legacy remote-profile request before checking service
// availability. Missing required name or profile ID and malformed binding return
// bad request; accepted data reaches retirement reporting without profile persistence.
func (h *DataManagementHandler) CreateS3Profile(c *gin.Context) {
	var req CreateS3ProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	h.respondAgentUnavailable(c)
}

// UpdateS3Profile binds the legacy remote settings and validates a nonblank trimmed
// route profile ID before checking availability. The binding check occurs first,
// preserving which malformed request error is returned when both inputs are unusable.
func (h *DataManagementHandler) UpdateS3Profile(c *gin.Context) {
	var req UpdateS3ProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	profileID := strings.TrimSpace(c.Param("profile_id"))
	if profileID == "" {
		response.BadRequest(c, "Invalid profile_id")
		return
	}

	h.respondAgentUnavailable(c)
}

// DeleteS3Profile validates a nonblank trimmed profile route ID before checking
// agent availability. Accepted IDs reach the retired facade response, and this
// compatibility handler performs no remote-profile deletion itself.
func (h *DataManagementHandler) DeleteS3Profile(c *gin.Context) {
	profileID := strings.TrimSpace(c.Param("profile_id"))
	if profileID == "" {
		response.BadRequest(c, "Invalid profile_id")
		return
	}

	h.respondAgentUnavailable(c)
}

// SetActiveS3Profile validates the trimmed route profile ID and then checks agent
// availability. Empty identifiers return bad request; accepted requests receive
// the retired facade response before any active-profile selection can be applied.
func (h *DataManagementHandler) SetActiveS3Profile(c *gin.Context) {
	profileID := strings.TrimSpace(c.Param("profile_id"))
	if profileID == "" {
		response.BadRequest(c, "Invalid profile_id")
		return
	}

	h.respondAgentUnavailable(c)
}

// ListBackupJobs returns the service availability response for the retired job-list
// route. The bound facade reports deprecation, while a zero-value handler reports
// an absent service instead of retrieving archive jobs from an agent.
func (h *DataManagementHandler) ListBackupJobs(c *gin.Context) {
	h.respondAgentUnavailable(c)
}

// GetBackupJob accepts a nonblank trimmed job route ID before checking availability.
// Missing identifiers return bad request; accepted identifiers reach the retired
// facade response and are not used to retrieve an agent job.
func (h *DataManagementHandler) GetBackupJob(c *gin.Context) {
	jobID := strings.TrimSpace(c.Param("job_id"))
	if jobID == "" {
		response.BadRequest(c, "Invalid backup job ID")
		return
	}

	h.respondAgentUnavailable(c)
}

// respondAgentUnavailable writes the absent-service error or the bound availability
// error after legacy request validation. A nil result from EnsureAgentEnabled
// returns without writing a response; the production retired facade returns its
// deprecation error and diagnostic socket metadata instead.
func (h *DataManagementHandler) respondAgentUnavailable(c *gin.Context) {
	if h.dataManagementService == nil {
		err := infraerrors.ServiceUnavailable(
			backup.DataManagementAgentUnavailableReason,
			"data management agent service is not configured",
		).WithMetadata(map[string]string{"socket_path": backup.DefaultDataManagementAgentSocketPath})
		response.ErrorFrom(c, err)
		return
	}

	if err := h.dataManagementService.EnsureAgentEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}
}

// getAgentHealth obtains health from the bound facade or supplies disabled default
// metadata for a nil service interface. The fallback includes the unconfigured
// reason and default socket path, allowing GetAgentHealth to return its administrator health payload.
func (h *DataManagementHandler) getAgentHealth(c *gin.Context) backup.DataManagementAgentHealth {
	// An absent service has no facade to supply diagnostic metadata. Keep health
	// queryable by reporting disabled defaults that GetAgentHealth can return in its success envelope.
	if h.dataManagementService == nil {
		return backup.DataManagementAgentHealth{
			Enabled:    false,
			Reason:     backup.DataManagementAgentUnavailableReason,
			SocketPath: backup.DefaultDataManagementAgentSocketPath,
		}
	}
	return h.dataManagementService.GetAgentHealth(c.Request.Context())
}
