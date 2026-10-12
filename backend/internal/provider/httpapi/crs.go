package httpapi

import (
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// CRSHandler binds administrator requests and delegates synchronization and per-item results to provider.CRSSync.
type CRSHandler struct{ core *provider.CRSSync }

type SyncFromCRSRequest struct {
	BaseURL             string   `json:"base_url" binding:"required"`
	Username            string   `json:"username" binding:"required"`
	Password            string   `json:"password" binding:"required"`
	SyncProxies         *bool    `json:"sync_proxies"`
	SelectedProviderIDs []string `json:"selected_provider_ids"`
}
type PreviewFromCRSRequest struct {
	BaseURL  string `json:"base_url" binding:"required"`
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

func NewCRSHandler(core *provider.CRSSync) *CRSHandler { return &CRSHandler{core: core} }

// SyncFromCRS synchronizes the selected CRS accounts for an administrator.
// POST /api/v1/admin/providers/sync/crs
func (h *CRSHandler) SyncFromCRS(c *gin.Context) {
	var req SyncFromCRSRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	// Proxy synchronization defaults to enabled; false disables it.
	syncProxies := true
	if req.SyncProxies != nil {
		syncProxies = *req.SyncProxies
	}

	result, err := h.core.SyncFromCRS(c.Request.Context(), provider.SyncFromCRSInput{
		BaseURL:             req.BaseURL,
		Username:            req.Username,
		Password:            req.Password,
		SyncProxies:         syncProxies,
		SelectedProviderIDs: req.SelectedProviderIDs,
	})
	if err != nil {
		// Return the error in the CRS endpoint's response format.
		response.InternalError(c, "CRS sync failed: "+err.Error())
		return
	}

	response.Success(c, result)
}

// PreviewFromCRS returns a preview before synchronization.
// POST /api/v1/admin/providers/sync/crs/preview
func (h *CRSHandler) PreviewFromCRS(c *gin.Context) {
	var req PreviewFromCRSRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	result, err := h.core.PreviewFromCRS(c.Request.Context(), provider.SyncFromCRSInput{
		BaseURL:  req.BaseURL,
		Username: req.Username,
		Password: req.Password,
	})
	if err != nil {
		response.InternalError(c, "CRS preview failed: "+err.Error())
		return
	}

	response.Success(c, result)
}
