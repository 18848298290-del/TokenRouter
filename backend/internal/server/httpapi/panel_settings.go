package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"

	serverdto "github.com/TokenFlux/TokenRouter/internal/server/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
	"github.com/TokenFlux/TokenRouter/internal/server/runtimeconfig"
)

// PanelSettingsStore reads and saves panel rate limits.
type PanelSettingsStore interface {
	GetPanelRateLimitSettings(context.Context) (*runtimeconfig.PanelRateLimitSettings, error)
	SetPanelRateLimitSettings(context.Context, *runtimeconfig.PanelRateLimitSettings) error
}

// PanelSettingsHandler handles admin requests for panel rate limits.
type PanelSettingsHandler struct{ settingService PanelSettingsStore }

// UpdatePanelRateLimitSettingsRequest carries panel API rate-limit settings.
type UpdatePanelRateLimitSettingsRequest struct {
	Enabled     bool `json:"enabled"`
	UserRPM     int  `json:"user_rpm"`
	HeavyRPM    int  `json:"heavy_rpm"`
	ExemptAdmin bool `json:"exempt_admin"`
	PublicIPRPM int  `json:"public_ip_rpm"`
}

// NewPanelSettingsHandler uses the supplied shared panel settings instance.
func NewPanelSettingsHandler(service *runtimeconfig.PanelSettings) *PanelSettingsHandler {
	return &PanelSettingsHandler{settingService: service}
}

// GetPanelRateLimitSettings returns panel API rate limits.
// GET /api/v1/admin/settings/panel-rate-limit
func (h *PanelSettingsHandler) GetPanelRateLimitSettings(c *gin.Context) {
	settings, err := h.settingService.GetPanelRateLimitSettings(c.Request.Context())
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	httpx.Success(c, serverdto.PanelRateLimitSettings{
		Enabled:     settings.Enabled,
		UserRPM:     settings.UserRPM,
		HeavyRPM:    settings.HeavyRPM,
		ExemptAdmin: settings.ExemptAdmin,
		PublicIPRPM: settings.PublicIPRPM,
	})
}

// UpdatePanelRateLimitSettings saves panel API rate limits and returns the stored values.
// PUT /api/v1/admin/settings/panel-rate-limit
func (h *PanelSettingsHandler) UpdatePanelRateLimitSettings(c *gin.Context) {
	var req UpdatePanelRateLimitSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	settings := &runtimeconfig.PanelRateLimitSettings{
		Enabled:     req.Enabled,
		UserRPM:     req.UserRPM,
		HeavyRPM:    req.HeavyRPM,
		ExemptAdmin: req.ExemptAdmin,
		PublicIPRPM: req.PublicIPRPM,
	}

	if err := h.settingService.SetPanelRateLimitSettings(c.Request.Context(), settings); err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}

	updatedSettings, err := h.settingService.GetPanelRateLimitSettings(c.Request.Context())
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	httpx.Success(c, serverdto.PanelRateLimitSettings{
		Enabled:     updatedSettings.Enabled,
		UserRPM:     updatedSettings.UserRPM,
		HeavyRPM:    updatedSettings.HeavyRPM,
		ExemptAdmin: updatedSettings.ExemptAdmin,
		PublicIPRPM: updatedSettings.PublicIPRPM,
	})
}
