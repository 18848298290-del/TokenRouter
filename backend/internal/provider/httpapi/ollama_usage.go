package httpapi

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// OllamaUsageHandler adapts administrator requests to usage, session, and settings operations.
type OllamaUsageHandler struct {
	ollamaCloudUsage *provider.OllamaCloudUsageService
}

type ollamaCloudUsageSessionRequest struct {
	Session string `json:"session" binding:"required"`
}
type ollamaCloudUsageAutoRefreshRequest struct {
	Enabled *bool `json:"enabled" binding:"required"`
}

func NewOllamaUsageHandler(core *provider.OllamaCloudUsageService) *OllamaUsageHandler {
	return &OllamaUsageHandler{ollamaCloudUsage: core}
}

func (h *OllamaUsageHandler) GetOllamaCloudUsageSettings(c *gin.Context) {
	if h.ollamaCloudUsage == nil {
		response.ErrorFrom(c, provider.ErrOllamaCloudUsageUnavailable)
		return
	}
	settings, err := h.ollamaCloudUsage.GetSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}

func (h *OllamaUsageHandler) UpdateOllamaCloudUsageSettings(c *gin.Context) {
	if h.ollamaCloudUsage == nil {
		response.ErrorFrom(c, provider.ErrOllamaCloudUsageUnavailable)
		return
	}
	var req provider.OllamaCloudUsageSettings
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := h.ollamaCloudUsage.UpdateSettings(c.Request.Context(), &req); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	settings, err := h.ollamaCloudUsage.GetSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}

func (h *OllamaUsageHandler) GetOllamaCloudUsage(c *gin.Context) {
	if !h.requireOllamaCloudUsage(c) {
		return
	}
	providerID, ok := ollamaCloudUsageProviderID(c)
	if !ok {
		return
	}
	state, err := h.ollamaCloudUsage.GetState(c.Request.Context(), providerID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, state)
}

func (h *OllamaUsageHandler) SaveOllamaCloudUsageSession(c *gin.Context) {
	if !h.requireOllamaCloudUsage(c) {
		return
	}
	providerID, ok := ollamaCloudUsageProviderID(c)
	if !ok {
		return
	}
	var req ollamaCloudUsageSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	state, err := h.ollamaCloudUsage.SaveSession(c.Request.Context(), providerID, req.Session)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, state)
}

func (h *OllamaUsageHandler) DeleteOllamaCloudUsageSession(c *gin.Context) {
	if !h.requireOllamaCloudUsage(c) {
		return
	}
	providerID, ok := ollamaCloudUsageProviderID(c)
	if !ok {
		return
	}
	state, err := h.ollamaCloudUsage.DeleteSession(c.Request.Context(), providerID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, state)
}

func (h *OllamaUsageHandler) SetOllamaCloudUsageAutoRefresh(c *gin.Context) {
	if !h.requireOllamaCloudUsage(c) {
		return
	}
	providerID, ok := ollamaCloudUsageProviderID(c)
	if !ok {
		return
	}
	var req ollamaCloudUsageAutoRefreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	state, err := h.ollamaCloudUsage.SetAutoRefresh(c.Request.Context(), providerID, *req.Enabled)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, state)
}

func (h *OllamaUsageHandler) RefreshOllamaCloudUsage(c *gin.Context) {
	if !h.requireOllamaCloudUsage(c) {
		return
	}
	providerID, ok := ollamaCloudUsageProviderID(c)
	if !ok {
		return
	}
	state, err := h.ollamaCloudUsage.Refresh(c.Request.Context(), providerID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, state)
}

func (h *OllamaUsageHandler) requireOllamaCloudUsage(c *gin.Context) bool {
	if h != nil && h.ollamaCloudUsage != nil {
		return true
	}
	response.ErrorFrom(c, provider.ErrOllamaCloudUsageUnavailable)
	return false
}

func ollamaCloudUsageProviderID(c *gin.Context) (int64, bool) {
	if c == nil {
		return 0, false
	}
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || providerID <= 0 {
		response.BadRequest(c, "Invalid provider ID")
		return 0, false
	}
	return providerID, true
}
