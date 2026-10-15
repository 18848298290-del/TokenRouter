package httpapi

import (
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// crs.go binds CRS synchronization and preview requests to the provider import
// use case.
// CRS synchronization can update existing providers and create selected source
// entries, with proxy synchronization enabled by default. The handler preserves
// nil versus empty selections when delegating to CRSSync, and preview reads
// source data before any synchronization request is applied.

// CRSHandler binds administrator source credentials to provider synchronization
// and preview use cases.
type CRSHandler struct {
	core *provider.CRSSync // borrowed source synchronization use case
}

// SyncFromCRSRequest carries source login, proxy policy and optional
// source-provider selection.
type SyncFromCRSRequest struct {
	BaseURL  string `json:"base_url" binding:"required"` // source CRS root URL
	Username string `json:"username" binding:"required"` // source administrator login name
	Password string `json:"password" binding:"required"` // source administrator login password
	// SyncProxies defaults to true when omitted. An explicit false disables proxy
	// synchronization while provider source handling remains delegated to CRSSync.
	SyncProxies *bool `json:"sync_proxies"` // source proxy synchronization policy
	// SelectedProviderIDs distinguishes nil from an explicit empty slice. Nil
	// admits all new source entries; empty selection updates existing providers
	// and creates no selected new entries.
	SelectedProviderIDs []string `json:"selected_provider_ids"` // selected source provider identifiers
}

// PreviewFromCRSRequest carries source login values for a read-only
// synchronization preview.
type PreviewFromCRSRequest struct {
	BaseURL  string `json:"base_url" binding:"required"` // source CRS root URL
	Username string `json:"username" binding:"required"` // source administrator login name
	Password string `json:"password" binding:"required"` // source administrator login password
}

// NewCRSHandler borrows the CRS synchronization use case for administrator
// source operations. The caller keeps its provider store, proxy store and
// source exporter usable while mounted requests execute.
func NewCRSHandler(core *provider.CRSSync) *CRSHandler { return &CRSHandler{core: core} }

// SyncFromCRS binds source login and selection and requests provider
// synchronization. Omitted sync_proxies defaults true; binding failures return
// 400 and top-level use-case failures use the endpoint's 500 message.
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

// PreviewFromCRS binds source login values and returns the synchronization
// preview. Binding failures return 400; the use case inspects source entries
// and maps top-level preview errors to the endpoint's 500 message.
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
