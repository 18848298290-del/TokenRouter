package httpapi

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/backup"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

type DataManagementHandler struct {
	dataManagementService dataManagementService
}

type dataManagementService interface {
	EnsureAgentEnabled(ctx context.Context) error
	GetAgentHealth(ctx context.Context) backup.DataManagementAgentHealth
}

type TestS3ConnectionRequest struct {
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region" binding:"required"`
	Bucket          string `json:"bucket" binding:"required"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	Prefix          string `json:"prefix"`
	ForcePathStyle  bool   `json:"force_path_style"`
	UseSSL          bool   `json:"use_ssl"`
}

type CreateBackupJobRequest struct {
	BackupType     string `json:"backup_type" binding:"required,oneof=postgres redis full"`
	UploadToS3     bool   `json:"upload_to_s3"`
	S3ProfileID    string `json:"s3_profile_id"`
	PostgresID     string `json:"postgres_profile_id"`
	RedisID        string `json:"redis_profile_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

type CreateSourceProfileRequest struct {
	ProfileID string                            `json:"profile_id" binding:"required"`
	Name      string                            `json:"name" binding:"required"`
	Config    backup.DataManagementSourceConfig `json:"config" binding:"required"`
	SetActive bool                              `json:"set_active"`
}

type UpdateSourceProfileRequest struct {
	Name   string                            `json:"name" binding:"required"`
	Config backup.DataManagementSourceConfig `json:"config" binding:"required"`
}

type CreateS3ProfileRequest struct {
	ProfileID       string `json:"profile_id" binding:"required"`
	Name            string `json:"name" binding:"required"`
	Enabled         bool   `json:"enabled"`
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	Bucket          string `json:"bucket"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	Prefix          string `json:"prefix"`
	ForcePathStyle  bool   `json:"force_path_style"`
	UseSSL          bool   `json:"use_ssl"`
	SetActive       bool   `json:"set_active"`
}

type UpdateS3ProfileRequest struct {
	Name            string `json:"name" binding:"required"`
	Enabled         bool   `json:"enabled"`
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	Bucket          string `json:"bucket"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	Prefix          string `json:"prefix"`
	ForcePathStyle  bool   `json:"force_path_style"`
	UseSSL          bool   `json:"use_ssl"`
}

func NewDataManagementHandler(dataManagementService *backup.DataManagementService) *DataManagementHandler {
	return &DataManagementHandler{dataManagementService: dataManagementService}
}

func (h *DataManagementHandler) GetAgentHealth(c *gin.Context) {
	health := h.getAgentHealth(c)
	payload := gin.H{
		"enabled":     health.Enabled,
		"reason":      health.Reason,
		"socket_path": health.SocketPath,
	}
	response.Success(c, payload)
}

func (h *DataManagementHandler) GetConfig(c *gin.Context) {
	h.respondAgentUnavailable(c)
}

func (h *DataManagementHandler) UpdateConfig(c *gin.Context) {
	var req backup.DataManagementConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	h.respondAgentUnavailable(c)
}

func (h *DataManagementHandler) TestS3(c *gin.Context) {
	var req TestS3ConnectionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	h.respondAgentUnavailable(c)
}

func (h *DataManagementHandler) CreateBackupJob(c *gin.Context) {
	var req CreateBackupJobRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	h.respondAgentUnavailable(c)
}

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

func (h *DataManagementHandler) ListS3Profiles(c *gin.Context) {
	h.respondAgentUnavailable(c)
}

func (h *DataManagementHandler) CreateS3Profile(c *gin.Context) {
	var req CreateS3ProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	h.respondAgentUnavailable(c)
}

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

func (h *DataManagementHandler) DeleteS3Profile(c *gin.Context) {
	profileID := strings.TrimSpace(c.Param("profile_id"))
	if profileID == "" {
		response.BadRequest(c, "Invalid profile_id")
		return
	}

	h.respondAgentUnavailable(c)
}

func (h *DataManagementHandler) SetActiveS3Profile(c *gin.Context) {
	profileID := strings.TrimSpace(c.Param("profile_id"))
	if profileID == "" {
		response.BadRequest(c, "Invalid profile_id")
		return
	}

	h.respondAgentUnavailable(c)
}

func (h *DataManagementHandler) ListBackupJobs(c *gin.Context) {
	h.respondAgentUnavailable(c)
}

func (h *DataManagementHandler) GetBackupJob(c *gin.Context) {
	jobID := strings.TrimSpace(c.Param("job_id"))
	if jobID == "" {
		response.BadRequest(c, "Invalid backup job ID")
		return
	}

	h.respondAgentUnavailable(c)
}

// respondAgentUnavailable reports an unavailable or retired data-management service.
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

func (h *DataManagementHandler) getAgentHealth(c *gin.Context) backup.DataManagementAgentHealth {
	if h.dataManagementService == nil {
		return backup.DataManagementAgentHealth{
			Enabled:    false,
			Reason:     backup.DataManagementAgentUnavailableReason,
			SocketPath: backup.DefaultDataManagementAgentSocketPath,
		}
	}
	return h.dataManagementService.GetAgentHealth(c.Request.Context())
}
