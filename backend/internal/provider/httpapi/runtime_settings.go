package httpapi

import (
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	providerdto "github.com/TokenFlux/TokenRouter/internal/provider/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// RuntimeSettingsHandler handles administrator requests through provider.RuntimeSettings.
type RuntimeSettingsHandler struct{ settingService *provider.RuntimeSettings }

// UpdateOverloadCooldownSettingsRequest contains overload cooldown settings.
type UpdateOverloadCooldownSettingsRequest struct {
	Enabled         bool `json:"enabled"`
	CooldownMinutes int  `json:"cooldown_minutes"`
}

// UpdateRateLimit429CooldownSettingsRequest contains 429 cooldown settings.
type UpdateRateLimit429CooldownSettingsRequest struct {
	Enabled         bool `json:"enabled"`
	CooldownSeconds int  `json:"cooldown_seconds"`
}

// UpdateOpenAIImagesOAuthUnavailableCooldownSettingsRequest contains cooldown settings for OAuth image unavailability.
type UpdateOpenAIImagesOAuthUnavailableCooldownSettingsRequest struct {
	CooldownMinutes int `json:"cooldown_minutes"`
}

// UpdateStreamTimeoutSettingsRequest contains streaming timeout settings.
type UpdateStreamTimeoutSettingsRequest struct {
	Enabled                bool   `json:"enabled"`
	Action                 string `json:"action"`
	TempUnschedMinutes     int    `json:"temp_unsched_minutes"`
	ThresholdCount         int    `json:"threshold_count"`
	ThresholdWindowMinutes int    `json:"threshold_window_minutes"`
}

// UpdateOpenAI403CooldownSettingsRequest contains OpenAI 403 cooldown settings.
type UpdateOpenAI403CooldownSettingsRequest struct {
	Enabled                 bool  `json:"enabled"`
	CooldownMinutes         int   `json:"cooldown_minutes"`
	ErrorOnThresholdEnabled *bool `json:"error_on_threshold_enabled"`
	ThresholdCount          *int  `json:"threshold_count"`
	ThresholdWindowMinutes  *int  `json:"threshold_window_minutes"`
}

// NewRuntimeSettingsHandler binds the shared provider settings service and its cache.
func NewRuntimeSettingsHandler(service *provider.RuntimeSettings) *RuntimeSettingsHandler {
	return &RuntimeSettingsHandler{settingService: service}
}

// GetOverloadCooldownSettings returns overload cooldown settings.
func (h *RuntimeSettingsHandler) GetOverloadCooldownSettings(c *gin.Context) {
	settings, err := h.settingService.GetOverloadCooldownSettings(c.Request.Context())
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	httpx.Success(c, providerdto.OverloadCooldownSettings{
		Enabled:         settings.Enabled,
		CooldownMinutes: settings.CooldownMinutes,
	})
}

// UpdateOverloadCooldownSettings validates and saves overload cooldown settings.
func (h *RuntimeSettingsHandler) UpdateOverloadCooldownSettings(c *gin.Context) {
	var req UpdateOverloadCooldownSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	settings := &provider.OverloadCooldownSettings{
		Enabled:         req.Enabled,
		CooldownMinutes: req.CooldownMinutes,
	}

	if err := h.settingService.SetOverloadCooldownSettings(c.Request.Context(), settings); err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}

	updatedSettings, err := h.settingService.GetOverloadCooldownSettings(c.Request.Context())
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	httpx.Success(c, providerdto.OverloadCooldownSettings{
		Enabled:         updatedSettings.Enabled,
		CooldownMinutes: updatedSettings.CooldownMinutes,
	})
}

// GetRateLimit429CooldownSettings returns 429 cooldown settings.
func (h *RuntimeSettingsHandler) GetRateLimit429CooldownSettings(c *gin.Context) {
	settings, err := h.settingService.GetRateLimit429CooldownSettings(c.Request.Context())
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	httpx.Success(c, providerdto.RateLimit429CooldownSettings{
		Enabled:         settings.Enabled,
		CooldownSeconds: settings.CooldownSeconds,
	})
}

// UpdateRateLimit429CooldownSettings validates and saves 429 cooldown settings.
func (h *RuntimeSettingsHandler) UpdateRateLimit429CooldownSettings(c *gin.Context) {
	var req UpdateRateLimit429CooldownSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	settings := &provider.RateLimit429CooldownSettings{
		Enabled:         req.Enabled,
		CooldownSeconds: req.CooldownSeconds,
	}

	if err := h.settingService.SetRateLimit429CooldownSettings(c.Request.Context(), settings); err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}

	updatedSettings, err := h.settingService.GetRateLimit429CooldownSettings(c.Request.Context())
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	httpx.Success(c, providerdto.RateLimit429CooldownSettings{
		Enabled:         updatedSettings.Enabled,
		CooldownSeconds: updatedSettings.CooldownSeconds,
	})
}

// GetOpenAIImagesOAuthUnavailableCooldownSettings returns cooldown settings for OAuth image unavailability.
func (h *RuntimeSettingsHandler) GetOpenAIImagesOAuthUnavailableCooldownSettings(c *gin.Context) {
	settings, err := h.settingService.GetOpenAIImagesOAuthUnavailableCooldownSettings(c.Request.Context())
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	httpx.Success(c, providerdto.OpenAIImagesOAuthUnavailableCooldownSettings{CooldownMinutes: settings.CooldownMinutes})
}

