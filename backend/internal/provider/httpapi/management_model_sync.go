package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// SyncUpstreamModels fetches the provider's currently supported models from upstream.
// POST /api/v1/admin/providers/:id/models/sync-upstream
func (h *ManagementHandler) SyncUpstreamModels(c *gin.Context) {
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return
	}

	provider, err := h.adminService.GetProvider(c.Request.Context(), providerID)
	if err != nil {
		response.NotFound(c, "Provider not found")
		return
	}

	if h.models == nil {
		response.InternalError(c, "Provider test service is not configured")
		return
	}

	models, err := h.models.Fetch(c.Request.Context(), provider)
	if err != nil {
		if syncErr, ok := errors.AsType[*providercore.UpstreamModelSyncError](err); ok {
			switch syncErr.Kind {
			case providercore.UpstreamModelSyncErrorConfiguration, providercore.UpstreamModelSyncErrorUnsupported:
				response.BadRequest(c, syncErr.SafeMessage())
			default:
				slog.Warn("sync_upstream_models_failed", "provider_id", providerID, "kind", syncErr.Kind)
				response.Error(c, http.StatusBadGateway, syncErr.SafeMessage())
			}
			return
		}

		slog.Warn("sync_upstream_models_failed", "provider_id", providerID)
		response.Error(c, http.StatusBadGateway, "Failed to sync upstream models from upstream")
		return
	}

	response.Success(c, gin.H{"models": models})
}

// SyncUpstreamModelsPreview fetches supported models using credentials for an unsaved provider.
// POST /api/v1/admin/providers/models/sync-upstream-preview
func (h *ManagementHandler) SyncUpstreamModelsPreview(c *gin.Context) {
	var req struct {
		Platform string `json:"platform" binding:"required"`
		Type     string `json:"type" binding:"required"`
		BaseURL  string `json:"base_url"`
		APIKey   string `json:"api_key" binding:"required"`
	}
	if err := response.BindJSONStrict(c, &req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	tempProvider := &providercore.Record{
		Platform: req.Platform,
		Type:     req.Type,
		Credentials: map[string]any{
			"api_key":  req.APIKey,
			"base_url": req.BaseURL,
		},
	}

	if h.models == nil {
		response.InternalError(c, "Provider test service is not configured")
		return
	}

	models, err := h.models.Fetch(c.Request.Context(), tempProvider)
	if err != nil {
		if syncErr, ok := errors.AsType[*providercore.UpstreamModelSyncError](err); ok {
			switch syncErr.Kind {
			case providercore.UpstreamModelSyncErrorConfiguration, providercore.UpstreamModelSyncErrorUnsupported:
				response.BadRequest(c, syncErr.SafeMessage())
			default:
				slog.Warn("sync_upstream_models_preview_failed", "platform", req.Platform, "kind", syncErr.Kind)
				response.Error(c, http.StatusBadGateway, syncErr.SafeMessage())
			}
			return
		}

		slog.Warn("sync_upstream_models_preview_failed", "platform", req.Platform)
		response.Error(c, http.StatusBadGateway, "Failed to sync upstream models from upstream")
		return
	}

	response.Success(c, gin.H{"models": models})
}
