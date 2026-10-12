package httpapi

import (
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	providerdto "github.com/TokenFlux/TokenRouter/internal/provider/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// Runtime health settings are validated and stored before the administrator
// receives their effective display values. Most update endpoints reread
// settings after persistence, so a readback failure can return an error after
// the write has completed; the image-unavailability endpoint returns its
// validated value directly.
//
// OpenAI 403 input preserves compatibility for omitted threshold controls by
// selecting defaults, while plain enablement and cooldown fields bind zero
// values when omitted. The shared RuntimeSettings use case owns validation and
// normalization. HTTP error mapping distinguishes rejected writes from read
// failures, and the response shapes retain each policy's duration units and
// threshold observations.

// RuntimeSettingsHandler binds administrator health-policy requests to the
// shared provider settings service.
type RuntimeSettingsHandler struct {
	settingService *provider.RuntimeSettings // borrowed shared health settings
}

// UpdateOverloadCooldownSettingsRequest carries enablement and cooldown
// duration for overload policy updates.
type UpdateOverloadCooldownSettingsRequest struct {
	Enabled         bool `json:"enabled"`          // configured policy enablement flag
	CooldownMinutes int  `json:"cooldown_minutes"` // minutes
}

// UpdateRateLimit429CooldownSettingsRequest carries enablement and cooldown
// duration for default 429 policy updates.
type UpdateRateLimit429CooldownSettingsRequest struct {
	Enabled         bool `json:"enabled"`          // configured policy enablement flag
	CooldownSeconds int  `json:"cooldown_seconds"` // seconds
}

// UpdateOpenAIImagesOAuthUnavailableCooldownSettingsRequest supplies the
// duration used after OAuth image-unavailability observations.
type UpdateOpenAIImagesOAuthUnavailableCooldownSettingsRequest struct {
	CooldownMinutes int `json:"cooldown_minutes"` // minutes
}

// UpdateStreamTimeoutSettingsRequest carries the stream-timeout action,
// avoidance duration and event threshold.
type UpdateStreamTimeoutSettingsRequest struct {
	Enabled                bool   `json:"enabled"`                  // configured policy enablement flag
	Action                 string `json:"action"`                   // stream-timeout action selector
	TempUnschedMinutes     int    `json:"temp_unsched_minutes"`     // minutes
	ThresholdCount         int    `json:"threshold_count"`          // observed-event count threshold
	ThresholdWindowMinutes int    `json:"threshold_window_minutes"` // minutes
}

// UpdateOpenAI403CooldownSettingsRequest carries cooldown policy and optional
// repeated-forbidden-response threshold controls.
type UpdateOpenAI403CooldownSettingsRequest struct {
	Enabled         bool `json:"enabled"`          // configured policy enablement flag
	CooldownMinutes int  `json:"cooldown_minutes"` // minutes
	// ErrorOnThresholdEnabled, ThresholdCount and ThresholdWindowMinutes use
	// pointer presence to preserve default selection for older clients. Omission
	// chooses DefaultOpenAI403CooldownSettings values; supplied false and zero
	// values reach service validation.
	ErrorOnThresholdEnabled *bool `json:"error_on_threshold_enabled"` // optional repeated-error escalation flag
	ThresholdCount          *int  `json:"threshold_count"`            // observed-event count threshold
	ThresholdWindowMinutes  *int  `json:"threshold_window_minutes"`   // minutes
}

// NewRuntimeSettingsHandler borrows the provider runtime settings service for
// administrator health-policy routes. The supplied instance owns default
// selection and storage access, and stays available while mounted reads or
// writes execute.
func NewRuntimeSettingsHandler(service *provider.RuntimeSettings) *RuntimeSettingsHandler {
	return &RuntimeSettingsHandler{settingService: service}
}

// GetOverloadCooldownSettings returns the effective overload cooldown policy
// from runtime settings. Read failures use the common domain mapping; the
// response encodes enablement and its duration in minutes.
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

// UpdateOverloadCooldownSettings binds the cooldown policy, saves it through
// validation and rereads the effective response. A setter failure returns 400;
// a readback failure reports an error after storage has completed.
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

// GetRateLimit429CooldownSettings returns the effective default 429 avoidance
// policy from runtime settings. Read failures use the common domain mapping;
// the response encodes enablement and its duration in seconds.
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

// UpdateRateLimit429CooldownSettings binds and validates default 429 avoidance
// settings before readback. Setter failures return 400, while readback failures
// use the shared mapping after the policy has already been saved.
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

// GetOpenAIImagesOAuthUnavailableCooldownSettings returns the effective
// image-unavailability cooldown duration in minutes. Configure the borrowed
// settings service before mounting this read endpoint. Read failures retain the
// common domain mapping, and success uses the dedicated single-duration DTO.
func (h *RuntimeSettingsHandler) GetOpenAIImagesOAuthUnavailableCooldownSettings(c *gin.Context) {
	settings, err := h.settingService.GetOpenAIImagesOAuthUnavailableCooldownSettings(c.Request.Context())
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	httpx.Success(c, providerdto.OpenAIImagesOAuthUnavailableCooldownSettings{CooldownMinutes: settings.CooldownMinutes})
}

// UpdateOpenAIImagesOAuthUnavailableCooldownSettings binds and saves the
// image-unavailability duration and returns its validated settings object.
// Binding and setter failures return 400; this endpoint sends the setter's
// object directly after success.
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

// GetStreamTimeoutSettings returns the effective stream-timeout action and
// threshold-window settings. Read failures retain the common domain mapping;
// success encodes enablement, action, avoidance duration, count and
// observation-window values.
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

// UpdateStreamTimeoutSettings binds the complete stream-timeout policy and
// saves it through runtime validation. Success rereads effective settings,
// while a readback failure returns an error after the policy write has
// completed.
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

	// Settings persistence has completed before readback. Query effective values
	// for the response, while a later read failure leaves that completed write in place.
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

// GetOpenAI403CooldownSettings returns the effective forbidden-response
// cooldown and repeated-error escalation policy. Read failures use the common
// mapping; the returned settings retain minute-based durations and threshold
// counts.
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

// UpdateOpenAI403CooldownSettings binds cooldown fields and defaults omitted
// threshold pointers before saving. Explicit pointer values override those
// defaults, and a failed readback reports an error after the validated write
// has completed.
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
