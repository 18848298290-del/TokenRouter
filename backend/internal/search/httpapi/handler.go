package httpapi

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/search"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// handler.go serves administrative search configuration, quota reset, and
// test-search HTTP requests.
// Administrator search settings responses include configured API keys together
// with usage read from the current Manager. The same adapter exposes quota
// reset and a bounded test search; route-group authentication protects these
// credential-bearing configuration and execution operations.

// Handler adapts search configuration and administrator test operations to JSON
// responses.
type Handler struct {
	settingService *search.ConfigService // shared configuration runtime and registry
}

// New binds the search configuration service used by administrator settings
// handlers.
func New(s *search.ConfigService) *Handler { return &Handler{settingService: s} }

// TestWebSearchEmulation binds an administrator query and runs a fifteen-second
// test through the current Manager. Empty or whitespace-only queries select the
// built-in Chinese query; malformed JSON is rejected before testing.
//
// POST /api/v1/admin/settings/web-search-emulation/test
func (h *Handler) TestWebSearchEmulation(c *gin.Context) {
	var req WebSearchEmulationTestRequest
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

// ResetWebSearchUsage deletes the literal supplier counter selected by the JSON
// provider_type. An exactly empty type is rejected; other strings reach
// Manager's reset operation, and a missing Manager returns an error.
//
// POST /api/v1/admin/settings/web-search-emulation/reset-usage
func (h *Handler) ResetWebSearchUsage(c *gin.Context) {
	var req WebSearchUsageResetRequest
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

// UpdateWebSearchEmulationConfig validates and saves submitted search settings,
// then returns the published configuration with current usage. Exactly empty keys can
// retain stored credentials, and the response includes keys; later read
// failures are reported after persistence.
//
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

	// Read after the service publishes the saved settings so retained API keys and
	// the corresponding copied configuration are returned before usage decoration.
	updated, err := h.settingService.GetWebSearchEmulationConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, search.PopulateWebSearchUsage(c.Request.Context(), updated, h.settingService.Registry()))
}

// GetWebSearchEmulationConfig returns a configuration copy decorated with
// current Manager usage and key-presence flags. Configured API keys remain in
// the response, and service read failures map through the common HTTP error
// responder.
//
// GET /api/v1/admin/settings/web-search-emulation
func (h *Handler) GetWebSearchEmulationConfig(c *gin.Context) {
	cfg, err := h.settingService.GetWebSearchEmulationConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, search.PopulateWebSearchUsage(c.Request.Context(), cfg, h.settingService.Registry()))
}