// UpdateOpenAIImagesOAuthUnavailableCooldownSettings validates and saves cooldown settings for OAuth image unavailability.
func (h *RuntimeSettingsHandler) UpdateOpenAIImagesOAuthUnavailableCooldownSettings(c *gin.Context) {
	var req UpdateOpenAIImagesOAuthUnavailableCooldownSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	settings := &provider.OpenAIImagesOAuthUnavailableCooldownSettings{CooldownMinutes: req.CooldownMinutes}
	if err := h.settingService.SetOpenAIImagesOAuthUnavailableCooldownSettings(c.Request.Context(), settings); err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	httpx.Success(c, providerdto.OpenAIImagesOAuthUnavailableCooldownSettings{CooldownMinutes: settings.CooldownMinutes})
}

// GetStreamTimeoutSettings returns streaming timeout settings.
func (h *RuntimeSettingsHandler) GetStreamTimeoutSettings(c *gin.Context) {
	settings, err := h.settingService.GetStreamTimeoutSettings(c.Request.Context())
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	httpx.Success(c, providerdto.StreamTimeoutSettings{
		Enabled:                settings.Enabled,
		Action:                 settings.Action,
		TempUnschedMinutes:     settings.TempUnschedMinutes,
		ThresholdCount:         settings.ThresholdCount,
		ThresholdWindowMinutes: settings.ThresholdWindowMinutes,
	})
}

// UpdateStreamTimeoutSettings validates and saves streaming timeout settings.
func (h *RuntimeSettingsHandler) UpdateStreamTimeoutSettings(c *gin.Context) {
	var req UpdateStreamTimeoutSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	settings := &provider.StreamTimeoutSettings{
		Enabled:                req.Enabled,
		Action:                 req.Action,
		TempUnschedMinutes:     req.TempUnschedMinutes,
		ThresholdCount:         req.ThresholdCount,
		ThresholdWindowMinutes: req.ThresholdWindowMinutes,
	}

	if err := h.settingService.SetStreamTimeoutSettings(c.Request.Context(), settings); err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}

	// Reload settings for the response after validation and persistence.
	updatedSettings, err := h.settingService.GetStreamTimeoutSettings(c.Request.Context())
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	httpx.Success(c, providerdto.StreamTimeoutSettings{
		Enabled:                updatedSettings.Enabled,
		Action:                 updatedSettings.Action,
		TempUnschedMinutes:     updatedSettings.TempUnschedMinutes,
		ThresholdCount:         updatedSettings.ThresholdCount,
		ThresholdWindowMinutes: updatedSettings.ThresholdWindowMinutes,
	})
}

// GetOpenAI403CooldownSettings returns OpenAI 403 cooldown settings.
func (h *RuntimeSettingsHandler) GetOpenAI403CooldownSettings(c *gin.Context) {
	settings, err := h.settingService.GetOpenAI403CooldownSettings(c.Request.Context())
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	httpx.Success(c, providerdto.OpenAI403CooldownSettings{
		Enabled:                 settings.Enabled,
		CooldownMinutes:         settings.CooldownMinutes,
		ErrorOnThresholdEnabled: settings.ErrorOnThresholdEnabled,
		ThresholdCount:          settings.ThresholdCount,
		ThresholdWindowMinutes:  settings.ThresholdWindowMinutes,
	})
}

// UpdateOpenAI403CooldownSettings validates and saves OpenAI 403 cooldown settings.
func (h *RuntimeSettingsHandler) UpdateOpenAI403CooldownSettings(c *gin.Context) {
	var req UpdateOpenAI403CooldownSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	defaults := provider.DefaultOpenAI403CooldownSettings()
	errorOnThresholdEnabled := defaults.ErrorOnThresholdEnabled
	if req.ErrorOnThresholdEnabled != nil {
		errorOnThresholdEnabled = *req.ErrorOnThresholdEnabled
	}
	thresholdCount := defaults.ThresholdCount
	if req.ThresholdCount != nil {
		thresholdCount = *req.ThresholdCount
	}
	thresholdWindowMinutes := defaults.ThresholdWindowMinutes
	if req.ThresholdWindowMinutes != nil {
		thresholdWindowMinutes = *req.ThresholdWindowMinutes
	}

	settings := &provider.OpenAI403CooldownSettings{
		Enabled:                 req.Enabled,
		CooldownMinutes:         req.CooldownMinutes,
		ErrorOnThresholdEnabled: errorOnThresholdEnabled,
		ThresholdCount:          thresholdCount,
		ThresholdWindowMinutes:  thresholdWindowMinutes,
	}

	if err := h.settingService.SetOpenAI403CooldownSettings(c.Request.Context(), settings); err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}

	updatedSettings, err := h.settingService.GetOpenAI403CooldownSettings(c.Request.Context())
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	httpx.Success(c, providerdto.OpenAI403CooldownSettings{
		Enabled:                 updatedSettings.Enabled,
		CooldownMinutes:         updatedSettings.CooldownMinutes,
		ErrorOnThresholdEnabled: updatedSettings.ErrorOnThresholdEnabled,
		ThresholdCount:          updatedSettings.ThresholdCount,
		ThresholdWindowMinutes:  updatedSettings.ThresholdWindowMinutes,
	})
}
