package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"

	serverdto "github.com/TokenFlux/TokenRouter/internal/server/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
	"github.com/TokenFlux/TokenRouter/internal/server/runtimeconfig"
)

// panel_settings.go reads and updates panel rate limits through administrative
// HTTP handlers.
// Panel limits are read directly for administrator display and persisted before
// the update response is assembled. Reading back the settings lets storage
// determine the displayed result; if that read fails, the handler returns an
// error even though the preceding save may have succeeded.

// PanelSettingsStore reads and persists the limits displayed by administrator handlers.
type PanelSettingsStore interface {
	// GetPanelRateLimitSettings reads storage with the supplied context and
	// returns usable defaults for a missing or malformed value. Other storage
	// failures are returned to the administrator handler.
	GetPanelRateLimitSettings(context.Context) (*runtimeconfig.PanelRateLimitSettings, error)
	// SetPanelRateLimitSettings validates the supplied settings and persists
	// them before updating the instance cache. Invalid values and storage errors
	// prevent the handler from proceeding to its readback.
	SetPanelRateLimitSettings(context.Context, *runtimeconfig.PanelRateLimitSettings) error
}

// PanelSettingsHandler reads and saves panel rate limits for administrator requests.
type PanelSettingsHandler struct {
	settingService PanelSettingsStore // shared panel settings reader and writer
}

// UpdatePanelRateLimitSettingsRequest contains the replacement values for the panel limiter.
// Ordinary bool and int fields use their zero values when omitted, so this
// endpoint treats its bound object as a full settings replacement.
type UpdatePanelRateLimitSettingsRequest struct {
	Enabled bool `json:"enabled"` // panel limit enable flag
	// UserRPM, HeavyRPM, and PublicIPRPM are requests per minute for their
	// respective buckets. Zero disables a bucket; PanelSettings rejects negative
	// values and values above its supported maximum before saving.
	UserRPM     int  `json:"user_rpm"`      // authenticated user's aggregate limit
	HeavyRPM    int  `json:"heavy_rpm"`     // authenticated user's heavy-query limit
	ExemptAdmin bool `json:"exempt_admin"`  // administrator user-bucket exemption flag
	PublicIPRPM int  `json:"public_ip_rpm"` // unauthenticated public-address limit
}

// NewPanelSettingsHandler binds the shared panel settings instance to admin handlers.
// Supply an initialized service before requests are served; the handler forwards
// request contexts to that instance for direct reads and validated writes.
func NewPanelSettingsHandler(service *runtimeconfig.PanelSettings) *PanelSettingsHandler {
	return &PanelSettingsHandler{settingService: service}
}

// GetPanelRateLimitSettings writes the current limits in the panel success envelope.
// The administrator route supplies authorization, and this handler reads storage
// with the request context. Storage failures are mapped through ErrorFrom.
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

// UpdatePanelRateLimitSettings saves a full replacement and returns readback values.
// Binding and save failures produce bad-request responses; a readback failure
// produces an error after the successful save. Omitted input fields take their
// bool or integer zero values during Gin binding.
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

	// Persistence has completed before this read. A failure here changes the
	// HTTP result but leaves the saved limits and instance cache available.
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
