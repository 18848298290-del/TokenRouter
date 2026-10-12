package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/idempotency"
	idemhttp "github.com/TokenFlux/TokenRouter/internal/idempotency/httpapi"
	middleware2 "github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/ops/maintenance"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// SystemHandler handles version queries, updates, rollbacks, and restart requests.
type SystemHandler struct {
	idemhttp.Executor
	updateSvc  systemUpdateService
	operations *maintenance.Operations
}

type systemUpdateService interface {
	CheckUpdate(ctx context.Context, force bool) (*ops.UpdateInfo, error)
	PerformUpdate(ctx context.Context) error
	Rollback() error
	ListRollbackVersions(ctx context.Context) ([]ops.RollbackVersion, error)
	RollbackToVersion(ctx context.Context, version string) error
}

// NewSystemRuntimeHandler uses the maintenance instance registered with the application lifecycle.
func NewSystemRuntimeHandler(update systemUpdateService, operations *maintenance.Operations) *SystemHandler {
	return &SystemHandler{updateSvc: update, operations: operations}
}

// GetVersion returns the current version
// GET /api/v1/admin/system/version
func (h *SystemHandler) GetVersion(c *gin.Context) {
	info, _ := h.updateSvc.CheckUpdate(c.Request.Context(), false)
	response.Success(c, gin.H{
		"version": info.CurrentVersion,
	})
}

// CheckUpdates checks for available updates
// GET /api/v1/admin/system/check-updates
func (h *SystemHandler) CheckUpdates(c *gin.Context) {
	force := c.Query("force") == "true"
	info, err := h.updateSvc.CheckUpdate(c.Request.Context(), force)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	response.Success(c, info)
}

// PerformUpdate downloads and applies the update
// POST /api/v1/admin/system/update
func (h *SystemHandler) PerformUpdate(c *gin.Context) {
	operationID := buildSystemOperationID(c, "update")
	payload := gin.H{"operation_id": operationID}
	h.ExecuteAdminIdempotentJSON(c, "admin.system.update", payload, h.DefaultSystemOperationIdempotencyTTL(), func(ctx context.Context) (any, error) {
		return h.operations.Update(ctx, operationID)
	})
}

// GetRollbackVersions returns versions eligible for rollback.
// GET /api/v1/admin/system/rollback-versions
func (h *SystemHandler) GetRollbackVersions(c *gin.Context) {
	versions, err := h.updateSvc.ListRollbackVersions(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	response.Success(c, gin.H{
		"versions": versions,
	})
}

// Rollback restores a previous version.
// An empty body or version restores the .backup left by an in-place update.
// A version such as {"version":"x.y.z"} downloads and installs that allowed release.
// POST /api/v1/admin/system/rollback
func (h *SystemHandler) Rollback(c *gin.Context) {
	var req struct {
		Version string `json:"version"`
	}
	if c.Request.Body != nil && c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
			response.Error(c, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	targetVersion := strings.TrimSpace(req.Version)

	operation := "rollback"
	if targetVersion != "" {
		operation = "rollback:" + targetVersion
	}
	operationID := buildSystemOperationID(c, operation)
	payload := gin.H{"operation_id": operationID, "version": targetVersion}
	h.ExecuteAdminIdempotentJSON(c, "admin.system.rollback", payload, h.DefaultSystemOperationIdempotencyTTL(), func(ctx context.Context) (any, error) {
		return h.operations.Rollback(ctx, operationID, targetVersion)
	})
}

// RestartService requests an application restart.
// POST /api/v1/admin/system/restart
func (h *SystemHandler) RestartService(c *gin.Context) {
	operationID := buildSystemOperationID(c, "restart")
	payload := gin.H{"operation_id": operationID}
	h.ExecuteAdminIdempotentJSON(c, "admin.system.restart", payload, h.DefaultSystemOperationIdempotencyTTL(), func(ctx context.Context) (any, error) {
		return h.operations.Restart(ctx, operationID)
	})
}

func buildSystemOperationID(c *gin.Context, operation string) string {
	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if key == "" {
		return "sysop-" + operation + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	actorScope := "admin:0"
	if subject, ok := middleware2.GetAuthSubjectFromContext(c); ok {
		actorScope = "admin:" + strconv.FormatInt(subject.UserID, 10)
	}
	seed := operation + "|" + actorScope + "|" + c.FullPath() + "|" + key
	hash := idempotency.HashIdempotencyKey(seed)
	if len(hash) > 24 {
		hash = hash[:24]
	}
	return "sysop-" + hash
}
