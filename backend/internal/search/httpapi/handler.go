package httpapi

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/search"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

type Handler struct{ settingService *search.ConfigService }

func New(s *search.ConfigService) *Handler { return &Handler{settingService: s} }

// TestWebSearchEmulation runs an administrator search test without reserving quota.
// POST /api/v1/admin/settings/web-search-emulation/test
func (h *Handler) TestWebSearchEmulation(c *gin.Context) {
	var req struct {
		Query string `json:"query"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Query) == "" {
		req.Query = "搜索今年世界大事件"
	}

	result, err := search.TestWebSearch(c.Request.Context(), req.Query, h.settingService.Registry())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// ResetWebSearchUsage clears quota usage for the requested provider type.
// POST /api/v1/admin/settings/web-search-emulation/reset-usage
func (h *Handler) ResetWebSearchUsage(c *gin.Context) {
	var req struct {
		ProviderType string `json:"provider_type"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if req.ProviderType == "" {
		response.BadRequest(c, "provider_type is required")
		return
	}
	if err := search.ResetWebSearchUsage(c.Request.Context(), req.ProviderType, h.settingService.Registry()); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, nil)
}

// UpdateWebSearchEmulationConfig saves search settings and returns the current configuration with usage.
// PUT /api/v1/admin/settings/web-search-emulation
func (h *Handler) UpdateWebSearchEmulationConfig(c *gin.Context) {
	var cfg search.WebSearchEmulationConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	if err := h.settingService.SaveWebSearchEmulationConfig(c.Request.Context(), &cfg); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	// Read the published configuration, including API keys, before adding usage.
	updated, err := h.settingService.GetWebSearchEmulationConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, search.PopulateWebSearchUsage(c.Request.Context(), updated, h.settingService.Registry()))
}

// GetWebSearchEmulationConfig returns search settings, including API keys, with current usage.
// GET /api/v1/admin/settings/web-search-emulation
func (h *Handler) GetWebSearchEmulationConfig(c *gin.Context) {
	cfg, err := h.settingService.GetWebSearchEmulationConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, search.PopulateWebSearchUsage(c.Request.Context(), cfg, h.settingService.Registry()))
}
